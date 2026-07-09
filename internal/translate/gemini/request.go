package gemini

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

var invalidGeminiIDChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

// ToOpenAI translates a Gemini-format request body to OpenAI format.
// Gemini reference: https://ai.google.dev/api/generate-content
//
// Example Gemini body:
//
//	{
//	  "contents": [{"parts":[{"text":"hi"}], "role":"user"}],
//	  "systemInstruction": {"parts":[{"text":"You are helpful"}]},
//	  "generationConfig": {"temperature":0.7, "maxOutputTokens":100}
//	}
func ToOpenAI(body []byte) ([]byte, error) {
	var gemini map[string]any
	if err := json.Unmarshal(body, &gemini); err != nil {
		return nil, fmt.Errorf("translate: invalid request body: %w", err)
	}

	openai := make(map[string]any)

	// Copy model field
	if m, ok := gemini["model"].(string); ok {
		openai["model"] = m
	}

	// Forward stream so Gemini-format streaming requests stay streaming
	// when proxied to the OpenAI upstream (Claude's ToOpenAI does the same).
	if s, ok := gemini["stream"].(bool); ok {
		openai["stream"] = s
	}

	// Convert systemInstruction → system message
	var messages []any
	if sys, ok := gemini["systemInstruction"].(map[string]any); ok {
		if parts, ok := sys["parts"].([]any); ok {
			sysText := joinGeminiParts(parts)
			if sysText != "" {
				messages = append(messages, map[string]any{
					"role":    "system",
					"content": sysText,
				})
			}
		}
	}

	// Convert contents → messages (a single Gemini content can produce
	// multiple OpenAI messages — e.g. a functionResponse part becomes
	// a {role:"tool"} message).
	if contents, ok := gemini["contents"].([]any); ok {
		for _, c := range contents {
			content, ok := c.(map[string]any)
			if !ok {
				continue
			}
			msgs := convertGeminiContent(content)
			messages = append(messages, msgs...)
		}
	}

	if len(messages) > 0 {
		openai["messages"] = messages
	}

	// Convert generationConfig
	if gc, ok := gemini["generationConfig"].(map[string]any); ok {
		if t, ok := gc["temperature"].(float64); ok {
			openai["temperature"] = t
		}
		if m, ok := gc["maxOutputTokens"].(float64); ok {
			openai["max_tokens"] = m
		}
		if tp, ok := gc["topP"].(float64); ok {
			openai["top_p"] = tp
		}
		if ss, ok := gc["stopSequences"].([]any); ok {
			openai["stop"] = ss
		}
	}

	// Convert tools (functionDeclarations)
	if tools, ok := gemini["tools"].([]any); ok {
		var openaiTools []any
		for _, t := range tools {
			tool, ok := t.(map[string]any)
			if !ok {
				continue
			}
			if fds, ok := tool["functionDeclarations"].([]any); ok {
				for _, fd := range fds {
					fdMap, ok := fd.(map[string]any)
					if !ok {
						continue
					}
					openaiTools = append(openaiTools, convertGeminiFunctionDecl(fdMap))
				}
			}
		}
		if len(openaiTools) > 0 {
			openai["tools"] = openaiTools
		}
	}

	result, err := json.Marshal(openai)
	if err != nil {
		return nil, fmt.Errorf("translate: marshal openai request: %w", err)
	}
	return result, nil
}

// convertGeminiContent converts a Gemini content object to one or more
// OpenAI messages. A single Gemini content can produce multiple OpenAI
// messages when it contains a functionResponse part: that becomes a
// {role:"tool"} message emitted *before* any text/image content from
// the same Gemini content.
func convertGeminiContent(input map[string]any) []any {
	role, _ := input["role"].(string)
	switch role {
	case "model":
		role = "assistant"
	case "":
		role = "user"
	}

	parts, _ := input["parts"].([]any)
	if len(parts) == 0 {
		return []any{map[string]any{"role": role, "content": ""}}
	}

	var textParts []string
	var imageBlocks []any
	var toolCalls []any
	var toolResponses []any

	for i, p := range parts {
		part, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if txt, ok := part["text"].(string); ok && txt != "" {
			textParts = append(textParts, txt)
		}
		if id, ok := part["inlineData"].(map[string]any); ok {
			mimeType, _ := id["mimeType"].(string)
			if mimeType == "" {
				mimeType = "image/png"
			}
			data, _ := id["data"].(string)
			imageBlocks = append(imageBlocks, map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": fmt.Sprintf("data:%s;base64,%s", mimeType, data),
				},
			})
		}
		if fc, ok := part["functionCall"].(map[string]any); ok {
			name, _ := fc["name"].(string)
			if name == "" {
				continue
			}
			args, _ := fc["args"].(map[string]any)
			if args == nil {
				args = map[string]any{}
			}
			argsBytes, _ := json.Marshal(args)
			toolCalls = append(toolCalls, map[string]any{
				"id":   geminiToolCallID(name, i),
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": string(argsBytes),
				},
			})
		}
		if fr, ok := part["functionResponse"].(map[string]any); ok {
			id, _ := fr["id"].(string)
			if id == "" {
				if name, _ := fr["name"].(string); name != "" {
					id = name
				}
			}
			if id == "" {
				continue
			}
			payload := extractFunctionResponseContent(fr["response"])
			payloadBytes, _ := json.Marshal(payload)
			toolResponses = append(toolResponses, map[string]any{
				"role":         "tool",
				"tool_call_id": id,
				"content":      string(payloadBytes),
			})
		}
	}

	out := make([]any, 0, 1+len(toolResponses))

	// Tool responses come first (separate messages).
	out = append(out, toolResponses...)

	if role == "assistant" && len(toolCalls) > 0 {
		msg := map[string]any{
			"role":       "assistant",
			"tool_calls": toolCalls,
		}
		switch {
		case len(textParts) == 0:
			msg["content"] = ""
		case len(textParts) == 1:
			msg["content"] = textParts[0]
		default:
			msg["content"] = textParts
		}
		out = append(out, msg)
		return out
	}

	// Plain user / assistant message (no tool_calls).
	var content any
	switch {
	case len(imageBlocks) == 0:
		switch {
		case len(textParts) == 0:
			content = ""
		case len(textParts) == 1:
			content = textParts[0]
		default:
			content = textParts
		}
	default:
		// Mix of text and images → array content.
		blocks := make([]any, 0, len(textParts)+len(imageBlocks))
		for _, t := range textParts {
			blocks = append(blocks, map[string]any{"type": "text", "text": t})
		}
		blocks = append(blocks, imageBlocks...)
		content = blocks
	}
	out = append(out, map[string]any{"role": role, "content": content})
	return out
}

// extractFunctionResponseContent returns the inner "result" payload of
// a Gemini functionResponse.response, falling back to the whole object
// when no result key is present.
func extractFunctionResponseContent(raw any) any {
	m, ok := raw.(map[string]any)
	if !ok {
		return raw
	}
	if r, ok := m["result"]; ok {
		return r
	}
	return m
}

// geminiToolCallID synthesizes a deterministic OpenAI tool_call.id from
// the Gemini function name and the part index. Keeps ids unique within
// a single content and Anthropic-pattern-clean.
func geminiToolCallID(name string, partIndex int) string {
	safe := invalidGeminiIDChars.ReplaceAllString(name, "_")
	if safe == "" {
		safe = "f"
	}
	return fmt.Sprintf("call_gemini_%s_%d", safe, partIndex)
}

// convertGeminiFunctionDecl converts a Gemini functionDeclaration to an OpenAI tool.
func convertGeminiFunctionDecl(fd map[string]any) map[string]any {
	name, _ := fd["name"].(string)
	desc, _ := fd["description"].(string)
	parameters := fd["parameters"]
	if parameters == nil {
		parameters = map[string]any{"type": "object", "properties": map[string]any{}}
	}
	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        name,
			"description": desc,
			"parameters":  parameters,
		},
	}
}

// joinGeminiParts concatenates text from Gemini parts.
func joinGeminiParts(parts []any) string {
	var texts []string
	for _, p := range parts {
		part, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if txt, ok := part["text"].(string); ok {
			texts = append(texts, txt)
		}
	}
	return strings.Join(texts, "\n")
}

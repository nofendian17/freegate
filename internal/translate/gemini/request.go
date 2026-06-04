package gemini

import (
	"encoding/json"
	"fmt"
	"strings"
)

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

	// Convert contents → messages
	if contents, ok := gemini["contents"].([]any); ok {
		for _, c := range contents {
			content, ok := c.(map[string]any)
			if !ok {
				continue
			}
			msg := convertGeminiContent(content)
			messages = append(messages, msg)
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

// convertGeminiContent converts a Gemini content object to an OpenAI message.
func convertGeminiContent(content map[string]any) map[string]any {
	role, _ := content["role"].(string)
	switch role {
	case "model":
		role = "assistant"
	case "":
		role = "user"
	}

	parts, _ := content["parts"].([]any)
	if len(parts) == 0 {
		return map[string]any{"role": role, "content": ""}
	}

	// If single text part, return as string
	if len(parts) == 1 {
		if p, ok := parts[0].(map[string]any); ok {
			if txt, ok := p["text"].(string); ok {
				return map[string]any{"role": role, "content": txt}
			}
		}
	}

	// Multiple parts → array content
	var contentBlocks []any
	for _, p := range parts {
		part, ok := p.(map[string]any)
		if !ok {
			continue
		}
		if txt, ok := part["text"].(string); ok && txt != "" {
			contentBlocks = append(contentBlocks, map[string]any{
				"type": "text",
				"text": txt,
			})
		}
		if id, ok := part["inlineData"].(map[string]any); ok {
			mimeType, _ := id["mimeType"].(string)
			data, _ := id["data"].(string)
			if mimeType == "" {
				mimeType = "image/png"
			}
			contentBlocks = append(contentBlocks, map[string]any{
				"type": "image_url",
				"image_url": map[string]any{
					"url": fmt.Sprintf("data:%s;base64,%s", mimeType, data),
				},
			})
		}
	}

	if len(contentBlocks) == 0 {
		return map[string]any{"role": role, "content": ""}
	}
	if len(contentBlocks) == 1 {
		if c, ok := contentBlocks[0].(map[string]any); ok && c["type"] == "text" {
			if txt, ok := c["text"].(string); ok {
				return map[string]any{"role": role, "content": txt}
			}
		}
	}
	return map[string]any{"role": role, "content": contentBlocks}
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

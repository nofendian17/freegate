package claude

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ToOpenAI translates a Claude-format request body to OpenAI format.
// Claude reference: https://docs.anthropic.com/en/api/messages
// OpenAI reference: https://platform.openai.com/docs/api-reference/chat
func ToOpenAI(body []byte) ([]byte, error) {
	var claude map[string]any
	if err := json.Unmarshal(body, &claude); err != nil {
		return nil, fmt.Errorf("translate: invalid request body: %w", err)
	}

	openai := make(map[string]any)

	// Copy scalar fields directly
	copyField(claude, openai, "model")
	copyField(claude, openai, "max_tokens")
	copyField(claude, openai, "temperature")
	copyField(claude, openai, "top_p")
	copyField(claude, openai, "stream")
	copyField(claude, openai, "metadata", "user_id", "user")
	copyField(claude, openai, "stop_sequences", "stop")

	// Convert top_k → drop (not supported by OpenAI)

	// Convert system prompt to messages[0]
	var messages []any
	if sys, ok := claude["system"]; ok {
		sysText := ""
		switch s := sys.(type) {
		case string:
			sysText = s
		case []any:
			parts := make([]string, 0, len(s))
			for _, p := range s {
				if block, ok := p.(map[string]any); ok {
					if t, _ := block["type"].(string); t == "text" {
						if txt, _ := block["text"].(string); txt != "" {
							parts = append(parts, txt)
						}
					}
				}
			}
			sysText = strings.Join(parts, "\n")
		}
		if sysText != "" {
			messages = append(messages, map[string]any{
				"role":    "system",
				"content": sysText,
			})
		}
	}

	// Convert Claude messages
	if claudeMsgs, ok := claude["messages"].([]any); ok {
		converted := convertClaudeMessages(claudeMsgs)
		messages = append(messages, converted...)
	}

	if len(messages) > 0 {
		openai["messages"] = messages
	}

	// Convert tools
	if claudeTools, ok := claude["tools"].([]any); ok && len(claudeTools) > 0 {
		openaiTools := make([]any, 0, len(claudeTools))
		for _, t := range claudeTools {
			if tool, ok := t.(map[string]any); ok {
				if ot := convertClaudeTool(tool); ot != nil {
					openaiTools = append(openaiTools, ot)
				}
			}
		}
		if len(openaiTools) > 0 {
			openai["tools"] = openaiTools
		}
	}

	// Convert tool_choice
	if tc, ok := claude["tool_choice"]; ok {
		if tcMap, ok := tc.(map[string]any); ok {
			openai["tool_choice"] = convertClaudeToolChoice(tcMap)
		}
	}

	result, err := json.Marshal(openai)
	if err != nil {
		return nil, fmt.Errorf("translate: marshal openai request: %w", err)
	}
	return result, nil
}

// convertClaudeMessages converts an array of Claude messages to OpenAI format.
func convertClaudeMessages(claudeMsgs []any) []any {
	var result []any

	for i, m := range claudeMsgs {
		msg, ok := m.(map[string]any)
		if !ok {
			result = append(result, m)
			continue
		}

		role, _ := msg["role"].(string)
		content, hasContent := msg["content"]

		switch role {
		case "user":
			// A Claude user message can produce 0+ OpenAI messages
			// (text-only → 1 user msg; text+tool_results → user + tool msgs;
			// tool_results-only → tool msgs only). Flatten into the result.
			result = append(result, convertClaudeUserMessage(msg)...)
		case "assistant":
			result = append(result, convertClaudeAssistantMessage(msg))
		case "tool":
			// Claude "tool" role → OpenAI "tool" role with tool_call_id
			result = append(result, msg)
		default:
			// Pass through unknown roles
			result = append(result, msg)
		}

		_ = i
		_ = hasContent
		_ = content
	}

	return result
}

// convertClaudeUserMessage converts a Claude user message to zero or more
// OpenAI messages. Claude user messages may contain tool_result content
// blocks, which need to be extracted as separate {role:"tool"} messages;
// the remaining text (if any) becomes a user message.
//
// Returns a slice so the caller can flatten: a text-only block becomes
// one user message; tool_results alone become tool messages; text plus
// tool_results become a user message followed by tool messages.
func convertClaudeUserMessage(msg map[string]any) []any {
	content, ok := msg["content"]
	if !ok {
		return []any{msg}
	}

	// String content → passthrough
	if _, ok := content.(string); ok {
		return []any{msg}
	}

	blocks, ok := content.([]any)
	if !ok {
		return []any{msg}
	}

	// Check for tool_result blocks
	var textParts []string
	var toolMessages []any
	toolFound := false

	for _, b := range blocks {
		block, ok := b.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := block["type"].(string)

		switch typ {
		case "text":
			if txt, _ := block["text"].(string); txt != "" {
				textParts = append(textParts, txt)
			}
		case "tool_result":
			toolFound = true
			toolMsg := convertToolResult(block)
			if toolMsg != nil {
				toolMessages = append(toolMessages, toolMsg)
			}
		case "image":
			// Will be handled by content block conversion
		}
	}

	if !toolFound {
		// No tool_result, convert content blocks normally
		openaiBlocks := convertClaudeContentBlocks(blocks, false)
		newMsg := cloneMap(msg)
		newMsg["content"] = openaiBlocks
		return []any{newMsg}
	}

	// Build the result list: tool messages first, then the user text
	// message (if any). This is the OpenAI-idiomatic order: tool
	// responses sit right after the assistant tool_calls. Putting the
	// user text first breaks FixMissingToolResponses (and upstreams
	// like MiniMax) which expect to see the tool response directly
	// after the assistant, leading to a duplicate tool_call_id when
	// the prepost step synthesizes a missing response.
	var results []any
	results = append(results, toolMessages...)
	if len(textParts) > 0 {
		results = append(results, map[string]any{
			"role":    "user",
			"content": strings.Join(textParts, "\n"),
		})
	}
	return results
}

// convertClaudeAssistantMessage converts a Claude assistant message to OpenAI format.
// Claude assistant messages may contain tool_use content blocks which become tool_calls.
func convertClaudeAssistantMessage(msg map[string]any) any {
	content, ok := msg["content"]
	if !ok {
		return msg
	}

	// String content → passthrough
	if _, ok := content.(string); ok {
		return msg
	}

	blocks, ok := content.([]any)
	if !ok {
		return msg
	}

	// Check for tool_use blocks
	var textParts []string
	var toolCalls []any
	toolUseFound := false

	for _, b := range blocks {
		block, ok := b.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := block["type"].(string)

		switch typ {
		case "text":
			if txt, _ := block["text"].(string); txt != "" {
				textParts = append(textParts, txt)
			}
		case "tool_use":
			toolUseFound = true
			tc := map[string]any{
				"id":   block["id"],
				"type": "function",
				"function": map[string]any{
					"name":      block["name"],
					"arguments": mustJSON(block["input"]),
				},
			}
			toolCalls = append(toolCalls, tc)
		case "thinking":
			// Claude thinking blocks: append text
			if txt, _ := block["thinking"].(string); txt != "" {
				textParts = append(textParts, txt)
			}
		case "redacted_thinking":
			if txt, _ := block["text"].(string); txt != "" {
				textParts = append(textParts, txt)
			}
		default:
			// Image blocks in assistant response? handle generically
		}
	}

	newMsg := cloneMap(msg)
	contentText := strings.Join(textParts, "")

	if toolUseFound {
		newMsg["content"] = contentText
		newMsg["tool_calls"] = toolCalls
	} else {
		// No tool_use, convert content blocks
		openaiBlocks := convertClaudeContentBlocks(blocks, false)
		newMsg["content"] = openaiBlocks
	}

	delete(newMsg, "role")
	newMsg["role"] = "assistant"
	return newMsg
}

// convertToolResult converts a Claude tool_result content block to an OpenAI tool message.
func convertToolResult(block map[string]any) any {
	toolUseID, _ := block["tool_use_id"].(string)
	rawContent := block["content"]

	contentStr := contentToString(rawContent)

	return map[string]any{
		"role":         "tool",
		"tool_call_id": toolUseID,
		"content":      contentStr,
	}
}

// convertClaudeContentBlocks converts Claude content blocks (text, image) to OpenAI format.
func convertClaudeContentBlocks(blocks []any, _ bool) any {
	var result []any
	for _, b := range blocks {
		block, ok := b.(map[string]any)
		if !ok {
			result = append(result, b)
			continue
		}
		typ, _ := block["type"].(string)

		switch typ {
		case "text":
			result = append(result, map[string]any{
				"type": "text",
				"text": block["text"],
			})
		case "image":
			if src, ok := block["source"].(map[string]any); ok {
				mediaType, _ := src["media_type"].(string)
				data, _ := src["data"].(string)
				if mediaType == "" {
					mediaType = "image/png"
				}
				result = append(result, map[string]any{
					"type": "image_url",
					"image_url": map[string]any{
						"url": fmt.Sprintf("data:%s;base64,%s", mediaType, data),
					},
				})
			}
		}
	}
	if len(result) == 0 {
		return ""
	}
	if len(result) == 1 {
		// If single text block, return as string for simplicity
		if r, ok := result[0].(map[string]any); ok && r["type"] == "text" {
			if txt, ok := r["text"].(string); ok {
				return txt
			}
		}
	}
	return result
}

// convertClaudeTool converts a Claude tool definition to OpenAI format.
func convertClaudeTool(tool map[string]any) any {
	name, _ := tool["name"].(string)
	if name == "" {
		return nil
	}
	desc, _ := tool["description"].(string)
	inputSchema := tool["input_schema"]
	if inputSchema == nil {
		inputSchema = map[string]any{"type": "object", "properties": map[string]any{}}
	}

	return map[string]any{
		"type": "function",
		"function": map[string]any{
			"name":        name,
			"description": desc,
			"parameters":  inputSchema,
		},
	}
}

// convertClaudeToolChoice converts Claude tool_choice to OpenAI format.
func convertClaudeToolChoice(tc map[string]any) any {
	typ, _ := tc["type"].(string)
	switch typ {
	case "any":
		return "required"
	case "auto":
		return "auto"
	case "tool":
		if name, ok := tc["name"].(string); ok {
			return map[string]any{
				"type": "function",
				"function": map[string]any{
					"name": name,
				},
			}
		}
		return "auto"
	default:
		return "auto"
	}
}

// --- helpers ---

// copyField copies a field from src to dst, optionally with renaming.
// srcPath is the source key; dstKey is the destination key (defaults to srcPath if empty).
func copyField(src, dst map[string]any, srcPath string, dstKey ...string) {
	key := srcPath
	if len(dstKey) > 0 && dstKey[0] != "" {
		key = dstKey[0]
	}
	if v, ok := src[srcPath]; ok {
		dst[key] = v
	}
}

// cloneMap shallow-clones a map.
func cloneMap(m map[string]any) map[string]any {
	result := make(map[string]any, len(m))
	for k, v := range m {
		result[k] = v
	}
	return result
}

// mustJSON marshals v to JSON string, returning "{}" on error.
func mustJSON(v any) string {
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// contentToString converts raw content (string, array, or map) to a string.
func contentToString(raw any) string {
	switch v := raw.(type) {
	case string:
		return v
	case []any:
		var parts []string
		for _, item := range v {
			if itemMap, ok := item.(map[string]any); ok {
				if txt, ok := itemMap["text"].(string); ok {
					parts = append(parts, txt)
				}
			} else if itemStr, ok := item.(string); ok {
				parts = append(parts, itemStr)
			}
		}
		return strings.Join(parts, "\n")
	case map[string]any:
		if txt, ok := v["text"].(string); ok {
			return txt
		}
		return mustJSON(v)
	default:
		return fmt.Sprintf("%v", v)
	}
}

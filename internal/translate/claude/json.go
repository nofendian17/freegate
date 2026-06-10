package claude

import (
	"encoding/json"
)

// --- Non-streaming JSON response translation ---

// JSONToClaude converts an OpenAI-format JSON response to Claude format.
func JSONToClaude(body []byte) ([]byte, error) {
	var openaiResp map[string]any
	if err := json.Unmarshal(body, &openaiResp); err != nil {
		return nil, err
	}

	claude := map[string]any{
		"id":            "msg_" + randID(8),
		"type":          "message",
		"role":          "assistant",
		"content":       []any{},
		"model":         openaiResp["model"],
		"stop_reason":   nil,
		"stop_sequence": nil,
	}

	// Extract usage
	if usage, ok := openaiResp["usage"].(map[string]any); ok {
		claude["usage"] = extractUsage(usage)
	} else {
		claude["usage"] = map[string]any{
			"input_tokens":  0,
			"output_tokens": 0,
		}
	}

	// Convert choices to content blocks
	if choices, ok := openaiResp["choices"].([]any); ok && len(choices) > 0 {
		choice, _ := choices[0].(map[string]any)
		if choice != nil {
			stopReason := ""
			if fr, ok := choice["finish_reason"].(string); ok {
				stopReason = mapFinishReason(fr)
			}
			claude["stop_reason"] = stopReason

			msg, _ := choice["message"].(map[string]any)
			if msg != nil {
				content := convertOpenAIMessage(msg)
				claude["content"] = content
			}
		}
	}

	return json.Marshal(claude)
}

func convertOpenAIMessage(msg map[string]any) []any {
	var content []any

	// Add thinking block first (reasoning_content preferred over reasoning)
	if rc, ok := msg["reasoning_content"].(string); ok && rc != "" {
		content = append(content, map[string]any{
			"type":     "thinking",
			"thinking": rc,
		})
	} else if r, ok := msg["reasoning"].(string); ok && r != "" {
		content = append(content, map[string]any{
			"type":     "thinking",
			"thinking": r,
		})
	}

	// Add text content
	switch c := msg["content"].(type) {
	case string:
		if c != "" {
			content = append(content, map[string]any{
				"type": "text",
				"text": c,
			})
		}
	case []any:
		for _, part := range c {
			content = append(content, part)
		}
	}

	// Add tool calls as tool_use blocks
	if tcList, ok := msg["tool_calls"].([]any); ok {
		for _, tcAny := range tcList {
			tc, _ := tcAny.(map[string]any)
			if tc == nil {
				continue
			}
			fn, _ := tc["function"].(map[string]any)
			if fn == nil {
				continue
			}
			name, _ := fn["name"].(string)
			argsStr, _ := fn["arguments"].(string)
			var input any
			json.Unmarshal([]byte(argsStr), &input)
			if input == nil {
				input = map[string]any{}
			}

			id, _ := tc["id"].(string)
			if id == "" {
				id = "toolu_" + randID(8)
			}

			content = append(content, map[string]any{
				"type":  "tool_use",
				"id":    id,
				"name":  name,
				"input": input,
			})
		}
	}

	if len(content) == 0 {
		content = append(content, map[string]any{
			"type": "text",
			"text": "",
		})
	}

	return content
}

// --- Shared helpers (used by both stream and json) ---

func extractUsage(u map[string]any) *usageInfo {
	ui := &usageInfo{}
	if pt, ok := u["prompt_tokens"].(float64); ok {
		ui.InputTokens = int64(pt)
	}
	if ct, ok := u["completion_tokens"].(float64); ok {
		ui.OutputTokens = int64(ct)
	}
	// Check for cache tokens in prompt_tokens_details
	if details, ok := u["prompt_tokens_details"].(map[string]any); ok {
		if cached, ok := details["cached_tokens"].(float64); ok {
			ui.CacheReadTokens = int64(cached)
			// Subtract from input_tokens to match Claude's accounting
			ui.InputTokens -= ui.CacheReadTokens
		}
		if cc, ok := details["cache_creation_tokens"].(float64); ok {
			ui.CacheCreateTokens = int64(cc)
		}
	}
	return ui
}

func mapFinishReason(reason string) string {
	switch reason {
	case "stop":
		return "end_turn"
	case "length":
		return "max_tokens"
	case "tool_calls":
		return "tool_use"
	case "content_filter":
		return "end_turn"
	default:
		return "end_turn"
	}
}

package claude

import (
	"encoding/json"
	"fmt"
	"strings"
)

// JSONToOpenAI converts a non-streaming Claude-format response body to
// OpenAI format. Mirrors 9router's response/claude-to-openai.js (non-
// streaming path).
//
// Claude shape:
//
//	{
//	  "id": "msg_...",
//	  "type": "message",
//	  "role": "assistant",
//	  "content": [{"type":"text","text":"..."},
//	              {"type":"tool_use","id","name","input":{...}}],
//	  "stop_reason": "end_turn" | "max_tokens" | "tool_use" | "stop_sequence",
//	  "usage": {"input_tokens":N, "output_tokens":N,
//	            "cache_read_input_tokens"?:N, "cache_creation_input_tokens"?:N}
//	}
//
// OpenAI shape:
//
//	{
//	  "id":"chatcmpl-...",
//	  "object":"chat.completion",
//	  "model":"...",
//	  "choices":[{"index":0,"message":{"role":"assistant","content":"...","tool_calls":[...]},
//	              "finish_reason":"stop"|"length"|"tool_calls"}],
//	  "usage":{"prompt_tokens":N,"completion_tokens":N,"total_tokens":N,
//	           "prompt_tokens_details":{...}?,"completion_tokens_details":{...}?}
//	}
func JSONToOpenAI(body []byte) ([]byte, error) {
	var claude map[string]any
	if err := json.Unmarshal(body, &claude); err != nil {
		return nil, fmt.Errorf("claude: invalid JSONToOpenAI body: %w", err)
	}

	model, _ := claude["model"].(string)
	id, _ := claude["id"].(string)
	if id == "" {
		id = "chatcmpl-" + randID(12)
	} else if !strings.HasPrefix(id, "chatcmpl-") {
		id = "chatcmpl-" + id
	}

	stopReason, _ := claude["stop_reason"].(string)
	finishReason := convertStopReasonOpenAI(stopReason)

	content, _ := claude["content"].([]any)
	text := extractTextFromBlocks(content)
	toolCalls := extractToolCallsFromBlocks(content)

	openaiMsg := map[string]any{"role": "assistant"}
	if toolCalls != nil {
		openaiMsg["content"] = text // may be "" — OpenAI accepts empty string
		openaiMsg["tool_calls"] = toolCalls
	} else {
		openaiMsg["content"] = text
	}

	result := map[string]any{
		"id":     id,
		"object": "chat.completion",
		"model":  model,
		"choices": []any{
			map[string]any{
				"index":         0,
				"message":       openaiMsg,
				"finish_reason": finishReason,
			},
		},
	}

	if u, ok := claude["usage"].(map[string]any); ok {
		result["usage"] = convertUsageOpenAI(u)
	}

	out, err := json.Marshal(result)
	if err != nil {
		return nil, fmt.Errorf("claude: marshal JSONToOpenAI: %w", err)
	}
	return out, nil
}

func extractTextFromBlocks(blocks []any) string {
	if len(blocks) == 0 {
		return ""
	}
	var sb strings.Builder
	for _, bAny := range blocks {
		b, _ := bAny.(map[string]any)
		if b == nil {
			continue
		}
		typ, _ := b["type"].(string)
		if typ == "text" {
			if txt, _ := b["text"].(string); txt != "" {
				sb.WriteString(txt)
			}
		}
	}
	return sb.String()
}

func extractToolCallsFromBlocks(blocks []any) []any {
	var out []any
	for _, bAny := range blocks {
		b, _ := bAny.(map[string]any)
		if b == nil {
			continue
		}
		typ, _ := b["type"].(string)
		if typ != "tool_use" {
			continue
		}
		id, _ := b["id"].(string)
		if id == "" {
			id = "toolu_" + randID(8)
		}
		name, _ := b["name"].(string)
		input, _ := b["input"].(map[string]any)
		if input == nil {
			input = map[string]any{}
		}
		argsBytes, _ := json.Marshal(input)
		out = append(out, map[string]any{
			"id":    id,
			"type":  "function",
			"index": len(out),
			"function": map[string]any{
				"name":      name,
				"arguments": string(argsBytes),
			},
		})
	}
	return out
}

func convertUsageOpenAI(u map[string]any) map[string]any {
	out := map[string]any{}
	var inputTokens, outputTokens int64
	if v, ok := u["input_tokens"].(float64); ok {
		inputTokens = int64(v)
	}
	if v, ok := u["output_tokens"].(float64); ok {
		outputTokens = int64(v)
	}
	var cacheRead, cacheCreate int64
	if v, ok := u["cache_read_input_tokens"].(float64); ok {
		cacheRead = int64(v)
	}
	if v, ok := u["cache_creation_input_tokens"].(float64); ok {
		cacheCreate = int64(v)
	}
	// prompt_tokens = input + cache_read + cache_creation (Anthropic's
	// input_tokens is the uncached portion; OpenAI's prompt_tokens is
	// the total of all prompt-side tokens).
	promptTokens := inputTokens + cacheRead + cacheCreate
	out["prompt_tokens"] = promptTokens
	out["completion_tokens"] = outputTokens
	out["total_tokens"] = promptTokens + outputTokens
	if cacheRead > 0 || cacheCreate > 0 {
		details := map[string]any{}
		if cacheRead > 0 {
			details["cached_tokens"] = cacheRead
		}
		if cacheCreate > 0 {
			details["cache_creation_tokens"] = cacheCreate
		}
		out["prompt_tokens_details"] = details
	}
	return out
}

// convertStopReasonOpenAI maps a Claude stop_reason to an OpenAI
// finish_reason.
func convertStopReasonOpenAI(stopReason string) string {
	switch stopReason {
	case "end_turn", "stop_sequence":
		return "stop"
	case "max_tokens":
		return "length"
	case "tool_use":
		return "tool_calls"
	default:
		return "stop"
	}
}

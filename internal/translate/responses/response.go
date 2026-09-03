package responses

import (
	"encoding/json"
	"fmt"
)

// JSONToOpenAI translates a Responses API JSON response to OpenAI Chat Completion.
// Used when upstream is responses (muse) but client expects chat.
func JSONToOpenAI(body []byte) ([]byte, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("responses: invalid json: %w", err)
	}
	// If already chat.completion (has choices), pass through
	if _, ok := raw["choices"]; ok {
		return body, nil
	}
	// Expect responses shape: {object:"response", output:[...], usage:{input_tokens, output_tokens}, id, model, created_at}
	if raw["object"] != "response" && raw["output"] == nil {
		return body, nil
	}

	// Build chat.completion
	out := make(map[string]any)
	if id, ok := raw["id"]; ok {
		if s, ok := id.(string); ok {
			// resp_xxx -> chatcmpl-xxx
			if len(s) > 5 && s[:5] == "resp_" {
				out["id"] = "chatcmpl-" + s[5:]
			} else {
				out["id"] = s
			}
		}
	}
	if out["id"] == nil {
		out["id"] = raw["id"]
	}
	out["object"] = "chat.completion"
	if ca, ok := raw["created_at"]; ok {
		out["created"] = ca
	} else if c, ok := raw["created"]; ok {
		out["created"] = c
	}
	if m, ok := raw["model"]; ok {
		out["model"] = m
	}

	// Collect output
	output, _ := raw["output"].([]any)
	var contentParts []string
	var toolCalls []any
	var reasoningContent string

	for _, itemRaw := range output {
		item, ok := itemRaw.(map[string]any)
		if !ok {
			continue
		}
		typ, _ := item["type"].(string)
		switch typ {
		case "message":
			if cnt, ok := item["content"].([]any); ok {
				for _, cRaw := range cnt {
					if c, ok := cRaw.(map[string]any); ok {
						if c["type"] == "output_text" {
							if t, _ := c["text"].(string); t != "" {
								contentParts = append(contentParts, t)
							}
						}
					}
				}
			}
		case "function_call":
			callID, _ := item["call_id"].(string)
			name, _ := item["name"].(string)
			args, _ := item["arguments"].(string)
			if args == "" {
				args = "{}"
			}
			toolCalls = append(toolCalls, map[string]any{
				"id":   callID,
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": args,
				},
			})
		case "custom_tool_call":
			callID, _ := item["call_id"].(string)
			name, _ := item["name"].(string)
			inp, _ := item["input"].(string)
			// Wrap as {"input": inp}
			argsBytes, _ := json.Marshal(map[string]any{"input": inp})
			toolCalls = append(toolCalls, map[string]any{
				"id":   callID,
				"type": "function",
				"function": map[string]any{
					"name":      name,
					"arguments": string(argsBytes),
				},
			})
		case "reasoning":
			if summary, ok := item["summary"].([]any); ok {
				for _, sRaw := range summary {
					if s, ok := sRaw.(map[string]any); ok {
						if t, _ := s["text"].(string); t != "" {
							if reasoningContent != "" {
								reasoningContent += "\n"
							}
							reasoningContent += t
						}
					}
				}
			}
		}
	}

	// Build message
	msg := map[string]any{"role": "assistant"}
	if len(contentParts) > 0 {
		msg["content"] = joinStrings(contentParts)
	} else if len(toolCalls) == 0 {
		msg["content"] = ""
	} else {
		msg["content"] = nil
	}
	if len(toolCalls) > 0 {
		msg["tool_calls"] = toolCalls
	}
	if reasoningContent != "" {
		msg["reasoning_content"] = reasoningContent
		msg["reasoning"] = reasoningContent
	}

	finishReason := "stop"
	if len(toolCalls) > 0 {
		finishReason = "tool_calls"
	}
	out["choices"] = []any{
		map[string]any{
			"index":         0,
			"message":       msg,
			"finish_reason": finishReason,
		},
	}

	// Usage mapping
	if u, ok := raw["usage"].(map[string]any); ok {
		inputTokens := intVal(u["input_tokens"])
		if inputTokens == 0 {
			inputTokens = intVal(u["prompt_tokens"])
		}
		outputTokens := intVal(u["output_tokens"])
		if outputTokens == 0 {
			outputTokens = intVal(u["completion_tokens"])
		}
		out["usage"] = map[string]any{
			"prompt_tokens":     inputTokens,
			"completion_tokens": outputTokens,
			"total_tokens":      inputTokens + outputTokens,
		}
	}

	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("responses: marshal openai: %w", err)
	}
	return b, nil
}

// JSONToResponses translates a Chat Completion JSON to Responses API JSON.
// Used when upstream is chat but client expects responses (opposite direction).
func JSONToResponses(body []byte) ([]byte, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("responses: invalid json: %w", err)
	}
	if raw["object"] == "response" {
		return body, nil
	}
	if _, ok := raw["choices"]; !ok {
		return body, nil
	}

	choices, _ := raw["choices"].([]any)
	if len(choices) == 0 {
		return body, nil
	}
	ch, _ := choices[0].(map[string]any)
	msg, _ := ch["message"].(map[string]any)
	if msg == nil {
		msg, _ = ch["delta"].(map[string]any)
	}

	out := make(map[string]any)
	// id resp_xxx
	if id, ok := raw["id"].(string); ok {
		if len(id) > 9 && id[:9] == "chatcmpl-" {
			out["id"] = "resp_" + id[9:]
		} else {
			out["id"] = "resp_" + id
		}
	} else {
		out["id"] = "resp_0"
	}
	out["object"] = "response"
	if c, ok := raw["created"]; ok {
		out["created_at"] = c
	}
	if m, ok := raw["model"]; ok {
		out["model"] = m
	}
	out["status"] = "completed"
	out["output"] = []any{}

	var output []any

	// reasoning -> reasoning item
	if rc, ok := msg["reasoning_content"].(string); ok && rc != "" {
		output = append(output, map[string]any{
			"type":    "reasoning",
			"id":      "rs_0",
			"summary": []any{map[string]any{"type": "summary_text", "text": rc}},
		})
	}

	// content -> message
	if cnt, ok := msg["content"]; ok && cnt != nil {
		var text string
		switch v := cnt.(type) {
		case string:
			text = v
		default:
			if b, err := json.Marshal(v); err == nil {
				text = string(b)
			}
		}
		if text != "" {
			output = append(output, map[string]any{
				"type":    "message",
				"id":      "msg_0",
				"role":    "assistant",
				"content": []any{map[string]any{"type": "output_text", "text": text}},
			})
		}
	}

	// tool_calls -> function_call
	if tcs, ok := msg["tool_calls"].([]any); ok {
		for _, tcRaw := range tcs {
			tc, ok := tcRaw.(map[string]any)
			if !ok {
				continue
			}
			id, _ := tc["id"].(string)
			fn, _ := tc["function"].(map[string]any)
			name, _ := fn["name"].(string)
			args, _ := fn["arguments"].(string)
			output = append(output, map[string]any{
				"type":      "function_call",
				"id":        "fc_" + id,
				"call_id":   id,
				"name":      name,
				"arguments": args,
			})
		}
	}

	out["output"] = output
	if u, ok := raw["usage"].(map[string]any); ok {
		prompt := intVal(u["prompt_tokens"])
		comp := intVal(u["completion_tokens"])
		out["usage"] = map[string]any{
			"input_tokens":  prompt,
			"output_tokens": comp,
		}
	}

	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("responses: marshal responses: %w", err)
	}
	return b, nil
}

func intVal(v any) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case int:
		return x
	case int64:
		return int(x)
	default:
		return 0
	}
}

func joinStrings(parts []string) string {
	if len(parts) == 0 {
		return ""
	}
	if len(parts) == 1 {
		return parts[0]
	}
	s := ""
	for i, p := range parts {
		if i > 0 {
			s += "\n"
		}
		s += p
	}
	return s
}

package responses

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ToOpenAI translates an OpenAI Responses API request to Chat Completions.
// Responses uses {input:[...], instructions:""}; Chat uses {messages:[...]}.
func ToOpenAI(body []byte) ([]byte, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("responses: invalid body: %w", err)
	}
	if _, hasInput := raw["input"]; !hasInput {
		return body, nil
	}
	inpRaw := raw["input"]
	// Normalize input to []any
	inputItems := normalizeInput(inpRaw)
	if inputItems == nil {
		return body, nil
	}

	out := make(map[string]any)
	// copy passthrough fields
	for k, v := range raw {
		switch k {
		case "input", "instructions", "prompt_cache_key", "store", "include", "reasoning", "client_metadata":
			// stripped or handled separately
		default:
			out[k] = v
		}
	}

	// max_output_tokens -> max_tokens
	if v, ok := raw["max_output_tokens"]; ok {
		if _, has := out["max_tokens"]; !has {
			out["max_tokens"] = v
		}
	}
	// reasoning.effort -> reasoning_effort
	if r, ok := raw["reasoning"].(map[string]any); ok {
		if eff, ok := r["effort"].(string); ok && eff != "" {
			out["reasoning_effort"] = eff
		}
	}

	var messages []any
	if instr, ok := raw["instructions"].(string); ok && instr != "" {
		messages = append(messages, map[string]any{"role": "system", "content": instr})
	}

	var currentAssistant map[string]any
	var currentToolCalls []any
	var pendingReasoning string
	var pendingEncrypted string

	extractReasoning := func(item map[string]any) string {
		if summary, ok := item["summary"].([]any); ok {
			var parts []string
			for _, s := range summary {
				if m, ok := s.(map[string]any); ok {
					if t, _ := m["text"].(string); t != "" {
						parts = append(parts, t)
					}
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, "\n")
			}
		}
		if cnt, ok := item["content"].([]any); ok {
			var parts []string
			for _, c := range cnt {
				if m, ok := c.(map[string]any); ok {
					if t, _ := m["text"].(string); t != "" {
						parts = append(parts, t)
					}
				}
			}
			if len(parts) > 0 {
				return strings.Join(parts, "\n")
			}
		}
		return ""
	}

	flushAssistant := func() {
		if currentAssistant != nil {
			// attach pending reasoning if any
			if pendingReasoning != "" {
				currentAssistant["reasoning_content"] = pendingReasoning
			}
			if pendingEncrypted != "" {
				currentAssistant["encrypted_content"] = pendingEncrypted
			}
			pendingReasoning = ""
			pendingEncrypted = ""
			messages = append(messages, currentAssistant)
			currentAssistant = nil
		}
	}

	for _, itRaw := range inputItems {
		item, ok := itRaw.(map[string]any)
		if !ok {
			continue
		}
		itemType, _ := item["type"].(string)
		if itemType == "" {
			// Droid CLI fallback: role without type => message
			if _, hasRole := item["role"]; hasRole {
				itemType = "message"
			}
		}

		switch itemType {
		case "message":
			flushAssistant()
			role, _ := item["role"].(string)
			contentRaw := item["content"]
			var content any
			if arr, ok := contentRaw.([]any); ok {
				var conv []any
				for _, cRaw := range arr {
					c, ok := cRaw.(map[string]any)
					if !ok {
						continue
					}
					ct, _ := c["type"].(string)
					switch ct {
					case "input_text", "output_text":
						conv = append(conv, map[string]any{"type": "text", "text": c["text"]})
					case "input_image":
						url := ""
						if u, ok := c["image_url"].(string); ok {
							url = u
						} else if fid, ok := c["file_id"].(string); ok {
							url = fid
						}
						conv = append(conv, map[string]any{"type": "image_url", "image_url": map[string]any{"url": url, "detail": strOr(c["detail"], "auto")}})
					default:
						conv = append(conv, c)
					}
				}
				if len(conv) == 1 {
					if m, ok := conv[0].(map[string]any); ok && m["type"] == "text" {
						if txt, ok := m["text"].(string); ok {
							content = txt
						} else {
							content = conv
						}
					} else {
						content = conv
					}
				} else {
					content = conv
				}
			} else {
				content = contentRaw
			}
			msg := map[string]any{"role": role, "content": content}
			// attach reasoning to assistant
			if role == "assistant" && pendingReasoning != "" {
				msg["reasoning_content"] = pendingReasoning
				if pendingEncrypted != "" {
					msg["encrypted_content"] = pendingEncrypted
				}
				pendingReasoning = ""
				pendingEncrypted = ""
			} else if role != "assistant" {
				pendingReasoning = ""
				pendingEncrypted = ""
			}
			messages = append(messages, msg)

		case "function_call", "custom_tool_call":
			if currentAssistant == nil {
				currentAssistant = map[string]any{"role": "assistant", "content": nil, "tool_calls": []any{}}
				if pendingReasoning != "" {
					currentAssistant["reasoning_content"] = pendingReasoning
				}
				if pendingEncrypted != "" {
					currentAssistant["encrypted_content"] = pendingEncrypted
				}
				pendingReasoning = ""
				pendingEncrypted = ""
			}
			name, _ := item["name"].(string)
			if strings.TrimSpace(name) == "" {
				continue
			}
			var argsStr string
			if itemType == "custom_tool_call" {
				inp := item["input"]
				switch v := inp.(type) {
				case string:
					argsStr = fmt.Sprintf(`{"input":%q}`, v)
					// store mapping for custom tools? not needed for request
				default:
					if b, err := json.Marshal(map[string]any{"input": fmt.Sprintf("%v", v)}); err == nil {
						argsStr = string(b)
					} else {
						argsStr = `{"input":""}`
					}
				}
				// For custom, marshal as {"input":"..."}
				if s, ok := item["input"].(string); ok {
					if b, err := json.Marshal(map[string]any{"input": s}); err == nil {
						argsStr = string(b)
					}
				}
			} else {
				switch v := item["arguments"].(type) {
				case string:
					argsStr = v
				default:
					if b, err := json.Marshal(v); err == nil {
						argsStr = string(b)
					} else {
						argsStr = "{}"
					}
				}
			}
			callID, _ := item["call_id"].(string)
			tc := map[string]any{"id": clampCallID(callID), "type": "function", "function": map[string]any{"name": name, "arguments": argsStr}}
			list := currentToolCalls
			if currentAssistant["tool_calls"] != nil {
				list, _ = currentAssistant["tool_calls"].([]any)
			}
			list = append(list, tc)
			currentAssistant["tool_calls"] = list

		case "function_call_output", "custom_tool_call_output":
			flushAssistant()
			callID, _ := item["call_id"].(string)
			var outStr string
			switch v := item["output"].(type) {
			case string:
				outStr = v
			default:
				if b, err := json.Marshal(v); err == nil {
					outStr = string(b)
				}
			}
			messages = append(messages, map[string]any{"role": "tool", "tool_call_id": clampCallID(callID), "content": outStr})

		case "reasoning":
			if txt := extractReasoning(item); txt != "" {
				if pendingReasoning != "" {
					pendingReasoning += "\n" + txt
				} else {
					pendingReasoning = txt
				}
			}
			if enc, ok := item["encrypted_content"].(string); ok && enc != "" {
				pendingEncrypted = enc
			}
		}
	}
	flushAssistant()

	// Tools conversion
	var tools []any
	if t, ok := raw["tools"].([]any); ok {
		tools = append(tools, t...)
	}
	// additional_tools items not needed for this path (9router handles via ADDITIONAL_TOOLS item)
	if len(tools) > 0 {
		var convTools []any
		for _, tRaw := range tools {
			t, ok := tRaw.(map[string]any)
			if !ok {
				continue
			}
			if fn, ok := t["function"]; ok {
				convTools = append(convTools, t)
				_ = fn
				continue
			}
			name, _ := t["name"].(string)
			if strings.TrimSpace(name) == "" {
				continue
			}
			typ, _ := t["type"].(string)
			if typ == "custom" {
				formatHint := ""
				if fm, ok := t["format"].(map[string]any); ok {
					if s, ok := fm["syntax"].(string); ok {
						formatHint += s
					}
					if d, ok := fm["definition"].(string); ok {
						if formatHint != "" {
							formatHint += "\n"
						}
						formatHint += d
					}
				}
				desc := ""
				if d, ok := t["description"].(string); ok {
					desc = d
				}
				if formatHint != "" {
					if desc != "" {
						desc += "\n\n" + formatHint
					} else {
						desc = formatHint
					}
				}
				convTools = append(convTools, map[string]any{
					"type": "function",
					"function": map[string]any{
						"name":        name,
						"description": desc,
						"parameters": map[string]any{
							"type": "object",
							"properties": map[string]any{
								"input": map[string]any{"type": "string", "description": "Raw freeform input for this custom tool"},
							},
							"required":             []any{"input"},
							"additionalProperties": false,
						},
					},
				})
				continue
			}
			desc, _ := t["description"].(string)
			params := t["parameters"]
			if params == nil {
				params = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			if pm, ok := params.(map[string]any); ok {
				if pm["type"] == "object" && pm["properties"] == nil {
					pm["properties"] = map[string]any{}
				}
			}
			convTools = append(convTools, map[string]any{
				"type": "function",
				"function": map[string]any{
					"name":        name,
					"description": desc,
					"parameters":  params,
					"strict":      t["strict"],
				},
			})
		}
		if len(convTools) > 0 {
			out["tools"] = convTools
		}
	}

	out["messages"] = messages
	// Ensure stream defaults? keep as is
	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("responses: marshal chat: %w", err)
	}
	return b, nil
}

// FromOpenAI translates a Chat Completions request to Responses API.
func FromOpenAI(body []byte) ([]byte, error) {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("responses: invalid body: %w", err)
	}
	if _, hasInput := raw["input"]; hasInput {
		// Already responses-shaped (Cursor CLI case)
		out := make(map[string]any)
		for k, v := range raw {
			out[k] = v
		}
		if _, ok := out["max_output_tokens"]; !ok {
			if v, ok := raw["max_completion_tokens"]; ok {
				out["max_output_tokens"] = v
			} else if v, ok := raw["max_tokens"]; ok {
				out["max_output_tokens"] = v
			}
		}
		delete(out, "max_tokens")
		delete(out, "max_completion_tokens")
		if m, ok := raw["model"].(string); ok {
			out["model"] = m
		}
		b, _ := json.Marshal(out)
		return b, nil
	}

	msgs, _ := raw["messages"].([]any)
	// Preserve client's stream preference; default false if absent
	streamVal := false
	if s, ok := raw["stream"].(bool); ok {
		streamVal = s
	}
	out := map[string]any{
		"model":  raw["model"],
		"stream": streamVal,
		"store":  false,
		"input":  []any{},
	}
	input := []any{}
	var instructions string
	hasSystem := false

	for _, mRaw := range msgs {
		m, ok := mRaw.(map[string]any)
		if !ok {
			continue
		}
		role, _ := m["role"].(string)
		if role == "system" || role == "developer" {
			if !hasSystem {
				if cnt, ok := m["content"].(string); ok {
					instructions = cnt
				} else if arr, ok := m["content"].([]any); ok {
					var parts []string
					for _, p := range arr {
						if pm, ok := p.(map[string]any); ok {
							if t, _ := pm["text"].(string); t != "" {
								parts = append(parts, t)
							}
						}
					}
					instructions = strings.Join(parts, "\n")
				}
				hasSystem = true
			}
			continue
		}
		if role == "user" || role == "assistant" {
			// reasoning item before assistant
			if role == "assistant" {
				if ri := buildReasoningInput(m); ri != nil {
					input = append(input, ri)
				}
			}
			contentType := "input_text"
			if role == "assistant" {
				contentType = "output_text"
			}
			contentRaw := m["content"]
			var content []any
			if s, ok := contentRaw.(string); ok && s != "" {
				content = []any{map[string]any{"type": contentType, "text": s}}
			} else if arr, ok := contentRaw.([]any); ok {
				for _, pRaw := range arr {
					p, ok := pRaw.(map[string]any)
					if !ok {
						continue
					}
					ct, _ := p["type"].(string)
					switch ct {
					case "text":
						content = append(content, map[string]any{"type": contentType, "text": p["text"]})
					case "image_url":
						var url string
						var detail string
						if iu, ok := p["image_url"].(map[string]any); ok {
							url, _ = iu["url"].(string)
							detail, _ = iu["detail"].(string)
						} else if u, ok := p["image_url"].(string); ok {
							url = u
						}
						if detail == "" {
							detail = "auto"
						}
						content = append(content, map[string]any{"type": "input_image", "image_url": url, "detail": detail})
					case "input_image":
						content = append(content, p)
					default:
						txt := ""
						if t, ok := p["text"].(string); ok {
							txt = t
						} else if c, ok := p["content"].(string); ok {
							txt = c
						} else {
							if b, err := json.Marshal(p); err == nil {
								txt = string(b)
							}
						}
						content = append(content, map[string]any{"type": contentType, "text": txt})
					}
				}
			}
			if len(content) > 0 {
				input = append(input, map[string]any{"type": "message", "role": role, "content": content})
			}
		}
		if role == "assistant" {
			if tcs, ok := m["tool_calls"].([]any); ok {
				for _, tcRaw := range tcs {
					tc, ok := tcRaw.(map[string]any)
					if !ok {
						continue
					}
					id, _ := tc["id"].(string)
					fn, _ := tc["function"].(map[string]any)
					name, _ := fn["name"].(string)
					if name == "" {
						name = "_unknown"
					}
					args, _ := fn["arguments"].(string)
					if args == "" {
						args = "{}"
					}
					input = append(input, map[string]any{"type": "function_call", "call_id": clampCallID(id), "name": name, "arguments": args})
				}
			}
		}
		if role == "tool" {
			cnt := m["content"]
			var outStr string
			switch v := cnt.(type) {
			case string:
				outStr = v
			case []any:
				var parts []string
				for _, c := range v {
					if pm, ok := c.(map[string]any); ok {
						if t, _ := pm["text"].(string); t != "" {
							parts = append(parts, t)
						}
					}
				}
				outStr = strings.Join(parts, "")
			default:
				if b, err := json.Marshal(v); err == nil {
					outStr = string(b)
				}
			}
			callID, _ := m["tool_call_id"].(string)
			input = append(input, map[string]any{"type": "function_call_output", "call_id": clampCallID(callID), "output": outStr})
		}
	}
	if !hasSystem {
		instructions = ""
	}
	out["instructions"] = instructions
	out["input"] = input

	// tools
	if tools, ok := raw["tools"].([]any); ok && len(tools) > 0 {
		var conv []any
		for _, tRaw := range tools {
			t, ok := tRaw.(map[string]any)
			if !ok {
				continue
			}
			if typ, _ := t["type"].(string); typ == "function" {
				fn, _ := t["function"].(map[string]any)
				if fn == nil {
					continue
				}
				name, _ := fn["name"].(string)
				desc, _ := fn["description"].(string)
				params := fn["parameters"]
				if params == nil {
					params = map[string]any{"type": "object", "properties": map[string]any{}}
				}
				if pm, ok := params.(map[string]any); ok && pm["type"] == "object" && pm["properties"] == nil {
					pm["properties"] = map[string]any{}
				}
				conv = append(conv, map[string]any{"type": "function", "name": name, "description": desc, "parameters": params, "strict": fn["strict"]})
			} else {
				conv = append(conv, t)
			}
		}
		if len(conv) > 0 {
			out["tools"] = conv
		}
	}
	// passthrough fields
	for _, k := range []string{"temperature", "top_p", "service_tier", "prompt_cache_key"} {
		if v, ok := raw[k]; ok {
			out[k] = v
		}
	}
	if v, ok := raw["max_output_tokens"]; ok {
		out["max_output_tokens"] = v
	} else if v, ok := raw["max_completion_tokens"]; ok {
		out["max_output_tokens"] = v
	} else if v, ok := raw["max_tokens"]; ok {
		out["max_output_tokens"] = v
	}
	if v, ok := raw["reasoning"]; ok {
		out["reasoning"] = v
	} else if v, ok := raw["reasoning_effort"]; ok {
		if s, ok := v.(string); ok {
			out["reasoning"] = map[string]any{"effort": s, "summary": "auto"}
		}
	}

	b, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("responses: marshal: %w", err)
	}
	return b, nil
}

func normalizeInput(inp any) []any {
	switch v := inp.(type) {
	case string:
		t := strings.TrimSpace(v)
		if t == "" {
			t = "..."
		}
		return []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": t}}}}
	case []any:
		if len(v) == 0 {
			return []any{map[string]any{"type": "message", "role": "user", "content": []any{map[string]any{"type": "input_text", "text": "..."}}}}
		}
		return v
	default:
		return nil
	}
}

func clampCallID(id string) string {
	if len(id) > 64 {
		return id[:64]
	}
	return id
}

func strOr(v any, def string) string {
	if s, ok := v.(string); ok && s != "" {
		return s
	}
	return def
}

func buildReasoningInput(msg map[string]any) map[string]any {
	var encrypted string
	if s, ok := msg["encrypted_content"].(string); ok && s != "" {
		encrypted = s
	} else if s, ok := msg["reasoning_encrypted_content"].(string); ok && s != "" {
		encrypted = s
	} else if r, ok := msg["reasoning"].(map[string]any); ok {
		if s, ok := r["encrypted_content"].(string); ok {
			encrypted = s
		}
	}
	var summaryText string
	if s, ok := msg["reasoning_content"].(string); ok && strings.TrimSpace(s) != "" {
		summaryText = s
	} else if s, ok := msg["reasoning"].(string); ok && strings.TrimSpace(s) != "" {
		summaryText = s
	} else if arr, ok := msg["reasoning_details"].([]any); ok {
		var parts []string
		for _, d := range arr {
			if m, ok := d.(map[string]any); ok {
				if t, _ := m["text"].(string); t != "" {
					parts = append(parts, t)
				} else if c, _ := m["content"].(string); c != "" {
					parts = append(parts, c)
				}
			}
		}
		summaryText = strings.Join(parts, "\n")
	}
	if encrypted == "" && summaryText == "" {
		return nil
	}
	item := map[string]any{"type": "reasoning"}
	if summaryText != "" {
		item["summary"] = []any{map[string]any{"type": "summary_text", "text": summaryText}}
	}
	if encrypted != "" {
		item["encrypted_content"] = encrypted
	}
	return item
}

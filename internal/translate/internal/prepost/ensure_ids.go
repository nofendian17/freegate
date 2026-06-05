package prepost

import (
	"encoding/json"
	"fmt"
	"regexp"
)

var (
	toolIDPattern  = regexp.MustCompile(AnthropicToolIDPattern)
	invalidIDChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)
)

// EnsureToolCallIds walks body.messages and sanitizes or regenerates
// any tool-call ids that violate the Anthropic pattern
// ^[a-zA-Z0-9_-]+$:
//
//   - OpenAI shape:  messages[i].tool_calls[j].id  and  messages[i].tool_call_id (role:"tool")
//   - Claude shape:  messages[i].content[k].id     (type:"tool_use")
//                    messages[i].content[k].tool_use_id (type:"tool_result")
//
// Sanitization strips invalid characters. If nothing remains, a
// deterministic id of the form "call_msg{i}_tc{j}_{name}" is generated.
func EnsureToolCallIds(body []byte) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("prepost: ensure tool call ids: %w", err)
	}

	msgs, ok := raw["messages"].([]any)
	if !ok {
		return body, nil
	}

	for i, mAny := range msgs {
		m, _ := mAny.(map[string]any)
		if m == nil {
			continue
		}

		// OpenAI: assistant tool_calls[].id
		if tcs, ok := m["tool_calls"].([]any); ok {
			for j, tcAny := range tcs {
				tc, _ := tcAny.(map[string]any)
				if tc == nil {
					continue
				}
				name := ""
				if fn, _ := tc["function"].(map[string]any); fn != nil {
					name, _ = fn["name"].(string)
				}
				id, _ := tc["id"].(string)
				tc["id"] = ensureID(id, name, i, j)
			}
		}

		// OpenAI: tool message tool_call_id
		if role, _ := m["role"].(string); role == "tool" {
			if id, _ := m["tool_call_id"].(string); id != "" {
				m["tool_call_id"] = ensureID(id, "", i, 0)
			}
		}

		// Claude: content[].tool_use / tool_result
		if content, ok := m["content"].([]any); ok {
			for k, partAny := range content {
				part, _ := partAny.(map[string]any)
				if part == nil {
					continue
				}
				typ, _ := part["type"].(string)
				switch typ {
				case "tool_use":
					name, _ := part["name"].(string)
					id, _ := part["id"].(string)
					part["id"] = ensureID(id, name, i, k)
				case "tool_result":
					if id, _ := part["tool_use_id"].(string); id != "" {
						part["tool_use_id"] = ensureID(id, "", i, k)
					}
				}
			}
		}
	}

	out, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("prepost: ensure tool call ids: marshal: %w", err)
	}
	return out, nil
}

// ensureID returns id if it already matches the Anthropic pattern;
// otherwise it sanitizes by stripping invalid characters; if nothing
// remains, it generates a deterministic id from the position and name.
func ensureID(id, name string, msgIdx, tcIdx int) string {
	if id != "" && toolIDPattern.MatchString(id) {
		return id
	}
	if id != "" {
		sanitized := invalidIDChars.ReplaceAllString(id, "")
		if sanitized != "" {
			return sanitized
		}
	}
	safeName := invalidIDChars.ReplaceAllString(name, "")
	if safeName != "" {
		safeName = "_" + safeName
	}
	return fmt.Sprintf("call_msg%d_tc%d%s", msgIdx, tcIdx, safeName)
}

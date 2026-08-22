package prepost

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// PrepareUpstream applies the three handler-level normalizations that were
// previously done as three separate JSON passes (NormalizeRoles,
// NormalizeRequestReasoning, EnsureStreamOptions) in a single pass.
//
// It performs one Unmarshal, applies all mutations on the same map, and
// marshals once. This reduces allocs from 3x to 1x on the hot path.
func PrepareUpstream(body []byte) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}
	// Fast path: if none of the relevant keys are present, avoid parsing.
	hasDeveloper := bytes.Contains(body, []byte(`"developer"`))
	hasReasoning := bytes.Contains(body, []byte(`"reasoning":`))
	hasStream := bytes.Contains(body, []byte(`"stream"`))
	if !hasDeveloper && !hasReasoning && !hasStream {
		return body, nil
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("prepost: prepare upstream: %w", err)
	}

	changed := false

	// 1. Normalize roles: developer -> system
	if hasDeveloper {
		if msgs, ok := raw["messages"].([]any); ok && len(msgs) > 0 {
			for i, mAny := range msgs {
				m, _ := mAny.(map[string]any)
				if m == nil {
					continue
				}
				if role, _ := m["role"].(string); role == "developer" {
					m["role"] = "system"
					msgs[i] = m
					changed = true
				}
			}
			if changed {
				raw["messages"] = msgs
			}
		}
	}

	// 2. Normalize request reasoning: reasoning -> reasoning_content for assistant
	if hasReasoning {
		if msgs, ok := raw["messages"].([]any); ok && len(msgs) > 0 {
			reasonChanged := false
			for _, mAny := range msgs {
				m, _ := mAny.(map[string]any)
				if m == nil {
					continue
				}
				if role, _ := m["role"].(string); role != "assistant" {
					continue
				}
				_, hasRC := m["reasoning_content"]
				r, hasR := m["reasoning"]
				if hasR && !hasRC {
					m["reasoning_content"] = r
					reasonChanged = true
				}
			}
			if reasonChanged {
				changed = true
			}
		}
	}

	// 3. Ensure stream_options when stream == true
	if hasStream {
		if stream, _ := raw["stream"].(bool); stream {
			if _, ok := raw["stream_options"]; !ok {
				raw["stream_options"] = map[string]any{"include_usage": true}
				changed = true
			}
		}
	}

	if !changed {
		return body, nil
	}

	out, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("prepost: prepare upstream: marshal: %w", err)
	}
	return out, nil
}

package prepost

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// NormalizeRequestReasoning ensures that any assistant message that has
// a "reasoning" field but is missing "reasoning_content" gets the latter
// set to the value of "reasoning". DeepSeek's thinking mode requires
// reasoning_content to be present in conversation history; some clients
// only forward the proxy-added "reasoning" field, not "reasoning_content".
func NormalizeRequestReasoning(body []byte) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}

	// Fast-path: skip if neither "reasoning" nor "reasoning_content" keys
	// are present. Use "reasoning": (with colon) to avoid false positives
	// from "reasoning_content".
	if !bytes.Contains(body, []byte(`"reasoning":`)) {
		return body, nil
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("prepost: normalize request reasoning: %w", err)
	}

	msgs, ok := raw["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return body, nil
	}

	changed := false
	for _, mAny := range msgs {
		m, _ := mAny.(map[string]any)
		if m == nil {
			continue
		}
		role, _ := m["role"].(string)
		if role != "assistant" {
			continue
		}
		_, hasRC := m["reasoning_content"]
		r, hasR := m["reasoning"]
		if hasR && !hasRC {
			m["reasoning_content"] = r
			changed = true
		}
	}

	if !changed {
		return body, nil
	}

	out, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("prepost: normalize request reasoning: marshal: %w", err)
	}
	return out, nil
}

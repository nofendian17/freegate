package prepost

import (
	"encoding/json"
	"fmt"
)

// NormalizeThinkingConfig removes body.thinking if the last entry in
// body.messages is not role:"user". The Anthropic API rejects thinking
// when the most recent turn is from the assistant (or empty).
//
// If messages is empty or thinking is not enabled, the body is returned
// unchanged.
func NormalizeThinkingConfig(body []byte) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("prepost: normalize thinking: %w", err)
	}

	thinking, ok := raw["thinking"].(map[string]any)
	if !ok {
		return body, nil
	}
	typ, _ := thinking["type"].(string)
	if typ != "enabled" {
		return body, nil
	}

	msgs, ok := raw["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return body, nil
	}
	last, _ := msgs[len(msgs)-1].(map[string]any)
	role, _ := last["role"].(string)
	if role == "user" {
		return body, nil
	}

	delete(raw, "thinking")
	out, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("prepost: normalize thinking: marshal: %w", err)
	}
	return out, nil
}

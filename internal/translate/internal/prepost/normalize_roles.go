package prepost

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// NormalizeRoles converts OpenAI "developer" role messages to "system".
// The "developer" role was introduced by OpenAI for o-series models as
// a replacement for "system". Providers that do not support it (e.g.
// DeepSeek) reject requests containing this role. Since "developer" is
// semantically equivalent to "system", normalizing it to "system" before
// forwarding ensures broad upstream compatibility.
func NormalizeRoles(body []byte) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}

	if !bytes.Contains(body, []byte(`"developer"`)) {
		return body, nil
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("prepost: normalize roles: %w", err)
	}

	msgs, ok := raw["messages"].([]any)
	if !ok || len(msgs) == 0 {
		return body, nil
	}

	changed := false
	for i, mAny := range msgs {
		m, _ := mAny.(map[string]any)
		if m == nil {
			continue
		}
		role, _ := m["role"].(string)
		if role == "developer" {
			m["role"] = "system"
			msgs[i] = m
			changed = true
		}
	}

	if !changed {
		return body, nil
	}

	raw["messages"] = msgs
	out, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("prepost: normalize roles: marshal: %w", err)
	}
	return out, nil
}

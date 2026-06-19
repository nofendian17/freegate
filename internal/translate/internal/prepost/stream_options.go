package prepost

import (
	"encoding/json"
	"fmt"
)

// EnsureStreamOptions adds stream_options: {"include_usage": true} to requests
// that have stream: true but no stream_options field. Some providers (e.g.
// DeepSeek) require stream_options to be explicitly set alongside stream = true
// and return a 400 error otherwise.
//
// This is safe to call unconditionally — it's a no-op when stream is false,
// missing, or stream_options is already present.
func EnsureStreamOptions(body []byte) ([]byte, error) {
	if len(body) == 0 {
		return body, nil
	}

	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("prepost: ensure stream options: %w", err)
	}

	stream, _ := raw["stream"].(bool)
	if !stream {
		return body, nil
	}

	// Already has stream_options — don't overwrite.
	if _, ok := raw["stream_options"]; ok {
		return body, nil
	}

	raw["stream_options"] = map[string]any{
		"include_usage": true,
	}

	out, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("prepost: ensure stream options: marshal: %w", err)
	}
	return out, nil
}

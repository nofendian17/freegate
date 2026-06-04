package httputil

import "encoding/json"

// Int64 converts a value (commonly from a generic map[string]any or
// json.Number) to int64, returning 0 for unrecognized types.
func Int64(v any) int64 {
	switch x := v.(type) {
	case int:
		return int64(x)
	case int32:
		return int64(x)
	case int64:
		return x
	case float64:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	default:
		return 0
	}
}

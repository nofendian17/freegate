package helpers

import (
	"crypto/rand"
	"strings"
)

// SplitSSE returns the complete sep-terminated chunks in data plus the
// partial trailing remainder, which the caller retains for the next read.
func SplitSSE(data, sep string) (chunks []string, rest string) {
	var out []string
	for {
		idx := strings.Index(data, sep)
		if idx < 0 {
			return out, data
		}
		out = append(out, data[:idx])
		data = data[idx+len(sep):]
	}
}

// AsInt64 coerces a JSON numeric value to int64; ok is false for
// non-numeric values.
func AsInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	}
	return 0, false
}

// AsInt coerces a JSON numeric value to int; non-numeric values yield 0.
func AsInt(v any) int {
	n, _ := AsInt64(v)
	return int(n)
}

// RandomID returns n lowercase-alphanumeric random characters.
// A single rand.Read fills the buffer instead of one CSPRNG call per char.
func RandomID(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	if n <= 0 {
		return ""
	}
	raw := make([]byte, n)
	if _, err := rand.Read(raw); err != nil {
		// Fallback: zero buffer still maps deterministically; callers only
		// need uniqueness-shaped IDs, not secrecy, in this path.
		for i := range raw {
			raw[i] = byte(i)
		}
	}
	b := make([]byte, n)
	for i, v := range raw {
		b[i] = chars[int(v)%len(chars)]
	}
	return string(b)
}

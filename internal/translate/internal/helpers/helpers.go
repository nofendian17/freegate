package helpers

import (
	"crypto/rand"
	"math/big"
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
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	case int64:
		return int(n)
	}
	return 0
}

// RandomID returns n lowercase-alphanumeric random characters.
func RandomID(n int) string {
	const chars = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	for i := range b {
		idx, _ := rand.Int(rand.Reader, big.NewInt(int64(len(chars))))
		b[i] = chars[idx.Int64()]
	}
	return string(b)
}

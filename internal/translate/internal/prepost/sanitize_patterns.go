package prepost

import (
	"bytes"
	"strings"
)

// Unicode property escapes (\p{...} / \P{...}) are valid in some regex
// flavors (ECMA with the u flag, Go/RE2) but rejected outright by others
// (Python re, strict JSON Schema validators). A provider in the latter
// camp fails the whole request with invalid_request_error naming the
// pattern — e.g. Claude Code tool schemas using \p{Cc} character classes,
// which one provider answered with "is not a regex".
//
// SanitizeUnsupportedPatterns drops pattern values (and patternProperties
// keys) containing such escapes. Dropping loosens validation but never
// breaks the request; the alternative is a hard 400 on every call using
// the affected tools (live incident: a provider answered "is not a regex"
// for a Claude Code tool pattern). It returns the number of dropped entries.
func SanitizeUnsupportedPatterns(v any) int {
	dropped := 0
	switch t := v.(type) {
	case map[string]any:
		if pat, ok := t["pattern"].(string); ok && hasUnicodePropertyEscape(pat) {
			delete(t, "pattern")
			dropped++
		}
		if pp, ok := t["patternProperties"].(map[string]any); ok {
			for key := range pp {
				if hasUnicodePropertyEscape(key) {
					delete(pp, key)
					dropped++
				}
			}
		}
		for _, val := range t {
			dropped += SanitizeUnsupportedPatterns(val)
		}
	case []any:
		for _, item := range t {
			dropped += SanitizeUnsupportedPatterns(item)
		}
	}
	return dropped
}

// hasUnicodePropertyEscape reports whether s contains a \p{...} or \P{...}
// escape. A lone backslash-p without the brace is left alone: it is valid
// (if meaningless) in every flavor and some other construct we do not know.
func hasUnicodePropertyEscape(s string) bool {
	return strings.Contains(s, `\p{`) || strings.Contains(s, `\P{`)
}

// HasUnicodePropertyPattern is the byte-level fast path: whether body may
// contain an escape SanitizeUnsupportedPatterns would drop. Callers check
// this (plus the relevant section key) before paying for a JSON parse.
func HasUnicodePropertyPattern(body []byte) bool {
	return bytes.Contains(body, []byte(`\p{`)) || bytes.Contains(body, []byte(`\P{`))
}

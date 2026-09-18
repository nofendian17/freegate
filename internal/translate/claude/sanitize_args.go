package claude

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strings"
)

// dsmlSigilRe matches any DSML tag opener or closer, ASCII or fullwidth
// pipes, any case. It is the truncation point for argument values.
//
// The bracket also matches its JSON-escaped form: Go's encoder (used by
// RepairToolArgs' re-marshal path) emits `<` as `\u003c`, so repaired
// arguments carry `\u003c/｜DSML｜\u003e` rather than the literal sigil.
var dsmlSigilRe = regexp.MustCompile(`(?i)(?:<|\\u003c)/?[|｜]DSML`)

// SanitizeToolArgs applies the vllm-project/vllm#56302 principle at the
// proxy layer: a parameter value ends at the first DSML tag.
//
// DeepSeek-V4 sometimes closes a parameter with `</｜DSML｜>` instead of
// `</｜DSML｜parameter>`. A serving stack without that fix flattens the
// swallowed parameters into one JSON string value, e.g.
// {"alpha":"first</｜DSML｜>...second"}. The sigil is rendered from special
// tokens the model cannot emit as content, so whatever follows it is markup,
// never legitimate argument data: each string value is truncated at the
// first sigil and trailing whitespace is trimmed.
//
// Limits (same as the server-side discussion): a swallowed sibling
// parameter (beta above) is unrecoverable here — the upstream already
// flattened it into alpha's value, and only the parser that still sees
// the DSML structure can re-split it. This guarantees no markup reaches
// the tool, not full argument recovery.
//
// Only call on RESPONSE-side arguments. Request-side tool input may
// legitimately discuss DSML (e.g. repro prompts) and must not be altered.
//
// Returns the input unchanged (byte-identical) when it holds no sigil or
// is not a JSON object; numbers keep their literal form via json.Number.
func SanitizeToolArgs(s string) string {
	if s == "" || !dsmlSigilRe.MatchString(s) {
		return s
	}
	dec := json.NewDecoder(strings.NewReader(s))
	dec.UseNumber()
	var v any
	if err := dec.Decode(&v); err != nil {
		return s
	}
	obj, ok := v.(map[string]any)
	if !ok {
		return s
	}
	if !truncateValuesAtDsml(obj) {
		return s
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(obj); err != nil {
		return s
	}
	return strings.TrimRight(buf.String(), "\n")
}

// truncateValuesAtDsml cuts every string value in the decoded arguments at
// the first DSML sigil, recursing into nested objects and arrays. It
// reports whether anything changed.
func truncateValuesAtDsml(v any) bool {
	changed := false
	switch t := v.(type) {
	case map[string]any:
		for k, e := range t {
			switch e := e.(type) {
			case string:
				if loc := dsmlSigilRe.FindStringIndex(e); loc != nil {
					t[k] = strings.TrimRight(e[:loc[0]], " \t\r\n")
					changed = true
				}
			default:
				if truncateValuesAtDsml(e) {
					changed = true
				}
			}
		}
	case []any:
		for i, e := range t {
			switch e := e.(type) {
			case string:
				if loc := dsmlSigilRe.FindStringIndex(e); loc != nil {
					t[i] = strings.TrimRight(e[:loc[0]], " \t\r\n")
					changed = true
				}
			default:
				if truncateValuesAtDsml(e) {
					changed = true
				}
			}
		}
	}
	return changed
}

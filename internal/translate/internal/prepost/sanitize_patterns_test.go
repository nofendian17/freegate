package prepost

import (
	"encoding/json"
	"strings"
	"testing"
)

const livePattern = `^(?!__.*__$)[^\\p{Cc}\\p{Cf}\\p{Zl}\\p{Zp}\\\"\\\\./[\\]]{1,200}$`

func TestSanitize_DropsNestedUnicodePatterns(t *testing.T) {
	schema := map[string]any{
		"type": "object",
		"properties": map[string]any{
			"field": map[string]any{"type": "string", "pattern": livePattern},
			"safe":  map[string]any{"type": "string", "pattern": `^[a-z]+$`},
			"nested": map[string]any{
				"type":  "object",
				"properties": map[string]any{
					"deep": map[string]any{"type": "string", "pattern": `\P{L}+`},
				},
			},
		},
		"items": map[string]any{"type": "string", "pattern": `\p{Nd}+`},
	}
	if n := SanitizeUnsupportedPatterns(schema); n != 3 {
		t.Fatalf("dropped=%d, want 3", n)
	}
	props := schema["properties"].(map[string]any)
	if _, ok := props["field"].(map[string]any)["pattern"]; ok {
		t.Error("live \\p pattern not dropped")
	}
	if p, _ := props["safe"].(map[string]any)["pattern"].(string); p != `^[a-z]+$` {
		t.Error("safe pattern must be preserved")
	}
	// Idempotent: second run drops nothing.
	if n := SanitizeUnsupportedPatterns(schema); n != 0 {
		t.Fatalf("second run dropped=%d, want 0", n)
	}
}

func TestSanitize_PatternPropertiesKeys(t *testing.T) {
	schema := map[string]any{
		"patternProperties": map[string]any{
			`^x-\p{L}+$`: map[string]any{"type": "string"},
			"^x-plain$":  map[string]any{"type": "string"},
		},
	}
	if n := SanitizeUnsupportedPatterns(schema); n != 1 {
		t.Fatalf("dropped=%d, want 1", n)
	}
	pp := schema["patternProperties"].(map[string]any)
	if _, ok := pp[`^x-\p{L}+$`]; ok {
		t.Error("patternProperties key with \\p not dropped")
	}
	if _, ok := pp["^x-plain$"]; !ok {
		t.Error("plain patternProperties key must be preserved")
	}
}

func TestSanitize_NonContainers(t *testing.T) {
	if n := SanitizeUnsupportedPatterns("pattern"); n != 0 {
		t.Fatalf("string dropped=%d, want 0", n)
	}
	if n := SanitizeUnsupportedPatterns(nil); n != 0 {
		t.Fatalf("nil dropped=%d, want 0", n)
	}
	if n := SanitizeUnsupportedPatterns(42.0); n != 0 {
		t.Fatalf("number dropped=%d, want 0", n)
	}
}

func TestPrepare_DropsToolPatternsModelIndependent(t *testing.T) {
	body := `{"model":"qwen-plus","messages":[{"role":"user","content":"hi"}],
		"tools":[{"name":"f","description":"d","input_schema":{"type":"object",
		"properties":{"field":{"type":"string","pattern":"` + livePattern + `"}}}}]}`
	out, applied, err := PrepareUpstreamWithModel([]byte(body), "qwen-plus")
	if err != nil {
		t.Fatal(err)
	}
	if !containsToken(applied, AppliedToolPatternDrop) {
		t.Fatalf("tokens=%v, want %q", applied, AppliedToolPatternDrop)
	}
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatal(err)
	}
	tools := raw["tools"].([]any)
	props := tools[0].(map[string]any)["input_schema"].(map[string]any)["properties"].(map[string]any)
	if _, ok := props["field"].(map[string]any)["pattern"]; ok {
		t.Error("pattern survived PrepareForUpstreamWithModel")
	}
	if strings.Contains(string(out), `\p{Cc}`) {
		t.Error("output still contains \\p escape")
	}
}

func TestPrepare_SafePatternsUntouched(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":"hi"}],
		"tools":[{"name":"f","input_schema":{"type":"object",
		"properties":{"city":{"type":"string","pattern":"^[a-z]+$"}}}}]}`
	out, applied, err := PrepareUpstreamWithModel([]byte(body), "m")
	if err != nil {
		t.Fatal(err)
	}
	if containsToken(applied, AppliedToolPatternDrop) {
		t.Fatalf("tokens=%v, must not include %q", applied, AppliedToolPatternDrop)
	}
	if !strings.Contains(string(out), `^[a-z]+$`) {
		t.Error("safe pattern must survive byte-identical")
	}
}

func TestNormalizeClaude_DropsToolPatterns(t *testing.T) {
	body := `{"model":"m","messages":[{"role":"user","content":[{"type":"text","text":"hi"}]}],
		"tools":[{"name":"f","description":"d","input_schema":{"type":"object",
		"properties":{"field":{"type":"string","pattern":"` + livePattern + `"}}}}]}`
	out, applied, err := NormalizeClaudeContent([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	if !containsToken(applied, AppliedToolPatternDrop) {
		t.Fatalf("tokens=%v, want %q", applied, AppliedToolPatternDrop)
	}
	if strings.Contains(string(out), `\p{Cc}`) {
		t.Error("output still contains \\p escape")
	}
	// Message content untouched.
	if !strings.Contains(string(out), `"text":"hi"`) {
		t.Error("message content altered")
	}
}

func containsToken(tokens []string, want string) bool {
	for _, t := range tokens {
		if t == want {
			return true
		}
	}
	return false
}

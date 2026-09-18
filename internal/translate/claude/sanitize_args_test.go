package claude

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestSanitizeToolArgs_MisspelledCloser mirrors vllm-project/vllm#56302:
// a parameter closed with `</｜DSML｜>` instead of `</｜DSML｜parameter>`
// must end at the first DSML tag. Beta is unrecoverable at proxy level
// (the upstream already flattened it into alpha's value), but alpha must
// arrive clean and no markup may reach the tool.
func TestSanitizeToolArgs_MisspelledCloser(t *testing.T) {
	d := "｜DSML｜"
	in := `{"alpha":"first</` + d + `>\n<` + d + "parameter name=\\\"beta\\\" string=\\\"true\\\">second\"}"
	got := SanitizeToolArgs(in)
	if strings.Contains(got, "DSML") {
		t.Errorf("expected no DSML markup, got: %q", got)
	}
	var v map[string]any
	if err := json.Unmarshal([]byte(got), &v); err != nil {
		t.Fatalf("result is not valid JSON: %v (%q)", err, got)
	}
	if v["alpha"] != "first" {
		t.Errorf("expected alpha truncated to 'first', got: %q", got)
	}
}

func TestSanitizeToolArgs_CleanPassthrough(t *testing.T) {
	for _, in := range []string{
		`{"alpha":"first","beta":"second"}`,
		`{"n":42,"f":4.5,"b":true,"z":null,"a":[1,"x"],"o":{"k":"v"}}`,
		`{}`,
		``,
		`not json at all`,
		`{"alpha":"a < b and c > d"}`,
	} {
		if got := SanitizeToolArgs(in); got != in {
			t.Errorf("expected byte-identical passthrough for %q, got %q", in, got)
		}
	}
}

func TestSanitizeToolArgs_Variants(t *testing.T) {
	cases := map[string]string{
		"ascii pipes":      `{"a":"x<|DSML|parameter>junk"}`,
		"lowercase":        `{"a":"x<｜dsml｜parameter>junk"}`,
		"nested":           `{"o":{"k":"v</｜DSML｜>"},"a":"ok"}`,
		"number preserved": `{"n":42,"s":"x</｜DSML｜>"}`,
		// Go's encoder escapes `<` as \u003c on re-marshal (RepairToolArgs
		// path); the truncation point must still be found.
		"json escaped": `{"a":"x\u003c/｜DSML｜\u003e"}`,
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			got := SanitizeToolArgs(in)
			if strings.Contains(got, "DSML") || strings.Contains(got, "dsml") {
				t.Errorf("expected no DSML markup, got: %q", got)
			}
			var v map[string]any
			if err := json.Unmarshal([]byte(got), &v); err != nil {
				t.Fatalf("result is not valid JSON: %v (%q)", err, got)
			}
		})
	}
}

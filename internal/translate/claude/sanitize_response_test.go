package claude

import (
	"strings"
	"testing"
)

func TestSanitizeAssistantText_DeepSeekLeak(t *testing.T) {
	in := "Here is the fix:\n<system-reminder>Warning: this conversation is from a prior session.</reminder>\n\\ No newline at end of file\n<feature-flag>\n<feature-flag-name>claude-cc-debug-superpowers-integration</feature-flag-name>\n<feature-flag-enabled>false</feature-flag-enabled>\n</feature-flag>\n<｜DSML｜parameter name=\"description\" string=\"true\">Locate project</｜DSML｜parameter>\ndone"
	got := SanitizeAssistantText(in)
	for _, leak := range []string{
		"system-reminder", "feature-flag", "DSML", "No newline",
		"</reminder>", "<｜", "｜>",
	} {
		if strings.Contains(got, leak) {
			t.Errorf("expected leak %q removed, got: %q", leak, got)
		}
	}
	if !strings.Contains(got, "Here is the fix:") || !strings.Contains(got, "done") {
		t.Errorf("expected legitimate content preserved, got: %q", got)
	}
}

// TestSanitizeAssistantText_DSMLLeakClasses covers the three leak shapes
// from vllm-project/vllm#54686: runaway invoke names, mis-spelled closers,
// and misspelled openers.
func TestSanitizeAssistantText_DSMLLeakClasses(t *testing.T) {
	d := "｜DSML｜"
	cases := map[string]string{
		// Class 1: invoke marker eaten, tail dumped into content.
		"runaway": "text <" + d + "tool_calls>\n<" + d + "invoke name=\"record_item {\ncategory: Dexes tail",
		// Class 2: mis-spelled closer </｜DSML｜> (missing tag name).
		"misclosed": "<" + d + "parameter name=\"alpha\" string=\"true\">first</" + d + ">\n<" + d + "parameter name=\"beta\" string=\"true\">second</" + d + "parameter>",
		// Class 3: misspelled opener, block consumed.
		"misspelled": "<" + d + "tool-calls\nrecord_item\n</plan>",
		"stray_plan":  "hi </plan> bye",
		"paired_plan": "a <plan>do x</plan> b",
	}
	for name, in := range cases {
		got := SanitizeAssistantText(in)
		t.Run(name, func(t *testing.T) {
			for _, leak := range []string{"DSML", "record_item", "category", "parameter", "<plan", "</plan>", "first", "second", "Dexes"} {
				if strings.Contains(got, leak) {
					t.Errorf("expected leak %q removed, got: %q", leak, got)
				}
			}
		})
	}
	if got := SanitizeAssistantText("text <" + d + "tool_calls>\n<" + d + "invoke name=\"record_item {\ncategory: Dexes tail"); !strings.Contains(got, "text") {
		t.Errorf("expected leading prose preserved, got: %q", got)
	}
	if got := SanitizeAssistantText("hi </plan> bye"); !strings.Contains(got, "hi") || !strings.Contains(got, "bye") {
		t.Errorf("expected surrounding prose preserved, got: %q", got)
	}
}

func TestSanitizeAssistantText_Passthrough(t *testing.T) {
	in := "Use `a < b` and 5 > 3 in code. Generic <tag> is not scaffold."
	if got := SanitizeAssistantText(in); got != in {
		t.Errorf("expected passthrough, got %q", got)
	}
}

func TestJSONToClaude_StripsDeepSeekScaffolding(t *testing.T) {
	in := `{"model":"deepseek-v4-flash","choices":[{"message":{"role":"assistant","content":"fix done\n<system-reminder>leak</reminder>"},"finish_reason":"stop"}]}`
	out, err := JSONToClaude([]byte(in))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(string(out), "system-reminder") || strings.Contains(string(out), "leak") {
		t.Errorf("expected scaffold stripped, got: %s", out)
	}
	if !strings.Contains(string(out), "fix done") {
		t.Errorf("expected content preserved, got: %s", out)
	}
}

func TestProcessChunk_StripsDeepSeekScaffolding(t *testing.T) {
	state := NewStreamState()
	chunk := map[string]any{"choices": []any{map[string]any{
		"delta":         map[string]any{"content": "hi <feature-flag><feature-flag-name>x</feature-flag-name></feature-flag> bye"},
		"finish_reason": nil,
	}}}
	events := ProcessChunk(chunk, state)
	joined := strings.Join(events, "")
	if strings.Contains(joined, "feature-flag") {
		t.Errorf("expected scaffold stripped, got: %s", joined)
	}
	if !strings.Contains(joined, "hi") || !strings.Contains(joined, "bye") {
		t.Errorf("expected content preserved, got: %s", joined)
	}
}

func TestSanitizeAssistantText_ExtremelyImportant(t *testing.T) {
	in := "<EXTREMELY_IMPORTANT>do not skip</EXTREMELY_IMPORTANT>ok"
	got := SanitizeAssistantText(in)
	if strings.Contains(got, "EXTREMELY") || strings.Contains(got, "do not skip") {
		t.Errorf("expected block removed, got %q", got)
	}
	if !strings.Contains(got, "ok") {
		t.Errorf("expected trailing content preserved, got %q", got)
	}
}

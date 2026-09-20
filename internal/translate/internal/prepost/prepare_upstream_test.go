package prepost

import (
	"encoding/json"
	"testing"
)

func TestPrepareUpstreamWithModel_DeepSeekFlash(t *testing.T) {
	body := []byte(`{"model":"deepseek-v4-flash","stream":true,"messages":[{"role":"developer","content":"sys"},{"role":"assistant","content":"hi"}]}`)
	out, applied, err := PrepareUpstreamWithModel(body, "deepseek-v4-flash")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	wantTokens := []string{AppliedDeveloperSystem, AppliedReasoningContent, AppliedStreamOptions, AppliedDeepSeekFlashTopP}
	if len(applied) != len(wantTokens) {
		t.Fatalf("expected tokens %v, got %v", wantTokens, applied)
	}
	for i, tok := range wantTokens {
		if applied[i] != tok {
			t.Errorf("expected token[%d]=%q, got %v", i, tok, applied)
		}
	}
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("invalid JSON result: %v (body=%s)", err, out)
	}
	msgs := raw["messages"].([]any)
	if msgs[0].(map[string]any)["role"] != "system" {
		t.Errorf("expected developer->system, got %v", msgs[0])
	}
	if rc := msgs[1].(map[string]any)["reasoning_content"]; rc != "" {
		t.Errorf("expected empty reasoning_content, got %v", rc)
	}
	if raw["top_p"] != 0.95 {
		t.Errorf("expected top_p=0.95, got %v", raw["top_p"])
	}
	if _, ok := raw["stream_options"]; !ok {
		t.Error("expected stream_options to be set")
	}
}

func TestPrepareUpstreamWithModel_NonDeepSeekUnchanged(t *testing.T) {
	body := []byte(`{"model":"qwen-plus","messages":[{"role":"assistant","content":"hi"}]}`)
	out, _, err := PrepareUpstreamWithModel(body, "qwen-plus")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(out) != string(body) {
		t.Errorf("expected body unchanged, got %s", out)
	}
}

// TestPrepareUpstreamWithModel_DeepSeekToolStop covers the wiring of the
// vllm#54686 port: DeepSeek tool requests leave with the DSML closer in
// stop and the token reported. Merge semantics (string/array/idempotent/
// full) are unit-tested at ensureDSMLToolStop level in dsml_tool_stop_test.
func TestPrepareUpstreamWithModel_DeepSeekToolStop(t *testing.T) {
	tools := `"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}]`
	body := []byte(`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],` + tools + `}`)
	out, applied, err := PrepareUpstreamWithModel(body, "deepseek-v4-flash")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(out, &raw); err != nil {
		t.Fatalf("invalid JSON result: %v (body=%s)", err, out)
	}
	stops, ok := raw["stop"].([]any)
	if !ok || len(stops) != 1 || stops[0] != DSMLToolCallsCloser {
		t.Errorf("expected stop=[closer], got %v", raw["stop"])
	}
	found := false
	for _, tok := range applied {
		if tok == AppliedDeepSeekToolStop {
			found = true
		}
	}
	if !found {
		t.Errorf("expected %q token, got %v", AppliedDeepSeekToolStop, applied)
	}
}

func TestPrepareUpstreamWithModel_DeepSeekToolStopSkipped(t *testing.T) {
	tools := `"tools":[{"type":"function","function":{"name":"get_weather","parameters":{"type":"object"}}}]`
	for name, tc := range map[string]struct {
		body  string
		model string
	}{
		"no tools":      {`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}]}`, "deepseek-v4-flash"},
		"empty tools":   {`{"model":"deepseek-v4-flash","messages":[{"role":"user","content":"hi"}],"tools":[]}`, "deepseek-v4-flash"},
		"non-deepseek":  {`{"model":"qwen-plus","messages":[{"role":"user","content":"hi"}],` + tools + `}`, "qwen-plus"},
		"unknown model": {`{"model":"m","messages":[{"role":"user","content":"hi"}],` + tools + `}`, "m"},
	} {
		t.Run(name, func(t *testing.T) {
			out, applied, err := PrepareUpstreamWithModel([]byte(tc.body), tc.model)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var raw map[string]any
			if err := json.Unmarshal(out, &raw); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			if _, ok := raw["stop"]; ok {
				t.Errorf("expected no stop, got %v", raw["stop"])
			}
			for _, tok := range applied {
				if tok == AppliedDeepSeekToolStop {
					t.Errorf("unexpected token, got %v", applied)
				}
			}
		})
	}
}

func TestPrepareUpstreamWithModel_EmptyModelPreservesLegacy(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":"hi","reasoning":"think"}]}`)
	got, applied, err := PrepareUpstreamWithModel(body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var raw map[string]any
	if err := json.Unmarshal(got, &raw); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	msgs, _ := raw["messages"].([]any)
	if len(msgs) != 1 {
		t.Fatalf("expected 1 message, got %v", raw["messages"])
	}
	m, _ := msgs[0].(map[string]any)
	if m["reasoning_content"] != "think" {
		t.Errorf("expected reasoning_content copied from reasoning, got %v", m["reasoning_content"])
	}
	found := false
	for _, tok := range applied {
		if tok == AppliedReasoningContent {
			found = true
		}
	}
	if !found {
		t.Errorf("expected %q in applied tokens %v", AppliedReasoningContent, applied)
	}
}

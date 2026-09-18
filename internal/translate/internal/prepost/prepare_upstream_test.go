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

func TestPrepareUpstreamWithModel_EmptyModelPreservesLegacy(t *testing.T) {
	body := []byte(`{"messages":[{"role":"assistant","content":"hi","reasoning":"think"}]}`)
	want, _, err := PrepareUpstream(body)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got, _, err := PrepareUpstreamWithModel(body, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("expected legacy equivalence\ngot:  %s\nwant: %s", got, want)
	}
}

package prepost

import (
	"encoding/json"
	"testing"
)

func TestNormalizeThinkingConfig(t *testing.T) {
	tests := []struct {
		name      string
		in        string
		wantKept  bool // expect "thinking" key to still exist in output
	}{
		{
			name:     "last message user, thinking kept",
			in:       `{"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"hi"}]}`,
			wantKept: true,
		},
		{
			name:     "last message assistant, thinking removed",
			in:       `{"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"hello"}]}`,
			wantKept: false,
		},
		{
			name:     "last message tool, thinking removed",
			in:       `{"thinking":{"type":"enabled","budget_tokens":1024},"messages":[{"role":"user","content":"hi"},{"role":"assistant","tool_calls":[{"id":"a"}]},{"role":"tool","tool_call_id":"a","content":"x"}]}`,
			wantKept: false,
		},
		{
			name:     "thinking not enabled, body unchanged",
			in:       `{"thinking":{"type":"disabled"},"messages":[{"role":"assistant","content":"x"}]}`,
			wantKept: true,
		},
		{
			name:     "no thinking, body unchanged",
			in:       `{"messages":[{"role":"assistant","content":"x"}]}`,
			wantKept: false,
		},
		{
			name:     "empty messages, thinking kept (nothing to check)",
			in:       `{"thinking":{"type":"enabled","budget_tokens":1024},"messages":[]}`,
			wantKept: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := NormalizeThinkingConfig([]byte(tc.in))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("invalid JSON: %v", err)
			}
			_, present := got["thinking"]
			if present != tc.wantKept {
				t.Errorf("thinking present=%v want=%v (output=%s)", present, tc.wantKept, out)
			}
		})
	}
}

func TestNormalizeThinkingConfig_Passthrough(t *testing.T) {
	out, err := NormalizeThinkingConfig(nil)
	if err != nil || out != nil {
		t.Errorf("empty body should passthrough, got out=%q err=%v", out, err)
	}
}

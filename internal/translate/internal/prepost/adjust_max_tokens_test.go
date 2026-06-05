package prepost

import (
	"encoding/json"
	"testing"
)

func TestAdjustMaxTokens(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		wantMT  float64
		wantSet bool // expect max_tokens to be present in output
	}{
		{
			name:    "no tools no max_tokens passthrough",
			in:      `{"model":"x"}`,
			wantMT:  0,
			wantSet: false,
		},
		{
			name:    "tools present no max_tokens -> DefaultMinTokens",
			in:      `{"model":"x","tools":[{"type":"function","function":{"name":"f"}}]}`,
			wantMT:  DefaultMinTokens,
			wantSet: true,
		},
		{
			name:    "tools present low max_tokens -> bumped",
			in:      `{"max_tokens":256,"tools":[{"type":"function"}]}`,
			wantMT:  DefaultMinTokens,
			wantSet: true,
		},
		{
			name:    "tools present sufficient max_tokens unchanged",
			in:      `{"max_tokens":8192,"tools":[{"type":"function"}]}`,
			wantMT:  8192,
			wantSet: true,
		},
		{
			name:    "thinking budget forces strict-greater",
			in:      `{"max_tokens":4096,"thinking":{"type":"enabled","budget_tokens":4096}}`,
			wantMT:  4096 + 1024,
			wantSet: true,
		},
		{
			name:    "thinking budget less than max_tokens unchanged",
			in:      `{"max_tokens":8192,"thinking":{"type":"enabled","budget_tokens":4096}}`,
			wantMT:  8192,
			wantSet: true,
		},
		{
			// tool-bump runs first and already exceeds the budget, so the
			// result is the bumped min-tokens, not budget+1024.
			name:    "thinking budget plus tools bumps low max_tokens above budget",
			in:      `{"max_tokens":512,"tools":[{"type":"function"}],"thinking":{"type":"enabled","budget_tokens":1024}}`,
			wantMT:  DefaultMinTokens,
			wantSet: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, err := AdjustMaxTokens([]byte(tc.in))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			var got map[string]any
			if err := json.Unmarshal(out, &got); err != nil {
				t.Fatalf("invalid JSON output: %v", err)
			}
			v, present := got["max_tokens"]
			if present != tc.wantSet {
				t.Fatalf("max_tokens present=%v want=%v (output=%s)", present, tc.wantSet, out)
			}
			if present {
				if v.(float64) != tc.wantMT {
					t.Errorf("max_tokens=%v want=%v", v, tc.wantMT)
				}
			}
		})
	}
}

func TestAdjustMaxTokens_Empty(t *testing.T) {
	out, err := AdjustMaxTokens(nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != nil {
		t.Errorf("expected nil passthrough, got %q", out)
	}
}

func TestAdjustMaxTokens_InvalidJSON(t *testing.T) {
	_, err := AdjustMaxTokens([]byte(`{`))
	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

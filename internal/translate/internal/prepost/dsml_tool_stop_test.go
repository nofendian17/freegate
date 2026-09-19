package prepost

import (
	"reflect"
	"testing"
)

func TestEnsureDSMLToolStop(t *testing.T) {
	closer := DSMLToolCallsCloser
	tests := []struct {
		name      string
		stop      any // nil = field absent
		wantStop  any
		wantAdapt bool
	}{
		{"absent", nil, []any{closer}, true},
		{"string", "\n", []any{"\n", closer}, true},
		{"string equal", closer, closer, false},
		{"array", []any{"END"}, []any{"END", closer}, true},
		{"array has closer", []any{"A", closer}, []any{"A", closer}, false},
		{"array full", []any{"1", "2", "3", "4"}, []any{"1", "2", "3", "4"}, false},
		{"malformed", 42, 42, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := map[string]any{}
			if tt.stop != nil {
				raw["stop"] = tt.stop
			}
			if got := ensureDSMLToolStop(raw); got != tt.wantAdapt {
				t.Fatalf("changed=%v, want %v (stop=%v)", got, tt.wantAdapt, raw["stop"])
			}
			if !reflect.DeepEqual(raw["stop"], tt.wantStop) {
				t.Errorf("stop=%v, want %v", raw["stop"], tt.wantStop)
			}
		})
	}
}

func TestDSMLToolCallsCloser_MatchesUpstreamLiteral(t *testing.T) {
	// Must stay byte-identical to vLLM DSML_TOOL_END (fullwidth U+FF5C
	// pipes): the sampler only stops on the exact literal the model emits.
	if DSMLToolCallsCloser != "</｜DSML｜tool_calls>" {
		t.Errorf("closer drifted: %q", DSMLToolCallsCloser)
	}
}

package upstream

import (
	"testing"
)

func TestKilo_Match_Prefix(t *testing.T) {
	k := &KiloUpstream{
		prefixes: []string{"kilo/", "kilo-", "openrouter/"},
	}

	tests := []struct {
		modelID string
		want    bool
	}{
		{"kilo-auto/free", true},
		{"kilo-default", true},
		{"openrouter/owl-alpha", true},
		{"openrouter/free", true},
		{"nvidia/nemotron-3:free", true},  // :free suffix matches
		{"nvidia/nemotron-3", false},      // no :free suffix, no prefix
		{"unknown-model", false},
		{"", false},
	}

	for _, tt := range tests {
		got := k.Match(tt.modelID)
		if got != tt.want {
			t.Errorf("Match(%q) = %v, want %v", tt.modelID, got, tt.want)
		}
	}
}

func TestKilo_Match_FreeSuffix(t *testing.T) {
	k := &KiloUpstream{
		prefixes: []string{"kilo/", "openrouter/"},
	}

	tests := []struct {
		modelID string
		want    bool
	}{
		{"nvidia/nemotron-3-super-120b-a12b:free", true},
		{"poolside/laguna-m.1:free", true},
		{"poolside/laguna-xs.2:free", true},
		{"nvidia/nemotron-3-nano-omni:free", true},
		{"model-without-suffix", false},
		{"model:notfree", false},
	}

	for _, tt := range tests {
		got := k.Match(tt.modelID)
		if got != tt.want {
			t.Errorf("Match(%q) = %v, want %v", tt.modelID, got, tt.want)
		}
	}
}

func TestKilo_Match_PrefixNoSuffix(t *testing.T) {
	k := &KiloUpstream{
		prefixes: []string{"kilo/", "openrouter/"},
	}

	if !k.Match("kilo/default") {
		t.Error("expected kilo/ prefix to match")
	}
	if !k.Match("openrouter/owl-alpha") {
		t.Error("expected openrouter/ prefix to match")
	}
	if k.Match("unknown-model") {
		t.Error("expected no match for unknown model")
	}
}

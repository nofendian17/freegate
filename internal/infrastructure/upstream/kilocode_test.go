package upstream

import (
	"testing"

	"freegate/internal/model"
)

func TestKilo_Match_PrefixInCache(t *testing.T) {
	k := NewKiloUpstream("", "", "")
	k.cache.Set([]model.Model{
		{ID: "kilo-auto/free", Provider: "kilo"},
		{ID: "openrouter/owl-alpha", Provider: "kilo"},
		{ID: "openrouter/free", Provider: "kilo"},
	})

	tests := []struct {
		modelID string
		want    bool
	}{
		{"kilo-auto/free", true},
		{"openrouter/owl-alpha", true},
		{"openrouter/free", true},
		{"kilo-default", false},
		{"openrouter/paid", false},
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

func TestKilo_Match_FreeSuffixInCache(t *testing.T) {
	k := NewKiloUpstream("", "", "")
	k.cache.Set([]model.Model{
		{ID: "nvidia/nemotron-3-super-120b-a12b:free", Provider: "kilo"},
		{ID: "poolside/laguna-m.1:free", Provider: "kilo"},
		{ID: "poolside/laguna-xs.2:free", Provider: "kilo"},
		{ID: "nvidia/nemotron-3-nano-omni:free", Provider: "kilo"},
		{ID: "qwen/qwen3.7-plus:free", Provider: "kilo"},
	})

	tests := []struct {
		modelID string
		want    bool
	}{
		{"nvidia/nemotron-3-super-120b-a12b:free", true},
		{"poolside/laguna-m.1:free", true},
		{"poolside/laguna-xs.2:free", true},
		{"nvidia/nemotron-3-nano-omni:free", true},
		{"qwen/qwen3.7-plus:free", true},
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

func TestKilo_Match_EmptyCache(t *testing.T) {
	k := NewKiloUpstream("", "", "")

	if k.Match("kilo-auto/free") {
		t.Error("expected no match when cache is empty")
	}
	if k.Match("openrouter/owl-alpha") {
		t.Error("expected no match when cache is empty")
	}
	if k.Match("nvidia/nemotron-3-super-120b-a12b:free") {
		t.Error("expected no match when cache is empty")
	}
}

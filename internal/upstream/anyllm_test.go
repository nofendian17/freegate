package upstream

import "testing"

func TestAnyLLMProvider_Name(t *testing.T) {
	p := &anyllmProvider{name: "opencode"}
	if got := p.Name(); got != "opencode" {
		t.Errorf("Name() = %q, want %q", got, "opencode")
	}
}

func TestAnyLLMProvider_Match_OpenCodeMatchesAll(t *testing.T) {
	p := &anyllmProvider{name: "opencode"}
	cases := []string{
		"deepseek-v4-flash-free",
		"gpt-4o",
		"kilo/something",
		"openrouter/foo",
	}
	for _, m := range cases {
		if !p.Match(m) {
			t.Errorf("opencode.Match(%q) = false, want true", m)
		}
	}
}

func TestAnyLLMProvider_Match_KiloPrefixes(t *testing.T) {
	p := &anyllmProvider{name: "kilo", prefixes: []string{"kilo/", "kilo-", "openrouter/"}}
	cases := map[string]bool{
		"kilo-auto/free":         true,
		"kilo-llama-3.3":          true,
		"openrouter/owl-alpha":    true,
		"nvidia/nemotron-3:free":  true, // :free suffix
		"deepseek-v4-flash-free":  false,
		"gpt-4o":                  false,
	}
	for m, want := range cases {
		if got := p.Match(m); got != want {
			t.Errorf("kilo.Match(%q) = %v, want %v", m, got, want)
		}
	}
}

package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestRelayRewrite(t *testing.T) {
	sel := NewRelaySelector()
	sel.SetPools([]RelayPool{{URL: "https://relay.example.com"}})
	req := httptest.NewRequest("POST", "https://opencode.ai/zen/v1/chat/completions", nil)
	if !sel.Apply(req) {
		t.Fatal("expected relay apply")
	}
	if req.URL.Host != "relay.example.com" {
		t.Errorf("host = %q", req.URL.Host)
	}
	if req.Header.Get("x-relay-target") != "https://opencode.ai" {
		t.Errorf("target = %q", req.Header.Get("x-relay-target"))
	}
	if req.Header.Get("x-relay-path") != "/zen/v1/chat/completions" {
		t.Errorf("path = %q", req.Header.Get("x-relay-path"))
	}
}

func TestRelayNoProxyBypass(t *testing.T) {
	req := httptest.NewRequest("GET", "https://api.llm7.io/v1/models", nil)
	must := &url.URL{Scheme: "https", Host: "api.llm7.io"}
	_ = must
	sel := NewRelaySelector()
	sel.SetPools([]RelayPool{{URL: "https://relay.example.com", NoProxy: "api.llm7.io"}})
	if sel.Apply(req) {
		t.Error("expected no-proxy bypass")
	}
	if req.URL.Host != "api.llm7.io" {
		t.Errorf("host must not rewrite, got %q", req.URL.Host)
	}
	if req.Header.Get("x-relay-target") != "" {
		t.Error("no relay headers on bypass")
	}
}

func TestRelayRoundRobin(t *testing.T) {
	sel := NewRelaySelector()
	sel.SetPools([]RelayPool{{URL: "https://a.example.com"}, {URL: "https://b.example.com"}})
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		req := httptest.NewRequest("GET", "https://x.test/", nil)
		_ = i
		_ = http.MethodGet
		sel.Apply(req)
		seen[req.URL.Host] = true
	}
	if !seen["a.example.com"] || !seen["b.example.com"] {
		t.Errorf("expected rotation across both, got %v", seen)
	}
}

func TestRelayStrictFallback(t *testing.T) {
	direct := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("direct-ok"))
	}))
	defer direct.Close()

	// Non-strict pool with dead relay: falls back to direct.
	sel := NewRelaySelector()
	sel.SetPools([]RelayPool{{URL: "http://127.0.0.1:1", Strict: false}})
	old := SharedRelay
	SharedRelay = sel
	defer func() { SharedRelay = old }()
	c := NewHTTPClientWithTransport(direct.URL, []string{"k"}, nil, nil)
	resp, err := c.Get(context.Background(), "/")
	if err != nil {
		t.Fatalf("non-strict must fall back to direct: %v", err)
	}
	defer resp.Body.Close()
	body := make([]byte, 9)
	n, _ := resp.Body.Read(body)
	if string(body[:n]) != "direct-ok" {
		t.Errorf("expected direct fallback body, got %q", body[:n])
	}

	// Strict pool with dead relay: surfaces the error.
	selStrict := NewRelaySelector()
	selStrict.SetPools([]RelayPool{{URL: "http://127.0.0.1:1", Strict: true}})
	SharedRelay = selStrict
	if _, err := c.Get(context.Background(), "/"); err == nil {
		t.Fatal("strict must fail, not fall back")
	}
}

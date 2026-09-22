package upstream

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"

	"freegate/internal/infrastructure/providers"
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

func TestRelayStrategies(t *testing.T) {
	pools := []RelayPool{{URL: "https://a.example.com"}, {URL: "https://b.example.com"}}

	sel := NewRelaySelector()
	sel.SetPools(pools)
	sel.SetStrategy("none")
	for i := 0; i < 3; i++ {
		p, _ := sel.Next()
		if p.URL != "https://a.example.com" {
			t.Fatalf("none must always pick first, got %s", p.URL)
		}
	}

	sel.SetStrategy("round-robin")
	seen := map[string]bool{}
	for i := 0; i < 4; i++ {
		p, _ := sel.Next()
		seen[p.URL] = true
	}
	if !seen["https://a.example.com"] || !seen["https://b.example.com"] {
		t.Fatalf("round-robin must rotate, got %v", seen)
	}

	sel.SetStrategy("random")
	for i := 0; i < 10; i++ {
		if _, ok := sel.Next(); !ok {
			t.Fatal("random must return a pool")
		}
	}

	sel.SetStrategy("bogus")
	p, _ := sel.Next()
	if p.URL == "" {
		t.Fatal("unknown strategy must keep current behavior")
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

func TestResolveRelayPools(t *testing.T) {
	pool := providers.ProxyPool{ProxyURL: "https://relay.test", NoProxy: "example.com", StrictProxy: true, Enabled: true}
	disabled := pool
	disabled.Enabled = false
	get := func(id uint) (providers.ProxyPool, error) {
		switch id {
		case 1:
			return pool, nil
		case 2:
			return disabled, nil
		default:
			return providers.ProxyPool{}, fmt.Errorf("not found")
		}
	}
	if got := ResolveRelayPools("u", "", nil, get); got != nil {
		t.Fatalf("global must be nil: %v", got)
	}
	if got := ResolveRelayPools("u", "direct", nil, get); len(got) != 0 || got == nil {
		t.Fatalf("direct must be empty non-nil: %v", got)
	}
	id := uint(1)
	got := ResolveRelayPools("u", "pool", &id, get)
	if len(got) != 1 || got[0].URL != pool.ProxyURL || got[0].NoProxy != pool.NoProxy || !got[0].Strict {
		t.Fatalf("pinned pool not resolved: %v", got)
	}
	bad := uint(9)
	if got := ResolveRelayPools("u", "pool", &bad, get); got != nil {
		t.Fatalf("missing pool must fall back to global: %v", got)
	}
	off := uint(2)
	if got := ResolveRelayPools("u", "pool", &off, get); got != nil {
		t.Fatalf("disabled pool must fall back to global: %v", got)
	}
	if got := ResolveRelayPools("u", "pool", nil, get); got != nil {
		t.Fatalf("pool mode without id must fall back to global: %v", got)
	}
}

// TestRelaySwapRace guards the live-swap path used by admin rebuilds
// (applyBuiltinProxies): swapping a client's relay selector while
// requests are in flight must not race. Run with -race.
func TestRelaySwapRace(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok"))
	}))
	defer srv.Close()
	c := NewHTTPClientWithTransport(srv.URL, []string{"k"}, nil, nil)
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				resp, err := c.Get(context.Background(), "/")
				if err != nil {
					t.Error(err)
					return
				}
				resp.Body.Close()
			}
		}()
	}
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			for j := 0; j < 25; j++ {
				switch (n + j) % 3 {
				case 0:
					c.SetRelayPools(nil)
				case 1:
					c.SetRelayPools([]RelayPool{})
				default:
					c.SetRelayPools([]RelayPool{{URL: "http://127.0.0.1:1"}})
				}
			}
		}(i)
	}
	wg.Wait()
}

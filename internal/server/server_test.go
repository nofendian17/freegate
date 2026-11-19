package server

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"freegate/internal/config"
	"freegate/internal/infrastructure/registry"
	"freegate/internal/infrastructure/upstream"
)

// TestBuildUpstreams_WarpWithoutPoolFailsClosed guards the typed-nil interface
// trap: with EGRESS_MODE=off there is no pool, so buildUpstreams must hand the
// builtins a truly nil Egress. A typed-nil *egress.Pool in the interface would
// slip past every `eg == nil` guard and make WARP mode dial the host's own IP
// instead of failing closed.
func TestBuildUpstreams_WarpWithoutPoolFailsClosed(t *testing.T) {
	cfg := &config.Config{
		UpstreamURLOpenCode: "https://opencode.example/v1",
		UpstreamKeyOpenCode: []string{"public"},
		UpstreamURLKilo:     "https://kilo.example/v1",
		UpstreamKeyKilo:     "anonymous",
		UpstreamURLLLM7:     "https://llm7.example/v1",
		EgressMode:          "off",
	}
	opencode, _, _, eg, err := buildUpstreams(cfg, upstream.NewTransport())
	if err != nil {
		t.Fatalf("build upstreams: %v", err)
	}
	if eg != nil {
		t.Fatalf("expected nil egress with EGRESS_MODE=off, got %#v", eg)
	}
	// Selecting WARP with no pool must fail closed, never dial the host IP.
	opencode.SetProxy("warp", nil)
	_, err = opencode.ListModels(context.Background())
	if err == nil || !errors.Is(err, upstream.ErrNoWarpPool) {
		t.Fatalf("warp without pool must fail closed with ErrNoWarpPool, got: %v", err)
	}
}

// TestApplyBuiltinProxies_DisabledPinGoesDirect guards the builtin path of
// the disable-proxy rule: once the pool a builtin is pinned to is disabled,
// traffic must connect directly instead of falling back to the global
// rotation (which may still contain other enabled relays).
func TestApplyBuiltinProxies_DisabledPinGoesDirect(t *testing.T) {
	var relayAHits, relayBHits, upstreamHits int
	modelsBody := `{"object":"list","data":[{"id":"m-free","object":"model"}]}`
	relayA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relayAHits++
		_, _ = w.Write([]byte(modelsBody))
	}))
	defer relayA.Close()
	relayB := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relayBHits++
		_, _ = w.Write([]byte(modelsBody))
	}))
	defer relayB.Close()
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		_, _ = w.Write([]byte(modelsBody))
	}))
	defer upstreamSrv.Close()

	ctx := t.Context()
	dsn := fmt.Sprintf("file:apply-builtin-%d?mode=memory&cache=shared", time.Now().UnixNano())
	store, err := registry.Open(ctx, dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	t.Cleanup(func() { _ = store.Close() })

	poolA, err := store.CreatePool(ctx, registry.ProxyPool{Name: "edge-a", ProxyURL: relayA.URL, Enabled: true})
	if err != nil {
		t.Fatalf("create pool a: %v", err)
	}
	if _, err := store.CreatePool(ctx, registry.ProxyPool{Name: "edge-b", ProxyURL: relayB.URL, Enabled: true}); err != nil {
		t.Fatalf("create pool b: %v", err)
	}
	if _, err := store.SetBuiltinProxy(ctx, "opencode", registry.ProxyModePool, &poolA.ID); err != nil {
		t.Fatalf("pin opencode: %v", err)
	}

	tr := upstreamSrv.Client().Transport.(*http.Transport)
	opencode := upstream.NewOpenCodeUpstreamWithTransport(upstreamSrv.URL, []string{"k"}, tr, nil)
	kilo := upstream.NewKiloUpstreamWithTransport(upstreamSrv.URL, "k", tr)
	llm7 := upstream.NewLLM7UpstreamWithTransport(upstreamSrv.URL, tr)

	oldRelay := upstream.SharedRelay
	upstream.SharedRelay = upstream.NewRelaySelector()
	t.Cleanup(func() { upstream.SharedRelay = oldRelay })

	if err := syncRelayPools(ctx, store); err != nil {
		t.Fatalf("sync pools: %v", err)
	}
	if err := applyBuiltinProxies(ctx, store, opencode, kilo, llm7); err != nil {
		t.Fatalf("apply builtin proxies: %v", err)
	}
	if _, err := opencode.ListModels(ctx); err != nil {
		t.Fatalf("list models (pinned): %v", err)
	}
	if relayAHits != 1 || upstreamHits != 0 {
		t.Fatalf("builtin must travel relay A: relayA=%d upstream=%d", relayAHits, upstreamHits)
	}

	if _, err := store.UpdatePool(ctx, poolA.ID, registry.ProxyPool{Name: poolA.Name, ProxyURL: poolA.ProxyURL, Enabled: false}); err != nil {
		t.Fatalf("disable pool: %v", err)
	}
	if err := syncRelayPools(ctx, store); err != nil {
		t.Fatalf("sync pools after disable: %v", err)
	}
	if err := applyBuiltinProxies(ctx, store, opencode, kilo, llm7); err != nil {
		t.Fatalf("apply builtin proxies after disable: %v", err)
	}
	if _, err := opencode.ListModels(ctx); err != nil {
		t.Fatalf("list models (disabled pin): %v", err)
	}
	if relayAHits != 1 || relayBHits != 0 || upstreamHits != 1 {
		t.Fatalf("disabled builtin pin must go direct: relayA=%d relayB=%d upstream=%d", relayAHits, relayBHits, upstreamHits)
	}
}

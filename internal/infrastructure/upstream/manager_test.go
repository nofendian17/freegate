package upstream

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"freegate/internal/infrastructure/registry"
)

func TestManager_RebuildPropagatesContextCancellation(t *testing.T) {
	store, err := registry.Open(t.Context(), "file:manager-cancel?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	manager := NewProviderManager(store, http.DefaultTransport.(*http.Transport).Clone())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := manager.Rebuild(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("Rebuild error = %v, want context.Canceled", err)
	}
}

func mgrModelsServer() *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{
			map[string]any{"id": "acme-gpt-1", "object": "model"},
		}})
	}))
}

func waitModels(t *testing.T, u *CustomUpstream, what string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		// Bare selection-seeded entries carry no OwnedBy; only a
		// completed catalog fetch attributes them.
		for _, m := range u.Models() {
			if m.OwnedBy != "" {
				return
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for models on %s", what)
}

func TestManager_RebuildSecondGenerationRefreshes(t *testing.T) {
	srv := mgrModelsServer()
	defer srv.Close()
	dsn := fmt.Sprintf("file:mgr-rebuild-%d?mode=memory&cache=shared", time.Now().UnixNano())
	store, err := registry.Open(t.Context(), dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := store.CreateProvider(t.Context(), registry.Provider{Name: "acme", BaseURL: srv.URL, APIKeys: []string{"k"}, Models: []string{"acme-gpt-1"}, RefreshSec: 10, Enabled: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	mgr := NewProviderManager(store, srv.Client().Transport.(*http.Transport))
	if err := mgr.Rebuild(t.Context()); err != nil {
		t.Fatalf("rebuild1: %v", err)
	}
	if len(mgr.All()) != 1 {
		t.Fatalf("expected 1 custom, got %d", len(mgr.All()))
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	defer mgr.Stop()
	mgr.Start(ctx)
	first := mgr.All()[0]
	waitModels(t, first, "first generation")

	if err := mgr.Rebuild(t.Context()); err != nil {
		t.Fatalf("rebuild2: %v", err)
	}
	second := mgr.All()[0]
	if second == first {
		t.Fatal("expected fresh object after rebuild")
	}
	waitModels(t, second, "second generation")
}

// TestManager_RebuildWidensSelection is a regression test for "adding new
// model in custom provider, not showing on list model": widening the stored
// selection must keep the new bare entry through Rebuild (not wiped by the
// old-cache carry-over) and the next refresh must serve it.
func TestManager_RebuildWidensSelection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{
			map[string]any{"id": "acme-gpt-1", "object": "model"},
			map[string]any{"id": "acme-new-1", "object": "model"},
		}})
	}))
	defer srv.Close()
	dsn := fmt.Sprintf("file:mgr-widen-%d?mode=memory&cache=shared", time.Now().UnixNano())
	store, err := registry.Open(t.Context(), dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	row, err := store.CreateProvider(t.Context(), registry.Provider{Name: "acme", BaseURL: srv.URL, APIKeys: []string{"k"}, Models: []string{"acme-gpt-1"}, RefreshSec: 10, Enabled: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	mgr := NewProviderManager(store, srv.Client().Transport.(*http.Transport))
	if err := mgr.Rebuild(t.Context()); err != nil {
		t.Fatalf("rebuild1: %v", err)
	}
	if _, err := mgr.Warm(t.Context(), "acme"); err != nil {
		t.Fatalf("warm1: %v", err)
	}
	if _, err := store.UpdateProvider(t.Context(), row.ID, registry.Provider{Name: "acme", BaseURL: srv.URL, APIKeys: []string{"k"}, Models: []string{"acme-gpt-1", "acme-new-1"}, RefreshSec: 10, Enabled: true}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := mgr.Rebuild(t.Context()); err != nil {
		t.Fatalf("rebuild2: %v", err)
	}
	second := mgr.All()[0]
	seen := map[string]bool{}
	for _, m := range second.Models() {
		seen[m.ID] = true
	}
	if !seen["acme-new-1"] {
		t.Fatalf("new model missing right after rebuild, got %v", second.Models())
	}
	got, err := mgr.Warm(t.Context(), "acme")
	if err != nil {
		t.Fatalf("warm2: %v", err)
	}
	seen = map[string]bool{}
	for _, m := range got {
		seen[m.ID] = true
	}
	if !seen["acme-gpt-1"] || !seen["acme-new-1"] {
		t.Fatalf("expected widened selection after refresh, got %v", got)
	}
}

// TestManager_PinnedPoolRoutesThroughRelay is an end-to-end proof of
// per-provider proxy selection: a pinned provider's catalog fetch travels
// the relay (x-relay-target set) and never hits the upstream directly,
// while a direct provider bypasses the relay entirely.
func TestManager_PinnedPoolRoutesThroughRelay(t *testing.T) {
	var upstreamHits, relayHits int
	var gotTarget, gotPath string
	upstreamSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHits++
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{
			map[string]any{"id": "m-relay", "object": "model"},
			map[string]any{"id": "m-direct", "object": "model"},
		}})
	}))
	defer upstreamSrv.Close()
	relaySrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		relayHits++
		gotTarget = r.Header.Get("x-relay-target")
		gotPath = r.Header.Get("x-relay-path")
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{
			map[string]any{"id": "m-relay", "object": "model"},
			map[string]any{"id": "m-direct", "object": "model"},
		}})
	}))
	defer relaySrv.Close()
	dsn := fmt.Sprintf("file:mgr-relay-%d?mode=memory&cache=shared", time.Now().UnixNano())
	store, err := registry.Open(t.Context(), dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	pool, err := store.CreatePool(t.Context(), registry.ProxyPool{Name: "edge-1", ProxyURL: relaySrv.URL, Enabled: true})
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	poolID := pool.ID
	if _, err := store.CreateProvider(t.Context(), registry.Provider{Name: "pinned", BaseURL: upstreamSrv.URL, APIKeys: []string{"k"}, Models: []string{"m-relay"}, RefreshSec: 10, Enabled: true, ProxyMode: registry.ProxyModePool, ProxyPoolID: &poolID}); err != nil {
		t.Fatalf("create pinned: %v", err)
	}
	if _, err := store.CreateProvider(t.Context(), registry.Provider{Name: "plain", BaseURL: upstreamSrv.URL, APIKeys: []string{"k"}, Models: []string{"m-direct"}, RefreshSec: 10, Enabled: true, ProxyMode: registry.ProxyModeDirect}); err != nil {
		t.Fatalf("create direct: %v", err)
	}
	mgr := NewProviderManager(store, upstreamSrv.Client().Transport.(*http.Transport))
	if err := mgr.Rebuild(t.Context()); err != nil {
		t.Fatalf("rebuild: %v", err)
	}
	got, err := mgr.Warm(t.Context(), "pinned")
	if err != nil {
		t.Fatalf("warm pinned: %v", err)
	}
	if len(got) != 1 || got[0].ID != "m-relay" {
		t.Fatalf("pinned warm: %+v", got)
	}
	if relayHits != 1 || upstreamHits != 0 {
		t.Fatalf("pinned must travel relay only: relay=%d upstream=%d", relayHits, upstreamHits)
	}
	if gotTarget != upstreamSrv.URL || gotPath != "/models" {
		t.Fatalf("relay headers: target=%q path=%q", gotTarget, gotPath)
	}
	got, err = mgr.Warm(t.Context(), "plain")
	if err != nil {
		t.Fatalf("warm direct: %v", err)
	}
	if len(got) != 1 || got[0].ID != "m-direct" {
		t.Fatalf("direct warm: %+v", got)
	}
	if relayHits != 1 || upstreamHits != 1 {
		t.Fatalf("direct must bypass relay: relay=%d upstream=%d", relayHits, upstreamHits)
	}
}

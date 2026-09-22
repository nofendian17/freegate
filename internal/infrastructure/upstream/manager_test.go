package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"freegate/internal/infrastructure/providers"
)

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
	store, err := providers.Open(dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	if _, err := store.CreateProvider(providers.Provider{Name: "acme", BaseURL: srv.URL, APIKeys: []string{"k"}, Models: []string{"acme-gpt-1"}, RefreshSec: 10, Enabled: true}); err != nil {
		t.Fatalf("create: %v", err)
	}
	mgr := NewProviderManager(store, srv.Client().Transport.(*http.Transport))
	if err := mgr.Rebuild(); err != nil {
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

	if err := mgr.Rebuild(); err != nil {
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
	store, err := providers.Open(dsn)
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	row, err := store.CreateProvider(providers.Provider{Name: "acme", BaseURL: srv.URL, APIKeys: []string{"k"}, Models: []string{"acme-gpt-1"}, RefreshSec: 10, Enabled: true})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	mgr := NewProviderManager(store, srv.Client().Transport.(*http.Transport))
	if err := mgr.Rebuild(); err != nil {
		t.Fatalf("rebuild1: %v", err)
	}
	if _, err := mgr.Warm("acme"); err != nil {
		t.Fatalf("warm1: %v", err)
	}
	if _, err := store.UpdateProvider(row.ID, providers.Provider{Name: "acme", BaseURL: srv.URL, APIKeys: []string{"k"}, Models: []string{"acme-gpt-1", "acme-new-1"}, RefreshSec: 10, Enabled: true}); err != nil {
		t.Fatalf("update: %v", err)
	}
	if err := mgr.Rebuild(); err != nil {
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
	got, err := mgr.Warm("acme")
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

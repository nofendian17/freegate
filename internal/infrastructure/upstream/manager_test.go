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
		if len(u.Models()) > 0 {
			return
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
	if _, err := store.CreateProvider(providers.Provider{Name: "acme", BaseURL: srv.URL, APIKeys: []string{"k"}, RefreshSec: 10, Enabled: true}); err != nil {
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

package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"freegate/internal/domain"
)

// TestCustom_Match_ExplicitSelection verifies only curated models route
// to the custom upstream: selection is exact, and unselected catalog
// entries never match.
func TestCustom_Match_ExplicitSelection(t *testing.T) {
	u := NewCustomUpstream("acme", "http://example.test", []string{"k"}, nil, []string{"acme-gpt-1"}, nil)
	if !u.Match("acme-gpt-1") {
		t.Fatal("expected selected model to match before any fetch")
	}
	if u.Match("acme-embed-1") {
		t.Fatal("expected unselected model to not match")
	}
	if u.Match("ACME-GPT-1") {
		t.Fatal("expected selection match to be exact (case-sensitive)")
	}
	empty := NewCustomUpstream("none", "http://example.test", []string{"k"}, nil, []string{}, nil)
	if empty.Match("anything") {
		t.Fatal("expected empty selection to match nothing")
	}
}

// TestCustom_Match_LegacyNil verifies the pre-selection upgrade path: a
// nil selection (row saved before the feature) routes everything, so
// existing providers keep working until the admin pins a selection.
func TestCustom_Match_LegacyNil(t *testing.T) {
	u := NewCustomUpstream("old", "http://example.test", []string{"k"}, nil, nil, nil)
	if !u.Match("anything-at-all") {
		t.Fatal("expected nil selection to match everything (legacy mode)")
	}
	if len(u.Models()) != 0 {
		t.Fatal("expected no seeded entries in legacy mode before first fetch")
	}
}

// TestCustom_ListModels_LegacyNil verifies a legacy refresh serves the
// whole fetched catalog in upstream order.
func TestCustom_ListModels_LegacyNil(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{
			map[string]any{"id": "old-1", "object": "model"},
			map[string]any{"id": "old-2", "object": "model"},
		}})
	}))
	defer srv.Close()
	u := NewCustomUpstream("old", srv.URL, []string{"k"}, nil, nil, srv.Client().Transport.(*http.Transport))
	got, err := u.ListModels(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 || got[0].ID != "old-1" || got[1].ID != "old-2" {
		t.Fatalf("expected whole catalog in order, got %v", got)
	}
	if !u.Match("old-2") {
		t.Fatal("expected legacy match after fetch")
	}
}

// TestCustom_ListModels_NewSelectedAppears is a regression test for
// "adding new model in custom provider, not showing on list model":
// after widening the selection (e.g. via Rebuild's keep-overwrite), a
// newly selected ID must appear in ListModels/Models even though it was
// absent from the pre-refresh cache.
func TestCustom_ListModels_NewSelectedAppears(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{
			map[string]any{"id": "acme-gpt-1", "object": "model"},
			map[string]any{"id": "acme-new-1", "object": "model"},
		}})
	}))
	defer srv.Close()
	u := NewCustomUpstream("acme", srv.URL, []string{"k"}, nil,
		[]string{"acme-gpt-1", "acme-new-1"}, srv.Client().Transport.(*http.Transport))
	// Simulate manager Rebuild's keep-overwrite: only the old model survives
	// in cache with fresh metadata, the new bare entry is wiped.
	u.SeedModels([]domain.Model{{ID: "acme-gpt-1", Object: "model", Provider: "custom:acme"}})
	got, err := u.ListModels(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	seen := map[string]bool{}
	for _, m := range got {
		seen[m.ID] = true
	}
	if !seen["acme-gpt-1"] || !seen["acme-new-1"] {
		t.Fatalf("expected both selected models after refresh, got %v", got)
	}
}
func TestCustom_ListModels_IntersectsSelection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{
			map[string]any{"id": "acme-gpt-1", "object": "model"},
			map[string]any{"id": "acme-embed-1", "object": "model"},
		}})
	}))
	defer srv.Close()
	u := NewCustomUpstream("acme", srv.URL, []string{"k"}, nil,
		[]string{"acme-gpt-1", "acme-gone-1"}, srv.Client().Transport.(*http.Transport))
	got, err := u.ListModels(context.Background())
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 selected models in cache, got %v", got)
	}
	if !u.Match("acme-gpt-1") || !u.Match("acme-gone-1") {
		t.Fatal("expected selected models to match after refresh")
	}
	if u.Match("acme-embed-1") {
		t.Fatal("expected unselected catalog model to not match")
	}
	for _, m := range u.Models() {
		if m.Provider != "custom:acme" {
			t.Fatalf("expected provider attribution, got %+v", m)
		}
	}
}

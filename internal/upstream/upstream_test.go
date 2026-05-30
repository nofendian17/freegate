package upstream

import (
	"context"
	"net/http"
	"testing"

	"freegate/internal/model"
)

type mockUpstream struct {
	name   string
	match  func(string) bool
	models []model.Model
}

func (m *mockUpstream) Name() string                                          { return m.name }
func (m *mockUpstream) Match(modelID string) bool                             { return m.match(modelID) }
func (m *mockUpstream) ListModels(ctx context.Context) ([]model.Model, error) { return nil, nil }
func (m *mockUpstream) ChatCompletion(ctx context.Context, body []byte) (*http.Response, error) {
	return nil, nil
}
func (m *mockUpstream) Models() []model.Model { return m.models }
func (m *mockUpstream) Start(ctx context.Context)  {}

func TestRouter_Select_Match(t *testing.T) {
	kilo := &mockUpstream{
		name:  "kilo",
		match: func(id string) bool { return id == "kilo-auto/free" },
	}
	oc := &mockUpstream{
		name:  "opencode",
		match: func(id string) bool { return true },
	}

	r := NewRouter(oc, kilo)
	u := r.Select("kilo-auto/free")
	if u.Name() != "kilo" {
		t.Fatalf("expected kilo, got %s", u.Name())
	}
}

func TestRouter_Select_Fallback(t *testing.T) {
	kilo := &mockUpstream{
		name:  "kilo",
		match: func(id string) bool { return false },
	}
	oc := &mockUpstream{
		name:  "opencode",
		match: func(id string) bool { return true },
	}

	r := NewRouter(oc, kilo)
	u := r.Select("unknown-model")
	if u.Name() != "opencode" {
		t.Fatalf("expected opencode, got %s", u.Name())
	}
}

func TestRouter_AllModels_Dedup(t *testing.T) {
	kilo := &mockUpstream{
		name: "kilo",
		models: []model.Model{
			{ID: "model-a", OwnedBy: "kilo"},
			{ID: "model-b", OwnedBy: "kilo"},
		},
	}
	oc := &mockUpstream{
		name: "opencode",
		models: []model.Model{
			{ID: "model-a", OwnedBy: "opencode"},
			{ID: "model-c", OwnedBy: "opencode"},
		},
	}

	r := NewRouter(oc, kilo)
	models := r.AllModels()

	if len(models) != 3 {
		t.Fatalf("expected 3 models after dedup, got %d", len(models))
	}

	// kilo should take priority for model-a
	seen := make(map[string]bool)
	for _, m := range models {
		if seen[m.ID] {
			t.Fatalf("duplicate found: %s", m.ID)
		}
		seen[m.ID] = true
	}
}

func TestRouter_IsReady_AllEmpty(t *testing.T) {
	kilo := &mockUpstream{name: "kilo", models: nil}
	oc := &mockUpstream{name: "opencode", models: nil}

	r := NewRouter(oc, kilo)
	if r.IsReady() {
		t.Fatal("expected not ready when all empty")
	}
}

func TestRouter_IsReady_KiloReady(t *testing.T) {
	kilo := &mockUpstream{
		name:   "kilo",
		models: []model.Model{{ID: "model-a"}},
	}
	oc := &mockUpstream{name: "opencode", models: nil}

	r := NewRouter(oc, kilo)
	if !r.IsReady() {
		t.Fatal("expected ready when kilo has models")
	}
}

func TestRouter_IsReady_DefaultReady(t *testing.T) {
	kilo := &mockUpstream{name: "kilo", models: nil}
	oc := &mockUpstream{
		name:   "opencode",
		models: []model.Model{{ID: "model-a"}},
	}

	r := NewRouter(oc, kilo)
	if !r.IsReady() {
		t.Fatal("expected ready when opencode has models")
	}
}

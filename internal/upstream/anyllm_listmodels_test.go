package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/mozilla-ai/any-llm-go/providers"
)

func TestAnyLLMProvider_ListModels_OpenCodeFreeFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object": "list",
			"data": [
				{"id": "free-model-1", "object": "model", "created": 1, "owned_by": "opencode"},
				{"id": "free-model-2", "object": "model", "created": 1, "owned_by": "opencode"},
				{"id": "deepseek-v4-flash-free", "object": "model", "created": 1, "owned_by": "opencode"},
				{"id": "paid-model", "object": "model", "created": 1, "owned_by": "opencode"}
			]
		}`))
	}))
	defer srv.Close()

	openCodeFree := func(m providers.Model) bool {
		return strings.HasSuffix(m.ID, "-free")
	}

	p, err := newAnyLLMProvider("opencode", srv.URL, "test-key", "", nil, nil, openCodeFree)
	if err != nil {
		t.Fatalf("newAnyLLMProvider: %v", err)
	}
	got, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	want := []string{"deepseek-v4-flash-free"}
	if len(got) != len(want) {
		t.Fatalf("got %d models, want %d: %+v", len(got), len(want), got)
	}
	for i, m := range got {
		if m.ID != want[i] {
			t.Errorf("model[%d].ID = %q, want %q", i, m.ID, want[i])
		}
		if m.Provider != "opencode" {
			t.Errorf("model[%d].Provider = %q, want %q", i, m.Provider, "opencode")
		}
		if !m.IsFree {
			t.Errorf("model[%d].IsFree = false, want true", i)
		}
	}
}

func TestAnyLLMProvider_ListModels_KiloFreeFilter(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"object": "list",
			"data": [
				{"id": "kilo-free-1", "object": "model", "created": 1, "owned_by": "kilo"},
				{"id": "kilo-free-2", "object": "model", "created": 1, "owned_by": "kilo"},
				{"id": "kilo-paid-1", "object": "model", "created": 1, "owned_by": "kilo"}
			]
		}`))
	}))
	defer srv.Close()

	kiloFree := func(m providers.Model) bool { return strings.Contains(m.ID, "free") }

	p, err := newAnyLLMProvider("kilo", srv.URL, "test-key", "", nil, []string{"kilo/"}, kiloFree)
	if err != nil {
		t.Fatalf("newAnyLLMProvider: %v", err)
	}
	got, err := p.ListModels(context.Background())
	if err != nil {
		t.Fatalf("ListModels: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d models, want 2: %+v", len(got), got)
	}
	for _, m := range got {
		if m.Provider != "kilo" {
			t.Errorf("model %q has Provider %q, want %q", m.ID, m.Provider, "kilo")
		}
	}
}

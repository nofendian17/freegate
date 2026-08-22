package upstream

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"freegate/internal/infrastructure/upstream/types"
	"freegate/internal/model"
)

func newTestLLM7(t *testing.T, body string) *LLM7Upstream {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	u := NewLLM7Upstream(srv.URL, nil)
	u.client = NewHTTPClient(srv.URL, []string{"unused"}, nil, nil)
	return u
}

func TestLLM7Free_UsageBasedOnlyTakesPrecedence(t *testing.T) {
	tru := true
	fals := false
	tests := []struct {
		name string
		m    types.LLM7Model
		want bool
	}{
		{"usage_based_only false = free regardless of tier", types.LLM7Model{ID: "a", UsageBasedOnly: &fals, Tier: "other"}, true},
		{"usage_based_only true = not free regardless of tier", types.LLM7Model{ID: "b", UsageBasedOnly: &tru, Tier: "turbo"}, false},
		{"usage_based_only nil + turbo = free", types.LLM7Model{Tier: "turbo"}, true},
		{"usage_based_only nil + Turbo case-insensitive", types.LLM7Model{Tier: "Turbo"}, true},
		{"usage_based_only nil + TURBO case-insensitive", types.LLM7Model{Tier: "TURBO"}, true},
		{"usage_based_only nil + other tier = not free", types.LLM7Model{Tier: "other"}, false},
		{"usage_based_only nil + empty tier = not free", types.LLM7Model{Tier: ""}, false},
	}
	for _, tt := range tests {
		got := llm7Free(tt.m)
		if got != tt.want {
			t.Errorf("%s: llm7Free = %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestLLM7_ListModels_FiltersFree(t *testing.T) {
	body := `{"object":"list","data":[
		{"id":"model-turbo","tier":"turbo"},
		{"id":"model-turbo-upper","tier":"Turbo"},
		{"id":"model-free-flag","usage_based_only":false,"tier":"other"},
		{"id":"model-paid-flag","usage_based_only":true,"tier":"turbo"},
		{"id":"model-paid-tier","tier":"paid"},
		{"id":"model-empty-tier","tier":""}
	]}`
	u := newTestLLM7(t, body)

	models, err := u.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got := make(map[string]bool, len(models))
	for _, m := range models {
		if !m.IsFree {
			t.Errorf("expected IsFree=true for %s", m.ID)
		}
		if m.Provider != "llm7" {
			t.Errorf("expected Provider=llm7 for %s, got %q", m.ID, m.Provider)
		}
		got[m.ID] = true
	}

	for _, want := range []string{"model-turbo", "model-turbo-upper", "model-free-flag"} {
		if !got[want] {
			t.Errorf("expected %s in free list", want)
		}
	}
	for _, notWant := range []string{"model-paid-flag", "model-paid-tier", "model-empty-tier"} {
		if got[notWant] {
			t.Errorf("did not expect %s in free list", notWant)
		}
	}
}

func TestLLM7_ListModels_Dedup(t *testing.T) {
	body := `{"object":"list","data":[
		{"id":"x","tier":"turbo"},
		{"id":"x","tier":"turbo"},
		{"id":"y","tier":"turbo"}
	]}`
	u := newTestLLM7(t, body)

	models, err := u.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 2 {
		t.Errorf("expected 2 unique models after dedup, got %d", len(models))
	}
}

func TestLLM7_ListModels_Empty(t *testing.T) {
	u := newTestLLM7(t, `{"object":"list","data":[]}`)

	models, err := u.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 0 {
		t.Errorf("expected empty list, got %d", len(models))
	}
}

func TestLLM7_ListModels_InvalidJSON(t *testing.T) {
	u := newTestLLM7(t, `not-json`)

	_, err := u.ListModels(context.Background())
	if err == nil {
		t.Fatal("expected error for invalid JSON, got nil")
	}
}

func TestLLM7_ListModels_IgnoresExtraFields(t *testing.T) {
	// Catalog returns richer objects; decoder must ignore unknown fields.
	body := `{"object":"list","data":[
		{"id":"m1","tier":"turbo","reasoning":true,"context_window":{"tokens":128000},"modalities":{"input":["text","image"]}},
		{"id":"m2","usage_based_only":false,"reasoning":false,"extra_unknown":"foo"}
	]}`
	u := newTestLLM7(t, body)

	models, err := u.ListModels(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
}

func TestLLM7_Match_ByCache(t *testing.T) {
	u := NewLLM7Upstream("", nil)
	u.cache.Set([]model.Model{
		{ID: "model-a", Provider: "llm7"},
		{ID: "model-b", Provider: "llm7"},
	})

	tests := []struct {
		modelID string
		want    bool
	}{
		{"model-a", true},
		{"model-b", true},
		{"model-c", false},
		{"", false},
	}
	for _, tt := range tests {
		got := u.Match(tt.modelID)
		if got != tt.want {
			t.Errorf("Match(%q) = %v, want %v", tt.modelID, got, tt.want)
		}
	}
}

func TestLLM7_Match_EmptyCache(t *testing.T) {
	u := NewLLM7Upstream("", nil)

	if u.Match("model-a") {
		t.Error("expected no match when cache is empty")
	}
	if u.Match("") {
		t.Error("expected no match for empty modelID even with empty cache")
	}
}

func TestLLM7_Name(t *testing.T) {
	u := NewLLM7Upstream("", nil)
	if got := u.Name(); got != "llm7" {
		t.Errorf("Name() = %q, want %q", got, "llm7")
	}
}

package upstream

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestCustom_Match_AllowBlock(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{"object": "list", "data": []any{
			map[string]any{"id": "acme-gpt-1", "object": "model"},
			map[string]any{"id": "acme-embed-1", "object": "model"},
		}})
	}))
	defer srv.Close()
	u := NewCustomUpstream("acme", srv.URL, []string{"k"}, nil, []string{"gpt"}, []string{"embed"}, srv.Client().Transport.(*http.Transport))
	if _, err := u.ListModels(context.Background()); err != nil {
		t.Fatalf("list: %v", err)
	}
	if !u.Match("acme-gpt-1") {
		t.Fatal("expected gpt model to match")
	}
	if u.Match("acme-embed-1") {
		t.Fatal("expected blocked model to not match")
	}
}

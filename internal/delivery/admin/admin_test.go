package admin

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"freegate/internal/domain"
	"freegate/internal/infrastructure/providers"
	"github.com/go-chi/chi/v5"
)

func TestAdmin_CreateProvider_TriggersRebuild(t *testing.T) {
	s, _ := providers.Open("file::memory:?cache=shared")
	rebuilt := 0
	h := New(s, func() error { rebuilt++; return nil }, nil, nil)
	r := chi.NewRouter()
	r.Mount("/", h.Routes())
	body, _ := json.Marshal(map[string]any{"name": "acme", "base_url": "https://api.acme.test/v1", "api_keys": []string{"sk-1"}, "refresh_sec": 60, "enabled": true})
	req := httptest.NewRequest("POST", "/api/providers", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if rebuilt != 1 {
		t.Fatalf("expected rebuild once, got %d", rebuilt)
	}
	var _ domain.Upstream
}

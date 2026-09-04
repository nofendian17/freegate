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
	s, _ := providers.Open("file:admin-create?mode=memory&cache=shared")
	rebuilt := 0
	h := New(s, func() error { rebuilt++; return nil }, nil)
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

func TestAdmin_UpdateProvider_BlankKeys_KeepsExisting(t *testing.T) {
	s, _ := providers.Open("file:admin-keepkeys?mode=memory&cache=shared")
	row, err := s.CreateProvider(providers.Provider{Name: "keepme", BaseURL: "https://api.keep.test/v1", APIKeys: []string{"sk-live-abc"}, RefreshSec: 60, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	h := New(s, func() error { return nil }, nil)
	r := chi.NewRouter()
	r.Mount("/", h.Routes())
	body, _ := json.Marshal(map[string]any{"name": "keepme", "base_url": "https://api.keep.test/v1", "refresh_sec": 60, "enabled": true})
	req := httptest.NewRequest("PUT", "/api/providers/1", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	raw, err := s.GetProviderRaw(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw.APIKeys) != 1 || raw.APIKeys[0] != "sk-live-abc" {
		t.Fatalf("keys not preserved: %q", raw.APIKeys)
	}
}

func TestAdmin_TestProvider_BadBaseURL_ReturnsOkFalse(t *testing.T) {
	s, err := providers.Open("file:admin-probe?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	row, err := s.CreateProvider(providers.Provider{Name: "bad", BaseURL: "https://api.test/v1", APIKeys: []string{"sk-1"}, RefreshSec: 60, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	bad, _ := s.GetProviderRaw(row.ID)
	bad.BaseURL = "http://exa mple.com\x7f"
	if _, err := s.UpdateProvider(row.ID, bad); err != nil {
		t.Fatal(err)
	}
	h := New(s, func() error { return nil }, nil)
	r := chi.NewRouter()
	r.Mount("/", h.Routes())
	req := httptest.NewRequest("POST", "/api/providers/1/test", nil)
	w := httptest.NewRecorder()
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("panicked: %v", rec)
		}
	}()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["ok"] != false {
		t.Fatalf("expected ok=false, got %v", got)
	}
}

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

func TestAdmin_CreateCombo_Tiers(t *testing.T) {
	s, _ := providers.Open("file:adcombo?mode=memory&cache=shared")
	h := New(s, func() error { return nil }, nil)
	r := chi.NewRouter()
	r.Mount("/", h.Routes())
	body, _ := json.Marshal(map[string]any{"name": "hemat", "tiers": []any{
		map[string]any{"provider": "opencode"},
		map[string]any{"provider": "kilo"},
	}})
	req := httptest.NewRequest("POST", "/api/combos", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got struct {
		Tiers []providers.ComboTier `json:"tiers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil || len(got.Tiers) != 2 {
		t.Fatalf("tiers not echoed: %v %s", err, w.Body.String())
	}
}

func TestAdmin_TestCombo_PerTier(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"},{"id":"m2"}]}`))
	}))
	defer srv.Close()
	s, _ := providers.Open("file:adcombo-test?mode=memory&cache=shared")
	if _, err := s.CreateProvider(providers.Provider{Name: "probe-me", BaseURL: srv.URL, APIKeys: []string{"sk-1"}, RefreshSec: 60, Enabled: true}); err != nil {
		t.Fatal(err)
	}
	combo, err := s.SaveCombo(providers.RouteCombo{Name: "mix", Tiers: []providers.ComboTier{{Provider: "custom:probe-me"}, {Provider: "opencode"}}})
	if err != nil {
		t.Fatal(err)
	}
	h := New(s, func() error { return nil }, nil)
	r := chi.NewRouter()
	r.Mount("/", h.Routes())
	req := httptest.NewRequest("POST", "/api/combos/"+itoa(combo.ID)+"/test", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got struct {
		Ok    bool `json:"ok"`
		Tiers []struct {
			Provider   string `json:"provider"`
			Ok         bool   `json:"ok"`
			ModelCount int    `json:"modelCount"`
			Skipped    bool   `json:"skipped"`
		} `json:"tiers"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v body=%s", err, w.Body.String())
	}
	if len(got.Tiers) != 2 {
		t.Fatalf("expected 2 tiers, got %s", w.Body.String())
	}
	if !got.Ok || !got.Tiers[0].Ok || got.Tiers[0].ModelCount != 2 {
		t.Fatalf("custom tier not probed: %s", w.Body.String())
	}
	if !got.Tiers[1].Skipped || !got.Tiers[1].Ok {
		t.Fatalf("builtin tier not skipped: %s", w.Body.String())
	}
}

func itoa(v uint) string {
	if v == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
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

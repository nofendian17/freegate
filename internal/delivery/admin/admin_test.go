package admin

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"freegate/internal/domain"
	"freegate/internal/infrastructure/providers"
	"freegate/internal/infrastructure/upstream"
	"github.com/go-chi/chi/v5"
)

var errRebuildSentinel = errors.New("rebuild boom")

// testRouter wires the admin endpoints onto a fresh router. It is test
// scaffolding: production mounts via Handler.Register in server.go.
func testRouter(h *Handler) chi.Router {
	r := chi.NewRouter()
	h.Register(r)
	return r
}

func TestAdmin_CustomProviderLifecycle(t *testing.T) {
	s, err := providers.Open(t.TempDir() + "/providers.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	rebuilt := 0
	h := New(s, func() error { rebuilt++; return nil }, nil)
	r := testRouter(h)
	request := func(method, path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewBufferString(body)))
		if w.Code != status {
			t.Fatalf("%s %s: status=%d body=%s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	request("POST", "/api/providers", `{"name":"default-on","base_url":"https://example.test/v1","api_keys":["test-key"]}`, http.StatusCreated)
	defaulted, err := s.GetProviderByName("default-on")
	if err != nil || !defaulted.Enabled {
		t.Fatalf("omitted enabled should default true: %+v, %v", defaulted, err)
	}
	w := request("POST", "/api/providers", `{"name":"before","base_url":"https://example.test/v1","api_keys":[],"enabled":false}`, http.StatusCreated)
	var created providers.Provider
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	if created.Enabled {
		t.Fatal("response enabled a disabled provider")
	}
	raw, err := s.GetProviderRaw(created.ID)
	if err != nil || raw.Enabled {
		t.Fatalf("disabled provider not persisted: %+v, %v", raw, err)
	}
	path := "/api/providers/" + strconv.FormatUint(uint64(created.ID), 10)
	request("PUT", path, `{"name":"before","base_url":"https://example.test/v1","api_keys":["test-key"],"enabled":true}`, http.StatusOK)
	request("POST", "/api/combos", `{"name":"mixed","tiers":[{"provider":"custom:before","model":"pinned"},{"provider":"opencode"}]}`, http.StatusCreated)
	request("POST", "/api/combos", `{"name":"solo","tiers":[{"provider":"custom:before"}]}`, http.StatusCreated)
	request("PUT", path, `{"name":"after","base_url":"https://example.test/v1","enabled":true}`, http.StatusOK)
	combos, err := s.ListCombos()
	if err != nil {
		t.Fatal(err)
	}
	if len(combos) != 2 || combos[0].Tiers[0].Provider != "custom:after" || combos[0].Tiers[0].Model != "pinned" || combos[1].Tiers[0].Provider != "custom:after" {
		t.Fatalf("rename lost combo references: %+v", combos)
	}
	raw, err = s.GetProviderRaw(created.ID)
	if err != nil || len(raw.APIKeys) != 1 || raw.APIKeys[0] != "test-key" {
		t.Fatalf("rename lost keys: %+v, %v", raw, err)
	}
	request("DELETE", path, "", http.StatusNoContent)
	combos, err = s.ListCombos()
	if err != nil || len(combos) != 1 || combos[0].Name != "mixed" || len(combos[0].Tiers) != 1 || combos[0].Tiers[0].Provider != "opencode" {
		t.Fatalf("delete did not clean up combos: %+v, %v", combos, err)
	}
	request("GET", path, "", http.StatusNotFound)
	if rebuilt != 7 {
		t.Fatalf("rebuilds=%d, want 7", rebuilt)
	}
}

func TestAdmin_CreateProvider_TriggersRebuild(t *testing.T) {
	s, _ := providers.Open("file:admin-create?mode=memory&cache=shared")
	rebuilt := 0
	h := New(s, func() error { rebuilt++; return nil }, nil)
	r := chi.NewRouter()
	r.Mount("/", testRouter(h))
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

func TestAdmin_PoolLifecycle(t *testing.T) {
	s, err := providers.Open(t.TempDir() + "/providers.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	rebuilt := 0
	h := New(s, func() error { rebuilt++; return nil }, nil)
	r := testRouter(h)
	request := func(method, path, body string, status int) *httptest.ResponseRecorder {
		t.Helper()
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest(method, path, bytes.NewBufferString(body)))
		if w.Code != status {
			t.Fatalf("%s %s: status=%d body=%s", method, path, w.Code, w.Body.String())
		}
		return w
	}
	w := request("POST", "/api/pools", `{"name":"relay-1","proxy_url":"https://relay-1.example.com"}`, http.StatusCreated)
	var created providers.ProxyPool
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil || created.ID == 0 {
		t.Fatalf("create pool: %v %s", err, w.Body.String())
	}
	request("POST", "/api/pools", `{"name":"bad","proxy_url":"not-a-url"}`, http.StatusBadRequest)
	path := "/api/pools/" + strconv.FormatUint(uint64(created.ID), 10)
	request("GET", path, "", http.StatusOK)
	request("PUT", path, `{"name":"relay-1","proxy_url":"https://relay-2.example.com","no_proxy":"example.com"}`, http.StatusOK)
	got, err := s.GetPool(created.ID)
	if err != nil || got.ProxyURL != "https://relay-2.example.com" || got.NoProxy != "example.com" {
		t.Fatalf("update not persisted: %v %+v", err, got)
	}
	request("DELETE", path, "", http.StatusNoContent)
	request("GET", path, "", http.StatusNotFound)
	if rebuilt != 3 {
		t.Fatalf("rebuilds=%d, want 3", rebuilt)
	}
}

func TestAdmin_PoolTest_DisablesDeadRelay(t *testing.T) {
	s, err := providers.Open(t.TempDir() + "/providers.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	row, err := s.CreatePool(providers.ProxyPool{Name: "dead-relay", ProxyURL: "http://127.0.0.1:1", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	h := New(s, func() error { return nil }, nil)
	r := testRouter(h)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/pools/"+strconv.FormatUint(uint64(row.ID), 10)+"/test", nil))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var out struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil || out.OK {
		t.Fatalf("expected ok=false, got %v %s", out, w.Body.String())
	}
	got, err := s.GetPool(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.Enabled {
		t.Error("dead relay must be disabled after failed test")
	}
	if got.TestStatus != "error" || got.LastError == "" {
		t.Errorf("expected error status persisted, got %+v", got)
	}
}

func TestAdmin_VercelDeploy_RequiresToken(t *testing.T) {	s, err := providers.Open(t.TempDir() + "/providers.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	h := New(s, func() error { return nil }, nil)
	r := testRouter(h)
	for _, body := range []string{`{}`, `{"project_name":"relay-1"}`, `{"vercel_token":"","project_name":"relay-1"}`} {
		w := httptest.NewRecorder()
		r.ServeHTTP(w, httptest.NewRequest("POST", "/api/pools/vercel-deploy", bytes.NewBufferString(body)))
		if w.Code != http.StatusBadRequest {
			t.Fatalf("body %s: status=%d, want 400 (%s)", body, w.Code, w.Body.String())
		}
	}
}

func TestAdmin_UpdateProvider_BlankKeys_KeepsExisting(t *testing.T) {
	s, _ := providers.Open("file:admin-keepkeys?mode=memory&cache=shared")
	row, err := s.CreateProvider(providers.Provider{Name: "keepme", BaseURL: "https://api.keep.test/v1", APIKeys: []string{"sk-live-abc"}, RefreshSec: 60, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	h := New(s, func() error { return nil }, nil)
	r := chi.NewRouter()
	r.Mount("/", testRouter(h))
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
	r.Mount("/", testRouter(h))
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
	r.Mount("/", testRouter(h))
	req := httptest.NewRequest("POST", "/api/combos/"+strconv.FormatUint(uint64(combo.ID), 10)+"/test", nil)
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

func TestAdmin_DeleteCombo_TriggersRebuild(t *testing.T) {
	s, _ := providers.Open("file:admin-delcombo?mode=memory&cache=shared")
	rebuilt := 0
	var rebuildErr error
	h := New(s, func() error { rebuilt++; return rebuildErr }, nil)
	r := chi.NewRouter()
	r.Mount("/", testRouter(h))
	body, _ := json.Marshal(map[string]any{"name": "gone", "tiers": []any{
		map[string]any{"provider": "opencode"},
	}})
	req := httptest.NewRequest("POST", "/api/combos", bytes.NewReader(body))
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", w.Code, w.Body.String())
	}
	var created struct {
		ID uint `json:"id"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if rebuilt != 1 {
		t.Fatalf("expected rebuild after create, got %d", rebuilt)
	}
	req = httptest.NewRequest("DELETE", "/api/combos/"+strconv.FormatUint(uint64(created.ID), 10), nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusNoContent {
		t.Fatalf("delete status=%d body=%s", w.Code, w.Body.String())
	}
	if rebuilt != 2 {
		t.Fatalf("expected rebuild after delete, got %d", rebuilt)
	}
	req = httptest.NewRequest("GET", "/api/combos", nil)
	w = httptest.NewRecorder()
	r.ServeHTTP(w, req)
	var list struct {
		Data []providers.RouteCombo `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	for _, c := range list.Data {
		if c.ID == created.ID {
			t.Fatalf("deleted combo still listed: %+v", c)
		}
	}
}

func TestAdmin_DeleteCombo_RebuildError(t *testing.T) {
	s, _ := providers.Open("file:admin-delcombo-err?mode=memory&cache=shared")
	combo, err := s.SaveCombo(providers.RouteCombo{Name: "gone", Tiers: []providers.ComboTier{{Provider: "opencode"}}})
	if err != nil {
		t.Fatal(err)
	}
	h := New(s, func() error { return errRebuildSentinel }, nil)
	r := chi.NewRouter()
	r.Mount("/", testRouter(h))
	req := httptest.NewRequest("DELETE", "/api/combos/"+strconv.FormatUint(uint64(combo.ID), 10), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

// TestAdmin_CreateProvider_WarmsCatalog is a regression test for the
// reported bug: right after adding a custom provider, chat requests for
// its models were routed to the default upstream because the new live
// upstream started with an empty catalog (Match requires cache.Has) and
// the background refresher hadn't ticked yet. Create must warm the
// catalog synchronously so Match succeeds immediately.
func TestAdmin_CreateProvider_WarmsCatalog(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"acme-model","object":"model"}]}`))
	}))
	defer fake.Close()

	s, err := providers.Open(t.TempDir() + "/providers.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	tr := http.DefaultTransport.(*http.Transport).Clone()
	mgr := upstream.NewProviderManager(s, tr)
	h := New(s, mgr.Rebuild, nil).WithWarmer(mgr.Warm)
	r := testRouter(h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/providers",
		bytes.NewBufferString(`{"name":"acme","base_url":"`+fake.URL+`","api_keys":["sk-1"],"models":["acme-model"]}`)))
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	// No refresher is running: routing must still hit the new provider.
	var live *upstream.CustomUpstream
	for _, u := range mgr.All() {
		if u.Name() == "custom:acme" {
			live = u
		}
	}
	if live == nil {
		t.Fatal("expected live custom:acme upstream after rebuild")
	}
	if !live.Match("acme-model") {
		t.Fatal("new provider must Match its catalog model immediately after create (else chat falls through to default upstream)")
	}
}

// TestAdmin_TestProvider_WarmsCatalog verifies the probe button also
// seeds the live catalog: a provider added while the upstream was down
// (empty cache) routes correctly right after a successful manual test.
func TestAdmin_TestProvider_WarmsCatalog(t *testing.T) {
	s, err := providers.Open(t.TempDir() + "/providers.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	mgr := upstream.NewProviderManager(s, http.DefaultTransport.(*http.Transport).Clone())
	h := New(s, mgr.Rebuild, nil).WithWarmer(mgr.Warm)
	r := testRouter(h)

	// Register first while the upstream is unreachable: create warms
	// best-effort and must still return 201.
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/providers",
		bytes.NewBufferString(`{"name":"late","base_url":"http://127.0.0.1:1","api_keys":["sk-1"]}`)))
	if w.Code != http.StatusCreated {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	row, err := s.GetProviderByName("late")
	if err != nil {
		t.Fatal(err)
	}

	// Bring a fake upstream up at a new URL and point the provider at it
	// via update (stale seed from the old config must be replaced).
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"id":"late-model","object":"model"}]}`))
	}))
	defer fake.Close()
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("PUT", "/api/providers/"+strconv.FormatUint(uint64(row.ID), 10),
		bytes.NewBufferString(`{"name":"late","base_url":"`+fake.URL+`","api_keys":["sk-1"],"models":["late-model"],"enabled":true}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var live *upstream.CustomUpstream
	for _, u := range mgr.All() {
		if u.Name() == "custom:late" {
			live = u
		}
	}
	if live == nil {
		t.Fatal("expected live custom:late upstream after update")
	}
	if !live.Match("late-model") {
		t.Fatal("updated provider must Match its new catalog model immediately after update")
	}
}

// TestAdmin_ProbeProvider_AdHoc verifies the pre-save probe: it lists the
// upstream catalog from form values without storing anything.
func TestAdmin_ProbeProvider_AdHoc(t *testing.T) {
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/models" {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write([]byte(`{"data":[{"id":"m1","object":"model"},{"id":"m2"}]}`))
	}))
	defer fake.Close()

	s, err := providers.Open(t.TempDir() + "/providers.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	h := New(s, func() error { return nil }, nil)
	r := testRouter(h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/providers/probe",
		bytes.NewBufferString(`{"base_url":"`+fake.URL+`","api_keys":["sk-1"]}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["ok"] != true {
		t.Fatalf("expected ok=true, got %v", got)
	}
	models, _ := got["models"].([]any)
	if len(models) != 2 || models[0] != "m1" || models[1] != "m2" {
		t.Fatalf("expected [m1 m2], got %v", got["models"])
	}
	rows, err := s.ListProviders()
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 0 {
		t.Fatalf("probe must not store anything, got %d providers", len(rows))
	}

	// Invalid URL is a 200 ok=false, not a 400: same contract as /test.
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/providers/probe",
		bytes.NewBufferString(`{"base_url":"not-a-url"}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	got = nil
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["ok"] != false {
		t.Fatalf("expected ok=false, got %v", got)
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
	r.Mount("/", testRouter(h))
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

// TestAdmin_UpdateProvider_OmitModels_KeepsSelection verifies omit-vs-clear:
// a PUT without the models key preserves the stored selection, while an
// explicit array (even []) overwrites it.
func TestAdmin_UpdateProvider_OmitModels_KeepsSelection(t *testing.T) {
	s, err := providers.Open("file:admin-omitmodels?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	row, err := s.CreateProvider(providers.Provider{Name: "sel", BaseURL: "https://api.sel.test/v1", APIKeys: []string{"sk-1"}, Models: []string{"m1", "m2"}, RefreshSec: 60, Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	h := New(s, func() error { return nil }, nil)
	r := chi.NewRouter()
	r.Mount("/", testRouter(h))
	put := func(body string) {
		t.Helper()
		req := httptest.NewRequest("PUT", "/api/providers/1", bytes.NewBufferString(body))
		w := httptest.NewRecorder()
		r.ServeHTTP(w, req)
		if w.Code != http.StatusOK {
			t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
		}
	}
	put(`{"name":"sel","base_url":"https://api.sel.test/v1","api_keys":["sk-1"],"refresh_sec":60,"enabled":true}`)
	raw, err := s.GetProviderRaw(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(raw.Models) != 2 || raw.Models[0] != "m1" || raw.Models[1] != "m2" {
		t.Fatalf("omitted models must be preserved, got %v", raw.Models)
	}
	put(`{"name":"sel","base_url":"https://api.sel.test/v1","api_keys":["sk-1"],"models":[],"refresh_sec":60,"enabled":true}`)
	raw, err = s.GetProviderRaw(row.ID)
	if err != nil {
		t.Fatal(err)
	}
	if raw.Models == nil || len(raw.Models) != 0 {
		t.Fatalf("explicit [] must clear the selection, got %v (nil=%v)", raw.Models, raw.Models == nil)
	}
}

// TestAdmin_Probe_ForwardsHeaders verifies the probe sends configured
// custom headers, so header-authenticated upstreams probe successfully.
func TestAdmin_Probe_ForwardsHeaders(t *testing.T) {
	var gotHeader string
	fake := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotHeader = r.Header.Get("X-Custom-Auth")
		_, _ = w.Write([]byte(`{"data":[{"id":"h1"}]}`))
	}))
	defer fake.Close()

	s, err := providers.Open(t.TempDir() + "/providers.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	h := New(s, func() error { return nil }, nil)
	r := testRouter(h)

	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("POST", "/api/providers/probe",
		bytes.NewBufferString(`{"base_url":"`+fake.URL+`","api_keys":[],"headers":{"X-Custom-Auth":"secret-1"}}`)))
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var got map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if got["ok"] != true {
		t.Fatalf("expected ok=true, got %v", got)
	}
	if gotHeader != "secret-1" {
		t.Fatalf("expected custom header forwarded, got %q", gotHeader)
	}
}

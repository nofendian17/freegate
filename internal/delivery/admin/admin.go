package admin

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"

	"freegate/internal/delivery/respond"
	"freegate/internal/domain"
	"freegate/internal/infrastructure/providers"
)

type Handler struct {
	store     *providers.Store
	rebuild   func() error
	transport *http.Transport
	// warm synchronously loads one custom provider's model catalog so it
	// routes immediately after add/update/test. Nil in tests that don't
	// wire a ProviderManager; all uses are best-effort.
	warm func(name string) ([]domain.Model, error)
	// inflight guards warmCache: one best-effort fetch per provider at a
	// time, so double-click storms don't stack goroutines.
	mu       sync.Mutex
	inflight map[string]struct{}
}

func New(store *providers.Store, rebuild func() error, transport *http.Transport) *Handler {
	return &Handler{store: store, rebuild: rebuild, transport: transport}
}

// WithWarmer attaches the catalog warmer (ProviderManager.Warm).
// Follows the existing WithX setter pattern (cf. ChatService).
func (h *Handler) WithWarmer(fn func(name string) ([]domain.Model, error)) *Handler {
	h.warm = fn
	return h
}

// warmCache best-effort loads the provider's catalog metadata into the
// live upstream object. It runs async so admin saves never block on
// upstream latency (up to Warm's 10s timeout): routing already works from
// the stored selection, which seeds the cache synchronously at rebuild;
// this only refreshes display metadata (object/created/owned_by).
func (h *Handler) warmCache(name string) {
	fn := h.warm
	if fn == nil || name == "" {
		return
	}
	h.mu.Lock()
	if h.inflight == nil {
		h.inflight = map[string]struct{}{}
	}
	if _, dup := h.inflight[name]; dup {
		h.mu.Unlock()
		return
	}
	h.inflight[name] = struct{}{}
	h.mu.Unlock()
	go func() {
		defer func() {
			h.mu.Lock()
			delete(h.inflight, name)
			h.mu.Unlock()
		}()
		if _, err := fn(name); err != nil {
			slog.Warn("custom provider warm failed, metadata refreshes on next tick", "provider", name, "error", err)
		}
	}()
}

func (h *Handler) Register(r chi.Router) {
	r.Get("/api/providers", h.listProviders)
	r.Post("/api/providers", h.createProvider)
	r.Get("/api/providers/{id}", h.getProvider)
	r.Put("/api/providers/{id}", h.updateProvider)
	r.Delete("/api/providers/{id}", h.deleteProvider)
	r.Post("/api/providers/{id}/test", h.testProvider)
	// Ad-hoc probe for the new-provider modal: same /models probe as
	// testProvider but against form values, storing nothing. Lets the
	// user pick models via checkboxes before the first save.
	r.Post("/api/providers/probe", h.probeProvider)
	r.Get("/api/combos", h.listCombos)
	r.Post("/api/combos", h.createCombo)
	r.Put("/api/combos/{id}", h.updateCombo)
	r.Delete("/api/combos/{id}", h.deleteCombo)
	r.Post("/api/combos/{id}/test", h.testCombo)
}

type providerIn struct {
	Name    string            `json:"name"`
	BaseURL string            `json:"base_url"`
	APIKeys []string          `json:"api_keys"`
	Headers map[string]string `json:"headers"`
	// Models is the explicit curated selection (checkboxes in the
	// dashboard, populated from the probe). Only these route here.
	// Pointer so omit-vs-clear stays distinct: absent keeps the stored
	// selection (nil preserves a legacy whole-catalog row), present
	// (even []) overwrites it.
	Models     *[]string `json:"models"`
	RefreshSec int       `json:"refresh_sec"`
	Priority   int       `json:"priority"`
	Enabled    bool      `json:"enabled"`
}

// nonEmpty drops blank keys; an update with no keys keeps existing ones.
func nonEmpty(keys []string) []string {
	var out []string
	for _, k := range keys {
		if strings.TrimSpace(k) != "" {
			out = append(out, k)
		}
	}
	return out
}

func (h *Handler) listProviders(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.ListProviders()
	if err != nil {
		respond.JSONError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"data": rows})
}

func (h *Handler) createProvider(w http.ResponseWriter, r *http.Request) {
	in := providerIn{Enabled: true}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	var models []string
	if in.Models != nil {
		models = *in.Models
	}
	row, err := h.store.CreateProvider(providers.Provider{Name: in.Name, BaseURL: in.BaseURL, APIKeys: in.APIKeys, Headers: in.Headers, Models: models, RefreshSec: in.RefreshSec, Priority: in.Priority, Enabled: in.Enabled})
	if err != nil {
		respond.JSONError(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	if err := h.rebuild(); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "rebuild_error", err.Error())
		return
	}
	h.warmCache(in.Name)
	respond.JSON(w, http.StatusCreated, row)
}

func (h *Handler) getProvider(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	row, err := h.store.GetProvider(uint(id))
	if err != nil {
		respond.JSONError(w, http.StatusNotFound, "not_found", "provider not found")
		return
	}
	respond.JSON(w, http.StatusOK, row)
}

func (h *Handler) updateProvider(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	var in providerIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	keys := nonEmpty(in.APIKeys)
	cur, err := h.store.GetProviderRaw(uint(id))
	if err != nil {
		respond.JSONError(w, http.StatusNotFound, "not_found", "provider not found")
		return
	}
	if len(keys) == 0 {
		keys = cur.APIKeys
	}
	// Absent models keeps the stored selection; present overwrites.
	models := cur.Models
	if in.Models != nil {
		models = *in.Models
	}
	row, err := h.store.UpdateProvider(uint(id), providers.Provider{Name: in.Name, BaseURL: in.BaseURL, APIKeys: keys, Headers: in.Headers, Models: models, RefreshSec: in.RefreshSec, Priority: in.Priority, Enabled: in.Enabled})
	if err != nil {
		respond.JSONError(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	if err := h.rebuild(); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "rebuild_error", err.Error())
		return
	}
	h.warmCache(in.Name)
	respond.JSON(w, http.StatusOK, row)
}

func (h *Handler) deleteProvider(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	if err := h.store.DeleteProvider(uint(id)); err != nil {
		respond.JSONError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if err := h.rebuild(); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "rebuild_error", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) testProvider(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	row, err := h.store.GetProviderRaw(uint(id))
	if err != nil {
		respond.JSONError(w, http.StatusNotFound, "not_found", "provider not found")
		return
	}
	ids, status, latencyMs, err := h.probeCatalog(r.Context(), row.BaseURL, row.APIKeys, row.Headers)
	if err != nil {
		respond.JSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	// Seed the live upstream's catalog so a successful probe is
	// immediately followed by correct routing (see warmCache). Note the
	// probe never stores the selection itself: only models the user
	// checks are saved.
	if status < 300 {
		h.warmCache(row.Name)
	}
	respond.JSON(w, http.StatusOK, map[string]any{"ok": status < 300, "modelCount": len(ids), "models": ids, "latencyMs": latencyMs, "status": status})
}

// probeProvider runs the /models probe against ad-hoc form values without
// storing anything, so a new provider's model checklist can be filled
// before the first save.
func (h *Handler) probeProvider(w http.ResponseWriter, r *http.Request) {
	var in struct {
		BaseURL string            `json:"base_url"`
		APIKeys []string          `json:"api_keys"`
		Headers map[string]string `json:"headers"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if u := strings.ToLower(strings.TrimSpace(in.BaseURL)); !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
		respond.JSON(w, http.StatusOK, map[string]any{"ok": false, "error": "base_url must be http(s) URL"})
		return
	}
	ids, status, latencyMs, err := h.probeCatalog(r.Context(), strings.TrimSpace(in.BaseURL), nonEmpty(in.APIKeys), in.Headers)
	if err != nil {
		respond.JSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"ok": status < 300, "modelCount": len(ids), "models": ids, "latencyMs": latencyMs, "status": status})
}

// probeCatalog GETs baseURL/models and returns the deduped model IDs in
// upstream order. Used by both the stored test and the ad-hoc probe.
// Custom headers are sent too (some upstreams need more than a bearer
// key); the first API key takes precedence when both are set.
func (h *Handler) probeCatalog(ctx context.Context, baseURL string, keys []string, headers map[string]string) (ids []string, status int, latencyMs int64, err error) {
	start := time.Now()
	req, err := http.NewRequestWithContext(ctx, "GET", strings.TrimRight(baseURL, "/")+"/models", nil)
	if err != nil {
		return nil, 0, 0, err
	}
	for k, v := range headers {
		if strings.TrimSpace(k) == "" || strings.EqualFold(k, "authorization") && len(keys) > 0 {
			continue
		}
		req.Header.Set(k, v)
	}
	if len(keys) > 0 {
		req.Header.Set("Authorization", "Bearer "+keys[0])
	}
	tr := h.transport
	if tr == nil {
		tr = http.DefaultTransport.(*http.Transport).Clone()
	}
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, 0, 0, err
	}
	defer resp.Body.Close()
	var list struct {
		Data []map[string]any `json:"data"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 10<<20)).Decode(&list)
	seen := map[string]bool{}
	for _, m := range list.Data {
		id, _ := m["id"].(string)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	return ids, resp.StatusCode, time.Since(start).Milliseconds(), nil
}

func (h *Handler) listCombos(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.ListCombos()
	if err != nil {
		respond.JSONError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"data": rows})
}

type comboIn struct {
	Name  string                `json:"name"`
	Tiers []providers.ComboTier `json:"tiers"`
}

func (h *Handler) createCombo(w http.ResponseWriter, r *http.Request) {
	var in comboIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	row, err := h.store.SaveCombo(providers.RouteCombo{Name: in.Name, Tiers: in.Tiers})
	if err != nil {
		respond.JSONError(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	if err := h.rebuild(); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "rebuild_error", err.Error())
		return
	}
	respond.JSON(w, http.StatusCreated, row)
}

func (h *Handler) updateCombo(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	var in comboIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	row, err := h.store.UpdateCombo(uint(id), providers.RouteCombo{Name: in.Name, Tiers: in.Tiers})
	if err != nil {
		respond.JSONError(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	if err := h.rebuild(); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "rebuild_error", err.Error())
		return
	}
	respond.JSON(w, http.StatusOK, row)
}

func (h *Handler) deleteCombo(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	if err := h.store.DeleteCombo(uint(id)); err != nil {
		respond.JSONError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if err := h.rebuild(); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "rebuild_error", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *Handler) testCombo(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	combos, err := h.store.ListCombos()
	if err != nil {
		respond.JSONError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	var combo *providers.RouteCombo
	for i := range combos {
		if combos[i].ID == uint(id) {
			combo = &combos[i]
			break
		}
	}
	if combo == nil {
		respond.JSONError(w, http.StatusNotFound, "not_found", "combo not found")
		return
	}
	rows, err := h.store.ListProviders()
	if err != nil {
		respond.JSONError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	byName := make(map[string]uint, len(rows))
	for _, p := range rows {
		byName[p.Name] = p.ID
	}
	tr := h.transport
	if tr == nil {
		tr = http.DefaultTransport.(*http.Transport).Clone()
	}
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	tiers := make([]map[string]any, 0, len(combo.Tiers))
	anyOK := false
	for _, tier := range combo.Tiers {
		res := h.probeTier(r, client, byName, tier.Provider)
		if ok, _ := res["ok"].(bool); ok {
			anyOK = true
		}
		tiers = append(tiers, res)
	}
	respond.JSON(w, http.StatusOK, map[string]any{"ok": anyOK, "tiers": tiers})
}

func (h *Handler) probeTier(r *http.Request, client *http.Client, byName map[string]uint, provider string) map[string]any {
	if providers.IsBuiltin(provider) {
		return map[string]any{"provider": provider, "ok": true, "skipped": true, "note": "builtin, see provider health"}
	}
	name := strings.TrimPrefix(provider, "custom:")
	pid, ok := byName[name]
	if !strings.HasPrefix(provider, "custom:") || !ok {
		return map[string]any{"provider": provider, "ok": false, "error": "unknown provider"}
	}
	row, err := h.store.GetProviderRaw(pid)
	if err != nil {
		return map[string]any{"provider": provider, "ok": false, "error": "unknown provider"}
	}
	if !row.Enabled {
		return map[string]any{"provider": provider, "ok": false, "error": "provider disabled"}
	}
	start := time.Now()
	req, err := http.NewRequestWithContext(r.Context(), "GET", row.BaseURL+"/models", nil)
	if err != nil {
		return map[string]any{"provider": provider, "ok": false, "error": err.Error()}
	}
	if len(row.APIKeys) > 0 {
		req.Header.Set("Authorization", "Bearer "+row.APIKeys[0])
	}
	resp, err := client.Do(req)
	if err != nil {
		return map[string]any{"provider": provider, "ok": false, "error": err.Error()}
	}
	defer resp.Body.Close()
	var list struct {
		Data []any `json:"data"`
	}
	_ = json.NewDecoder(io.LimitReader(resp.Body, 10<<20)).Decode(&list)
	return map[string]any{"provider": provider, "ok": resp.StatusCode < 300, "latencyMs": time.Since(start).Milliseconds(), "status": resp.StatusCode, "modelCount": len(list.Data)}
}

package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"freegate/internal/delivery/respond"
	"freegate/internal/infrastructure/providers"
)

type Handler struct {
	store     *providers.Store
	rebuild   func() error
	transport *http.Transport
}

func New(store *providers.Store, rebuild func() error, transport *http.Transport) *Handler {
	return &Handler{store: store, rebuild: rebuild, transport: transport}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	h.Register(r)
	return r
}

func (h *Handler) Register(r chi.Router) {
	r.Get("/api/providers", h.listProviders)
	r.Post("/api/providers", h.createProvider)
	r.Get("/api/providers/{id}", h.getProvider)
	r.Put("/api/providers/{id}", h.updateProvider)
	r.Delete("/api/providers/{id}", h.deleteProvider)
	r.Post("/api/providers/{id}/test", h.testProvider)
	r.Get("/api/combos", h.listCombos)
	r.Post("/api/combos", h.createCombo)
	r.Put("/api/combos/{id}", h.updateCombo)
	r.Delete("/api/combos/{id}", h.deleteCombo)
	r.Post("/api/combos/{id}/test", h.testCombo)
}

type providerIn struct {
	Name       string            `json:"name"`
	BaseURL    string            `json:"base_url"`
	APIKeys    []string          `json:"api_keys"`
	Headers    map[string]string `json:"headers"`
	ModelAllow []string          `json:"model_allow"`
	ModelBlock []string          `json:"model_block"`
	RefreshSec int               `json:"refresh_sec"`
	Priority   int               `json:"priority"`
	Enabled    bool              `json:"enabled"`
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
	var in providerIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	row, err := h.store.CreateProvider(providers.Provider{Name: in.Name, BaseURL: in.BaseURL, APIKeys: in.APIKeys, Headers: in.Headers, ModelAllow: in.ModelAllow, ModelBlock: in.ModelBlock, RefreshSec: in.RefreshSec, Priority: in.Priority, Enabled: in.Enabled})
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
	if len(keys) == 0 {
		cur, err := h.store.GetProviderRaw(uint(id))
		if err != nil {
			respond.JSONError(w, http.StatusNotFound, "not_found", "provider not found")
			return
		}
		keys = cur.APIKeys
	}
	row, err := h.store.UpdateProvider(uint(id), providers.Provider{Name: in.Name, BaseURL: in.BaseURL, APIKeys: keys, Headers: in.Headers, ModelAllow: in.ModelAllow, ModelBlock: in.ModelBlock, RefreshSec: in.RefreshSec, Priority: in.Priority, Enabled: in.Enabled})
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
	start := time.Now()
	req, err := http.NewRequestWithContext(r.Context(), "GET", row.BaseURL+"/models", nil)
	if err != nil {
		respond.JSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if len(row.APIKeys) > 0 {
		req.Header.Set("Authorization", "Bearer "+row.APIKeys[0])
	}
	tr := h.transport
	if tr == nil {
		tr = http.DefaultTransport.(*http.Transport).Clone()
	}
	client := &http.Client{Transport: tr, Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		respond.JSON(w, http.StatusOK, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	defer resp.Body.Close()
	var list struct {
		Data []any `json:"data"`
	}
	_ = json.NewDecoder(http.MaxBytesReader(w, resp.Body, 10<<20)).Decode(&list)
	respond.JSON(w, http.StatusOK, map[string]any{"ok": resp.StatusCode < 300, "modelCount": len(list.Data), "latencyMs": time.Since(start).Milliseconds(), "status": resp.StatusCode})
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
		res := h.probeTier(w, r, client, byName, tier.Provider)
		if ok, _ := res["ok"].(bool); ok {
			anyOK = true
		}
		tiers = append(tiers, res)
	}
	respond.JSON(w, http.StatusOK, map[string]any{"ok": anyOK, "tiers": tiers})
}

var builtinTier = map[string]bool{"opencode": true, "kilo": true, "llm7": true}

func (h *Handler) probeTier(w http.ResponseWriter, r *http.Request, client *http.Client, byName map[string]uint, provider string) map[string]any {
	if builtinTier[provider] {
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
	_ = json.NewDecoder(http.MaxBytesReader(w, resp.Body, 10<<20)).Decode(&list)
	return map[string]any{"provider": provider, "ok": resp.StatusCode < 300, "latencyMs": time.Since(start).Milliseconds(), "status": resp.StatusCode, "modelCount": len(list.Data)}
}

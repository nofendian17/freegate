package admin

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"freegate/internal/delivery/respond"
	"freegate/internal/domain"
	"freegate/internal/infrastructure/providers"
)

type Handler struct {
	store   *providers.Store
	rebuild func() error
	lookup  func(name string) domain.Upstream
	onChain func(chain []domain.Upstream)
}

func New(store *providers.Store, rebuild func() error, lookup func(string) domain.Upstream, onChain func([]domain.Upstream)) *Handler {
	return &Handler{store: store, rebuild: rebuild, lookup: lookup, onChain: onChain}
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
	r.Post("/api/combos/{id}/activate", h.activateCombo)
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
	out := keys[:0]
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
	resp, err := http.DefaultClient.Do(req)
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

func (h *Handler) createCombo(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name    string   `json:"name"`
		Members []string `json:"members"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	row, err := h.store.SaveCombo(providers.RouteCombo{Name: in.Name, Members: in.Members})
	if err != nil {
		respond.JSONError(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	respond.JSON(w, http.StatusCreated, row)
}

func (h *Handler) updateCombo(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	var in struct {
		Name    string   `json:"name"`
		Members []string `json:"members"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	row, err := h.store.UpdateCombo(uint(id), providers.RouteCombo{Name: in.Name, Members: in.Members})
	if err != nil {
		respond.JSONError(w, http.StatusBadRequest, "validation_error", err.Error())
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

func (h *Handler) activateCombo(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	if err := h.store.ActivateCombo(uint(id)); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	active, err := h.store.ActiveCombo()
	if err != nil {
		respond.JSONError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if err := h.rebuild(); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "rebuild_error", err.Error())
		return
	}
	var chain []domain.Upstream
	for _, m := range active.Members {
		if h.lookup != nil {
			if u := h.lookup(m); u != nil {
				chain = append(chain, u)
			}
		}
	}
	if h.onChain != nil {
		h.onChain(chain)
	}
	respond.JSON(w, http.StatusOK, active)
}

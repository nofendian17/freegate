package admin

import (
	"encoding/json"
	"net/http"

	"github.com/go-chi/chi/v5"

	"freegate/internal/delivery/respond"
	"freegate/internal/infrastructure/providers"
)

// listBuiltinProxies returns the relay selection of every builtin upstream
// (opencode, kilo, llm7), defaulting to the global rotation.
func (h *Handler) listBuiltinProxies(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.ListBuiltinProxies()
	if err != nil {
		respond.JSONError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"data": rows})
}

// updateBuiltinProxy stores the relay selection of one builtin upstream:
// "" follows the global rotation, "direct" skips relays, "pool" pins to
// the given pool. Triggers a rebuild so the live upstream picks it up.
func (h *Handler) updateBuiltinProxy(w http.ResponseWriter, r *http.Request) {
	name := chi.URLParam(r, "name")
	if !providers.IsBuiltin(name) {
		respond.JSONError(w, http.StatusNotFound, "not_found", "unknown builtin provider")
		return
	}
	var in struct {
		ProxyMode   string `json:"proxy_mode"`
		ProxyPoolID *uint  `json:"proxy_pool_id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	if _, _, err := h.resolveProxy(in.ProxyMode, in.ProxyPoolID); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "validation_error", err.Error())
		return
	}
	row, err := h.store.SetBuiltinProxy(name, in.ProxyMode, in.ProxyPoolID)
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

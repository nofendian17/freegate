package admin

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"freegate/internal/delivery/respond"
	"freegate/internal/infrastructure/providers"
)

type builtinProxyIn struct {
	ProxyMode   string `json:"proxy_mode" validate:"proxy_mode"`
	ProxyPoolID *uint  `json:"proxy_pool_id" validate:"omitempty,min=1"`
}

// listBuiltinProxies returns the relay selection of every builtin upstream
// (opencode, kilo, llm7), defaulting to the global rotation.
func (h *Handler) listBuiltinProxies(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.ListBuiltinProxies(r.Context())
	if err != nil {
		respondStoreError(w, err)
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
	var in builtinProxyIn
	if !decodeInput(w, r, &in) {
		return
	}
	// SetBuiltinProxy validates the selection itself (single GetPool), so
	// no pre-validation here — one query, one error path.
	row, err := h.store.SetBuiltinProxy(r.Context(), name, in.ProxyMode, in.ProxyPoolID)
	if err != nil {
		respondStoreError(w, err)
		return
	}
	if err := h.rebuild(r.Context()); err != nil {
		respondRebuildError(w, err)
		return
	}
	respond.JSON(w, http.StatusOK, row)
}

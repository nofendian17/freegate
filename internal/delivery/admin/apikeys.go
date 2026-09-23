package admin

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"freegate/internal/delivery/respond"
)

type clientKeyIn struct {
	Name string `json:"name" validate:"required,resource_name"`
}

type clientKeyUpdateIn struct {
	Name    string `json:"name" validate:"omitempty,resource_name"`
	Enabled *bool  `json:"enabled"`
}

// listClientKeys returns all client API keys (hashes never leave the store).
// No rebuild: keys are read live from the DB on every /v1/* request.
func (h *Handler) listClientKeys(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.ListClientKeys(r.Context())
	if err != nil {
		respondStoreError(w, err)
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"data": rows})
}

// createClientKey mints a key and returns the row plus the raw secret.
func (h *Handler) createClientKey(w http.ResponseWriter, r *http.Request) {
	var in clientKeyIn
	if !decodeInput(w, r, &in) {
		return
	}
	row, raw, err := h.store.CreateClientKey(r.Context(), in.Name)
	if err != nil {
		respondStoreError(w, err)
		return
	}
	respond.JSON(w, http.StatusCreated, map[string]any{
		"id": row.ID, "name": row.Name, "prefix": row.Prefix,
		"enabled": row.Enabled, "created_at": row.CreatedAt,
		"api_key": raw,
	})
}

// updateClientKey renames or enables/disables a key. The secret never
// changes (delete + create to rotate).
func (h *Handler) updateClientKey(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	var in clientKeyUpdateIn
	if !decodeInput(w, r, &in) {
		return
	}
	cur, err := h.store.GetClientKey(r.Context(), uint(id))
	if err != nil {
		respondStoreError(w, err)
		return
	}
	name := cur.Name
	if in.Name != "" {
		name = in.Name
	}
	enabled := cur.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	row, err := h.store.UpdateClientKey(r.Context(), uint(id), name, enabled)
	if err != nil {
		respondStoreError(w, err)
		return
	}
	respond.JSON(w, http.StatusOK, row)
}

// revealClientKey returns the raw secret for one key so it can be
// viewed/copied again later. Deliberately separate from list/get: only
// this per-key path exposes the secret, and it stays admin-gated like
// the rest of /api/*.
func (h *Handler) revealClientKey(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	raw, err := h.store.RevealClientKey(r.Context(), uint(id))
	if err != nil {
		respondStoreError(w, err)
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"id": id, "api_key": raw})
}

// deleteClientKey revokes a key immediately (no rebuild needed —
// verification reads the DB live).
func (h *Handler) deleteClientKey(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	if err := h.store.DeleteClientKey(r.Context(), uint(id)); err != nil {
		respondStoreError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

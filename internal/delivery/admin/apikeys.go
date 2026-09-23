package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"freegate/internal/delivery/respond"
)

// listClientKeys returns all client API keys (hashes never leave the store).
// No rebuild: keys are read live from the DB on every /v1/* request.
func (h *Handler) listClientKeys(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.ListClientKeys()
	if err != nil {
		respond.JSONError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"data": rows})
}

// createClientKey mints a key and returns the row plus the raw secret.
func (h *Handler) createClientKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name string `json:"name"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	row, raw, err := h.store.CreateClientKey(in.Name)
	if err != nil {
		respond.JSONError(w, http.StatusBadRequest, "validation_error", err.Error())
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
	var in struct {
		Name    string `json:"name"`
		Enabled *bool  `json:"enabled"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	cur, err := h.store.GetClientKey(uint(id))
	if err != nil {
		respond.JSONError(w, http.StatusNotFound, "not_found", "api key not found")
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
	row, err := h.store.UpdateClientKey(uint(id), name, enabled)
	if err != nil {
		respond.JSONError(w, http.StatusBadRequest, "validation_error", err.Error())
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
	if _, err := h.store.GetClientKey(uint(id)); err != nil {
		respond.JSONError(w, http.StatusNotFound, "not_found", "api key not found")
		return
	}
	raw, err := h.store.RevealClientKey(uint(id))
	if err != nil {
		respond.JSONError(w, http.StatusBadRequest, "unavailable", err.Error())
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"id": id, "api_key": raw})
}

// deleteClientKey revokes a key immediately (no rebuild needed —
// verification reads the DB live).
func (h *Handler) deleteClientKey(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	if _, err := h.store.GetClientKey(uint(id)); err != nil {
		respond.JSONError(w, http.StatusNotFound, "not_found", "api key not found")
		return
	}
	if err := h.store.DeleteClientKey(uint(id)); err != nil {
		respond.JSONError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

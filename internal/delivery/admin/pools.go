package admin

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"freegate/internal/delivery/respond"
	"freegate/internal/infrastructure/providers"
)

type poolIn struct {
	Name        string `json:"name"`
	ProxyURL    string `json:"proxy_url"`
	NoProxy     string `json:"no_proxy"`
	StrictProxy bool   `json:"strict_proxy"`
	Enabled     bool   `json:"enabled"`
}

func (h *Handler) listPools(w http.ResponseWriter, r *http.Request) {
	rows, err := h.store.ListPools()
	if err != nil {
		respond.JSONError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	respond.JSON(w, http.StatusOK, map[string]any{"data": rows})
}

func (h *Handler) createPool(w http.ResponseWriter, r *http.Request) {
	in := poolIn{Enabled: true}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	row, err := h.store.CreatePool(providers.ProxyPool{Name: in.Name, ProxyURL: in.ProxyURL, NoProxy: in.NoProxy, StrictProxy: in.StrictProxy, Enabled: in.Enabled})
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

func (h *Handler) getPool(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	row, err := h.store.GetPool(uint(id))
	if err != nil {
		respond.JSONError(w, http.StatusNotFound, "not_found", "pool not found")
		return
	}
	respond.JSON(w, http.StatusOK, row)
}

func (h *Handler) updatePool(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	var in poolIn
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&in); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}
	row, err := h.store.UpdatePool(uint(id), providers.ProxyPool{Name: in.Name, ProxyURL: in.ProxyURL, NoProxy: in.NoProxy, StrictProxy: in.StrictProxy, Enabled: in.Enabled})
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

func (h *Handler) deletePool(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.Atoi(chi.URLParam(r, "id"))
	if err := h.store.DeletePool(uint(id)); err != nil {
		respond.JSONError(w, http.StatusInternalServerError, "store_error", err.Error())
		return
	}
	if err := h.rebuild(); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "rebuild_error", err.Error())
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

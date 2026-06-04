package handler

import (
	"net/http"

	"freegate/internal/delivery/respond"
	"freegate/internal/model"
)

func (h *Handler) ListModels(w http.ResponseWriter, r *http.Request) {
	models := h.upstream.AllModels()
	if len(models) == 0 {
		respond.JSONError(w, http.StatusServiceUnavailable, "unavailable", "models not ready")
		return
	}

	resp := model.ModelList{Object: "list", Data: models}
	respond.JSON(w, http.StatusOK, resp)
}

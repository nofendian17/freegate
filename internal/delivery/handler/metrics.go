package handler

import (
	"net/http"

	"freegate/internal/delivery/respond"
)

func (h *Handler) Metrics(w http.ResponseWriter, r *http.Request) {
	respond.JSON(w, http.StatusOK, h.mtr.Metrics())
}

package handler

import (
	"net/http"

	"freegate/internal/delivery/respond"
)

func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	respond.Ready(w, h.models.IsReady())
}

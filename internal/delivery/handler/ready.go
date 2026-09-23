package handler

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"freegate/internal/delivery/respond"
)

func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	ready := h.models.IsReady()
	if ready && h.ready != nil {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if err := h.ready.PingContext(ctx); err != nil {
			slog.Error("readiness check failed", "error", err)
			ready = false
		}
	}
	respond.Ready(w, ready)
}

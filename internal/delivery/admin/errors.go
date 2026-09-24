package admin

import (
	"context"
	"errors"
	"log/slog"
	"net/http"

	"freegate/internal/delivery/respond"
	"freegate/internal/infrastructure/registry"
)

func respondStoreError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, registry.ErrInvalidArgument):
		respond.JSONError(w, http.StatusBadRequest, "validation_error", err.Error())
	case errors.Is(err, registry.ErrNotFound):
		respond.JSONError(w, http.StatusNotFound, "not_found", "resource not found")
	case errors.Is(err, registry.ErrConflict):
		respond.JSONError(w, http.StatusConflict, "conflict", "resource already exists")
	case errors.Is(err, context.DeadlineExceeded):
		respond.JSONError(w, http.StatusGatewayTimeout, "store_timeout", "storage operation timed out")
	case errors.Is(err, context.Canceled):
		respond.JSONError(w, http.StatusRequestTimeout, "request_canceled", "request canceled")
	default:
		slog.Error("storage operation failed", "error", err)
		respond.JSONError(w, http.StatusInternalServerError, "store_error", "storage operation failed")
	}
}

func respondRebuildError(w http.ResponseWriter, err error) {
	slog.Error("runtime rebuild failed", "error", err)
	respond.JSONError(w, http.StatusInternalServerError, "rebuild_error", "runtime rebuild failed")
}

func probeErrorMessage(err error) string {
	if errors.Is(err, registry.ErrInvalidArgument) || errors.Is(err, registry.ErrNotFound) {
		return err.Error()
	}
	slog.Error("proxy probe setup failed", "error", err)
	return "proxy selection unavailable"
}

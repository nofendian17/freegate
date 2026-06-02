package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"freegate/internal/model"
	"freegate/internal/respond"
)

const MaxRequestBodySize = 10 << 20

// Upstream is the single interface the handler needs from the proxy client.
type Upstream interface {
	ProxyChat(w http.ResponseWriter, r *http.Request, modelID string, body []byte)
	AllModels() []model.Model
	IsReady() bool
	Metrics() map[string]any
}

type Handler struct {
	upstream Upstream
}

func New(upstream Upstream) *Handler {
	return &Handler{upstream: upstream}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/v1/models", h.ListModels)
	r.Get("/v1/metrics", h.Metrics)
	r.Get("/ready", h.Ready)
	r.Post("/v1/chat/completions", h.Chat)
	return r
}

func (h *Handler) ListModels(w http.ResponseWriter, r *http.Request) {
	models := h.upstream.AllModels()
	if len(models) == 0 {
		respond.JSONError(w, http.StatusServiceUnavailable, "unavailable", "models not ready")
		return
	}

	resp := model.ModelList{Object: "list", Data: models}
	respond.JSON(w, http.StatusOK, resp)
}

func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	respond.Ready(w, h.upstream.IsReady())
}

func (h *Handler) Metrics(w http.ResponseWriter, r *http.Request) {
	respond.JSON(w, http.StatusOK, h.upstream.Metrics())
}

func (h *Handler) Chat(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		respond.JSONError(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body exceeds 10 MB limit")
		return
	}

	if len(body) == 0 {
		respond.JSONError(w, http.StatusBadRequest, "bad_request", "empty request body")
		return
	}

	modelID, err := extractModelID(body)
	if err != nil {
		respond.JSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	h.upstream.ProxyChat(w, r, modelID, body)
}

func extractModelID(body []byte) (string, error) {
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return "", fmt.Errorf("invalid request body: %w", err)
	}
	if req.Model == "" {
		return "", fmt.Errorf("missing required field: model")
	}
	return req.Model, nil
}

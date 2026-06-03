package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	anyllm "github.com/mozilla-ai/any-llm-go"

	"freegate/internal/model"
	"freegate/internal/respond"
)

const MaxRequestBodySize = 10 << 20

// Upstream is the single interface the handler needs from the proxy client.
type Upstream interface {
	ProxyChat(w http.ResponseWriter, r *http.Request, params anyllm.CompletionParams)
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
	var params anyllm.CompletionParams
	if err := json.Unmarshal(body, &params); err != nil {
		respond.JSONError(w, http.StatusBadRequest, "bad_request", fmt.Sprintf("invalid request body: %v", err))
		return
	}
	if params.Model == "" {
		respond.JSONError(w, http.StatusBadRequest, "bad_request", "missing required field: model")
		return
	}
	if len(params.Messages) == 0 {
		respond.JSONError(w, http.StatusBadRequest, "bad_request", "messages is required and must be non-empty")
		return
	}
	h.upstream.ProxyChat(w, r, params)
}

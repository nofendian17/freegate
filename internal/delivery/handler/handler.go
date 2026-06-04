package handler

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"freegate/internal/model"
)

const MaxRequestBodySize = 10 << 20

// ChatProxy is the interface the handler needs from the chat service.
type ChatProxy interface {
	ProxyChat(ctx context.Context, w http.ResponseWriter, r *http.Request, modelID string, body []byte) error
}

// ModelLister is the interface the handler needs from the model service.
type ModelLister interface {
	AllModels() []model.Model
	IsReady() bool
}

// MetricsProvider exposes the metrics snapshot for the /v1/metrics endpoint.
type MetricsProvider interface {
	Metrics() map[string]any
}

// Handler handles HTTP requests for the freegate proxy.
// It supports OpenAI, Claude, and Gemini API formats through automatic
// format detection and translation.
type Handler struct {
	chat   ChatProxy
	models ModelLister
	mtr    MetricsProvider
}

func New(chat ChatProxy, models ModelLister, mtr MetricsProvider) *Handler {
	return &Handler{chat: chat, models: models, mtr: mtr}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/v1/models", h.ListModels)
	r.Get("/v1/metrics", h.Metrics)
	r.Get("/ready", h.Ready)
	r.Post("/v1/chat/completions", h.Chat)
	// Claude-native endpoint (optional, clients can also POST Claude bodies to /v1/chat/completions)
	r.Post("/v1/messages", h.Chat)
	return r
}

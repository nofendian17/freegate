package handler

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	"freegate/internal/model"
)

const MaxRequestBodySize = 10 << 20

// Upstream is the single interface the handler needs from the proxy client.
type Upstream interface {
	ProxyChat(w http.ResponseWriter, r *http.Request, modelID string, body []byte)
	AllModels() []model.Model
	IsReady() bool
	Metrics() map[string]any
}

// Handler handles HTTP requests for the freegate proxy.
// It supports OpenAI, Claude, and Gemini API formats through automatic
// format detection and translation.
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
	// Claude-native endpoint (optional, clients can also POST Claude bodies to /v1/chat/completions)
	r.Post("/v1/messages", h.Chat)
	return r
}

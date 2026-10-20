package handler

import (
	"context"
	"net/http"

	"freegate/internal/domain"
)

// DefaultMaxRequestBodySize caps POST bodies when no override is configured.
const DefaultMaxRequestBodySize = 30 << 20 // 30 MB

// ChatProxy is the interface the handler needs from the chat service.
type ChatProxy interface {
	ProxyChat(ctx context.Context, w http.ResponseWriter, r *http.Request, modelID string, body []byte) error
}

// ModelLister is the interface the handler needs from the model service.
type ModelLister interface {
	AllModels() []domain.Model
	IsReady() bool
}

// MetricsProvider exposes the metrics snapshot for the /v1/metrics endpoint.
type MetricsProvider interface {
	Metrics() map[string]any
}

// ReadinessChecker verifies a required backing service before /ready passes.
type ReadinessChecker interface {
	PingContext(ctx context.Context) error
}

// Handler handles HTTP requests for the freegate proxy.
// It supports OpenAI, Claude, and Gemini API formats through automatic
// format detection and translation.
type Handler struct {
	chat    ChatProxy
	models  ModelLister
	mtr     MetricsProvider
	ready   ReadinessChecker
	maxBody int64
}

func New(chat ChatProxy, models ModelLister, mtr MetricsProvider) *Handler {
	return &Handler{chat: chat, models: models, mtr: mtr, maxBody: DefaultMaxRequestBodySize}
}

func (h *Handler) WithReadiness(checker ReadinessChecker) *Handler {
	h.ready = checker
	return h
}

// WithMaxRequestBodySize overrides the request body cap. Non-positive values
// keep the current limit.
func (h *Handler) WithMaxRequestBodySize(bytes int64) *Handler {
	if bytes > 0 {
		h.maxBody = bytes
	}
	return h
}

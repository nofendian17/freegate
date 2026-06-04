package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"freegate/internal/model"
	"freegate/internal/respond"
	"freegate/internal/translate"
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
	r.Get("/", h.Root)
	r.Get("/v1/models", h.ListModels)
	r.Get("/v1/metrics", h.Metrics)
	r.Get("/ready", h.Ready)
	r.Post("/v1/chat/completions", h.Chat)
	// Claude-native endpoint (optional, clients can also POST Claude bodies to /v1/chat/completions)
	r.Post("/v1/messages", h.Chat)
	return r
}

func (h *Handler) Root(w http.ResponseWriter, r *http.Request) {
	respond.JSON(w, http.StatusOK, map[string]any{
		"service": "freegate - multi-upstream AI proxy",
		"routes": map[string]string{
			"GET  /":                    "this help",
			"GET  /ready":               "health check",
			"GET  /v1/models":           "list available free models",
			"POST /v1/chat/completions": "OpenAI-compatible chat completion (also accepts Claude and Gemini formats)",
			"POST /v1/messages":         "Claude-native endpoint (auto-translated to OpenAI upstream)",
		},
		"upstreams": map[string]string{
			"opencode": "default, model without prefix",
			"kilo":     "prefix: kilo/, kilo-, openrouter/, suffix: :free",
		},
	})
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

	// Detect format from body (OpenAI, Claude, or Gemini)
	format := translate.Detect(body)

	// Extract model ID (works for OpenAI, Claude; Gemini may need fallback)
	modelID := translate.ExtractModelID(body)
	if modelID == "" {
		id, err := extractModelID(body)
		if err != nil {
			respond.JSONError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
		modelID = id
	}

	// Translate request body to OpenAI intermediate format if needed
	if format != translate.FormatOpenAI {
		translated, err := translate.Request(body, format, translate.FormatOpenAI)
		if err != nil {
			respond.JSONError(w, http.StatusBadRequest, "translation_error", err.Error())
			return
		}
		body = translated
	}

	// For non-OpenAI clients, wrap the response writer to translate
	// the upstream's OpenAI response back to the source format
	if format != translate.FormatOpenAI {
		wr := translate.NewResponseWriter(w, format)
		defer wr.Close()
		h.upstream.ProxyChat(wr, r, modelID, body)
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

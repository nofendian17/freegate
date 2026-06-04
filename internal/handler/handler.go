package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/mozilla-ai/any-llm-go/providers"

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

// syncRequestReasoningContent populates the Reasoning field on assistant
// messages from the wire-level "reasoning_content" key (used by DeepSeek,
// Qwen, Moonshot, etc.). The any-llm-go Message struct expects "reasoning" as
// the JSON tag, so "reasoning_content" is silently dropped during unmarshal.
func syncRequestReasoningContent(params anyllm.CompletionParams, rawBody []byte) {
	var root map[string]any
	if err := json.Unmarshal(rawBody, &root); err != nil {
		return
	}
	msgs, _ := root["messages"].([]any)
	for i, m := range msgs {
		msg, ok := m.(map[string]any)
		if !ok {
			continue
		}
		role, _ := msg["role"].(string)
		if role != "assistant" {
			continue
		}
		rc, ok := msg["reasoning_content"].(string)
		if !ok || rc == "" {
			continue
		}
		if i < len(params.Messages) {
			params.Messages[i].Reasoning = &providers.Reasoning{Content: rc}
		}
	}
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

	// Normalize reasoning_content on incoming assistant messages.
	// The any-llm-go Message struct uses "reasoning" as the JSON tag, but
	// OpenAI-compatible providers send "reasoning_content" on the wire.
	// We check both the parsed messages (via this injection) and the raw body
	// so that reasoning_content from client requests is forwarded upstream.
	syncRequestReasoningContent(params, body)

	h.upstream.ProxyChat(w, r, params)
}

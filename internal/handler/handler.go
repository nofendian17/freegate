package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"

	"freegate/internal/model"
)

const MaxRequestBodySize = 10 << 20

type ChatProxy interface {
	ProxyChat(w http.ResponseWriter, r *http.Request, modelID string, body []byte)
}

type ModelProvider interface {
	AllModels() []model.Model
	IsReady() bool
}

type Handler struct {
	chatProxy     ChatProxy
	modelProvider ModelProvider
}

func New(chatProxy ChatProxy, modelProvider ModelProvider) *Handler {
	return &Handler{
		chatProxy:     chatProxy,
		modelProvider: modelProvider,
	}
}

func (h *Handler) Routes() chi.Router {
	r := chi.NewRouter()
	r.Get("/v1/models", h.ListModels)
	r.Get("/ready", h.Ready)
	r.Post("/v1/chat/completions", h.Chat)
	return r
}

func (h *Handler) ListModels(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	models := h.modelProvider.AllModels()
	if len(models) == 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
		w.Write([]byte(`{"error":{"type":"unavailable","message":"models not ready"}}`))
		return
	}

	resp := model.ModelList{Object: "list", Data: models}
	w.WriteHeader(http.StatusOK)
	json.NewEncoder(w).Encode(resp)
}

func (h *Handler) Ready(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	if h.modelProvider.IsReady() {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"status":"ok"}`))
		return
	}
	w.WriteHeader(http.StatusServiceUnavailable)
	w.Write([]byte(`{"status":"not ready"}`))
}

func (h *Handler) Chat(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, MaxRequestBodySize)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeJSONError(w, http.StatusRequestEntityTooLarge, "body_too_large", "request body exceeds 10 MB limit")
		return
	}

	if len(body) == 0 {
		writeJSONError(w, http.StatusBadRequest, "bad_request", "empty request body")
		return
	}

	modelID, err := extractModelID(body)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	h.chatProxy.ProxyChat(w, r, modelID, body)
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

func writeJSONError(w http.ResponseWriter, status int, tp, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	fmt.Fprintf(w, `{"error":{"type":"%s","message":"%s"}}`, tp, msg)
}

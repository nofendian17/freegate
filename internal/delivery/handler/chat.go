package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"freegate/internal/delivery/respond"
	"freegate/internal/translate"
)

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

	// The URL path disambiguates when body-based detection is ambiguous:
	// POST /v1/messages is always Claude, POST /v1/chat/completions is always OpenAI.
	// This handles real Anthropic SDKs that send requests without anthropic_version
	// in the body (only the anthropic-version header).
	if format == translate.FormatOpenAI && strings.HasSuffix(r.URL.Path, "/messages") {
		format = translate.FormatClaude
	} else if format == translate.FormatClaude && strings.HasSuffix(r.URL.Path, "/chat/completions") {
		format = translate.FormatOpenAI
	}

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

	// Normalize roles for upstream compatibility (e.g., developer → system).
	// This runs for all formats — the normalization is a no-op when no
	// unsupported roles are present.
	body, err = translate.NormalizeRoles(body)
	if err != nil {
		respond.JSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	// Normalize reasoning fields: copy reasoning → reasoning_content for
	// assistant messages that are missing reasoning_content. Required for
	// DeepSeek thinking mode (deepseek-reasoner).
	body, err = translate.NormalizeRequestReasoning(body)
	if err != nil {
		respond.JSONError(w, http.StatusBadRequest, "bad_request", err.Error())
		return
	}

	// For non-OpenAI clients, wrap the response writer to translate
	// the upstream's OpenAI response back to the source format
	if format != translate.FormatOpenAI {
		wr := translate.NewResponseWriter(w, format)
		defer wr.Close()
		_ = h.chat.ProxyChat(r.Context(), wr, r, modelID, body)
		return
	}

	_ = h.chat.ProxyChat(r.Context(), w, r, modelID, body)
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

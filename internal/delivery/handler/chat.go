package handler

import (
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"freegate/internal/delivery/respond"
	"freegate/internal/translate"
)

var responseModels []string

func init() {
	// Direct config via env RESPONSE_MODELS (comma-separated substrings)
	if v := os.Getenv("RESPONSE_MODELS"); v != "" {
		for _, s := range strings.Split(v, ",") {
			s = strings.TrimSpace(s)
			if s != "" {
				responseModels = append(responseModels, strings.ToLower(s))
			}
		}
	} else {
		// default direct config for muse family
		responseModels = []string{"muse-spark", "muse_spark"}
	}
}

// SetResponseModels overrides the direct config (called from server wiring).
func SetResponseModels(models []string) {
	if len(models) == 0 {
		return
	}
	var lower []string
	for _, m := range models {
		m = strings.TrimSpace(strings.ToLower(m))
		if m != "" {
			lower = append(lower, m)
		}
	}
	if len(lower) > 0 {
		responseModels = lower
	}
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

	// Detect format from body (OpenAI, Claude, Gemini, Responses)
	format := translate.DetectByPath(r.URL.Path, body)

	// The URL path disambiguates when body-based detection is ambiguous:
	// POST /v1/messages is always Claude, POST /v1/chat/completions is always OpenAI.
	// This handles real Anthropic SDKs that send requests without anthropic_version
	// in the body (only the anthropic-version header).
	if format == translate.FormatOpenAI && strings.HasSuffix(r.URL.Path, "/messages") {
		format = translate.FormatClaude
	} else if format == translate.FormatClaude && strings.HasSuffix(r.URL.Path, "/chat/completions") {
		format = translate.FormatOpenAI
	}
	// /v1/responses is always responses (handled by DetectByPath)
	if strings.HasSuffix(r.URL.Path, "/responses") {
		format = translate.FormatOpenAIResponses
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

	// Determine upstream target format. Muse models are served by /zen/v1/responses.
	targetFormat := targetFormatForModel(modelID)

	slog.Debug("chat translate", "model", modelID, "src", format, "dst", targetFormat, "path", r.URL.Path, "response_models", responseModels)
	if os.Getenv("LOG_LEVEL") == "debug" || os.Getenv("UPSTREAM_CAPTURE") == "true" {
		slog.Info("chat FMT", "model", modelID, "fmt", string(format)+"→"+string(targetFormat), "path", r.URL.Path)
	}

	// Translate request body from source format to target format if needed
	if format != targetFormat {
		translated, err := translate.Request(body, format, targetFormat)
		if err != nil {
			respond.JSONError(w, http.StatusBadRequest, "translation_error", err.Error())
			return
		}
		slog.Debug("chat request translated", "src", format, "dst", targetFormat, "in_len", len(body), "out_len", len(translated))
		body = translated
	}

	// One-pass upstream preparation: only for OpenAI chat target.
	// Responses target has its own normalization (max_output_tokens) handled
	// in the responses translator.
	if targetFormat == translate.FormatOpenAI {
		body, err = translate.PrepareForUpstream(body)
		if err != nil {
			respond.JSONError(w, http.StatusBadRequest, "bad_request", err.Error())
			return
		}
	}

	// For mismatched formats, wrap the response writer to translate
	// the upstream's target response back to the client's source format
	if format != targetFormat {
		wr := translate.NewResponseWriterWithDst(w, targetFormat, format)
		defer wr.Close()
		if err := h.chat.ProxyChat(r.Context(), wr, r, modelID, body); err != nil {
			h.writeChatError(w, err)
		}
		return
	}

	if err := h.chat.ProxyChat(r.Context(), w, r, modelID, body); err != nil {
		h.writeChatError(w, err)
	}
}

// writeChatError maps a ProxyChat error to an HTTP status and writes an
// OpenAI-compatible error response. Upstream HTTP errors (including 429)
// are passed through verbatim by ProxyChat, so errors reaching this
// function are transport/selection failures: always a 502 gateway error.
func (h *Handler) writeChatError(w http.ResponseWriter, err error) {
	respond.JSONError(w, http.StatusBadGateway, "upstream_error", err.Error())
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

// targetFormatForModel returns the upstream format for a given model.
// Direct config via RESPONSE_MODELS env (comma-separated substrings, case-insensitive).
// Defaults to muse-spark family for backward compat.
func targetFormatForModel(modelID string) translate.Format {
	// Strip thinking suffix "model(level)" if present
	base := modelID
	if idx := strings.LastIndex(base, "("); idx >= 0 && strings.HasSuffix(base, ")") {
		base = strings.TrimSpace(base[:idx])
	}
	base = strings.ToLower(strings.TrimSpace(base))
	for _, pat := range responseModels {
		if pat != "" && strings.Contains(base, pat) {
			return translate.FormatOpenAIResponses
		}
	}
	return translate.FormatOpenAI
}

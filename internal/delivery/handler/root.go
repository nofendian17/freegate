package handler

import (
	"net/http"

	"freegate/internal/delivery/respond"
)

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

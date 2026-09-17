package upstream

import (
	"encoding/json"
	"log/slog"
	"strings"
)

// No-op tool injected into Zen requests that carry no tools.
//
// Root cause (verified live 2026-09-18 across /chat/completions,
// /messages, and /responses): the free-tier gate rejects anonymous
// requests whose body has a missing or empty `tools` array with 403
// FreeTierError, while the same request with a non-empty `tools` array
// returns 200 — header- and TLS-identical. It is a bot/abuse heuristic:
// genuine agent sessions always carry tools.
//
// The injection mirrors what the genuine client itself does for Copilot
// compatibility (a `_noop` tool marked do-not-call), in the native shape
// of each endpoint. It applies to anonymous requests only: keyed requests
// bypass the tools requirement (verified live), so paying users keep
// exact passthrough. If a model ever calls the noop tool, the tool_call
// passes through to the downstream client untouched.
const noopToolName = "_noop"

const noopToolDescription = "Do not call this tool. It exists only for API compatibility and must never be invoked."

// ensureUpstreamTools returns body unchanged when it already carries a
// non-empty tools array or is not a JSON object; otherwise it injects a
// single no-op tool in the endpoint's native shape.
func ensureUpstreamTools(body []byte, endpoint string) []byte {
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return body
	}
	// Inject only when tools is absent, null, or an empty array. A
	// present-but-malformed value (string/number/object) passes through
	// untouched so upstream returns its own clear 4xx instead of us
	// silently replacing caller intent.
	if t, ok := raw["tools"]; ok && t != nil {
		if arr, ok := t.([]any); !ok || len(arr) > 0 {
			return body
		}
	}
	switch {
	case strings.HasSuffix(endpoint, "/messages"):
		raw["tools"] = []any{map[string]any{
			"name":        noopToolName,
			"description": noopToolDescription,
			"input_schema": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		}}
	case strings.HasSuffix(endpoint, "/responses"):
		raw["tools"] = []any{map[string]any{
			"type":        "function",
			"name":        noopToolName,
			"description": noopToolDescription,
			"parameters": map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
			// Responses-family providers hardcode strict:false so
			// non-constrained schemas register (genuine client parity).
			"strict": false,
		}}
	default:
		raw["tools"] = []any{map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        noopToolName,
				"description": noopToolDescription,
				"parameters": map[string]any{
					"type":       "object",
					"properties": map[string]any{},
				},
			},
		}}
	}
	out, err := json.Marshal(raw)
	if err != nil {
		return body
	}
	slog.Debug("opencode noop tool injected", "endpoint", endpoint)
	return out
}

package proxy

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"strings"

	"freegate/internal/translate/claude"
)

func normalizeJSONWithMeta(dst io.Writer, src io.Reader, model, requestID string) TokenUsage {
	body, err := io.ReadAll(src)
	if err != nil {
		slog.Warn("failed to read response body", "error", err)
		// Write a proper error response instead of partial/corrupt body
		errResp := []byte(`{"error":{"type":"upstream_error","message":"failed to read upstream response"}}`)
		dst.Write(errResp)
		return TokenUsage{}
	}

	var resp map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	if err := dec.Decode(&resp); err != nil {
		dst.Write(body)
		return TokenUsage{}
	}
	// Some gateways frame the body as JSON + trailing data (a second
	// object, SSE remnants). A strict client (and the dashboard probe)
	// rejects the whole body for that; recover the first value — the
	// completion — instead of passing the corrupt framing through.
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		slog.Warn("upstream trailing data after JSON body, using first value",
			"model", model,
			"request_id", requestID,
			"path", "json",
		)
	}

	// OneHub-style gateways wrap the completion in a data envelope:
	// {"data": {"choices": [...], "usage": {...}}}. Unwrap it so the
	// response below normalizes (and clients parse) as OpenAI.
	if _, hasChoices := resp["choices"]; !hasChoices {
		if data, ok := resp["data"].(map[string]any); ok {
			if _, ok := data["choices"]; ok {
				resp = data
			}
		}
	}

	// Extract usage before normalizing
	usage := TokenUsage{}
	if u, ok := resp["usage"].(map[string]any); ok {
		usage = extractUsageFromMap(u)
	}

	syncMessageReasoning(resp)
	repairToolCallsJSON(resp)
	ensureFinishReason(resp)

	if isEmptyJSONCompletion(resp) {
		// Degenerate upstream response (observed during the muse-spark
		// outage: llm7 answered an unavailable model with HTTP 200 and a
		// bare {role:"assistant"} message). Surface it loudly instead of
		// letting the client see a silently empty assistant turn.
		slog.Warn("upstream empty completion",
			"model", model,
			"request_id", requestID,
			"path", "json",
		)
	}

	transformed, err := json.Marshal(resp)
	if err != nil {
		dst.Write(body)
		return usage
	}

	dst.Write(transformed)
	return usage
}

// isEmptyJSONCompletion reports whether an OpenAI chat-completion response
// carries no assistant payload at all: no choices, or messages with neither
// content, tool_calls, nor reasoning. An explicit error object is NOT
// degenerate — that path is already surfaced as an upstream failure.
func isEmptyJSONCompletion(resp map[string]any) bool {
	if _, isErr := resp["error"]; isErr {
		return false
	}
	choices, _ := resp["choices"].([]any)
	if len(choices) == 0 {
		return true
	}
	for _, cAny := range choices {
		c, ok := cAny.(map[string]any)
		if !ok {
			return true
		}
		msg, _ := c["message"].(map[string]any)
		if msg == nil {
			return true
		}
		if s, _ := msg["content"].(string); strings.TrimSpace(s) != "" {
			return false
		}
		if tc, has := msg["tool_calls"].([]any); has && len(tc) > 0 {
			return false
		}
		if r, _ := msg["reasoning_content"].(string); r != "" {
			return false
		}
	}
	return true
}

// ensureFinishReason synthesizes a finish_reason when the upstream omitted
// it (or sent null / empty), so strict OpenAI clients (opencode's
// llm/protocols/openai-chat.ts "missing finish_reason for choice 0"
// validator) don't fail the stream. Mirrors opencode's onHalt
// → finishEvents which defaults to "stop" when no finish was seen.
// When a tool_calls choice is present without a finish_reason we default
// to "tool_calls" so callers treat it as a completed tool call, matching
// opencode's hasToolCalls → tool-calls coalescing in finishEvents.
func ensureFinishReason(resp map[string]any) {
	choices, _ := resp["choices"].([]any)
	for i, cAny := range choices {
		choice, ok := cAny.(map[string]any)
		if !ok {
			continue
		}
		fr, hasFR := choice["finish_reason"]
		if hasFR && fr != nil {
			if s, ok := fr.(string); ok && s != "" {
				continue
			}
		}
		// Empty or absent: pick tool_calls when the synthesized choice
		// looks like a tool call, else stop. Also stamp remaining nulls
		// so every choice has a value — strict clients validate all.
		synthetic := "stop"
		if msg, _ := choice["message"].(map[string]any); msg != nil {
			if _, hasTC := msg["tool_calls"]; hasTC {
				synthetic = "tool_calls"
			}
		}
		choice["finish_reason"] = synthetic
		choices[i] = choice
	}
	if len(choices) > 0 {
		resp["choices"] = choices
	}
}

// repairToolCallsJSON normalizes malformed tool-call arguments in a
// non-streaming OpenAI response. Models such as tencent/hy3-free sometimes
// emit arguments that are not valid JSON objects; the client rejects those
// with "input JSON failed to parse". Each argument string is run through
// claude.RepairToolArgs, which always yields a valid JSON object (or "{}").
func repairToolCallsJSON(resp map[string]any) {
	choices, ok := resp["choices"].([]any)
	if !ok {
		return
	}
	for _, c := range choices {
		choice, ok := c.(map[string]any)
		if !ok {
			continue
		}
		msg, ok := choice["message"].(map[string]any)
		if !ok {
			continue
		}
		tcs, ok := msg["tool_calls"].([]any)
		if !ok {
			continue
		}
		for _, tcAny := range tcs {
			tc, ok := tcAny.(map[string]any)
			if !ok {
				continue
			}
			fn, ok := tc["function"].(map[string]any)
			if !ok {
				continue
			}
			args, ok := fn["arguments"].(string)
			if !ok || args == "" {
				continue
			}
			// Sanitize after repair (vllm#56302): values end at the
			// first DSML sigil so no markup reaches the tool.
			fn["arguments"] = claude.SanitizeToolArgs(claude.RepairToolArgs(args))
		}
	}
}

func syncMessageReasoning(resp map[string]any) {
	choices, _ := resp["choices"].([]any)
	for _, choice := range choices {
		c, ok := choice.(map[string]any)
		if !ok {
			continue
		}
		msg, ok := c["message"].(map[string]any)
		if !ok {
			continue
		}
		sanitizeMessageText(msg)
		syncReasoning(msg)
		// OpenAI chat.completion: every assistant message carries `content`
		// (string or null). Some free-tier upstreams omit the field entirely
		// on empty completions (e.g. `{"role":"assistant"}` with no content
		// and no tool_calls), which strict OpenAI clients reject. Default to
		// null when absent without tool_calls — mirroring opencode's
		// lowerAssistantMessage (`content.length === 0 ? null : ...`).
		if _, hasContent := msg["content"]; !hasContent {
			if _, hasToolCalls := msg["tool_calls"]; !hasToolCalls {
				msg["content"] = nil
			}
		}
	}
}

// syncReasoning copies `reasoning_content` into `reasoning` when the
// latter is absent, so clients that only read the `reasoning` field
// still get the text. `reasoning_content` is preserved because
// providers like DeepSeek require it to be passed back through
// conversation history in thinking mode; stripping it causes
// subsequent requests to be rejected.
//
// If neither field is present, `reasoning` is set to nil so the JSON
// encoder emits the key.
func syncReasoning(m map[string]any) {
	rc, hasRC := m["reasoning_content"]
	_, hasR := m["reasoning"]

	if hasRC && !hasR {
		m["reasoning"] = rc
	}
	if !hasRC && !hasR {
		m["reasoning"] = nil
	}
}

// maxErrorBodySize caps how much of an upstream error body is buffered:
// real error payloads are a few KB, so this prevents a misbehaving
// upstream from ballooning memory while still passing the body through.
const maxErrorBodySize = 1 << 20

// extractErrorMessage pulls a short message out of a typical upstream
// error body: OpenAI-style {"error":{"message":"…"}}, a top-level
// "message", or a small plain-text body. Returns "" if nothing usable.
func extractErrorMessage(body []byte) string {
	body = bytes.TrimSpace(body)
	if len(body) == 0 {
		return ""
	}
	var v struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &v); err == nil {
		if v.Error.Message != "" {
			return truncateMessage(v.Error.Message)
		}
		if v.Message != "" {
			return truncateMessage(v.Message)
		}
	}
	if len(body) <= 512 {
		return truncateMessage(string(body))
	}
	return ""
}

// truncateMessage caps a logged error message so one noisy upstream error
// cannot flood the dashboard table.
func truncateMessage(s string) string {
	const max = 300
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"freegate/internal/httputil"
	"freegate/internal/translate/claude"
)

// TokenUsage holds token counts extracted from an upstream response.
type TokenUsage struct {
	Prompt     int
	Completion int
	Total      int
}

func copyNormalized(w http.ResponseWriter, resp *http.Response) (TokenUsage, error) {
	ct := resp.Header.Get("Content-Type")
	isStreaming := strings.Contains(ct, "text/event-stream")

	if isStreaming {
		rd := bufio.NewReader(resp.Body)
		if isAnthropicSSE(rd) {
			return normalizeClaudeStream(w, rd), nil
		}
		return normalizeOpenAIStream(w, rd), nil
	}
	return normalizeJSON(w, resp.Body), nil
}

// isAnthropicSSE peeks at the stream to check if it starts with "event:",
// which indicates Anthropic/Claude SSE format vs OpenAI SSE format.
func isAnthropicSSE(rd *bufio.Reader) bool {
	peek, err := rd.Peek(6)
	if err != nil {
		return false
	}
	return bytes.HasPrefix(peek, []byte("event:"))
}

func normalizeOpenAIStream(dst io.Writer, rd *bufio.Reader) TokenUsage {
	fl, _ := dst.(http.Flusher)
	var usage TokenUsage

	// Buffer per-index tool-call arguments so malformed JSON emitted across
	// incremental deltas can be repaired into a single valid object before
	// the client parses it. Models such as tencent/hy3-free stream tool args
	// as fragments that, joined, are not valid JSON, causing the client to
	// fail with "input JSON failed to parse". The repaired arguments are
	// emitted as one delta when the stream finishes (finish_reason or [DONE]).
	toolArgs := make(map[int]*strings.Builder)
	toolSeen := make(map[int]bool)
	var metaID, metaModel string
	var metaCreated int64
	metaCaptured := false
	finished := false
	seenFinish := false
	hasToolSeen := false

	emitRepaired := func() {
		if finished {
			return
		}
		finished = true
		for i := range toolSeen {
			b := toolArgs[i]
			if b == nil || b.Len() == 0 {
				continue
			}
			repaired := claude.RepairToolArgs(b.String())
			chunk := buildOpenAIChunk(metaID, metaModel, metaCreated, map[string]any{
				"tool_calls": []any{map[string]any{
					"index":    i,
					"function": map[string]any{"arguments": repaired},
				}},
			})
			if _, werr := io.WriteString(dst, chunk); werr != nil {
				slog.Warn("stream write error", "error", werr)
				return
			}
			if fl != nil {
				fl.Flush()
			}
		}
	}

	for {
		line, err := rd.ReadString('\n')
		if err != nil && err != io.EOF {
			slog.Warn("stream read error", "error", err)
			break
		}

		trimmed := strings.TrimRight(line, "\r\n")
		if trimmed == "" {
			if err == io.EOF {
				break
			}
			continue
		}

		usage = extractUsageFromSSE(line, usage)

		if !strings.HasPrefix(trimmed, "data: ") {
			// Non-data line (blank, comments, event: markers) — pass through.
			if _, werr := io.WriteString(dst, line); werr != nil {
				slog.Warn("stream write error", "error", werr)
				break
			}
			if fl != nil {
				fl.Flush()
			}
			if err == io.EOF {
				break
			}
			continue
		}

		data := strings.TrimPrefix(trimmed, "data: ")
		data = strings.TrimRight(data, "\r\n ")
		if data == "[DONE]" {
			if !seenFinish && (metaCaptured || hasToolSeen) {
				fr := "stop"
				if hasToolSeen {
					fr = "tool_calls"
				}
				synth := map[string]any{"choices": []any{map[string]any{"index": 0, "delta": map[string]any{}, "finish_reason": fr}}}
				if metaID != "" {
					synth["id"] = metaID
				}
				if metaModel != "" {
					synth["model"] = metaModel
				}
				if metaCreated != 0 {
					synth["created"] = metaCreated
				}
				synth["object"] = "chat.completion.chunk"
				if b, err := json.Marshal(synth); err == nil {
					emitRepaired()
					_, _ = io.WriteString(dst, "data: "+string(b)+"\n\n")
					if fl != nil {
						fl.Flush()
					}
				}
			} else {
				emitRepaired()
			}
			if _, werr := io.WriteString(dst, "data: [DONE]\n\n"); werr != nil {
				slog.Warn("stream write error", "error", werr)
				break
			}
			if fl != nil {
				fl.Flush()
			}
			if err == io.EOF {
				break
			}
			continue
		}

		var chunk map[string]any
		if json.Unmarshal([]byte(data), &chunk) != nil {
			// Unparseable data line — pass through unchanged.
			if _, werr := io.WriteString(dst, "data: "+data+"\n\n"); werr != nil {
				slog.Warn("stream write error", "error", werr)
				break
			}
			if fl != nil {
				fl.Flush()
			}
			if err == io.EOF {
				break
			}
			continue
		}

		// Normalize reasoning before any empty-check bookkeeping.
		if !metaCaptured {
			if v, ok := chunk["id"].(string); ok {
				metaID = v
			}
			if v, ok := chunk["model"].(string); ok {
				metaModel = v
			}
			if v, ok := chunk["created"].(float64); ok {
				metaCreated = int64(v)
			}
			metaCaptured = true
		}

		// Note: finish_reason is only treated as "real" when it's a
		// non-empty string. Upstreams (and the OpenAI spec itself) send
		// `finish_reason: null` explicitly on every non-terminal delta —
		// that is NOT a signal that the stream is done, so it must not be
		// mutated or treated as terminal here. If a genuinely buggy
		// upstream never sends a real finish_reason at all (e.g. a single
		// null/empty chunk followed by [DONE] or EOF), the fallback
		// synthesis below and at [DONE]/EOF appends a proper terminal
		// chunk without corrupting the chunk that carried content or
		// tool-call fragments.
		finishReason := ""
		if choices, ok := chunk["choices"].([]any); ok && len(choices) > 0 {
			if c, ok := choices[0].(map[string]any); ok {
				if fr, ok := c["finish_reason"].(string); ok {
					finishReason = fr
				}
				if delta, ok := c["delta"].(map[string]any); ok {
					bufferToolArgs(delta, toolArgs, toolSeen)
					syncDeltaReasoning(chunk)
				}
			}
		}

		if finishReason != "" {
			seenFinish = true
		}
		if len(toolSeen) > 0 {
			hasToolSeen = true
		}

		// Flush repaired arguments BEFORE the finish chunk so the client
		// sees the full tool-call arguments before stop_reason.
		if finishReason != "" {
			emitRepaired()
		}

		transformed, merr := json.Marshal(chunk)
		if merr != nil {
			transformed = []byte(data)
		}
		if _, werr := io.WriteString(dst, "data: "+string(transformed)+"\n\n"); werr != nil {
			slog.Warn("stream write error", "error", werr)
			break
		}
		if fl != nil {
			fl.Flush()
		}

		if err == io.EOF {
			break
		}
	}
	// If the stream ended without a terminal chunk (upstream truncated or
	// missing finish_reason), synthesize one so opencode's
	// "missing finish_reason for choice 0" validator doesn't reject the
	// response. Mirrors opencode's onHalt→finishEvents fallback.
	if !seenFinish && (metaCaptured || hasToolSeen) {
		fr := "stop"
		if hasToolSeen {
			fr = "tool_calls"
		}
		emitTerminalChunk(dst, fl, metaID, metaModel, metaCreated, fr)
	}
	return usage
}

// emitTerminalChunk writes a synthetic finish chunk for streams that ended
// without one (e.g. muse-spark empty completion, tencent/hy3 truncation,
// upstream close before finish_reason). Mirrors opencode's
// llm/protocols/openai-chat.ts onHalt → finishEvents.
//
// finish_reason must be set on the choice itself (not nested inside
// delta) — that's the only field downstream consumers (OpenAI clients,
// and claude.ProcessChunk's OpenAI→Claude SSE translator) ever look at
// to detect the terminal chunk. buildOpenAIChunk always sets the
// choice-level finish_reason to nil, so it can't be reused here.
func emitTerminalChunk(dst io.Writer, fl http.Flusher, id, model string, created int64, finishReason string) {
	chunk := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": finishReason,
		}},
	}
	b, err := json.Marshal(chunk)
	if err != nil {
		slog.Warn("stream marshal error", "error", err)
		return
	}
	if _, werr := io.WriteString(dst, "data: "+string(b)+"\n\n"); werr != nil {
		slog.Warn("stream write error", "error", werr)
		return
	}
	if fl != nil {
		fl.Flush()
	}
}

// bufferToolArgs accumulates tool-call arguments from a delta into per-index
// buffers, and removes the (still-incremental) arguments from the delta so
// they are not written to the client until repaired and flushed at finish.
// The id and name are left in place so the client still sees them.
func bufferToolArgs(delta map[string]any, toolArgs map[int]*strings.Builder, toolSeen map[int]bool) {
	tcs, ok := delta["tool_calls"].([]any)
	if !ok {
		return
	}
	for _, tcAny := range tcs {
		tc, ok := tcAny.(map[string]any)
		if !ok {
			continue
		}
		idx, _ := tc["index"].(float64)
		i := int(idx)
		toolSeen[i] = true
		fn, ok := tc["function"].(map[string]any)
		if !ok {
			continue
		}
		if args, ok := fn["arguments"].(string); ok && args != "" {
			b := toolArgs[i]
			if b == nil {
				b = &strings.Builder{}
				toolArgs[i] = b
			}
			b.WriteString(args)
		}
		// Strip arguments from the line we emit now; the repaired full
		// arguments are emitted later via emitRepaired.
		delete(fn, "arguments")
	}
}

// buildOpenAIChunk renders a single OpenAI chat.completion.chunk SSE record
// carrying the given delta (used to emit repaired tool-call arguments).
func buildOpenAIChunk(id, model string, created int64, delta map[string]any) string {
	chunk := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   model,
		"choices": []any{map[string]any{
			"index":         0,
			"delta":         delta,
			"finish_reason": nil,
		}},
	}
	b, err := json.Marshal(chunk)
	if err != nil {
		return ""
	}
	return "data: " + string(b) + "\n\n"
}

// normalizeClaudeStream translates Anthropic/Claude SSE events into
// OpenAI chat.completion.chunk SSE lines using the existing claude
// streaming translator and writes them to dst.
func normalizeClaudeStream(dst io.Writer, src *bufio.Reader) TokenUsage {
	fl, _ := dst.(http.Flusher)
	state := claude.NewClaudeToOpenAIState()
	var usage TokenUsage

	for {
		line, err := src.ReadString('\n')
		if err != nil && err != io.EOF {
			slog.Warn("claude stream read error", "error", err)
			break
		}

		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			if err == io.EOF {
				break
			}
			continue
		}

		// Only process data: lines; skip event: and others
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		data = strings.TrimRight(data, "\r\n ")

		var chunk map[string]any
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}

		// Extract usage from Claude events for TokenUsage reporting
		eventType, _ := chunk["type"].(string)
		switch eventType {
		case "message_start":
			if msg, ok := chunk["message"].(map[string]any); ok {
				usage = extractClaudeUsage(msg, usage)
			}
		case "message_delta":
			if u, ok := chunk["usage"].(map[string]any); ok {
				usage = extractClaudeUsage(u, usage)
			}
		}

		events := state.ProcessChunk(chunk)
		for _, evt := range events {
			if _, werr := io.WriteString(dst, evt); werr != nil {
				slog.Warn("claude stream write error", "error", werr)
				return usage
			}
			if fl != nil {
				fl.Flush()
			}
		}

		if err == io.EOF {
			break
		}
	}

	// Send the terminal [DONE] marker for OpenAI clients
	if _, err := io.WriteString(dst, "data: [DONE]\n\n"); err == nil {
		if fl != nil {
			fl.Flush()
		}
	}

	return usage
}

// extractClaudeUsage parses Claude-style usage (input_tokens,
// output_tokens) and merges into the running TokenUsage.
func extractClaudeUsage(m map[string]any, current TokenUsage) TokenUsage {
	if v, ok := asInt(m["input_tokens"]); ok {
		current.Prompt = v
	}
	if v, ok := asInt(m["output_tokens"]); ok {
		current.Completion = v
	}
	current.Total = current.Prompt + current.Completion
	return current
}

// asInt tries to coerce a JSON-decoded value (float64) to int.
func asInt(v any) (int, bool) {
	switch n := v.(type) {
	case float64:
		return int(n), true
	case int:
		return n, true
	case int64:
		return int(n), true
	}
	return 0, false
}

// extractUsageFromSSE checks if line contains a data: JSON with usage.
func extractUsageFromSSE(line string, current TokenUsage) TokenUsage {
	if !strings.HasPrefix(line, "data: ") {
		return current
	}
	data := strings.TrimPrefix(line, "data: ")
	data = strings.TrimRight(data, "\r\n")
	if data == "[DONE]" {
		return current
	}
	var chunk struct {
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return current
	}
	if chunk.Usage != nil {
		current.Prompt = chunk.Usage.PromptTokens
		current.Completion = chunk.Usage.CompletionTokens
		current.Total = chunk.Usage.TotalTokens
	}
	return current
}

func normalizeSSELine(line string) string {
	if !strings.HasPrefix(line, "data: ") {
		return line
	}

	data := strings.TrimPrefix(line, "data: ")
	data = strings.TrimRight(data, "\r\n")

	if data == "[DONE]" {
		return line
	}

	var chunk map[string]interface{}
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return line
	}

	syncDeltaReasoning(chunk)

	transformed, err := json.Marshal(chunk)
	if err != nil {
		return line
	}

	ending := line[len(line)-1:]
	if len(line) > 1 && line[len(line)-2] == '\r' {
		ending = "\r\n"
	}
	return "data: " + string(transformed) + ending
}

func syncDeltaReasoning(chunk map[string]interface{}) {
	choices, _ := chunk["choices"].([]interface{})
	for _, choice := range choices {
		c, ok := choice.(map[string]interface{})
		if !ok {
			continue
		}
		delta, ok := c["delta"].(map[string]interface{})
		if !ok {
			continue
		}
		syncReasoning(delta)
	}
}

func normalizeJSON(dst io.Writer, src io.Reader) TokenUsage {
	body, err := io.ReadAll(src)
	if err != nil {
		slog.Warn("failed to read response body", "error", err)
		dst.Write(body)
		return TokenUsage{}
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(body, &resp); err != nil {
		dst.Write(body)
		return TokenUsage{}
	}

	// Extract usage before normalizing
	usage := TokenUsage{}
	if u, ok := resp["usage"].(map[string]interface{}); ok {
		if p, ok := u["prompt_tokens"].(float64); ok {
			usage.Prompt = int(p)
		}
		if c, ok := u["completion_tokens"].(float64); ok {
			usage.Completion = int(c)
		}
		if t, ok := u["total_tokens"].(float64); ok {
			usage.Total = int(t)
		}
	}

	syncMessageReasoning(resp)
	repairToolCallsJSON(resp)
	ensureFinishReason(resp)

	transformed, err := json.Marshal(resp)
	if err != nil {
		dst.Write(body)
		return usage
	}

	dst.Write(transformed)
	return usage
}

// ensureFinishReason synthesizes a finish_reason when the upstream omitted
// it (or sent null / empty), so strict OpenAI clients (opencode's
// llm/protocols/openai-chat.ts "missing finish_reason for choice 0"
// validator) don't fail the stream. Mirrors opencode's onHalt
// → finishEvents which defaults to "stop" when no finish was seen.
// When a tool_calls choice is present without a finish_reason we default
// to "tool_calls" so callers treat it as a completed tool call, matching
// opencode's hasToolCalls → tool-calls coalescing in finishEvents.
func ensureFinishReason(resp map[string]interface{}) {
	choices, _ := resp["choices"].([]interface{})
	for i, cAny := range choices {
		choice, ok := cAny.(map[string]interface{})
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
		if msg, _ := choice["message"].(map[string]interface{}); msg != nil {
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
func repairToolCallsJSON(resp map[string]interface{}) {
	choices, ok := resp["choices"].([]interface{})
	if !ok {
		return
	}
	for _, c := range choices {
		choice, ok := c.(map[string]interface{})
		if !ok {
			continue
		}
		msg, ok := choice["message"].(map[string]interface{})
		if !ok {
			continue
		}
		tcs, ok := msg["tool_calls"].([]interface{})
		if !ok {
			continue
		}
		for _, tcAny := range tcs {
			tc, ok := tcAny.(map[string]interface{})
			if !ok {
				continue
			}
			fn, ok := tc["function"].(map[string]interface{})
			if !ok {
				continue
			}
			args, ok := fn["arguments"].(string)
			if !ok || args == "" {
				continue
			}
			fn["arguments"] = claude.RepairToolArgs(args)
		}
	}
}

func syncMessageReasoning(resp map[string]interface{}) {
	choices, _ := resp["choices"].([]interface{})
	for _, choice := range choices {
		c, ok := choice.(map[string]interface{})
		if !ok {
			continue
		}
		msg, ok := c["message"].(map[string]interface{})
		if !ok {
			continue
		}
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
func syncReasoning(m map[string]interface{}) {
	rc, hasRC := m["reasoning_content"]
	_, hasR := m["reasoning"]

	if hasRC && !hasR {
		m["reasoning"] = rc
	}
	if !hasRC && !hasR {
		m["reasoning"] = nil
	}
}

// NormalizeResponse copies headers from the upstream response, calls
// WriteHeader, and streams the response body through reasoning-field
// normalization. It owns the response body and closes it before
// returning. TokenUsage is reported so callers can update metrics.
func NormalizeResponse(w http.ResponseWriter, resp *http.Response) (TokenUsage, error) {
	httputil.CopyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	return copyNormalized(w, resp)
}

// PassThroughError copies an upstream error response (429/5xx) to the
// client verbatim and returns a short readable message captured from the
// body, so the request log can record what the upstream actually said
// instead of only the bare status code. Returns "" when no usable message
// is present.
func PassThroughError(w http.ResponseWriter, resp *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
	httputil.CopyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
	return extractErrorMessage(body)
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

package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"freegate/internal/domain"
	"freegate/internal/httputil"
	"freegate/internal/translate/claude"
)

// TokenUsage holds token counts extracted from an upstream response.
type TokenUsage struct {
	Prompt     int
	Completion int
	Total      int
}

func copyNormalizedWithContext(ctx context.Context, w http.ResponseWriter, resp *http.Response) (TokenUsage, error) {
	ct := resp.Header.Get("Content-Type")
	isStreaming := strings.Contains(ct, "text/event-stream")
	model, reqID := correlationMeta(resp.Header)

	if isStreaming {
		rd := bufio.NewReader(resp.Body)
		if isAnthropicSSE(rd) {
			return normalizeClaudeStreamWithContext(ctx, w, rd), nil
		}
		return normalizeOpenAIStreamWithMeta(ctx, w, rd, model, reqID), nil
	}
	return normalizeJSONWithMeta(w, resp.Body, model, reqID), nil
}

func normalizeOpenAIStream(dst io.Writer, rd *bufio.Reader) TokenUsage {
	return normalizeOpenAIStreamWithMeta(context.Background(), dst, rd, "", "")
}

func normalizeClaudeStream(dst io.Writer, src *bufio.Reader) TokenUsage {
	return normalizeClaudeStreamWithContext(context.Background(), dst, src)
}

func copyNormalized(w http.ResponseWriter, resp *http.Response) (TokenUsage, error) {
	return copyNormalizedWithContext(context.Background(), w, resp)
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

// correlationMeta extracts the correlation fields ChatService injects into
// the upstream response headers before normalization, so degenerate-response
// warnings can be tied back to the originating request.
func correlationMeta(h http.Header) (model, requestID string) {
	return h.Get("X-Fg-Model"), h.Get("X-Fg-Request-Id")
}

func normalizeOpenAIStreamWithContext(ctx context.Context, dst io.Writer, rd *bufio.Reader) TokenUsage {
	return normalizeOpenAIStreamWithMeta(ctx, dst, rd, "", "")
}

func normalizeOpenAIStreamWithMeta(ctx context.Context, dst io.Writer, rd *bufio.Reader, model, requestID string) TokenUsage {
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
	sawAnyPayload := false

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
		select {
		case <-ctx.Done():
			slog.Info("stream cancelled", "error", ctx.Err())
			return usage
		default:
		}
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
					if s, _ := delta["content"].(string); s != "" {
						sawAnyPayload = true
					}
					if s, _ := delta["reasoning_content"].(string); s != "" {
						sawAnyPayload = true
					}
					if s, _ := delta["reasoning"].(string); s != "" {
						sawAnyPayload = true
					}
					if tcs, _ := delta["tool_calls"].([]any); len(tcs) > 0 {
						sawAnyPayload = true
					}
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
		// Flush buffered+repaired tool arguments BEFORE the synthesized
		// terminal chunk. On the EOF path (no [DONE], no real
		// finish_reason — e.g. muse-spark truncation) skipping this used
		// to drop every buffered argument, so the translated tool_use
		// reached Claude Code with input {} and failed schema validation
		// ("The required parameter `command` is missing").
		emitRepaired()
		emitTerminalChunk(dst, fl, metaID, metaModel, metaCreated, fr)
	}
	if metaCaptured && !sawAnyPayload {
		// Degenerate upstream response: chunk train carried no content,
		// reasoning, or tool calls at all (observed during the muse-spark
		// outage: llm7 answered an unavailable model with HTTP 200 and a
		// stream of empty choices[] lines). Surface it loudly instead of
		// letting the client see a silently empty assistant turn.
		slog.Warn("upstream empty completion",
			"model", model,
			"request_id", requestID,
			"path", "stream",
		)
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
func normalizeClaudeStreamWithContext(ctx context.Context, dst io.Writer, src *bufio.Reader) TokenUsage {
	fl, _ := dst.(http.Flusher)
	state := claude.NewClaudeToOpenAIState()
	var usage TokenUsage
	stopped := false

	for {
		select {
		case <-ctx.Done():
			slog.Info("claude stream cancelled", "error", ctx.Err())
			return usage
		default:
		}
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

		// Some free-tier upstreams (e.g. mimo) replay the final tool_use
		// block — input_json_delta + content_block_stop, sometimes the
		// terminal message_delta/message_stop again — AFTER the first
		// message_stop. The client has already closed the assistant
		// message; replaying a duplicate tool call after that is what
		// makes the client see doubled tool input (X}{Y). message_stop is
		// terminal: drop everything after it. This mirrors the
		// finishSent guard in the OpenAI-stream path (stream.go).
		if stopped {
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

		eventType, _ := chunk["type"].(string)
		if eventType == "message_stop" {
			stopped = true
		}

		// Extract usage from Claude events for TokenUsage reporting
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
	return normalizeJSONWithMeta(dst, src, "", "")
}

func normalizeJSONWithMeta(dst io.Writer, src io.Reader, model, requestID string) TokenUsage {
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
func isEmptyJSONCompletion(resp map[string]interface{}) bool {
	if _, isErr := resp["error"]; isErr {
		return false
	}
	choices, _ := resp["choices"].([]interface{})
	if len(choices) == 0 {
		return true
	}
	for _, cAny := range choices {
		c, ok := cAny.(map[string]interface{})
		if !ok {
			return true
		}
		msg, _ := c["message"].(map[string]interface{})
		if msg == nil {
			return true
		}
		if s, _ := msg["content"].(string); strings.TrimSpace(s) != "" {
			return false
		}
		if tc, has := msg["tool_calls"].([]interface{}); has && len(tc) > 0 {
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
	return NormalizeResponseWithContext(context.Background(), w, resp)
}

// NormalizeResponseWithContext is context-aware variant that respects cancellation.
func NormalizeResponseWithContext(ctx context.Context, w http.ResponseWriter, resp *http.Response) (TokenUsage, error) {
	httputil.CopyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)
	return copyNormalizedWithContext(ctx, w, resp)
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

// Domain-aware variants — decouple application from net/http.

func copyNormalizedDomainWithContext(ctx context.Context, w http.ResponseWriter, resp *domain.UpstreamResponse) (TokenUsage, error) {
	ct := resp.Header.Get("Content-Type")
	isStreaming := strings.Contains(ct, "text/event-stream")
	model, reqID := correlationMeta(resp.Header)
	if isStreaming {
		rd := bufio.NewReader(resp.Body)
		if isResponsesSSE(rd) {
			return copyPassthroughStream(ctx, w, rd), nil
		}
		if isAnthropicSSE(rd) {
			return normalizeClaudeStreamWithContext(ctx, w, rd), nil
		}
		return normalizeOpenAIStreamWithMeta(ctx, w, rd, model, reqID), nil
	}
	// For non-streaming, peek body to detect Responses API JSON.
	// We need to read without losing data; buffer it.
	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return TokenUsage{}, err
	}
	if isResponsesJSONBytes(bodyBytes) {
		return copyPassthroughJSONBytes(w, bodyBytes), nil
	}
	return normalizeJSONWithMeta(w, bytes.NewReader(bodyBytes), model, reqID), nil
}

// isResponsesSSE peeks at the stream to detect OpenAI Responses API SSE
// (event: response.*). Responses SSE also starts with "event:" like Claude,
// but its event names are response.* vs Claude's message_* / content_block_*.
func isResponsesSSE(rd *bufio.Reader) bool {
	peek, err := rd.Peek(512)
	if err != nil && len(peek) == 0 {
		return false
	}
	return bytes.Contains(peek, []byte("event: response.")) || bytes.Contains(peek, []byte(`"type":"response.`))
}

func isResponsesJSONBytes(b []byte) bool {
	// Responses JSON has "object":"response" and "output" array
	return bytes.Contains(b, []byte(`"object"`)) && bytes.Contains(b, []byte(`"response"`)) && bytes.Contains(b, []byte(`"output"`))
}

func copyPassthroughJSONBytes(dst io.Writer, body []byte) TokenUsage {
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err == nil {
		if u, ok := resp["usage"].(map[string]any); ok {
			var tu TokenUsage
			if p, ok := u["input_tokens"].(float64); ok {
				tu.Prompt = int(p)
			} else if p, ok := u["prompt_tokens"].(float64); ok {
				tu.Prompt = int(p)
			}
			if c, ok := u["output_tokens"].(float64); ok {
				tu.Completion = int(c)
			} else if c, ok := u["completion_tokens"].(float64); ok {
				tu.Completion = int(c)
			}
			tu.Total = tu.Prompt + tu.Completion
			dst.Write(body)
			return tu
		}
	}
	dst.Write(body)
	return TokenUsage{}
}

func copyPassthroughStream(ctx context.Context, dst io.Writer, src *bufio.Reader) TokenUsage {
	// Simple copy of SSE stream without transformation; respects context cancellation.
	buf := make([]byte, 4096)
	for {
		select {
		case <-ctx.Done():
			return TokenUsage{}
		default:
		}
		n, err := src.Read(buf)
		if n > 0 {
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return TokenUsage{}
			}
			if fl, ok := dst.(http.Flusher); ok {
				fl.Flush()
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
	}
	return TokenUsage{}
}

func copyPassthroughJSON(dst io.Writer, src io.Reader) TokenUsage {
	body, err := io.ReadAll(src)
	if err != nil {
		return TokenUsage{}
	}
	// Try to extract usage if present
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err == nil {
		if u, ok := resp["usage"].(map[string]any); ok {
			var tu TokenUsage
			if p, ok := u["input_tokens"].(float64); ok {
				tu.Prompt = int(p)
			} else if p, ok := u["prompt_tokens"].(float64); ok {
				tu.Prompt = int(p)
			}
			if c, ok := u["output_tokens"].(float64); ok {
				tu.Completion = int(c)
			} else if c, ok := u["completion_tokens"].(float64); ok {
				tu.Completion = int(c)
			}
			tu.Total = tu.Prompt + tu.Completion
			dst.Write(body)
			return tu
		}
	}
	dst.Write(body)
	return TokenUsage{}
}

func NormalizeDomainResponseWithContext(ctx context.Context, w http.ResponseWriter, resp *domain.UpstreamResponse) (TokenUsage, error) {
	httputil.CopyHeaders(w.Header(), resp.Header)
	// Strip freegate-internal correlation headers so they never reach the
	// client; they exist only to label normalization warnings.
	w.Header().Del("X-Fg-Model")
	w.Header().Del("X-Fg-Request-Id")
	// The normalized body we write below differs in size from the raw
	// upstream payload (reasoning sync, finish_reason synthesis, JSON
	// re-marshalling), so a copied Content-Length would be wrong and
	// clients would abort with a content-length mismatch. Drop it and let
	// net/http compute the real value.
	w.Header().Del("Content-Length")
	w.WriteHeader(resp.StatusCode)
	return copyNormalizedDomainWithContext(ctx, w, resp)
}

func PassThroughDomainError(w http.ResponseWriter, resp *domain.UpstreamResponse) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodySize))
	httputil.CopyHeaders(w.Header(), resp.Header)
	// Same as above: the body may be truncated at maxErrorBodySize or
	// re-serialized downstream, so never trust the upstream's length.
	w.Header().Del("Content-Length")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
	return extractErrorMessage(body)
}

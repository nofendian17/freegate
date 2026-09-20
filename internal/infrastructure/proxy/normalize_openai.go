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

	"freegate/internal/translate/claude"
)

// TokenUsage holds token counts extracted from an upstream response.
type TokenUsage struct {
	Prompt     int
	Completion int
	Total      int
}

// extractUsageFromMap extracts token usage from a usage map, handling both
// OpenAI (prompt_tokens/completion_tokens/total_tokens) and Responses API
// (input_tokens/output_tokens) field names.
func extractUsageFromMap(u map[string]any) TokenUsage {
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
	if t, ok := u["total_tokens"].(float64); ok {
		tu.Total = int(t)
	} else {
		tu.Total = tu.Prompt + tu.Completion
	}
	return tu
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
			// Sanitize after repair (vllm#56302): values end at the
			// first DSML sigil so no markup reaches the tool.
			repaired := claude.SanitizeToolArgs(claude.RepairToolArgs(b.String()))
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
					sanitizeDeltaText(delta)
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

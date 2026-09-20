package proxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"freegate/internal/domain"
	"freegate/internal/httputil"
	"freegate/internal/translate"
)

// Domain-aware variants — decouple application from net/http.

func copyNormalizedDomainWithContext(ctx context.Context, w http.ResponseWriter, resp *domain.UpstreamResponse) (TokenUsage, error) {
	ct := resp.Header.Get("Content-Type")
	isStreaming := strings.Contains(ct, "text/event-stream")
	model, reqID := correlationMeta(resp.Header)
	if resp.Format == string(translate.FormatClaude) {
		if isStreaming {
			return copyPassthroughClaudeStream(ctx, w, bufio.NewReader(resp.Body)), nil
		}
		return copyPassthroughJSON(w, resp.Body), nil
	}
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
	if ok, usage := isResponsesJSONBytes(bodyBytes); ok {
		return copyPassthroughJSONBytes(w, bodyBytes, usage), nil
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

func isResponsesJSONBytes(b []byte) (bool, TokenUsage) {
	// Responses JSON has "object":"response" and "output" array.
	// Use proper JSON parsing instead of naive substring matching to avoid
	// false positives on regular chat completions that happen to contain
	// these substrings in nested fields.
	// Also extract usage during this pass to avoid a second unmarshal in
	// copyPassthroughJSONBytes.
	var probe struct {
		Object string          `json:"object"`
		Output json.RawMessage `json:"output"`
		Usage  json.RawMessage `json:"usage"`
	}
	if err := json.Unmarshal(b, &probe); err != nil {
		return false, TokenUsage{}
	}
	if probe.Object != "response" || len(probe.Output) == 0 {
		return false, TokenUsage{}
	}
	var usage TokenUsage
	if len(probe.Usage) > 0 {
		var u map[string]any
		if json.Unmarshal(probe.Usage, &u) == nil {
			usage = extractUsageFromMap(u)
		}
	}
	return true, usage
}

func copyPassthroughJSONBytes(dst io.Writer, body []byte, usage TokenUsage) TokenUsage {
	dst.Write(body)
	return usage
}

func copyPassthroughStream(ctx context.Context, dst io.Writer, src *bufio.Reader) TokenUsage {
	// Copy SSE stream while extracting Responses usage from the final
	// response.completed event. Instead of buffering the entire stream,
	// we scan SSE lines incrementally and only keep the last usage found.
	var usage TokenUsage
	var lineBuf []byte
	buf := make([]byte, 4096)
	for {
		select {
		case <-ctx.Done():
			return usage
		default:
		}
		n, err := src.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if _, werr := dst.Write(chunk); werr != nil {
				return usage
			}
			if fl, ok := dst.(http.Flusher); ok {
				fl.Flush()
			}
			// Scan for complete SSE lines and check for response.completed
			lineBuf = append(lineBuf, chunk...)
			for {
				idx := bytes.IndexByte(lineBuf, '\n')
				if idx < 0 {
					break
				}
				line := lineBuf[:idx]
				lineBuf = lineBuf[idx+1:]
				if u := extractUsageFromCompletedLine(line); u != nil {
					usage = *u
				}
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
	}
	return usage
}

// copyPassthroughClaudeStream copies a raw Claude Messages SSE stream while
// extracting token usage from message_start/message_delta events. The bytes
// pass through untouched so the translate layer downstream still sees the
// Claude event shapes it expects.
func copyPassthroughClaudeStream(ctx context.Context, dst io.Writer, src *bufio.Reader) TokenUsage {
	var usage TokenUsage
	var lineBuf []byte
	buf := make([]byte, 4096)
	for {
		select {
		case <-ctx.Done():
			return usage
		default:
		}
		n, err := src.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if _, werr := dst.Write(chunk); werr != nil {
				return usage
			}
			if fl, ok := dst.(http.Flusher); ok {
				fl.Flush()
			}
			lineBuf = append(lineBuf, chunk...)
			for {
				idx := bytes.IndexByte(lineBuf, '\n')
				if idx < 0 {
					break
				}
				line := lineBuf[:idx]
				lineBuf = lineBuf[idx+1:]
				usage = extractClaudeUsageFromSSELine(string(line), usage)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			break
		}
	}
	return usage
}

// extractClaudeUsageFromSSELine folds Claude message_start/message_delta
// usage payloads into the running total. Non-usage lines are ignored.
func extractClaudeUsageFromSSELine(line string, current TokenUsage) TokenUsage {
	if !strings.HasPrefix(line, "data: ") {
		return current
	}
	data := strings.TrimPrefix(line, "data: ")
	data = strings.TrimRight(data, "\r\n")
	var evt map[string]any
	if err := json.Unmarshal([]byte(data), &evt); err != nil {
		return current
	}
	switch evt["type"] {
	case "message_start":
		if msg, ok := evt["message"].(map[string]any); ok {
			if u, ok := msg["usage"].(map[string]any); ok {
				return extractClaudeUsage(u, current)
			}
		}
	case "message_delta":
		if u, ok := evt["usage"].(map[string]any); ok {
			return extractClaudeUsage(u, current)
		}
	}
	return current
}

// extractUsageFromCompletedLine checks if an SSE line contains a
// response.completed event with usage data, and returns it if found.
func extractUsageFromCompletedLine(line []byte) *TokenUsage {
	if !bytes.HasPrefix(line, []byte("data: ")) {
		return nil
	}
	data := bytes.TrimPrefix(line, []byte("data: "))
	data = bytes.TrimRight(data, "\r\n")
	if len(data) == 0 || data[0] != '{' {
		return nil
	}
	// Pre-filter: only response.completed events carry usage data.
	// This avoids json.Unmarshal on every delta event (200-500+ per stream).
	if !bytes.Contains(data, []byte("response.completed")) {
		return nil
	}
	var evt map[string]any
	if err := json.Unmarshal(data, &evt); err != nil {
		return nil
	}
	// Must be a response.completed event
	if evt["type"] != "response.completed" {
		return nil
	}
	var u map[string]any
	if resp, ok := evt["response"].(map[string]any); ok {
		if usageMap, ok := resp["usage"].(map[string]any); ok {
			u = usageMap
		}
	} else if usageMap, ok := evt["usage"].(map[string]any); ok {
		u = usageMap
	}
	if u == nil {
		return nil
	}
	tu := extractUsageFromMap(u)
	return &tu
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
			tu := extractUsageFromMap(u)
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

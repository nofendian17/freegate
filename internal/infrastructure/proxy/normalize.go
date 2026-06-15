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

	for {
		line, err := rd.ReadString('\n')
		if err != nil && err != io.EOF {
			slog.Warn("stream read error", "error", err)
			break
		}

		if len(line) > 0 {
			usage = extractUsageFromSSE(line, usage)
			normalized := normalizeSSELine(line)
			if _, werr := io.WriteString(dst, normalized); werr != nil {
				slog.Warn("stream write error", "error", werr)
				break
			}
			if fl != nil {
				fl.Flush()
			}
		}

		if err == io.EOF {
			break
		}
	}
	return usage
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

	transformed, err := json.Marshal(resp)
	if err != nil {
		dst.Write(body)
		return usage
	}

	dst.Write(transformed)
	return usage
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

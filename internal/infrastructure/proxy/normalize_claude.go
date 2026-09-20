package proxy

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"freegate/internal/translate/claude"
)

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
			InputTokens      int `json:"input_tokens"`
			OutputTokens     int `json:"output_tokens"`
		} `json:"usage"`
	}
	if err := json.Unmarshal([]byte(data), &chunk); err != nil {
		return current
	}
	if chunk.Usage != nil {
		if chunk.Usage.PromptTokens > 0 {
			current.Prompt = chunk.Usage.PromptTokens
		} else {
			current.Prompt = chunk.Usage.InputTokens
		}
		if chunk.Usage.CompletionTokens > 0 {
			current.Completion = chunk.Usage.CompletionTokens
		} else {
			current.Completion = chunk.Usage.OutputTokens
		}
		if chunk.Usage.TotalTokens > 0 {
			current.Total = chunk.Usage.TotalTokens
		} else {
			current.Total = current.Prompt + current.Completion
		}
	}
	return current
}

func syncDeltaReasoning(chunk map[string]any) {
	choices, _ := chunk["choices"].([]any)
	for _, choice := range choices {
		c, ok := choice.(map[string]any)
		if !ok {
			continue
		}
		delta, ok := c["delta"].(map[string]any)
		if !ok {
			continue
		}
		syncReasoning(delta)
	}
}

// sanitizeDeltaText strips agentic scaffolding leaked by free-tier models
// (notably DeepSeek: <system-reminder>, <feature-flag>, DSML tags, git
// markers) from assistant text fields. Tool-call arguments are never
// touched — stripping there would corrupt JSON.
func sanitizeDeltaText(delta map[string]any) {
	for _, k := range []string{"content", "reasoning_content", "reasoning"} {
		if s, ok := delta[k].(string); ok && s != "" {
			if cleaned := claude.SanitizeAssistantText(s); cleaned != s {
				delta[k] = cleaned
			}
		}
	}
}

// sanitizeMessageText is the non-streaming counterpart of
// sanitizeDeltaText: it cleans the assistant message object, handling
// both string content and OpenAI content-part arrays.
func sanitizeMessageText(msg map[string]any) {
	for _, k := range []string{"content", "reasoning_content", "reasoning"} {
		switch v := msg[k].(type) {
		case string:
			if v != "" {
				if cleaned := claude.SanitizeAssistantText(v); cleaned != v {
					msg[k] = cleaned
				}
			}
		case []any:
			for _, pAny := range v {
				p, ok := pAny.(map[string]any)
				if !ok {
					continue
				}
				if t, _ := p["text"].(string); t != "" {
					if cleaned := claude.SanitizeAssistantText(t); cleaned != t {
						p["text"] = cleaned
					}
				}
			}
		}
	}
}

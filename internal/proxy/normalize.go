package proxy

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

// TokenUsage holds token counts extracted from an upstream response.
type TokenUsage struct {
	Prompt     int
	Completion int
	Total      int
}

func copyNormalized(dst http.ResponseWriter, src *http.Response, requestID string) TokenUsage {
	ct := src.Header.Get("Content-Type")
	isStreaming := strings.Contains(ct, "text/event-stream")

	if isStreaming {
		return normalizeStream(dst, src.Body, requestID)
	}
	return normalizeJSON(dst, src.Body, requestID)
}

func normalizeStream(dst io.Writer, src io.Reader, requestID string) TokenUsage {
	fl, _ := dst.(http.Flusher)
	rd := bufio.NewReader(src)
	var usage TokenUsage

	for {
		line, err := rd.ReadString('\n')
		if err != nil && err != io.EOF {
			slog.Warn("stream read error", "request_id", requestID, "error", err)
			break
		}

		if len(line) > 0 {
			// Check for usage in this SSE line before normalizing
			usage = extractUsageFromSSE(line, usage)
			normalized := normalizeSSELine(line)
			if _, werr := io.WriteString(dst, normalized); werr != nil {
				slog.Warn("stream write error", "request_id", requestID, "error", werr)
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

func normalizeJSON(dst io.Writer, src io.Reader, requestID string) TokenUsage {
	body, err := io.ReadAll(src)
	if err != nil {
		slog.Warn("failed to read response body", "request_id", requestID, "error", err)
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

func syncReasoning(m map[string]interface{}) {
	_, hasRC := m["reasoning_content"]
	_, hasR := m["reasoning"]

	if hasRC && !hasR {
		m["reasoning"] = m["reasoning_content"]
	} else if hasR && !hasRC {
		m["reasoning_content"] = m["reasoning"]
	} else if !hasRC && !hasR {
		m["reasoning"] = nil
		m["reasoning_content"] = nil
	}
}

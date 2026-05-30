package proxy

import (
	"bufio"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"
)

func copyNormalized(dst http.ResponseWriter, src *http.Response, requestID string) {
	ct := src.Header.Get("Content-Type")
	isStreaming := strings.Contains(ct, "text/event-stream")

	if isStreaming {
		normalizeStream(dst, src.Body, requestID)
	} else {
		normalizeJSON(dst, src.Body, requestID)
	}
}

func normalizeStream(dst io.Writer, src io.Reader, requestID string) {
	fl, _ := dst.(http.Flusher)
	rd := bufio.NewReader(src)

	for {
		line, err := rd.ReadString('\n')
		if err != nil && err != io.EOF {
			slog.Warn("stream read error", "request_id", requestID, "error", err)
			break
		}

		if len(line) > 0 {
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

func normalizeJSON(dst io.Writer, src io.Reader, requestID string) {
	body, err := io.ReadAll(src)
	if err != nil {
		slog.Warn("failed to read response body", "request_id", requestID, "error", err)
		dst.Write(body)
		return
	}

	var resp map[string]interface{}
	if err := json.Unmarshal(body, &resp); err != nil {
		dst.Write(body)
		return
	}

	syncMessageReasoning(resp)

	transformed, err := json.Marshal(resp)
	if err != nil {
		dst.Write(body)
		return
	}

	dst.Write(transformed)
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

package translate

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"freegate/internal/httputil"
	"freegate/internal/translate/claude"
)

// ResponseWriter wraps an http.ResponseWriter to translate upstream responses
// from OpenAI format back to the source (Claude/Gemini) format.
// It intercepts Write calls and performs format-specific translation
// for both streaming (SSE) and non-streaming (JSON) responses.
type ResponseWriter struct {
	inner        http.ResponseWriter
	format       Format
	isStream     bool
	statusCode   int
	buf          bytes.Buffer // for non-streaming buffering
	state        *claude.StreamState
	headerWritten bool
}

// NewResponseWriter creates a response translator for the given source format.
func NewResponseWriter(w http.ResponseWriter, format Format) *ResponseWriter {
	return &ResponseWriter{
		inner:  w,
		format: format,
	}
}

func (rw *ResponseWriter) Header() http.Header {
	return rw.inner.Header()
}

func (rw *ResponseWriter) WriteHeader(statusCode int) {
	rw.statusCode = statusCode
	rw.headerWritten = true
	// Only pass through non-200 headers immediately; for 200 we may
	// modify Content-Type and delay headers until first write.
	if statusCode != http.StatusOK {
		httputil.CopyHeaders(rw.inner.Header(), rw.inner.Header())
		rw.inner.WriteHeader(statusCode)
	}
}

func (rw *ResponseWriter) Write(p []byte) (int, error) {
	if rw.statusCode == 0 {
		rw.statusCode = http.StatusOK
	}

	// Error responses pass through untranslated
	if rw.statusCode != http.StatusOK {
		if !rw.headerWritten {
			rw.inner.WriteHeader(rw.statusCode)
		}
		return rw.inner.Write(p)
	}

	// Determine streaming on first write
	if !rw.isStream {
		ct := rw.Header().Get("Content-Type")
		rw.isStream = strings.Contains(ct, "text/event-stream")
	}

	if rw.isStream {
		return rw.writeStream(p)
	}

	// Non-streaming: buffer for later translation
	n, _ := rw.buf.Write(p)
	return n, nil
}

// Close must be called to flush any buffered non-streaming response.
func (rw *ResponseWriter) Close() error {
	if rw.statusCode != http.StatusOK {
		return nil
	}
	if rw.isStream {
		return nil
	}
	if rw.buf.Len() == 0 {
		return nil
	}

	translated, err := ResponseJSON(rw.buf.Bytes(), rw.format)
	if err != nil {
		slog.Warn("translate: json response translation failed, passing through", "error", err)
		translated = rw.buf.Bytes()
	}
	if !rw.headerWritten {
		rw.inner.WriteHeader(http.StatusOK)
	}
	_, err = rw.inner.Write(translated)
	return err
}

// writeStream translates OpenAI SSE bytes → Claude SSE events and writes to inner.
// Each Write() call receives one or more SSE lines (separated by \n).
func (rw *ResponseWriter) writeStream(p []byte) (int, error) {
	if rw.state == nil {
		rw.state = claude.NewStreamState()
	}
	state := rw.state
	if state.IsClosed() {
		return len(p), nil
	}

	// Accumulate incoming bytes and extract complete lines.
	lines := state.Feed(p)

	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			// Non-data lines (event:, empty lines, etc.) pass through
			rw.writeLine(line + "\n")
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		data = strings.TrimRight(data, "\r\n ")

		if data == "[DONE]" {
			state.MarkClosed()
			continue
		}

		var chunk map[string]any
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			rw.writeLine(line + "\n")
			continue
		}

		events := claude.ProcessChunk(chunk, state)
		for _, evt := range events {
			rw.writeLine(evt)
		}
	}
	return len(p), nil
}

func (rw *ResponseWriter) writeLine(s string) {
	if !rw.headerWritten {
		rw.inner.Header().Set("Content-Type", "text/event-stream")
		rw.inner.Header().Set("Cache-Control", "no-cache")
		rw.inner.Header().Set("Connection", "keep-alive")
		rw.inner.WriteHeader(http.StatusOK)
		rw.headerWritten = true
	}
	io.WriteString(rw.inner, s)
	if fl, ok := rw.inner.(http.Flusher); ok {
		fl.Flush()
	}
}

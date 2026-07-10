package translate

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	"freegate/internal/translate/claude"
	"freegate/internal/translate/gemini"
)

// streamState bundles the per-direction streaming state holders. Only
// one of these is non-nil for any (src, dst) pair.
type streamState struct {
	oaiToClaude *claude.StreamState         // src=OpenAI, dst=Claude
	claudeToOAI *claude.ClaudeToOpenAIState // src=Claude,  dst=OpenAI
	oaiToGemini *gemini.StreamState         // src=OpenAI, dst=Gemini
	geminiToOAI *gemini.GeminiToOpenAIState // src=Gemini,  dst=OpenAI
}

// ResponseWriter wraps an http.ResponseWriter to translate upstream
// responses from src format back to dst format. It intercepts Write
// calls and performs format-specific translation for both streaming
// (SSE) and non-streaming (JSON) responses.
//
// All upstreams in the current hub-and-spoke design speak OpenAI, so the
// default NewResponseWriter constructor assumes src=FormatOpenAI. To
// translate from a non-OpenAI upstream, use NewResponseWriterWithDst.
type ResponseWriter struct {
	inner         http.ResponseWriter
	src           Format
	dst           Format
	isStream      bool
	statusCode    int
	buf           bytes.Buffer // for non-streaming buffering
	state         *streamState
	headerWritten bool
}

// NewResponseWriter creates a response translator for the given client
// format. The upstream is assumed to speak OpenAI. This is the
// constructor used by the existing handler; it remains source-compatible.
func NewResponseWriter(w http.ResponseWriter, dst Format) *ResponseWriter {
	return NewResponseWriterWithDst(w, FormatOpenAI, dst)
}

// NewResponseWriterWithDst creates a response translator that
// translates from the upstream's src format to the client's dst format.
func NewResponseWriterWithDst(w http.ResponseWriter, src, dst Format) *ResponseWriter {
	return &ResponseWriter{
		inner: w,
		src:   src,
		dst:   dst,
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

	translated, err := ResponseJSON(rw.buf.Bytes(), rw.src, rw.dst)
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

// writeStream translates SSE bytes from src format to dst format and
// writes to inner. Each Write() call receives one or more SSE lines.
func (rw *ResponseWriter) writeStream(p []byte) (int, error) {
	if rw.state == nil {
		rw.state = &streamState{}
	}

	switch {
	case rw.src == FormatOpenAI && rw.dst == FormatClaude:
		return rw.streamOpenAIToClaude(p)
	case rw.src == FormatClaude && rw.dst == FormatOpenAI:
		return rw.streamClaudeToOpenAI(p)
	case rw.src == FormatOpenAI && rw.dst == FormatGemini:
		return rw.streamOpenAIToGemini(p)
	case rw.src == FormatGemini && rw.dst == FormatOpenAI:
		return rw.streamGeminiToOpenAI(p)
	case rw.src == rw.dst:
		// Pass-through.
		return rw.passThrough(p)
	}

	// Two-hop (e.g. Claude → Gemini streaming): not supported. Pass
	// through the raw bytes.
	slog.Warn("translate: streaming two-hop translation not supported, passing through",
		"src", rw.src, "dst", rw.dst)
	return rw.passThrough(p)
}

func (rw *ResponseWriter) passThrough(p []byte) (int, error) {
	if !rw.headerWritten {
		rw.inner.Header().Set("Content-Type", "text/event-stream")
		rw.inner.WriteHeader(http.StatusOK)
		rw.headerWritten = true
	}
	return rw.inner.Write(p)
}

// --- OpenAI → Claude streaming ---

func (rw *ResponseWriter) streamOpenAIToClaude(p []byte) (int, error) {
	if rw.state.oaiToClaude == nil {
		rw.state.oaiToClaude = claude.NewStreamState()
	}
	state := rw.state.oaiToClaude
	if state.IsClosed() {
		return len(p), nil
	}

	lines := state.Feed(p)

	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
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

// --- Claude → OpenAI streaming ---

func (rw *ResponseWriter) streamClaudeToOpenAI(p []byte) (int, error) {
	if rw.state.claudeToOAI == nil {
		rw.state.claudeToOAI = claude.NewClaudeToOpenAIState()
	}
	state := rw.state.claudeToOAI
	if state == nil {
		return len(p), nil
	}

	lines := state.Feed(p)

	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		data = strings.TrimRight(data, "\r\n ")

		if data == "[DONE]" {
			continue
		}

		var chunk map[string]any
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			rw.writeLine(line + "\n")
			continue
		}

		events := state.ProcessChunk(chunk)
		for _, evt := range events {
			rw.writeLine(evt)
		}
	}
	return len(p), nil
}

// --- OpenAI → Gemini streaming ---

func (rw *ResponseWriter) streamOpenAIToGemini(p []byte) (int, error) {
	if rw.state.oaiToGemini == nil {
		rw.state.oaiToGemini = gemini.NewStreamState()
	}
	state := rw.state.oaiToGemini
	if state == nil {
		return len(p), nil
	}

	lines := state.Feed(p)

	for _, line := range lines {
		if !strings.HasPrefix(line, "data: ") {
			continue
		}

		data := strings.TrimPrefix(line, "data: ")
		data = strings.TrimRight(data, "\r\n ")
		if data == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			rw.writeLine(line + "\n")
			continue
		}
		events := gemini.ProcessChunk(chunk, state)
		for _, evt := range events {
			rw.writeLine(evt)
		}
	}
	return len(p), nil
}

// --- Gemini → OpenAI streaming ---

func (rw *ResponseWriter) streamGeminiToOpenAI(p []byte) (int, error) {
	if rw.state.geminiToOAI == nil {
		rw.state.geminiToOAI = gemini.NewGeminiToOpenAIState()
	}
	state := rw.state.geminiToOAI
	if state == nil {
		return len(p), nil
	}

	lines := state.Feed(p)

	for _, line := range lines {
		// Gemini streaming is raw newline-delimited JSON (no SSE
		// "data: " prefix). Strip optional prefix.
		line = strings.TrimRight(line, "\r\n")
		line, _ = strings.CutPrefix(line, "data: ")
		if line == "" || line == "[DONE]" {
			continue
		}
		var chunk map[string]any
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			rw.writeLine(line + "\n")
			continue
		}
		events := state.ProcessChunk(chunk)
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

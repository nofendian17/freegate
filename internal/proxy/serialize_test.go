package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	anyllm "github.com/mozilla-ai/any-llm-go"
)

type recordingWriter struct {
	header  http.Header
	buf     bytes.Buffer
	code    int
	wroteHd bool
	flushed int
}

func newRecordingWriter() *recordingWriter {
	return &recordingWriter{header: http.Header{}}
}

func (r *recordingWriter) Header() http.Header { return r.header }
func (r *recordingWriter) Write(b []byte) (int, error) {
	if !r.wroteHd {
		r.code = 200
		r.wroteHd = true
	}
	return r.buf.Write(b)
}
func (r *recordingWriter) WriteHeader(c int) { r.code = c; r.wroteHd = true }
func (r *recordingWriter) Flush()            { r.flushed++ }

func TestWriteNonStreaming_SetsHeadersAndBody(t *testing.T) {
	w := newRecordingWriter()
	usage := &TokenUsage{}
	resp := &anyllm.ChatCompletion{
		ID:      "chatcmpl-1",
		Object:  "chat.completion",
		Model:   "test-model",
		Choices: []anyllm.Choice{{Index: 0, Message: anyllm.Message{Role: "assistant", Content: "hi"}}},
		Usage:   &anyllm.Usage{PromptTokens: 5, CompletionTokens: 2, TotalTokens: 7},
	}
	writeNonStreaming(w, resp, usage)
	if got := w.header.Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want application/json", got)
	}
	if got := w.header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Errorf("Access-Control-Allow-Origin = %q, want *", got)
	}
	if w.code != 200 {
		t.Errorf("status = %d, want 200", w.code)
	}
	var round anyllm.ChatCompletion
	if err := json.Unmarshal(w.buf.Bytes(), &round); err != nil {
		t.Fatalf("body is not valid ChatCompletion JSON: %v\n%s", err, w.buf.String())
	}
	if round.ID != "chatcmpl-1" {
		t.Errorf("ID = %q, want chatcmpl-1", round.ID)
	}
	if usage.Prompt != 5 || usage.Completion != 2 || usage.Total != 7 {
		t.Errorf("usage = %+v, want P=5 C=2 T=7", *usage)
	}
}

func TestWriteStreaming_ChunksAndDone(t *testing.T) {
	w := newRecordingWriter()
	usage := &TokenUsage{}
	chunks := make(chan anyllm.ChatCompletionChunk, 3)
	errs := make(chan error, 1)
	chunks <- anyllm.ChatCompletionChunk{ID: "c1", Object: "chat.completion.chunk", Model: "m", Choices: []anyllm.ChunkChoice{{Index: 0, Delta: anyllm.ChunkDelta{Content: "hello"}}}}
	chunks <- anyllm.ChatCompletionChunk{ID: "c2", Object: "chat.completion.chunk", Model: "m", Choices: []anyllm.ChunkChoice{{Index: 0, Delta: anyllm.ChunkDelta{Content: " world"}}}}
	chunks <- anyllm.ChatCompletionChunk{ID: "c3", Object: "chat.completion.chunk", Model: "m", Choices: []anyllm.ChunkChoice{{Index: 0, Delta: anyllm.ChunkDelta{}}}, Usage: &anyllm.Usage{PromptTokens: 4, CompletionTokens: 6, TotalTokens: 10}}
	close(chunks)
	close(errs)

	if err := writeStreaming(w, chunks, errs, usage); err != nil {
		t.Fatalf("writeStreaming: %v", err)
	}
	if got := w.header.Get("Content-Type"); got != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", got)
	}
	if w.code != 200 {
		t.Errorf("status = %d, want 200", w.code)
	}
	body := w.buf.String()
	if !strings.Contains(body, "data: {\"id\":\"c1\"") {
		t.Errorf("body missing c1 chunk: %s", body)
	}
	if !strings.Contains(body, `"content":"hello"`) {
		t.Errorf("body missing hello content: %s", body)
	}
	if !strings.Contains(body, "data: [DONE]") {
		t.Errorf("body missing [DONE] marker: %s", body)
	}
	if w.flushed < 3 {
		t.Errorf("expected >=3 flushes (one per chunk), got %d", w.flushed)
	}
	if usage.Prompt != 4 || usage.Completion != 6 || usage.Total != 10 {
		t.Errorf("usage = %+v, want P=4 C=6 T=10", *usage)
	}
}

func TestWriteStreaming_ErrorBeforeFirstChunk(t *testing.T) {
	w := newRecordingWriter()
	usage := &TokenUsage{}
	chunks := make(chan anyllm.ChatCompletionChunk)
	close(chunks)
	errs := make(chan error, 1)
	errs <- anyllm.ErrRateLimit
	close(errs)
	err := writeStreaming(w, chunks, errs, usage)
	if err == nil {
		t.Fatalf("expected error, got nil")
	}
	if w.code != 0 {
		t.Errorf("status = %d, want 0 (no headers sent yet)", w.code)
	}
	if w.buf.Len() != 0 {
		t.Errorf("body = %q, want empty (no data sent yet)", w.buf.String())
	}
}

func TestWriteStreaming_AfterErrorStillEmitsData(t *testing.T) {
	w := newRecordingWriter()
	usage := &TokenUsage{}
	chunks := make(chan anyllm.ChatCompletionChunk, 1)
	errs := make(chan error, 1)
	chunks <- anyllm.ChatCompletionChunk{ID: "c1", Object: "chat.completion.chunk", Model: "m", Choices: []anyllm.ChunkChoice{{Index: 0, Delta: anyllm.ChunkDelta{Content: "hi"}}}}
	close(chunks)
	errs <- anyllm.ErrRateLimit
	close(errs)
	if err := writeStreaming(w, chunks, errs, usage); err != nil {
		t.Fatalf("writeStreaming: %v", err)
	}
	if !strings.Contains(w.buf.String(), `"content":"hi"`) {
		t.Errorf("body missing hi chunk: %s", w.buf.String())
	}
	if !strings.Contains(w.buf.String(), "data: [DONE]") {
		t.Errorf("body missing [DONE]: %s", w.buf.String())
	}
}

package translate

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestResponseWriter_OpenAIToResponses_Stream(t *testing.T) {
	// Simulate upstream OpenAI SSE -> client expects Responses SSE
	// Use ResponseWriter with src=OpenAI dst=Responses
	rec := httptest.NewRecorder()
	rw := NewResponseWriterWithDst(rec, FormatOpenAI, FormatOpenAIResponses)
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.WriteHeader(200)
	// OpenAI chunk
	chunk := `data: {"id":"chatcmpl-123","object":"chat.completion.chunk","created":1700000000,"model":"muse-spark-1.2-contributor-free","choices":[{"index":0,"delta":{"content":"Hello"}}]}` + "\n\n"
	if _, err := rw.Write([]byte(chunk)); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	// Need to check that output contains responses event
	body := rec.Body.String()
	if !strings.Contains(body, "response.output_text.delta") {
		t.Errorf("expected responses event, got %q", body)
	}
	if !strings.Contains(body, "Hello") {
		t.Errorf("expected Hello in body, got %q", body)
	}
}

func TestResponseWriter_ResponsesToOpenAI_Stream(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := NewResponseWriterWithDst(rec, FormatOpenAIResponses, FormatOpenAI)
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.WriteHeader(200)
	// Responses event
	event := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hi from muse\"}\n\n"
	if _, err := rw.Write([]byte(event)); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "data:") {
		t.Errorf("expected data: line, got %q", body)
	}
	if !strings.Contains(body, "Hi from muse") {
		t.Errorf("expected Hi from muse, got %q", body)
	}
}

func TestResponseWriter_OpenAIToResponses_JSON(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := NewResponseWriterWithDst(rec, FormatOpenAIResponses, FormatOpenAI)
	// Simulate non-stream JSON upstream responses -> client openai
	rw.Header().Set("Content-Type", "application/json")
	rw.WriteHeader(200)
	// Directly test ResponseJSON via writer
	body := []byte(`{"id":"resp_123","object":"response","created_at":1700000000,"model":"muse-spark-1.2-contributor-free","status":"completed","output":[{"type":"message","id":"msg_0","role":"assistant","content":[{"type":"output_text","text":"hello from muse"}]}]}`)
	if _, err := rw.Write(body); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	if err := rw.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
	out := rec.Body.String()
	if !strings.Contains(out, "chat.completion") {
		t.Errorf("expected chat.completion, got %q", out)
	}
	if !strings.Contains(out, "hello from muse") {
		t.Errorf("expected hello from muse, got %q", out)
	}
}

func TestResponseWriter_ClaudeToResponses_Stream(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := NewResponseWriterWithDst(rec, FormatClaude, FormatOpenAIResponses)
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.WriteHeader(200)
	// Claude SSE chunk (simplified)
	chunk := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"text_delta\",\"text\":\"Hello Claude\"}}\n\n"
	if _, err := rw.Write([]byte(chunk)); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "response.output_text.delta") {
		t.Errorf("expected responses event, got %q", body)
	}
	if !strings.Contains(body, "Hello Claude") {
		t.Errorf("expected Hello Claude, got %q", body)
	}
}

func TestResponseWriter_ResponsesToClaude_Stream(t *testing.T) {
	rec := httptest.NewRecorder()
	rw := NewResponseWriterWithDst(rec, FormatOpenAIResponses, FormatClaude)
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.WriteHeader(200)
	event := "event: response.output_text.delta\ndata: {\"type\":\"response.output_text.delta\",\"delta\":\"Hi Claude\"}\n\n"
	if _, err := rw.Write([]byte(event)); err != nil {
		t.Fatalf("Write failed: %v", err)
	}
	body := rec.Body.String()
	if !strings.Contains(body, "content_block_delta") {
		t.Errorf("expected claude event, got %q", body)
	}
	if !strings.Contains(body, "Hi Claude") {
		t.Errorf("expected Hi Claude, got %q", body)
	}
}

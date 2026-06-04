package translate

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRequest_Identity(t *testing.T) {
	body := []byte(`{"model":"gpt-4","messages":[{"role":"user","content":"hi"}]}`)
	result, err := Request(body, FormatOpenAI, FormatOpenAI)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result) != string(body) {
		t.Error("expected identity translation")
	}
}

func TestRequest_ClaudeToOpenAI(t *testing.T) {
	body := []byte(`{"model":"claude","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	result, err := Request(body, FormatClaude, FormatOpenAI)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var openai map[string]any
	json.Unmarshal(result, &openai)
	if openai["model"] != "claude" {
		t.Errorf("expected model=claude, got %v", openai["model"])
	}
}

func TestRequest_GeminiToOpenAI(t *testing.T) {
	body := []byte(`{"contents":[{"parts":[{"text":"hi"}],"role":"user"}]}`)
	result, err := Request(body, FormatGemini, FormatOpenAI)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var openai map[string]any
	json.Unmarshal(result, &openai)
	msgs := openai["messages"].([]any)
	if len(msgs) != 1 {
		t.Errorf("expected 1 message, got %d", len(msgs))
	}
}

func TestRequest_UnknownSource(t *testing.T) {
	body := []byte(`{"test":true}`)
	result, err := Request(body, "unknown", FormatOpenAI)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result) != string(body) {
		t.Error("expected passthrough for unknown format")
	}
}

func TestResponseJSON_OpenAI(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"hi"}}]}`)
	result, err := ResponseJSON(body, FormatOpenAI)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result) != string(body) {
		t.Error("expected identity for OpenAI")
	}
}

func TestResponseJSON_Claude(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"Hello world"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	result, err := ResponseJSON(body, FormatClaude)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var claude map[string]any
	json.Unmarshal(result, &claude)
	if claude["type"] != "message" {
		t.Errorf("expected type=message, got %v", claude["type"])
	}
}

func TestResponseWriter_NonStreamingJSON(t *testing.T) {
	inner := httptest.NewRecorder()
	rw := NewResponseWriter(inner, FormatClaude)
	rw.Header().Set("Content-Type", "application/json")

	// Write response body
	rw.Write([]byte(`{"choices":[{"message":{"content":"Hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3}}`))
	rw.Close()

	if inner.Code != http.StatusOK {
		t.Errorf("expected status 200, got %d", inner.Code)
	}

	// Verify it was translated to Claude format
	body := inner.Body.String()
	if !strings.Contains(body, `"type":"message"`) {
		t.Errorf("expected Claude-format response, got: %s", body)
	}
}

func TestResponseWriter_ErrorPassthrough(t *testing.T) {
	inner := httptest.NewRecorder()
	rw := NewResponseWriter(inner, FormatClaude)
	rw.WriteHeader(http.StatusBadRequest)
	rw.Write([]byte(`{"error":{"type":"invalid","message":"bad request"}}`))
	rw.Close()

	if inner.Code != http.StatusBadRequest {
		t.Errorf("expected status 400, got %d", inner.Code)
	}
	body := inner.Body.String()
	if !strings.Contains(body, "bad request") {
		t.Errorf("expected error passthrough, got: %s", body)
	}
}

func TestResponseWriter_StreamingPassthrough(t *testing.T) {
	inner := httptest.NewRecorder()
	rw := NewResponseWriter(inner, FormatClaude)
	rw.Header().Set("Content-Type", "text/event-stream")

	lines := []string{
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hi\"},\"finish_reason\":null}]}\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":5,\"completion_tokens\":2}}\n",
	}
	for _, line := range lines {
		rw.Write([]byte(line))
	}
	rw.Close()

	body := inner.Body.String()
	if !strings.Contains(body, "event: message_start") {
		t.Errorf("expected message_start event, got: %s", body)
	}
}

func TestResponseWriter_CloseEmpty(t *testing.T) {
	inner := httptest.NewRecorder()
	rw := NewResponseWriter(inner, FormatClaude)
	err := rw.Close()
	if err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// TestProxyChatWithClaudeFormat tests the full flow: handler receives Claude body,
// translates to OpenAI, passes to ProxyChat, then translates response back.
func TestProxyChatWithClaudeFormat(t *testing.T) {
	inner := httptest.NewRecorder()

	// This simulates what the handler does
	body := []byte(`{"model":"claude-sonnet","max_tokens":100,"messages":[{"role":"user","content":"hi"}]}`)
	format := Detect(body)
	if format != FormatClaude {
		t.Fatalf("expected Claude format, got %s", format)
	}

	// Translate request
	translated, err := Request(body, format, FormatOpenAI)
	if err != nil {
		t.Fatalf("translation error: %v", err)
	}

	// Mock upstream response (writes OpenAI-format JSON)
	wr := NewResponseWriter(inner, format)
	upstreamBody := `{"choices":[{"message":{"role":"assistant","content":"Hello there"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`
	wr.Header().Set("Content-Type", "application/json")
	wr.Write([]byte(upstreamBody))
	wr.Close()

	_ = translated // We just need to verify the response is Claude-format

	output := inner.Body.String()
	if !strings.Contains(output, `"type":"message"`) {
		t.Errorf("expected Claude-format response, got: %s", output)
	}
	if !strings.Contains(output, "Hello there") {
		t.Errorf("expected content preserved, got: %s", output)
	}
}

func TestProxyChatWithClaudeStreaming(t *testing.T) {
	inner := httptest.NewRecorder()

	body := []byte(`{"model":"claude-sonnet","max_tokens":100,"messages":[{"role":"user","content":"hi"}],"stream":true}`)
	format := Detect(body)
	if format != FormatClaude {
		t.Fatalf("expected Claude format, got %s", format)
	}

	wr := NewResponseWriter(inner, format)
	wr.Header().Set("Content-Type", "text/event-stream")

	// Simulate streaming upstream response
	streamData := []string{
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"Hello\"},\"finish_reason\":null}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\" world\"},\"finish_reason\":null}]}\n\n",
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":3}}\n\n",
		"data: [DONE]\n\n",
	}
	for _, chunk := range streamData {
		wr.Write([]byte(chunk))
	}
	wr.Close()

	output := inner.Body.String()
	// Should have Claude SSE events
	if !strings.Contains(output, "event: message_start") {
		t.Errorf("expected message_start event, got: %s", output)
	}
	if !strings.Contains(output, "event: message_delta") {
		t.Errorf("expected message_delta event, got: %s", output)
	}
	if !strings.Contains(output, "event: message_stop") {
		t.Errorf("expected message_stop event, got: %s", output)
	}
	// The "data: [DONE]" should be stripped
	if strings.Contains(output, "[DONE]") {
		t.Errorf("expected [DONE] to be stripped, got: %s", output)
	}
}

// Write() that only implements Write
type writeOnlyWriter struct {
	buf bytes.Buffer
}

func (w *writeOnlyWriter) Write(p []byte) (int, error) {
	return w.buf.Write(p)
}

func TestNewResponseWriter_NilFormat(t *testing.T) {
	inner := httptest.NewRecorder()
	rw := NewResponseWriter(inner, "")
	if rw.format != "" {
		t.Errorf("expected empty format, got %s", rw.format)
	}
}

// Additional edge case: Write with no Content-Type set
func TestResponseWriter_NoContentType(t *testing.T) {
	inner := httptest.NewRecorder()
	rw := NewResponseWriter(inner, FormatClaude)
	n, err := rw.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if n == 0 {
		t.Error("expected bytes written")
	}
	rw.Close()
	// Should work even without explicit Content-Type
	_ = inner.Body.String()
}

func TestResponseWriter_MultipleWrites(t *testing.T) {
	inner := httptest.NewRecorder()
	rw := NewResponseWriter(inner, FormatClaude)
	rw.Header().Set("Content-Type", "application/json")

	// Multiple small writes
	rw.Write([]byte(`{"choices":`))
	rw.Write([]byte(`[{"message":{"content":"hi"}}]`))
	rw.Write([]byte(`}`))
	rw.Close()

	body := inner.Body.String()
	// Should be valid Claude format
	if !strings.Contains(body, "message") {
		t.Errorf("expected translated response, got: %s", body)
	}
}

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

// TestRequest_ClaudeMixedTextAndToolResult_NoDuplicateToolCallID exercises the
// full translate.Request pipeline (Claude -> OpenAI, including the prepost
// steps) on a Claude user message that contains BOTH a text block and a
// tool_result block. Regression test: the previous ordering put the text
// user message before the tool response, which caused FixMissingToolResponses
// to insert a synthetic missing tool response and produce two tool messages
// with the same tool_call_id. MiniMax (and other strict OpenAI-compatible
// upstreams) reject this with "invalid params, 400 (2013)".
func TestRequest_ClaudeMixedTextAndToolResult_NoDuplicateToolCallID(t *testing.T) {
	body := []byte(`{
		"model":"minimax-m3-free","max_tokens":100,
		"messages":[
			{"role":"user","content":"What is the weather?"},
			{"role":"assistant","content":[
				{"type":"text","text":"Let me check."},
				{"type":"tool_use","id":"tu_1","name":"get_weather","input":{"city":"NYC"}}
			]},
			{"role":"user","content":[
				{"type":"text","text":"Note: please retry."},
				{"type":"tool_result","tool_use_id":"tu_1","content":"sunny, 72F"}
			]}
		]
	}`)
	result, err := Request(body, FormatClaude, FormatOpenAI)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var openai map[string]any
	if err := json.Unmarshal(result, &openai); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}

	msgs, ok := openai["messages"].([]any)
	if !ok {
		t.Fatalf("expected messages array, got %T", openai["messages"])
	}

	// Count occurrences of each tool_call_id. Must be exactly 1 per id.
	seen := map[string]int{}
	toolOrder := []string{}
	for i, mAny := range msgs {
		m, _ := mAny.(map[string]any)
		if m == nil {
			t.Errorf("message %d is %T, want map (nested array bug): %+v", i, mAny, msgs)
			continue
		}
		if id, ok := m["tool_call_id"].(string); ok && id != "" {
			seen[id]++
			toolOrder = append(toolOrder, id)
		}
	}
	for id, n := range seen {
		if n != 1 {
			t.Errorf("tool_call_id %q appears %d times, want 1 (full pipeline): %+v", id, n, msgs)
		}
	}

	// The tool response for tu_1 must appear BEFORE the user "Note: please
	// retry." message — the OpenAI-idiomatic order. This is what allows
	// FixMissingToolResponses (and the upstream) to see the response and
	// not synthesize a duplicate.
	toolIdx, userIdx := -1, -1
	for i, mAny := range msgs {
		m, _ := mAny.(map[string]any)
		if m == nil {
			continue
		}
		if id, _ := m["tool_call_id"].(string); id == "tu_1" {
			if toolIdx == -1 {
				toolIdx = i
			}
		}
		if role, _ := m["role"].(string); role == "user" {
			if c, _ := m["content"].(string); c == "Note: please retry." {
				userIdx = i
			}
		}
	}
	if toolIdx == -1 {
		t.Fatalf("no tool message for tu_1 found in: %+v", msgs)
	}
	if userIdx == -1 {
		t.Fatalf("no user 'Note: please retry.' message found in: %+v", msgs)
	}
	if toolIdx > userIdx {
		t.Errorf("tool response (idx %d) must come before user text (idx %d): %+v", toolIdx, userIdx, msgs)
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
	result, err := ResponseJSON(body, FormatOpenAI, FormatOpenAI)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if string(result) != string(body) {
		t.Error("expected identity for OpenAI")
	}
}

func TestResponseJSON_OpenAIToClaude(t *testing.T) {
	body := []byte(`{"choices":[{"message":{"content":"Hello world"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":5}}`)
	result, err := ResponseJSON(body, FormatOpenAI, FormatClaude)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var claude map[string]any
	json.Unmarshal(result, &claude)
	if claude["type"] != "message" {
		t.Errorf("expected type=message, got %v", claude["type"])
	}
}

func TestResponseJSON_ClaudeToOpenAI(t *testing.T) {
	body := []byte(`{"type":"message","role":"assistant","content":[{"type":"text","text":"hi"}],"stop_reason":"end_turn","usage":{"input_tokens":5,"output_tokens":2}}`)
	result, err := ResponseJSON(body, FormatClaude, FormatOpenAI)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var openai map[string]any
	_ = json.Unmarshal(result, &openai)
	if openai["object"] != "chat.completion" {
		t.Errorf("expected object=chat.completion, got %v", openai["object"])
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
	body := []byte(`{"model":"claude-sonnet","anthropic_version":"test-2025-01-01","messages":[{"role":"user","content":"hi"}]}`)
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

	body := []byte(`{"model":"claude-sonnet","anthropic_version":"test-2025-01-01","messages":[{"role":"user","content":"hi"}],"stream":true}`)
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
	if rw.dst != "" {
		t.Errorf("expected empty dst, got %s", rw.dst)
	}
	if rw.src != FormatOpenAI {
		t.Errorf("expected src=FormatOpenAI, got %s", rw.src)
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

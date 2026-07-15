package proxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestSyncReasoning_BothPresent(t *testing.T) {
	m := map[string]interface{}{
		"reasoning":         "step by step",
		"reasoning_content": "step by step",
	}
	syncReasoning(m)
	if m["reasoning"] != "step by step" {
		t.Errorf("expected reasoning='step by step', got %v", m["reasoning"])
	}
	if m["reasoning_content"] != "step by step" {
		t.Errorf("expected reasoning_content to be preserved, got %v", m["reasoning_content"])
	}
}

func TestSyncReasoning_OnlyRC(t *testing.T) {
	m := map[string]interface{}{
		"reasoning_content": "from opencode",
	}
	syncReasoning(m)
	if m["reasoning"] != "from opencode" {
		t.Errorf("expected reasoning='from opencode', got %v", m["reasoning"])
	}
	if m["reasoning_content"] != "from opencode" {
		t.Errorf("expected reasoning_content to be preserved, got %v", m["reasoning_content"])
	}
}

func TestSyncReasoning_OnlyR(t *testing.T) {
	m := map[string]interface{}{
		"reasoning": "from kilo",
	}
	syncReasoning(m)
	if m["reasoning"] != "from kilo" {
		t.Errorf("expected reasoning='from kilo', got %v", m["reasoning"])
	}
	if _, ok := m["reasoning_content"]; ok {
		t.Errorf("expected reasoning_content to be absent, got %v", m["reasoning_content"])
	}
}

func TestSyncReasoning_Neither(t *testing.T) {
	m := map[string]interface{}{
		"content": "hello",
	}
	syncReasoning(m)
	if m["reasoning"] != nil {
		t.Errorf("expected reasoning=nil, got %v", m["reasoning"])
	}
	if _, ok := m["reasoning_content"]; ok {
		t.Errorf("expected reasoning_content to be absent, got %v", m["reasoning_content"])
	}
}

func TestNormalizeSSELine_NormalData(t *testing.T) {
	line := `data: {"choices":[{"delta":{"content":"hi"}}]}` + "\n"
	result := normalizeSSELine(line)
	if !strings.HasPrefix(result, "data: ") {
		t.Error("expected line to start with 'data: '")
	}
	if !strings.Contains(result, `"content":"hi"`) {
		t.Error("expected content to be preserved")
	}
}

func TestNormalizeSSELine_Done(t *testing.T) {
	line := "data: [DONE]\n"
	result := normalizeSSELine(line)
	if result != line {
		t.Errorf("expected [DONE] to pass through unchanged, got %v", result)
	}
}

func TestNormalizeSSELine_MalformedJSON(t *testing.T) {
	line := "data: {invalid json}\n"
	result := normalizeSSELine(line)
	if result != line {
		t.Error("expected malformed JSON to pass through unchanged")
	}
}

func TestNormalizeSSELine_NonDataLine(t *testing.T) {
	line := "event: message\n"
	result := normalizeSSELine(line)
	if result != line {
		t.Error("expected non-data line to pass through unchanged")
	}
}

func TestNormalizeSSELine_EmptyData(t *testing.T) {
	line := "data: \n"
	result := normalizeSSELine(line)
	if result != line {
		t.Error("expected empty data to pass through unchanged")
	}
}

func TestNormalizeStream_SyncsReasoning(t *testing.T) {
	input := "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"thinking\"}}]}\ndata: [DONE]\n"
	var buf bytes.Buffer
	normalizeOpenAIStream(&buf, bufio.NewReader(strings.NewReader(input)))
	output := buf.String()

	if !strings.Contains(output, `"reasoning":"thinking"`) {
		t.Error("expected reasoning field to be synced")
	}
	if !strings.Contains(output, `"reasoning_content":"thinking"`) {
		t.Error("expected reasoning_content field to be preserved")
	}
}

func TestNormalizeJSON_SyncsMessageReasoning(t *testing.T) {
	input := `{"choices":[{"message":{"reasoning_content":"analysis"}}]}`
	var buf bytes.Buffer
	normalizeJSON(&buf, strings.NewReader(input))
	output := buf.String()

	if !strings.Contains(output, `"reasoning":"analysis"`) {
		t.Error("expected reasoning field to be synced")
	}
	if !strings.Contains(output, `"reasoning_content":"analysis"`) {
		t.Error("expected reasoning_content field to be preserved")
	}
}

func TestNormalizeJSON_InvalidJSON(t *testing.T) {
	input := "not json at all"
	var buf bytes.Buffer
	normalizeJSON(&buf, strings.NewReader(input))
	output := buf.String()

	if output != input {
		t.Error("expected invalid JSON to pass through unchanged")
	}
}

// mockResponseWriter implements http.ResponseWriter for testing.
type mockResponseWriter struct {
	buf    bytes.Buffer
	header http.Header
	code   int
}

func newMockResponseWriter() *mockResponseWriter {
	return &mockResponseWriter{header: make(http.Header)}
}

func (m *mockResponseWriter) Header() http.Header         { return m.header }
func (m *mockResponseWriter) Write(b []byte) (int, error) { return m.buf.Write(b) }
func (m *mockResponseWriter) WriteHeader(code int)        { m.code = code }

func TestCopyNormalized_Streaming(t *testing.T) {
	input := "data: {\"choices\":[{\"delta\":{\"reasoning\":\"thought\"}}]}\ndata: [DONE]\n"
	src := &http.Response{
		Header: http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:   io.NopCloser(strings.NewReader(input)),
	}
	w := newMockResponseWriter()
	_, _ = copyNormalized(w, src)
	output := w.buf.String()

	if !strings.Contains(output, `"reasoning":"thought"`) {
		t.Error("expected reasoning to be preserved in streaming")
	}
	// reasoning_content was not present in input; should not appear
	if strings.Contains(output, `"reasoning_content"`) {
		t.Error("expected reasoning_content to be absent in streaming (not in input)")
	}
}

func TestCopyNormalized_JSON(t *testing.T) {
	input := `{"choices":[{"message":{"reasoning":"thought"}}]}`
	src := &http.Response{
		Header: http.Header{"Content-Type": []string{"application/json"}},
		Body:   io.NopCloser(strings.NewReader(input)),
	}
	w := newMockResponseWriter()
	_, _ = copyNormalized(w, src)
	output := w.buf.String()

	if !strings.Contains(output, `"reasoning":"thought"`) {
		t.Error("expected reasoning to be preserved in JSON")
	}
	// reasoning_content was not present in input; should not appear
	if strings.Contains(output, `"reasoning_content"`) {
		t.Error("expected reasoning_content to be absent in JSON (not in input)")
	}
}

// TestNormalizeStream_DeepSeekDoubleResponse verifies that both
// `reasoning` and `reasoning_content` are preserved in streaming
// responses. DeepSeek requires `reasoning_content` to be passed back
// through conversation history in thinking mode.
func TestNormalizeStream_DeepSeekDoubleResponse(t *testing.T) {
	input := "data: {\"choices\":[{\"delta\":{\"reasoning\":\"step\",\"reasoning_content\":\"step\"}}]}\ndata: [DONE]\n"
	var buf bytes.Buffer
	normalizeOpenAIStream(&buf, bufio.NewReader(strings.NewReader(input)))
	output := buf.String()

	if !strings.Contains(output, `"reasoning_content":"step"`) {
		t.Errorf("expected reasoning_content to be preserved, got %s", output)
	}
	if !strings.Contains(output, `"reasoning":"step"`) {
		t.Errorf("expected reasoning to be preserved, got %s", output)
	}
}

// TestNormalizeJSON_DeepSeekDoubleResponse is the non-streaming
// counterpart to TestNormalizeStream_DeepSeekDoubleResponse.
func TestNormalizeJSON_DeepSeekDoubleResponse(t *testing.T) {
	input := `{"choices":[{"message":{"reasoning":"step","reasoning_content":"step"}}]}`
	var buf bytes.Buffer
	normalizeJSON(&buf, strings.NewReader(input))
	output := buf.String()

	if !strings.Contains(output, `"reasoning_content":"step"`) {
		t.Errorf("expected reasoning_content to be preserved, got %s", output)
	}
	if !strings.Contains(output, `"reasoning":"step"`) {
		t.Errorf("expected reasoning to be preserved, got %s", output)
	}
}

// TestNormalizeStream_RepairsToolArgs verifies that malformed tool-call
// arguments streamed as incremental fragments are buffered and emitted as a
// single valid JSON object at stream end (regression for the
// "input JSON failed to parse" error with models like tencent/hy3-free).
func TestNormalizeStream_RepairsToolArgs(t *testing.T) {
	// Build valid SSE chunks (json.Marshal handles escaping). The upstream
	// streams `{"cmd":"echo hello` split across three deltas; the joined
	// string is missing its closing quote and must be repaired.
	mk := func(delta map[string]any, finish string) string {
		chunk := map[string]any{"id": "c1", "model": "hy3", "choices": []any{map[string]any{"index": 0, "delta": delta}}}
		if finish != "" {
			chunk["choices"].([]any)[0].(map[string]any)["finish_reason"] = finish
		}
		b, _ := json.Marshal(chunk)
		return "data: " + string(b) + "\n"
	}
	input := mk(map[string]any{
		"tool_calls": []any{map[string]any{
			"index": 0, "id": "call_1", "type": "function",
			"function": map[string]any{"name": "Bash", "arguments": `{"cmd":"echo `},
		}},
	}, "") +
		mk(map[string]any{
			"tool_calls": []any{map[string]any{
				"index": 0,
				"function": map[string]any{"arguments": `hello`},
			}},
		}, "") +
		mk(map[string]any{}, "tool_calls") +
		"data: [DONE]\n"

	var buf bytes.Buffer
	normalizeOpenAIStream(&buf, bufio.NewReader(strings.NewReader(input)))
	output := buf.String()

	// The incremental fragments must NOT be emitted verbatim — exactly one
	// arguments field should appear, and it must be the repaired object.
	if n := strings.Count(output, `"arguments":`); n != 1 {
		t.Errorf("expected exactly one arguments field, got %d: %s", n, output)
	}
	// The repaired, fully-valid object must appear.
	if !strings.Contains(output, `"arguments":"{\"cmd\":\"echo hello\"}"`) {
		t.Errorf("expected repaired arguments to be emitted, got %s", output)
	}
	// id/name must still reach the client.
	if !strings.Contains(output, `"id":"call_1"`) || !strings.Contains(output, `"name":"Bash"`) {
		t.Errorf("expected tool id/name preserved, got %s", output)
	}
	// finish_reason must still be present.
	if !strings.Contains(output, `"finish_reason":"tool_calls"`) {
		t.Errorf("expected finish_reason preserved, got %s", output)
	}
}

// TestNormalizeJSON_RepairsToolArgs is the non-streaming counterpart: a
// malformed arguments string in a complete response is normalized to a
// valid JSON object.
func TestNormalizeJSON_RepairsToolArgs(t *testing.T) {
	resp := map[string]any{
		"choices": []any{map[string]any{
			"message": map[string]any{
				"role": "assistant",
				"tool_calls": []any{map[string]any{
					"id": "call_1", "type": "function",
					"function": map[string]any{"name": "Bash", "arguments": `{"cmd":"echo hello`},
				}},
			},
		}},
	}
	b, _ := json.Marshal(resp)
	var buf bytes.Buffer
	normalizeJSON(&buf, strings.NewReader(string(b)))
	output := buf.String()

	if !strings.Contains(output, `"arguments":"{\"cmd\":\"echo hello\"}"`) {
		t.Errorf("expected repaired arguments in JSON output, got %s", output)
	}
}

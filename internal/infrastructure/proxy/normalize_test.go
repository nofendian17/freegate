package proxy

import (
	"bufio"
	"bytes"
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

func TestNormalizeStream_DropsCommentsAndEventLines(t *testing.T) {
	// OpenRouter-style comment and event: lines must not reach the client;
	// only data: lines (including [DONE]) are forwarded.
	input := ": OPENROUTER PROCESSING\nevent: message\ndata: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\ndata: [DONE]\n"
	var buf bytes.Buffer
	normalizeOpenAIStream(&buf, bufio.NewReader(strings.NewReader(input)))
	output := buf.String()

	if strings.Contains(output, "OPENROUTER PROCESSING") {
		t.Error("expected upstream comment line to be dropped")
	}
	if strings.Contains(output, "event: message") {
		t.Error("expected upstream event: line to be dropped")
	}
	if !strings.Contains(output, `"content":"hi"`) {
		t.Error("expected data payload to be preserved")
	}
	if !strings.Contains(output, "data: [DONE]") {
		t.Error("expected [DONE] marker to be preserved")
	}
}

// TestNormalizeStream_NDJSONNoNewlines covers upstreams (e.g. OpenRouter→
// Novita for tencent/hy3) that concatenate data: events with no newline
// separator. The reader must still split them into individual events.
func TestNormalizeStream_NDJSONNoNewlines(t *testing.T) {
	input := "data: {\"choices\":[{\"delta\":{\"content\":\"Hello\"}}]}data: {\"choices\":[{\"delta\":{\"content\":\" world\"}}]}data: [DONE]"
	var buf bytes.Buffer
	normalizeOpenAIStream(&buf, bufio.NewReader(strings.NewReader(input)))
	output := buf.String()

	count := strings.Count(output, "data: {\"choices\"")
	if count != 2 {
		t.Errorf("expected 2 data events, got %d: %s", count, output)
	}
	if !strings.Contains(output, `"content":"Hello"`) {
		t.Error("expected first event payload preserved")
	}
	if !strings.Contains(output, `"content":" world"`) {
		t.Error("expected second event payload preserved")
	}
	if !strings.Contains(output, "data: [DONE]") {
		t.Error("expected [DONE] marker preserved")
	}
}

// TestNormalizeStream_ChunkedNDJSON ensures events arriving split across
// reads (mid-JSON) are reassembled correctly.
func TestNormalizeStream_ChunkedNDJSON(t *testing.T) {
	// A reader that yields one byte at a time.
	r := &byteReader{s: "data: {\"choices\":[{\"delta\":{\"content\":\"a\"}}]}data: {\"choices\":[{\"delta\":{\"content\":\"b\"}}]}data: [DONE]"}
	var buf bytes.Buffer
	normalizeOpenAIStream(&buf, bufio.NewReader(r))
	output := buf.String()

	if got := strings.Count(output, "data: {\"choices\""); got != 2 {
		t.Errorf("expected 2 data events, got %d: %s", got, output)
	}
	if !strings.Contains(output, "data: [DONE]") {
		t.Error("expected [DONE] marker preserved")
	}
}

type byteReader struct {
	s string
	i int
}

func (b *byteReader) Read(p []byte) (int, error) {
	if b.i >= len(b.s) {
		return 0, io.EOF
	}
	p[0] = b.s[b.i]
	b.i++
	return 1, nil
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

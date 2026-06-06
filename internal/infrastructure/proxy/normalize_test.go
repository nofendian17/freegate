package proxy

import (
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
	if _, ok := m["reasoning_content"]; ok {
		t.Errorf("expected reasoning_content to be dropped, got %v", m["reasoning_content"])
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
	if _, ok := m["reasoning_content"]; ok {
		t.Errorf("expected reasoning_content to be dropped, got %v", m["reasoning_content"])
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
	normalizeStream(&buf, strings.NewReader(input))
	output := buf.String()

	if !strings.Contains(output, `"reasoning":"thinking"`) {
		t.Error("expected reasoning field to be synced")
	}
	if strings.Contains(output, `"reasoning_content"`) {
		t.Error("expected reasoning_content field to be dropped")
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
	if strings.Contains(output, `"reasoning_content"`) {
		t.Error("expected reasoning_content field to be dropped")
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
	if strings.Contains(output, `"reasoning_content"`) {
		t.Error("expected reasoning_content to be absent in streaming")
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
	if strings.Contains(output, `"reasoning_content"`) {
		t.Error("expected reasoning_content to be absent in JSON")
	}
}

// TestNormalizeStream_DeepSeekDoubleResponse guards against the
// regression where DeepSeek responses (which arrive with both
// `reasoning` and `reasoning_content` populated) caused a double
// response: clients reading either field would render the reasoning
// text twice.
func TestNormalizeStream_DeepSeekDoubleResponse(t *testing.T) {
	input := "data: {\"choices\":[{\"delta\":{\"reasoning\":\"step\",\"reasoning_content\":\"step\"}}]}\ndata: [DONE]\n"
	var buf bytes.Buffer
	normalizeStream(&buf, strings.NewReader(input))
	output := buf.String()

	if strings.Contains(output, `"reasoning_content"`) {
		t.Errorf("expected reasoning_content to be dropped, got %s", output)
	}
	if c := strings.Count(output, `"reasoning":"step"`); c != 1 {
		t.Errorf("expected exactly one reasoning field, got %d in %s", c, output)
	}
}

// TestNormalizeJSON_DeepSeekDoubleResponse is the non-streaming
// counterpart to TestNormalizeStream_DeepSeekDoubleResponse.
func TestNormalizeJSON_DeepSeekDoubleResponse(t *testing.T) {
	input := `{"choices":[{"message":{"reasoning":"step","reasoning_content":"step"}}]}`
	var buf bytes.Buffer
	normalizeJSON(&buf, strings.NewReader(input))
	output := buf.String()

	if strings.Contains(output, `"reasoning_content"`) {
		t.Errorf("expected reasoning_content to be dropped, got %s", output)
	}
	if c := strings.Count(output, `"reasoning":"step"`); c != 1 {
		t.Errorf("expected exactly one reasoning field, got %d in %s", c, output)
	}
}

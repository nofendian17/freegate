package proxy

import (
	"bufio"
	"bytes"
	"context"
	"log/slog"
	"strings"
	"testing"
)

func captureLogs(t *testing.T) *bytes.Buffer {
	t.Helper()
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })
	return &buf
}

const museEmptyJSON = `{"id":"x","object":"chat.completion","choices":[{"index":0,"message":{"role":"assistant"},"finish_reason":null}],"usage":{"prompt_tokens":14,"completion_tokens":100,"total_tokens":114}}`

// TestNormalizeJSON_EmptyCompletionLogsWarning guards against the silent
// masking observed during the muse-spark outage: llm7 sometimes answers an
// unavailable model with HTTP 200 and a bare {role:"assistant"} message —
// no content, no tool_calls, no finish_reason. freegate must log a
// structured warning carrying correlation fields instead of pretending
// nothing happened.
func TestNormalizeJSON_EmptyCompletionLogsWarning(t *testing.T) {
	logs := captureLogs(t)

	var out bytes.Buffer
	normalizeJSONWithMeta(&out, strings.NewReader(museEmptyJSON), "muse-spark-1.2-contributor-free", "req-empty-1")

	logged := logs.String()
	if !strings.Contains(logged, "upstream empty completion") {
		t.Errorf("expected empty-completion warning, logs:\n%s", logged)
	}
	if !strings.Contains(logged, "req-empty-1") || !strings.Contains(logged, "muse-spark-1.2-contributor-free") {
		t.Errorf("warning must carry request_id and model for correlation, logs:\n%s", logged)
	}
}

// TestNormalizeJSON_ContentCompletionNoWarning ensures the detector does not
// cry wolf on healthy responses.
func TestNormalizeJSON_ContentCompletionNoWarning(t *testing.T) {
	logs := captureLogs(t)

	in := `{"id":"x","choices":[{"index":0,"message":{"role":"assistant","content":"CONTROL-OK"},"finish_reason":"stop"}]}`
	var out bytes.Buffer
	normalizeJSONWithMeta(&out, strings.NewReader(in), "gpt-oss", "req-ok")

	if strings.Contains(logs.String(), "upstream empty completion") {
		t.Errorf("healthy response must not warn, logs:\n%s", logs.String())
	}
}

// TestNormalizeStream_EmptyCompletionLogsWarning covers the streaming shape:
// a chunk train of empty choices[] lines that ends at EOF with neither
// content nor tool calls.
func TestNormalizeStream_EmptyCompletionLogsWarning(t *testing.T) {
	logs := captureLogs(t)

	in := "" +
		"data: {\"id\":\"r1\",\"model\":\"muse\",\"choices\":[]}\n\n" +
		"data: {\"id\":\"\",\"model\":\"\",\"choices\":[]}\n\n" +
		"data: {\"id\":\"\",\"model\":\"\",\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":50,\"total_tokens\":51}}\n\n"

	var out bytes.Buffer
	normalizeOpenAIStreamWithMeta(context.Background(), &out, bufio.NewReader(strings.NewReader(in)), "muse-spark", "req-empty-stream")

	logged := logs.String()
	if !strings.Contains(logged, "upstream empty completion") {
		t.Errorf("expected empty-completion warning for stream, logs:\n%s", logged)
	}
	if !strings.Contains(logged, "req-empty-stream") {
		t.Errorf("warning must carry request_id, logs:\n%s", logged)
	}
}

// TestNormalizeStream_ContentStreamNoWarning keeps the positive case quiet.
func TestNormalizeStream_ContentStreamNoWarning(t *testing.T) {
	logs := captureLogs(t)

	in := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
		"data: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n" +
		"data: [DONE]\n\n"

	var out bytes.Buffer
	normalizeOpenAIStreamWithMeta(context.Background(), &out, bufio.NewReader(strings.NewReader(in)), "gpt-oss", "req-ok")

	if strings.Contains(logs.String(), "upstream empty completion") {
		t.Errorf("healthy stream must not warn, logs:\n%s", logs.String())
	}
}

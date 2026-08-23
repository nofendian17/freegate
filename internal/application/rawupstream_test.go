package application

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"bytes"

	"freegate/internal/domain"
)

// TestProxyChat_RawUpstreamLoggedToStdoutWhenEnabled verifies the debug
// workflow: with the raw-upstream log switch on, every upstream SSE line is
// printed through slog so failures can be diagnosed from the server log —
// no files involved.
func TestProxyChat_RawUpstreamLoggedToStdoutWhenEnabled(t *testing.T) {
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	raw := "data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hi\"},\"finish_reason\":null}]}\n\n" +
		"data: [DONE]\n\n"
	resp := &domain.UpstreamResponse{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(raw)),
	}
	upstream := &mockUpstream{name: "llm7", response: resp}

	cs := NewChatService(&mockRouter{upstream: upstream}, nil).
		WithRawUpstreamLog(true)

	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	r.Header.Set("X-Request-ID", "trace-log-1")
	w := httptest.NewRecorder()

	if err := cs.ProxyChat(context.Background(), w, r, "muse-spark-1.2-contributor-free", []byte(`{}`)); err != nil {
		t.Fatalf("ProxyChat failed: %v", err)
	}

	logged := logBuf.String()
	// slog's TextHandler escapes quotes inside values, so assert on
	// substrings that survive that encoding.
	for _, want := range []string{
		"upstream raw response",
		"finish_reason",
		"[DONE]",
		"trace-log-1",
	} {
		if !strings.Contains(logged, want) {
			t.Errorf("log output missing %q;\nlogs:\n%s", want, logged)
		}
	}
}

// TestProxyChat_NoRawUpstreamLogWhenDisabled keeps prod quiet by default.
func TestProxyChat_NoRawUpstreamLogWhenDisabled(t *testing.T) {
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	resp := &domain.UpstreamResponse{
		StatusCode: 200,
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader("data: x\n")),
	}
	upstream := &mockUpstream{name: "test", response: resp}
	cs := NewChatService(&mockRouter{upstream: upstream}, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	if err := cs.ProxyChat(context.Background(), w, r, "m", []byte(`{}`)); err != nil {
		t.Fatalf("ProxyChat failed: %v", err)
	}

	if strings.Contains(logBuf.String(), "upstream raw response") {
		t.Errorf("raw upstream must not be logged when disabled;\nlogs:\n%s", logBuf.String())
	}
}

// TestProxyChat_RawUpstreamLogFlushesPartialTail guards byte fidelity: an
// upstream that ends mid-line (no trailing newline — the muse-spark
// truncation shape) must still have its final partial line logged on close.
func TestProxyChat_RawUpstreamLogFlushesPartialTail(t *testing.T) {
	var logBuf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logBuf, nil)))
	t.Cleanup(func() { slog.SetDefault(prev) })

	resp := &domain.UpstreamResponse{
		StatusCode: 200,
		Header:     http.Header{"Content-Type": []string{"text/event-stream"}},
		// No trailing newline after last data line.
		Body: io.NopCloser(strings.NewReader("data: {\"a\":1}\ndata: {\"cost\":0}")),
	}
	upstream := &mockUpstream{name: "llm7", response: resp}
	cs := NewChatService(&mockRouter{upstream: upstream}, nil).WithRawUpstreamLog(true)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	if err := cs.ProxyChat(context.Background(), w, r, "muse", []byte(`{}`)); err != nil {
		t.Fatalf("ProxyChat failed: %v", err)
	}

	if !strings.Contains(logBuf.String(), "cost") {
		t.Errorf("partial tail line was not flushed to log;\nlogs:\n%s", logBuf.String())
	}
}

// TestProxyChat_CorrelationHeadersNeverReachClient pins the invariant that
// the internal X-Fg-* correlation headers exist only between ChatService and
// the normalization layer: they must not appear in client-visible response
// headers, on the success path (stripped by NormalizeDomainResponseWithContext)
// nor on the error pass-through path (429/5xx).
func TestProxyChat_CorrelationHeadersNeverReachClient(t *testing.T) {
	t.Run("error passthrough", func(t *testing.T) {
		resp := &domain.UpstreamResponse{
			StatusCode: 429,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body:       io.NopCloser(strings.NewReader(`{"error":{"message":"slow down"}}`)),
		}
		upstream := &mockUpstream{name: "test", response: resp}
		cs := NewChatService(&mockRouter{upstream: upstream}, nil)

		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		if err := cs.ProxyChat(context.Background(), w, r, "muse", []byte(`{}`)); err != nil {
			t.Fatalf("ProxyChat failed: %v", err)
		}
		for _, h := range []string{"X-Fg-Model", "X-Fg-Request-Id"} {
			if got := w.Header().Get(h); got != "" {
				t.Errorf("%s leaked to client on error path: %q", h, got)
			}
		}
	})

	t.Run("success", func(t *testing.T) {
		resp := &domain.UpstreamResponse{
			StatusCode: 200,
			Header:     http.Header{},
			Body:       io.NopCloser(strings.NewReader("{}")),
		}
		upstream := &mockUpstream{name: "test", response: resp}
		cs := NewChatService(&mockRouter{upstream: upstream}, nil)

		w := httptest.NewRecorder()
		r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
		if err := cs.ProxyChat(context.Background(), w, r, "muse", []byte(`{}`)); err != nil {
			t.Fatalf("ProxyChat failed: %v", err)
		}
		for _, h := range []string{"X-Fg-Model", "X-Fg-Request-Id"} {
			if got := w.Header().Get(h); got != "" {
				t.Errorf("%s leaked to client on success path: %q", h, got)
			}
		}
	})
}

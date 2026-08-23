package application

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"freegate/internal/domain"
	"freegate/internal/infrastructure/metrics"
)

type mockRouter struct {
	upstream domain.Upstream
}

func (m *mockRouter) Select(modelID string) domain.Upstream {
	return m.upstream
}

type mockUpstream struct {
	name     string
	response *domain.UpstreamResponse
	err      error
	calls    int
}

func (m *mockUpstream) Name() string { return m.name }
func (m *mockUpstream) Match(modelID string) bool {
	return true
}
func (m *mockUpstream) ListModels(ctx context.Context) ([]domain.Model, error) {
	return nil, nil
}
func (m *mockUpstream) ChatCompletion(ctx context.Context, body []byte) (*domain.UpstreamResponse, error) {
	m.calls++
	if m.err != nil {
		return nil, m.err
	}
	return m.response, nil
}
func (m *mockUpstream) Models() []domain.Model                     { return nil }
func (m *mockUpstream) Start(ctx context.Context, _ time.Duration) {}

func TestChatServiceProxyChatSuccess(t *testing.T) {
	resp := &domain.UpstreamResponse{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader("{}")),
		Header:     http.Header{},
	}
	upstream := &mockUpstream{name: "test", response: resp}
	router := &mockRouter{upstream: upstream}

	cs := NewChatService(router, nil)
	w := &recordingResponseWriter{header: http.Header{}}
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)

	err := cs.ProxyChat(context.Background(), w, r, "test-model", []byte("{}"))
	if err != nil {
		t.Fatalf("ProxyChat failed: %v", err)
	}
	if upstream.calls != 1 {
		t.Errorf("expected 1 upstream call, got %d", upstream.calls)
	}
}

// TestChatServiceProxyChatPassesThrough429 verifies that a 429 from the
// upstream is forwarded to the client unchanged (no automatic retry or IP
// rotation — the user picks the VPN server manually).
func TestChatServiceProxyChatPassesThrough429(t *testing.T) {
	body := `{"error":{"message":"Rate limit exceeded. Please try again later."}}`
	resp := &domain.UpstreamResponse{
		StatusCode: 429,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
	upstream := &mockUpstream{name: "test", response: resp}
	router := &mockRouter{upstream: upstream}

	cs := NewChatService(router, nil)
	w := &recordingResponseWriter{header: http.Header{}}
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)

	err := cs.ProxyChat(context.Background(), w, r, "test-model", []byte("{}"))
	if err != nil {
		t.Fatalf("ProxyChat failed: %v", err)
	}
	if upstream.calls != 1 {
		t.Errorf("expected exactly 1 upstream call (no retry), got %d", upstream.calls)
	}
	if w.status != 429 {
		t.Errorf("expected status 429 written to client, got %d", w.status)
	}
	if !strings.Contains(string(w.body), "Rate limit exceeded") {
		t.Errorf("expected provider 429 body to pass through, got %q", string(w.body))
	}
}

// TestChatServiceProxyChatUpstreamHTTPErrorCountsInStats verifies that an
// upstream HTTP error status (e.g. a provider 400 or 429 passed through)
// is counted in the upstream_errors stat, not only transport failures.
func TestChatServiceProxyChatUpstreamHTTPErrorCountsInStats(t *testing.T) {
	body := `{"error":{"message":"Error from provider (Console): [1210]"}}`
	resp := &domain.UpstreamResponse{
		StatusCode: 400,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     http.Header{"Content-Type": []string{"application/json"}},
	}
	upstream := &mockUpstream{name: "test", response: resp}
	router := &mockRouter{upstream: upstream}
	metrics := metrics.New()

	cs := NewChatService(router, metrics)
	w := &recordingResponseWriter{header: http.Header{}}
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)

	if err := cs.ProxyChat(context.Background(), w, r, "test-model", []byte("{}")); err != nil {
		t.Fatalf("ProxyChat failed: %v", err)
	}
	snap := metrics.Snapshot()
	if got, _ := snap["upstream_errors"].(int64); got != 1 {
		t.Errorf("expected upstream_errors=1 after upstream HTTP 400, got %v", snap["upstream_errors"])
	}
}

func TestChatServiceProxyChatUpstreamError(t *testing.T) {
	upstream := &mockUpstream{name: "test", err: context.DeadlineExceeded}
	router := &mockRouter{upstream: upstream}

	cs := NewChatService(router, nil)
	w := &recordingResponseWriter{header: http.Header{}}
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)

	err := cs.ProxyChat(context.Background(), w, r, "test-model", []byte("{}"))
	if err == nil {
		t.Fatal("expected error from transport failure")
	}
	if upstream.calls != 1 {
		t.Errorf("expected 1 upstream call, got %d", upstream.calls)
	}
}

type recordingResponseWriter struct {
	header http.Header
	body   []byte
	status int
}

func (r *recordingResponseWriter) Header() http.Header { return r.header }
func (r *recordingResponseWriter) Write(b []byte) (int, error) {
	r.body = append(r.body, b...)
	return len(b), nil
}
func (r *recordingResponseWriter) WriteHeader(s int) { r.status = s }

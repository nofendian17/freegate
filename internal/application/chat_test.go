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
)

type mockRouter struct {
	upstream domain.Upstream
	err      error
}

func (m *mockRouter) Select(modelID string) (domain.Upstream, error) {
	return m.upstream, m.err
}

type mockUpstream struct {
	name      string
	responses []*http.Response
	errors    []error
	calls     int
}

func (m *mockUpstream) Name() string { return m.name }
func (m *mockUpstream) Match(modelID string) bool {
	return true
}
func (m *mockUpstream) ListModels(ctx context.Context) ([]domain.Model, error) {
	return nil, nil
}
func (m *mockUpstream) ChatCompletion(ctx context.Context, req domain.ChatRequest) (*http.Response, error) {
	i := m.calls
	m.calls++
	if i < len(m.errors) && m.errors[i] != nil {
		return nil, m.errors[i]
	}
	return m.responses[i], nil
}
func (m *mockUpstream) Models() []domain.Model    { return nil }
func (m *mockUpstream) Start(ctx context.Context) {}

type mockIPRotator struct {
	forceNewIPCalls int
}

func (m *mockIPRotator) NewIP() error { return nil }
func (m *mockIPRotator) ForceNewIP() error {
	m.forceNewIPCalls++
	return nil
}
func (m *mockIPRotator) CurrentIP() string { return "127.0.0.1" }

func TestChatServiceProxyChatSuccess(t *testing.T) {
	resp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader("{}")),
		Header:     http.Header{},
	}
	upstream := &mockUpstream{name: "test", responses: []*http.Response{resp}}
	router := &mockRouter{upstream: upstream}
	ipRotator := &mockIPRotator{}

	cs := NewChatService(router, ipRotator, nil, 0, 0)
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

func TestChatServiceProxyChatRetriesOn429(t *testing.T) {
	resp429 := &http.Response{
		StatusCode: 429,
		Body:       io.NopCloser(strings.NewReader("")),
		Header:     http.Header{},
	}
	resp200 := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader("{}")),
		Header:     http.Header{},
	}
	upstream := &mockUpstream{
		name:      "test",
		responses: []*http.Response{resp429, resp200},
	}
	router := &mockRouter{upstream: upstream}
	ipRotator := &mockIPRotator{}

	cs := NewChatService(router, ipRotator, nil, 1, 10*time.Millisecond)
	w := &recordingResponseWriter{header: http.Header{}}
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)

	err := cs.ProxyChat(context.Background(), w, r, "test-model", []byte("{}"))
	if err != nil {
		t.Fatalf("ProxyChat failed: %v", err)
	}
	if upstream.calls != 2 {
		t.Errorf("expected 2 upstream calls, got %d", upstream.calls)
	}
	if ipRotator.forceNewIPCalls != 1 {
		t.Errorf("expected 1 ForceNewIP call, got %d", ipRotator.forceNewIPCalls)
	}
}

func TestChatServiceProxyChatClosesBodyOn429(t *testing.T) {
	closed := false
	body := &closeTracker{ReadCloser: io.NopCloser(strings.NewReader("")), onClose: func() { closed = true }}
	resp429 := &http.Response{StatusCode: 429, Body: body, Header: http.Header{}}
	resp200 := &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader("{}")), Header: http.Header{}}
	upstream := &mockUpstream{name: "test", responses: []*http.Response{resp429, resp200}}
	router := &mockRouter{upstream: upstream}
	ipRotator := &mockIPRotator{}

	cs := NewChatService(router, ipRotator, nil, 1, 10*time.Millisecond)
	w := &recordingResponseWriter{header: http.Header{}}
	r := httptest.NewRequest("POST", "/v1/chat/completions", nil)
	_ = cs.ProxyChat(context.Background(), w, r, "test-model", []byte("{}"))

	if !closed {
		t.Error("expected 429 response body to be closed before retry")
	}
}

type closeTracker struct {
	io.ReadCloser
	onClose func()
}

func (c *closeTracker) Close() error {
	if c.onClose != nil {
		c.onClose()
	}
	return c.ReadCloser.Close()
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

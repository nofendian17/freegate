package proxy

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	anyllm "github.com/mozilla-ai/any-llm-go"
	"github.com/mozilla-ai/any-llm-go/providers"

	"freegate/internal/model"
	"freegate/internal/upstream"
)

type stubUpstream struct {
	name         string
	completionFn func(ctx context.Context, params anyllm.CompletionParams) (*anyllm.ChatCompletion, error)
	streamFn     func(ctx context.Context, params anyllm.CompletionParams) (<-chan anyllm.ChatCompletionChunk, <-chan error)
	models       []model.Model
}

func (s *stubUpstream) Name() string                         { return s.name }
func (s *stubUpstream) Match(string) bool                    { return true }
func (s *stubUpstream) Models() []model.Model                { return s.models }
func (s *stubUpstream) Start(context.Context, time.Duration) {}
func (s *stubUpstream) ChatCompletion(context.Context, []byte) (*http.Response, error) {
	return nil, nil
}
func (s *stubUpstream) ListModels(context.Context) ([]model.Model, error) {
	return s.models, nil
}
func (s *stubUpstream) Provider() providers.Provider { return &stubProvider{upstream: s} }

type stubProvider struct {
	upstream *stubUpstream
}

func (p *stubProvider) Name() string { return p.upstream.name }
func (p *stubProvider) Completion(ctx context.Context, params anyllm.CompletionParams) (*anyllm.ChatCompletion, error) {
	if p.upstream.completionFn == nil {
		return nil, anyllm.ErrProvider
	}
	return p.upstream.completionFn(ctx, params)
}
func (p *stubProvider) CompletionStream(ctx context.Context, params anyllm.CompletionParams) (<-chan anyllm.ChatCompletionChunk, <-chan error) {
	if p.upstream.streamFn == nil {
		ch := make(chan anyllm.ChatCompletionChunk)
		er := make(chan error, 1)
		er <- anyllm.ErrProvider
		close(er)
		close(ch)
		return ch, er
	}
	return p.upstream.streamFn(ctx, params)
}

type fakeRouter struct{ u *stubUpstream }

func (f *fakeRouter) Select(string) upstream.Upstream { return f.u }
func (f *fakeRouter) AllModels() []model.Model        { return f.u.models }
func (f *fakeRouter) IsReady() bool                   { return true }

type fakeRotator struct{ calls atomic.Int32 }

func (r *fakeRotator) ForceNewIP() error { r.calls.Add(1); return nil }

type recordingRW struct {
	header http.Header
	buf    bytes.Buffer
	code   int
	flush  int
}

func newRec() *recordingRW { return &recordingRW{header: http.Header{}} }
func (r *recordingRW) Header() http.Header         { return r.header }
func (r *recordingRW) Write(b []byte) (int, error) { return r.buf.Write(b) }
func (r *recordingRW) WriteHeader(c int)           { r.code = c }
func (r *recordingRW) Flush()                      { r.flush++ }

func newTestProxy(u *stubUpstream, rot *fakeRotator) *Client {
	c := NewClient(&fakeRouter{u: u})
	c.maxRetry = 2
	if rot != nil {
		c.WithTorController(rot)
	}
	return c
}

func TestProxyChat_NonStream_RateLimitThenSuccess(t *testing.T) {
	var calls atomic.Int32
	u := &stubUpstream{
		name: "stub",
		completionFn: func(ctx context.Context, params anyllm.CompletionParams) (*anyllm.ChatCompletion, error) {
			n := calls.Add(1)
			if n == 1 {
				return nil, anyllm.ErrRateLimit
			}
			return &anyllm.ChatCompletion{
				ID: "ok", Object: "chat.completion", Model: params.Model,
				Choices: []anyllm.Choice{{Index: 0, Message: anyllm.Message{Role: "assistant", Content: "hi"}, FinishReason: "stop"}},
			}, nil
		},
	}
	rot := &fakeRotator{}
	c := newTestProxy(u, rot)

	w := newRec()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	params := anyllm.CompletionParams{Model: "test", Messages: []anyllm.Message{{Role: "user", Content: "hello"}}}
	c.ProxyChat(w, r, params)

	if calls.Load() != 2 {
		t.Errorf("expected 2 upstream calls, got %d", calls.Load())
	}
	if rot.calls.Load() != 1 {
		t.Errorf("expected 1 IP rotation, got %d", rot.calls.Load())
	}
	if c.metrics.RetryCount.Load() != 1 {
		t.Errorf("expected RetryCount=1, got %d", c.metrics.RetryCount.Load())
	}
	if w.code != 200 {
		t.Errorf("status = %d, want 200", w.code)
	}
	if !strings.Contains(w.buf.String(), `"content":"hi"`) {
		t.Errorf("body missing content: %s", w.buf.String())
	}
}

func TestProxyChat_NonStream_AuthError(t *testing.T) {
	var calls atomic.Int32
	u := &stubUpstream{
		name: "stub",
		completionFn: func(ctx context.Context, params anyllm.CompletionParams) (*anyllm.ChatCompletion, error) {
			calls.Add(1)
			return nil, anyllm.ErrAuthentication
		},
	}
	c := newTestProxy(u, nil)

	w := newRec()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	params := anyllm.CompletionParams{Model: "test", Messages: []anyllm.Message{{Role: "user", Content: "hello"}}}
	c.ProxyChat(w, r, params)

	if calls.Load() != 1 {
		t.Errorf("expected 1 upstream call (no retry), got %d", calls.Load())
	}
	if w.code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", w.code)
	}
	if !strings.Contains(w.buf.String(), `"upstream_error"`) {
		t.Errorf("body missing upstream_error: %s", w.buf.String())
	}
}

func TestProxyChat_NonStream_AllRateLimited(t *testing.T) {
	var calls atomic.Int32
	u := &stubUpstream{
		name: "stub",
		completionFn: func(ctx context.Context, params anyllm.CompletionParams) (*anyllm.ChatCompletion, error) {
			calls.Add(1)
			return nil, anyllm.ErrRateLimit
		},
	}
	rot := &fakeRotator{}
	c := newTestProxy(u, rot)

	w := newRec()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	params := anyllm.CompletionParams{Model: "test", Messages: []anyllm.Message{{Role: "user", Content: "hello"}}}
	c.ProxyChat(w, r, params)

	if calls.Load() != 3 {
		t.Errorf("expected 3 upstream calls (2 retries), got %d", calls.Load())
	}
	if rot.calls.Load() != 2 {
		t.Errorf("expected 2 IP rotations, got %d", rot.calls.Load())
	}
	if c.metrics.RetryCount.Load() != 2 {
		t.Errorf("expected RetryCount=2, got %d", c.metrics.RetryCount.Load())
	}
	if w.code != http.StatusBadGateway {
		t.Errorf("status = %d, want 502", w.code)
	}
}

func TestProxyChat_Stream_RateLimitThenSuccess(t *testing.T) {
	var calls atomic.Int32
	u := &stubUpstream{
		name: "stub",
		streamFn: func(ctx context.Context, params anyllm.CompletionParams) (<-chan anyllm.ChatCompletionChunk, <-chan error) {
			n := calls.Add(1)
			ch := make(chan anyllm.ChatCompletionChunk)
			er := make(chan error, 1)
			if n == 1 {
				er <- anyllm.ErrRateLimit
				close(er)
				close(ch)
			} else {
				go func() {
					defer close(ch)
					defer close(er)
					ch <- anyllm.ChatCompletionChunk{ID: "c1", Object: "chat.completion.chunk", Model: params.Model, Choices: []anyllm.ChunkChoice{{Index: 0, Delta: anyllm.ChunkDelta{Content: "hi"}}}}
				}()
			}
			return ch, er
		},
	}
	c := newTestProxy(u, nil)

	w := newRec()
	r := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	params := anyllm.CompletionParams{Model: "test", Messages: []anyllm.Message{{Role: "user", Content: "hello"}}, Stream: true}
	c.ProxyChat(w, r, params)

	if calls.Load() != 2 {
		t.Errorf("expected 2 stream attempts, got %d", calls.Load())
	}
	if w.code != 200 {
		t.Errorf("status = %d, want 200", w.code)
	}
	if !strings.Contains(w.buf.String(), `"content":"hi"`) {
		t.Errorf("body missing content: %s", w.buf.String())
	}
	if !strings.Contains(w.buf.String(), "data: [DONE]") {
		t.Errorf("body missing [DONE]: %s", w.buf.String())
	}
}

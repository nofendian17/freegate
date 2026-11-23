package upstream

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"freegate/internal/infrastructure/egress"
)

// testWarpDialer is a poolDialer that dials the local test server directly and
// counts rotations, so the retry path can be tested without a WARP device.
type testWarpDialer struct {
	rotations *atomic.Int32
	released  *atomic.Int32
	dialer    *net.Dialer
}

func (d *testWarpDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	return d.dialer.DialContext(ctx, network, addr)
}

func (d *testWarpDialer) Acquire(ctx context.Context) (context.Context, *egress.Lease) {
	release := func() { d.released.Add(1) }
	fail := func(string) <-chan struct{} {
		d.rotations.Add(1)
		return egress.ClosedSignal()
	}
	return ctx, egress.NewTestLease(d.DialContext, release, fail)
}

func newWarpClient(upstreamURL string, rotations *atomic.Int32) (*HTTPClient, *atomic.Int32) {
	released := &atomic.Int32{}
	d := &testWarpDialer{rotations: rotations, released: released, dialer: &net.Dialer{}}
	route := EgressRoute{testDialer: d}
	c := NewHTTPClientWithTransport(upstreamURL, []string{"public"}, nil, nil)
	c.SetRoute(NewWarpTransport(route, nil), nil, &route)
	c.SetFreeLimitHook(func(_ context.Context, resp *http.Response) bool {
		return isFreeUsageLimit(resp)
	})
	return c, released
}

func TestFreeLimitHookRotatesAndRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"type":"FreeUsageLimitError","message":"free usage limit reached"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	var rotations atomic.Int32
	c, released := newWarpClient(srv.URL, &rotations)
	resp, err := c.Post(context.Background(), "/v1/chat/completions", []byte(`{"model":"m"}`))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("upstream calls = %d, want 2", got)
	}
	if got := rotations.Load(); got != 1 {
		t.Errorf("rotations = %d, want 1", got)
	}
	if got := released.Load(); got != 2 {
		t.Errorf("released leases = %d, want 2", got)
	}
}

func TestFreeLimitHookIgnoresGeneric429(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limit exceeded"}}`)
	}))
	defer srv.Close()

	var rotations atomic.Int32
	c, released := newWarpClient(srv.URL, &rotations)
	resp, err := c.Post(context.Background(), "/v1/chat/completions", []byte(`{"model":"m"}`))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429", resp.StatusCode)
	}
	if got := calls.Load(); got != 1 {
		t.Errorf("upstream calls = %d, want 1 (no retry)", got)
	}
	if got := rotations.Load(); got != 0 {
		t.Errorf("rotations = %d, want 0", got)
	}
	if got := released.Load(); got != 0 {
		t.Errorf("released before body close = %d, want 0", got)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "rate limit exceeded") {
		t.Errorf("body = %q", body)
	}
	_ = resp.Body.Close()
	if got := released.Load(); got != 1 {
		t.Errorf("released after body close = %d, want 1", got)
	}
}

// TestFreeLimitHookRotatesUntilSuccess reproduces a request that gets two
// free-usage 429s and must rotate twice rather than dropping the connection.
func TestFreeLimitHookRotatesUntilSuccess(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= 2 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"type":"FreeUsageLimitError","message":"free usage limit reached"}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	var rotations atomic.Int32
	c, _ := newWarpClient(srv.URL, &rotations)
	resp, err := c.Post(context.Background(), "/v1/chat/completions", []byte(`{"model":"m"}`))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200 after repeated 429s", resp.StatusCode)
	}
	if got := calls.Load(); got != 3 {
		t.Errorf("upstream calls = %d, want 3", got)
	}
	if got := rotations.Load(); got != 2 {
		t.Errorf("rotations = %d, want 2", got)
	}
}

// TestFreeLimitHookRetriesOnlyOnce documents the bound: after the configured
// maximum rotations a still-limited upstream is returned unchanged.
// TestKiloAndLLM7InstallRateLimitHook guards that their free 429s rotate too.
func TestKiloAndLLM7InstallRateLimitHook(t *testing.T) {
	ctx := context.Background()
	check := func(name string, c *HTTPClient) {
		t.Helper()
		if c.onFreeLimit == nil {
			t.Fatalf("%s: rate limit hook not installed", name)
		}
		resp := &http.Response{StatusCode: http.StatusTooManyRequests, Body: http.NoBody}
		if !c.onFreeLimit(ctx, resp) {
			t.Errorf("%s: generic 429 did not match the hook", name)
		}
		resp.StatusCode = http.StatusOK
		if c.onFreeLimit(ctx, resp) {
			t.Errorf("%s: 200 matched the rate limit hook", name)
		}
	}
	kilo := NewKiloUpstreamWithEgress("http://example.invalid", "k", nil, egress.NewRouter())
	check("kilo", kilo.client)
	llm7 := NewLLM7UpstreamWithEgress("http://example.invalid", nil, egress.NewRouter())
	check("llm7", llm7.client)
}

func TestGeneric429RotatesAndRetries(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = io.WriteString(w, `{"error":{"message":"too many requests"}}`)
			return
		}
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	var rotations atomic.Int32
	released := &atomic.Int32{}
	d := &testWarpDialer{rotations: &rotations, released: released, dialer: &net.Dialer{}}
	route := EgressRoute{testDialer: d}
	c := NewHTTPClientWithTransport(srv.URL, []string{"public"}, nil, nil)
	c.SetRoute(NewWarpTransport(route, nil), nil, &route)
	c.SetFreeLimitHook(anyRateLimit)

	resp, err := c.Post(context.Background(), "/v1/chat/completions", []byte(`{"model":"m"}`))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK || calls.Load() != 2 || rotations.Load() != 1 {
		t.Fatalf("status=%d calls=%d rotations=%d", resp.StatusCode, calls.Load(), rotations.Load())
	}
}

func TestFreeLimitHookRetriesOnlyOnce(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"type":"FreeUsageLimitError"}`)
	}))
	defer srv.Close()

	var rotations atomic.Int32
	c, _ := newWarpClient(srv.URL, &rotations)
	resp, err := c.Post(context.Background(), "/v1/chat/completions", []byte(`{"model":"m"}`))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 after retries exhausted", resp.StatusCode)
	}
	if got := calls.Load(); got != maxFreeLimitRetries+1 {
		t.Errorf("upstream calls = %d, want %d", got, maxFreeLimitRetries+1)
	}
	if got := rotations.Load(); got != maxFreeLimitRetries {
		t.Errorf("rotations = %d, want %d", got, maxFreeLimitRetries)
	}
}

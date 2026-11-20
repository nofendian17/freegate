package upstream

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"freegate/internal/infrastructure/egress"
	"freegate/internal/infrastructure/egress/egresstest"
)

// newWarpClient builds an HTTPClient whose WARP transport dials through a real
// egress.Pool over a local SOCKS5 server, so rotation and retry can be tested
// without a sidecar.
func newWarpClient(t *testing.T, upstreamURL, controlURL string) *HTTPClient {
	t.Helper()
	proxy := egresstest.New(t)
	pool := egress.NewPool(egress.Options{
		Endpoints: []egress.Endpoint{{Name: "warp-1", Socks: proxy.Addr(), Control: controlURL}},
	})
	router := egress.NewRouter()
	router.SetDefault(pool)
	route := EgressRoute{Router: router}
	c := NewHTTPClientWithTransport(upstreamURL, []string{"public"}, nil, nil)
	c.SetRoute(NewWarpTransport(route, nil), nil, &route)
	c.SetFreeLimitHook(func(_ context.Context, resp *http.Response) bool {
		return isFreeUsageLimit(resp)
	})
	return c
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
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/rotate" {
			rotations.Add(1)
		}
		_, _ = io.WriteString(w, `{"ip":"2a09:bac1:34a0:10::1"}`)
	}))
	defer control.Close()

	c := newWarpClient(t, srv.URL, control.URL)
	resp, err := c.Post(context.Background(), "/v1/chat/completions", []byte(`{"model":"m"}`))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := calls.Load(); got != 2 {
		t.Errorf("upstream calls = %d, want 2", got)
	}
	if got := rotations.Load(); got != 1 {
		t.Errorf("rotations = %d, want 1", got)
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
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/rotate" {
			rotations.Add(1)
		}
		_, _ = io.WriteString(w, `{"ip":"2a09:bac1:34a0:10::1"}`)
	}))
	defer control.Close()

	c := newWarpClient(t, srv.URL, control.URL)
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
	// The hook must rewind the body so the caller still sees the error.
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if !strings.Contains(string(body), "rate limit exceeded") {
		t.Errorf("body = %q", body)
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
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost && r.URL.Path == "/rotate" {
			rotations.Add(1)
		}
		_, _ = io.WriteString(w, `{"ip":"2a09:bac1:34a0:10::1"}`)
	}))
	defer control.Close()

	c := newWarpClient(t, srv.URL, control.URL)
	resp, err := c.Post(context.Background(), "/v1/chat/completions", []byte(`{"model":"m"}`))
	if err != nil {
		t.Fatalf("Post: %v", err)
	}
	_ = resp.Body.Close()
	if got := calls.Load(); got != 2 {
		t.Errorf("upstream calls = %d, want 2", got)
	}
	if got := rotations.Load(); got != 1 {
		t.Errorf("rotations = %d, want 1", got)
	}
}

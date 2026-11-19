package upstream

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

type testEgress struct {
	dialed  atomic.Int32
	rotated atomic.Int32
	dialFn  func(ctx context.Context, network, addr string) (net.Conn, error)
}

func (e *testEgress) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	e.dialed.Add(1)
	if e.dialFn != nil {
		return e.dialFn(ctx, network, addr)
	}
	var d net.Dialer
	return d.DialContext(ctx, network, addr)
}

func (e *testEgress) RotateAsync(reason string) {
	e.rotated.Add(1)
}

func TestWarpTransport_NilEgressFails(t *testing.T) {
	tr := NewWarpTransport(nil, nil)
	client := &http.Client{Transport: tr}
	_, err := client.Get("http://example.com")
	if err == nil {
		t.Fatal("expected error with nil egress")
	}
	if !errors.Is(err, ErrNoWarpPool) {
		t.Fatalf("expected ErrNoWarpPool, got: %v", err)
	}
}

func TestWarpTransport_DialsViaEgress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	eg := &testEgress{}
	tr := NewWarpTransport(eg, nil)
	client := &http.Client{Transport: tr}

	resp, err := client.Get(srv.URL)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	_ = resp.Body.Close()

	if eg.dialed.Load() != 1 {
		t.Fatalf("expected 1 dial via egress, got %d", eg.dialed.Load())
	}
}

func TestBuiltinSetProxy_Warp(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	eg := &testEgress{}

	// Kilo
	kilo := NewKiloUpstreamWithEgress(srv.URL, "k", nil, eg)
	kilo.SetProxy("warp", nil)
	_, err := kilo.client.Get(context.Background(), "/")
	if err != nil {
		t.Fatalf("kilo request failed: %v", err)
	}
	if eg.dialed.Load() == 0 {
		t.Fatal("expected kilo dial via egress in warp mode")
	}

	// LLM7
	eg.dialed.Store(0)
	llm7 := NewLLM7UpstreamWithEgress(srv.URL, nil, eg)
	llm7.SetProxy("warp", nil)
	_, err = llm7.client.Get(context.Background(), "/")
	if err != nil {
		t.Fatalf("llm7 request failed: %v", err)
	}
	if eg.dialed.Load() == 0 {
		t.Fatal("expected llm7 dial via egress in warp mode")
	}
}

package upstream

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"freegate/internal/infrastructure/egress"
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

func (e *testEgress) Acquire(ctx context.Context) (context.Context, *egress.Lease) {
	return ctx, nil
}

func TestWarpTransport_NilEgressFails(t *testing.T) {
	tr := NewWarpTransport(EgressRoute{}, nil)
	client := &http.Client{Transport: tr}
	_, err := client.Get("http://example.com")
	if err == nil {
		t.Fatal("expected error with nil egress")
	}
	if !errors.Is(err, ErrNoWarpPool) {
		t.Fatalf("expected ErrNoWarpPool, got: %v", err)
	}
}

func TestWarpTransportPinsHTTP11AndDisablesKeepAlives(t *testing.T) {
	tr := NewWarpTransport(EgressRoute{}, nil)
	if !tr.DisableKeepAlives {
		t.Error("WARP transport keeps connections alive; a retired exit could be reused")
	}
	if tr.ForceAttemptHTTP2 {
		t.Error("WARP transport still attempts HTTP/2")
	}
	if tr.TLSNextProto == nil || len(tr.TLSNextProto) != 0 {
		t.Error("WARP transport did not clear HTTP/2 next-protos")
	}
	if tr.TLSClientConfig == nil || len(tr.TLSClientConfig.NextProtos) != 1 || tr.TLSClientConfig.NextProtos[0] != "http/1.1" {
		t.Error("WARP transport does not pin ALPN to http/1.1")
	}
}

func TestWarpTransport_DialsViaEgress(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	eg := &testEgress{}
	tr := NewWarpTransport(EgressRoute{testDialer: eg}, nil)
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

// TestBuiltinSetProxy_WarpWithoutRouterFailsClosed guards the whole feature:
// selecting WARP with no pool must fail, never dial the host IP.
func TestBuiltinSetProxy_WarpWithoutRouterFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	kilo := NewKiloUpstreamWithEgress(srv.URL, "k", nil, nil)
	kilo.SetProxy("warp", nil, 0)
	if _, err := kilo.client.Get(context.Background(), "/"); !errors.Is(err, ErrNoWarpPool) {
		t.Fatalf("kilo warp without pool: got %v, want ErrNoWarpPool", err)
	}

	llm7 := NewLLM7UpstreamWithEgress(srv.URL, nil, nil)
	llm7.SetProxy("warp", nil, 0)
	if _, err := llm7.client.Get(context.Background(), "/"); !errors.Is(err, ErrNoWarpPool) {
		t.Fatalf("llm7 warp without pool: got %v, want ErrNoWarpPool", err)
	}
}

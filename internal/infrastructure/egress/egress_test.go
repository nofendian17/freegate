package egress

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"freegate/internal/infrastructure/egress/egresstest"
)

// controlAddr is a fake control API. Each index gets its own IPv6 /64 so the
// prefix-collision retry does not turn a one-rotation test into three.
func controlAddr(n int) string {
	return fmt.Sprintf("2a09:bac1:34a%d:10::1", n)
}

// newControlAPI serves GET /ip (used for collision checks) and counts
// POST /rotate. Passing nil for calls disables counting.
func newControlAPI(t *testing.T, ip string, calls *atomic.Int32) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/rotate":
			if calls != nil {
				calls.Add(1)
			}
		case r.Method == http.MethodGet && r.URL.Path == "/ip":
		default:
			t.Errorf("unexpected control call %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"ip":"` + ip + `"}`))
	}))
}

func TestParsePool(t *testing.T) {
	eps, err := ParsePool(" warp-1:1080@http://warp-1:8081 , warp-2:1080 ")
	if err != nil {
		t.Fatalf("ParsePool: %v", err)
	}
	if len(eps) != 2 {
		t.Fatalf("got %d endpoints", len(eps))
	}
	if eps[0].Name != "warp-1" || eps[0].Socks != "warp-1:1080" || eps[0].Control != "http://warp-1:8081" {
		t.Errorf("endpoint 0 = %+v", eps[0])
	}
	if eps[1].Control != "" {
		t.Errorf("endpoint 1 control = %q", eps[1].Control)
	}

	for _, bad := range []string{"warp-1", "warp-1:1080@not-a-url", "warp-1:abc"} {
		if _, err := ParsePool(bad); err == nil {
			t.Errorf("ParsePool(%q) accepted an invalid endpoint", bad)
		}
	}
}

func TestDialThroughSocks5(t *testing.T) {
	// A local echo server stands in for the upstream: the fake SOCKS5 dials
	// whatever target it is given, so this checks the tunnel end to end.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = conn.Close() }()
				_, _ = io.Copy(conn, conn)
			}()
		}
	}()

	proxy := egresstest.New(t)
	p := NewPool(Options{Endpoints: []Endpoint{{Name: "warp-1", Socks: proxy.Addr()}}})

	conn, err := p.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if got := proxy.Target(); got != ln.Addr().String() {
		t.Errorf("proxy saw target %q", got)
	}
	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	buf := make([]byte, 4)
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(buf) != "ping" {
		t.Errorf("echo = %q", buf)
	}
}

// localTarget returns the address of a throwaway TCP listener that accepts
// and closes connections, so a dial succeeds without touching the network.
func localTarget(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()
	return ln.Addr().String()
}

func TestDialFailsOver(t *testing.T) {
	proxy := egresstest.New(t)
	// 127.0.0.1:1 is a port nothing listens on, so the first endpoint fails.
	p := NewPool(Options{
		Endpoints: []Endpoint{
			{Name: "dead", Socks: "127.0.0.1:1"},
			{Name: "live", Socks: proxy.Addr()},
		},
		FailureCool: time.Minute,
	})

	conn, err := p.DialContext(context.Background(), "tcp", localTarget(t))
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	_ = conn.Close()

	dead := p.Snapshot()[0]
	if dead.Status != StatusCooling {
		t.Errorf("dead endpoint status = %q, want cooling", dead.Status)
	}
}

func TestDialFallbackDirect(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			_ = c.Close()
		}
	}()

	p := NewPool(Options{
		Endpoints:      []Endpoint{{Name: "dead", Socks: "127.0.0.1:1"}},
		FallbackDirect: true,
	})
	conn, err := p.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("fallback dial: %v", err)
	}
	_ = conn.Close()
}

func TestDialNoEndpointFailsClosed(t *testing.T) {
	p := NewPool(Options{Endpoints: []Endpoint{{Name: "dead", Socks: "127.0.0.1:1"}}})
	_, err := p.DialContext(context.Background(), "tcp", localTarget(t))
	if !errors.Is(err, ErrNoEndpoint) {
		t.Fatalf("err = %v, want ErrNoEndpoint", err)
	}
}

func TestRotateCallsControlAPI(t *testing.T) {
	var calls atomic.Int32
	control := newControlAPI(t, controlAddr(0), &calls)
	defer control.Close()

	proxy := egresstest.New(t)
	p := NewPool(Options{
		Endpoints: []Endpoint{{Name: "warp-1", Socks: proxy.Addr(), Control: control.URL}},
	})
	ctx, lease := p.Acquire(context.Background())
	conn, err := p.DialContext(ctx, "tcp", localTarget(t))
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	<-lease.RotateAsync("free usage limit", nil)
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if calls.Load() == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if calls.Load() != 1 {
		t.Fatalf("control calls = %d, want 1", calls.Load())
	}
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		snap := p.Snapshot()[0]
		if snap.Status == StatusReady && snap.IP != "" {
			if snap.IP != "2a09:bac1:34a0:10::1" {
				t.Errorf("ip = %q", snap.IP)
			}
			if snap.Rotations != 1 {
				t.Errorf("rotations = %d", snap.Rotations)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("endpoint never became ready: %+v", p.Snapshot())
}

// echoTarget keeps accepted connections open, unlike localTarget which closes
// them immediately. A drain test needs a connection that stays live.
func echoTarget(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				_, _ = io.Copy(c, c)
			}()
		}
	}()
	return ln.Addr().String()
}

// TestRotateWaitsForInFlightConnection is the regression guard for the WARP
// rotation cutting live traffic. WARP drops the whole tunnel on rotation, so
// the control API must not be called while a connection is still open.
func TestRotateWaitsForInFlightConnection(t *testing.T) {
	var calls atomic.Int32
	control := newControlAPI(t, controlAddr(0), &calls)
	defer control.Close()

	proxy := egresstest.New(t)
	p := NewPool(Options{Endpoints: []Endpoint{{Name: "warp-1", Socks: proxy.Addr(), Control: control.URL}}})
	ctx, lease := p.Acquire(context.Background())
	conn, err := p.DialContext(ctx, "tcp", echoTarget(t))
	if err != nil {
		t.Fatal(err)
	}

	done := lease.RotateAsync("free usage limit", nil)
	// Wait until the rotation goroutine has actually claimed the endpoint,
	// then assert it did not call the control API while the connection lives.
	deadline := time.Now().Add(3 * time.Second)
	for p.Snapshot()[0].Status != StatusRotating && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if snap := p.Snapshot()[0]; snap.Status != StatusRotating {
		t.Fatalf("status while draining = %q, want rotating", snap.Status)
	}
	if got := calls.Load(); got != 0 {
		t.Fatalf("control calls with a live connection = %d, want 0", got)
	}

	_ = conn.Close()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("rotation did not finish after the connection closed")
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("control calls after drain = %d, want 1", got)
	}
	if snap := p.Snapshot()[0]; snap.Status != StatusReady || snap.Rotations != 1 {
		t.Fatalf("endpoint after rotation = %+v, want ready with 1 rotation", snap)
	}
}

// TestRotateSkippedWhenEndpointBusy verifies the drain timeout is a hard stop:
// a long stream is never cut off just to chase a fresh IP. The endpoint goes
// back to service and the caller keeps serving the limit response.
func TestRotateSkippedWhenEndpointBusy(t *testing.T) {
	var calls atomic.Int32
	control := newControlAPI(t, controlAddr(0), &calls)
	defer control.Close()

	proxy := egresstest.New(t)
	p := NewPool(Options{
		Endpoints:    []Endpoint{{Name: "warp-1", Socks: proxy.Addr(), Control: control.URL}},
		DrainTimeout: 100 * time.Millisecond,
	})
	ctx, lease := p.Acquire(context.Background())
	conn, err := p.DialContext(ctx, "tcp", echoTarget(t))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close() }()

	<-lease.RotateAsync("free usage limit", nil)
	if got := calls.Load(); got != 0 {
		t.Fatalf("control calls with a busy endpoint = %d, want 0", got)
	}
	if snap := p.Snapshot()[0]; snap.Status != StatusReady || snap.Rotations != 0 {
		t.Fatalf("endpoint after skipped rotation = %+v, want ready with 0 rotations", snap)
	}
	// The endpoint must remain usable after an aborted rotation.
	c2, err := p.DialContext(context.Background(), "tcp", echoTarget(t))
	if err != nil {
		t.Fatalf("dial after skipped rotation: %v", err)
	}
	_ = c2.Close()
}

// TestRotateDrainCallsCloseIdle checks that the transport gets a chance to
// release connections that finish while the drain is in progress.
func TestRotateDrainCallsCloseIdle(t *testing.T) {
	control := newControlAPI(t, controlAddr(0), nil)
	defer control.Close()

	proxy := egresstest.New(t)
	p := NewPool(Options{Endpoints: []Endpoint{{Name: "warp-1", Socks: proxy.Addr(), Control: control.URL}}})
	ctx, lease := p.Acquire(context.Background())
	conn, err := p.DialContext(ctx, "tcp", echoTarget(t))
	if err != nil {
		t.Fatal(err)
	}

	var idleClosed atomic.Int32
	done := lease.RotateAsync("free usage limit", func() { idleClosed.Add(1) })
	deadline := time.Now().Add(3 * time.Second)
	for idleClosed.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	_ = conn.Close()
	<-done
	if idleClosed.Load() == 0 {
		t.Fatal("closeIdle was never called during the drain")
	}
}

func TestRotateWithoutControlKeepsEndpointUsable(t *testing.T) {
	proxy := egresstest.New(t)
	p := NewPool(Options{
		Endpoints: []Endpoint{{Name: "warp-1", Socks: proxy.Addr()}},
	})
	ctx, lease := p.Acquire(context.Background())
	conn, err := p.DialContext(ctx, "tcp", localTarget(t))
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	// No control API: the endpoint cannot change identity, but it still
	// serves traffic, so it must not be taken out of the pool.
	<-lease.RotateAsync("test", nil)
	if snap := p.Snapshot()[0]; snap.Status != StatusReady {
		t.Fatalf("endpoint status = %q, want ready", snap.Status)
	}
	conn, err = p.DialContext(context.Background(), "tcp", localTarget(t))
	if err != nil {
		t.Fatalf("dial after rotation attempt: %v", err)
	}
	_ = conn.Close()
}

// TestRotateReusedConnectionUsesLastDial covers the pooled-connection path:
// a request that reuses a connection never dials, but its 429 still rotates
// the exit that most recently served a dial.
func TestRotateReusedConnectionUsesLastDial(t *testing.T) {
	var calls atomic.Int32
	control := newControlAPI(t, controlAddr(0), &calls)
	defer control.Close()

	proxy := egresstest.New(t)
	p := NewPool(Options{
		Endpoints: []Endpoint{{Name: "warp-1", Socks: proxy.Addr(), Control: control.URL}},
	})
	ctx, lease := p.Acquire(context.Background())
	conn, err := p.DialContext(ctx, "tcp", localTarget(t))
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	_ = lease // this lease was attributed by the dial; the next one is not

	_, reused := p.Acquire(context.Background())
	<-reused.RotateAsync("free usage limit", nil)
	if got := calls.Load(); got != 1 {
		t.Fatalf("control calls = %d, want 1", got)
	}
}

func TestNilPoolFailsClosed(t *testing.T) {
	var p *Pool
	ctx, lease := p.Acquire(context.Background())
	if lease != nil {
		t.Error("nil pool must not attribute a lease")
	}
	if _, err := p.DialContext(ctx, "tcp", "opencode.ai:443"); !errors.Is(err, ErrNoEndpoint) {
		t.Fatalf("nil pool dial err = %v, want ErrNoEndpoint", err)
	}
	if p.Len() != 0 {
		t.Error("nil pool Len")
	}
	if got := p.Snapshot(); got != nil {
		t.Errorf("nil pool snapshot = %v", got)
	}
}

func TestRotateOnlyDialedEndpoint(t *testing.T) {
	calls := map[string]*atomic.Int32{"a": {}, "b": {}}
	// Distinct /64s: otherwise the served endpoint's fresh IP collides with
	// the other exit and the collision retry rotates it more than once.
	ctrlA := newControlAPI(t, controlAddr(1), calls["a"])
	ctrlB := newControlAPI(t, controlAddr(2), calls["b"])
	defer ctrlA.Close()
	defer ctrlB.Close()
	proxy := egresstest.New(t)
	p := NewPool(Options{Endpoints: []Endpoint{
		{Name: "warp-a", Socks: proxy.Addr(), Control: ctrlA.URL},
		{Name: "warp-b", Socks: proxy.Addr(), Control: ctrlB.URL},
	}})
	ctx, lease := p.Acquire(context.Background())
	conn, err := p.DialContext(ctx, "tcp", localTarget(t))
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	<-lease.RotateAsync("free usage limit", nil)
	// The round-robin cursor starts at warp-a, so that is the dial this
	// request used. Only warp-a may rotate; warp-b must keep its identity.
	if got := calls["a"].Load(); got != 1 {
		t.Fatalf("served endpoint rotations = %d, want 1", got)
	}
	if got := calls["b"].Load(); got != 0 {
		t.Fatalf("untouched endpoint rotated %d times, want 0", got)
	}
}

func TestRotateWithoutDialIsNoop(t *testing.T) {
	var calls atomic.Int32
	control := newControlAPI(t, controlAddr(0), &calls)
	defer control.Close()

	p := NewPool(Options{
		Endpoints: []Endpoint{{Name: "warp-1", Socks: "127.0.0.1:1", Control: control.URL}},
	})
	// No dial has been attributed to the lease yet: a 429 burst must not
	// rotate a container at random.
	_, lease := p.Acquire(context.Background())
	<-lease.RotateAsync("free usage limit", nil)
	if got := calls.Load(); got != 0 {
		t.Fatalf("control calls = %d, want 0", got)
	}
}

func TestPrefix64(t *testing.T) {
	cases := []struct {
		ip, want string
	}{
		{"2a09:bac1:34a0:10::1", "2a09:bac1:34a0:10::/64"},
		{"2a09:bac1:34a0:10::abcd", "2a09:bac1:34a0:10::/64"},
		{"2a09:bac1:34a1:10::1", "2a09:bac1:34a1:10::/64"},
		{"", ""},
		{"not-an-ip", ""},
		{"203.0.113.7", ""},
	}
	for _, tc := range cases {
		if got := prefix64(tc.ip); got != tc.want {
			t.Errorf("prefix64(%q) = %q, want %q", tc.ip, got, tc.want)
		}
	}
}

// TestSnapshotFlagsDuplicatePrefix checks that two exits sharing a /64 are
// both marked, so the pool's apparent size is not mistaken for quota.
func TestSnapshotFlagsDuplicatePrefix(t *testing.T) {
	shared := "2a09:bac1:34a1:10::"
	a := newControlAPI(t, shared+"1", nil)
	defer a.Close()
	b := newControlAPI(t, shared+"2", nil)
	defer b.Close()

	proxy := egresstest.New(t)
	p := NewPool(Options{Endpoints: []Endpoint{
		{Name: "a", Socks: proxy.Addr(), Control: a.URL},
		{Name: "b", Socks: proxy.Addr(), Control: b.URL},
	}})
	p.probeIPs(context.Background())
	snaps := p.Snapshot()
	if len(snaps) != 2 {
		t.Fatalf("snapshots = %d, want 2", len(snaps))
	}
	for _, s := range snaps {
		if !s.DuplicatePrefix {
			t.Errorf("endpoint %q not flagged as sharing a /64: %+v", s.Name, s)
		}
		if s.Prefix != "2a09:bac1:34a1:10::/64" {
			t.Errorf("endpoint %q prefix = %q", s.Name, s.Prefix)
		}
	}
}

// TestRotateRetriesOnPrefixCollision verifies that a fresh identity in a /64
// already used by another exit is rejected in favour of a unique one.
func TestRotateRetriesOnPrefixCollision(t *testing.T) {
	colliding := "2a09:bac1:34a1:10::9"
	unique := controlAddr(2)
	var aRotations, bRotations atomic.Int32
	controlA := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/ip" {
			_, _ = w.Write([]byte(`{"ip":"` + colliding + `"}`))
			return
		}
		if r.Method == http.MethodPost && r.URL.Path == "/rotate" {
			ip := colliding
			if aRotations.Add(1) > 1 {
				ip = unique
			}
			_, _ = w.Write([]byte(`{"ip":"` + ip + `"}`))
			return
		}
		t.Errorf("unexpected control call %s %s", r.Method, r.URL.Path)
	}))
	defer controlA.Close()
	controlB := newControlAPI(t, controlAddr(1), &bRotations)
	defer controlB.Close()

	proxy := egresstest.New(t)
	p := NewPool(Options{Endpoints: []Endpoint{
		{Name: "a", Socks: proxy.Addr(), Control: controlA.URL},
		{Name: "b", Socks: proxy.Addr(), Control: controlB.URL},
	}})
	ctx, lease := p.Acquire(context.Background())
	conn, err := p.DialContext(ctx, "tcp", localTarget(t))
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	<-lease.RotateAsync("free usage limit", nil)
	if got := aRotations.Load(); got != 2 {
		t.Fatalf("rotations of served endpoint = %d, want 2 (one collision retry)", got)
	}
	if got := bRotations.Load(); got != 0 {
		t.Fatalf("rotations of untouched endpoint = %d, want 0", got)
	}
	if got := p.Snapshot()[0].IP; got != unique {
		t.Fatalf("final IP = %q, want unique %q", got, unique)
	}
}

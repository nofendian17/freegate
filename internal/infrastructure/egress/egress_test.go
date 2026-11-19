package egress

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeSocks5 is a minimal SOCKS5 server that records the requested target and
// echoes everything it receives back to the client.
type fakeSocks5 struct {
	ln     net.Listener
	target atomic.Value // string
	wg     sync.WaitGroup
}

func newFakeSocks5(t *testing.T) *fakeSocks5 {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	f := &fakeSocks5{ln: ln}
	f.wg.Add(1)
	go func() {
		defer f.wg.Done()
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go f.handle(conn)
		}
	}()
	t.Cleanup(func() {
		_ = ln.Close()
		f.wg.Wait()
	})
	return f
}

func (f *fakeSocks5) addr() string { return f.ln.Addr().String() }

func (f *fakeSocks5) handle(conn net.Conn) {
	defer func() { _ = conn.Close() }()
	var greeting [3]byte
	if _, err := io.ReadFull(conn, greeting[:]); err != nil {
		return
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return
	}
	var head [4]byte
	if _, err := io.ReadFull(conn, head[:]); err != nil {
		return
	}
	var host string
	switch head[3] {
	case 0x01:
		var b [4]byte
		if _, err := io.ReadFull(conn, b[:]); err != nil {
			return
		}
		host = net.IP(b[:]).String()
	case 0x04:
		var b [16]byte
		if _, err := io.ReadFull(conn, b[:]); err != nil {
			return
		}
		host = net.IP(b[:]).String()
	case 0x03:
		var l [1]byte
		if _, err := io.ReadFull(conn, l[:]); err != nil {
			return
		}
		b := make([]byte, l[0])
		if _, err := io.ReadFull(conn, b); err != nil {
			return
		}
		host = string(b)
	default:
		return
	}
	var port [2]byte
	if _, err := io.ReadFull(conn, port[:]); err != nil {
		return
	}
	f.target.Store(net.JoinHostPort(host, strconv.Itoa(int(port[0])<<8|int(port[1]))))
	// Success with an all-zero IPv4 bound address.
	if _, err := conn.Write([]byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}); err != nil {
		return
	}
	_, _ = io.Copy(conn, conn)
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
	proxy := newFakeSocks5(t)
	p := NewPool(Options{Endpoints: []Endpoint{{Name: "warp-1", Socks: proxy.addr()}}})

	conn, err := p.DialContext(context.Background(), "tcp", "opencode.ai:443")
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer func() { _ = conn.Close() }()

	if got := proxy.target.Load(); got != "opencode.ai:443" {
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

func TestDialFailsOver(t *testing.T) {
	proxy := newFakeSocks5(t)
	// 127.0.0.1:1 is a port nothing listens on, so the first endpoint fails.
	p := NewPool(Options{
		Endpoints: []Endpoint{
			{Name: "dead", Socks: "127.0.0.1:1"},
			{Name: "live", Socks: proxy.addr()},
		},
		FailureCool: time.Minute,
	})

	conn, err := p.DialContext(context.Background(), "tcp", "opencode.ai:443")
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
	_, err := p.DialContext(context.Background(), "tcp", "opencode.ai:443")
	if !errors.Is(err, ErrNoEndpoint) {
		t.Fatalf("err = %v, want ErrNoEndpoint", err)
	}
}

func TestRotateCallsControlAPI(t *testing.T) {
	var calls atomic.Int32
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/rotate" {
			t.Errorf("unexpected control call %s %s", r.Method, r.URL.Path)
		}
		calls.Add(1)
		_, _ = w.Write([]byte(`{"ip":"2a09:bac1:34a0:10::1"}`))
	}))
	defer control.Close()

	proxy := newFakeSocks5(t)
	p := NewPool(Options{
		Endpoints: []Endpoint{{Name: "warp-1", Socks: proxy.addr(), Control: control.URL}},
	})
	conn, err := p.DialContext(context.Background(), "tcp", "opencode.ai:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	p.RotateAsync("free usage limit")
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

func TestRotateWithoutControlKeepsEndpointUsable(t *testing.T) {
	proxy := newFakeSocks5(t)
	p := NewPool(Options{
		Endpoints: []Endpoint{{Name: "warp-1", Socks: proxy.addr()}},
	})
	conn, err := p.DialContext(context.Background(), "tcp", "opencode.ai:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()

	// No control API: the endpoint cannot change identity, but it still
	// serves traffic, so it must not be taken out of the pool.
	p.RotateAsync("test")
	time.Sleep(200 * time.Millisecond)
	if snap := p.Snapshot()[0]; snap.Status != StatusReady {
		t.Fatalf("endpoint status = %q, want ready", snap.Status)
	}
	conn, err = p.DialContext(context.Background(), "tcp", "opencode.ai:443")
	if err != nil {
		t.Fatalf("dial after rotation attempt: %v", err)
	}
	_ = conn.Close()
}

func TestNilPoolDialsDirect(t *testing.T) {
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
	var p *Pool
	conn, err := p.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatalf("nil pool dial: %v", err)
	}
	_ = conn.Close()
	if p.Len() != 0 {
		t.Error("nil pool Len")
	}
	if got := p.Snapshot(); got != nil {
		t.Errorf("nil pool snapshot = %v", got)
	}
}

func TestRotateWithoutDialIsNoop(t *testing.T) {
	var calls atomic.Int32
	control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"ip":"2a09:bac1:34a0:10::2"}`))
	}))
	defer control.Close()

	p := NewPool(Options{
		Endpoints: []Endpoint{{Name: "warp-1", Socks: "127.0.0.1:1", Control: control.URL}},
	})
	// No dial has been attributed to an endpoint yet: a 429 burst must not
	// rotate a container at random.
	p.RotateAsync("free usage limit")
	time.Sleep(100 * time.Millisecond)
	if got := calls.Load(); got != 0 {
		t.Fatalf("control calls = %d, want 0", got)
	}
}

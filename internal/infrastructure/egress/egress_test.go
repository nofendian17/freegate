package egress

import (
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"
)

type fakeBackend struct {
	mu       sync.Mutex
	snaps    []Snapshot
	acquired int
	released int
	failed   int
	closed   int
	delay    time.Duration
	failNoop bool
}

func newFakeBackend() *fakeBackend {
	return &fakeBackend{snaps: []Snapshot{{ID: "old", Prefix: "2001:db8::/64", Egress: "2001:db8::1"}}}
}

func (f *fakeBackend) Acquire(context.Context) (*Lease, error) {
	f.mu.Lock()
	f.acquired++
	f.mu.Unlock()
	return &Lease{
		dial: func(context.Context, string, string) (net.Conn, error) {
			c1, c2 := net.Pipe()
			_ = c2.Close()
			return c1, nil
		},
		release: func() {
			f.mu.Lock()
			f.released++
			f.mu.Unlock()
		},
		fail: func(string) <-chan struct{} {
			f.mu.Lock()
			f.failed++
			noop := f.failNoop
			snaps := []Snapshot{{ID: "fresh", Prefix: "2001:db9::/64", Egress: "2001:db9::1"}}
			delay := f.delay
			f.mu.Unlock()
			if noop {
				return ClosedSignal()
			}
			done := make(chan struct{})
			go func() {
				if delay > 0 {
					time.Sleep(delay)
				}
				f.mu.Lock()
				f.snaps = snaps
				f.mu.Unlock()
				close(done)
			}()
			return done
		},
	}, nil
}

func (f *fakeBackend) Snapshot() []Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Snapshot(nil), f.snaps...)
}

func (f *fakeBackend) Close() {
	f.mu.Lock()
	f.closed++
	f.mu.Unlock()
}

func (f *fakeBackend) counts() (int, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.acquired, f.released, f.failed
}

func (f *fakeBackend) closeCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.closed
}

func TestDialWithoutLeaseReleasesTemporaryLease(t *testing.T) {
	backend := newFakeBackend()
	p := newPoolFromBackend(backend, false)
	conn, err := p.DialContext(context.Background(), "tcp", "example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	_, released, _ := backend.counts()
	if released != 1 {
		t.Fatalf("released = %d, want 1", released)
	}
}

func TestDialUsesLeaseFromContext(t *testing.T) {
	backend := newFakeBackend()
	p := newPoolFromBackend(backend, false)
	ctx, lease := p.Acquire(context.Background())
	defer lease.Release()
	conn, err := p.DialContext(ctx, "tcp", "example.com:443")
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
	if _, released, _ := backend.counts(); released != 0 {
		t.Fatal("context lease was released by DialContext")
	}
	lease.Release()
	lease.Release()
	if _, released, _ := backend.counts(); released != 1 {
		t.Fatalf("released = %d after two Release calls, want 1", released)
	}
}

func TestRotateAsyncWaitsForFreshExit(t *testing.T) {
	backend := newFakeBackend()
	backend.delay = 80 * time.Millisecond
	p := newPoolFromBackend(backend, false)
	_, lease := p.Acquire(context.Background())
	defer lease.Release()
	started := time.Now()
	done := lease.RotateAsync(context.Background(), "free usage limit", nil)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("rotation did not finish")
	}
	if time.Since(started) < 50*time.Millisecond {
		t.Fatal("rotation returned before a fresh exit was ready")
	}
	if _, _, failed := backend.counts(); failed != 1 {
		t.Fatalf("fail calls = %d, want 1", failed)
	}
}

func TestRotateAsyncReturnsWhenReplacementFails(t *testing.T) {
	backend := newFakeBackend()
	backend.failNoop = true
	p := newPoolFromBackend(backend, false)
	// A long wait proves the call returns on the replacement signal instead
	// of sleeping out the full rotate window.
	p.rotateWait = 10 * time.Second
	_, lease := p.Acquire(context.Background())
	defer lease.Release()
	started := time.Now()
	done := lease.RotateAsync(context.Background(), "free usage limit", nil)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("rotation did not return after a failed replacement")
	}
	if elapsed := time.Since(started); elapsed > time.Second {
		t.Fatalf("rotation waited %s for a failed replacement", elapsed)
	}
}

func TestNilPoolFailsClosed(t *testing.T) {
	var p *Pool
	if _, err := p.DialContext(context.Background(), "tcp", "example.com:443"); !errors.Is(err, ErrNoEndpoint) {
		t.Fatalf("nil pool dial = %v, want ErrNoEndpoint", err)
	}
	if p.Snapshot() != nil || p.Len() != 0 {
		t.Fatal("nil pool reported exits")
	}
	ctx, lease := p.Acquire(context.Background())
	if ctx == nil || lease != nil {
		t.Fatal("nil pool Acquire changed the context or returned a lease")
	}
}

func TestFallbackDirectDialsHost(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	p := newPoolFromBackend(nil, true)
	conn, err := p.DialContext(context.Background(), "tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	_ = conn.Close()
}

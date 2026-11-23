// Package egress routes outbound connections through a pool of in-process
// Cloudflare WARP exits and rotates the exit identity when an upstream reports
// a free-tier limit.
//
// Each exit is an independent WARP device with its own public egress. The free
// tier counts quota per IPv6 /64, so a replacement exit must leave from a new
// outer source (a routed /64) to be a real rotation rather than a cosmetic
// address change.
//
// A nil backend never dials: every method fails closed. Direct connections are
// a separate, explicit path (see Options.FallbackDirect), so a missing
// configuration can never silently leak the host's own address.
package egress

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"freegate/internal/warp"
)

// ErrNoEndpoint is returned when no usable WARP exit is available and direct
// fallback is disabled.
var ErrNoEndpoint = errors.New("egress: no usable WARP endpoint")

// Options configures a Pool.
type Options struct {
	// Size is the upper bound on exits kept by the WARP pool.
	Size int
	// Stack selects the address family inside the tunnel.
	Stack warp.Stack
	// SourceIPv6 is the routed prefix each exit draws its outer source from.
	SourceIPv6 netip.Prefix
	// FallbackDirect dials without WARP when the pool is empty. It is opt-in:
	// the zero value fails closed instead of leaking the host IP.
	FallbackDirect bool
	// RegistrationTimeout bounds the synchronous first WARP registration.
	RegistrationTimeout time.Duration
	// RotateWait bounds how long one retry waits for a fresh exit.
	RotateWait time.Duration
}

func (o *Options) withDefaults() {
	if o.Size <= 0 {
		o.Size = warp.DefaultPoolSize
	}
	if o.Stack == "" {
		o.Stack = warp.DefaultStack
	}
	if o.RegistrationTimeout <= 0 {
		o.RegistrationTimeout = warp.RegistrationWindow
	}
	if o.RotateWait <= 0 {
		o.RotateWait = 30 * time.Second
	}
}

// Snapshot is the observable state of one WARP exit.
type Snapshot struct {
	ID        string `json:"id"`
	Egress    string `json:"egress,omitempty"`
	Prefix    string `json:"prefix,omitempty"`
	Stack     string `json:"stack"`
	Status    string `json:"status"`
	Idle      bool   `json:"idle"`
	Penalized bool   `json:"penalized,omitempty"`
}

type leaseKey struct{}

// backend is the narrow surface the adapter needs from a WARP pool. Keeping it
// an interface lets tests exercise dial/rotation paths without a real device.
type backend interface {
	Acquire(context.Context) (*Lease, error)
	Snapshot() []Snapshot
	Close()
}

// Lease attributes one request to the exit that served its dial. It is also
// the handle used to retire that exit after a free-tier limit.
type Lease struct {
	once      sync.Once
	owner     *Pool
	dial      func(context.Context, string, string) (net.Conn, error)
	release   func()
	fail      func(string)
	waitFresh func(context.Context, []Snapshot)
}

// DialContext dials through this lease's exit. A released lease fails closed,
// so a connection is never silently redirected to another exit.
func (l *Lease) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if l == nil || l.dial == nil {
		return nil, ErrNoEndpoint
	}
	return l.dial(ctx, network, addr)
}

// LeaseFromContext returns the lease recorded by Pool.Acquire, if any.
func LeaseFromContext(ctx context.Context) *Lease {
	if ctx == nil {
		return nil
	}
	l, _ := ctx.Value(leaseKey{}).(*Lease)
	return l
}

// Release returns the lease to its pool. Safe to call more than once.
func (l *Lease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		if l.release != nil {
			l.release()
		}
	})
}

// RotateAsync retires the exit that carried the request and waits for a
// replacement, so the caller's retry leaves from a new identity.
func (l *Lease) RotateAsync(ctx context.Context, reason string, closeIdle func()) <-chan struct{} {
	done := make(chan struct{})
	if l == nil || l.fail == nil {
		close(done)
		return done
	}
	var before []Snapshot
	if l.owner != nil {
		before = l.owner.Snapshot()
	}
	l.fail(reason)
	if closeIdle != nil {
		closeIdle()
	}
	if l.waitFresh == nil {
		close(done)
		return done
	}
	go func() {
		defer close(done)
		l.waitFresh(ctx, before)
	}()
	return done
}

// Pool is a set of WARP exits with round-robin selection and in-place
// replacement. It is safe for concurrent use and fails closed when empty.
type Pool struct {
	backend        backend
	fallbackDirect bool
	dialer         *net.Dialer
	rotateWait     time.Duration
}

// NewPool builds a pool backed by a real in-process WARP pool. A registration
// failure is a hard error: WARP was explicitly requested and dialing directly
// would defeat the purpose.
func NewPool(ctx context.Context, log *slog.Logger, opts Options) (*Pool, error) {
	opts.withDefaults()
	ctx, cancel := context.WithTimeout(ctx, opts.RegistrationTimeout)
	defer cancel()
	wp, err := warp.NewWithOptions(ctx, log, warp.Options{
		Size:       opts.Size,
		Stack:      opts.Stack,
		SourceIPv6: opts.SourceIPv6,
	})
	if err != nil {
		return nil, err
	}
	p := newPoolFromBackend(&warpBackend{p: wp}, opts.FallbackDirect)
	p.rotateWait = opts.RotateWait
	slog.Info("egress: WARP pool enabled", "size", opts.Size, "stack", opts.Stack,
		"source", opts.SourceIPv6.String(), "fallback_direct", opts.FallbackDirect)
	return p, nil
}

func newPoolFromBackend(b backend, fallbackDirect bool) *Pool {
	return &Pool{
		backend:        b,
		fallbackDirect: fallbackDirect,
		dialer:         &net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second},
		rotateWait:     30 * time.Second,
	}
}

// Acquire returns a context carrying a fresh lease for this pool.
func (p *Pool) Acquire(ctx context.Context) (context.Context, *Lease) {
	if p == nil || p.backend == nil {
		return ctx, nil
	}
	l, err := p.backend.Acquire(ctx)
	if err != nil || l == nil {
		return ctx, nil
	}
	l.owner = p
	if l.waitFresh == nil {
		l.waitFresh = p.waitFresh
	}
	return context.WithValue(ctx, leaseKey{}, l), l
}

// DialContext dials addr through the lease carried by ctx. When the context
// has no lease (background refreshes, health probes), a short-lived lease is
// acquired and released when the connection closes. A nil pool fails closed.
func (p *Pool) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if p == nil || p.backend == nil {
		if p != nil && p.fallbackDirect {
			return p.dialer.DialContext(ctx, network, addr)
		}
		return nil, ErrNoEndpoint
	}
	if l := LeaseFromContext(ctx); l != nil && l.owner == p && l.dial != nil {
		return l.dial(ctx, network, addr)
	}
	l, err := p.backend.Acquire(ctx)
	if err != nil || l == nil {
		if p.fallbackDirect {
			return p.dialer.DialContext(ctx, network, addr)
		}
		if err != nil {
			return nil, fmt.Errorf("%w: %v", ErrNoEndpoint, err)
		}
		return nil, ErrNoEndpoint
	}
	conn, err := l.dial(ctx, network, addr)
	if err != nil {
		l.Release()
		return nil, err
	}
	return &leaseConn{Conn: conn, lease: l}, nil
}

// waitFresh waits until a new exit appears, so a retry after a free-tier limit
// does not leave from the same quota scope.
func (p *Pool) waitFresh(ctx context.Context, before []Snapshot) {
	if p == nil || p.backend == nil {
		return
	}
	deadline := time.Now().Add(p.rotateWait)
	for time.Now().Before(deadline) {
		select {
		case <-ctx.Done():
			return
		default:
		}
		for _, now := range p.backend.Snapshot() {
			seen := false
			for _, old := range before {
				if old.ID == now.ID && old.Prefix == now.Prefix {
					seen = true
					break
				}
			}
			if !seen {
				return
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Close retires every exit in the pool.
func (p *Pool) Close() {
	if p == nil || p.backend == nil {
		return
	}
	p.backend.Close()
}

// Snapshot returns the current state of every exit.
func (p *Pool) Snapshot() []Snapshot {
	if p == nil || p.backend == nil {
		return nil
	}
	return p.backend.Snapshot()
}

// Len reports how many exits are configured.
func (p *Pool) Len() int {
	if p == nil || p.backend == nil {
		return 0
	}
	return len(p.Snapshot())
}

// leaseConn ties a short-lived lease to the connection it dialed.
type leaseConn struct {
	net.Conn
	lease *Lease
	once  sync.Once
}

func (c *leaseConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.lease.Release)
	return err
}

// warpBackend adapts warp.Pool to the local backend interface and keeps warp's
// lease type inside the package that owns it.
type warpBackend struct {
	p *warp.Pool
}

func (b *warpBackend) Acquire(ctx context.Context) (*Lease, error) {
	l, err := b.p.Acquire(ctx)
	if err != nil {
		return nil, err
	}
	return &Lease{
		dial:    l.DialContext,
		release: l.Release,
		fail:    func(reason string) { b.p.Fail(l, reason) },
	}, nil
}

func (b *warpBackend) Snapshot() []Snapshot {
	snap := b.p.Snapshot()
	out := make([]Snapshot, 0, len(snap.Exits))
	for _, e := range snap.Exits {
		out = append(out, Snapshot{
			ID:        e.ID,
			Egress:    e.Egress,
			Prefix:    e.Prefix,
			Stack:     e.Stack,
			Status:    e.Status,
			Idle:      e.Idle,
			Penalized: e.Penalized,
		})
	}
	return out
}

func (b *warpBackend) Close() { b.p.Close() }

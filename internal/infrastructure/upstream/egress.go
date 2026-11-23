package upstream

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/http"

	"freegate/internal/domain"
	"freegate/internal/infrastructure/egress"
)

// ErrNoWarpPool is returned when WARP is selected but no pool is configured
// for that route. It is a hard failure: dialing directly would leak the host's
// own address, which is exactly what WARP is meant to prevent.
var ErrNoWarpPool = errors.New("egress: WARP pool not configured")

// poolDialer is the runtime surface EgressRoute needs. egress.Pool implements
// it; tests supply a fake so no real sidecar is required.
type poolDialer interface {
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
	Acquire(ctx context.Context) (context.Context, *egress.Lease)
}

// EgressRoute resolves one upstream's WARP pool at dial time. It is a struct,
// not an interface, so there is no typed-nil trap: a zero value or a nil
// Router always fails closed.
type EgressRoute struct {
	// Router is the live pool registry. Nil is safe and fails closed.
	Router *egress.Router
	// PoolID pins the route to one persisted pool. Zero selects the
	// env-configured default pool.
	PoolID uint
	// testDialer overrides Router.Pool for tests. Nil in production.
	testDialer poolDialer
}

// dialer resolves the pool, normalizing a nil *egress.Pool to a nil interface
// so callers never see a typed nil.
func (r EgressRoute) dialer() poolDialer {
	if r.testDialer != nil {
		return r.testDialer
	}
	if r.Router == nil {
		return nil
	}
	p := r.Router.Pool(r.PoolID)
	if p == nil {
		return nil
	}
	return p
}

// Acquire returns a context carrying a lease for this route's pool, so a
// free-tier limit rotates the endpoint that served the dial.
func (r EgressRoute) Acquire(ctx context.Context) (context.Context, *egress.Lease) {
	d := r.dialer()
	if d == nil {
		return ctx, nil
	}
	return d.Acquire(ctx)
}

// DialContext dials addr through the route's pool, failing closed when the
// pool is missing.
func (r EgressRoute) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d := r.dialer()
	if d == nil {
		return nil, ErrNoWarpPool
	}
	return d.DialContext(ctx, network, addr)
}

// NewWarpTransport clones base and overrides DialContext to route outbound
// connections through the route's WARP pool.
func NewWarpTransport(route EgressRoute, base *http.Transport) *http.Transport {
	if base == nil {
		base = NewTransport()
	}
	tr := base.Clone()
	tr.DialContext = route.DialContext
	// A userspace exit is per-attempt: keep-alives would leave a pooled
	// connection bound to an exit after it is retired, and h2 over a fresh
	// netstack connection is not what the upstream path was verified with.
	tr.DisableKeepAlives = true
	tr.ForceAttemptHTTP2 = false
	tr.TLSNextProto = make(map[string]func(string, *tls.Conn) http.RoundTripper)
	if tr.TLSClientConfig == nil {
		tr.TLSClientConfig = &tls.Config{NextProtos: []string{"http/1.1"}}
	} else {
		cfg := tr.TLSClientConfig.Clone()
		cfg.NextProtos = []string{"http/1.1"}
		tr.TLSClientConfig = cfg
	}
	return tr
}

// isFreeUsageLimit probes resp for a free-tier usage limit and restores its
// body. domain.IsFreeUsageLimit rewinds the UpstreamResponse it is given, so
// the restored body must be copied back onto the http.Response the caller
// still holds.
func isFreeUsageLimit(resp *http.Response) bool {
	if resp == nil {
		return false
	}
	ur := domain.NewUpstreamResponse(resp)
	ok := domain.IsFreeUsageLimit(ur)
	resp.Body = ur.Body
	return ok
}

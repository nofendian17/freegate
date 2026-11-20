package upstream

import (
	"net/http"
	"sync"

	"freegate/internal/infrastructure/egress"
)

// transportPair owns the direct and WARP transports for one upstream. WARP
// transports are built lazily per pool ID and cached; the router resolves the
// pool at dial time, so editing a pool does not require a new transport.
type transportPair struct {
	mu     sync.Mutex
	router *egress.Router
	base   *http.Transport

	warp    *http.Transport
	warpID  uint
	warpSet bool
}

func newTransportPair(base *http.Transport, router *egress.Router) *transportPair {
	if base == nil {
		base = NewTransport()
	}
	return &transportPair{router: router, base: base}
}

// Direct returns the non-WARP transport.
func (t *transportPair) Direct() *http.Transport { return t.base }

// Warp returns the transport for the given WARP pool ID. A nil builder uses
// the standard dial path; OpenCode passes a builder for its Bun-shaped TLS.
func (t *transportPair) Warp(id uint, build func(EgressRoute, *http.Transport) *http.Transport) *http.Transport {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.warpSet && t.warpID == id {
		return t.warp
	}
	route := EgressRoute{Router: t.router, PoolID: id}
	tr := NewWarpTransport(route, t.base)
	if build != nil {
		tr = build(route, t.base)
	}
	t.warp, t.warpID, t.warpSet = tr, id, true
	return tr
}

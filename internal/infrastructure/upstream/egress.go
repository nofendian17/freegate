package upstream

import (
	"context"
	"errors"
	"net"
	"net/http"

	"freegate/internal/domain"
)

var ErrNoWarpPool = errors.New("egress: WARP pool not configured")

// Egress is the outbound path for upstream connections that must not use the
// host's own IP. Implemented by egress.Pool; nil means a direct connection.
type Egress interface {
	// DialContext opens a connection to addr through the current exit.
	DialContext(ctx context.Context, network, addr string) (net.Conn, error)
	// RotateAsync asks the exit that served the last dial to change identity.
	// It must not block the caller.
	RotateAsync(reason string)
}

// NewWarpTransport clones base and overrides DialContext to route outbound
// connections through the WARP egress pool. When eg is nil, attempts to dial
// fail closed with ErrNoWarpPool instead of leaking the host's own IP.
func NewWarpTransport(eg Egress, base *http.Transport) *http.Transport {
	if base == nil {
		base = NewTransport()
	}
	tr := base.Clone()
	tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
		if eg == nil {
			return nil, ErrNoWarpPool
		}
		return eg.DialContext(ctx, network, addr)
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

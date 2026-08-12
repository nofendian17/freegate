package upstream

import (
	"context"
	"log/slog"
	"net"
	"sync"
	"time"

	"golang.org/x/net/proxy"
)

// Dialer routes upstream HTTP connections either directly (no tunnel) or
// through the VPNGate SOCKS5 tunnel, switchable at runtime from the
// dashboard (replaces the old static BYPASS_PROXY env flag). Both upstreams
// share one Dialer so a single toggle flips the whole proxy.
type Dialer struct {
	mu        sync.RWMutex
	socks     proxy.Dialer
	direct    bool
	socksAddr string
}

// NewDialer builds a Dialer that uses the given SOCKS5 tunnel address
// unless switched to direct. An empty socksAddr yields a direct-only
// dialer (still switchable, but with no tunnel to fall back to).
func NewDialer(socksAddr string) *Dialer {
	d := &Dialer{socksAddr: socksAddr}
	if socksAddr != "" {
		sd, err := proxy.SOCKS5("tcp", socksAddr, nil, proxy.Direct)
		if err != nil {
			slog.Warn("SOCKS5 dialer failed, using direct connection", "error", err)
		} else {
			d.socks = sd
		}
	}
	return d
}

// SetDirect flips the routing mode. direct=true sends all upstream traffic
// straight from the proxy container (no tunnel); direct=false routes it
// through the VPN SOCKS5 tunnel.
func (d *Dialer) SetDirect(direct bool) {
	d.mu.Lock()
	d.direct = direct
	d.mu.Unlock()
	slog.Info("upstream dialer mode changed", "direct", direct)
}

// IsDirect reports whether the dialer currently routes directly.
func (d *Dialer) IsDirect() bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.direct
}

// DialContext dials through the configured route: the SOCKS5 tunnel unless
// direct mode is active (or no tunnel dialer is available).
func (d *Dialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	d.mu.RLock()
	socks := d.socks
	direct := d.direct
	d.mu.RUnlock()

	if direct || socks == nil {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
	}
	if dc, ok := socks.(proxy.ContextDialer); ok {
		return dc.DialContext(ctx, network, addr)
	}
	return socks.Dial(network, addr)
}

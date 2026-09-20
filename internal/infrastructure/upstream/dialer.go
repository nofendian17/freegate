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
	onFlush   func() // called after VPN reconnect to flush stale conns
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

// SetOnFlush registers a callback invoked after VPN reconnects so the
// shared http.Transport can drop stale pooled HTTP/2 connections that
// the dead tunnel left behind.
func (d *Dialer) SetOnFlush(fn func()) {
	d.mu.Lock()
	d.onFlush = fn
	d.mu.Unlock()
}

// Flush calls the registered callback. VPN supervisor calls this after
// a successful reconnect.
func (d *Dialer) Flush() {
	d.mu.RLock()
	fn := d.onFlush
	d.mu.RUnlock()
	if fn != nil {
		fn()
		slog.Info("upstream dialer flushed stale connections")
	}
}

// SetDirect flips the routing mode. direct=true sends all upstream traffic
// straight from the proxy container (no tunnel); direct=false routes it
// through the VPN SOCKS5 tunnel. Flushes stale pooled connections on toggle.
func (d *Dialer) SetDirect(direct bool) {
	d.mu.Lock()
	d.direct = direct
	onFlush := d.onFlush
	d.mu.Unlock()
	slog.Info("upstream dialer mode changed", "direct", direct)
	if onFlush != nil {
		onFlush()
	}
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

	if _, ok := ctx.Deadline(); !ok {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
	}
	if direct || socks == nil {
		return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, network, addr)
	}
	if dc, ok := socks.(proxy.ContextDialer); ok {
		return dc.DialContext(ctx, network, addr)
	}
	// The SOCKS5 dialer does not implement ContextDialer; run the
	// context-unaware Dial in a goroutine so we can still honour
	// context cancellation from the caller.
	type connErr struct {
		conn net.Conn
		err  error
	}
	ch := make(chan connErr, 1)
	go func() {
		conn, err := socks.Dial(network, addr)
		ch <- connErr{conn, err}
	}()
	select {
	case <-ctx.Done():
		// Drain the goroutine: if Dial succeeded after cancellation, close
		// the connection so it is not leaked. Bounded so a hung
		// context-unaware Dial cannot pin this goroutine forever — after
		// the grace period both the dial and the drain are abandoned and
		// reaped when Dial eventually returns (buffered ch, no block).
		go func() {
			timer := time.NewTimer(30 * time.Second)
			defer timer.Stop()
			select {
			case ce := <-ch:
				if ce.conn != nil {
					ce.conn.Close()
				}
			case <-timer.C:
			}
		}()
		return nil, ctx.Err()
	case ce := <-ch:
		return ce.conn, ce.err
	}
}

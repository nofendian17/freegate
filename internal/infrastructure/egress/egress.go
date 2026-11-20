// Package egress routes outbound connections through a pool of WARP proxies
// and rotates their exit identity when an upstream reports a free-tier limit.
//
// Each endpoint is one WARP sidecar (the official Cloudflare client behind a
// SOCKS5 listener) plus an optional control API that rotates its identity in
// place. OpenCode's free tier is counted per IPv6 /64, so rotating the client
// keys hands the next request a fresh quota without re-registering the device.
//
// A nil *Pool never dials: every method fails closed. Direct connections are a
// separate, explicit path (see Options.FallbackDirect), so a missing
// configuration can never silently leak the host's own address.
package egress

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ErrNoEndpoint is returned when every endpoint is rotating or cooling and
// direct fallback is disabled.
var ErrNoEndpoint = errors.New("egress: no usable WARP endpoint")

// Endpoint describes one WARP sidecar.
type Endpoint struct {
	// Name labels the endpoint in logs and snapshots (e.g. "warp-1").
	Name string
	// Socks is the host:port of the SOCKS5 listener.
	Socks string
	// Control is the base URL of the rotation API (optional).
	Control string
}

// Options configures a Pool. Zero values get defaults.
type Options struct {
	Endpoints []Endpoint
	// FallbackDirect dials without WARP when no endpoint is usable. It is
	// opt-in: the zero value fails closed instead of leaking the host IP.
	FallbackDirect bool
	// DialTimeout bounds one SOCKS5 dial (TCP connect + handshake).
	DialTimeout time.Duration
	// RotateTimeout bounds one control-API rotation.
	RotateTimeout time.Duration
	// DrainTimeout bounds how long a rotation waits for in-flight traffic on
	// the endpoint to finish. WARP tears down the whole tunnel on rotation, so
	// rotating while a request streams would cut that request off.
	DrainTimeout time.Duration
	// RotateAttempts bounds how many identities one rotation tries when the
	// new address lands in a /64 another endpoint already uses.
	RotateAttempts int
	// FailureCool is how long an endpoint is skipped after a failed dial or
	// a failed rotation.
	FailureCool time.Duration
}

func (o *Options) withDefaults() {
	if o.DialTimeout <= 0 {
		o.DialTimeout = 15 * time.Second
	}
	if o.RotateTimeout <= 0 {
		o.RotateTimeout = 60 * time.Second
	}
	if o.DrainTimeout <= 0 {
		o.DrainTimeout = 60 * time.Second
	}
	if o.RotateAttempts <= 0 {
		o.RotateAttempts = 3
	}
	if o.FailureCool <= 0 {
		o.FailureCool = time.Minute
	}
}

// Status is the lifecycle state of one endpoint.
type Status string

const (
	StatusReady    Status = "ready"
	StatusRotating Status = "rotating"
	StatusCooling  Status = "cooling"
)

// Snapshot is a point-in-time view of one endpoint, for logs and the UI.
type Snapshot struct {
	Name    string `json:"name"`
	Socks   string `json:"socks"`
	Control string `json:"control,omitempty"`
	Status  Status `json:"status"`
	IP      string `json:"ip,omitempty"`
	Prefix  string `json:"prefix,omitempty"`
	// DuplicatePrefix is true when another endpoint in the pool exits from
	// the same IPv6 /64. OpenCode's free tier is counted per /64, so two
	// endpoints sharing one prefix share a single quota.
	DuplicatePrefix bool      `json:"duplicate_prefix,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	RotatedAt       time.Time `json:"rotated_at,omitempty"`
	CoolUntil       time.Time `json:"cool_until,omitempty"`
	Rotations       int       `json:"rotations"`
}

type endpoint struct {
	Endpoint
	mu        sync.Mutex
	status    Status
	ip        string
	lastErr   string
	coolUntil time.Time
	rotatedAt time.Time
	rotations int
	// probedIP is the current exit address learned from the control API's
	// GET /ip. Unlike ip (set only by a rotation), it is filled in for every
	// endpoint so prefix collisions can be detected before they cost quota.
	probedIP string
	// active counts connections currently open through this endpoint. A
	// rotation drains this to zero before asking WARP to tear down the tunnel.
	active atomic.Int64
	// ready closes when an in-flight rotation finishes, so a dial can wait
	// for the identity change instead of polling a timeout.
	ready chan struct{}
}

func (e *endpoint) usable(now time.Time) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.status == StatusRotating {
		return false
	}
	if e.status == StatusCooling {
		if now.Before(e.coolUntil) {
			return false
		}
		// Cooldown expired: allow a probe dial and stop reporting "cooling".
		e.status = StatusReady
	}
	return true
}

func (e *endpoint) markReady(ip string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.status = StatusReady
	e.ip = ip
	// The rotation response is the freshest probe we have; without this the
	// next snapshot would keep reporting the pre-rotation address.
	e.probedIP = ip
	e.lastErr = ""
	e.coolUntil = time.Time{}
	e.rotatedAt = time.Now()
	e.rotations++
	e.finishRotateLocked()
}

func (e *endpoint) markCooling(err error, d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.status = StatusCooling
	e.lastErr = errString(err)
	e.coolUntil = time.Now().Add(d)
	e.finishRotateLocked()
}

// finishRotateLocked releases anyone waiting on the current rotation. Caller
// holds e.mu.
func (e *endpoint) finishRotateLocked() {
	if e.ready != nil {
		close(e.ready)
		e.ready = nil
	}
}

// beginRotate flips the endpoint to rotating, refusing a second concurrent
// rotation of the same container.
func (e *endpoint) beginRotate() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.status == StatusRotating {
		return false
	}
	e.status = StatusRotating
	e.ready = make(chan struct{})
	return true
}

// readyCh returns the channel to wait on while rotating, or nil.
func (e *endpoint) readyCh() <-chan struct{} {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.ready
}

func (e *endpoint) snapshot() Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	return Snapshot{
		Name:      e.Name,
		Socks:     e.Socks,
		Control:   e.Control,
		Status:    e.status,
		IP:        e.currentIPLocked(),
		Prefix:    prefix64(e.currentIPLocked()),
		LastError: e.lastErr,
		RotatedAt: e.rotatedAt,
		CoolUntil: e.coolUntil,
		Rotations: e.rotations,
	}
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// prefix64 returns the canonical IPv6 /64 an address belongs to. An empty
// string means the address is unknown or not IPv6 and cannot be compared.
func prefix64(ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil || !addr.Is6() {
		return ""
	}
	return netip.PrefixFrom(addr, 64).Masked().String()
}

// currentIP reports the freshest known exit address for an endpoint: the
// control-API probe when available, otherwise the last rotation result.
func (e *endpoint) currentIP() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.currentIPLocked()
}

// currentIPLocked is currentIP for callers that already hold e.mu.
func (e *endpoint) currentIPLocked() string {
	if e.probedIP != "" {
		return e.probedIP
	}
	return e.ip
}

// probeIP asks the control API for the endpoint's current exit address.
func (e *endpoint) probeIP(ctx context.Context, client *http.Client) {
	if strings.TrimSpace(e.Control) == "" {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, e.Control+"/ip", http.NoBody)
	if err != nil {
		return
	}
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return
	}
	ip := decodeRotation(resp.Body)
	if ip == "" {
		return
	}
	e.mu.Lock()
	e.probedIP = ip
	e.mu.Unlock()
}

// probeIPs refreshes the known exit address of every endpoint. It is
// best-effort: a sidecar that cannot answer keeps its last known address.
func (p *Pool) probeIPs(ctx context.Context) {
	if p == nil {
		return
	}
	client := &http.Client{Timeout: 10 * time.Second}
	for _, ep := range p.eps {
		ep.probeIP(ctx, client)
	}
}

// prefixInUse reports whether ip lands in a /64 already used by another
// endpoint in the pool. OpenCode's free tier is per /64, so two exits sharing
// a prefix share one quota and rotating into it buys nothing.
func (p *Pool) prefixInUse(ep *endpoint, ip string) bool {
	prefix := prefix64(ip)
	if prefix == "" {
		return false
	}
	for _, other := range p.eps {
		if other == ep {
			continue
		}
		if prefix64(other.currentIP()) == prefix {
			return true
		}
	}
	return false
}

// trackedConn counts the connections dialed through an endpoint so a rotation
// can wait for in-flight traffic before WARP drops the tunnel. Closing is
// idempotent: net/http and a caller may both close the same connection.
type trackedConn struct {
	net.Conn
	ep   *endpoint
	once sync.Once
}

func (c *trackedConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(func() { c.ep.active.Add(-1) })
	return err
}

// Pool is a set of WARP endpoints with round-robin selection and in-place
// rotation.
type Pool struct {
	opts   Options
	dialer *net.Dialer
	client *http.Client

	eps  []*endpoint
	next atomic.Uint64
	// last is the endpoint of the most recent successful dial. It is the
	// fallback attribution for a request that reused a pooled connection and
	// therefore never dialed itself.
	last atomic.Pointer[endpoint]
}

// NewPool builds a pool. It returns nil when no endpoints are configured so
// callers can treat "no egress" as a plain nil check.
func NewPool(opts Options) *Pool {
	opts.withDefaults()
	if len(opts.Endpoints) == 0 {
		return nil
	}
	p := &Pool{
		opts:   opts,
		dialer: &net.Dialer{Timeout: opts.DialTimeout, KeepAlive: 30 * time.Second},
		client: &http.Client{Timeout: opts.RotateTimeout},
	}
	for _, ep := range opts.Endpoints {
		p.eps = append(p.eps, &endpoint{Endpoint: ep, status: StatusReady})
	}
	return p
}

// ParsePool reads the EGRESS_WARP_POOL form:
//
//	warp-1:1080@http://warp-1:8081,warp-2:1080@http://warp-2:8081
//
// The control part is optional. A bare host:port gets the name of its host.
func ParsePool(raw string) ([]Endpoint, error) {
	var out []Endpoint
	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		socks, control, _ := strings.Cut(item, "@")
		socks = strings.TrimSpace(socks)
		host, port, err := net.SplitHostPort(socks)
		if err != nil || host == "" {
			return nil, fmt.Errorf("egress: invalid endpoint %q (want host:port[@control-url])", item)
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("egress: invalid port in %q", item)
		}
		ep := Endpoint{Name: host, Socks: socks, Control: strings.TrimSpace(control)}
		if ep.Control != "" {
			u, err := url.Parse(ep.Control)
			if err != nil || u.Scheme == "" || u.Host == "" {
				return nil, fmt.Errorf("egress: invalid control URL %q", ep.Control)
			}
			ep.Control = strings.TrimRight(ep.Control, "/")
		}
		out = append(out, ep)
	}
	return out, nil
}

// candidates returns usable endpoints starting at the round-robin cursor.
func (p *Pool) candidates(now time.Time) []*endpoint {
	if p == nil || len(p.eps) == 0 {
		return nil
	}
	n := len(p.eps)
	start := int(p.next.Add(1)-1) % n
	out := make([]*endpoint, 0, n)
	for i := 0; i < n; i++ {
		ep := p.eps[(start+i)%n]
		if ep.usable(now) {
			out = append(out, ep)
		}
	}
	return out
}

// DialContext dials addr through the first usable WARP endpoint, failing over
// across the pool. When every endpoint is rotating it waits for one to come
// back; when nothing is usable it falls back to a direct dial only if
// Options.FallbackDirect is set. A nil pool fails closed.
func (p *Pool) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if p == nil {
		return nil, ErrNoEndpoint
	}
	// A rotation drains in-flight traffic (DrainTimeout) and then calls the
	// control API (RotateTimeout); one dial may still need its own timeout.
	// Bound the wait by the whole lifecycle, not just the control call.
	waitUntil := time.Now().Add(p.opts.DrainTimeout + p.opts.RotateTimeout + p.opts.DialTimeout)
	var lastErr error
	for {
		tried := 0
		for _, ep := range p.candidates(time.Now()) {
			tried++
			conn, err := dialSOCKS5(ctx, p.dialer, ep.Socks, addr)
			if err == nil {
				p.last.Store(ep)
				if l := leaseFrom(ctx); l != nil && l.pool == p {
					l.ep.Store(ep)
				}
				ep.active.Add(1)
				return &trackedConn{Conn: conn, ep: ep}, nil
			}
			if ctx.Err() != nil {
				// The caller gave up; the endpoint is not at fault.
				return nil, ctx.Err()
			}
			lastErr = err
			ep.markCooling(err, p.opts.FailureCool)
			slog.Warn("egress: endpoint dial failed", "endpoint", ep.Name, "socks", ep.Socks, "target", addr, "error", err)
		}
		if tried > 0 {
			// Endpoints were available but all refused: cooling them already
			// happened, so waiting would not help.
			break
		}
		if !p.anyRotating() || time.Now().After(waitUntil) {
			break
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
	if p.opts.FallbackDirect {
		// This request leaves from the host's own address. Loud on purpose:
		// an operator who wanted WARP needs to know the pool is exhausted.
		slog.Warn("egress: pool exhausted, falling back to a direct connection", "target", addr)
		return p.dialer.DialContext(ctx, network, addr)
	}
	if lastErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrNoEndpoint, lastErr)
	}
	return nil, ErrNoEndpoint
}

// anyRotating reports whether at least one endpoint is changing identity, so
// a dial can wait instead of failing closed.
func (p *Pool) anyRotating() bool {
	for _, ep := range p.eps {
		if ep.readyCh() != nil {
			return true
		}
	}
	return false
}

// leaseKey carries a *Lease in a request context so the dial that serves the
// request can record which endpoint it used.
type leaseKey struct{}

// Lease attributes one request to the endpoint that served its dial, so a
// free-tier limit rotates exactly that exit. The zero/nil lease is a no-op.
type Lease struct {
	pool *Pool
	ep   atomic.Pointer[endpoint]
}

// Acquire returns a context carrying a fresh lease for this pool.
func (p *Pool) Acquire(ctx context.Context) (context.Context, *Lease) {
	if p == nil {
		return ctx, nil
	}
	l := &Lease{pool: p}
	return context.WithValue(ctx, leaseKey{}, l), l
}

func leaseFrom(ctx context.Context) *Lease {
	if ctx == nil {
		return nil
	}
	l, _ := ctx.Value(leaseKey{}).(*Lease)
	return l
}

// RotateAsync rotates the endpoint attributed to this lease. It returns a
// channel closed when the attempt finishes, so the caller can retry after the
// new identity is ready instead of polling a timeout.
//
// WARP drops every connection through the tunnel when it rotates, so the
// rotation first drains in-flight traffic on that endpoint. closeIdle is
// called while waiting so the transport releases connections that finished
// while the drain was in progress; pass nil when no transport is involved.
//
// A request that reused a pooled connection never dialed, so its lease has no
// endpoint; it falls back to the pool's most recent dial. When neither is
// available (nothing dialed yet, or another rotation already claimed it) the
// call is a no-op on purpose: a burst of 429s for one exit must not cascade
// into rotating every other exit.
func (l *Lease) RotateAsync(reason string, closeIdle func()) <-chan struct{} {
	done := make(chan struct{})
	if l == nil || l.pool == nil {
		close(done)
		return done
	}
	ep := l.ep.Swap(nil)
	if ep == nil {
		ep = l.pool.last.Swap(nil)
	}
	if ep == nil {
		slog.Debug("egress: rotation skipped, no endpoint attributed", "reason", reason)
		close(done)
		return done
	}
	go func() {
		defer close(done)
		l.pool.rotate(ep, reason, closeIdle)
	}()
	return done
}

// drain waits until no connections are open through ep. New dials are already
// blocked by beginRotate; closeIdle releases pooled connections that finished
// while we wait, so a completed request does not hold the drain for the whole
// idle timeout.
func (p *Pool) drain(ep *endpoint, closeIdle func()) bool {
	deadline := time.Now().Add(p.opts.DrainTimeout)
	for {
		if ep.active.Load() == 0 {
			return true
		}
		if closeIdle != nil {
			closeIdle()
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func (p *Pool) rotate(ep *endpoint, reason string, closeIdle func()) {
	if !ep.beginRotate() {
		// Another request already rotates this exit; wait for it so the
		// caller's retry sees the new identity.
		if ch := ep.readyCh(); ch != nil {
			<-ch
		}
		return
	}
	// A rotation tears down the WARP tunnel and everything flowing through
	// it. Never do that while a request is still streaming: mark the endpoint
	// rotating (done above, so new dials move on) and wait for the active
	// connections to close first.
	if !p.drain(ep, closeIdle) {
		slog.Warn("egress: rotation skipped, endpoint still serving requests",
			"endpoint", ep.Name, "reason", reason, "active", ep.active.Load())
		ep.mu.Lock()
		ep.status = StatusReady
		ep.finishRotateLocked()
		ep.mu.Unlock()
		return
	}
	if strings.TrimSpace(ep.Control) == "" {
		// The endpoint still serves traffic, it just cannot change identity.
		// Keep it usable and tell the operator once per attempt.
		ep.mu.Lock()
		ep.status = StatusReady
		ep.finishRotateLocked()
		ep.mu.Unlock()
		slog.Warn("egress: rotation requested but no control endpoint is configured", "endpoint", ep.Name)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.opts.RotateTimeout)
	defer cancel()

	// Learn the other exits' current addresses so a new identity can be
	// rejected when it lands in a /64 that is already in use.
	p.probeIPs(ctx)

	for attempt := 1; attempt <= p.opts.RotateAttempts; attempt++ {
		if ctx.Err() != nil {
			ep.markCooling(ctx.Err(), p.opts.FailureCool)
			return
		}
		ip, err := p.callRotate(ctx, ep)
		if err != nil {
			ep.markCooling(err, p.opts.FailureCool)
			slog.Warn("egress: rotation failed", "endpoint", ep.Name, "reason", reason, "error", err)
			return
		}
		if p.prefixInUse(ep, ip) && attempt < p.opts.RotateAttempts {
			slog.Warn("egress: rotated into a /64 already in use, retrying",
				"endpoint", ep.Name, "ip", ip, "prefix", prefix64(ip), "attempt", attempt)
			continue
		}
		ep.markReady(ip)
		if p.prefixInUse(ep, ip) {
			slog.Warn("egress: rotation left endpoint sharing a /64 with another exit",
				"endpoint", ep.Name, "ip", ip, "prefix", prefix64(ip), "attempts", p.opts.RotateAttempts)
		}
		slog.Info("egress: rotated", "endpoint", ep.Name, "reason", reason, "ip", ip, "attempt", attempt)
		return
	}
}

// callRotate asks the control API for one fresh identity and returns its IP.
func (p *Pool) callRotate(ctx context.Context, ep *endpoint) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.Control+"/rotate", http.NoBody)
	if err != nil {
		return "", err
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		return "", fmt.Errorf("control API returned %s", resp.Status)
	}
	return decodeRotation(resp.Body), nil
}

// Snapshot returns the current state of every endpoint.
func (p *Pool) Snapshot() []Snapshot {
	if p == nil {
		return nil
	}
	out := make([]Snapshot, 0, len(p.eps))
	for _, ep := range p.eps {
		out = append(out, ep.snapshot())
	}
	// Flag every endpoint sharing an IPv6 /64: they share the free-tier
	// quota, so the apparent pool size overstates the available identities.
	counts := make(map[string]int)
	for i := range out {
		if out[i].Prefix != "" {
			counts[out[i].Prefix]++
		}
	}
	for i := range out {
		if out[i].Prefix != "" && counts[out[i].Prefix] > 1 {
			out[i].DuplicatePrefix = true
		}
	}
	return out
}

// Len reports how many endpoints are configured.
func (p *Pool) Len() int {
	if p == nil {
		return 0
	}
	return len(p.eps)
}

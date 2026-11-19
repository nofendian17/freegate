// Package egress routes outbound connections through a pool of WARP proxies
// and rotates their exit identity when an upstream reports a free-tier limit.
//
// Each endpoint is one WARP sidecar (the official Cloudflare client behind a
// SOCKS5 listener) plus an optional control API that rotates its identity in
// place. OpenCode's free tier is counted per IPv6 /64, so rotating the client
// keys hands the next request a fresh quota without re-registering the device.
package egress

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
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
	// FallbackDirect dials without WARP when no endpoint is usable. Leaving
	// it false makes a dead pool fail closed instead of leaking the host IP.
	FallbackDirect bool
	// DialTimeout bounds one SOCKS5 dial (TCP connect + handshake).
	DialTimeout time.Duration
	// RotateTimeout bounds one control-API rotation.
	RotateTimeout time.Duration
	// FailureCool is how long an endpoint is skipped after a failed dial or
	// a failed rotation.
	FailureCool time.Duration
	// WaitReady is how long a dial waits for an endpoint that is currently
	// rotating. With a single-container pool this is what lets the retry
	// after a rotation succeed instead of failing closed.
	WaitReady time.Duration
}

func (o *Options) withDefaults() {
	if o.DialTimeout <= 0 {
		o.DialTimeout = 15 * time.Second
	}
	if o.RotateTimeout <= 0 {
		o.RotateTimeout = 60 * time.Second
	}
	if o.FailureCool <= 0 {
		o.FailureCool = time.Minute
	}
	if o.WaitReady <= 0 {
		o.WaitReady = 20 * time.Second
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
	Name      string    `json:"name"`
	Socks     string    `json:"socks"`
	Control   string    `json:"control,omitempty"`
	Status    Status    `json:"status"`
	IP        string    `json:"ip,omitempty"`
	LastError string    `json:"last_error,omitempty"`
	RotatedAt time.Time `json:"rotated_at,omitempty"`
	CoolUntil time.Time `json:"cool_until,omitempty"`
	Rotations int       `json:"rotations"`
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
	e.lastErr = ""
	e.coolUntil = time.Time{}
	e.rotatedAt = time.Now()
	e.rotations++
}

func (e *endpoint) markCooling(err error, d time.Duration) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.status = StatusCooling
	e.lastErr = errString(err)
	e.coolUntil = time.Now().Add(d)
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
	return true
}

func (e *endpoint) snapshot() Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	return Snapshot{
		Name:      e.Name,
		Socks:     e.Socks,
		Control:   e.Control,
		Status:    e.status,
		IP:        e.ip,
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

// Pool is a set of WARP endpoints with round-robin selection and in-place
// rotation.
type Pool struct {
	opts   Options
	dialer *net.Dialer
	client *http.Client

	eps  []*endpoint
	next atomic.Uint64
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
// across the pool. When every endpoint is rotating it waits up to WaitReady
// for one to come back; when nothing is usable after that it falls back to a
// direct dial only if Options.FallbackDirect is set.
func (p *Pool) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	if p == nil {
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	waitUntil := time.Now().Add(p.opts.WaitReady)
	var lastErr error
	for {
		tried := 0
		for _, ep := range p.candidates(time.Now()) {
			tried++
			conn, err := dialSOCKS5(ctx, p.dialer, ep.Socks, addr)
			if err == nil {
				p.last.Store(ep)
				return conn, nil
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
		case <-time.After(250 * time.Millisecond):
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
		ep.mu.Lock()
		rotating := ep.status == StatusRotating
		ep.mu.Unlock()
		if rotating {
			return true
		}
	}
	return false
}

// RotateAsync rotates the endpoint that served the most recent successful
// dial. It returns immediately; the container keeps serving the rest of the
// pool while its identity changes.
//
// When no endpoint is recorded (another rotation already claimed it, or no
// dial has happened yet) the call is a no-op on purpose: a burst of 429s for
// one exit must not cascade into rotating every other exit, which would burn
// identities without fixing anything.
func (p *Pool) RotateAsync(reason string) {
	if p == nil {
		return
	}
	ep := p.last.Swap(nil)
	if ep == nil {
		slog.Debug("egress: rotation skipped, no endpoint attributed", "reason", reason)
		return
	}
	go p.rotate(ep, reason)
}

func (p *Pool) rotate(ep *endpoint, reason string) {
	if !ep.beginRotate() {
		return
	}
	if strings.TrimSpace(ep.Control) == "" {
		// The endpoint still serves traffic, it just cannot change identity.
		// Keep it usable and tell the operator once per attempt.
		ep.mu.Lock()
		ep.status = StatusReady
		ep.mu.Unlock()
		slog.Warn("egress: rotation requested but no control endpoint is configured", "endpoint", ep.Name)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), p.opts.RotateTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ep.Control+"/rotate", http.NoBody)
	if err != nil {
		ep.markCooling(err, p.opts.FailureCool)
		return
	}
	resp, err := p.client.Do(req)
	if err != nil {
		ep.markCooling(err, p.opts.FailureCool)
		slog.Warn("egress: rotation failed", "endpoint", ep.Name, "reason", reason, "error", err)
		return
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		err := fmt.Errorf("control API returned %s", resp.Status)
		ep.markCooling(err, p.opts.FailureCool)
		slog.Warn("egress: rotation failed", "endpoint", ep.Name, "reason", reason, "error", err)
		return
	}
	ip := decodeRotation(resp.Body)
	ep.markReady(ip)
	slog.Info("egress: rotated", "endpoint", ep.Name, "reason", reason, "ip", ip)
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
	return out
}

// Len reports how many endpoints are configured.
func (p *Pool) Len() int {
	if p == nil {
		return 0
	}
	return len(p.eps)
}

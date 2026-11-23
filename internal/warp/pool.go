package warp

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"runtime"
	"sync"
	"time"
)

const DefaultPoolSize = 2

// egressCooldown keeps a public /64 (or IPv4 /32) that upstream rejected out
// of the pool for this long. The free tier scopes its quota to that block, so
// a new tunnel inside it would only repeat the same answer.
const egressCooldown = 30 * time.Minute

// refillRetryDelay spaces out a failed registration attempt.
const refillRetryDelay = 5 * time.Second

// A pool can only hold as many exits as Cloudflare hands out distinct egress
// blocks. After a few duplicate registrations the pool backs off instead of
// hammering the registration API forever.
const (
	refillFailureThreshold = 3
	refillBackoffBase      = time.Minute
	refillBackoffMax       = 5 * time.Minute
)

// scaleDownDelay keeps exits created for a burst hot for a while after the
// burst, so a following burst reuses them instead of paying for new
// registrations.
const scaleDownDelay = 60 * time.Second

var errDuplicateEgress = errors.New("WARP egress is already active or cooling down")

// ErrClosed is returned when a pool is used after Close.
var ErrClosed = errors.New("WARP pool is closed")

type Pool struct {
	ctx     context.Context
	cancel  context.CancelFunc
	log     *slog.Logger
	size    int
	preload int
	stack   Stack
	source  netip.Prefix
	newExit func(context.Context) (*exit, error)

	mu        sync.Mutex
	active    []*exit
	inflight  int
	waiting   int
	next      int
	reserved  map[string]bool
	cooling   map[string]time.Time
	changed   chan struct{}
	closed    bool
	refilling bool
	scaling   bool
	// refillFailures counts consecutive failed preload registrations;
	// refillPaused keeps every refill trigger quiet until it expires.
	refillFailures int
	refillPaused   time.Time
	wait           sync.WaitGroup
	createMu       sync.Mutex
}

type Lease struct {
	pool *Pool
	exit *exit
	once sync.Once
}

// ExitSnapshot is the observable state of one in-process WARP exit.
type ExitSnapshot struct {
	ID        string `json:"id"`
	Egress    string `json:"egress,omitempty"`
	Prefix    string `json:"prefix,omitempty"`
	Stack     string `json:"stack"`
	Status    string `json:"status"`
	Idle      bool   `json:"idle"`
	Penalized bool   `json:"penalized,omitempty"`
}

// Snapshot is a point-in-time view of a pool for health and admin reporting.
type Snapshot struct {
	Stack   string         `json:"stack"`
	Size    int            `json:"size"`
	Preload int            `json:"preload"`
	Active  int            `json:"active"`
	Standby int            `json:"standby"`
	Cooling int            `json:"cooling"`
	Exits   []ExitSnapshot `json:"exits"`
}

// Options configure a WARP pool.
type Options struct {
	// Size is the upper bound on exits. Half of it (at least one) is kept hot
	// even while the pool is idle.
	Size int
	// Stack selects the address family inside the tunnel.
	Stack Stack
	// SourceIPv6 is the outer IPv6 prefix each exit binds its WireGuard socket
	// to. Every exit draws its own address from it, so a replacement reaches
	// Cloudflare from a new source and receives a new public egress.
	SourceIPv6 netip.Prefix
	// AutoSource discovers a routed public prefix from the host when
	// SourceIPv6 is unset. Explicit configuration always wins.
	AutoSource bool
}

// NewWithOptions creates a WARP pool whose exits use the selected IP stack
// and, optionally, rotate their outer IPv6 source within a prefix.
func NewWithOptions(ctx context.Context, log *slog.Logger, opts Options) (*Pool, error) {
	normalized, err := ParseStack(string(opts.Stack))
	if err != nil {
		return nil, err
	}
	sourcePrefix := resolveSourcePrefix(opts, log, DiscoverSourcePrefix)
	if sourcePrefix.IsValid() && !sourceBindingSupported() {
		return nil, fmt.Errorf("WARP outer source requires Linux IPV6_FREEBIND; running on %s", runtime.GOOS)
	}
	if sourcePrefix.IsValid() {
		if err := ValidateSourcePrefix(sourcePrefix); err != nil {
			return nil, fmt.Errorf("WARP outer source: %w", err)
		}
	}
	size := opts.Size
	if size <= 0 {
		size = DefaultPoolSize
	}
	r := newRegistrar()
	pool, err := newPool(ctx, log, size, func(ctx context.Context) (*exit, error) {
		reg, err := r.register(ctx)
		if err != nil {
			return nil, err
		}
		var source netip.Addr
		if sourcePrefix.IsValid() {
			source, err = randomSource(sourcePrefix)
			if err != nil {
				return nil, err
			}
		}
		return newExit(ctx, reg, normalized, source)
	})
	if err != nil {
		return nil, err
	}
	pool.stack = normalized
	pool.source = sourcePrefix
	return pool, nil
}

// Source reports the outer prefix used by exits. An invalid prefix means the
// pool is running without source rotation.
func (p *Pool) Source() netip.Prefix {
	if p == nil {
		return netip.Prefix{}
	}
	return p.source
}

// resolveSourcePrefix picks the outer source prefix. An explicit prefix always
// wins; auto-detection failure degrades to no source (WARP still works over
// IPv4 endpoints, but rotation may stay in the same quota /64).
func resolveSourcePrefix(opts Options, log *slog.Logger, discover func() (netip.Prefix, error)) netip.Prefix {
	if opts.SourceIPv6.IsValid() {
		return opts.SourceIPv6
	}
	if !opts.AutoSource {
		return netip.Prefix{}
	}
	prefix, err := discover()
	if err != nil {
		if log != nil {
			log.Warn("WARP outer source unavailable; running without source rotation", "error", SourceError(err).Error())
		}
		return netip.Prefix{}
	}
	if log != nil {
		log.Info("WARP outer source auto-detected", "prefix", prefix.String())
	}
	return prefix
}

// randomSource draws one host address from prefix. Bits above the prefix
// length are kept, every bit below is random, so two exits practically never
// share an outer source.
func randomSource(prefix netip.Prefix) (netip.Addr, error) {
	address := prefix.Masked().Addr().As16()
	var noise [16]byte
	if _, err := rand.Read(noise[:]); err != nil {
		return netip.Addr{}, fmt.Errorf("draw WARP outer source: %w", err)
	}
	keep := prefix.Bits()
	for index := range address {
		switch bits := keep - index*8; {
		case bits >= 8:
		case bits <= 0:
			address[index] = noise[index]
		default:
			mask := byte(0xFF) << (8 - bits)
			address[index] = address[index]&mask | noise[index]&^mask
		}
	}
	return netip.AddrFrom16(address), nil
}

func newPool(ctx context.Context, log *slog.Logger, size int, create func(context.Context) (*exit, error)) (*Pool, error) {
	if size <= 0 {
		size = DefaultPoolSize
	}
	// Keep half of the ceiling warm (at least one) so a burst finds ready
	// exits instead of paying for registrations while requests queue.
	preload := size / 2
	if preload < 1 {
		preload = 1
	}
	// The caller's context bounds the synchronous first registration, not the
	// pool: a startup deadline expires moments after construction.
	poolCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	p := &Pool{
		ctx:      poolCtx,
		cancel:   cancel,
		log:      log,
		size:     size,
		preload:  preload,
		newExit:  create,
		reserved: make(map[string]bool),
		cooling:  make(map[string]time.Time),
		changed:  make(chan struct{}),
	}
	// Cloudflare can answer the first registration with a rate limit even on
	// a fresh process (the limit follows the source address), so wait out one
	// window inside the caller's deadline instead of failing startup over a
	// pause that is about to end.
	first, err := p.createExit(ctx)
	for err != nil {
		pause := registrationPause(err)
		if pause <= 0 {
			cancel()
			return nil, err
		}
		p.info("WARP first registration paused", "pause", pause.Round(time.Second))
		timer := time.NewTimer(pause)
		select {
		case <-ctx.Done():
			timer.Stop()
			cancel()
			return nil, ctx.Err()
		case <-timer.C:
		}
		first, err = p.createExit(ctx)
	}
	p.active = []*exit{first}
	p.startRefill()
	return p, nil
}

func (p *Pool) debug(msg string, args ...any) {
	if p.log != nil {
		p.log.Debug(msg, args...)
	}
}
func (p *Pool) info(msg string, args ...any) {
	if p.log != nil {
		p.log.Info(msg, args...)
	}
}
func (p *Pool) warn(msg string, args ...any) {
	if p.log != nil {
		p.log.Warn(msg, args...)
	}
}
func (p *Pool) err(msg string, args ...any) {
	if p.log != nil {
		p.log.Error(msg, args...)
	}
}

// desiredLocked is the number of exits the pool should keep hot: one per
// in-flight lease, one per waiting request, plus a spare.
func (p *Pool) desiredLocked() int {
	want := p.inflight + p.waiting + 1
	if want < p.preload {
		want = p.preload
	}
	if want < 2 {
		want = 2
	}
	return min(want, p.size)
}

// Acquire takes a ready exit, growing the ring in the background as demand
// rises. The caller must Release the lease when the response body is closed.
func (p *Pool) Acquire(ctx context.Context) (*Lease, error) {
	for {
		p.mu.Lock()
		if p.closed {
			p.mu.Unlock()
			return nil, ErrClosed
		}
		for attempts := 0; attempts < len(p.active); attempts++ {
			index := p.next % len(p.active)
			p.next = (p.next + 1) % len(p.active)
			active := p.active[index]
			if active.addRef() {
				p.inflight++
				p.mu.Unlock()
				p.startRefill()
				return &Lease{pool: p, exit: active}, nil
			}
		}
		p.waiting++
		changed := p.changed
		needRefill := len(p.active) < p.desiredLocked()
		p.mu.Unlock()
		if needRefill {
			p.startRefill()
		}
		var err error
		select {
		case <-ctx.Done():
			err = ctx.Err()
		case <-p.ctx.Done():
			err = ErrClosed
		case <-changed:
		}
		p.mu.Lock()
		p.waiting--
		p.mu.Unlock()
		if err != nil {
			return nil, err
		}
	}
}

// Fail drops the exit that served lease and reports why: a free-tier 429, a
// broken handshake, or a stalled stream. The returned channel closes when the
// replacement attempt finishes, whether it produced a new exit or restored the
// old one, so a retry knows rotation has settled.
func (p *Pool) Fail(lease *Lease, reason string) <-chan struct{} {
	done := make(chan struct{})
	if lease == nil || lease.exit == nil || reason == "" {
		close(done)
		return done
	}
	p.penalize(lease.exit, reason, done)
	return done
}

func (p *Pool) Size() int { return p.size }

// Snapshot reports pool capacity and the public egress of every active exit.
// It never exposes keys or device tokens.
func (p *Pool) Snapshot() Snapshot {
	p.mu.Lock()
	defer p.mu.Unlock()
	snapshot := Snapshot{
		Stack:   string(p.stack),
		Size:    p.size,
		Preload: p.preload,
		Active:  len(p.active),
		Cooling: len(p.cooling),
	}
	for _, active := range p.active {
		idle := active.idle()
		if idle {
			snapshot.Standby++
		}
		key := active.egressKey()
		snapshot.Exits = append(snapshot.Exits, ExitSnapshot{
			ID:        active.id,
			Egress:    active.egress.String(),
			Prefix:    key,
			Stack:     string(active.stack),
			Status:    "ready",
			Idle:      idle,
			Penalized: active.isPenalized(),
		})
	}
	return snapshot
}

func (p *Pool) penalize(bad *exit, reason string, done chan struct{}) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		close(done)
		return
	}
	index := p.activeIndexLocked(bad)
	if index < 0 {
		p.mu.Unlock()
		close(done)
		return
	}
	if !bad.markPenalized() {
		p.mu.Unlock()
		close(done)
		return
	}
	if key := bad.egressKey(); key != "" {
		p.cooling[key] = time.Now().Add(egressCooldown)
		delete(p.reserved, key)
	}
	p.active = append(p.active[:index], p.active[index+1:]...)
	if p.next >= len(p.active) {
		p.next = 0
	}
	p.notifyLocked()
	p.wait.Add(1)
	p.mu.Unlock()

	p.warn("rotating WARP egress", "exit", bad.id, "reason", reason)
	go func() {
		defer p.wait.Done()
		defer close(done)
		p.replace(bad, reason)
	}()
}

func (p *Pool) replace(old *exit, reason string) {
	replacement, err := p.createExit(p.ctx)
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		if replacement != nil {
			p.releaseReservation(replacement)
			replacement.close()
		}
		old.retire()
		return
	}
	if err != nil {
		restored := len(p.active) == 0
		if restored {
			old.clearPenalty()
			if key := old.egressKey(); key != "" {
				p.reserved[key] = true
				delete(p.cooling, key)
			}
			p.active = append(p.active, old)
			p.notifyLocked()
		}
		p.mu.Unlock()
		if !restored {
			old.retire()
		}
		if pause := registrationPause(err); pause > 0 {
			p.info("WARP replacement paused", "pause", pause.Round(time.Second))
		} else {
			p.err("cannot replace WARP egress", "exit", old.id, "error", err)
		}
		p.startRefill()
		return
	}
	if p.activeIndexLocked(old) >= 0 {
		p.mu.Unlock()
		p.releaseReservation(replacement)
		replacement.close()
		old.retire()
		return
	}
	placed := false
	if len(p.active) < p.size {
		p.active = append(p.active, replacement)
		placed = true
	}
	p.notifyLocked()
	p.mu.Unlock()
	if !placed {
		p.releaseReservation(replacement)
		replacement.close()
		old.retire()
		return
	}
	p.info("WARP egress replaced", "old", old.id, "new", replacement.id, "egress", replacement.egress.String(), "reason", reason)
	old.retire()
	p.startRefill()
}

func (p *Pool) refill() {
	p.mu.Lock()
	if p.closed || p.refilling || time.Now().Before(p.refillPaused) || !p.needsRefillLocked() {
		p.mu.Unlock()
		return
	}
	p.refilling = true
	p.mu.Unlock()

	replacement, err := p.createExit(p.ctx)
	p.mu.Lock()
	p.refilling = false
	placed := false
	if err == nil && !p.closed && len(p.active) < p.size {
		p.active = append(p.active, replacement)
		replacement = nil
		placed = true
	}
	if err == nil {
		p.refillFailures = 0
		p.refillPaused = time.Time{}
	}
	if placed || p.closed {
		p.notifyLocked()
	}
	p.mu.Unlock()

	if replacement != nil {
		p.releaseReservation(replacement)
		replacement.close()
	}
	if err != nil && p.ctx.Err() == nil {
		delay := refillRetryDelay
		if pause := registrationPause(err); pause > 0 {
			delay = max(pause, refillRetryDelay)
			if delay > maxRateLimitPause {
				delay = maxRateLimitPause
			}
			p.info("WARP registration paused", "pause", delay.Round(time.Second))
		} else if errors.Is(err, errDuplicateEgress) {
			p.mu.Lock()
			p.refillFailures++
			failures := p.refillFailures
			p.mu.Unlock()
			if failures >= refillFailureThreshold {
				shift := min(failures-refillFailureThreshold, 3)
				delay = refillBackoffBase << shift
				if delay > refillBackoffMax {
					delay = refillBackoffMax
				}
				p.info("WARP preload paused after duplicate egresses", "pause", delay.Round(time.Second), "failures", failures)
			}
		} else {
			p.warn("cannot pre-register WARP egress", "error", err)
		}
		p.mu.Lock()
		if paused := time.Now().Add(delay); paused.After(p.refillPaused) {
			p.refillPaused = paused
		}
		p.mu.Unlock()
		p.after(delay, p.retryRefill)
		return
	}
	p.startRefill()
}

func (p *Pool) retryRefill() {
	p.mu.Lock()
	p.refillPaused = time.Time{}
	p.mu.Unlock()
	p.startRefill()
}

func (p *Pool) startRefill() {
	p.mu.Lock()
	if p.closed || p.refilling || time.Now().Before(p.refillPaused) || !p.needsRefillLocked() {
		p.mu.Unlock()
		return
	}
	p.wait.Add(1)
	p.mu.Unlock()
	go func() {
		defer p.wait.Done()
		p.refill()
	}()
}

// released records that a lease was returned and trims the ring back toward
// the demand of the moment. Freed exits stay hot for scaleDownDelay.
func (p *Pool) released() {
	p.mu.Lock()
	if p.inflight > 0 {
		p.inflight--
	}
	closed := p.closed
	wantRefill := !closed && len(p.active) < p.desiredLocked()
	scheduleScaleDown := !closed && !p.scaling && len(p.active) > p.desiredLocked()
	if scheduleScaleDown {
		p.scaling = true
	}
	p.mu.Unlock()
	if closed {
		return
	}
	if wantRefill {
		p.startRefill()
	}
	if scheduleScaleDown {
		p.after(scaleDownDelay, p.scaleDown)
	}
}

func (p *Pool) scaleDown() {
	p.mu.Lock()
	p.scaling = false
	if p.closed {
		p.mu.Unlock()
		return
	}
	want := p.desiredLocked()
	var dropped []*exit
	if surplus := len(p.active) - want; surplus > 0 {
		kept := p.active[:0]
		for _, candidate := range p.active {
			if surplus > 0 && candidate.idle() {
				dropped = append(dropped, candidate)
				surplus--
				continue
			}
			kept = append(kept, candidate)
		}
		p.active = kept
	}
	if p.next >= len(p.active) {
		p.next = 0
	}
	p.mu.Unlock()
	for _, drop := range dropped {
		p.releaseReservation(drop)
		drop.retire()
		p.info("WARP egress scaled down", "exit", drop.id, "egress", drop.egress.String())
	}
}

// after runs fn once delay has elapsed, unless the pool closes first.
func (p *Pool) after(delay time.Duration, fn func()) {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.wait.Add(1)
	p.mu.Unlock()
	go func() {
		defer p.wait.Done()
		timer := time.NewTimer(delay)
		defer timer.Stop()
		select {
		case <-p.ctx.Done():
		case <-timer.C:
			fn()
		}
	}()
}

func (p *Pool) createExit(ctx context.Context) (*exit, error) {
	p.createMu.Lock()
	defer p.createMu.Unlock()
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		p.mu.Lock()
		closed := p.closed
		p.mu.Unlock()
		if closed {
			return nil, ErrClosed
		}
		candidate, err := p.newExit(ctx)
		if err != nil {
			return nil, err
		}
		key := candidate.egressKey()
		now := time.Now()
		p.mu.Lock()
		for subnet, until := range p.cooling {
			if !until.After(now) {
				delete(p.cooling, subnet)
			}
		}
		usable := key != "" && !p.reserved[key] && !p.cooling[key].After(now)
		if usable {
			p.reserved[key] = true
		}
		p.mu.Unlock()
		if usable {
			p.info("WARP egress ready", "stack", candidate.stack, "exit", candidate.id, "egress", candidate.egress.String())
			return candidate, nil
		}
		lastErr = fmt.Errorf("%w: %s", errDuplicateEgress, key)
		p.warn("discarding duplicate WARP exit", "exit", candidate.id, "key", key)
		candidate.close()
	}
	return nil, lastErr
}

func (p *Pool) releaseReservation(e *exit) {
	if e == nil {
		return
	}
	key := e.egressKey()
	if key == "" {
		return
	}
	p.mu.Lock()
	delete(p.reserved, key)
	p.mu.Unlock()
}

func (p *Pool) notifyLocked() {
	close(p.changed)
	p.changed = make(chan struct{})
}

func (p *Pool) activeIndexLocked(target *exit) int {
	for index, candidate := range p.active {
		if candidate == target {
			return index
		}
	}
	return -1
}

func (p *Pool) needsRefillLocked() bool {
	return len(p.active) < p.desiredLocked()
}

// Close retires every exit and waits for background work to unwind.
func (p *Pool) Close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	active := p.active
	p.active = nil
	p.notifyLocked()
	p.mu.Unlock()

	p.cancel()
	for _, item := range active {
		p.releaseReservation(item)
		item.retire()
	}
	p.wait.Wait()
}

// DialContext dials through this lease's exit. A released lease fails closed.
func (l *Lease) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	if l == nil || l.exit == nil {
		return nil, errors.New("WARP lease is closed")
	}
	return l.exit.dialContext(ctx, network, address)
}

// Release returns the lease to its pool. Safe to call more than once.
func (l *Lease) Release() {
	if l == nil {
		return
	}
	l.once.Do(func() {
		if l.exit != nil {
			l.exit.release()
		}
		if l.pool != nil {
			l.pool.released()
		}
	})
}

func (l *Lease) EgressKey() string {
	if l == nil || l.exit == nil {
		return ""
	}
	return l.exit.egressKey()
}

func (l *Lease) String() string {
	if l == nil || l.exit == nil {
		return "<nil>"
	}
	return fmt.Sprintf("%s/%s", l.exit.id, l.exit.egress)
}

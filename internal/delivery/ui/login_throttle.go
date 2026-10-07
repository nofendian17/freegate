package ui

import (
	"sync"
	"time"
)

// loginThrottle enforces an exponentially increasing wait after
// consecutive failed logins, keyed by client IP: after the n-th failure
// the next attempt is blocked for min(base*2^(n-1), max). A successful
// login resets the count, so the legitimate owner never faces more than
// one short wait after a typo.
//
// There is no background janitor: failed logins are rare in normal
// operation, so entries are pruned lazily inside fail(). The map is hard
// capped so a flood of rotating source IPs cannot grow memory without
// bound — on overflow all entries are dropped (per-IP tracking is already
// defeated in that scenario; the RateLimiter middleware caps request rate
// separately).
type loginThrottle struct {
	mu         sync.Mutex
	entries    map[string]*loginAttempt
	base       time.Duration
	max        time.Duration
	maxEntries int
}

type loginAttempt struct {
	failures    int
	nextAllowed time.Time
	lastSeen    time.Time
}

// loginThrottleBase/max are the production waits: 2s after the first
// failure, doubling each further failure, capped at 5 minutes.
const (
	loginThrottleBase = 2 * time.Second
	loginThrottleMax  = 5 * time.Minute
)

func newLoginThrottle() *loginThrottle {
	return &loginThrottle{
		entries:    make(map[string]*loginAttempt),
		base:       loginThrottleBase,
		max:        loginThrottleMax,
		maxEntries: 4096,
	}
}

// blocked reports how much longer the IP must wait before its next
// attempt, or 0 if it may proceed.
func (t *loginThrottle) blocked(ip string) time.Duration {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	a := t.entries[ip]
	if a == nil {
		return 0
	}
	if d := time.Until(a.nextAllowed); d > 0 {
		return d
	}
	return 0
}

// fail records a failed attempt and returns the wait before the next one
// is accepted. A nil throttle records nothing and returns 0.
func (t *loginThrottle) fail(ip string) time.Duration {
	if t == nil {
		return 0
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	now := time.Now()
	for key, a := range t.entries {
		if now.Sub(a.lastSeen) > t.max {
			delete(t.entries, key)
		}
	}
	if len(t.entries) >= t.maxEntries {
		t.entries = make(map[string]*loginAttempt)
	}
	a := t.entries[ip]
	if a == nil {
		a = &loginAttempt{}
		t.entries[ip] = a
	}
	a.failures++
	a.lastSeen = now
	d := t.base
	for i := 1; i < a.failures && d < t.max; i++ {
		d *= 2
	}
	if d > t.max {
		d = t.max
	}
	a.nextAllowed = now.Add(d)
	return d
}

// reset clears the IP's history after a successful login.
func (t *loginThrottle) reset(ip string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, ip)
}

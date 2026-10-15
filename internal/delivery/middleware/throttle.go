package middleware

import (
	"sync"
	"time"
)

// AuthThrottle enforces an exponentially increasing wait after consecutive
// failed authentication attempts, keyed by client IP: after the n-th failure
// the next attempt is blocked for min(base*2^(n-1), max). A successful
// attempt resets the count, so the legitimate owner never faces more than
// one short wait after a typo.
//
// It backs both POST /login (see ui.Handler) and AdminAuth's rejected
// credential path, so header-token guessing faces the same backoff as the
// login form instead of unlimited tries.
//
// There is no background janitor: failed attempts are rare in normal
// operation, so entries are pruned lazily inside Fail. The map is hard
// capped so a flood of rotating source IPs cannot grow memory without
// bound — on overflow all entries are dropped (per-IP tracking is already
// defeated in that scenario; the RateLimiter middleware caps request rate
// separately).
type AuthThrottle struct {
	mu         sync.Mutex
	entries    map[string]*authAttempt
	base       time.Duration
	max        time.Duration
	maxEntries int
}

type authAttempt struct {
	failures    int
	nextAllowed time.Time
	lastSeen    time.Time
}

// authThrottleBase/Max are the production waits: 2s after the first
// failure, doubling each further failure, capped at 5 minutes.
const (
	authThrottleBase = 2 * time.Second
	authThrottleMax  = 5 * time.Minute
)

// NewAuthThrottle returns a throttle with the production waits.
func NewAuthThrottle() *AuthThrottle {
	return &AuthThrottle{
		entries:    make(map[string]*authAttempt),
		base:       authThrottleBase,
		max:        authThrottleMax,
		maxEntries: 4096,
	}
}

// Blocked reports how much longer the IP must wait before its next
// attempt, or 0 if it may proceed. Safe on a nil throttle.
func (t *AuthThrottle) Blocked(ip string) time.Duration {
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

// Fail records a failed attempt and returns the wait before the next one
// is accepted. A nil throttle records nothing and returns 0.
func (t *AuthThrottle) Fail(ip string) time.Duration {
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
		t.entries = make(map[string]*authAttempt)
	}
	a := t.entries[ip]
	if a == nil {
		a = &authAttempt{}
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

// Reset clears the IP's history after a successful attempt. Safe on a nil
// throttle.
func (t *AuthThrottle) Reset(ip string) {
	if t == nil {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.entries, ip)
}

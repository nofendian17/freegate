package middleware

import (
	"testing"
	"time"
)

// newTestThrottle builds a throttle with tiny waits so tests can sleep
// past them instead of scaling up real second-scale delays.
func newTestThrottle() *AuthThrottle {
	return &AuthThrottle{
		entries:    make(map[string]*authAttempt),
		base:       10 * time.Millisecond,
		max:        time.Minute,
		maxEntries: 4096,
	}
}

func TestAuthThrottle_WaitDoublesEachFailure(t *testing.T) {
	th := newTestThrottle()
	want := []time.Duration{10, 20, 40, 80, 160, 320}
	for i, w := range want {
		if got := th.Fail("1.2.3.4"); got != w*time.Millisecond {
			t.Fatalf("failure %d: wait = %v, want %v", i+1, got, w*time.Millisecond)
		}
	}
}

func TestAuthThrottle_CapsAtMax(t *testing.T) {
	th := newTestThrottle()
	th.base = time.Second
	th.max = 4 * time.Second
	for i := 0; i < 10; i++ {
		th.Fail("1.2.3.4")
	}
	if got := th.Fail("1.2.3.4"); got != 4*time.Second {
		t.Fatalf("wait = %v, want capped at %v", got, 4*time.Second)
	}
}

func TestAuthThrottle_BlockedUntilWaitPasses(t *testing.T) {
	th := newTestThrottle()
	if d := th.Blocked("1.2.3.4"); d != 0 {
		t.Fatalf("fresh ip blocked for %v", d)
	}
	th.Fail("1.2.3.4")
	if d := th.Blocked("1.2.3.4"); d <= 0 {
		t.Fatal("blocked window should be positive right after a failure")
	}
	time.Sleep(11 * time.Millisecond)
	if d := th.Blocked("1.2.3.4"); d != 0 {
		t.Fatalf("still blocked %v after wait passed", d)
	}
}

func TestAuthThrottle_ResetOnSuccess(t *testing.T) {
	th := newTestThrottle()
	th.Fail("1.2.3.4")
	th.Fail("1.2.3.4")
	th.Reset("1.2.3.4")
	if d := th.Blocked("1.2.3.4"); d != 0 {
		t.Fatalf("reset ip still blocked for %v", d)
	}
	if got := th.Fail("1.2.3.4"); got != th.base {
		t.Fatalf("wait after reset = %v, want base %v (failure count should restart)", got, th.base)
	}
}

func TestAuthThrottle_PerIPIsolation(t *testing.T) {
	th := newTestThrottle()
	th.Fail("1.2.3.4")
	th.Fail("1.2.3.4")
	th.Fail("1.2.3.4")
	if got := th.Fail("5.6.7.8"); got != th.base {
		t.Fatalf("different ip wait = %v, want base %v", got, th.base)
	}
}

func TestAuthThrottle_NilIsNoop(t *testing.T) {
	var th *AuthThrottle
	if d := th.Blocked("1.2.3.4"); d != 0 {
		t.Fatalf("nil throttle blocked for %v", d)
	}
	if d := th.Fail("1.2.3.4"); d != 0 {
		t.Fatalf("nil throttle fail = %v, want 0", d)
	}
	th.Reset("1.2.3.4") // must not panic
}

func TestAuthThrottle_EntriesArePruned(t *testing.T) {
	th := newTestThrottle()
	th.maxEntries = 2
	th.Fail("a")
	th.Fail("b")
	// Third unique ip exceeds the cap: entries drop, including stale ones,
	// so the map cannot grow without bound.
	th.Fail("c")
	if len(th.entries) > th.maxEntries {
		t.Fatalf("entries = %d, cap = %d", len(th.entries), th.maxEntries)
	}
}

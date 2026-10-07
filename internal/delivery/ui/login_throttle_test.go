package ui

import (
	"testing"
	"time"
)

// newTestThrottle builds a throttle with tiny waits so tests can sleep
// past them instead of scaling up real second-scale delays.
func newTestThrottle() *loginThrottle {
	return &loginThrottle{
		entries:    make(map[string]*loginAttempt),
		base:       10 * time.Millisecond,
		max:        time.Minute,
		maxEntries: 4096,
	}
}

func TestLoginThrottle_WaitDoublesEachFailure(t *testing.T) {
	th := newTestThrottle()
	want := []time.Duration{10, 20, 40, 80, 160, 320}
	for i, w := range want {
		if got := th.fail("1.2.3.4"); got != w*time.Millisecond {
			t.Fatalf("failure %d: wait = %v, want %v", i+1, got, w*time.Millisecond)
		}
	}
}

func TestLoginThrottle_CapsAtMax(t *testing.T) {
	th := newTestThrottle()
	th.base = time.Second
	th.max = 4 * time.Second
	for i := 0; i < 10; i++ {
		th.fail("1.2.3.4")
	}
	if got := th.fail("1.2.3.4"); got != 4*time.Second {
		t.Fatalf("wait = %v, want capped at %v", got, 4*time.Second)
	}
}

func TestLoginThrottle_BlockedUntilWaitPasses(t *testing.T) {
	th := newTestThrottle()
	if d := th.blocked("1.2.3.4"); d != 0 {
		t.Fatalf("fresh ip blocked for %v", d)
	}
	th.fail("1.2.3.4")
	if d := th.blocked("1.2.3.4"); d <= 0 {
		t.Fatal("blocked window should be positive right after a failure")
	}
	time.Sleep(11 * time.Millisecond)
	if d := th.blocked("1.2.3.4"); d != 0 {
		t.Fatalf("still blocked %v after wait passed", d)
	}
}

func TestLoginThrottle_ResetOnSuccess(t *testing.T) {
	th := newTestThrottle()
	th.fail("1.2.3.4")
	th.fail("1.2.3.4")
	th.reset("1.2.3.4")
	if d := th.blocked("1.2.3.4"); d != 0 {
		t.Fatalf("reset ip still blocked for %v", d)
	}
	if got := th.fail("1.2.3.4"); got != th.base {
		t.Fatalf("wait after reset = %v, want base %v (failure count should restart)", got, th.base)
	}
}

func TestLoginThrottle_PerIPIsolation(t *testing.T) {
	th := newTestThrottle()
	th.fail("1.2.3.4")
	th.fail("1.2.3.4")
	th.fail("1.2.3.4")
	if got := th.fail("5.6.7.8"); got != th.base {
		t.Fatalf("different ip wait = %v, want base %v", got, th.base)
	}
}

func TestLoginThrottle_NilIsNoop(t *testing.T) {
	var th *loginThrottle
	if d := th.blocked("1.2.3.4"); d != 0 {
		t.Fatalf("nil throttle blocked for %v", d)
	}
	th.reset("1.2.3.4") // must not panic
}

func TestLoginThrottle_EntriesArePruned(t *testing.T) {
	th := newTestThrottle()
	th.maxEntries = 2
	th.fail("a")
	th.fail("b")
	// Third unique ip exceeds the cap: entries drop, including stale ones,
	// so the map cannot grow without bound.
	th.fail("c")
	if len(th.entries) > th.maxEntries {
		t.Fatalf("entries = %d, cap = %d", len(th.entries), th.maxEntries)
	}
}

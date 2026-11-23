package warp

import (
	"context"
	"net/netip"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fakeExit(id, egress string) *exit {
	return &exit{
		id:     id,
		egress: netip.MustParseAddr(egress),
		stack:  StackIPv6,
	}
}

func TestRandomSourceKeepsPrefixAndDiffers(t *testing.T) {
	prefix := netip.MustParsePrefix("2001:db8:1234:5678::/64")
	seen := make(map[netip.Addr]bool)
	for i := 0; i < 20; i++ {
		addr, err := randomSource(prefix)
		if err != nil {
			t.Fatal(err)
		}
		if !prefix.Contains(addr) {
			t.Fatalf("source %s escaped prefix %s", addr, prefix)
		}
		if seen[addr] {
			t.Fatalf("duplicate source %s", addr)
		}
		seen[addr] = true
	}
}

func TestParseStack(t *testing.T) {
	cases := map[string]Stack{
		"":      StackIPv6,
		"v6":    StackIPv6,
		"IPV4":  StackIPv4,
		"dual":  StackDual,
		"other": "",
	}
	for in, want := range cases {
		got, err := ParseStack(in)
		if want == "" {
			if err == nil {
				t.Errorf("ParseStack(%q) accepted invalid value", in)
			}
			continue
		}
		if err != nil || got != want {
			t.Errorf("ParseStack(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
}

func TestEgressKeyUses64(t *testing.T) {
	e := fakeExit("1", "2606:4700:1234:5678::1234")
	if got, want := e.egressKey(), "2606:4700:1234:5678::/64"; got != want {
		t.Fatalf("egressKey = %q, want %q", got, want)
	}
	v4 := fakeExit("2", "162.159.192.10")
	if got, want := v4.egressKey(), "162.159.192.10/32"; got != want {
		t.Fatalf("IPv4 egressKey = %q, want %q", got, want)
	}
}

func TestParseTraceRequiresWarpAndPublicIP(t *testing.T) {
	good := "ip=2606:4700:1::1\nwarp=on\n"
	addr, err := parseTrace(strings.NewReader(good))
	if err != nil || addr.String() != "2606:4700:1::1" {
		t.Fatalf("parseTrace = %s, %v", addr, err)
	}
	if _, err := parseTrace(strings.NewReader("ip=2606:4700:1::1\nwarp=off\n")); err == nil {
		t.Fatal("warp=off was accepted")
	}
	if _, err := parseTrace(strings.NewReader("ip=192.168.1.1\nwarp=on\n")); err == nil {
		t.Fatal("private address was accepted")
	}
}

func TestPoolAcquireRelease(t *testing.T) {
	var created atomic.Int64
	p, err := newPool(context.Background(), nil, 1, func(context.Context) (*exit, error) {
		n := created.Add(1)
		return fakeExit(string(rune('a'+n)), "2606:4700:"+string(rune('0'+n))+"::1"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	lease, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if lease.EgressKey() == "" {
		t.Fatal("lease has no egress key")
	}
	lease.Release()
	lease.Release() // idempotent
	if got := p.Snapshot().Standby; got != 1 {
		t.Fatalf("standby = %d, want 1", got)
	}
}

func TestPoolReportRotatesAndCoolsEgress(t *testing.T) {
	var created atomic.Int64
	p, err := newPool(context.Background(), nil, 1, func(context.Context) (*exit, error) {
		n := created.Add(1)
		return fakeExit(string(rune('a'+n)), "2606:4700:"+string(rune('0'+n))+"::1"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	firstID := p.Snapshot().Exits[0].ID
	lease, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	oldKey := lease.EgressKey()
	p.Report(lease, 429, nil)
	lease.Release()

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		snap := p.Snapshot()
		if snap.Active == 1 && len(snap.Exits) == 1 && snap.Exits[0].ID != firstID {
			if _, cooling := p.cooling[oldKey]; !cooling {
				t.Fatal("old /64 is not cooling")
			}
			if p.HasUntriedActive(map[string]bool{}) != true {
				t.Fatal("replacement should be untried")
			}
			if p.HasUntriedActive(map[string]bool{snap.Exits[0].Prefix: true}) {
				t.Fatal("tried replacement reported as untried")
			}
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("exit was not replaced: %+v", p.Snapshot())
}

func TestFailedExitRetiresAfterLeaseRelease(t *testing.T) {
	var created atomic.Int64
	var first *exit
	p, err := newPool(context.Background(), nil, 1, func(context.Context) (*exit, error) {
		n := created.Add(1)
		e := fakeExit(string(rune('a'+n)), "2606:4700:"+string(rune('0'+n))+"::1")
		if n == 1 {
			first = e
		}
		return e, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	lease, err := p.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	p.Fail(lease, "transport error")
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(p.Snapshot().Exits) == 1 && p.Snapshot().Exits[0].ID != first.id {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	first.mu.Lock()
	closedWhileLeased := first.closed
	first.mu.Unlock()
	if closedWhileLeased {
		t.Fatal("failed exit was closed while a lease was still held")
	}
	lease.Release()
	deadline = time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		first.mu.Lock()
		closed := first.closed
		first.mu.Unlock()
		if closed {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("failed exit was not closed after lease release")
}

func TestPoolDiscardsDuplicateEgress(t *testing.T) {
	var created atomic.Int64
	p, err := newPool(context.Background(), nil, 2, func(context.Context) (*exit, error) {
		n := created.Add(1)
		if n == 1 {
			return fakeExit("first", "2606:4700:1::1"), nil
		}
		// Every refill candidate lands in the first exit's /64.
		return fakeExit("dup", "2606:4700:1::2"), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	// Refill runs in the background; the duplicate must never enter the ring.
	time.Sleep(50 * time.Millisecond)
	if got := p.Snapshot().Active; got != 1 {
		t.Fatalf("active exits = %d, want 1", got)
	}
	if p.refillFailures == 0 {
		t.Fatal("duplicate registration was not counted")
	}
}

package warp

import (
	"context"
	"net/netip"
	"reflect"
	"testing"
)

func TestEndpointPreference(t *testing.T) {
	endpoints := []string{
		"[2606:4700:d0::a29f:c001]:2408",
		"162.159.192.1:2408",
		"[2606:4700:d0::a29f:c002]:2408",
		"162.159.193.1:2408",
		"engage.cloudflareclient.com:2408", // hostname, kept out by both helpers
	}
	wantIPv4 := []string{
		"162.159.192.1:2408",
		"162.159.193.1:2408",
		"[2606:4700:d0::a29f:c001]:2408",
		"[2606:4700:d0::a29f:c002]:2408",
	}
	wantIPv6 := []string{
		"[2606:4700:d0::a29f:c001]:2408",
		"[2606:4700:d0::a29f:c002]:2408",
		"162.159.192.1:2408",
		"162.159.193.1:2408",
	}
	if got := preferIPv4Endpoints(endpoints); !reflect.DeepEqual(got, wantIPv4) {
		t.Errorf("preferIPv4Endpoints = %v, want %v", got, wantIPv4)
	}
	if got := preferIPv6Endpoints(endpoints); !reflect.DeepEqual(got, wantIPv6) {
		t.Errorf("preferIPv6Endpoints = %v, want %v", got, wantIPv6)
	}
}

func TestOrderByStack(t *testing.T) {
	addrs := []netip.Addr{
		netip.MustParseAddr("1.2.3.4"),
		netip.MustParseAddr("2606:4700::1"),
		netip.MustParseAddr("1.2.3.4"),
		netip.MustParseAddr("2606:4700::2"),
	}
	cases := []struct {
		stack Stack
		want  []string
	}{
		{StackDual, []string{"2606:4700::1", "2606:4700::2", "1.2.3.4"}},
		{StackIPv6, []string{"2606:4700::1", "2606:4700::2"}},
		{StackIPv4, []string{"1.2.3.4"}},
	}
	for _, tc := range cases {
		got := orderByStack(addrs, tc.stack)
		values := make([]string, 0, len(got))
		for _, ip := range got {
			values = append(values, ip.String())
		}
		if !reflect.DeepEqual(values, tc.want) {
			t.Errorf("orderByStack(%s) = %v, want %v", tc.stack, values, tc.want)
		}
	}
}

func TestProbeTargetsPreferIPv6OnDual(t *testing.T) {
	dual := probeTargets(StackDual)
	if len(dual) < 2 || dual[0].network != "tcp6" || dual[1].network != "tcp4" {
		t.Fatalf("dual probe order = %+v, want tcp6 then tcp4", dual)
	}
	if got := probeTargets(StackIPv6); len(got) != 1 || got[0].network != "tcp6" {
		t.Fatalf("ipv6 probe targets = %+v", got)
	}
}

func TestDialAddrFailsClosedOutsideStack(t *testing.T) {
	// No network stack is attached: the family check must reject the address
	// before the tunnel is touched, so a misconfiguration never leaks a host
	// dial.
	e := &exit{stack: StackIPv6}
	if _, err := e.dialAddr(context.Background(), "tcp", netip.MustParseAddr("1.2.3.4"), "443"); err == nil {
		t.Fatal("IPv6-only stack accepted an IPv4 destination")
	}
	e = &exit{stack: StackIPv4}
	if _, err := e.dialAddr(context.Background(), "tcp", netip.MustParseAddr("2606:4700::1"), "443"); err == nil {
		t.Fatal("IPv4-only stack accepted an IPv6 destination")
	}
}

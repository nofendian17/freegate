package warp

import (
	"net/netip"
	"testing"
)

func TestCandidatePrefixRejectsUnroutableAddresses(t *testing.T) {
	cases := []struct {
		addr netip.Addr
		bits int
		ok   bool
	}{
		{netip.MustParseAddr("2606:4700:1234:5678::1"), 64, true},
		{netip.MustParseAddr("2606:4700:1234:5678::1"), 48, true},
		{netip.MustParseAddr("2606:4700:1234:5678::1"), 128, false}, // no host bits
		{netip.MustParseAddr("fe80::1"), 64, false},                 // link-local
		{netip.MustParseAddr("fdfd:e8ea:1df8::1"), 64, false},       // ULA
		{netip.MustParseAddr("2001:db8::1"), 64, false},             // documentation
		{netip.MustParseAddr("::1"), 128, false},                    // loopback
	}
	for _, tc := range cases {
		_, ok := candidatePrefix(tc.addr, tc.bits)
		if ok != tc.ok {
			t.Errorf("candidatePrefix(%s, %d) ok = %v, want %v", tc.addr, tc.bits, ok, tc.ok)
		}
	}
}

func TestCandidatePrefixNormalizesTo64(t *testing.T) {
	prefix, ok := candidatePrefix(netip.MustParseAddr("2606:4700:1234:abcd::1"), 48)
	if !ok {
		t.Fatal("global /48 prefix was rejected")
	}
	if got, want := prefix.String(), "2606:4700:1234:abcd::/64"; got != want {
		t.Fatalf("prefix = %s, want %s", got, want)
	}
}

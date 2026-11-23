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
		{netip.MustParseAddr("::192.168.1.1"), 64, false},           // IPv4-compatible
		{netip.MustParseAddr("::ffff:192.168.1.1"), 64, false},      // IPv4-mapped
	}
	for _, tc := range cases {
		_, ok := candidatePrefix(tc.addr, tc.bits)
		if ok != tc.ok {
			t.Errorf("candidatePrefix(%s, %d) ok = %v, want %v", tc.addr, tc.bits, ok, tc.ok)
		}
	}
}

func TestResolveSourcePrefixFallsBackWhenAutoDetectionFails(t *testing.T) {
	got := resolveSourcePrefix(Options{AutoSource: true}, nil, func() (netip.Prefix, error) {
		return netip.Prefix{}, ErrNoSourcePrefix
	})
	if got.IsValid() {
		t.Fatalf("fallback returned prefix %s, want none", got)
	}

	explicit := netip.MustParsePrefix("2606:4700:1234:abcd::/64")
	got = resolveSourcePrefix(Options{AutoSource: true, SourceIPv6: explicit}, nil, func() (netip.Prefix, error) {
		t.Fatal("discovery must not run when a prefix is configured")
		return netip.Prefix{}, nil
	})
	if got != explicit {
		t.Fatalf("explicit prefix = %s, want %s", got, explicit)
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

func TestValidateSourcePrefix(t *testing.T) {
	valid := []string{
		"2606:4700:1234:abcd::/64",
		"2606:4700:1234:abcd::/48",
		"2606:4700:1234:abcd::1/64",
	}
	for _, raw := range valid {
		if err := ValidateSourcePrefix(netip.MustParsePrefix(raw)); err != nil {
			t.Errorf("ValidateSourcePrefix(%s) = %v, want nil", raw, err)
		}
	}

	invalid := []string{
		"2606:4700:1234:abcd::/65", // longer than /64
		"2001:db8::/64",            // documentation
		"fd00::/64",                // ULA
		"fe80::/64",                // link-local
		"::ffff:10.0.0.0/96",       // IPv4-mapped
		"10.0.0.0/24",              // not IPv6
	}
	for _, raw := range invalid {
		if err := ValidateSourcePrefix(netip.MustParsePrefix(raw)); err == nil {
			t.Errorf("ValidateSourcePrefix(%s) accepted an unroutable prefix", raw)
		}
	}
}

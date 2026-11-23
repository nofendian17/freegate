package egress

import (
	"strings"
	"testing"
)

const publicPrefix = "2606:4700:1234:5678::/64"

func TestParseConfig(t *testing.T) {
	for _, stack := range []string{"ipv6", "dual"} {
		opts, err := ParseConfig(stack, 2, publicPrefix)
		if err != nil {
			t.Fatalf("ParseConfig(%q): %v", stack, err)
		}
		if opts.Size != 2 || string(opts.Stack) != stack || opts.SourceIPv6.String() != publicPrefix {
			t.Fatalf("options = %+v", opts)
		}
	}

	cases := []struct {
		stack  string
		size   int
		source string
	}{
		{"bogus", 2, publicPrefix},
		{"ipv6", 0, publicPrefix},
		{"ipv6", 33, publicPrefix},
		{"ipv6", 2, "10.0.0.0/24"},
		{"ipv6", 2, "2606:4700:1234:5678::/65"},
		// Cloudflare's IPv4 egress is shared and cannot be rotated.
		{"ipv4", 2, publicPrefix},
		// Non-public prefixes cannot be routed sources; auto-detection
		// rejects them, and so must explicit configuration.
		{"ipv6", 2, "2001:db8::/64"},
		{"ipv6", 2, "fd00::/64"},
		{"ipv6", 2, "fe80::/64"},
	}
	for _, tc := range cases {
		if _, err := ParseConfig(tc.stack, tc.size, tc.source); err == nil {
			t.Errorf("ParseConfig(%q, %d, %q) accepted invalid config", tc.stack, tc.size, tc.source)
		}
	}
}

func TestParseConfigAutoSource(t *testing.T) {
	for _, raw := range []string{"", "auto", " AUTO "} {
		opts, err := ParseConfig("dual", 2, raw)
		if err != nil {
			t.Fatalf("ParseConfig(%q): %v", raw, err)
		}
		if !opts.AutoSource || opts.SourceIPv6.IsValid() {
			t.Fatalf("ParseConfig(%q) = %+v, want auto source", raw, opts)
		}
	}
}

func TestParseConfigMasksPrefix(t *testing.T) {
	opts, err := ParseConfig("", 1, "2606:4700:1234:5678::1/64")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(opts.SourceIPv6.String(), "::/64") {
		t.Fatalf("prefix was not masked: %s", opts.SourceIPv6)
	}
}

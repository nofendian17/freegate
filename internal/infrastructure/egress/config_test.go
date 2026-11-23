package egress

import (
	"strings"
	"testing"
)

func TestParseConfig(t *testing.T) {
	opts, err := ParseConfig("ipv6", 2, "2001:db8:1234:5678::/64")
	if err != nil {
		t.Fatal(err)
	}
	if opts.Size != 2 || opts.Stack != "ipv6" || opts.SourceIPv6.String() != "2001:db8:1234:5678::/64" {
		t.Fatalf("options = %+v", opts)
	}

	cases := []struct {
		stack  string
		size   int
		source string
	}{
		{"bogus", 2, "2001:db8::/64"},
		{"ipv6", 0, "2001:db8::/64"},
		{"ipv6", 33, "2001:db8::/64"},
		{"ipv6", 2, ""},
		{"ipv6", 2, "10.0.0.0/24"},
		{"ipv6", 2, "2001:db8::/65"},
		{"ipv4", 2, "2001:db8::/64"},
	}
	for _, tc := range cases {
		if _, err := ParseConfig(tc.stack, tc.size, tc.source); err == nil {
			t.Errorf("ParseConfig(%q, %d, %q) accepted invalid config", tc.stack, tc.size, tc.source)
		}
	}
}

func TestParseConfigMasksPrefix(t *testing.T) {
	opts, err := ParseConfig("", 1, "2001:db8:1234:5678::1/64")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(opts.SourceIPv6.String(), "::/64") {
		t.Fatalf("prefix was not masked: %s", opts.SourceIPv6)
	}
}

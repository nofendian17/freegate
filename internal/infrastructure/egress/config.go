package egress

import (
	"fmt"
	"net/netip"
	"strings"

	"freegate/internal/warp"
)

// ParseConfig validates WARP pool settings and returns the egress options.
// An empty source or "auto" discovers a routed public IPv6 prefix from the
// host; an explicit prefix always wins. Without a source, every replacement
// exit would leave from the same host address and land in the same quota /64.
func ParseConfig(stack string, size int, source string) (Options, error) {
	parsedStack, err := warp.ParseStack(stack)
	if err != nil {
		return Options{}, fmt.Errorf("warp stack: %w", err)
	}
	if size < 1 || size > 32 {
		return Options{}, fmt.Errorf("warp pool size must be between 1 and 32, got %d", size)
	}
	// An IPv4-only stack egresses through Cloudflare's shared public IPv4,
	// so its free quota cannot be rotated and is usually already spent.
	// IPv6 and dual are supported; dual reaches A-only hosts while keeping
	// IPv6 upstreams on the exit's own /64.
	if parsedStack == warp.StackIPv4 {
		return Options{}, fmt.Errorf("warp stack %q is not supported; use ipv6 or dual", parsedStack)
	}
	raw := strings.ToLower(strings.TrimSpace(source))
	if raw == "" || raw == "auto" {
		return Options{Size: size, Stack: parsedStack, AutoSource: true}, nil
	}
	prefix, err := netip.ParsePrefix(raw)
	if err != nil {
		return Options{}, fmt.Errorf("warp source_ipv6: %q is not an IPv6 prefix", source)
	}
	prefix = prefix.Masked()
	if err := warp.ValidateSourcePrefix(prefix); err != nil {
		return Options{}, fmt.Errorf("warp source_ipv6: %w", err)
	}
	return Options{Size: size, Stack: parsedStack, SourceIPv6: prefix}, nil
}

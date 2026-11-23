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
	if parsedStack != warp.StackIPv6 {
		return Options{}, fmt.Errorf("warp source rotation requires an ipv6 stack, got %q", parsedStack)
	}
	raw := strings.ToLower(strings.TrimSpace(source))
	if raw == "" || raw == "auto" {
		return Options{Size: size, Stack: parsedStack, AutoSource: true}, nil
	}
	prefix, err := netip.ParsePrefix(raw)
	if err != nil {
		return Options{}, fmt.Errorf("warp source_ipv6: %q is not an IPv6 prefix", source)
	}
	if !prefix.Addr().Is6() || prefix.Bits() > 64 {
		return Options{}, fmt.Errorf("warp source_ipv6: %s must be an IPv6 /64 or shorter", prefix)
	}
	return Options{Size: size, Stack: parsedStack, SourceIPv6: prefix.Masked()}, nil
}

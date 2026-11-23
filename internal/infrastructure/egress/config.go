package egress

import (
	"fmt"
	"net/netip"
	"strings"

	"freegate/internal/warp"
)

// ParseConfig validates WARP pool settings and returns the egress options.
// SourceIPv6 is mandatory: without a routed prefix every replacement exit
// would leave from the same host address and land in the same quota /64.
func ParseConfig(stack string, size int, source string) (Options, error) {
	parsedStack, err := warp.ParseStack(stack)
	if err != nil {
		return Options{}, fmt.Errorf("warp stack: %w", err)
	}
	if size < 1 || size > 32 {
		return Options{}, fmt.Errorf("warp pool size must be between 1 and 32, got %d", size)
	}
	prefix, err := netip.ParsePrefix(strings.TrimSpace(source))
	if err != nil {
		return Options{}, fmt.Errorf("warp source_ipv6: %q is not an IPv6 prefix", source)
	}
	if !prefix.Addr().Is6() || prefix.Bits() > 64 {
		return Options{}, fmt.Errorf("warp source_ipv6: %s must be an IPv6 /64 or shorter", prefix)
	}
	if parsedStack != warp.StackIPv6 {
		return Options{}, fmt.Errorf("warp source_ipv6 requires an ipv6 stack, got %q", parsedStack)
	}
	return Options{Size: size, Stack: parsedStack, SourceIPv6: prefix.Masked()}, nil
}

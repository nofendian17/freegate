//go:build linux

package supervisor

import "strings"

// tunGateEnabled reports whether new-tunnel-interface detection is
// meaningful on this platform.
func tunGateEnabled() bool { return true }

// isTunName reports whether an interface name is a TUN/TAP device.
func isTunName(name string) bool {
	return strings.HasPrefix(name, "tun") || strings.HasPrefix(name, "tap")
}

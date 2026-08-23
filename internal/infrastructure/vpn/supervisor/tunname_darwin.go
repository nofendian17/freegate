//go:build darwin

package supervisor

import "strings"

// tunGateEnabled reports whether new-tunnel-interface detection is
// meaningful on this platform.
func tunGateEnabled() bool { return true }

// isTunName reports whether an interface name is a tunnel device. macOS
// assigns utunN dynamically, so the name cannot be pinned; detection
// compares against a pre-start snapshot instead of fixed names (system
// utun0..utun3 always exist and must not count).
func isTunName(name string) bool {
	return strings.HasPrefix(name, "utun") || strings.HasPrefix(name, "tun")
}

//go:build windows

package supervisor

// tunGateEnabled reports whether new-tunnel-interface detection is
// meaningful on this platform. The wintun/TAP adapter name is not
// predictable ("Ethernet 2"-style), so the interface gate is skipped and
// tunnel-up relies on process liveness plus the egress IP probe.
func tunGateEnabled() bool { return false }

func isTunName(string) bool { return false }

// Package supervisor owns the VPN tunnel lifecycle shared by both
// deployment modes: the cmd/vpngate-supervisor sidecar binary and the
// in-process provider used by single-binary builds (internal/
// infrastructure/vpn).
//
// It keeps an OpenVPN tunnel to a VPNGate relay server up (reconnect
// loop), exposes SOCKS5 through that tunnel, tracks the measured egress
// IP, and offers weighted-random server selection plus explicit
// connect-to-hostname. The HTTP control API lives in the sidecar's main;
// this package is transport-agnostic.
//
// Security note: VPNGate servers are community-run and NOT trusted.
// Upstream calls are HTTPS end-to-end so API keys stay protected, but the
// relay sees destination metadata. This provides IP rotation, not
// anonymity.
package supervisor

import "time"

// Config wires the supervisor's filters and refresh behavior.
type Config struct {
	SocksAddr string
	Country   string
	MinScore  int
	MaxPing   int
	// RefreshInt bounds how often the VPNGate server list is re-fetched.
	RefreshInt time.Duration
}

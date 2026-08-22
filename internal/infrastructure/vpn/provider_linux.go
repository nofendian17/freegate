//go:build linux

package vpn

// linuxTunDevice is the TUN interface name used by OpenVPN on Linux.
const linuxTunDevice = "tun0"

// linuxOpenVPNCandidates overrides the default candidate list for Linux if needed.
// Currently the generic openVPNCandidatesForOS handles it; this file exists
// to satisfy per-OS build separation and to host Linux-specific tun checks.
func linuxPreflightCheck() error { return nil }

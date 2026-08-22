//go:build windows

package vpn

// windowsTunService is the Windows service name for TAP.
const windowsTunService = "OpenVPNService"

func windowsPreflightCheck() error { return nil }

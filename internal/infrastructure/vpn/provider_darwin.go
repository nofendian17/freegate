//go:build darwin

package vpn

// darwinTunDevices are the possible TUN devices on macOS (utun).
var darwinTunDevices = []string{"tun0", "tun1", "tun2", "utun0", "utun1"}

func darwinPreflightCheck() error { return nil }

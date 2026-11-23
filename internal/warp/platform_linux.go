//go:build linux

package warp

// sourceBindingSupported reports whether this OS can bind an unassigned
// address from a routed prefix (Linux IPV6_FREEBIND). Only Linux supports the
// outer-source rotation used to get a fresh WARP quota /64.
func sourceBindingSupported() bool { return true }

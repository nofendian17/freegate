//go:build !linux

package warp

// macOS and Windows have no IPV6_FREEBIND equivalent: a socket cannot bind a
// random address that is not assigned to an interface. WARP still works over
// IPv4 endpoints there, but rotation cannot change the outer source.
func sourceBindingSupported() bool { return false }

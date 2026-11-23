//go:build !linux

package warp

import "syscall"

// WARP source binding relies on Linux IPV6_FREEBIND. On other platforms the
// caller can still build an in-process pool without SourceIPv6; a configured
// source fails closed at bind time instead of leaking the host address.
func freebindControl(_, _ string, _ syscall.RawConn) error { return nil }

package egress

import (
	"context"
	"net"
)

// NewTestLease builds a Lease from callbacks. Consumers of this package use
// it to exercise dial/rotation/retry paths without registering a WARP device.
func NewTestLease(dial func(context.Context, string, string) (net.Conn, error), release func(), fail func(string)) *Lease {
	return &Lease{dial: dial, release: release, fail: fail, egressKey: func() string { return "test" }}
}

type testBackend struct{}

func (testBackend) Acquire(context.Context) (*Lease, error) {
	return NewTestLease(func(context.Context, string, string) (net.Conn, error) {
		return nil, ErrNoEndpoint
	}, func() {}, func(string) {}), nil
}

func (testBackend) Snapshot() []Snapshot {
	return []Snapshot{{ID: "test", Egress: "2001:db8::1", Prefix: "2001:db8::/64", Stack: "ipv6", Status: "ready"}}
}

func (testBackend) Close() {}

// NewTestPool returns a Pool with a no-network backend. It is used by tests of
// consumers that need a live-looking pool without registering a device.
func NewTestPool() *Pool { return newPoolFromBackend(testBackend{}, false) }

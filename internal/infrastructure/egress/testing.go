package egress

import (
	"context"
	"net"
)

// NewTestLease builds a Lease from callbacks. Consumers of this package use
// it to exercise dial/rotation/retry paths without registering a WARP device.
// The fail callback returns a channel that closes when rotation settles; return
// a closed channel for an immediate return.
func NewTestLease(dial func(context.Context, string, string) (net.Conn, error), release func(), fail func(string) <-chan struct{}) *Lease {
	return &Lease{dial: dial, release: release, fail: fail}
}

// ClosedSignal is a channel that is already closed, for callbacks that finish
// synchronously.
func ClosedSignal() <-chan struct{} {
	done := make(chan struct{})
	close(done)
	return done
}

type testBackend struct{}

func (testBackend) Acquire(context.Context) (*Lease, error) {
	return NewTestLease(func(context.Context, string, string) (net.Conn, error) {
		return nil, ErrNoEndpoint
	}, func() {}, func(string) <-chan struct{} { return ClosedSignal() }), nil
}

func (testBackend) Snapshot() []Snapshot {
	return []Snapshot{{ID: "test", Egress: "2001:db8::1", Prefix: "2001:db8::/64", Stack: "ipv6", Status: "ready"}}
}

func (testBackend) Close() {}

// NewTestPool returns a Pool with a no-network backend. It is used by tests of
// consumers that need a live-looking pool without registering a device.
func NewTestPool() *Pool { return newPoolFromBackend(testBackend{}, false) }

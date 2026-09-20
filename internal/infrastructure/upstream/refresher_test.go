package upstream

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"
)

func TestRefresher_OnFailureCalledOnError(t *testing.T) {
	var calls atomic.Int32
	var hooks atomic.Int32
	r := &Refresher{
		name:     "test",
		refresh:  func(ctx context.Context) error { calls.Add(1); return errors.New("boom") },
		interval: time.Hour,
		backoff:  time.Millisecond,
		maxBack:  time.Millisecond,
	}
	r.WithOnFailure(func() { hooks.Add(1) })

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	r.Run(ctx)

	if calls.Load() == 0 {
		t.Fatal("refresh was never attempted")
	}
	if hooks.Load() != calls.Load() {
		t.Fatalf("onFailure called %d times for %d failures", hooks.Load(), calls.Load())
	}
}

func TestRefresher_NoOnFailureOnSuccess(t *testing.T) {
	var hooks atomic.Int32
	r := NewRefresher("test", func(ctx context.Context) error { return nil }, time.Hour).
		WithOnFailure(func() { hooks.Add(1) })

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	r.Run(ctx)

	if hooks.Load() != 0 {
		t.Fatalf("onFailure called %d times despite no failures", hooks.Load())
	}
}

func TestHTTPClient_CloseIdleConnectionsNilSafe(t *testing.T) {
	var c *HTTPClient
	c.CloseIdleConnections() // must not panic

	c = &HTTPClient{}
	c.CloseIdleConnections() // nil inner client, must not panic

	c = NewHTTPClientWithTransport("http://example.com", nil, nil, nil)
	c.CloseIdleConnections() // real transport, must not panic
}

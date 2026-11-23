package egress

import (
	"context"
	"errors"
	"testing"

	"freegate/internal/warp"
)

func fakeFactory(ctx context.Context, opts Options) (*Pool, error) {
	return newPoolFromBackend(newFakeBackend(), false), nil
}

func TestRouterAppliesAndPreservesPools(t *testing.T) {
	r := NewRouter()
	r.newPool = fakeFactory
	cfg := PoolConfig{ID: 1, Name: "one", Stack: warp.StackIPv6, Size: 2}
	if err := r.Apply(context.Background(), []PoolConfig{cfg}); err != nil {
		t.Fatal(err)
	}
	first := r.Pool(1)
	if first == nil {
		t.Fatal("pool was not installed")
	}
	if err := r.Apply(context.Background(), []PoolConfig{cfg}); err != nil {
		t.Fatal(err)
	}
	if r.Pool(1) != first {
		t.Fatal("unchanged config rebuilt a live pool")
	}
	cfg.Size = 3
	if err := r.Apply(context.Background(), []PoolConfig{cfg}); err != nil {
		t.Fatal(err)
	}
	if r.Pool(1) == first {
		t.Fatal("changed config kept the old pool")
	}
	if got := r.Snapshot(); len(got) != 1 || got[0].ID != 1 {
		t.Fatalf("snapshot = %+v", got)
	}
}

func TestRouterApplyFailureKeepsPreviousPools(t *testing.T) {
	r := NewRouter()
	r.newPool = fakeFactory
	if err := r.Apply(context.Background(), []PoolConfig{{ID: 1, Name: "one"}}); err != nil {
		t.Fatal(err)
	}
	first := r.Pool(1)
	wantErr := errors.New("registration failed")
	r.newPool = func(context.Context, Options) (*Pool, error) { return nil, wantErr }
	if err := r.Apply(context.Background(), []PoolConfig{{ID: 2, Name: "two"}}); !errors.Is(err, wantErr) {
		t.Fatalf("Apply error = %v", err)
	}
	if r.Pool(1) != first || r.Pool(2) != nil {
		t.Fatal("failed Apply changed the router state")
	}
}

func TestNilRouterIsSafe(t *testing.T) {
	var r *Router
	r.Apply(context.Background(), nil)
	if r.Pool(1) != nil || r.Default() != nil || r.Snapshot() != nil {
		t.Fatal("nil router reported state")
	}
}

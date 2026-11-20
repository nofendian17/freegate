package egress

import (
	"testing"
)

func TestRouterResolvesAndPreservesPools(t *testing.T) {
	r := NewRouter()
	eps := []Endpoint{{Name: "warp-1", Socks: "warp-1:1080"}}
	r.Apply([]PoolConfig{{ID: 1, Name: "one", Endpoints: eps}})

	first := r.Pool(1)
	if first == nil {
		t.Fatal("pool 1 not resolved")
	}
	// Same configuration keeps the live pool (rotation state, cooldowns).
	r.Apply([]PoolConfig{{ID: 1, Name: "one", Endpoints: eps}})
	if r.Pool(1) != first {
		t.Fatal("unchanged config must preserve the live pool")
	}
	// Changed endpoints build a new pool.
	r.Apply([]PoolConfig{{ID: 1, Name: "one", Endpoints: []Endpoint{{Name: "warp-2", Socks: "warp-2:1080"}}}})
	if r.Pool(1) == first {
		t.Fatal("changed config must rebuild the pool")
	}
	// Unknown and default IDs fail closed.
	if r.Pool(99) != nil {
		t.Fatal("unknown pool must be nil")
	}
	if r.Pool(0) != nil {
		t.Fatal("default pool must be nil until set")
	}
}

func TestRouterDefaultAndNilSafety(t *testing.T) {
	var nilRouter *Router
	if nilRouter.Pool(1) != nil || nilRouter.Default() != nil || nilRouter.Snapshot() != nil {
		t.Fatal("nil router must be safe and empty")
	}
	nilRouter.Apply(nil) // must not panic

	r := NewRouter()
	pool := NewPool(Options{Endpoints: []Endpoint{{Name: "warp-1", Socks: "warp-1:1080"}}})
	r.SetDefault(pool)
	if r.Pool(0) != pool || r.Default() != pool {
		t.Fatal("default pool not resolved")
	}
	// ID 0 is reserved; a persisted pool with ID 0 is ignored.
	r.Apply([]PoolConfig{{ID: 0, Name: "ignored", Endpoints: []Endpoint{{Name: "x", Socks: "x:1"}}}})
	if r.Pool(0) != pool {
		t.Fatal("Apply must not overwrite the env default")
	}
	if snap := r.Snapshot(); len(snap) != 1 || !snap[0].Default {
		t.Fatalf("snapshot = %+v", snap)
	}
}

func TestRouterDropsRemovedPools(t *testing.T) {
	r := NewRouter()
	r.Apply([]PoolConfig{{ID: 1, Name: "one", Endpoints: []Endpoint{{Name: "warp-1", Socks: "warp-1:1080"}}}})
	r.Apply(nil)
	if r.Pool(1) != nil {
		t.Fatal("removed pool must fail closed")
	}
}

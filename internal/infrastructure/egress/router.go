package egress

import (
	"context"
	"slices"
	"sync"
	"sync/atomic"
	"time"
)

// PoolConfig describes one persisted WARP pool. ID 0 is reserved for the
// env-configured default pool.
type PoolConfig struct {
	ID             uint
	Name           string
	Endpoints      []Endpoint
	FallbackDirect bool
	DialTimeout    time.Duration
	RotateTimeout  time.Duration
	DrainTimeout   time.Duration
	FailureCool    time.Duration
}

func (c PoolConfig) options() Options {
	return Options{
		Endpoints:      c.Endpoints,
		FallbackDirect: c.FallbackDirect,
		DialTimeout:    c.DialTimeout,
		RotateTimeout:  c.RotateTimeout,
		DrainTimeout:   c.DrainTimeout,
		FailureCool:    c.FailureCool,
	}
}

type poolEntry struct {
	cfg  PoolConfig
	pool *Pool
}

// Router resolves a WARP pool ID to its current pool and supports hot reload.
// Transports hold a Router and look the pool up at dial time, so editing a
// pool in the admin UI takes effect without rebuilding every upstream.
//
// The zero value is usable and fails closed: a nil router, or a missing
// pool, resolves to a nil *Pool, which never dials directly.
type Router struct {
	mu          sync.Mutex
	pools       atomic.Pointer[map[uint]*poolEntry]
	defaultPool atomic.Pointer[Pool]
}

// NewRouter returns an empty router. Pool 0 is unset until SetDefault.
func NewRouter() *Router {
	r := &Router{}
	empty := map[uint]*poolEntry{}
	r.pools.Store(&empty)
	return r
}

// SetDefault installs the env-configured pool used when a provider selects
// WARP without pinning a pool. Nil disables the default.
func (r *Router) SetDefault(p *Pool) {
	if r == nil {
		return
	}
	r.defaultPool.Store(p)
}

// Default returns the env-configured pool, or nil.
func (r *Router) Default() *Pool {
	if r == nil {
		return nil
	}
	return r.defaultPool.Load()
}

// Pool resolves id to its pool. ID 0 returns the default pool. Unknown IDs
// return nil, so callers fail closed.
func (r *Router) Pool(id uint) *Pool {
	if r == nil {
		return nil
	}
	if id == 0 {
		return r.Default()
	}
	cur := r.pools.Load()
	if cur == nil {
		return nil
	}
	entry := (*cur)[id]
	if entry == nil {
		return nil
	}
	return entry.pool
}

// Apply replaces the persisted pool set, preserving live pool instances whose
// configuration did not change so endpoint rotation state and cooldowns
// survive an admin rebuild.
func (r *Router) Apply(configs []PoolConfig) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.pools.Load()
	next := make(map[uint]*poolEntry, len(configs))
	for _, cfg := range configs {
		if cfg.ID == 0 {
			continue
		}
		if prev := (*old)[cfg.ID]; prev != nil && sameConfig(prev.cfg, cfg) {
			next[cfg.ID] = prev
			continue
		}
		next[cfg.ID] = &poolEntry{cfg: cfg, pool: NewPool(cfg.options())}
	}
	r.pools.Store(&next)
}

func sameConfig(a, b PoolConfig) bool {
	if a.Name != b.Name ||
		a.FallbackDirect != b.FallbackDirect ||
		a.DialTimeout != b.DialTimeout ||
		a.RotateTimeout != b.RotateTimeout ||
		a.DrainTimeout != b.DrainTimeout ||
		a.FailureCool != b.FailureCool ||
		len(a.Endpoints) != len(b.Endpoints) {
		return false
	}
	for i := range a.Endpoints {
		if a.Endpoints[i] != b.Endpoints[i] {
			return false
		}
	}
	return true
}

// PoolStatus is a point-in-time view of one configured pool.
type PoolStatus struct {
	ID        uint       `json:"id"`
	Name      string     `json:"name"`
	Default   bool       `json:"default"`
	Endpoints []Snapshot `json:"endpoints"`
}

// Snapshot returns the default pool and every persisted pool, sorted by ID.
func (r *Router) Snapshot() []PoolStatus {
	if r == nil {
		return nil
	}
	var out []PoolStatus
	// Refresh exit addresses on demand so prefix collisions are visible even
	// for endpoints that never triggered a rotation through this process.
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if p := r.Default(); p != nil {
		p.probeIPs(ctx)
		out = append(out, PoolStatus{ID: 0, Name: "default (env)", Default: true, Endpoints: p.Snapshot()})
	}
	cur := r.pools.Load()
	if cur == nil {
		return out
	}
	ids := make([]uint, 0, len(*cur))
	for id := range *cur {
		ids = append(ids, id)
	}
	sortUints(ids)
	for _, id := range ids {
		entry := (*cur)[id]
		if entry == nil {
			continue
		}
		entry.pool.probeIPs(ctx)
		out = append(out, PoolStatus{ID: id, Name: entry.cfg.Name, Endpoints: entry.pool.Snapshot()})
	}
	return out
}

func sortUints(in []uint) { slices.Sort(in) }

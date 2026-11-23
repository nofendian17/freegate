package egress

import (
	"context"
	"net/netip"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"freegate/internal/warp"
)

// PoolConfig describes one persisted WARP pool. ID 0 is reserved for the
// env-configured default pool.
type PoolConfig struct {
	ID                  uint
	Name                string
	Stack               warp.Stack
	Size                int
	SourceIPv6          netip.Prefix
	FallbackDirect      bool
	RegistrationTimeout time.Duration
}

func (c PoolConfig) options() Options {
	return Options{
		Size:                c.Size,
		Stack:               c.Stack,
		SourceIPv6:          c.SourceIPv6,
		FallbackDirect:      c.FallbackDirect,
		RegistrationTimeout: c.RegistrationTimeout,
	}
}

type poolEntry struct {
	cfg  PoolConfig
	pool *Pool
}

// Router resolves a WARP pool ID to its current pool and supports hot reload.
// Transports hold a Router and look the pool up at dial time, so editing a pool
// in the admin UI takes effect without rebuilding every upstream.
//
// The zero value is usable and fails closed: a nil router, or a missing pool,
// resolves to a nil *Pool, which never dials directly.
type Router struct {
	mu          sync.Mutex
	pools       atomic.Pointer[map[uint]*poolEntry]
	defaultPool atomic.Pointer[Pool]
	// newPool is overridden by tests so applying a config never registers a
	// real Cloudflare device.
	newPool func(context.Context, Options) (*Pool, error)
}

// SetPoolFactory overrides pool construction. Tests use it to avoid
// registering real Cloudflare devices when applying persisted pools.
func (r *Router) SetPoolFactory(f func(context.Context, Options) (*Pool, error)) {
	if r != nil {
		r.newPool = f
	}
}

// NewRouter returns an empty router. Pool 0 is unset until SetDefault.
func NewRouter() *Router {
	r := &Router{newPool: func(ctx context.Context, opts Options) (*Pool, error) {
		return NewPool(ctx, nil, opts)
	}}
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

// Apply replaces the persisted pool set. Unchanged pools keep their live
// instances so exit identity and cooldowns survive an admin rebuild. A failed
// registration aborts the whole swap and closes every pool built in this pass.
func (r *Router) Apply(ctx context.Context, configs []PoolConfig) error {
	if r == nil {
		return nil
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.pools.Load()
	next := make(map[uint]*poolEntry, len(configs))
	built := make([]*Pool, 0, len(configs))
	for _, cfg := range configs {
		if cfg.ID == 0 {
			continue
		}
		if prev := (*old)[cfg.ID]; prev != nil && sameConfig(prev.cfg, cfg) {
			next[cfg.ID] = prev
			continue
		}
		factory := r.newPool
		if factory == nil {
			factory = func(ctx context.Context, opts Options) (*Pool, error) {
				return NewPool(ctx, nil, opts)
			}
		}
		p, err := factory(ctx, cfg.options())
		if err != nil {
			for _, item := range built {
				item.Close()
			}
			return err
		}
		built = append(built, p)
		next[cfg.ID] = &poolEntry{cfg: cfg, pool: p}
	}
	// Closing an old pool only retires its exits: a tunnel with a live lease
	// stays up until the response body is closed, so an in-flight stream is
	// not cut off by an admin rebuild.
	reused := make(map[*Pool]bool, len(next))
	for _, entry := range next {
		reused[entry.pool] = true
	}
	if old != nil {
		for _, entry := range *old {
			if !reused[entry.pool] {
				entry.pool.Close()
			}
		}
	}
	r.pools.Store(&next)
	return nil
}

// Close retires the default pool and every persisted pool. Safe to call more
// than once; it is used during server shutdown.
func (r *Router) Close() {
	if r == nil {
		return
	}
	r.mu.Lock()
	cur := r.pools.Load()
	pools := make([]*Pool, 0, 1)
	if cur != nil {
		for _, entry := range *cur {
			pools = append(pools, entry.pool)
		}
	}
	r.mu.Unlock()
	for _, p := range pools {
		p.Close()
	}
	if def := r.Default(); def != nil {
		def.Close()
	}
}

func sameConfig(a, b PoolConfig) bool {
	return a.Name == b.Name &&
		a.Stack == b.Stack &&
		a.Size == b.Size &&
		a.SourceIPv6 == b.SourceIPv6 &&
		a.FallbackDirect == b.FallbackDirect &&
		a.RegistrationTimeout == b.RegistrationTimeout
}

// PoolStatus is a point-in-time view of one configured pool.
type PoolStatus struct {
	ID      uint       `json:"id"`
	Name    string     `json:"name"`
	Default bool       `json:"default"`
	Exits   []Snapshot `json:"exits"`
}

// Snapshot returns the default pool and every persisted pool, sorted by ID.
func (r *Router) Snapshot() []PoolStatus {
	if r == nil {
		return nil
	}
	var out []PoolStatus
	if p := r.Default(); p != nil {
		out = append(out, PoolStatus{ID: 0, Name: "default (env)", Default: true, Exits: p.Snapshot()})
	}
	cur := r.pools.Load()
	if cur == nil {
		return out
	}
	ids := make([]uint, 0, len(*cur))
	for id := range *cur {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	for _, id := range ids {
		entry := (*cur)[id]
		if entry == nil {
			continue
		}
		out = append(out, PoolStatus{ID: id, Name: entry.cfg.Name, Exits: entry.pool.Snapshot()})
	}
	return out
}

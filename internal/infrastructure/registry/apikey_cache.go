package registry

import (
	"sync"
	"time"
)

const clientKeyVerifyTTL = 30 * time.Second

// clientKeyVerifyMaxEntries caps the cache so negative lookups under a
// key-scan attack cannot grow memory unboundedly.
const clientKeyVerifyMaxEntries = 10000

type clientKeyVerifyEntry struct {
	valid     bool
	expiresAt time.Time
}

// clientKeyVerifyCache caches VerifyClientKey results. A lookup that misses
// runs a SQLite query and then stores the result — and an admin revoke can
// land in that gap, so every invalidation bumps a generation counter: a
// set() carrying a generation that changed while the query ran is dropped
// instead of resurrecting a revoked key for up to the TTL.
type clientKeyVerifyCache struct {
	mu         sync.RWMutex
	entries    map[string]clientKeyVerifyEntry
	generation uint64
}

func newClientKeyVerifyCache() *clientKeyVerifyCache {
	return &clientKeyVerifyCache{entries: make(map[string]clientKeyVerifyEntry)}
}

func (c *clientKeyVerifyCache) get(hash string) (bool, bool) {
	if c == nil {
		return false, false
	}
	now := time.Now()
	c.mu.RLock()
	e, ok := c.entries[hash]
	c.mu.RUnlock()
	if !ok {
		return false, false
	}
	if now.After(e.expiresAt) {
		// Lazily evict the expired entry so the map cannot fill with
		// dead negative entries under key-scan traffic.
		c.mu.Lock()
		if cur, ok := c.entries[hash]; ok && now.After(cur.expiresAt) {
			delete(c.entries, hash)
		}
		c.mu.Unlock()
		return false, false
	}
	return e.valid, true
}

func (c *clientKeyVerifyCache) set(hash string, valid bool, gen uint64) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if gen != c.generation {
		// An invalidate ran while the DB query was in flight; this result
		// may predate a revoke and must not be cached.
		return
	}
	if c.entries == nil {
		c.entries = make(map[string]clientKeyVerifyEntry)
	}
	if len(c.entries) >= clientKeyVerifyMaxEntries {
		// Evict an expired entry first; fall back to a random one when
		// everything is still live to keep memory bounded.
		now := time.Now()
		evicted := false
		for k, e := range c.entries {
			if now.After(e.expiresAt) {
				delete(c.entries, k)
				evicted = true
				break
			}
		}
		if !evicted {
			for k := range c.entries {
				delete(c.entries, k)
				break
			}
		}
	}
	c.entries[hash] = clientKeyVerifyEntry{valid: valid, expiresAt: time.Now().Add(clientKeyVerifyTTL)}
}

// currentGeneration snapshots the invalidation generation before a slow
// verify query starts; pass it to set so a concurrent revoke drops the
// would-be cache entry.
func (c *clientKeyVerifyCache) currentGeneration() uint64 {
	if c == nil {
		return 0
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.generation
}

func (c *clientKeyVerifyCache) invalidate(hash string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.entries, hash)
	c.generation++
	c.mu.Unlock()
}

func (c *clientKeyVerifyCache) invalidateAll() {
	if c == nil {
		return
	}
	c.mu.Lock()
	clear(c.entries)
	c.generation++
	c.mu.Unlock()
}

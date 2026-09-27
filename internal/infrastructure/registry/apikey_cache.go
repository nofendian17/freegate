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

type clientKeyVerifyCache struct {
	mu      sync.RWMutex
	entries map[string]clientKeyVerifyEntry
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

func (c *clientKeyVerifyCache) set(hash string, valid bool) {
	if c == nil {
		return
	}
	c.mu.Lock()
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
	c.mu.Unlock()
}

func (c *clientKeyVerifyCache) invalidate(hash string) {
	if c == nil {
		return
	}
	c.mu.Lock()
	delete(c.entries, hash)
	c.mu.Unlock()
}

func (c *clientKeyVerifyCache) invalidateAll() {
	if c == nil {
		return
	}
	c.mu.Lock()
	clear(c.entries)
	c.mu.Unlock()
}

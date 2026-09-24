package registry

import (
	"sync"
	"time"
)

const clientKeyVerifyTTL = 30 * time.Second

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
	if !ok || now.After(e.expiresAt) {
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

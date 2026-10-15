package registry

import (
	"testing"
	"time"
)

func TestVerifyClientKey_CachesResults(t *testing.T) {
	s, err := Open(t.Context(), t.TempDir()+"/providers.db")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	_, raw, err := s.CreateClientKey(t.Context(), "cached-client")
	if err != nil {
		t.Fatal(err)
	}
	if ok := verifyClientKey(t, s, raw); !ok {
		t.Fatal("freshly created key must verify")
	}
	hash := hashClientKey(raw)
	if _, ok := s.verifyCache.get(hash); !ok {
		t.Fatal("verify result must be cached")
	}
	if _, err := s.UpdateClientKey(t.Context(), 1, "cached-client", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.verifyCache.get(hash); ok {
		t.Fatal("update must invalidate the verify cache")
	}
	if verifyClientKey(t, s, raw) {
		t.Fatal("disabled key must not verify")
	}
}

func TestClientKeyVerifyCache_StaleSetAfterInvalidateDropped(t *testing.T) {
	c := newClientKeyVerifyCache()
	// A verify that read the generation before a concurrent revoke...
	gen := c.currentGeneration()
	c.invalidateAll()
	// ...must not repopulate the cache with a possibly-revoked verdict.
	c.set("h", true, gen)
	if _, ok := c.get("h"); ok {
		t.Fatal("set from a pre-invalidate generation must be dropped")
	}
	// A fresh generation does store.
	c.set("h", true, c.currentGeneration())
	if valid, ok := c.get("h"); !ok || !valid {
		t.Fatal("set from the current generation must be stored")
	}
	// Single-hash invalidate bumps the generation too: sets that raced it
	// are dropped conservatively.
	gen = c.currentGeneration()
	c.invalidate("h")
	c.set("other", true, gen)
	if _, ok := c.get("other"); ok {
		t.Fatal("set from a pre-invalidate generation must be dropped after invalidate()")
	}
}

func TestClientKeyVerifyCache_Expires(t *testing.T) {
	c := newClientKeyVerifyCache()
	c.mu.Lock()
	c.entries["h"] = clientKeyVerifyEntry{valid: true, expiresAt: time.Now().Add(-time.Second)}
	c.mu.Unlock()
	if _, ok := c.get("h"); ok {
		t.Fatal("expired entry must miss")
	}
}

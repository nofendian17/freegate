package upstream

import (
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"

	"freegate/internal/infrastructure/providers"
)

// ResolveRelayPools maps a proxy setting to a pool list for SetRelayPools:
// nil follows the global rotation, an empty slice means direct, otherwise
// the pinned pool. A missing or disabled pinned pool falls back to global
// with a warning so routing never breaks on a stale reference. owner is
// the upstream name used in the warning (e.g. "opencode", "custom:acme").
func ResolveRelayPools(owner, mode string, poolID *uint, getPool func(uint) (providers.ProxyPool, error)) []RelayPool {
	switch providers.NormalizeProxyMode(mode) {
	case providers.ProxyModeDirect:
		return []RelayPool{}
	case providers.ProxyModePool:
		if poolID == nil {
			return nil
		}
		pool, err := getPool(*poolID)
		if err != nil {
			slog.Warn("upstream pinned to missing pool, using global rotation", "upstream", owner, "pool_id", *poolID)
			return nil
		}
		if !pool.Enabled {
			slog.Warn("upstream pinned to disabled pool, using global rotation", "upstream", owner, "pool", pool.Name)
			return nil
		}
		return []RelayPool{{URL: pool.ProxyURL, NoProxy: pool.NoProxy, Strict: pool.StrictProxy}}
	default:
		return nil
	}
}

func ShouldBypassNoProxy(targetURL, noProxy string) bool {
	noProxy = strings.TrimSpace(noProxy)
	if noProxy == "" {
		return false
	}
	if noProxy == "*" {
		return true
	}
	parsed, err := url.Parse(targetURL)
	if err != nil {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		host = strings.ToLower(targetURL)
	}
	for _, p := range strings.Split(noProxy, ",") {
		p = strings.ToLower(strings.TrimSpace(p))
		if p == "" {
			continue
		}
		if p == "*" {
			return true
		}
		if strings.HasPrefix(p, ".") {
			if strings.HasSuffix(host, p) || host == p[1:] {
				return true
			}
		}
		if host == p || strings.HasSuffix(host, "."+p) {
			return true
		}
	}
	return false
}

func BuildEdgeRelayHeaders(targetURL string, existing map[string]string) map[string]string {
	headers := make(map[string]string, len(existing)+2)
	for k, v := range existing {
		headers[k] = v
	}
	parsed, err := url.Parse(targetURL)
	if err == nil {
		headers["x-relay-target"] = parsed.Scheme + "://" + parsed.Host
		path := parsed.Path
		if path == "" {
			path = "/"
		}
		if parsed.RawQuery != "" {
			path += "?" + parsed.RawQuery
		}
		headers["x-relay-path"] = path
	}
	return headers
}

type RelayPool struct {
	URL     string
	NoProxy string
	Strict  bool
}

type RelaySelector struct {
	mu       sync.RWMutex
	pools    []RelayPool
	strategy string // round-robin (default), random, none
	idx      uint64 // atomic round-robin
}

var SharedRelay = NewRelaySelector()

func NewRelaySelector() *RelaySelector { return &RelaySelector{} }

func (s *RelaySelector) SetPools(pools []RelayPool) {
	s.mu.Lock()
	s.pools = pools
	s.mu.Unlock()
}

// SetStrategy switches rotation: round-robin (default), random, or none
// (always first pool). Unknown values keep the current strategy.
func (s *RelaySelector) SetStrategy(strategy string) {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case "random", "none", "round-robin", "":
		s.mu.Lock()
		s.strategy = strings.ToLower(strings.TrimSpace(strategy))
		s.mu.Unlock()
	}
}

func (s *RelaySelector) Next() (RelayPool, bool) {
	s.mu.RLock()
	n := len(s.pools)
	if n == 0 {
		s.mu.RUnlock()
		return RelayPool{}, false
	}
	pools := s.pools
	strategy := s.strategy
	s.mu.RUnlock()
	switch strategy {
	case "random":
		return pools[rand.N(n)], true
	case "none":
		return pools[0], true
	default:
		return pools[atomic.AddUint64(&s.idx, 1)%uint64(n)], true
	}
}

func (s *RelaySelector) Apply(req *http.Request) bool {
	_, applied := s.ApplyStrict(req)
	return applied
}

// ApplyStrict rewrites req to the next pool and reports whether that pool
// is strict. Callers use strict to decide fallback: a failed strict relay
// must error, a failed non-strict one retries direct.
func (s *RelaySelector) ApplyStrict(req *http.Request) (strict, applied bool) {
	pool, ok := s.Next()
	if !ok || req == nil || req.URL == nil {
		return false, false
	}
	relayURL := strings.TrimSpace(pool.URL)
	if relayURL == "" || ShouldBypassNoProxy(req.URL.String(), pool.NoProxy) {
		return false, false
	}
	parsed, err := url.Parse(relayURL)
	if err != nil {
		return false, false
	}
	for k, v := range BuildEdgeRelayHeaders(req.URL.String(), nil) {
		req.Header.Set(k, v)
	}
	req.URL.Scheme = parsed.Scheme
	req.URL.Host = parsed.Host
	req.URL.Path = parsed.Path
	req.URL.RawQuery = parsed.RawQuery
	req.Host = parsed.Host
	return pool.Strict, true
}

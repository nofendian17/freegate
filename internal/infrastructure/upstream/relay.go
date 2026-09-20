package upstream

import (
	"math/rand/v2"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
)

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

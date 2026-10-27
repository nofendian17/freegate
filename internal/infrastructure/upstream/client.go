package upstream

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const MaxResponseBodySize = 10 << 20 // 10 MB

// keyCooldownTTL is how long a key that just got a 429 is skipped before it
// may be tried again. OpenCode's per-key limiter is 1000 req/min, so a failed
// key stays unusable for roughly the rest of its window.
const keyCooldownTTL = 60 * time.Second

type HTTPClient struct {
	client   *http.Client
	baseURL  string
	apiKeys  []string
	nextKey  atomic.Uint64
	headers  map[string]string
	cooldown *keyCooldown
	// relay overrides the global SharedRelay when non-nil: a selector
	// with one pool pins this client to it, an empty selector means
	// direct (no relay). Nil follows the global rotation. Atomic so
	// admin rebuilds can swap it while requests are in flight.
	relay atomic.Pointer[RelaySelector]
}

// SetRelay overrides the relay selector for this client. Nil restores the
// global SharedRelay. Pass a selector with no pools for direct connections.
func (c *HTTPClient) SetRelay(s *RelaySelector) { c.relay.Store(s) }

// SetRelayPools overrides edge-relay behavior: nil follows the global
// rotation, an empty slice means direct (no relay), otherwise the client
// is pinned to the given pools (normally one).
func (c *HTTPClient) SetRelayPools(pools []RelayPool) {
	if pools == nil {
		c.SetRelay(nil)
		return
	}
	sel := NewRelaySelector()
	sel.SetPools(append([]RelayPool(nil), pools...))
	c.SetRelay(sel)
}

func (c *HTTPClient) relayOrShared() *RelaySelector {
	if s := c.relay.Load(); s != nil {
		return s
	}
	return SharedRelay
}

type keyCooldown struct {
	mu    sync.RWMutex
	until map[string]time.Time
	ttl   time.Duration
}

func newKeyCooldown(ttl time.Duration) *keyCooldown {
	return &keyCooldown{until: make(map[string]time.Time), ttl: ttl}
}

func (k *keyCooldown) mark(key string) {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.until[key] = time.Now().Add(k.ttl)
}

func (k *keyCooldown) isLimited(key string) bool {
	// Fast path under RLock: steady-state checks never write. Only an
	// expired entry upgrades to a write lock for the lazy delete.
	k.mu.RLock()
	until, ok := k.until[key]
	if !ok {
		k.mu.RUnlock()
		return false
	}
	if time.Now().Before(until) {
		k.mu.RUnlock()
		return true
	}
	k.mu.RUnlock()
	k.mu.Lock()
	defer k.mu.Unlock()
	// Re-check under the write lock: a concurrent mark may have renewed it.
	if until, ok := k.until[key]; !ok || time.Now().After(until) {
		delete(k.until, key)
		return false
	}
	return true
}

// NewTransport builds a tuned http.Transport for upstream calls.
// Shared between all upstreams so idle connections are pooled once
// rather than per-upstream.
func NewTransport() *http.Transport {
	tr := &http.Transport{
		// Explicit connect timeout: a blackholed endpoint must fail into
		// the same-tier retry / combo failover within seconds. Without a
		// DialContext the zero-value dialer has no deadline at all — only
		// http.DefaultTransport sets one — so connects could hang until
		// the OS TCP timeout.
		DialContext: (&net.Dialer{
			Timeout:   10 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		// HTTP/2 multiplexes concurrent streams over a single TCP+TLS
		// connection, so far fewer handshakes cross lossy networks.
		// All upstreams negotiate h2 (verified live); others
		// fall back to HTTP/1.1 automatically.
		ForceAttemptHTTP2:   true,
		TLSHandshakeTimeout: 10 * time.Second,
		// Fail fast on stalled upstreams so the same-tier retry and combo
		// failover can try the next candidate: a healthy upstream sends
		// response headers within seconds (streaming), so 30s is ample
		// headroom without parking a request for a minute on a dead tier.
		ResponseHeaderTimeout: 30 * time.Second,
		MaxIdleConns:          256,
		MaxIdleConnsPerHost:   64,
		// Must stay below the model refresh cadence (60s): a pooled
		// connection blackholed mid-flight (e.g. an h2 session with
		// a stream in flight during CloseIdleConnections, which
		// misses it) would otherwise be reused by every refresh, and each
		// failed reuse resets the idle clock — pinning the refresher on the
		// dead session forever. At 30s the poisoned session ages out between
		// refreshes and the next attempt redials. Chat traffic is unaffected
		// (it reuses connections within seconds).
		IdleConnTimeout: 30 * time.Second,
	}
	return tr
}

// NewHTTPClientWithTransport builds an HTTPClient reusing a shared Transport.
func NewHTTPClientWithTransport(baseURL string, apiKeys []string, headers map[string]string, tr *http.Transport) *HTTPClient {
	if tr == nil {
		if dt, ok := http.DefaultTransport.(*http.Transport); ok {
			tr = dt
		} else {
			tr = NewTransport()
		}
	}
	hc := &http.Client{Timeout: 0, Transport: tr}
	if headers == nil {
		headers = make(map[string]string)
	}
	return &HTTPClient{
		client:   hc,
		baseURL:  strings.TrimSuffix(baseURL, "/"),
		apiKeys:  apiKeys,
		headers:  headers,
		cooldown: newKeyCooldown(keyCooldownTTL),
	}
}

func (c *HTTPClient) Get(ctx context.Context, path string) (*http.Response, error) {
	return c.do(ctx, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, "GET", c.baseURL+path, http.NoBody)
	})
}

func (c *HTTPClient) Post(ctx context.Context, path string, body []byte) (*http.Response, error) {
	return c.PostWithHeaders(ctx, path, body, nil)
}

func (c *HTTPClient) PostWithHeaders(ctx context.Context, path string, body []byte, extra map[string]string) (*http.Response, error) {
	// Strip `n` parameter (number of choices). Most providers only support
	// n=1 and return 422 if n > 1; freegate only processes the first choice.
	cleaned, err := stripN(body)
	if err != nil {
		return nil, fmt.Errorf("strip n: %w", err)
	}
	// Probe stream once up front instead of re-unmarshalling on every
	// retry attempt (429 key rotation rebuilds the request).
	isStream := isStreamBody(cleaned)
	return c.doWithHeaders(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+path, bytes.NewReader(cleaned))
		if err != nil {
			return nil, fmt.Errorf("build POST request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		if isStream {
			req.Header.Set("Accept", "text/event-stream")
		}
		return req, nil
	}, extra)
}

// do sends a request, retrying on 429 with the next API key so a rate-limited
// account automatically falls over to the next one. Keys that returned 429 go
// into a short cooldown and are skipped by later requests. A single key keeps
// the previous behavior: a 429 is returned as-is.
func (c *HTTPClient) do(ctx context.Context, build func() (*http.Request, error)) (*http.Response, error) {
	return c.doWithHeaders(ctx, build, nil)
}

func (c *HTTPClient) doWithHeaders(ctx context.Context, build func() (*http.Request, error), extra map[string]string) (*http.Response, error) {
	attempts := len(c.apiKeys)
	if attempts < 1 {
		attempts = 1
	}
	for attempt := 0; attempt < attempts; attempt++ {
		req, err := build()
		if err != nil {
			return nil, err
		}
		key := c.currentKey()
		req.Header.Set("Authorization", "Bearer "+key)
		for k, v := range c.headers {
			req.Header.Set(k, v)
		}
		for k, v := range extra {
			req.Header.Set(k, v)
		}
		// Keep x-api-key in sync with the bearer actually used. When rotation
		// selects a non-public key — including 429 failover to the next key
		// — mirror it over a default "public" marker so the pair never
		// mismatches. An explicitly non-public x-api-key is left untouched.
		// Conversely, anonymous attempts (public/empty bearer) must NOT
		// carry `x-api-key: public`: the genuine OpenCode client
		// authenticates with `Authorization: Bearer` only (anomalyco/opencode
		// commit 5a83358, session/llm/request.ts: no x-api-key is ever set)
		// and the free-tier gate treats the public marker as a non-genuine
		// fingerprint (403 FreeTierError).
		if key != "" && key != "public" && strings.EqualFold(req.Header.Get("x-api-key"), "public") {
			req.Header.Set("x-api-key", key)
		}
		if key == "" || key == "public" {
			if cur := req.Header.Get("x-api-key"); cur == "" || strings.EqualFold(cur, "public") {
				req.Header.Del("x-api-key")
			}
		}
		strict, applied := c.relayOrShared().ApplyStrict(req)

		resp, err := c.client.Do(req)
		if err != nil && applied && !strict {
			// Non-strict relay unreachable: rebuild the request without
			// relay rewriting and try direct once.
			directReq, derr := build()
			if derr != nil {
				return nil, derr
			}
			key = c.currentKey()
			directReq.Header.Set("Authorization", "Bearer "+key)
			for k, v := range c.headers {
				directReq.Header.Set(k, v)
			}
			for k, v := range extra {
				directReq.Header.Set(k, v)
			}
			resp, err = c.client.Do(directReq)
		}
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusTooManyRequests && len(c.apiKeys) > 1 && attempt+1 < attempts {
			c.cooldown.mark(key)
			_ = resp.Body.Close()
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("unreachable: no attempt returned a response")
}

// ReadAll fetches a small JSON document (the model catalog) and returns the
// decoded body.
//
// It declares Accept-Encoding itself instead of letting the transport
// negotiate gzip. Go's transparent gzip decoding assumes every hop keeps body
// and headers consistent: if a relay decodes the upstream body with its own
// fetch() but forwards Content-Encoding verbatim (Bun/undici do exactly that,
// oven-sh/bun#35546), the transport fails on the first read with
// "gzip: invalid header" and the refresh dies with zero bytes to salvage.
// Setting the header ourselves turns transparent decoding off, so a stale
// Content-Encoding becomes inert; a body that arrives gzip-encoded anyway is
// detected by its magic bytes and inflated here.
func (c *HTTPClient) ReadAll(ctx context.Context, path string) ([]byte, error) {
	resp, err := c.doWithHeaders(ctx, func() (*http.Request, error) {
		return http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+path, http.NoBody)
	}, map[string]string{"Accept-Encoding": "identity"})
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, MaxResponseBodySize+1))
	if err != nil {
		return nil, err
	}
	// Catalogs are JSON: a non-2xx or an empty 2xx is a failure, not an
	// empty list. Report the status and a body snippet here, otherwise the
	// caller only sees "unexpected end of JSON input" and the one detail
	// that explains it — the status — is gone.
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("GET %s: %s (content-type %q): %s", path, resp.Status, resp.Header.Get("Content-Type"), bodySnippet(body))
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("GET %s: empty body (%s, content-type %q)", path, resp.Status, resp.Header.Get("Content-Type"))
	}
	// Identity was requested, but an upstream or relay may compress anyway.
	// Trust the bytes, not the header.
	if len(body) > 2 && body[0] == 0x1f && body[1] == 0x8b {
		zr, zerr := gzip.NewReader(bytes.NewReader(body))
		if zerr != nil {
			return nil, fmt.Errorf("gunzip catalog body: %w", zerr)
		}
		defer func() { _ = zr.Close() }()
		return io.ReadAll(io.LimitReader(zr, MaxResponseBodySize+1))
	}
	return body, nil
}

// bodySnippet renders a short, single-line quote of an error body for logs.
const bodySnippetLimit = 200

func bodySnippet(body []byte) string {
	s := strings.Join(strings.Fields(string(body)), " ")
	if s == "" {
		return "(no body)"
	}
	if len(s) > bodySnippetLimit {
		return s[:bodySnippetLimit] + "…"
	}
	return s
}

// CloseIdleConnections drops pooled idle connections so the next request
// redials. Used as a Refresher on-failure hook: a refresh that fails on a
// blackholed pooled session must not let the next retry reuse the same
// dead session.
func (c *HTTPClient) CloseIdleConnections() {
	if c == nil || c.client == nil {
		return
	}
	if tr, ok := c.client.Transport.(interface{ CloseIdleConnections() }); ok && tr != nil {
		tr.CloseIdleConnections()
	}
}

// currentKey returns the API key used for this request. With multiple keys
// configured it rotates round-robin so usage (and any per-key rate limit)
// spreads across accounts, skipping keys still in cooldown; a single key keeps
// the old behavior.
func (c *HTTPClient) currentKey() string {
	if len(c.apiKeys) == 0 {
		return ""
	}
	if len(c.apiKeys) == 1 {
		return c.apiKeys[0]
	}
	n := uint64(len(c.apiKeys))
	for range len(c.apiKeys) {
		i := c.nextKey.Add(1) - 1
		key := c.apiKeys[i%n]
		if !c.cooldown.isLimited(key) {
			return key
		}
	}
	// Every key is in cooldown; fall back to round-robin so a request still
	// goes out and can confirm or re-mark the limit instead of failing fast.
	i := c.nextKey.Add(1) - 1
	return c.apiKeys[i%n]
}

// stripN removes the "n" field from a JSON request body if present.
// freegate only uses the first choice; most providers reject n > 1 with 422.
func stripN(body []byte) ([]byte, error) {
	// Fast path: the vast majority of bodies have no "n" key.
	if !bytes.Contains(body, []byte(`"n"`)) {
		return body, nil
	}
	var raw map[string]any
	if err := json.Unmarshal(body, &raw); err != nil {
		return body, nil // not JSON, pass through
	}
	if _, ok := raw["n"]; ok {
		delete(raw, "n")
		out, err := json.Marshal(raw)
		if err != nil {
			return nil, fmt.Errorf("marshal after strip n: %w", err)
		}
		return out, nil
	}
	return body, nil
}

package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"golang.org/x/net/proxy"
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
}

type keyCooldown struct {
	mu    sync.Mutex
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
	k.mu.Lock()
	defer k.mu.Unlock()
	until, ok := k.until[key]
	if !ok || time.Now().After(until) {
		delete(k.until, key)
		return false
	}
	return true
}

func NewHTTPClient(baseURL string, apiKeys []string, socksAddr string, headers map[string]string) *HTTPClient {
	hc := &http.Client{Timeout: 0}
	if socksAddr != "" {
		dialer, err := proxy.SOCKS5("tcp", socksAddr, nil, proxy.Direct)
		if err != nil {
			slog.Warn("SOCKS5 dialer failed, using direct connection", "error", err)
		} else {
			tr := &http.Transport{ForceAttemptHTTP2: false}
			if dc, ok := dialer.(proxy.ContextDialer); ok {
				tr.DialContext = dc.DialContext
			} else {
				tr.DialContext = func(ctx context.Context, network, addr string) (net.Conn, error) {
					return dialer.Dial(network, addr)
				}
			}
			hc.Transport = tr
		}
	}
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
	// Strip `n` parameter (number of choices). Most providers only support
	// n=1 and return 422 if n > 1; freegate only processes the first choice.
	cleaned, err := stripN(body)
	if err != nil {
		return nil, fmt.Errorf("strip n: %w", err)
	}
	return c.do(ctx, func() (*http.Request, error) {
		req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+path, bytes.NewReader(cleaned))
		if err != nil {
			return nil, fmt.Errorf("build POST request: %w", err)
		}
		req.Header.Set("Content-Type", "application/json")
		var probe struct {
			Stream *bool `json:"stream"`
		}
		_ = json.Unmarshal(cleaned, &probe)
		if probe.Stream != nil && *probe.Stream {
			req.Header.Set("Accept", "text/event-stream")
		}
		return req, nil
	})
}

// do sends a request, retrying on 429 with the next API key so a rate-limited
// account automatically falls over to the next one. Keys that returned 429 go
// into a short cooldown and are skipped by later requests. A single key keeps
// the previous behavior: a 429 is returned as-is.
func (c *HTTPClient) do(ctx context.Context, build func() (*http.Request, error)) (*http.Response, error) {
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

		resp, err := c.client.Do(req)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode == http.StatusTooManyRequests && len(c.apiKeys) > 1 && attempt+1 < attempts {
			c.cooldown.mark(key)
			resp.Body.Close()
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("unreachable: no attempt returned a response")
}

func (c *HTTPClient) ReadAll(ctx context.Context, path string) ([]byte, error) {
	resp, err := c.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, MaxResponseBodySize+1))
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

package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const MaxResponseBodySize = 10 << 20 // 10 MB

type HTTPClient struct {
	client  *http.Client
	baseURL string
	apiKey  string
	headers map[string]string
}

func NewHTTPClient(baseURL, apiKey string, d *Dialer, headers map[string]string) *HTTPClient {
	hc := &http.Client{Timeout: 0}
	if d != nil {
		tr := &http.Transport{
			ForceAttemptHTTP2: false,
			// Bound only the connection phase: a dead VPN tunnel or a
			// slow relay should fail fast instead of hanging client
			// requests. Streaming responses are unaffected because the
			// body streams after the header arrives.
			TLSHandshakeTimeout:   10 * time.Second,
			ResponseHeaderTimeout: 30 * time.Second,
		}
		tr.DialContext = d.DialContext
		hc.Transport = tr
	}
	if headers == nil {
		headers = make(map[string]string)
	}
	return &HTTPClient{
		client:  hc,
		baseURL: strings.TrimSuffix(baseURL, "/"),
		apiKey:  apiKey,
		headers: headers,
	}
}

func (c *HTTPClient) Get(ctx context.Context, path string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", c.baseURL+path, http.NoBody)
	if err != nil {
		return nil, fmt.Errorf("build GET request: %w", err)
	}
	c.applyAuth(req)
	return c.client.Do(req)
}

func (c *HTTPClient) Post(ctx context.Context, path string, body []byte) (*http.Response, error) {
	// Strip `n` parameter (number of choices). Most providers only support
	// n=1 and return 422 if n > 1; freegate only processes the first choice.
	cleaned, err := stripN(body)
	if err != nil {
		return nil, fmt.Errorf("strip n: %w", err)
	}

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
	c.applyAuth(req)
	return c.client.Do(req)
}

func (c *HTTPClient) ReadAll(ctx context.Context, path string) ([]byte, error) {
	resp, err := c.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, MaxResponseBodySize+1))
}

func (c *HTTPClient) applyAuth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
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

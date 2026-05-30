package upstream

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"golang.org/x/net/proxy"
)

type HTTPClient struct {
	client  *http.Client
	baseURL string
	apiKey  string
	headers map[string]string
}

func NewHTTPClient(baseURL, apiKey, socksAddr string, headers map[string]string) *HTTPClient {
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
	req, err := http.NewRequestWithContext(ctx, "POST", c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("build POST request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "text/event-stream")
	c.applyAuth(req)
	return c.client.Do(req)
}

func (c *HTTPClient) ReadAll(ctx context.Context, path string) ([]byte, error) {
	resp, err := c.Get(ctx, path)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (c *HTTPClient) applyAuth(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
}

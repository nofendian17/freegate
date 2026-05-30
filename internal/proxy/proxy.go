package proxy

import (
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"freegate/internal/metrics"
	"freegate/internal/model"
	"freegate/internal/respond"
	"freegate/internal/upstream"
)

const (
	StreamBufferSize = 32 * 1024
	RetryDelay       = 3 * time.Second
	DefaultMaxRetry  = 2
)

type Router interface {
	Select(modelID string) upstream.Upstream
	AllModels() []model.Model
	IsReady() bool
}

type IPRotator interface {
	ForceNewIP() error
}

type Client struct {
	router    Router
	maxRetry  int
	ipRotator IPRotator
	metrics   *metrics.Metrics
}

func NewClient(router Router) *Client {
	return &Client{
		router:   router,
		maxRetry: DefaultMaxRetry,
		metrics:  metrics.New(),
	}
}

func (c *Client) WithTorController(ir IPRotator) *Client {
	c.ipRotator = ir
	return c
}

// Metrics returns the metrics snapshot for the /v1/metrics endpoint.
func (c *Client) Metrics() map[string]any {
	return c.metrics.Snapshot()
}

func (c *Client) AllModels() []model.Model {
	return c.router.AllModels()
}

func (c *Client) IsReady() bool {
	return c.router.IsReady()
}

func (c *Client) ProxyChat(w http.ResponseWriter, r *http.Request, modelID string, body []byte) {
	requestID := r.Header.Get("X-Request-ID")
	c.metrics.TotalRequests.Add(1)
	slog.Info("chat request",
		"request_id", requestID,
		"model", modelID,
		"content_length", len(body),
		"remote", r.RemoteAddr,
	)

	u := c.router.Select(modelID)
	c.metrics.IncrUpstream(u.Name())
	slog.Info("upstream selected", "request_id", requestID, "model", modelID, "upstream", u.Name())

	var resp *http.Response
	var err error
	for attempt := 0; attempt <= c.maxRetry; attempt++ {
		if attempt > 0 {
			if c.ipRotator != nil {
				if torErr := c.ipRotator.ForceNewIP(); torErr != nil {
					slog.Warn("tor: forced IP rotation failed", "request_id", requestID, "attempt", attempt, "error", torErr)
				} else {
					slog.Info("tor: IP rotated for retry", "request_id", requestID, "attempt", attempt)
				}
			}
			c.metrics.RetryCount.Add(1)
			select {
			case <-r.Context().Done():
				respond.JSONError(w, http.StatusGatewayTimeout, "client_closed", "client disconnected during retry")
				return
			case <-time.After(RetryDelay):
			}
		}

		resp, err = u.ChatCompletion(r.Context(), body)
		if err != nil {
			c.metrics.UpstreamErrors.Add(1)
			slog.Error("upstream request failed", "request_id", requestID, "upstream", u.Name(), "error", err)
			respond.JSONError(w, http.StatusBadGateway, "upstream_error", fmt.Sprintf("upstream request failed: %v", err))
			return
		}

		if resp.StatusCode != http.StatusTooManyRequests {
			break
		}

		resp.Body.Close()
		slog.Warn("upstream returned 429, rotating IP and retrying",
			"request_id", requestID,
			"upstream", u.Name(),
			"attempt", attempt+1,
			"max_retry", c.maxRetry,
		)
	}
	defer resp.Body.Close()

	slog.Info("upstream response", "request_id", requestID, "upstream", u.Name(), "status", resp.StatusCode)

	copyHeaders(w, resp)
	w.Header().Set("Access-Control-Allow-Origin", "*")
	w.WriteHeader(resp.StatusCode)

	copyNormalized(w, resp, requestID)
}

func copyHeaders(dst http.ResponseWriter, src *http.Response) {
	hopByHop := map[string]bool{
		"Connection":          true,
		"Proxy-Connection":    true,
		"Keep-Alive":          true,
		"Proxy-Authenticate":  true,
		"Proxy-Authorization": true,
		"TE":                  true,
		"Trailers":            true,
		"Transfer-Encoding":   true,
		"Upgrade":             true,
	}
	for k, vs := range src.Header {
		if hopByHop[k] {
			continue
		}
		for _, v := range vs {
			dst.Header().Add(k, v)
		}
	}
}

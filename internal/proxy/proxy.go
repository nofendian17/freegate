package proxy

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	anyllm "github.com/mozilla-ai/any-llm-go"

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

// RequestLogger is a callback type for request logging.
type RequestLogger = model.RequestLogger

// Router is the subset of upstream.Router that proxy.Client needs.
type Router interface {
	Select(modelID string) upstream.Upstream
	AllModels() []model.Model
	IsReady() bool
}

type IPRotator interface {
	ForceNewIP() error
}

type Client struct {
	router     Router
	maxRetry   int
	ipRotator  IPRotator
	metrics    *metrics.Metrics
	requestLog RequestLogger
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

// WithRequestLogger wires a callback that receives one entry per completed
// proxied request. Pass nil to disable.
func (c *Client) WithRequestLogger(fn RequestLogger) *Client {
	c.requestLog = fn
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

// ProxyChat proxies an OpenAI-compatible chat completion to the selected
// upstream. params.Model determines routing. params.Stream toggles SSE.
func (c *Client) ProxyChat(w http.ResponseWriter, r *http.Request, params anyllm.CompletionParams) {
	start := time.Now()
	requestID := r.Header.Get("X-Request-ID")
	c.metrics.TotalRequests.Add(1)

	var (
		finalStatus      int
		finalUpstream    string
		finalErr         error
		finalTotalTokens int
		finalPrompt      int
		finalCompletion  int
	)
	defer func() {
		if c.requestLog == nil {
			return
		}
		errStr := ""
		if finalErr != nil {
			errStr = finalErr.Error()
		}
		c.requestLog(model.RequestLogEntry{
			Ts:               start,
			Method:           r.Method,
			Path:             r.URL.Path,
			Model:            params.Model,
			Upstream:         finalUpstream,
			Status:           finalStatus,
			DurationMs:       time.Since(start).Milliseconds(),
			IP:               clientIPFromRequest(r),
			Error:            errStr,
			TotalTokens:      finalTotalTokens,
			PromptTokens:     finalPrompt,
			CompletionTokens: finalCompletion,
		})
	}()

	slog.Info("chat request",
		"request_id", requestID,
		"model", params.Model,
		"stream", params.Stream,
		"remote", r.RemoteAddr,
	)

	u := c.router.Select(params.Model)
	c.metrics.IncrUpstream(u.Name())
	finalUpstream = u.Name()
	slog.Info("upstream selected", "request_id", requestID, "model", params.Model, "upstream", u.Name())

	// any-llm-go providers expose themselves via upstream.Upstream.Provider().
	pAny, ok := u.(interface{ Provider() anyllm.Provider })
	if !ok {
		finalStatus = http.StatusInternalServerError
		finalErr = fmt.Errorf("upstream %s does not expose a any-llm-go Provider", u.Name())
		respond.JSONError(w, http.StatusInternalServerError, "internal_error", finalErr.Error())
		return
	}
	provider := pAny.Provider()

	var usage TokenUsage
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
				finalStatus = http.StatusGatewayTimeout
				finalErr = fmt.Errorf("client disconnected during retry")
				respond.JSONError(w, http.StatusGatewayTimeout, "client_closed", "client disconnected during retry")
				return
			case <-time.After(RetryDelay):
			}
		}

		if params.Stream {
			chunks, errs := provider.CompletionStream(r.Context(), params)
			streamErr := writeStreaming(w, chunks, errs, &usage)
			if streamErr == nil {
				finalStatus = http.StatusOK
				finalTotalTokens += usage.Total
				finalPrompt += usage.Prompt
				finalCompletion += usage.Completion
				if usage.Total > 0 {
					c.metrics.TotalTokens.Add(int64(usage.Total))
				}
				if usage.Prompt > 0 {
					c.metrics.PromptTokens.Add(int64(usage.Prompt))
				}
				if usage.Completion > 0 {
					c.metrics.CompletionTokens.Add(int64(usage.Completion))
				}
				return
			}
			if errors.Is(streamErr, anyllm.ErrRateLimit) {
				slog.Warn("upstream returned rate limit, rotating IP and retrying",
					"request_id", requestID, "upstream", u.Name(), "attempt", attempt+1, "max_retry", c.maxRetry)
				continue
			}
			c.metrics.UpstreamErrors.Add(1)
			finalStatus = http.StatusBadGateway
			finalErr = streamErr
			slog.Error("streaming upstream request failed", "request_id", requestID, "upstream", u.Name(), "error", streamErr)
			respond.JSONError(w, http.StatusBadGateway, "upstream_error", fmt.Sprintf("upstream request failed: %v", streamErr))
			return
		}

		resp, err := provider.Completion(r.Context(), params)
		if errors.Is(err, anyllm.ErrRateLimit) {
			slog.Warn("upstream returned rate limit, rotating IP and retrying",
				"request_id", requestID, "upstream", u.Name(), "attempt", attempt+1, "max_retry", c.maxRetry)
			continue
		}
		if err != nil {
			c.metrics.UpstreamErrors.Add(1)
			finalStatus = http.StatusBadGateway
			finalErr = err
			slog.Error("upstream request failed", "request_id", requestID, "upstream", u.Name(), "error", err)
			respond.JSONError(w, http.StatusBadGateway, "upstream_error", fmt.Sprintf("upstream request failed: %v", err))
			return
		}

		finalStatus = http.StatusOK
		writeNonStreaming(w, resp, &usage)
		finalTotalTokens += usage.Total
		finalPrompt += usage.Prompt
		finalCompletion += usage.Completion
		if usage.Total > 0 {
			c.metrics.TotalTokens.Add(int64(usage.Total))
		}
		if usage.Prompt > 0 {
			c.metrics.PromptTokens.Add(int64(usage.Prompt))
		}
		if usage.Completion > 0 {
			c.metrics.CompletionTokens.Add(int64(usage.Completion))
		}
		return
	}

	c.metrics.UpstreamErrors.Add(1)
	finalStatus = http.StatusBadGateway
	finalErr = anyllm.ErrRateLimit
	respond.JSONError(w, http.StatusBadGateway, "upstream_error", "upstream returned rate limit after all retries")
}

// clientIPFromRequest extracts the client IP from request headers or RemoteAddr.
// Priority: X-Forwarded-For > X-Real-IP > RemoteAddr.
func clientIPFromRequest(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.IndexByte(xff, ','); i != -1 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if xri := r.Header.Get("X-Real-IP"); xri != "" {
		return strings.TrimSpace(xri)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

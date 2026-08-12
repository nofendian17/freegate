package application

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"freegate/internal/domain"
	"freegate/internal/httputil"
	"freegate/internal/infrastructure/metrics"
	proxyinfra "freegate/internal/infrastructure/proxy"
)

// Router selects an Upstream for a given model ID.
type Router interface {
	Select(modelID string) (domain.Upstream, error)
}

// ChatService orchestrates chat-completion requests: routing, request
// logging, and metrics. Upstream responses — including 4xx/5xx statuses
// such as 429 — are passed through to the client verbatim. There is no
// automatic retry or IP rotation here: the user picks the VPN exit server
// manually from the dashboard.
type ChatService struct {
	router  Router
	metrics *metrics.Metrics
	logger  domain.RequestLogger
}

// NewChatService constructs a ChatService. Pass nil for m to disable
// metrics.
func NewChatService(router Router, m *metrics.Metrics) *ChatService {
	return &ChatService{
		router:  router,
		metrics: m,
	}
}

// WithRequestLogger wires a callback that receives one entry per completed
// proxied request. Pass nil to disable.
func (s *ChatService) WithRequestLogger(fn domain.RequestLogger) *ChatService {
	s.logger = fn
	return s
}

// ProxyChat routes the request to the appropriate upstream and streams the
// response back to w. Whatever the upstream returns — success or an error
// status like 429 — is forwarded to the client unchanged; the function
// only returns an error for transport/selection failures.
func (s *ChatService) ProxyChat(ctx context.Context, w http.ResponseWriter, r *http.Request, modelID string, body []byte) error {
	start := time.Now()
	requestID := ""
	method := ""
	path := ""
	ip := ""
	if r != nil {
		requestID = r.Header.Get("X-Request-ID")
		method = r.Method
		ip = httputil.ClientIP(r)
		if r.URL != nil {
			path = r.URL.Path
		}
	}
	if s.metrics != nil {
		s.metrics.TotalRequests.Add(1)
	}

	var (
		finalStatus       int
		finalUpstream     string
		finalErr          error
		finalTotalTokens  int
		finalPromptTokens int
		finalComplTokens  int
	)
	defer func() {
		if s.logger == nil {
			return
		}
		errStr := ""
		if finalErr != nil {
			errStr = finalErr.Error()
		}
		s.logger(domain.RequestLogEntry{
			Ts:               start,
			Method:           method,
			Path:             path,
			Model:            modelID,
			Upstream:         finalUpstream,
			Status:           finalStatus,
			DurationMs:       time.Since(start).Milliseconds(),
			IP:               ip,
			Error:            errStr,
			TotalTokens:      finalTotalTokens,
			PromptTokens:     finalPromptTokens,
			CompletionTokens: finalComplTokens,
		})
	}()

	slog.Info("chat request",
		"request_id", requestID,
		"model", modelID,
		"content_length", len(body),
		"remote", r.RemoteAddr,
	)

	u, err := s.router.Select(modelID)
	if err != nil {
		wrappedErr := fmt.Errorf("select upstream: %w", err)
		if s.metrics != nil {
			s.metrics.UpstreamErrors.Add(1)
		}
		finalStatus = http.StatusBadGateway
		finalErr = wrappedErr
		slog.Error("upstream select failed", "request_id", requestID, "model", modelID, "error", err)
		return wrappedErr
	}
	if s.metrics != nil {
		s.metrics.IncrUpstream(u.Name())
	}
	finalUpstream = u.Name()
	slog.Info("upstream selected", "request_id", requestID, "model", modelID, "upstream", u.Name())

	resp, err := u.ChatCompletion(ctx, domain.ChatRequest{Body: body, OriginalReq: r})
	if err != nil {
		wrappedErr := fmt.Errorf("upstream request: %w", err)
		if s.metrics != nil {
			s.metrics.UpstreamErrors.Add(1)
		}
		finalStatus = http.StatusBadGateway
		finalErr = wrappedErr
		slog.Error("upstream request failed", "request_id", requestID, "upstream", u.Name(), "error", err)
		return wrappedErr
	}
	defer resp.Body.Close()

	slog.Info("upstream response", "request_id", requestID, "upstream", u.Name(), "status", resp.StatusCode)
	finalStatus = resp.StatusCode

	w.Header().Set("Access-Control-Allow-Origin", "*")
	// Upstream HTTP errors (429/5xx) are passed through verbatim; capture
	// the upstream's own error message so the record log shows why it
	// failed instead of only the bare status code.
	if resp.StatusCode >= 400 {
		if msg := proxyinfra.PassThroughError(w, resp); msg != "" {
			finalErr = fmt.Errorf("upstream: %s", msg)
		}
		return nil
	}
	usage, err := proxyinfra.NormalizeResponse(w, resp)
	if err != nil {
		slog.Warn("normalize response failed", "request_id", requestID, "upstream", u.Name(), "error", err)
	} else {
		finalTotalTokens = usage.Total
		finalPromptTokens = usage.Prompt
		finalComplTokens = usage.Completion
		if s.metrics != nil && usage.Total > 0 {
			s.metrics.InputTokens.Add(int64(usage.Prompt))
			s.metrics.OutputTokens.Add(int64(usage.Completion))
		}
	}
	return nil
}

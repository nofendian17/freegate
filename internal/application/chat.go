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
	Select(modelID string) domain.Upstream
}

type chainSelector interface {
	SelectChain(modelID string) []domain.Upstream
}

func (s *ChatService) candidates(modelID string) []domain.Upstream {
	if cs, ok := s.router.(chainSelector); ok {
		if chain := cs.SelectChain(modelID); len(chain) > 0 {
			return chain
		}
	}
	if u := s.router.Select(modelID); u != nil {
		return []domain.Upstream{u}
	}
	return nil
}

// ChatService orchestrates chat-completion requests: routing, request
// logging, and metrics. Ordered upstream candidates are tried in turn:
// transport errors, 429s, and 5xx fail over to the next candidate; the
// first success — or the last failure when exhausted — is passed through
// to the client verbatim. There is no IP rotation here: the user picks
// the VPN exit server manually from the dashboard.
type ChatService struct {
	router         Router
	metrics        *metrics.Metrics
	logger         domain.RequestLogger
	rawUpstreamLog bool
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

// ProxyChat routes the request to the ordered upstream candidates and
// streams the response back to w. Transport errors, 429s, and 5xx fail
// over to the next candidate; the first success — or the last failure
// when the chain is exhausted — is forwarded to the client unchanged;
// the function only returns an error for selection failures or when
// every candidate fails at the transport level.
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

	candidates := s.candidates(modelID)
	if len(candidates) == 0 {
		wrappedErr := fmt.Errorf("select upstream: no upstream for model %q", modelID)
		if s.metrics != nil {
			s.metrics.UpstreamErrors.Add(1)
		}
		finalStatus = http.StatusBadGateway
		finalErr = wrappedErr
		slog.Error("upstream select failed", "request_id", requestID, "model", modelID, "error", wrappedErr)
		return wrappedErr
	}

	var (
		u    domain.Upstream
		resp *domain.UpstreamResponse
	)
	for i, cand := range candidates {
		u = cand
		last := i == len(candidates)-1
		if s.metrics != nil {
			s.metrics.IncrUpstream(u.Name())
		}
		finalUpstream = u.Name()
		slog.Info("upstream selected", "request_id", requestID, "model", modelID, "upstream", u.Name())

		rsp, err := u.ChatCompletion(ctx, body)
		if err != nil {
			if !last {
				slog.Warn("upstream request failed, trying next candidate", "request_id", requestID, "upstream", u.Name(), "error", err)
				continue
			}
			wrappedErr := fmt.Errorf("upstream request: %w", err)
			if s.metrics != nil {
				s.metrics.UpstreamErrors.Add(1)
			}
			finalStatus = http.StatusBadGateway
			finalErr = wrappedErr
			slog.Error("upstream request failed", "request_id", requestID, "upstream", u.Name(), "error", err)
			return wrappedErr
		}
		if (rsp.StatusCode == http.StatusTooManyRequests || rsp.StatusCode >= 500) && !last {
			slog.Warn("upstream retryable status, trying next candidate", "request_id", requestID, "upstream", u.Name(), "status", rsp.StatusCode)
			rsp.Close()
			continue
		}
		resp = rsp
		break
	}
	defer resp.Close()

	slog.Info("upstream response", "request_id", requestID, "upstream", u.Name(), "status", resp.StatusCode)
	finalStatus = resp.StatusCode

	// Raw upstream logging (opt-in via UPSTREAM_CAPTURE=true): every
	// upstream response line is printed via slog so degenerate upstream
	// behavior (e.g. muse-spark's EOF-truncated streams) is visible in the
	// server log without any file capture.
	if s.rawUpstreamLog {
		resp.Body = newRawLineLogger(resp.Body, modelID, requestID)
	}

	w.Header().Set("Access-Control-Allow-Origin", "*")
	// Upstream HTTP errors (429/5xx) are passed through verbatim; capture
	// the upstream's own error message so the record log shows why it
	// failed instead of only the bare status code. Count it in stats too —
	// these were previously invisible in the dashboard's error counters.
	if resp.StatusCode >= 400 {
		if s.metrics != nil {
			s.metrics.UpstreamErrors.Add(1)
		}
		if msg := proxyinfra.PassThroughDomainError(w, resp); msg != "" {
			finalErr = fmt.Errorf("upstream: %s", msg)
		}
		return nil
	}

	// Correlation headers for the normalization layer: degenerate-response
	// warnings (e.g. upstream empty completion) carry these so operators can
	// trace a warning back to this request. Set only on the success path —
	// the error pass-through above copies upstream headers verbatim and must
	// not leak these to the client. NormalizeDomainResponseWithContext
	// strips them before writing the client response.
	resp.Header.Set("X-Fg-Model", modelID)
	resp.Header.Set("X-Fg-Request-Id", requestID)
	usage, err := proxyinfra.NormalizeDomainResponseWithContext(ctx, w, resp)
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

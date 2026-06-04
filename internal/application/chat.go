package application

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"freegate/internal/domain"
	"freegate/internal/httputil"
	"freegate/internal/infrastructure/metrics"
	proxyinfra "freegate/internal/infrastructure/proxy"
)

const (
	DefaultMaxRetries = 2
	DefaultRetryDelay = 3 * time.Second
)

// Router selects an Upstream for a given model ID.
type Router interface {
	Select(modelID string) (domain.Upstream, error)
}

// ChatService orchestrates chat-completion requests: routing, retries with
// IP rotation, request logging, and metrics.
type ChatService struct {
	router     Router
	ipRotator  domain.IPRotator
	metrics    *metrics.Metrics
	logger     domain.RequestLogger
	maxRetries int
	retryDelay time.Duration
}

// NewChatService constructs a ChatService. Pass nil for ipRotator to
// disable IP rotation. Pass nil for m to disable metrics.
func NewChatService(
	router Router,
	ipRotator domain.IPRotator,
	m *metrics.Metrics,
	maxRetries int,
	retryDelay time.Duration,
) *ChatService {
	return &ChatService{
		router:     router,
		ipRotator:  ipRotator,
		metrics:    m,
		maxRetries: maxRetries,
		retryDelay: retryDelay,
	}
}

// WithRequestLogger wires a callback that receives one entry per completed
// proxied request. Pass nil to disable.
func (s *ChatService) WithRequestLogger(fn domain.RequestLogger) *ChatService {
	s.logger = fn
	return s
}

// MaxRetriesExceededError is returned when all retry attempts on a 429
// response have been exhausted.
type MaxRetriesExceededError struct {
	ModelID string
}

func (e *MaxRetriesExceededError) Error() string {
	return fmt.Sprintf("max retries exceeded for model %s", e.ModelID)
}

// ProxyChat routes the request to the appropriate upstream, retries on
// 429 with Tor IP rotation, and streams the response back to w.
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
		if s.metrics != nil {
			s.metrics.UpstreamErrors.Add(1)
		}
		finalStatus = http.StatusBadGateway
		finalErr = err
		slog.Error("upstream select failed", "request_id", requestID, "model", modelID, "error", err)
		return fmt.Errorf("select upstream: %w", err)
	}
	if s.metrics != nil {
		s.metrics.IncrUpstream(u.Name())
	}
	finalUpstream = u.Name()
	slog.Info("upstream selected", "request_id", requestID, "model", modelID, "upstream", u.Name())

	var resp *http.Response
	for attempt := 0; attempt <= s.maxRetries; attempt++ {
		if attempt > 0 {
			if s.ipRotator != nil {
				if torErr := s.ipRotator.ForceNewIP(); torErr != nil {
					slog.Warn("tor: forced IP rotation failed", "request_id", requestID, "attempt", attempt, "error", torErr)
				} else {
					slog.Info("tor: IP rotated for retry", "request_id", requestID, "attempt", attempt)
				}
			}
			if s.metrics != nil {
				s.metrics.RetryCount.Add(1)
			}
			select {
			case <-ctx.Done():
				finalStatus = http.StatusGatewayTimeout
				finalErr = fmt.Errorf("client disconnected during retry")
				return finalErr
			case <-time.After(s.retryDelay):
			}
		}

		resp, err = u.ChatCompletion(ctx, domain.ChatRequest{Body: body, OriginalReq: r})
		if err != nil {
			if s.metrics != nil {
				s.metrics.UpstreamErrors.Add(1)
			}
			finalStatus = http.StatusBadGateway
			finalErr = err
			slog.Error("upstream request failed", "request_id", requestID, "upstream", u.Name(), "error", err)
			return fmt.Errorf("upstream request: %w", err)
		}

		if resp.StatusCode != http.StatusTooManyRequests {
			if resp.StatusCode >= 400 {
				if s.metrics != nil {
					s.metrics.UpstreamErrors.Add(1)
				}
				finalStatus = resp.StatusCode

				// Read provider error body for a more descriptive message
				errBody, readErr := io.ReadAll(io.LimitReader(resp.Body, 512))
				resp.Body.Close()
				if readErr == nil && len(errBody) > 0 {
					finalErr = fmt.Errorf("provider %d: %s", resp.StatusCode, extractProviderErr(errBody))
				} else {
					finalErr = fmt.Errorf("provider returned HTTP %d", resp.StatusCode)
				}
			}
			break
		}

		resp.Body.Close()
		slog.Warn("upstream returned 429, rotating IP and retrying",
			"request_id", requestID,
			"upstream", u.Name(),
			"attempt", attempt+1,
			"max_retry", s.maxRetries,
		)
	}
	if resp.StatusCode == http.StatusTooManyRequests {
		if s.metrics != nil {
			s.metrics.UpstreamErrors.Add(1)
		}
		finalStatus = resp.StatusCode
		finalErr = &MaxRetriesExceededError{ModelID: modelID}
		return finalErr
	}
	defer resp.Body.Close()

	slog.Info("upstream response", "request_id", requestID, "upstream", u.Name(), "status", resp.StatusCode)
	finalStatus = resp.StatusCode

	w.Header().Set("Access-Control-Allow-Origin", "*")
	usage, err := proxyinfra.NormalizeResponse(w, resp)
	if err != nil {
		slog.Warn("normalize response failed", "request_id", requestID, "upstream", u.Name(), "error", err)
	} else {
		finalTotalTokens = usage.Total
		finalPromptTokens = usage.Prompt
		finalComplTokens = usage.Completion
		if s.metrics != nil && usage.Total > 0 {
			s.metrics.TotalTokens.Add(int64(usage.Total))
		}
	}
	return nil
}

// extractProviderErr attempts to extract a human-readable error message from a
// provider's JSON error response body. Falls back to the raw body on failure.
func extractProviderErr(body []byte) string {
	trimmed := strings.TrimSpace(string(body))
	// Try to parse {"error": {"message": "..."}} (OpenAI-style)
	var errResp struct {
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error.Message != "" {
		return errResp.Error.Message
	}
	// Try {"message": "..."} (some providers)
	var msgResp struct {
		Message string `json:"message"`
	}
	if err := json.Unmarshal(body, &msgResp); err == nil && msgResp.Message != "" {
		return msgResp.Message
	}
	// Truncate raw body
	if len(trimmed) > 120 {
		trimmed = trimmed[:120] + "…"
	}
	return trimmed
}

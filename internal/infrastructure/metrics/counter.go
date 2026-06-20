package metrics

import (
	"log/slog"
	"sync"
	"sync/atomic"
)

// Metrics holds simple request counters for observability.
type Metrics struct {
	TotalRequests atomic.Int64
	RetryCount    atomic.Int64
	RateLimitHits atomic.Int64
	UpstreamErrors atomic.Int64
	InputTokens    atomic.Int64
	OutputTokens   atomic.Int64

	mu    sync.RWMutex
	perUp map[string]*atomic.Int64
}

// New creates a new Metrics instance.
func New() *Metrics {
	return &Metrics{
		perUp: make(map[string]*atomic.Int64),
	}
}

// IncrUpstream increments the counter for the given upstream name.
func (m *Metrics) IncrUpstream(name string) {
	m.mu.RLock()
	counter, ok := m.perUp[name]
	m.mu.RUnlock()
	if !ok {
		m.mu.Lock()
		counter, ok = m.perUp[name]
		if !ok {
			counter = &atomic.Int64{}
			m.perUp[name] = counter
		}
		m.mu.Unlock()
	}
	counter.Add(1)
}

// Snapshot returns a copy of all metrics as a map.
func (m *Metrics) Snapshot() map[string]any {
	return m.snapshot()
}

// Metrics is an alias for Snapshot, used to satisfy handler.MetricsProvider.
func (m *Metrics) Metrics() map[string]any {
	return m.snapshot()
}

func (m *Metrics) snapshot() map[string]any {
	m.mu.RLock()
	defer m.mu.RUnlock()

	upstreams := make(map[string]int64, len(m.perUp))
	for name, c := range m.perUp {
		upstreams[name] = c.Load()
	}

	return map[string]any{
		"total_requests":  m.TotalRequests.Load(),
		"retry_count":     m.RetryCount.Load(),
		"rate_limit_hits": m.RateLimitHits.Load(),
		"upstream_errors": m.UpstreamErrors.Load(),
		"input_tokens":    m.InputTokens.Load(),
		"output_tokens":   m.OutputTokens.Load(),
		"per_upstream":    upstreams,
	}
}

// LogStats logs the current metrics at info level.
func (m *Metrics) LogStats() {
	slog.Info("metrics", "data", m.Snapshot())
}

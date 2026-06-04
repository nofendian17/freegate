package collector

import (
	"context"
	"time"

	"freegate/internal/model"
	"freegate/internal/ringbuffer"
)

const (
	TimeseriesCapacity = 360
	TimeseriesInterval = 10 * time.Second
)

// Recorder holds the in-memory ring buffers that back the dashboard.
type Recorder struct {
	metricsFn  func() map[string]any
	requests   *ringbuffer.RingBuffer[model.RequestLogEntry]
	timeseries *ringbuffer.RingBuffer[model.TimeseriesEntry]
	modelsFn   func() []model.Model
	torIPFn    func() string
	startedAt  time.Time
}

// NewRecorder creates a Recorder bound to a metrics-snapshot function.
func NewRecorder(metricsFn func() map[string]any) *Recorder {
	return &Recorder{
		metricsFn:  metricsFn,
		requests:   ringbuffer.New[model.RequestLogEntry](100),
		timeseries: ringbuffer.New[model.TimeseriesEntry](TimeseriesCapacity),
		startedAt:  time.Now(),
	}
}

// RecordRequestLog stores a single request entry.
func (r *Recorder) RecordRequestLog(e model.RequestLogEntry) {
	if e.Ts.IsZero() {
		e.Ts = time.Now()
	}
	r.requests.Push(e)
}

// Requests returns the most recent requests, oldest first.
func (r *Recorder) Requests() []model.RequestLogEntry {
	return r.requests.Snapshot()
}

// Timeseries returns the timeseries history, oldest first.
func (r *Recorder) Timeseries() []model.TimeseriesEntry {
	return r.timeseries.Snapshot()
}

// Uptime returns the duration since the recorder was created.
func (r *Recorder) Uptime() time.Duration {
	return time.Since(r.startedAt)
}

// UptimeSeconds returns the uptime as whole seconds.
func (r *Recorder) UptimeSeconds() int64 {
	return int64(time.Since(r.startedAt).Seconds())
}

// StartedAtUnix returns the unix timestamp of recorder start.
func (r *Recorder) StartedAtUnix() int64 {
	return r.startedAt.Unix()
}

// SetModelsFunc wires a callback that returns the current model list.
func (r *Recorder) SetModelsFunc(fn func() []model.Model) {
	r.modelsFn = fn
}

// SetTorIPFunc wires a callback that returns the current Tor exit IP.
func (r *Recorder) SetTorIPFunc(fn func() string) {
	r.torIPFn = fn
}

// Models returns the current model list, or empty if no callback is set.
func (r *Recorder) Models() []model.Model {
	if r.modelsFn == nil {
		return nil
	}
	return r.modelsFn()
}

// TorIP returns the current Tor exit IP, or empty if no callback is set.
func (r *Recorder) TorIP() string {
	if r.torIPFn == nil {
		return ""
	}
	return r.torIPFn()
}

// Metrics returns the current metrics snapshot.
func (r *Recorder) Metrics() map[string]any {
	if r.metricsFn == nil {
		return nil
	}
	return r.metricsFn()
}

// Start launches the background timeseries sampler. Cancel ctx to stop it.
func (r *Recorder) Start(ctx context.Context) {
	go r.sampleLoop(ctx)
}

func (r *Recorder) sampleLoop(ctx context.Context) {
	ticker := time.NewTicker(TimeseriesInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case t := <-ticker.C:
			snap := map[string]any{}
			if r.metricsFn != nil {
				snap = r.metricsFn()
			}

			perUp := map[string]int{}
			if m, ok := snap["per_upstream"].(map[string]int64); ok {
				for k, v := range m {
					perUp[k] = int(v)
				}
			}

			entry := model.TimeseriesEntry{
				Ts:            t,
				TotalRequests: asInt64(snap["total_requests"]),
				Errors:        asInt64(snap["upstream_errors"]),
				Retries:       asInt64(snap["retry_count"]),
				RateLimitHits: asInt64(snap["rate_limit_hits"]),
				PerUpstream:   perUp,
			}
			r.timeseries.Push(entry)
		}
	}
}

func asInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	default:
		return 0
	}
}

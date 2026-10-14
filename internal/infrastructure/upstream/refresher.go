package upstream

import (
	"context"
	"log/slog"
	"time"
)

type RefreshFunc func(ctx context.Context) error

type Refresher struct {
	name       string
	refresh    RefreshFunc
	interval   time.Duration
	backoff    time.Duration
	maxBackoff time.Duration
	// onFailure runs after a failed refresh, before the backoff wait.
	// Upstreams wire pool flushing here so a retry never reuses the
	// blackholed pooled session that just failed. Nil = no hook.
	onFailure func()
	// quiet downgrades the failure log from warn to debug and skips the
	// success info log. For fail-open probes (e.g. the OpenCode client
	// version tracker) where failure is routine — offline, rate-limited,
	// or a below-minimum release — and the fallback is always correct.
	quiet bool
}

func NewRefresher(name string, fn RefreshFunc, interval time.Duration) *Refresher {
	if interval <= 0 {
		interval = ModelRefreshInterval
	}
	return &Refresher{
		name:       name,
		refresh:    fn,
		interval:   interval,
		backoff:    InitialBackoff,
		maxBackoff: MaxBackoff,
	}
}

// WithOnFailure registers a hook invoked after every failed refresh.
// It returns the refresher for chaining.
func (r *Refresher) WithOnFailure(fn func()) *Refresher {
	r.onFailure = fn
	return r
}

// WithQuiet downgrades the failure log from warn to debug and skips the
// success info log. It returns the refresher for chaining.
func (r *Refresher) WithQuiet() *Refresher {
	r.quiet = true
	return r
}

// Run blocks and runs the refresh loop until ctx is cancelled.
// Call it from a goroutine that is tracked by a WaitGroup.
func (r *Refresher) Run(ctx context.Context) {
	t := time.NewTimer(r.backoff)
	defer t.Stop()
	backoff := r.backoff
	for {
		select {
		case <-ctx.Done():
			slog.Debug("refresher stopped", "upstream", r.name)
			return
		default:
		}

		if err := r.refresh(ctx); err != nil {
			if r.quiet {
				slog.Debug("refresh failed", "upstream", r.name, "error", err)
			} else {
				slog.Warn("model refresh failed", "upstream", r.name, "error", err)
			}
			if r.onFailure != nil {
				r.onFailure()
			}
			t.Reset(backoff)
			select {
			case <-ctx.Done():
				return
			case <-t.C:
			}
			backoff = min(backoff*2, r.maxBackoff)
			continue
		}

		if !r.quiet {
			slog.Info("models refreshed", "upstream", r.name)
		}
		backoff = r.backoff

		t.Reset(r.interval)
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

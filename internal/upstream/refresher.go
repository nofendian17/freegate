package upstream

import (
	"context"
	"log/slog"
	"time"
)

type RefreshFunc func(ctx context.Context) error

type Refresher struct {
	name     string
	refresh  RefreshFunc
	interval time.Duration
	backoff  time.Duration
	maxBack  time.Duration
}

func NewRefresher(name string, fn RefreshFunc) *Refresher {
	return &Refresher{
		name:     name,
		refresh:  fn,
		interval: ModelRefreshInterval,
		backoff:  InitialBackoff,
		maxBack:  MaxBackoff,
	}
}

func (r *Refresher) Start(ctx context.Context) {
	go r.loop(ctx)
}

func (r *Refresher) loop(ctx context.Context) {
	backoff := r.backoff
	for {
		select {
		case <-ctx.Done():
			slog.Debug("refresher stopped", "upstream", r.name)
			return
		default:
		}

		if err := r.refresh(ctx); err != nil {
			slog.Warn("model refresh failed", "upstream", r.name, "error", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, r.maxBack)
			continue
		}

		slog.Info("models refreshed", "upstream", r.name)
		backoff = r.backoff

		select {
		case <-ctx.Done():
			return
		case <-time.After(r.interval):
		}
	}
}

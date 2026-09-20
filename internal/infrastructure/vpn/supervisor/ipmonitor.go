package supervisor

import (
	"context"
	"log/slog"
	"time"
)

func (s *Supervisor) ipRefresher(ctx context.Context) {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		select {
		case <-ctx.Done():
			slog.Info("vpngate: IP refresher stopped")
			return
		case <-ticker.C:
		}
		// Skip this tick if the previous probe cycle is still running
		// (single-flight): cycles can outlast the 15s tick on slow relays.
		if !s.refreshBusy.CompareAndSwap(false, true) {
			continue
		}
		func() {
			defer s.refreshBusy.Store(false)
			s.refreshCycle(ctx)
		}()
	}
}

// refreshCycle runs one connected-check + bounded-retry IP probe. It
// returns early when ctx is cancelled so Close is not delayed by retry
// sleeps.
func (s *Supervisor) refreshCycle(ctx context.Context) {
	s.mu.Lock()
	connected := s.connected && s.cur != nil
	s.mu.Unlock()
	if !connected {
		return
	}
	// Egress probes through free relays routinely need a few tries
	// before any echo service answers. Retry inside the tick so a
	// slow-but-working tunnel fills the label in this cycle instead
	// of waiting for the next tick.
	for attempt := 0; attempt < ipRefreshAttempts; attempt++ {
		ip, err := fetchPublicIP()
		if err == nil && ip != "" {
			s.mu.Lock()
			if s.connected && s.cur != nil {
				s.ip = ip
			}
			s.mu.Unlock()
			break
		}
		slog.Debug("vpngate: ip refresh attempt failed",
			"attempt", attempt+1, "error", err)
		if attempt+1 < ipRefreshAttempts && !sleepCtx(ctx, ipRefreshRetryDelay) {
			return
		}
	}
}

package upstream

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"freegate/internal/domain"
	"freegate/internal/infrastructure/providers"
)

func refreshInterval(sec int) time.Duration {
	if sec <= 0 || sec > 3600 {
		return 60 * time.Second
	}
	return time.Duration(sec) * time.Second
}

type ProviderManager struct {
	mu        sync.RWMutex
	store     *providers.Store
	tr        *http.Transport
	customs   map[string]*CustomUpstream
	intervals map[string]time.Duration
	runCtx    context.Context
	runs      map[string]context.CancelFunc
	cancel    context.CancelFunc
}

func NewProviderManager(s *providers.Store, tr *http.Transport) *ProviderManager {
	return &ProviderManager{store: s, tr: tr, customs: map[string]*CustomUpstream{}, intervals: map[string]time.Duration{}, runs: map[string]context.CancelFunc{}}
}

func (m *ProviderManager) Rebuild() error {
	rows, err := m.store.ListProviders()
	if err != nil {
		return err
	}
	next := map[string]*CustomUpstream{}
	nextIntervals := map[string]time.Duration{}
	for _, r := range rows {
		if !r.Enabled {
			continue
		}
		full, err := m.store.GetProviderRaw(r.ID)
		if err != nil {
			return err
		}
		if full.Models != nil && len(full.Models) == 0 {
			slog.Warn("custom provider has no models selected, it will not route", "provider", full.Name)
		}
		fresh := NewCustomUpstream(full.Name, full.BaseURL, full.APIKeys, full.Headers, full.Models, m.tr)
		m.mu.RLock()
		old := m.customs[r.Name]
		m.mu.RUnlock()
		if old != nil {
			// Carry over fresh metadata for still-selected models only;
			// deselected models must not leak back in via the old cache.
			// The constructor already seeded bare entries for the new
			// selection, so Match works even before the next fetch.
			keep := make([]domain.Model, 0)
			for _, om := range old.Models() {
				if fresh.Match(om.ID) {
					keep = append(keep, om)
				}
			}
			if len(keep) > 0 {
				fresh.SeedModels(keep)
			}
		}
		next[r.Name] = fresh
		nextIntervals[r.Name] = refreshInterval(full.RefreshSec)
	}
	m.mu.Lock()
	m.customs = next
	m.intervals = nextIntervals
	started := m.runCtx != nil
	m.mu.Unlock()
	if started {
		m.reconcile()
	}
	return nil
}

// Warm synchronously fetches the model catalog for one live custom
// upstream, so a newly added or updated provider routes correctly
// immediately instead of waiting for the background refresher's first
// tick. ListModels stores the catalog in the upstream's cache as a side
// effect; the ComboRouter holds the same object pointers, so no further
// rebuild is needed. Best-effort: callers log the error and continue.
func (m *ProviderManager) Warm(name string) ([]domain.Model, error) {
	m.mu.RLock()
	u := m.customs[name]
	m.mu.RUnlock()
	if u == nil {
		return nil, fmt.Errorf("unknown custom provider %q", name)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	return u.ListModels(ctx)
}

func (m *ProviderManager) All() []*CustomUpstream {
	m.mu.RLock()
	defer m.mu.RUnlock()
	out := make([]*CustomUpstream, 0, len(m.customs))
	for _, u := range m.customs {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// startOne launches a background refresher for a custom upstream. It must
// be called with m.mu held because it writes to m.runs. The goroutine
// launch (go u.Start) is non-blocking, so lock hold time stays O(n) where
// n is the number of upstreams — safe for the small counts involved.
func (m *ProviderManager) startOne(name string, u *CustomUpstream, d time.Duration) {
	if d <= 0 {
		d = 60 * time.Second
	}
	child, cancel := context.WithCancel(m.runCtx)
	m.runs[name] = cancel
	go u.Start(child, d)
}

func (m *ProviderManager) reconcile() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, cancel := range m.runs {
		if _, ok := m.customs[name]; !ok {
			cancel()
			delete(m.runs, name)
		}
	}
	if m.runCtx == nil {
		return
	}
	for name, u := range m.customs {
		if cancel, ok := m.runs[name]; ok {
			cancel()
		}
		m.startOne(name, u, m.intervals[name])
	}
}

func (m *ProviderManager) Start(ctx context.Context) {
	bg, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.runCtx = bg
	m.cancel = cancel
	snap := make([]struct {
		name string
		u    *CustomUpstream
		d    time.Duration
	}, 0)
	for name, u := range m.customs {
		snap = append(snap, struct {
			name string
			u    *CustomUpstream
			d    time.Duration
		}{name, u, m.intervals[name]})
	}
	for _, s := range snap {
		m.startOne(s.name, s.u, s.d)
	}
	m.mu.Unlock()
}

func (m *ProviderManager) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, c := range m.runs {
		c()
		delete(m.runs, name)
	}
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.runCtx = nil
}

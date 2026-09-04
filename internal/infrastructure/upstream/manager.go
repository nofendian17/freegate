package upstream

import (
	"context"
	"net/http"
	"sort"
	"sync"
	"time"

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
		fresh := NewCustomUpstream(full.Name, full.BaseURL, full.APIKeys, full.Headers, full.ModelAllow, full.ModelBlock, m.tr)
		m.mu.RLock()
		old := m.customs[r.Name]
		m.mu.RUnlock()
		if old != nil {
			fresh.SeedModels(old.Models())
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

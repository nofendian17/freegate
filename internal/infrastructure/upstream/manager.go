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
	cancel    context.CancelFunc
}

func NewProviderManager(s *providers.Store, tr *http.Transport) *ProviderManager {
	return &ProviderManager{store: s, tr: tr, customs: map[string]*CustomUpstream{}, intervals: map[string]time.Duration{}}
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
	m.mu.Unlock()
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

func (m *ProviderManager) Start(ctx context.Context) {
	bg, cancel := context.WithCancel(ctx)
	m.mu.Lock()
	m.cancel = cancel
	snap := make([]struct {
		u *CustomUpstream
		d time.Duration
	}, 0)
	for name, u := range m.customs {
		d := m.intervals[name]
		if d <= 0 {
			d = 60 * time.Second
		}
		snap = append(snap, struct {
			u *CustomUpstream
			d time.Duration
		}{u, d})
	}
	m.mu.Unlock()
	for _, s := range snap {
		go s.u.Start(bg, s.d)
	}
}

func (m *ProviderManager) Stop() {
	m.mu.RLock()
	c := m.cancel
	m.mu.RUnlock()
	if c != nil {
		c()
	}
}

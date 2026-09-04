package upstream

import (
	"sync"

	"freegate/internal/domain"
	"freegate/internal/model"
)

type ComboRouter struct {
	mu      sync.RWMutex
	legacy  *Router
	combos  map[string]*ComboUpstream
	customs []domain.Upstream
}

func NewComboRouter(legacy *Router) *ComboRouter {
	return &ComboRouter{legacy: legacy, combos: map[string]*ComboUpstream{}}
}

type ComboTierRow struct {
	Name      string
	Providers []string
}

func (c *ComboRouter) RebuildCombos(rows []ComboTierRow, lookup func(string) domain.Upstream) {
	next := make(map[string]*ComboUpstream, len(rows))
	for _, r := range rows {
		if r.Name == "" {
			continue
		}
		var tiers []domain.Upstream
		for _, p := range r.Providers {
			if u := lookup(p); u != nil {
				tiers = append(tiers, u)
			}
		}
		if len(tiers) == 0 {
			continue
		}
		next[r.Name] = NewComboUpstream(r.Name, tiers)
	}
	c.mu.Lock()
	c.combos = next
	c.mu.Unlock()
}

func (c *ComboRouter) SetCustoms(all []domain.Upstream) {
	c.mu.Lock()
	c.customs = append([]domain.Upstream(nil), all...)
	c.mu.Unlock()
}

func (c *ComboRouter) SelectChain(modelID string) []domain.Upstream {
	c.mu.RLock()
	combo := c.combos[modelID]
	customs := append([]domain.Upstream(nil), c.customs...)
	legacy := c.legacy
	c.mu.RUnlock()
	if combo != nil {
		return []domain.Upstream{combo}
	}
	var out []domain.Upstream
	for _, u := range customs {
		if u != nil && u.Match(modelID) {
			out = append(out, u)
		}
	}
	if len(out) > 0 {
		return out
	}
	if legacy != nil {
		if u := legacy.Select(modelID); u != nil {
			return []domain.Upstream{u}
		}
	}
	return nil
}

func (c *ComboRouter) Select(modelID string) domain.Upstream {
	if chain := c.SelectChain(modelID); len(chain) > 0 {
		return chain[0]
	}
	return nil
}

func (c *ComboRouter) AllModels() []model.Model {
	seen := map[string]bool{}
	var out []model.Model
	if c.legacy != nil {
		for _, m := range c.legacy.AllModels() {
			if !seen[m.ID] {
				seen[m.ID] = true
				out = append(out, m)
			}
		}
	}
	c.mu.RLock()
	combos := make([]*ComboUpstream, 0, len(c.combos))
	for _, cu := range c.combos {
		combos = append(combos, cu)
	}
	customs := append([]domain.Upstream(nil), c.customs...)
	c.mu.RUnlock()
	for _, cu := range combos {
		for _, m := range cu.Models() {
			if !seen[m.ID] {
				seen[m.ID] = true
				out = append(out, m)
			}
		}
	}
	for _, u := range customs {
		for _, m := range u.Models() {
			if !seen[m.ID] {
				seen[m.ID] = true
				out = append(out, m)
			}
		}
	}
	return out
}

func (c *ComboRouter) IsReady() bool {
	if c.legacy != nil && c.legacy.IsReady() {
		return true
	}
	c.mu.RLock()
	customs := append([]domain.Upstream(nil), c.customs...)
	c.mu.RUnlock()
	for _, u := range customs {
		if len(u.Models()) > 0 {
			return true
		}
	}
	return false
}

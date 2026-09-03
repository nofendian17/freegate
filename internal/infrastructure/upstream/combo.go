package upstream

import (
	"sync"

	"freegate/internal/domain"
	"freegate/internal/model"
)

type ComboRouter struct {
	mu     sync.RWMutex
	legacy *Router
	chain  []domain.Upstream
	byName map[string]domain.Upstream
}

func NewComboRouter(legacy *Router) *ComboRouter {
	return &ComboRouter{legacy: legacy, byName: map[string]domain.Upstream{}}
}

func (c *ComboRouter) Register(u domain.Upstream) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.byName[u.Name()] = u
}

func (c *ComboRouter) SetChain(chain []domain.Upstream) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.chain = append([]domain.Upstream(nil), chain...)
}

func (c *ComboRouter) SelectChain(modelID string) []domain.Upstream {
	c.mu.RLock()
	chain := append([]domain.Upstream(nil), c.chain...)
	legacy := c.legacy
	c.mu.RUnlock()
	var out []domain.Upstream
	for _, u := range chain {
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
	defer c.mu.RUnlock()
	for _, u := range c.chain {
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
	defer c.mu.RUnlock()
	for _, u := range c.chain {
		if len(u.Models()) > 0 {
			return true
		}
	}
	return false
}

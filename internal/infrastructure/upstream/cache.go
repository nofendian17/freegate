package upstream

import (
	"sync"

	"freegate/internal/model"
)

type ModelCache struct {
	mu     sync.RWMutex
	models []model.Model
	index  map[string]struct{}
}

func NewModelCache() *ModelCache {
	return &ModelCache{index: make(map[string]struct{})}
}

func (c *ModelCache) Set(models []model.Model) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.models = models
	idx := make(map[string]struct{}, len(models))
	for _, m := range models {
		idx[m.ID] = struct{}{}
	}
	c.index = idx
}

func (c *ModelCache) Get() []model.Model {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if c.models == nil {
		return nil
	}
	result := make([]model.Model, len(c.models))
	copy(result, c.models)
	return result
}

func (c *ModelCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.models)
}

func (c *ModelCache) Has(id string) bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	_, ok := c.index[id]
	return ok
}

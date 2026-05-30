package upstream

import (
	"sync"

	"freegate/internal/model"
)

type ModelCache struct {
	mu     sync.RWMutex
	models []model.Model
}

func NewModelCache() *ModelCache {
	return &ModelCache{}
}

func (c *ModelCache) Set(models []model.Model) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.models = models
}

func (c *ModelCache) Get() []model.Model {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.models
}

func (c *ModelCache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.models)
}

package application

import (
	"sync"

	"freegate/internal/model"
)

// RouterRegistry is the read-only model catalog from a single router
// (or any compatible source). ModelService aggregates one or more of
// these into a unified view.
type RouterRegistry interface {
	AllModels() []model.Model
	IsReady() bool
}

// ModelService merges model listings from one or more routers and
// reports aggregate readiness.
type ModelService struct {
	mu      sync.RWMutex
	routers []RouterRegistry
}

// NewModelService creates a ModelService from one or more routers.
func NewModelService(routers ...RouterRegistry) *ModelService {
	return &ModelService{routers: routers}
}

// AddRouter appends a router to the aggregated set.
func (s *ModelService) AddRouter(r RouterRegistry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.routers = append(s.routers, r)
}

// AllModels returns the deduplicated union of models from all routers.
func (s *ModelService) AllModels() []model.Model {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Count total models across all routers to pre-allocate
	total := 0
	for _, r := range s.routers {
		total += len(r.AllModels())
	}
	seen := make(map[string]bool, total)
	out := make([]model.Model, 0, total)
	for _, r := range s.routers {
		for _, m := range r.AllModels() {
			if !seen[m.ID] {
				seen[m.ID] = true
				out = append(out, m)
			}
		}
	}
	return out
}

// IsReady reports true if any registered router has models loaded.
func (s *ModelService) IsReady() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.routers {
		if r.IsReady() {
			return true
		}
	}
	return false
}

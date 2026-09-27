package application

import (
	"sync"

	"freegate/internal/domain"
)

// RouterRegistry is the read-only model catalog from a single router
// (or any compatible source). ModelService aggregates one or more of
// these into a unified view.
type RouterRegistry interface {
	AllModels() []domain.Model
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
// Each router is queried once (not twice for sizing then merging), and the
// router list itself is copied under lock so aggregation runs lock-free:
// hot /v1/models polls no longer block AddRouter/Rebuild writers while
// copying full catalog slices.
func (s *ModelService) AllModels() []domain.Model {
	s.mu.RLock()
	routers := make([]RouterRegistry, len(s.routers))
	copy(routers, s.routers)
	s.mu.RUnlock()

	snapshots := make([][]domain.Model, 0, len(routers))
	total := 0
	for _, r := range routers {
		models := r.AllModels()
		snapshots = append(snapshots, models)
		total += len(models)
	}
	seen := make(map[string]bool, total)
	out := make([]domain.Model, 0, total)
	for _, models := range snapshots {
		for _, m := range models {
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

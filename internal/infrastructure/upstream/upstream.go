package upstream

import (
	"time"

	"freegate/internal/domain"
	"freegate/internal/model"
)

const (
	ModelRefreshInterval = 60 * time.Second
	InitialBackoff       = time.Second
	MaxBackoff           = 5 * time.Minute
)

type Upstream = domain.Upstream

type Router struct {
	upstreams       []domain.Upstream
	defaultUpstream domain.Upstream
}

func NewRouter(defaultUpstream domain.Upstream, upstreams ...domain.Upstream) *Router {
	return &Router{
		upstreams:       upstreams,
		defaultUpstream: defaultUpstream,
	}
}

func (r *Router) Select(modelID string) domain.Upstream {
	for _, u := range r.upstreams {
		if u.Match(modelID) {
			return u
		}
	}
	return r.defaultUpstream
}

func (r *Router) AllModels() []model.Model {
	seen := make(map[string]bool)
	var result []model.Model

	for _, u := range r.upstreams {
		for _, m := range u.Models() {
			if !seen[m.ID] {
				seen[m.ID] = true
				result = append(result, m)
			}
		}
	}

	for _, m := range r.defaultUpstream.Models() {
		if !seen[m.ID] {
			seen[m.ID] = true
			result = append(result, m)
		}
	}

	return result
}

func (r *Router) IsReady() bool {
	for _, u := range r.upstreams {
		if len(u.Models()) > 0 {
			return true
		}
	}
	return len(r.defaultUpstream.Models()) > 0
}

package upstream

import (
	"context"
	"net/http"
	"time"

	"freegate/internal/model"
)

const (
	ModelRefreshInterval = 60 * time.Second
	InitialBackoff       = time.Second
	MaxBackoff           = 5 * time.Minute
)

type Upstream interface {
	Name() string
	Match(modelID string) bool
	ListModels(ctx context.Context) ([]model.Model, error)
	ChatCompletion(ctx context.Context, body []byte) (*http.Response, error)
	Models() []model.Model
	Start(ctx context.Context, refreshInterval time.Duration)
}

type Router struct {
	upstreams       []Upstream
	defaultUpstream Upstream
}

func NewRouter(defaultUpstream Upstream, upstreams ...Upstream) *Router {
	return &Router{
		upstreams:       upstreams,
		defaultUpstream: defaultUpstream,
	}
}

func (r *Router) Select(modelID string) Upstream {
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

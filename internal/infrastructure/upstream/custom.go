package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"freegate/internal/domain"
	"freegate/internal/model"
)

type CustomUpstream struct {
	name   string
	client *HTTPClient
	cache  *ModelCache
	allow  []string
	block  []string
}

func NewCustomUpstream(name, baseURL string, keys []string, headers map[string]string, allow, block []string, tr *http.Transport) *CustomUpstream {
	if headers == nil {
		headers = map[string]string{}
	}
	lower := func(in []string) []string {
		out := make([]string, 0, len(in))
		for _, s := range in {
			if s = strings.ToLower(strings.TrimSpace(s)); s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return &CustomUpstream{
		name:   name,
		client: NewHTTPClientWithTransport(baseURL, keys, headers, tr),
		cache:  NewModelCache(),
		allow:  lower(allow),
		block:  lower(block),
	}
}

func (u *CustomUpstream) Name() string { return "custom:" + u.name }

func (u *CustomUpstream) Match(modelID string) bool {
	m := strings.ToLower(modelID)
	for _, b := range u.block {
		if strings.Contains(m, b) {
			return false
		}
	}
	if len(u.allow) > 0 {
		hit := false
		for _, a := range u.allow {
			if strings.Contains(m, a) {
				hit = true
				break
			}
		}
		if !hit {
			return false
		}
	}
	return u.cache.Has(modelID)
}

func (u *CustomUpstream) ListModels(ctx context.Context) ([]model.Model, error) {
	body, err := u.client.ReadAll(ctx, "/models")
	if err != nil {
		return nil, fmt.Errorf("custom %s: fetch models: %w", u.name, err)
	}
	var list struct {
		Data []struct {
			ID      string `json:"id"`
			Object  string `json:"object"`
			Created int64  `json:"created"`
			OwnedBy string `json:"owned_by"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("custom %s: parse models: %w", u.name, err)
	}
	seen := map[string]bool{}
	out := make([]model.Model, 0, len(list.Data))
	for _, m := range list.Data {
		if m.ID == "" || seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		obj := m.Object
		if obj == "" {
			obj = "model"
		}
		owner := m.OwnedBy
		if owner == "" {
			owner = "custom:" + u.name
		}
		out = append(out, model.Model{ID: m.ID, Object: obj, Created: m.Created, OwnedBy: owner, IsFree: true, Provider: "custom:" + u.name})
	}
	u.cache.Set(out)
	return out, nil
}

func (u *CustomUpstream) Models() []model.Model { return u.cache.Get() }

func (u *CustomUpstream) SeedModels(m []model.Model) { u.cache.Set(m) }

func (u *CustomUpstream) ChatCompletion(ctx context.Context, body []byte) (*domain.UpstreamResponse, error) {
	resp, err := u.client.Post(ctx, "/chat/completions", body)
	if err != nil {
		return nil, err
	}
	return domain.NewUpstreamResponse(resp), nil
}

func (u *CustomUpstream) Start(ctx context.Context, refreshInterval time.Duration) {
	NewRefresher("custom:"+u.name, func(ctx context.Context) error {
		_, err := u.ListModels(ctx)
		return err
	}, refreshInterval).Run(ctx)
}

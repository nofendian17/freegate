package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"freegate/internal/model"
)

type OpenCodeUpstream struct {
	client *HTTPClient
	cache  *ModelCache
}

func NewOpenCodeUpstream(baseURL, apiKey, socksAddr string) *OpenCodeUpstream {
	headers := map[string]string{"x-opencode-client": "desktop"}
	return &OpenCodeUpstream{
		client: NewHTTPClient(baseURL, apiKey, socksAddr, headers),
		cache:  NewModelCache(),
	}
}

func (o *OpenCodeUpstream) Name() string {
	return "opencode"
}

func (o *OpenCodeUpstream) Start(ctx context.Context) {
	refresher := NewRefresher("opencode", func(ctx context.Context) error {
		models, err := o.ListModels(ctx)
		if err != nil {
			return err
		}
		o.cache.Set(models)
		return nil
	})
	refresher.Start(ctx)
}

func (o *OpenCodeUpstream) Match(modelID string) bool {
	return true
}

func (o *OpenCodeUpstream) ListModels(ctx context.Context) ([]model.Model, error) {
	body, err := o.client.ReadAll(ctx, "/models")
	if err != nil {
		return nil, fmt.Errorf("opencode: fetch models: %w", err)
	}

	var list model.OpenCodeModelList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("opencode: parse models: %w", err)
	}

	var free []model.Model
	seen := make(map[string]bool)
	for _, m := range list.Data {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		if m.Cost == "0" || strings.HasSuffix(m.ID, "-free") {
			free = append(free, model.Model{
				ID:       m.ID,
				Object:   m.Object,
				Created:  m.Created,
				OwnedBy:  m.OwnedBy,
				IsFree:   true,
				Provider: "opencode",
			})
		}
	}

	return free, nil
}

func (o *OpenCodeUpstream) Models() []model.Model {
	return o.cache.Get()
}

func (o *OpenCodeUpstream) ChatCompletion(ctx context.Context, body []byte) (*http.Response, error) {
	return o.client.Post(ctx, "/chat/completions", body)
}

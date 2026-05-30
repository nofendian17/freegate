package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"freegate/internal/model"
)

type KiloUpstream struct {
	client   *HTTPClient
	cache    *ModelCache
	prefixes []string
}

func NewKiloUpstream(baseURL, apiKey, socksAddr string, prefixes []string) *KiloUpstream {
	return &KiloUpstream{
		client:   NewHTTPClient(baseURL, apiKey, socksAddr, nil),
		cache:    NewModelCache(),
		prefixes: prefixes,
	}
}

func (k *KiloUpstream) Name() string {
	return "kilo"
}

func (k *KiloUpstream) Start(ctx context.Context) {
	refresher := NewRefresher("kilo", func(ctx context.Context) error {
		models, err := k.ListModels(ctx)
		if err != nil {
			return err
		}
		k.cache.Set(models)
		return nil
	})
	refresher.Start(ctx)
}

func (k *KiloUpstream) Match(modelID string) bool {
	if strings.HasSuffix(modelID, ":free") {
		return true
	}
	for _, p := range k.prefixes {
		if strings.HasPrefix(modelID, p) {
			return true
		}
	}
	return false
}

func (k *KiloUpstream) ListModels(ctx context.Context) ([]model.Model, error) {
	body, err := k.client.ReadAll(ctx, "/models")
	if err != nil {
		return nil, fmt.Errorf("kilo: fetch models: %w", err)
	}

	var list model.KiloModelList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("kilo: parse models: %w", err)
	}

	var free []model.Model
	seen := make(map[string]bool)
	for _, m := range list.Data {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		if m.IsFree {
			free = append(free, model.Model{
				ID:       m.ID,
				Object:   "model",
				Created:  m.Created,
				OwnedBy:  "kilo",
				IsFree:   true,
				Provider: "kilo",
			})
		}
	}

	return free, nil
}

func (k *KiloUpstream) Models() []model.Model {
	return k.cache.Get()
}

func (k *KiloUpstream) ChatCompletion(ctx context.Context, body []byte) (*http.Response, error) {
	return k.client.Post(ctx, "/chat/completions", body)
}

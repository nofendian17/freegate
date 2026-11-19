package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"freegate/internal/domain"
	"freegate/internal/infrastructure/registry"
	"freegate/internal/infrastructure/upstream/types"
)

type KiloUpstream struct {
	client *HTTPClient
	cache  *ModelCache
	baseTr *http.Transport
	warpTr *http.Transport
}

func NewKiloUpstreamWithTransport(baseURL, apiKey string, tr *http.Transport) *KiloUpstream {
	return NewKiloUpstreamWithEgress(baseURL, apiKey, tr, nil)
}

func NewKiloUpstreamWithEgress(baseURL, apiKey string, tr *http.Transport, eg Egress) *KiloUpstream {
	if tr == nil {
		tr = NewTransport()
	}
	return &KiloUpstream{
		client: NewHTTPClientWithTransport(baseURL, []string{apiKey}, nil, tr),
		cache:  NewModelCache(),
		baseTr: tr,
		warpTr: NewWarpTransport(eg, tr),
	}
}

func (k *KiloUpstream) Name() string {
	return "kilo"
}

// SetRelayPools overrides edge-relay behavior: nil follows the global
// rotation, an empty slice means direct, otherwise pinned to the pools.
func (k *KiloUpstream) SetRelayPools(pools []RelayPool) { k.client.SetRelayPools(pools) }

// SetProxy sets both the proxy mode and the relay pools for Kilo.
// When mode is "warp", traffic routes through the WARP exit pool.
// Otherwise it routes directly or via the given relay pools.
func (k *KiloUpstream) SetProxy(mode string, pools []RelayPool) {
	if registry.NormalizeProxyMode(mode) == registry.ProxyModeWarp {
		k.client.SetTransport(k.warpTr)
	} else {
		k.client.SetTransport(k.baseTr)
	}
	k.client.SetRelayPools(pools)
}

func (k *KiloUpstream) Start(ctx context.Context, refreshInterval time.Duration) {
	refresher := NewRefresher("kilo", func(ctx context.Context) error {
		models, err := k.ListModels(ctx)
		if err != nil {
			return err
		}
		k.cache.Set(models)
		return nil
	}, refreshInterval).WithOnFailure(k.client.CloseIdleConnections)
	refresher.Run(ctx)
}

func (k *KiloUpstream) Match(modelID string) bool {
	if modelID == "" {
		return false
	}
	return k.cache.Has(modelID)
}

func (k *KiloUpstream) ListModels(ctx context.Context) ([]domain.Model, error) {
	body, err := k.client.ReadAll(ctx, "/models")
	if err != nil {
		return nil, fmt.Errorf("kilo: fetch models: %w", err)
	}

	var list types.KiloModelList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("kilo: parse models: %w", err)
	}

	var free []domain.Model
	seen := make(map[string]bool)
	for _, m := range list.Data {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		if m.IsFree {
			free = append(free, domain.Model{
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

func (k *KiloUpstream) Models() []domain.Model {
	return k.cache.Get()
}

func (k *KiloUpstream) ChatCompletion(ctx context.Context, body []byte) (*domain.UpstreamResponse, error) {
	resp, err := k.client.Post(ctx, "/chat/completions", body)
	if err != nil {
		return nil, err
	}
	return domain.NewUpstreamResponse(resp), nil
}

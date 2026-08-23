package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"freegate/internal/domain"
	"freegate/internal/infrastructure/upstream/types"
	"freegate/internal/model"
)

// LLM7 (api.llm7.io) is a keyless free gateway: any non-empty bearer token
// works, "unused" by convention. The live catalog replaces the last-known
// list on every refresh; an unreachable catalog keeps the last-known models.
type LLM7Upstream struct {
	client *HTTPClient
	cache  *ModelCache
}

func NewLLM7Upstream(baseURL string, d *Dialer) *LLM7Upstream {
	return NewLLM7UpstreamWithTransport(baseURL, NewTransport(d))
}

func NewLLM7UpstreamWithTransport(baseURL string, tr *http.Transport) *LLM7Upstream {
	return &LLM7Upstream{
		client: NewHTTPClientWithTransport(baseURL, []string{"unused"}, nil, tr),
		cache:  NewModelCache(),
	}
}

func (u *LLM7Upstream) Name() string { return "llm7" }

func (u *LLM7Upstream) Start(ctx context.Context, refreshInterval time.Duration) {
	refresher := NewRefresher("llm7", func(ctx context.Context) error {
		models, err := u.ListModels(ctx)
		if err != nil {
			return err
		}
		u.cache.Set(models)
		return nil
	}, refreshInterval)
	refresher.Run(ctx)
}

func (u *LLM7Upstream) Match(modelID string) bool {
	if modelID == "" {
		return false
	}
	return u.cache.Has(modelID)
}

// llm7Free reports whether the catalog entry is usable anonymously without a
// per-token charge: usage-based models are excluded, and only the "turbo"
// tier is considered free when the flag is absent.
func llm7Free(m types.LLM7Model) bool {
	if m.UsageBasedOnly != nil {
		return !*m.UsageBasedOnly
	}
	return strings.EqualFold(m.Tier, "turbo")
}

func (u *LLM7Upstream) ListModels(ctx context.Context) ([]model.Model, error) {
	body, err := u.client.ReadAll(ctx, "/models")
	if err != nil {
		return nil, fmt.Errorf("llm7: fetch models: %w", err)
	}

	var list types.LLM7ModelList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("llm7: parse models: %w", err)
	}

	var out []model.Model
	seen := make(map[string]bool)
	for _, m := range list.Data {
		if seen[m.ID] || !llm7Free(m) {
			continue
		}
		seen[m.ID] = true
		out = append(out, model.Model{
			ID:       m.ID,
			Object:   "model",
			OwnedBy:  "llm7",
			IsFree:   true,
			Provider: "llm7",
		})
	}
	return out, nil
}

func (u *LLM7Upstream) Models() []model.Model { return u.cache.Get() }

func (u *LLM7Upstream) ChatCompletion(ctx context.Context, body []byte) (*domain.UpstreamResponse, error) {
	resp, err := u.client.Post(ctx, "/chat/completions", body)
	if err != nil {
		return nil, err
	}
	return domain.NewUpstreamResponse(resp), nil
}

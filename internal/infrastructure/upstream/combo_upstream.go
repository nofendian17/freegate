package upstream

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"freegate/internal/domain"
	"freegate/internal/model"
)

var _ domain.Upstream = (*ComboUpstream)(nil)

type ComboUpstream struct {
	name  string
	tiers []domain.Upstream
}

func NewComboUpstream(name string, tiers []domain.Upstream) *ComboUpstream {
	kept := make([]domain.Upstream, 0, len(tiers))
	for _, u := range tiers {
		if u != nil {
			kept = append(kept, u)
		}
	}
	return &ComboUpstream{name: name, tiers: kept}
}

func (u *ComboUpstream) Name() string { return "combo:" + u.name }

func (u *ComboUpstream) Match(modelID string) bool { return modelID == u.name }

func (u *ComboUpstream) Models() []model.Model {
	return []model.Model{{ID: u.name, Object: "model", OwnedBy: "combo", IsFree: true, Provider: "combo:" + u.name}}
}

func (u *ComboUpstream) ListModels(ctx context.Context) ([]model.Model, error) {
	return u.Models(), nil
}

func (u *ComboUpstream) Start(ctx context.Context, refreshInterval time.Duration) {}

func (u *ComboUpstream) ChatCompletion(ctx context.Context, body []byte) (*domain.UpstreamResponse, error) {
	if len(u.tiers) == 0 {
		return nil, fmt.Errorf("combo %q has no tiers", u.name)
	}
	var lastErr error
	for i, tier := range u.tiers {
		last := i == len(u.tiers)-1
		resp, err := tier.ChatCompletion(ctx, body)
		if err != nil {
			lastErr = err
			if !last {
				slog.Warn("combo tier failed, trying next", "combo", u.name, "tier", tier.Name(), "error", err)
				continue
			}
			return nil, fmt.Errorf("combo %q: %w", u.name, err)
		}
		if resp == nil {
			lastErr = fmt.Errorf("combo %q tier %q returned nil response", u.name, tier.Name())
			if last {
				return nil, fmt.Errorf("combo %q: %w", u.name, lastErr)
			}
			slog.Warn("combo tier returned nil response, trying next", "combo", u.name, "tier", tier.Name())
			continue
		}
		if (resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500) && !last {
			slog.Warn("combo tier retryable status, trying next", "combo", u.name, "tier", tier.Name(), "status", resp.StatusCode)
			resp.Close()
			continue
		}
		return resp, nil
	}
	return nil, fmt.Errorf("combo %q: %w", u.name, lastErr)
}

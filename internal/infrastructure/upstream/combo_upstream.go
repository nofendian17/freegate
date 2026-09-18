package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"freegate/internal/domain"
	"freegate/internal/translate"
)

var _ domain.Upstream = (*ComboUpstream)(nil)

type ComboTier struct {
	Upstream domain.Upstream
	Model    string
}

type ComboUpstream struct {
	name  string
	tiers []ComboTier
}

func NewComboUpstream(name string, tiers []ComboTier) *ComboUpstream {
	kept := make([]ComboTier, 0, len(tiers))
	for _, t := range tiers {
		if t.Upstream != nil {
			kept = append(kept, t)
		}
	}
	return &ComboUpstream{name: name, tiers: kept}
}

func (u *ComboUpstream) Name() string { return "combo:" + u.name }

func (u *ComboUpstream) Match(modelID string) bool { return modelID == u.name }

func (u *ComboUpstream) Models() []domain.Model {
	return []domain.Model{{ID: u.name, Object: "model", OwnedBy: "combo", IsFree: true, Provider: "combo:" + u.name}}
}

func (u *ComboUpstream) ListModels(ctx context.Context) ([]domain.Model, error) {
	return u.Models(), nil
}

func (u *ComboUpstream) Start(ctx context.Context, refreshInterval time.Duration) {}

func (u *ComboUpstream) ChatCompletion(ctx context.Context, body []byte) (*domain.UpstreamResponse, error) {
	if len(u.tiers) == 0 {
		return nil, fmt.Errorf("combo %q has no tiers", u.name)
	}
	source := translate.RequestFormat(ctx, body)
	var lastErr error
	for i, tier := range u.tiers {
		last := i == len(u.tiers)-1
		out, err := rewriteModel(body, tier.Model)
		target := translate.FormatOpenAI
		if provider, ok := tier.Upstream.(interface {
			RequestFormat([]byte) translate.Format
		}); ok {
			target = provider.RequestFormat(out)
		}
		if err == nil && source != target {
			out, err = translate.Request(out, source, target)
		}
		if err == nil && target == translate.FormatOpenAI {
			var tokens []string
			out, tokens, err = translate.PrepareForUpstreamWithModel(out, tier.Model)
			if len(tokens) > 0 {
				slog.Debug("combo tier normalized", "combo", u.name, "tier", tier.Upstream.Name(), "normalized", strings.Join(tokens, ","))
			}
		}
		if err == nil && target == translate.FormatClaude {
			var tokens []string
			out, tokens, err = translate.NormalizeClaudeContent(out)
			if len(tokens) > 0 {
				slog.Debug("combo tier normalized", "combo", u.name, "tier", tier.Upstream.Name(), "normalized", strings.Join(tokens, ","))
			}
		}
		if err != nil {
			lastErr = err
			if !last {
				slog.Warn("combo tier body rewrite failed, trying next", "combo", u.name, "tier", tier.Upstream.Name(), "error", err)
				continue
			}
			return nil, fmt.Errorf("combo %q: %w", u.name, err)
		}
		resp, err := tier.Upstream.ChatCompletion(translate.WithRequestFormat(ctx, target), out)
		if err != nil {
			lastErr = err
			if !last {
				slog.Warn("combo tier failed, trying next", "combo", u.name, "tier", tier.Upstream.Name(), "error", err)
				continue
			}
			return nil, fmt.Errorf("combo %q: %w", u.name, err)
		}
		if resp == nil {
			lastErr = fmt.Errorf("combo %q tier %q returned nil response", u.name, tier.Upstream.Name())
			if last {
				return nil, fmt.Errorf("combo %q: %w", u.name, lastErr)
			}
			slog.Warn("combo tier returned nil response, trying next", "combo", u.name, "tier", tier.Upstream.Name())
			continue
		}
		if isTierRetryable(resp) && !last {
			slog.Warn("combo tier retryable status, trying next", "combo", u.name, "tier", tier.Upstream.Name(), "status", resp.StatusCode)
			resp.Close()
			continue
		}
		if resp.Format == "" {
			resp.Format = string(target)
		}
		return resp, nil
	}
	return nil, fmt.Errorf("combo %q: %w", u.name, lastErr)
}

// isTierRetryable reports whether a combo tier response is worth failing
// over: transport-level retryables (429/5xx) plus free-tier access
// rejections (e.g. opencode.ai FreeTierError), which are tier-scoped —
// another tier may serve the same request.
func isTierRetryable(resp *domain.UpstreamResponse) bool {
	if resp == nil {
		return true
	}
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return true
	}
	return resp.StatusCode >= 400 && domain.IsFreeTierRejection(resp)
}

func rewriteModel(body []byte, m string) ([]byte, error) {
	if strings.TrimSpace(m) == "" {
		return body, nil
	}
	var raw map[string]any
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	if err := dec.Decode(&raw); err != nil {
		return nil, fmt.Errorf("decode body for model rewrite: %w", err)
	}
	raw["model"] = m
	out, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("encode body for model rewrite: %w", err)
	}
	return out, nil
}

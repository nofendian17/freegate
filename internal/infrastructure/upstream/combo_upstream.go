package upstream

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"freegate/internal/domain"
	"freegate/internal/translate"
)

var _ domain.Upstream = (*ComboUpstream)(nil)

// comboTierCooldownTTL is how long a tier that just failed (transport error,
// nil response, or retryable status) is skipped before it may be tried again.
// A dead tier stalls for ResponseHeaderTimeout (30s); without a cooldown the
// next request would park on it again instead of using the healthy tier.
const comboTierCooldownTTL = 30 * time.Second

type ComboTier struct {
	Upstream domain.Upstream
	Model    string
}

type ComboUpstream struct {
	name  string
	tiers []ComboTier

	mu     sync.Mutex
	cooled map[int]time.Time
}

func NewComboUpstream(name string, tiers []ComboTier) *ComboUpstream {
	kept := make([]ComboTier, 0, len(tiers))
	for _, t := range tiers {
		if t.Upstream != nil {
			kept = append(kept, t)
		}
	}
	return &ComboUpstream{name: name, tiers: kept, cooled: make(map[int]time.Time)}
}

// inCooldown reports whether tier i is still cooled down. Caller must hold mu.
func (u *ComboUpstream) inCooldownLocked(i int, now time.Time) bool {
	until, ok := u.cooled[i]
	if !ok {
		return false
	}
	if now.After(until) {
		delete(u.cooled, i)
		return false
	}
	return true
}

// hasFreshTierFrom reports whether any tier at index >= from is not cooled.
func (u *ComboUpstream) hasFreshTierFrom(from int, now time.Time) bool {
	for j := from; j < len(u.tiers); j++ {
		if until, ok := u.cooled[j]; !ok || now.After(until) {
			return true
		}
	}
	return false
}

func (u *ComboUpstream) markCooldown(i int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.cooled == nil {
		u.cooled = make(map[int]time.Time)
	}
	u.cooled[i] = time.Now().Add(comboTierCooldownTTL)
}

func (u *ComboUpstream) clearCooldown(i int) {
	u.mu.Lock()
	delete(u.cooled, i)
	u.mu.Unlock()
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
		// Skip a tier still in cooldown when a fresh tier remains below it,
		// so the next request after a 30s stall goes straight to the healthy
		// tier. If every remaining tier is cooled, fall through and try
		// anyway (stale fallback) rather than failing fast with no attempt.
		now := time.Now()
		u.mu.Lock()
		cooled := u.inCooldownLocked(i, now)
		freshBelow := u.hasFreshTierFrom(i+1, now)
		u.mu.Unlock()
		if cooled && freshBelow {
			slog.Warn("combo tier in cooldown, skipping", "combo", u.name, "tier", tier.Upstream.Name())
			lastErr = fmt.Errorf("combo %q tier %q in cooldown", u.name, tier.Upstream.Name())
			continue
		}
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
		// One same-tier retry on transport errors (err or nil response
		// only — retryable statuses still fail over immediately).
		// Transient dials die (EOF, TLS handshake timeouts) while a
		// fresh dial usually succeeds; retrying preserves the requested
		// model instead of serving another tier's model after a ~30s stall.
		var resp *domain.UpstreamResponse
		tctx := translate.WithRequestFormat(ctx, target)
		for attempt := 0; attempt < 2; attempt++ {
			resp, err = tier.Upstream.ChatCompletion(tctx, out)
			if err == nil && resp != nil {
				break
			}
			if attempt == 0 {
				slog.Debug("combo tier transport error, retrying same tier", "combo", u.name, "tier", tier.Upstream.Name(), "error", err)
			}
		}
		if err != nil {
			lastErr = err
			u.markCooldown(i)
			if !last {
				slog.Warn("combo tier failed, trying next", "combo", u.name, "tier", tier.Upstream.Name(), "error", err)
				continue
			}
			return nil, fmt.Errorf("combo %q: %w", u.name, err)
		}
		if resp == nil {
			lastErr = fmt.Errorf("combo %q tier %q returned nil response", u.name, tier.Upstream.Name())
			u.markCooldown(i)
			if last {
				return nil, fmt.Errorf("combo %q: %w", u.name, lastErr)
			}
			slog.Warn("combo tier returned nil response, trying next", "combo", u.name, "tier", tier.Upstream.Name())
			continue
		}
		if domain.IsRetryableStatus(resp) && !last {
			slog.Warn("combo tier retryable status, trying next", "combo", u.name, "tier", tier.Upstream.Name(), "status", resp.StatusCode)
			resp.Close()
			u.markCooldown(i)
			continue
		}
		u.clearCooldown(i)
		if resp.Format == "" {
			resp.Format = string(target)
		}
		return resp, nil
	}
	return nil, fmt.Errorf("combo %q: %w", u.name, lastErr)
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

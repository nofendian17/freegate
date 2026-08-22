package upstream

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"freegate/internal/domain"
	"freegate/internal/infrastructure/upstream/types"
	"freegate/internal/model"
)

type OpenCodeUpstream struct {
	client    *HTTPClient
	cache     *ModelCache
	allowlist map[string]bool
}

func NewOpenCodeUpstream(baseURL string, apiKeys []string, d *Dialer, freeAllowlist []string) *OpenCodeUpstream {
	return NewOpenCodeUpstreamWithTransport(baseURL, apiKeys, NewTransport(d), freeAllowlist)
}

func NewOpenCodeUpstreamWithTransport(baseURL string, apiKeys []string, tr *http.Transport, freeAllowlist []string) *OpenCodeUpstream {
	// Mimic the headers the official OpenCode client sends for opencode
	// provider models so the upstream treats requests as first-party.
	headers := map[string]string{
		"x-opencode-client":  "desktop",
		"x-opencode-session": genUUID(),
		"x-opencode-request": genUUID(),
		"x-opencode-project": genUUID(),
		"User-Agent":         "opencode/latest/1.0.0/desktop",
	}
	al := make(map[string]bool, len(freeAllowlist))
	for _, id := range freeAllowlist {
		id = strings.TrimSpace(id)
		if id != "" {
			al[id] = true
		}
	}
	return &OpenCodeUpstream{
		client:    NewHTTPClientWithTransport(baseURL, apiKeys, headers, tr),
		cache:     NewModelCache(),
		allowlist: al,
	}
}

func (o *OpenCodeUpstream) Name() string {
	return "opencode"
}

func (o *OpenCodeUpstream) Start(ctx context.Context, refreshInterval time.Duration) {
	refresher := NewRefresher("opencode", func(ctx context.Context) error {
		models, err := o.ListModels(ctx)
		if err != nil {
			return err
		}
		o.cache.Set(models)
		return nil
	}, refreshInterval)
	refresher.Run(ctx)
}

func (o *OpenCodeUpstream) Match(modelID string) bool {
	return true
}

func (o *OpenCodeUpstream) ListModels(ctx context.Context) ([]model.Model, error) {
	body, err := o.client.ReadAll(ctx, "/models")
	if err != nil {
		return nil, fmt.Errorf("opencode: fetch models: %w", err)
	}

	var list types.OpenCodeModelList
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("opencode: parse models: %w", err)
	}

	// The upstream /v1/models endpoint is OpenAI-compatible and does not
	// include cost data. Free models are identified by the "-free" suffix,
	// which is the same naming convention opencode uses in its own catalog
	// (e.g. glm-4.7-free, kimi-k2.5-free, deepseek-v4-flash-free), with a
	// small allowlist for known exceptions that don't follow that
	// convention (e.g. big-pickle, which is served as deepseek-v4-flash
	// with cost 0 by the upstream).
	var free []model.Model
	seen := make(map[string]bool)
	for _, m := range list.Data {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		if strings.HasSuffix(m.ID, "-free") || o.allowlist[m.ID] {
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

func (o *OpenCodeUpstream) ChatCompletion(ctx context.Context, body []byte) (*domain.UpstreamResponse, error) {
	resp, err := o.client.Post(ctx, "/chat/completions", body)
	if err != nil {
		return nil, err
	}
	return domain.NewUpstreamResponse(resp), nil
}

// genUUID returns a random RFC 4122 v4 UUID without pulling in a dependency.
func genUUID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "00000000-0000-0000-0000-000000000000"
	}
	b[6] = (b[6] & 3) | 8<<4 // version 4
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

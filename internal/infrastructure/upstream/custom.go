package upstream

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"freegate/internal/domain"
)

type CustomUpstream struct {
	name     string
	client   *HTTPClient
	cache    *ModelCache
	selected map[string]struct{}
	// selectedOrder preserves the stored selection order so ListModels
	// and Rebuild merges stay deterministic (map iteration is random).
	selectedOrder []string
}

var _ domain.Upstream = (*CustomUpstream)(nil)

func NewCustomUpstream(name, baseURL string, keys []string, headers map[string]string, models []string, tr *http.Transport) *CustomUpstream {
	if headers == nil {
		headers = map[string]string{}
	}
	u := &CustomUpstream{
		name:     name,
		client:   NewHTTPClientWithTransport(baseURL, keys, headers, tr),
		cache:    NewModelCache(),
		selected: make(map[string]struct{}, len(models)),
	}
	u.SetSelected(models)
	return u
}

// SetSelected replaces the explicit model selection and seeds the cache
// with bare entries, so Match works before the first catalog fetch. Fresh
// metadata overlays these entries on the next ListModels.
// A nil slice selects the legacy mode: the whole catalog routes,
// preserving the pre-selection semantic for rows saved before the
// selection feature existed. Nothing is seeded; the first fetch fills
// the cache, and until then Match accepts everything.
func (u *CustomUpstream) SetSelected(models []string) {
	if models == nil {
		u.selected = nil
		u.selectedOrder = nil
		return
	}
	sel := make(map[string]struct{}, len(models))
	order := make([]string, 0, len(models))
	bare := make([]domain.Model, 0, len(models))
	for _, m := range models {
		if m = strings.TrimSpace(m); m == "" {
			continue
		}
		if _, dup := sel[m]; dup {
			continue
		}
		sel[m] = struct{}{}
		order = append(order, m)
		bare = append(bare, domain.Model{ID: m, Object: "model", Provider: "custom:" + u.name})
	}
	u.selected = sel
	u.selectedOrder = order
	u.cache.Set(bare)
}

// RestoreKept overlays fresh metadata from the previous generation onto
// the current selection, preserving bare entries for newly added models.
// Must be called before the object is published (Rebuild); the constructor
// already seeded bare entries for the full new selection.
func (u *CustomUpstream) RestoreKept(old []domain.Model) {
	if u.selected == nil {
		return
	}
	byOld := make(map[string]domain.Model, len(old))
	for _, m := range old {
		if _, ok := u.selected[m.ID]; !ok {
			continue
		}
		if _, dup := byOld[m.ID]; !dup {
			byOld[m.ID] = m
		}
	}
	bareByID := make(map[string]domain.Model, len(u.selectedOrder))
	for _, m := range u.cache.Get() {
		if _, ok := u.selected[m.ID]; !ok {
			continue
		}
		if _, dup := bareByID[m.ID]; !dup {
			bareByID[m.ID] = m
		}
	}
	merged := make([]domain.Model, 0, len(u.selectedOrder))
	for _, id := range u.selectedOrder {
		if m, ok := byOld[id]; ok {
			merged = append(merged, m)
			continue
		}
		if m, ok := bareByID[id]; ok {
			merged = append(merged, m)
			continue
		}
		merged = append(merged, domain.Model{ID: id, Object: "model", Provider: "custom:" + u.name})
	}
	u.cache.Set(merged)
}

func (u *CustomUpstream) Name() string { return "custom:" + u.name }

// Match reports whether modelID routes to this provider: any model in
// legacy mode (nil selection), otherwise only explicitly selected ones.
// Nothing is auto-added to a curated selection.
func (u *CustomUpstream) Match(modelID string) bool {
	if u.selected == nil {
		return true
	}
	_, ok := u.selected[modelID]
	return ok
}

func (u *CustomUpstream) ListModels(ctx context.Context) ([]domain.Model, error) {
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
	byID := make(map[string]domain.Model, len(list.Data))
	order := make([]string, 0, len(list.Data))
	for _, m := range list.Data {
		if m.ID == "" {
			continue
		}
		if _, dup := byID[m.ID]; dup {
			continue
		}
		obj := m.Object
		if obj == "" {
			obj = "model"
		}
		owner := m.OwnedBy
		if owner == "" {
			owner = "custom:" + u.name
		}
		byID[m.ID] = domain.Model{ID: m.ID, Object: obj, Created: m.Created, OwnedBy: owner, IsFree: true, Provider: "custom:" + u.name}
		order = append(order, m.ID)
	}
	// Legacy mode (nil selection): the whole fetched catalog is served,
	// the pre-selection semantic. Curated mode serves the explicit
	// selection in stored order: fresh metadata where the fetch has it,
	// previous entry (or a bare seed) otherwise, so newly added models
	// appear immediately and one bad refresh can't silently drop an
	// explicit choice.
	if u.selected == nil {
		out := make([]domain.Model, 0, len(order))
		for _, id := range order {
			out = append(out, byID[id])
		}
		u.cache.Set(out)
		return out, nil
	}
	curByID := make(map[string]domain.Model, len(u.selected))
	for _, m := range u.cache.Get() {
		if _, ok := u.selected[m.ID]; !ok {
			continue
		}
		if _, dup := curByID[m.ID]; !dup {
			curByID[m.ID] = m
		}
	}
	out := make([]domain.Model, 0, len(u.selected))
	for _, id := range u.selectedOrder {
		if fresh, ok := byID[id]; ok {
			out = append(out, fresh)
			continue
		}
		if cur, ok := curByID[id]; ok {
			out = append(out, cur)
			continue
		}
		out = append(out, domain.Model{ID: id, Object: "model", Provider: "custom:" + u.name})
	}
	u.cache.Set(out)
	return out, nil
}

func (u *CustomUpstream) Models() []domain.Model { return u.cache.Get() }

func (u *CustomUpstream) SeedModels(m []domain.Model) { u.cache.Set(m) }

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
	}, refreshInterval).WithOnFailure(u.client.CloseIdleConnections).Run(ctx)
}

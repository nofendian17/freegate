package upstream

import (
	"context"
	"testing"
	"time"

	"freegate/internal/domain"
)

type chainStub struct {
	name   string
	models []domain.Model
}

func (s *chainStub) Name() string { return s.name }
func (s *chainStub) Match(id string) bool {
	for _, m := range s.models {
		if m.ID == id {
			return true
		}
	}
	return false
}
func (s *chainStub) ListModels(ctx context.Context) ([]domain.Model, error) { return s.models, nil }
func (s *chainStub) ChatCompletion(ctx context.Context, b []byte) (*domain.UpstreamResponse, error) {
	return nil, nil
}
func (s *chainStub) Models() []domain.Model                      { return s.models }
func (s *chainStub) Start(ctx context.Context, d time.Duration) {}

func TestCombo_SelectChain_OrderAndFallback(t *testing.T) {
	def := &chainStub{name: "opencode"}
	legacy := NewRouter(def, &chainStub{name: "kilo"})
	cr := NewComboRouter(legacy)
	if got := cr.Select("anything"); got.Name() != "opencode" {
		t.Fatalf("expected legacy fallback, got %s", got.Name())
	}
	a := &chainStub{name: "custom:a", models: []domain.Model{{ID: "anything"}}}
	b := &chainStub{name: "custom:b", models: []domain.Model{{ID: "anything"}}}
	cr.SetCustoms([]domain.Upstream{a, b})
	chain := cr.SelectChain("anything")
	if len(chain) != 2 || chain[0].Name() != "custom:a" || chain[1].Name() != "custom:b" {
		t.Fatalf("bad chain order: %v", chain)
	}
}

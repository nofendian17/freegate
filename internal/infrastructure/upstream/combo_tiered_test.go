package upstream

import (
	"context"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"freegate/internal/domain"
	"freegate/internal/model"
)

type tierStub struct {
	name    string
	status  int
	body    string
	calls   int
	gotBody []byte
}

func (s *tierStub) Name() string                                          { return s.name }
func (s *tierStub) Match(id string) bool                                  { return true }
func (s *tierStub) ListModels(ctx context.Context) ([]model.Model, error) { return nil, nil }
func (s *tierStub) ChatCompletion(ctx context.Context, b []byte) (*domain.UpstreamResponse, error) {
	s.calls++
	s.gotBody = append([]byte(nil), b...)
	rec := httptest.NewRecorder()
	rec.WriteHeader(s.status)
	_, _ = io.WriteString(rec, s.body)
	return domain.NewUpstreamResponse(rec.Result()), nil
}
func (s *tierStub) Models() []model.Model                      { return nil }
func (s *tierStub) Start(ctx context.Context, d time.Duration) {}

func TestComboUpstream_Failover_SendsSameBody(t *testing.T) {
	t1 := &tierStub{name: "opencode", status: 429, body: `{"error":"slow"}`}
	t2 := &tierStub{name: "kilo", status: 500, body: `{"error":"boom"}`}
	t3 := &tierStub{name: "custom:acme", status: 200, body: `{"ok":true}`}
	cu := NewComboUpstream("hemat", []domain.Upstream{t1, t2, t3})
	if !cu.Match("hemat") || cu.Match("other") {
		t.Fatal("exact-name match broken")
	}
	body := []byte(`{"model":"hemat","messages":[]}`)
	resp, err := cu.ChatCompletion(context.Background(), body)
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	defer resp.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
	if t1.calls != 1 || t2.calls != 1 || t3.calls != 1 {
		t.Fatalf("calls=%d,%d,%d", t1.calls, t2.calls, t3.calls)
	}
	if string(t3.gotBody) != string(body) {
		t.Fatalf("body rewritten: %q", t3.gotBody)
	}
}

func TestComboUpstream_Exhausted_ReturnsLast(t *testing.T) {
	t1 := &tierStub{name: "a", status: 429, body: `{"error":"first"}`}
	t2 := &tierStub{name: "b", status: 500, body: `{"error":"second"}`}
	cu := NewComboUpstream("hemat", []domain.Upstream{t1, t2})
	resp, err := cu.ChatCompletion(context.Background(), []byte(`{}`))
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	defer resp.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 500 || !strings.Contains(string(raw), "second") {
		t.Fatalf("status=%d body=%s", resp.StatusCode, raw)
	}
}

func TestComboUpstream_NilResponse_FailsOver(t *testing.T) {
	nilTier := &nilStub{name: "flaky"}
	ok := &tierStub{name: "custom:acme", status: 200, body: `{"ok":true}`}
	cu := &ComboUpstream{name: "hemat", tiers: []domain.Upstream{nilTier, ok}}
	resp, err := cu.ChatCompletion(context.Background(), []byte(`{}`))
	if err != nil {
		t.Fatalf("chat: %v", err)
	}
	defer resp.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("status=%d", resp.StatusCode)
	}
}

func TestComboUpstream_AllNilResponse_ErrorNoPanic(t *testing.T) {
	defer func() {
		if rec := recover(); rec != nil {
			t.Fatalf("panicked: %v", rec)
		}
	}()
	cu := &ComboUpstream{name: "hemat", tiers: []domain.Upstream{&nilStub{name: "a"}, &nilStub{name: "b"}}}
	if _, err := cu.ChatCompletion(context.Background(), []byte(`{}`)); err == nil {
		t.Fatal("expected error when all tiers return nil")
	}
}

type nilStub struct{ name string }

func (s *nilStub) Name() string                                          { return s.name }
func (s *nilStub) Match(id string) bool                                  { return true }
func (s *nilStub) ListModels(ctx context.Context) ([]model.Model, error) { return nil, nil }
func (s *nilStub) ChatCompletion(ctx context.Context, b []byte) (*domain.UpstreamResponse, error) {
	return nil, nil
}
func (s *nilStub) Models() []model.Model                      { return nil }
func (s *nilStub) Start(ctx context.Context, d time.Duration) {}

func TestComboRouter_AllModels_StableOrder(t *testing.T) {
	def := &tierStub{name: "opencode"}
	cr := NewComboRouter(NewRouter(def))
	lookup := func(name string) domain.Upstream { return def }
	cr.RebuildCombos([]ComboTierRow{
		{Name: "zeta", Providers: []string{"opencode"}},
		{Name: "alpha", Providers: []string{"opencode"}},
	}, lookup)
	first := cr.AllModels()
	second := cr.AllModels()
	if len(first) != len(second) {
		t.Fatalf("length changed: %d vs %d", len(first), len(second))
	}
	for i := range first {
		if first[i].ID != second[i].ID {
			t.Fatalf("unstable order: %v vs %v", first, second)
		}
	}
	seen := map[string]int{}
	for i, m := range first {
		seen[m.ID] = i
	}
	if seen["alpha"] > seen["zeta"] {
		t.Fatalf("combos not sorted: %v", first)
	}
}

func TestComboRouter_VirtualModel_ExactMatch(t *testing.T) {
	def := &tierStub{name: "opencode"}
	legacy := NewRouter(def)
	cr := NewComboRouter(legacy)
	cr.RebuildCombos([]ComboTierRow{{Name: "hemat", Providers: []string{"combo:hemat"}}}, func(name string) domain.Upstream {
		if name == "combo:hemat" {
			return NewComboUpstream("hemat", []domain.Upstream{def})
		}
		return nil
	})
	got := cr.SelectChain("hemat")
	if len(got) != 1 || got[0].Name() != "combo:hemat" {
		t.Fatalf("no virtual match: %v", got)
	}
	found := false
	for _, m := range cr.AllModels() {
		if m.ID == "hemat" {
			found = true
		}
	}
	if !found {
		t.Fatal("virtual model missing from catalog")
	}
}

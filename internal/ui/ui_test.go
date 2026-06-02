package ui

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"freegate/internal/model"
)

type fakeData struct {
	metrics map[string]any
	models  []model.Model
	reqs    []model.RequestLogEntry
	ts      []model.TimeseriesEntry
	uptime  int64
	start   int64
	torIP   string
}

func (f *fakeData) Metrics() map[string]any              { return f.metrics }
func (f *fakeData) Models() []model.Model                { return f.models }
func (f *fakeData) Requests() []model.RequestLogEntry    { return f.reqs }
func (f *fakeData) Timeseries() []model.TimeseriesEntry  { return f.ts }
func (f *fakeData) UptimeSeconds() int64                 { return f.uptime }
func (f *fakeData) StartedAtUnix() int64                 { return f.start }
func (f *fakeData) TorIP() string                        { return f.torIP }

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	tpl, err := LoadTemplates(webTemplatesFS(t))
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	return NewHandler(&fakeData{
		metrics: map[string]any{
			"total_requests":  int64(42),
			"retry_count":     int64(3),
			"upstream_errors": int64(1),
			"rate_limit_hits": int64(7),
			"per_upstream":    map[string]int64{"opencode": 30, "kilo": 12},
		},
		models: []model.Model{
			{ID: "test-model-1", Provider: "opencode", IsFree: true},
			{ID: "test-model-2", Provider: "kilo", IsFree: true},
		},
		reqs: []model.RequestLogEntry{
			{Ts: time.Now(), Method: "POST", Path: "/v1/chat/completions", Model: "test-model-1", Upstream: "opencode", Status: 200, DurationMs: 1234, IP: "127.0.0.1"},
		},
		ts: []model.TimeseriesEntry{
			{Ts: time.Now(), TotalRequests: 10, Errors: 0, Retries: 0, RateLimitHits: 0, PerUpstream: map[string]int{"opencode": 10}},
		},
		uptime: 90,
		start:  time.Now().Add(-90 * time.Second).Unix(),
	}, tpl, webStaticFS(t))
}

func serveViaRoutes(h *Handler, method, target string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.Routes().ServeHTTP(rr, httptest.NewRequest(method, target, nil))
	return rr
}

func TestDashboardRenders(t *testing.T) {
	h := newTestHandler(t)
	rr := serveViaRoutes(h, "GET", "/")

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{
		"freegate",
		"Total Requests", "Retries", "Upstream Errors", "Rate-Limit Hits",
		"opencode", "kilo",
		"test-model-1", "test-model-2",
		"htmx.min.js", "chart.umd.js", "app.css",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

func TestPartialStatsRenders(t *testing.T) {
	h := newTestHandler(t)
	rr := serveViaRoutes(h, "GET", "/partials/stats")

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"42", "3", "1", "7"} {
		if !strings.Contains(body, want) {
			t.Errorf("partials/stats missing %q", want)
		}
	}
}

func TestPartialModelsFilter(t *testing.T) {
	h := newTestHandler(t)

	rr := serveViaRoutes(h, "GET", "/partials/models?provider=opencode")
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "test-model-1") {
		t.Errorf("expected test-model-1 (opencode)")
	}
	if strings.Contains(body, "test-model-2") {
		t.Errorf("did not expect test-model-2 (kilo) when filter=opencode")
	}

	rr = serveViaRoutes(h, "GET", "/partials/models?provider=kilo")
	body = rr.Body.String()
	if !strings.Contains(body, "test-model-2") {
		t.Errorf("expected test-model-2 (kilo) when filter=kilo")
	}
}

func TestPartialRequestsRenders(t *testing.T) {
	h := newTestHandler(t)
	rr := serveViaRoutes(h, "GET", "/partials/requests")

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"test-model-1", "opencode", "200", "1234ms", "127.0.0.1"} {
		if !strings.Contains(body, want) {
			t.Errorf("partials/requests missing %q", want)
		}
	}
}

func TestAPITimeseries(t *testing.T) {
	h := newTestHandler(t)
	rr := serveViaRoutes(h, "GET", "/api/timeseries")

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"total_requests":10`) {
		t.Errorf("timeseries missing data, got: %s", body)
	}
}

func TestAPIHealth(t *testing.T) {
	h := newTestHandler(t)
	rr := serveViaRoutes(h, "GET", "/api/health")

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"ok":true`) {
		t.Errorf("health missing ok:true, got: %s", body)
	}
	if !strings.Contains(body, `"model_count":2`) {
		t.Errorf("health missing model_count:2, got: %s", body)
	}
}

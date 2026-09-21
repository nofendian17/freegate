package ui

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"freegate/internal/domain"
)

type fakeData struct {
	metrics map[string]any
	models  []domain.Model
	reqs    []domain.RequestLogEntry
	ts      []domain.TimeseriesEntry
	uptime  int64
	start   int64
}

func (f *fakeData) Metrics() map[string]any              { return f.metrics }
func (f *fakeData) Models() []domain.Model               { return f.models }
func (f *fakeData) Requests() []domain.RequestLogEntry   { return f.reqs }
func (f *fakeData) Timeseries() []domain.TimeseriesEntry { return f.ts }
func (f *fakeData) UptimeSeconds() int64                 { return f.uptime }
func (f *fakeData) StartedAtUnix() int64                 { return f.start }

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	tpl, err := LoadTemplates(webTemplatesFS(t))
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	return New(&fakeData{
		metrics: map[string]any{
			"total_requests":  int64(42),
			"upstream_errors": int64(1),
			"input_tokens":    int64(1000),
			"output_tokens":   int64(500),
			"per_upstream":    map[string]int64{"opencode": 30, "kilo": 12},
		},
		models: []domain.Model{
			{ID: "test-model-1", Provider: "opencode", IsFree: true},
			{ID: "test-model-2", Provider: "kilo", IsFree: true},
		},
		reqs: []domain.RequestLogEntry{
			{Ts: time.Now(), Method: "POST", Path: "/v1/chat/completions", Model: "test-model-1", Upstream: "opencode", Status: 200, DurationMs: 1234, IP: "127.0.0.1"},
		},
		ts: []domain.TimeseriesEntry{
			{Ts: time.Now(), TotalRequests: 10, Errors: 0, PerUpstream: map[string]int{"opencode": 10}},
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
		"Total Requests", "Upstream Errors", "Input Tokens", "Output Tokens",
		"opencode", "kilo",
		"test-model-1", "test-model-2",
		"htmx.min.js", "chart.umd.js", "app.css",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

// TestDashboardLogoutButton ensures the dashboard nav exposes the logout
// form so admins can end their session without clearing cookies manually.
func TestDashboardLogoutButton(t *testing.T) {
	h := newTestHandler(t)
	rr := serveViaRoutes(h, "GET", "/")
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{
		`<form method="POST" action="/logout"`,
		`type="submit"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

package ui

import (
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"freegate/internal/infrastructure/vpngate"
	"freegate/internal/model"
)

type fakeData struct {
	metrics map[string]any
	models  []model.Model
	reqs    []model.RequestLogEntry
	ts      []model.TimeseriesEntry
	uptime  int64
	start   int64
	vpnIP   string
}

func (f *fakeData) Metrics() map[string]any             { return f.metrics }
func (f *fakeData) Models() []model.Model               { return f.models }
func (f *fakeData) Requests() []model.RequestLogEntry   { return f.reqs }
func (f *fakeData) Timeseries() []model.TimeseriesEntry { return f.ts }
func (f *fakeData) UptimeSeconds() int64                { return f.uptime }
func (f *fakeData) StartedAtUnix() int64                { return f.start }
func (f *fakeData) VPNIP() string                       { return f.vpnIP }

func newTestHandler(t *testing.T) *Handler {
	t.Helper()
	tpl, err := LoadTemplates(webTemplatesFS(t))
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}
	return NewHandler(&fakeData{
		metrics: map[string]any{
			"total_requests":  int64(42),
			"upstream_errors": int64(1),
			"input_tokens":    int64(1000),
			"output_tokens":   int64(500),
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
			{Ts: time.Now(), TotalRequests: 10, Errors: 0, PerUpstream: map[string]int{"opencode": 10}},
		},
		uptime: 90,
		start:  time.Now().Add(-90 * time.Second).Unix(),
	}, &fakeVPN{}, tpl, webStaticFS(t))
}

type fakeVPN struct {
	servers   []vpngate.ServerInfo
	status    vpngate.StatusInfo
	ping      vpngate.PingResult
	connectTo string
	rotateErr error
	direct    bool
}

func (f *fakeVPN) ListServers() ([]vpngate.ServerInfo, error)    { return f.servers, nil }
func (f *fakeVPN) RefreshServers() ([]vpngate.ServerInfo, error) { return f.servers, nil }
func (f *fakeVPN) ConnectTo(h string) error                      { f.connectTo = h; return nil }
func (f *fakeVPN) ForceNewIP() error                             { return f.rotateErr }
func (f *fakeVPN) Status() (vpngate.StatusInfo, error)           { return f.status, nil }
func (f *fakeVPN) Ping() (vpngate.PingResult, error)             { return f.ping, nil }
func (f *fakeVPN) SetDirect(v bool) error                        { f.direct = v; return nil }
func (f *fakeVPN) Direct() bool                                  { return f.direct }
func (f *fakeVPN) CurrentIP() string                             { return f.status.IP }
func (f *fakeVPN) InstallHint() string                           { return "" }

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
		"flagEmoji",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("body missing %q", want)
		}
	}
}

// TestDashboardVPNDirectPickerDefault guards against the dashboard picker
// silently defaulting to "$ direct (no VPN)" while the active route is still
// the tunnel. The placeholder option must render first so the initial
// selection is a neutral "select server…", and the client-side route sync
// (which keeps the picker honest and makes a direct click actually engage
// direct mode) must be present in the served page.
func TestDashboardVPNDirectPickerDefault(t *testing.T) {
	h := newTestHandler(t)
	rr := serveViaRoutes(h, "GET", "/")
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()

	placeholder := `<option value="">$ select server…</option>`
	direct := `<option value="__direct__">$ direct (no VPN)</option>`
	phIdx := strings.Index(body, placeholder)
	dirIdx := strings.Index(body, direct)
	if phIdx < 0 || dirIdx < 0 {
		t.Fatalf("picker options missing (placeholder@%d direct@%d)", phIdx, dirIdx)
	}
	if dirIdx < phIdx {
		t.Error("direct option renders before the neutral placeholder; picker would default to direct while the route may still be the tunnel")
	}
	for _, want := range []string{"vpnRouteDirect", "syncVPNSelectToRoute"} {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard missing route-sync logic %q", want)
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
	for _, want := range []string{"42", "1", "1000", "500"} {
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
	if !strings.Contains(body, "test-btn") {
		t.Errorf("expected test button in model rows")
	}

	rr = serveViaRoutes(h, "GET", "/partials/models?provider=kilo")
	body = rr.Body.String()
	if !strings.Contains(body, "test-model-2") {
		t.Errorf("expected test-model-2 (kilo) when filter=kilo")
	}
}

func TestModelRowsEmptyColspan(t *testing.T) {
	h := newTestHandler(t)
	h.data.(*fakeData).models = nil
	rr := serveViaRoutes(h, "GET", "/partials/models")
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `colspan="4"`) {
		t.Errorf("expected empty-state row with colspan=4, got: %s", body)
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

func TestAPIVPNServers(t *testing.T) {
	h := newTestHandler(t)
	h.vpn.(*fakeVPN).servers = []vpngate.ServerInfo{
		{Hostname: "vpn-korea-1", IP: "1.2.3.4", Country: "South Korea", Score: 5000, Ping: "12"},
	}
	rr := serveViaRoutes(h, "GET", "/api/vpn/servers")

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, "vpn-korea-1") {
		t.Errorf("servers missing hostname, got: %s", body)
	}
	if !strings.Contains(body, "South Korea") {
		t.Errorf("servers missing country, got: %s", body)
	}
}

func TestAPIVPNConnect(t *testing.T) {
	h := newTestHandler(t)
	vpn := h.vpn.(*fakeVPN)
	vpn.status = vpngate.StatusInfo{Connected: true, Server: "vpn-korea-1", IP: "1.2.3.4"}

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/vpn/connect", strings.NewReader(`{"server":"vpn-korea-1"}`))
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	if vpn.connectTo != "vpn-korea-1" {
		t.Errorf("expected connect to vpn-korea-1, got %q", vpn.connectTo)
	}
	if !strings.Contains(rr.Body.String(), `"connected":true`) {
		t.Errorf("connect response missing status, got: %s", rr.Body.String())
	}
}

func TestAPIVPNConnectMissingServer(t *testing.T) {
	h := newTestHandler(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/vpn/connect", strings.NewReader(`{}`))
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != 400 {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestAPIVPNRotate(t *testing.T) {
	h := newTestHandler(t)
	vpn := h.vpn.(*fakeVPN)
	vpn.status = vpngate.StatusInfo{Connected: true, Server: "vpn-korea-2", IP: "5.6.7.8"}

	rr := serveViaRoutes(h, "POST", "/api/vpn/rotate")

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), "vpn-korea-2") {
		t.Errorf("rotate response missing server, got: %s", rr.Body.String())
	}
}

func TestAPIVPNPing(t *testing.T) {
	h := newTestHandler(t)
	h.vpn.(*fakeVPN).ping = vpngate.PingResult{
		Connected: true,
		Server:    "vpn-korea-1",
		DNSOK:     true, DNSMS: 12,
		EgressOK: true, EgressIP: "1.2.3.4", HTTPMS: 210, HTTPCode: 200,
	}
	rr := serveViaRoutes(h, "POST", "/api/vpn/ping")

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, `"dns_ok":true`) {
		t.Errorf("ping missing dns_ok, got: %s", body)
	}
	if !strings.Contains(body, `"egress_ok":true`) {
		t.Errorf("ping missing egress_ok, got: %s", body)
	}
	if !strings.Contains(body, `"egress_ip":"1.2.3.4"`) {
		t.Errorf("ping missing egress_ip, got: %s", body)
	}
	if !strings.Contains(body, `"direct":false`) {
		t.Errorf("ping missing route mode direct:false, got: %s", body)
	}
}

func TestAPIVPNPingDirectMode(t *testing.T) {
	// In direct mode the ping result must carry the route mode so the
	// dashboard can label the tunnel check as such.
	h := newTestHandler(t)
	vpn := h.vpn.(*fakeVPN)
	vpn.direct = true
	vpn.ping = vpngate.PingResult{
		Connected: true,
		DNSOK:     true, DNSMS: 8,
		EgressOK: true, EgressIP: "9.9.9.9", HTTPMS: 150, HTTPCode: 200,
	}
	rr := serveViaRoutes(h, "POST", "/api/vpn/ping")

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	if !strings.Contains(rr.Body.String(), `"direct":true`) {
		t.Errorf("ping missing direct:true in direct mode, got: %s", rr.Body.String())
	}
}

func TestAPIVPNPingError(t *testing.T) {
	h := newTestHandler(t)
	h.vpn.(*fakeVPN).ping = vpngate.PingResult{Connected: false, DNSError: "tunnel not connected"}
	rr := serveViaRoutes(h, "POST", "/api/vpn/ping")

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200 (ping result is valid even when disconnected)", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"connected":false`) {
		t.Errorf("ping missing connected:false, got: %s", rr.Body.String())
	}
}

func TestAPIVPNDirect(t *testing.T) {
	h := newTestHandler(t)
	vpn := h.vpn.(*fakeVPN)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/vpn/direct", strings.NewReader(`{"direct":true}`))
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200 (body: %s)", rr.Code, rr.Body.String())
	}
	if !vpn.direct {
		t.Error("expected dialer to be switched to direct")
	}
	if !strings.Contains(rr.Body.String(), `"direct":true`) {
		t.Errorf("direct response missing field, got: %s", rr.Body.String())
	}
}

func TestAPIVPNDirectBackToTunnel(t *testing.T) {
	h := newTestHandler(t)
	vpn := h.vpn.(*fakeVPN)
	vpn.direct = true

	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/vpn/direct", strings.NewReader(`{"direct":false}`))
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if vpn.direct {
		t.Error("expected dialer to be switched back to tunnel")
	}
}

func TestAPIVPNDirectInvalidBody(t *testing.T) {
	h := newTestHandler(t)
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("POST", "/api/vpn/direct", strings.NewReader(`{}`))
	h.Routes().ServeHTTP(rr, req)

	if rr.Code != 400 {
		t.Fatalf("status = %d, want 400", rr.Code)
	}
}

func TestAPIVPNStatus(t *testing.T) {
	h := newTestHandler(t)
	h.vpn.(*fakeVPN).status = vpngate.StatusInfo{Connected: true, Server: "vpn-korea-1", IP: "1.2.3.4"}
	rr := serveViaRoutes(h, "GET", "/api/vpn/status")

	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), `"server":"vpn-korea-1"`) {
		t.Errorf("status missing server, got: %s", rr.Body.String())
	}
}

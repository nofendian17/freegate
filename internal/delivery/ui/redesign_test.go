package ui

import (
	"strings"
	"testing"
	"time"

	"freegate/internal/domain"
)

// Pins from the UI redesign slices. Each test asserts on the served page so
// behavior is locked at the same boundary users see.

// TestDashboard_ModelsBodyHasSinglePoller pins the model-filter race fix:
// #models-body must not poll /partials/models itself (its poll dropped the
// provider query param and reset a filtered list every 10s). The select's
// poller — which includes the filter — is the only one allowed.
func TestDashboard_CustomProviderFilter(t *testing.T) {
	h := newTestHandler(t)
	data := h.data.(*fakeData)
	data.models = append(data.models,
		domain.Model{ID: "custom-one", Provider: "custom:acme"},
		domain.Model{ID: "custom-two", Provider: "custom:acme"},
	)
	rr := serveViaRoutes(h, "GET", "/")
	option := `<option value="custom:acme">$ custom:acme</option>`
	if strings.Count(rr.Body.String(), option) != 1 {
		t.Fatal("custom provider must appear exactly once in filter options")
	}
	filtered := serveViaRoutes(h, "GET", "/partials/models?provider=custom%3Aacme").Body.String()
	if !strings.Contains(filtered, "custom-one") || !strings.Contains(filtered, "custom-two") || strings.Contains(filtered, "test-model-1") {
		t.Fatalf("unexpected filtered models: %s", filtered)
	}
}

func TestDashboard_ModelsBodyHasSinglePoller(t *testing.T) {
	rr := serveViaRoutes(newTestHandler(t), "GET", "/")
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if got := strings.Count(body, `hx-get="/partials/models"`); got != 1 {
		t.Errorf("expected exactly 1 hx-get to /partials/models (the filter select), got %d", got)
	}
	tbody := tagBlock(body, `<tbody id="models-body"`, `</tbody>`)
	for _, banned := range []string{"hx-get", "hx-trigger"} {
		if strings.Contains(tbody, banned) {
			t.Errorf("#models-body still carries %q; background poll resets the active filter", banned)
		}
	}
}

// TestDashboard_ModalCloseKeydownScopesToOwnModal pins the keyboard fix:
// Enter/Space on the test modal's close button must not run closeErrorModal.
// The handler must scope each .modal-close to its own modal.
func TestDashboard_ModalCloseKeydownScoped(t *testing.T) {
	rr := serveViaRoutes(newTestHandler(t), "GET", "/")
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"closest('#error-modal')", "closest('#test-modal')"} {
		if !strings.Contains(body, want) {
			t.Errorf("served dashboard missing modal-scoped close handling %q", want)
		}
	}
}

// TestDashboard_ModelTestParseFailureMarksError pins the model-test state fix:
// a 2xx response whose body fails to parse must render as a failure, not
// keep the success tone.
func TestDashboard_ModelTestParseFailureMarksError(t *testing.T) {
	rr := serveViaRoutes(newTestHandler(t), "GET", "/")
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "catch (err) {\n          ok = false;") {
		t.Error("model test handler does not mark parse failures as errors")
	}
}

// TestDashboard_VPNRefreshChecksHTTPStatus pins the refresh-list fix: a
// non-OK POST /api/vpn/servers/refresh must surface an http failure instead
// of reporting "server list refreshed".
func TestDashboard_VPNRefreshChecksHTTPStatus(t *testing.T) {
	rr := serveViaRoutes(newTestHandler(t), "GET", "/")
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "refresh failed: http") {
		t.Error("VPN refresh-list handler does not check the response status")
	}
}

// TestRequestsPartial_KiloUsesAmberTone pins tone consistency: the Go view
// model emits tone "amber" for kilo; the partial must not recolor it purple.
func TestRequestsPartial_KiloUsesAmberTone(t *testing.T) {
	h := New(&fakeData{
		metrics: map[string]any{
			"total_requests": int64(1), "upstream_errors": int64(0),
			"input_tokens": int64(1), "output_tokens": int64(1),
			"per_upstream": map[string]int64{"kilo": 1},
		},
		models: []domain.Model{},
		reqs: []domain.RequestLogEntry{
			{Ts: time.Now(), Method: "POST", Path: "/v1/chat/completions", Model: "m", Upstream: "kilo", Status: 200, DurationMs: 5, IP: "127.0.0.1"},
		},
		ts: nil, uptime: 1, start: time.Now().Unix(),
	}, &fakeVPN{}, &fakeDirect{}, mustLoadTemplates(t), webStaticFS(t))

	rr := serveViaRoutes(h, "GET", "/partials/requests")
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `tone-amber">kilo`) {
		t.Errorf("kilo pill should use tone-amber, got: %s", body)
	}
}

// TestProvidersPage_Slice2Pins locks the slice-2 provider UX so future
// template edits cannot silently drop it: the hidden combo form, the busy
// helper, the status-checked getJSON, named tier fields, the rendered-element
// focus trap, and the empty-state hints.
func TestProvidersPage_Slice2Pins(t *testing.T) {
	h := newTestHandler(t)
	w := serveViaRoutes(h, "GET", "/providers")
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		`id="combo-form"`,         // coherent single combo editor
		"function withBusy(",      // in-flight button states
		"function getJSON(",       // HTTP-status-checked fetch
		`name="tier_provider"`,    // named dynamic form fields
		"offsetParent !== null",   // focus trap skips hidden controls
		"no custom providers yet", // actionable empty state
		"no combos yet",           // actionable empty state
	} {
		if !strings.Contains(body, want) {
			t.Errorf("providers page missing slice-2 behavior %q", want)
		}
	}
}

// TestAppCSS_Slice1Palette pins the slice-1 token swap so a stray hardcoded
// neon value cannot sneak back into the palette.
func TestAppCSS_Slice1Palette(t *testing.T) {
	css := readStaticFile(t, "css/app.css")
	for _, want := range []string{
		"--primary: #FFB454;",
		"--bg: #0F1115;",
		"--success: #7EE787;",
		"--error: #FF7B72;",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css missing token %q", want)
		}
	}
	for _, banned := range []string{"#00FF41", "#FF0040"} {
		if strings.Contains(css, banned) {
			t.Errorf("app.css still hardcodes legacy neon %s", banned)
		}
	}
}

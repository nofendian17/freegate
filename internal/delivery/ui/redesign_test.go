package ui

import (
	"io/fs"
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

// TestDashboard_ModalCloseKeydownScoped pins the Alpine modal contract:
// both modals close on Escape and on backdrop click, and each close button
// is scoped to its own modal.
func TestDashboard_ModalCloseKeydownScoped(t *testing.T) {
	rr := serveViaRoutes(newTestHandler(t), "GET", "/")
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{"@keydown.escape.window", "@click.outside"} {
		if !strings.Contains(body, want) {
			t.Errorf("served dashboard missing Alpine modal close handling %q", want)
		}
	}
}

// TestDashboard_ModelTestParseFailureMarksError pins the model-test state
// fix: a 2xx response whose body fails to parse must render as a failure,
// not keep the success styling.
func TestDashboard_ModelTestParseFailureMarksError(t *testing.T) {
	data, err := fs.ReadFile(webStaticFS(t), "js/dashboard.js")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "ok = false;") {
		t.Error("dashboard.js model test handler does not mark parse failures as errors")
	}
}

// TestRequestsPartial_StatusToneHook pins the status tone test hook: a 200
// row emits data-status-tone="green", which the template's {{if eq}}
// branches map to achromatic styling (palette stays achromatic per
// design.md).
func TestRequestsPartial_StatusToneHook(t *testing.T) {
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
	}, mustLoadTemplates(t), webStaticFS(t))

	rr := serveViaRoutes(h, "GET", "/partials/requests")
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	if !strings.Contains(body, `data-status-tone="green"`) || !strings.Contains(body, ">kilo<") {
		t.Errorf("200 kilo row should carry the green status hook, got: %s", body)
	}
}

// TestProvidersPage_Slice2Pins locks the slice-2 provider UX so future
// template edits cannot silently drop it: the hidden combo form, the busy
// helper, the status-checked getJSON, named tier fields, and the empty-state
// hints. Rendering moved client-side (Alpine + JSON API), so the behavioral
// pins now read providers.js.
func TestProvidersPage_Slice2Pins(t *testing.T) {
	h := newTestHandler(t)
	w := serveViaRoutes(h, "GET", "/providers")
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		`id="combo-form"`, // coherent single combo editor
		`id="combo-tiers"`,
		`id="provider-modal"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("providers page missing slice-2 markup %q", want)
		}
	}
	js := readStaticFile(t, "js/providers.js")
	for _, want := range []string{
		"function withBusy(",      // in-flight button states
		"function getJSON(",       // HTTP-status-checked fetch
		`name="tier_provider"`,    // named dynamic form fields
		"no custom providers yet", // actionable empty state
		"no combos yet",           // actionable empty state
		"no proxy pools yet",      // actionable empty state
		"poolLastTrigger",         // focus restore
	} {
		if !strings.Contains(js, want) {
			t.Errorf("providers.js missing slice-2 behavior %q", want)
		}
	}
}

// TestProvidersPage_PoolModalA11y pins the pool modal's accessible behavior
// so future template edits cannot silently drop it: focus trap, focus
// restore, keyboard close, and help-text associations.
func TestProvidersPage_PoolModalA11y(t *testing.T) {
	h := newTestHandler(t)
	w := serveViaRoutes(h, "GET", "/providers")
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		`id="pool-modal"`,                    // modal exists
		`aria-labelledby="pool-modal-title"`, // titled dialog
		`for="pf-name"`,                      // labeled inputs
		`for="pf-proxy-url"`,
		`for="pf-no-proxy"`,
		`for="pf-token"`,
		`aria-describedby="pf-name-help"`, // help-text associations
		`aria-describedby="pf-no-proxy-help"`,
		`aria-describedby="pf-enabled-help"`,
		`aria-describedby="pf-strict-help"`,
		`id="pool-modal-close"`, // keyboard-reachable close
	} {
		if !strings.Contains(body, want) {
			t.Errorf("pool modal missing accessible behavior %q", want)
		}
	}
}

// TestAppCSS_DesignTokens pins the design.md token swap (achromatic palette,
// single destructive accent) against the built stylesheet.
func TestAppCSS_DesignTokens(t *testing.T) {
	css := readStaticFile(t, "css/app.css")
	for _, banned := range []string{"#00FF41", "#FF0040", "#FFB454", "#0F1115"} {
		if strings.Contains(css, banned) {
			t.Errorf("app.css still hardcodes legacy neon %s", banned)
		}
	}
	for _, want := range []string{
		"--color-canvas:",
		"--color-paper:",
		"--color-ink:",
		"--color-hairline:",
		"--color-ember:",
		"#f5f5f5",
		"#0a0a0a",
		"#e5e5e5",
		"#e7000b",
	} {
		if !strings.Contains(css, want) {
			t.Errorf("app.css missing design.md token %q", want)
		}
	}
}

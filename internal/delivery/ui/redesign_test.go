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

// TestRequestsPartial_LocalTimeHook pins the timezone contract: each row
// time is a <time> with an ISO datetime attr and data-localtime, so
// dashboard.js can rewrite it in the viewer's timezone. The UTC fallback
// text stays for no-JS.
func TestRequestsPartial_LocalTimeHook(t *testing.T) {
	h := New(&fakeData{
		metrics: map[string]any{},
		models:  []domain.Model{},
		reqs: []domain.RequestLogEntry{
			{Ts: time.Date(2026, 9, 22, 12, 0, 0, 0, time.UTC), Method: "POST", Path: "/v1/chat/completions", Model: "m", Upstream: "kilo", Status: 200, DurationMs: 5, IP: "127.0.0.1"},
		},
		ts: nil, uptime: 1, start: time.Now().Unix(),
	}, mustLoadTemplates(t), webStaticFS(t))

	rr := serveViaRoutes(h, "GET", "/partials/requests")
	if rr.Code != 200 {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	body := rr.Body.String()
	for _, want := range []string{
		`<time datetime="2026-09-22T12:00:00Z" data-localtime>`,
		`>12:00:00</time>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("requests row missing local-time hook %q, got: %s", want, body)
		}
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
		"window.FG",               // shared helpers from ui.js
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
	shared := readStaticFile(t, "js/ui.js")
	for _, want := range []string{
		"function withBusy(", // in-flight button states
		"function getJSON(",  // HTTP-status-checked fetch
	} {
		if !strings.Contains(shared, want) {
			t.Errorf("ui.js missing shared behavior %q", want)
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

// TestSettingsPage_ClientKeysSection pins the settings page contract:
// API key management table, creation form, and the JS that drives them.
// Shared fetch/DOM helpers live in ui.js; page scripts bind via window.FG.
func TestSettingsPage_ClientKeysSection(t *testing.T) {
	h := newTestHandler(t)
	w := serveViaRoutes(h, "GET", "/settings")
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		`id="key-table"`,
		`id="key-err"`,
		`id="key-created"`,
		`id="key-form"`,
		`id="key-new"`,
		`id="key-save"`,
		`id="key-cancel"`,
		`id="kf-id"`,
		`id="kf-name"`,
		`id="kf-enabled"`,
		`for="kf-name"`,
		`/api/api-keys`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("settings client keys missing %q", want)
		}
	}
	js := readStaticFile(t, "js/settings.js")
	for _, want := range []string{
		"window.FG", // shared helpers from ui.js
		"loadKeys",
		"renderKeys",
		"keyCreated",
		"showCreatedKey", // copyable one-time secret banner
		"/api/api-keys/",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("settings.js missing %q", want)
		}
	}
	shared := readStaticFile(t, "js/ui.js")
	for _, want := range []string{
		"window.FG",
		"function esc",
		"function show",
		"function getJSON",
		"function withBusy",
	} {
		if !strings.Contains(shared, want) {
			t.Errorf("ui.js missing %q", want)
		}
	}
	prv := readStaticFile(t, "js/providers.js")
	if strings.Contains(prv, "function getJSON") || strings.Contains(prv, "function withBusy") {
		t.Errorf("providers.js must use shared ui.js helpers, not duplicate them")
	}
}

// TestSettingsPage_Nav pins the settings nav entry (desktop + mobile).
func TestSettingsPage_Nav(t *testing.T) {
	h := newTestHandler(t)
	for _, path := range []string{"/", "/providers", "/settings"} {
		w := serveViaRoutes(h, "GET", path)
		if w.Code != 200 {
			t.Fatalf("%s: status = %d, want 200", path, w.Code)
		}
		if !strings.Contains(w.Body.String(), `href="/settings"`) {
			t.Errorf("%s: nav missing settings entry", path)
		}
	}
}

// TestProvidersPage_BuiltinProxySection pins the built-in providers'
// proxy section: one labeled dropdown per builtin plus the JS that loads
// and saves their relay selection.
func TestProvidersPage_BuiltinProxySection(t *testing.T) {
	h := newTestHandler(t)
	w := serveViaRoutes(h, "GET", "/providers")
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		`id="builtin-table"`,
		`id="builtin-err"`,
		`id="builtin-proxy-opencode"`,
		`id="builtin-proxy-kilo"`,
		`id="builtin-proxy-llm7"`,
		`data-builtin="opencode"`,
		`data-builtin="kilo"`,
		`data-builtin="llm7"`,
		`/api/builtin-proxies`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("builtin proxy section missing %q", want)
		}
	}
	js := readStaticFile(t, "js/providers.js")
	for _, want := range []string{
		"loadBuiltinProxies",
		"renderBuiltinProxyOptions",
		"setBuiltinProxy",
	} {
		if !strings.Contains(js, want) {
			t.Errorf("providers.js missing %q", want)
		}
	}
}

// TestProvidersPage_ProviderEditorA11y pins the provider editor's dialog
// contract: titled role=dialog, labeled inputs, a model checklist filter,
// and the JS behaviors behind empty-save, focus restore, header warnings,
// and human-readable test output.
func TestProvidersPage_ProviderEditorA11y(t *testing.T) {
	h := newTestHandler(t)
	w := serveViaRoutes(h, "GET", "/providers")
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	body := w.Body.String()
	for _, want := range []string{
		`id="provider-modal"`,
		`role="dialog"`, // titled dialog
		`aria-labelledby="provider-modal-title"`,
		`for="f-name"`, // labeled inputs
		`for="f-base-url"`,
		`for="f-api-keys"`,
		`for="f-headers"`,
		`for="f-refresh"`,
		`for="f-priority"`,
		`for="f-enabled"`,
		`for="f-proxy"`,        // per-provider proxy dropdown
		`inputmode="url"`,      // URL keyboard on mobile
		`id="f-models-filter"`, // model checklist filter
		`id="f-models-select-all"`,
		`id="f-models-deselect-all"`,
		`id="f-proxy"`,
		`id="f-api-keys-label"`, // hint toggles new vs edit
	} {
		if !strings.Contains(body, want) {
			t.Errorf("provider editor missing %q", want)
		}
	}
	js := readStaticFile(t, "js/providers.js")
	for _, want := range []string{
		"no models selected",   // empty-save guard on new providers
		"restoreProviderFocus", // focus restore after save/delete
		"testSummary",          // human-readable test output
		"applyModelFilter",     // checklist filter behavior
		"setModelsChecked",     // bulk select/deselect
		"parseProxy",           // per-provider proxy selection
		"renderProxyOptions",   // proxy dropdown options from pools
		"line(s) without",      // malformed header warning
	} {
		if !strings.Contains(js, want) {
			t.Errorf("providers.js missing %q", want)
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

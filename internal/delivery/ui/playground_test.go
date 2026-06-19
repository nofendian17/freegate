package ui

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"

	"freegate/internal/model"
)

// TestPlaygroundCSSNoDesignViolations asserts that the playground CSS block
// (delimited by the marker comments we add) does not introduce any pattern
// that violates the TerminalUI design system: non-zero border-radius,
// non-`none` box-shadow, or a sans-serif font-family declaration.
func TestPlaygroundCSSNoDesignViolations(t *testing.T) {
	const marker = "/* Playground Modal */"
	const cssPath = "../../../web/static/css/app.css"

	data, err := os.ReadFile(cssPath)
	if err != nil {
		t.Fatalf("read %s: %v", cssPath, err)
	}
	css := string(data)
	start := strings.Index(css, marker)
	if start == -1 {
		t.Skip("playground CSS section not yet added")
	}
	section := css[start:]

	// Non-zero border-radius (e.g. `border-radius: 4px`). `0`, `0px`, `0%` are fine.
	nonZeroRadius := regexp.MustCompile(`(?i)border-radius\s*:\s*[1-9][0-9.]*\s*(px|rem|em|%)`)
	if loc := nonZeroRadius.FindStringIndex(section); loc != nil {
		t.Errorf("playground CSS contains non-zero border-radius: %q", section[loc[0]:loc[1]])
	}

	// Any box-shadow value other than the keyword `none`.
	boxShadow := regexp.MustCompile(`(?i)box-shadow\s*:\s*([^;}]+)`)
	if m := boxShadow.FindStringSubmatch(section); m != nil {
		val := strings.TrimSpace(m[1])
		if !strings.EqualFold(val, "none") {
			t.Errorf("playground CSS contains box-shadow: %q", val)
		}
	}

	// Inline font-family declaration that names a non-mono family.
	// We allow the existing --mono variable and any value that includes the
	// word "mono" (e.g. "JetBrains Mono", "monospace").
	fontFamily := regexp.MustCompile(`(?i)font-family\s*:\s*([^;}]+)`)
	for _, m := range fontFamily.FindAllStringSubmatch(section, -1) {
		val := strings.ToLower(strings.TrimSpace(m[1]))
		if strings.Contains(val, "mono") {
			continue
		}
		if strings.HasPrefix(val, "var(--") {
			continue
		}
		t.Errorf("playground CSS contains non-mono font-family: %q", m[1])
	}
}

// TestPlaygroundModalTemplateLoads verifies the partial is registered
// with the loader and renders the expected element IDs.
func TestPlaygroundModalTemplateLoads(t *testing.T) {
	tpl, err := LoadTemplates(webTemplatesFS(t))
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "partials/playground_modal.html", map[string]any{}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	body := buf.String()

	for _, id := range []string{
		`id="pg-overlay"`,
		`id="pg-panel"`,
		`id="pg-model"`,
		`id="pg-stream"`,
		`id="pg-system"`,
		`id="pg-list"`,
		`id="pg-empty"`,
		`id="pg-input"`,
		`id="pg-send"`,
		`id="pg-stop"`,
		`id="pg-close"`,
		`id="pg-clear"`,
		`id="pg-system-toggle"`,
	} {
		if !strings.Contains(body, id) {
			t.Errorf("playground modal missing %s", id)
		}
	}
}

// TestPlaygroundJSExists is a smoke test that catches gross omissions in
// the JS module. It does not execute the code — that happens in a real
// browser. It asserts the file exists and contains the function and
// identifier names the rest of the system depends on.
func TestPlaygroundJSExists(t *testing.T) {
	const jsPath = "../../../web/static/js/playground.js"
	data, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("read %s: %v", jsPath, err)
	}
	js := string(data)

	// Persistence + shim identity + streaming functions (must keep working across refactors)
	must := []string{
		"freegate.playground.v1",      // localStorage key
		"window.fgPlayground",         // public surface
		"function open(",              // modal open
		"function close(",             // modal close
		"function clear(",             // clear thread
		"function load(",              // localStorage load
		"function save(",              // localStorage save
		"function onInputKeydown(",    // Enter-to-send
		"function onModelsLoaded(",    // model select restore
		"function onSystemInput(",     // system prompt input
		"function toggleSystem(",      // collapse/expand
		"function onStreamToggle(",    // stream checkbox handler
		"function requestBody(",       // build OpenAI request body
		"function beforeSend(",        // validation + optimistic UI
		"function send(",              // form submit handler (hx-on:submit)
		"function handleFetchResponse(", // fetch() response handler (non-streaming)
		"function appendUserMessage(", // optimistic user bubble
		"function createAssistantPlaceholder(", // optimistic assistant bubble
		"function finalizeAssistant(", // close out the assistant bubble
		"function parseSSEChunks(",    // SSE streaming parser
		"function stopStreaming(",     // abort/stop handler
	}
	for _, want := range must {
		if !strings.Contains(js, want) {
			t.Errorf("playground.js missing %q", want)
		}
	}

	// The streaming implementation uses native ReadableStream / TextDecoder /
	// getReader() — these are now required, not banned. See spec.md Sprint 1.
	// We still ban legacy function names and eval/document.write.
	for _, banned := range []string{
		"streamResponse(", // legacy streaming function
		"nonStreamResponse(", // legacy non-streaming function
		"loadModels(",     // legacy model fetcher
		"function handleResponse(", // legacy htmx:after-request hook
	} {
		if strings.Contains(js, banned) {
			t.Errorf("playground.js contains legacy pattern %q", banned)
		}
	}

	// Guardrail: never use eval or document.write.
	for _, bad := range []string{"eval(", "document.write"} {
		if strings.Contains(js, bad) {
			t.Errorf("playground.js contains forbidden pattern %q", bad)
		}
	}
}

// TestPlaygroundModalUsesHTMX asserts that the modal template now drives
// behavior via HTMX attributes (hx-get for the model picker, hx-on:* for
// event wiring) and uses hx-on:submit + the shim's send() for the chat
// form. The earlier hx-post + hx-vals='js:...' design was removed because
// htmx 2.0.4's js: expression evaluator chokes on member-access expressions
// (see .claude/validation/playground-rewrite-2026-06-07/report.md).
func TestPlaygroundModalUsesHTMX(t *testing.T) {
	const tplPath = "../../../web/templates/partials/playground_modal.html"
	data, err := os.ReadFile(tplPath)
	if err != nil {
		t.Fatalf("read %s: %v", tplPath, err)
	}
	body := string(data)

	must := []string{
		`hx-get="/partials/playground/models"`,            // model picker loads via HTMX
		`hx-on:submit="window.fgPlayground.send(event)"`, // form submit goes to shim
		`hx-on:htmx:after-request="window.fgPlayground.onModelsLoaded()"`, // model list swap
		`window.fgPlayground.close`,            // close trigger
		`window.fgPlayground.clear`,            // clear trigger
		`window.fgPlayground.toggleSystem`,     // system prompt collapse
		`window.fgPlayground.onInputKeydown`,   // Enter-to-send
		`window.fgPlayground.onStreamToggle`,   // stream checkbox change
		`window.fgPlayground.stopStreaming`,    // stop button click
	}
	for _, want := range must {
		if !strings.Contains(body, want) {
			t.Errorf("playground_modal.html missing %q", want)
		}
	}

	// Ban the old hx-post + hx-vals='js:...' design — it does not work in
	// htmx 2.0.4 (see validation report). The form must use hx-on:submit
	// with a shim function that calls fetch() directly.
	for _, banned := range []string{
		`hx-post="/v1/chat/completions"`,
		`hx-vals='js:`,
		`hx-on:htmx:before-request`,
		`hx-on:htmx:after-request="window.fgPlayground.handleResponse`,
		`onsubmit="window.fgPlayground.send`,
	} {
		if strings.Contains(body, banned) {
			t.Errorf("playground_modal.html still uses legacy pattern %q; should use hx-on:submit + shim send()", banned)
		}
	}
}

// TestPlaygroundModelsPartial asserts the new partial renders a proper
// <option> list (one per model) and the empty-state fallback.
func TestPlaygroundModelsPartial(t *testing.T) {
	tpl, err := LoadTemplates(webTemplatesFS(t))
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}

	// Happy path: with models
	var buf bytes.Buffer
	models := []model.Model{
		{ID: "test-model-1", Provider: "opencode", IsFree: true},
		{ID: "test-model-2", Provider: "kilo", IsFree: true},
	}
	if err := tpl.ExecuteTemplate(&buf, "partials/playground_models.html", models); err != nil {
		t.Fatalf("execute with models: %v", err)
	}
	body := buf.String()
	for _, want := range []string{
		`<option value="test-model-1">test-model-1</option>`,
		`<option value="test-model-2">test-model-2</option>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("playground_models.html missing %q (got: %s)", want, body)
		}
	}

	// Empty path: no models
	buf.Reset()
	if err := tpl.ExecuteTemplate(&buf, "partials/playground_models.html", []model.Model{}); err != nil {
		t.Fatalf("execute empty: %v", err)
	}
	body = buf.String()
	if !strings.Contains(body, "// no models available") {
		t.Errorf("playground_models.html missing empty-state placeholder (got: %s)", body)
	}
}

// TestDashboardWiresPlayground asserts that the dashboard template
// includes the playground modal partial, the playground.js script,
// and the open-playground button. This is a string-search guardrail
// that catches wiring regressions without running a browser.
func TestDashboardWiresPlayground(t *testing.T) {
	const tplPath = "../../../web/templates/dashboard.html"
	data, err := os.ReadFile(tplPath)
	if err != nil {
		t.Fatalf("read %s: %v", tplPath, err)
	}
	body := string(data)

	must := []string{
		`id="open-playground"`,                                   // open button
		`partials/playground_modal.html`,                         // modal include
		`<script src="/static/js/playground.js" defer></script>`, // js include
	}
	for _, want := range must {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard.html missing %q", want)
		}
	}
}

// TestPlaygroundCSSHasMobileRules asserts that the playground CSS section
// includes mobile-friendly rules: small-phone breakpoint, touch target sizing,
// iOS-safe font sizes for inputs, and safe-area insets.
func TestPlaygroundCSSHasMobileRules(t *testing.T) {
	const marker = "/* Playground Modal */"
	const cssPath = "../../../web/static/css/app.css"

	data, err := os.ReadFile(cssPath)
	if err != nil {
		t.Fatalf("read %s: %v", cssPath, err)
	}
	css := string(data)
	start := strings.Index(css, marker)
	if start == -1 {
		t.Skip("playground CSS section not yet added")
	}
	section := css[start:]

	must := []string{
		"@media (max-width: 480px)", // small phones
		"@media (max-width: 768px)", // tablets / large phones
		"safe-area-inset",           // iOS notch / home indicator
		"min-height: 44px",          // WCAG touch target
		"min-width: 44px",           // WCAG touch target
		"font-size: 16px",           // iOS no-zoom
	}
	for _, want := range must {
		if !strings.Contains(section, want) {
			t.Errorf("playground CSS section missing mobile rule %q", want)
		}
	}
}

// TestErrorModalCSSHasMobileRules asserts that the error modal CSS section
// (shown when clicking an .error-link in the recent-requests table) becomes
// full-width on mobile, with safe-area insets and a WCAG-compliant close
// button touch target. This is a string-search guardrail that catches
// responsive regressions without running a browser.
func TestErrorModalCSSHasMobileRules(t *testing.T) {
	const startMarker = "/* ----- Error Modal ----- */"
	const endMarker = "/* ----- Error Link"
	const cssPath = "../../../web/static/css/app.css"

	data, err := os.ReadFile(cssPath)
	if err != nil {
		t.Fatalf("read %s: %v", cssPath, err)
	}
	css := string(data)
	start := strings.Index(css, startMarker)
	if start == -1 {
		t.Skip("error modal CSS section not yet added")
	}
	end := strings.Index(css[start:], endMarker)
	if end == -1 {
		t.Fatalf("error modal CSS section end marker %q not found", endMarker)
	}
	section := css[start : start+end]

	must := []string{
		"@media (max-width: 480px)", // small phones
		"@media (max-width: 768px)", // tablets / large phones
		"safe-area-inset",           // iOS notch / home indicator
		"min-height: 44px",          // WCAG touch target (close button)
		"min-width: 44px",           // WCAG touch target (close button)
		"width: 100%",               // full-width on mobile (not 480px)
	}
	for _, want := range must {
		if !strings.Contains(section, want) {
			t.Errorf("error modal CSS section missing mobile rule %q", want)
		}
	}
}

package ui

import (
	"bytes"
	"os"
	"regexp"
	"strings"
	"testing"
)

// TestPlaygroundCSSNoDesignViolations asserts that the playground CSS block
// (delimited by the marker comments we add) does not introduce any pattern
// that violates the design system: non-zero border-radius outside the
// documented 18px/24px scale, non-`none` box-shadow, or a non-mono
// font-family declaration. ponytail: the built Tailwind stylesheet is
// generated — audit the input (web/assets/tailwind.css) when this fails.
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
		// Alpine component contract: overlay state, message loop, events.
		`x-data="playground()"`,
		`x-show="open"`,
		`x-on:open-playground.window`,
		`x-for="(m, i) in messages"`,
		`@submit.prevent="send()"`,
	} {
		if !strings.Contains(body, id) {
			t.Errorf("playground modal missing %s", id)
		}
	}
}

// TestPlaygroundAlpineComponent is a smoke test that pins the Alpine
// component contract: registration name, persistence, thread rendering,
// streaming parser, and abort wiring. It does not execute the code —
// that happens in a real browser.
func TestPlaygroundAlpineComponent(t *testing.T) {
	const jsPath = "../../../web/static/js/playground.js"
	data, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("read %s: %v", jsPath, err)
	}
	js := string(data)

	must := []string{
		"Alpine.data('playground'", // Alpine component registration
		"freegate.playground.v1",   // localStorage key
		"function parseSSEChunks(", // SSE streaming parser
		"sendStream",               // streaming path
		"sendOnce",                 // non-streaming path
		"requestBody",              // OpenAI request body builder
		"loadModels",               // /v1/models fetch
		"new AbortController",      // stop/abort wiring
		"pushAssistant",            // assistant bubble finalize
		"onEnter",                  // Enter-to-send vs Shift+Enter newline
		"alpine:init",
	}
	for _, want := range must {
		if !strings.Contains(js, want) {
			t.Errorf("playground.js missing %q", want)
		}
	}

	// Guardrail: never use eval or document.write.
	for _, bad := range []string{"eval(", "document.write"} {
		if strings.Contains(js, bad) {
			t.Errorf("playground.js contains forbidden pattern %q", bad)
		}
	}
}

// TestPlaygroundModalUsesHTMX asserts the modal is declarative: HTMX polls
// the read endpoints while Alpine owns the interactive state (no legacy
// hx-on:submit + window.fgPlayground shim wiring).
func TestPlaygroundModalUsesHTMX(t *testing.T) {
	const tplPath = "../../../web/templates/partials/playground_modal.html"
	data, err := os.ReadFile(tplPath)
	if err != nil {
		t.Fatalf("read %s: %v", tplPath, err)
	}
	body := string(data)

	must := []string{
		`x-data="playground()"`,            // Alpine owns modal state
		`@submit.prevent="send()"`,         // form submit goes to the component
		`@click="close()"`,                 // close trigger
		`@click="clear()"`,                 // clear trigger
		`@keydown.enter="onEnter($event)"`, // Enter-to-send (Shift+Enter = newline)
		`x-model="model"`,                  // model picker binding
		`x-model="stream"`,                 // stream checkbox binding
		`x-model="system"`,                 // system prompt binding
		`x-model="input"`,                  // input binding
		`@click="stop()"`,                  // stop button
		`x-for="(m, i) in messages"`,       // thread rendering
	}
	for _, want := range must {
		if !strings.Contains(body, want) {
			t.Errorf("playground_modal.html missing %q", want)
		}
	}

	// The hx-on:* + window.fgPlayground shim design is gone — Alpine owns
	// all event wiring now.
	for _, banned := range []string{
		`hx-post="/v1/chat/completions"`,
		`hx-vals='js:`,
		`hx-on:htmx:before-request`,
		`window.fgPlayground`,
		`onsubmit="window.fgPlayground.send`,
	} {
		if strings.Contains(body, banned) {
			t.Errorf("playground_modal.html still uses legacy shim pattern %q; Alpine owns event wiring now", banned)
		}
	}
}

// TestPlaygroundModelsFallback asserts the Alpine playground degrades
// gracefully: the model picker renders its loading placeholder when the
// /v1/models fetch has not resolved yet.
func TestPlaygroundModelsFallback(t *testing.T) {
	tpl, err := LoadTemplates(webTemplatesFS(t))
	if err != nil {
		t.Fatalf("LoadTemplates: %v", err)
	}

	var buf bytes.Buffer
	if err := tpl.ExecuteTemplate(&buf, "partials/playground_modal.html", map[string]any{}); err != nil {
		t.Fatalf("execute: %v", err)
	}
	body := buf.String()
	for _, want := range []string{
		`Loading models`,
		`x-for="m in models"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("playground_modal.html missing model fallback %q", want)
		}
	}
}

// TestDashboardWiresPlayground asserts the dashboard wires up the playground:
// the modal partial is included and the Alpine component script is listed for
// the shared layout head, where page scripts are emitted before alpine.min.js
// so components register before Alpine auto-starts.
func TestDashboardWiresPlayground(t *testing.T) {
	layoutBytes, err := os.ReadFile("../../../web/templates/layout.html")
	if err != nil {
		t.Fatalf("read layout.html: %v", err)
	}
	layout := string(layoutBytes)
	for _, want := range []string{
		`{{range .Scripts}}<script src="{{.}}" defer></script>`, // page script slot
		`<script src="/static/js/alpine.min.js" defer></script>`,
		`id="open-playground"`,         // open button
		`$dispatch('open-playground')`, // Alpine event trigger
	} {
		if !strings.Contains(layout, want) {
			t.Errorf("layout.html missing %q", want)
		}
	}

	// alpine.min.js must load after the page scripts: Alpine auto-starts in a
	// microtask, so a later Alpine.data(...) registration would never run and
	// every component would break at runtime.
	if strings.Index(layout, ".Scripts") > strings.Index(layout, "alpine.min.js") {
		t.Error("alpine.min.js is loaded before the page scripts; Alpine would start before components register")
	}

	page, err := os.ReadFile("../../../web/templates/dashboard.html")
	if err != nil {
		t.Fatalf("read dashboard.html: %v", err)
	}
	if !strings.Contains(string(page), "partials/playground_modal.html") {
		t.Error("dashboard.html missing playground modal include")
	}

	handlerSrc, err := os.ReadFile("../../../internal/delivery/ui/dashboard.go")
	if err != nil {
		t.Fatalf("read dashboard.go: %v", err)
	}
	for _, want := range []string{"dashboard.js", "playground.js"} {
		if !strings.Contains(string(handlerSrc), want) {
			t.Errorf("dashboard.go does not register %q in the script slot", want)
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

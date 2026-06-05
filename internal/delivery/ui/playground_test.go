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
		`id="pg-close"`,
		`id="pg-clear"`,
		`id="pg-system-toggle"`,
	} {
		if !strings.Contains(body, id) {
			t.Errorf("playground modal missing %s", id)
		}
	}
}

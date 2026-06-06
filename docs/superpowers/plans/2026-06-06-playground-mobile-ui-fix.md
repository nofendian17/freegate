# Playground Mobile UI Fix Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make the chat playground usable on mobile (320px–768px) and small tablets in portrait/landscape, while preserving the desktop layout (≥769px) exactly. Fix touch target sizes, header overflow, iOS safe-areas, font sizes, and input ergonomics.

**Architecture:** Pure CSS additions inside the existing `/* Playground Modal */` section of `web/static/css/app.css`. No new templates, no JS changes, no Go changes — CSS-only fixes keep the blast radius small and avoid re-testing all the Go wiring tests.

**Tech Stack:** CSS media queries, CSS custom properties (existing `--space-*`, `--text`, `--primary`, etc.), `env(safe-area-inset-*)`, flexbox.

**Spec:** `docs/superpowers/specs/2026-06-06-chat-playground-design.md` (mobile width requirement: `full width on <768px` — already met for the panel, but inner controls are not).

---

## Mobile Issues Found (Phase 1: Investigation)

| # | File:Line | Issue | Impact |
|---|-----------|-------|--------|
| 1 | `app.css:1049-1055` | Single mobile breakpoint at 768px only sets `width: 100vw` and shrinks model select. Header controls (model + stream + clear + close) don't wrap, overflow horizontally on <400px. | Layout breaks on small phones. |
| 2 | `app.css:1049-1055` | Stream toggle uses 12px text — too small for touch. | Hard to read/tap. |
| 3 | `app.css:1023-1026` | Close button (`&times;`) has no min-width/min-height — touch target likely <24px (WCAG 2.5.5 requires 44x44). | Hard to tap close. |
| 4 | `app.css:1030-1032` | Clear button has no explicit touch-target size. | Hard to tap clear. |
| 5 | `app.css:1042-1044` | Input textarea font-size 13px — iOS Safari auto-zooms on focus when font-size < 16px. | Page zooms when typing. |
| 6 | `app.css:1042-1044` | System prompt textarea 13px — same iOS issue. | Same. |
| 7 | `app.css:999-1002` (panel) | No `env(safe-area-inset-*)` padding — iOS notch and home indicator overlap content. | Content hidden under notch/home bar. |
| 8 | `app.css:1013-1018` (header) | `.pg-header` has no top safe-area padding. | Title clipped by notch. |
| 9 | `app.css:1042-1046` (input row) | Send button is inline right of textarea — on 320px, the button takes ~80px leaving textarea <240px. | Cramped input. |
| 10 | `app.css:1026-1028` (close) | Close button uses `padding: 0 var(--space-1)` — visually small. | Hard to see. |
| 11 | `app.css:956-958` (msg body) | `.msg-body` 13px — readable on desktop, slightly small on mobile. | Marginal. |
| 12 | `app.css:963-966` (msg meta) | `.msg-meta` 11px — too small on mobile. | Hard to read timestamps. |
| 13 | `app.css:1026-1028` (close) | No visible border/background — close is hard to find. | UX issue. |
| 14 | (CSS) | No landscape-specific rules — on phone landscape (height <500px), footer with system prompt + input may dominate. | Cramped. |
| 15 | `playground.js` (no changes) | When `modal-open` class is on body, body is not scroll-locked — page scrolls behind modal on iOS. | Scroll bleed. |

**Decisions made autonomously:**
- **CSS-only** (no JS, no Go, no template changes) — minimum blast radius.
- **Two breakpoints**: `≤480px` (small phones) and `≤768px` (tablets/phones) plus landscape.
- **Touch target floor**: 44x44px for all interactive elements on mobile.
- **iOS font-size floor**: 16px for any user-typed input on mobile.
- **No template markup change** — keep the existing `.pg-header-controls` flex layout; just make it wrap and reflow on mobile via CSS.

---

## File Structure

### Modified files (2)

| File | Change |
|---|---|
| `web/static/css/app.css` | Add mobile + landscape + touch-target rules inside the existing `/* Playground Modal */` section (no marker boundary — append after current mobile block at line ~1055). |
| `internal/delivery/ui/playground_test.go` | Add `TestPlaygroundCSSHasMobileRules` test that asserts key mobile-friendly rules are present in the playground CSS section. |

### Unchanged

All Go code, all HTML templates, all JS. Pure CSS fix.

---

## Task 1: Add mobile CSS rules (TDD)

**Files:**
- Modify: `internal/delivery/ui/playground_test.go` (add test)
- Modify: `web/static/css/app.css` (add rules)

- [ ] **Step 1: Write the failing test**

Append to `internal/delivery/ui/playground_test.go`:

```go
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
		"@media (max-width: 480px)",         // small phones
		"@media (max-width: 768px)",         // tablets / large phones
		"safe-area-inset",                    // iOS notch / home indicator
		"min-height: 44px",                   // WCAG touch target
		"min-width: 44px",                    // WCAG touch target
		"font-size: 16px",                    // iOS no-zoom
	}
	for _, want := range must {
		if !strings.Contains(section, want) {
			t.Errorf("playground CSS section missing mobile rule %q", want)
		}
	}
}
```

- [ ] **Step 2: Run the test — confirm RED**

```bash
cd /home/deployer/freegate && go test ./internal/delivery/ui/ -run TestPlaygroundCSSHasMobileRules -v
```

Expected: `FAIL` with messages about missing rules.

- [ ] **Step 3: Add the mobile CSS rules**

Find the existing `@media (max-width: 768px)` block in `web/static/css/app.css` (around line 1052). After that block, append the new mobile rules. The block to add (paste after the closing `}` of the 768px block):

```css
/* === Mobile refinements (small phones, touch, iOS) === */

@media (max-width: 768px) {
  /* Safe-area insets for iOS notch + home indicator */
  .pg-header {
    padding-top: calc(var(--space-3) + env(safe-area-inset-top, 0px));
  }
  .pg-footer {
    padding-bottom: calc(var(--space-3) + env(safe-area-inset-bottom, 0px));
    padding-left: calc(var(--space-4) + env(safe-area-inset-left, 0px));
    padding-right: calc(var(--space-4) + env(safe-area-inset-right, 0px));
  }

  /* Header controls wrap to a second line when crowded */
  .pg-header {
    flex-wrap: wrap;
  }
  .pg-header-controls {
    flex-wrap: wrap;
    gap: var(--space-2);
    row-gap: var(--space-2);
  }

  /* Larger touch targets on mobile */
  .pg-close,
  .pg-clear,
  .pg-send,
  .pg-system-toggle {
    min-height: 44px;
    min-width: 44px;
    padding: var(--space-2) var(--space-3);
  }

  /* iOS no-zoom: 16px font on textareas + buttons */
  .pg-input,
  .pg-system {
    font-size: 16px;
  }

  /* Bump font sizes for readability on mobile */
  .msg-body { font-size: 14px; }
  .msg-meta { font-size: 12px; }
  .pg-stream-toggle { font-size: 14px; }
  .pg-system-toggle { font-size: 14px; }

  /* Close button: visible border on mobile (no hover state) */
  .pg-close {
    border: 1px solid var(--border);
  }
}

@media (max-width: 480px) {
  /* Tiny phones: model select on its own row, full width */
  .pg-header-controls {
    width: 100%;
    flex-wrap: wrap;
  }
  .pg-model {
    flex: 1 1 100%;
    min-width: 0;
    max-width: 100%;
  }
  .pg-stream-toggle,
  .pg-clear,
  .pg-close {
    flex: 0 0 auto;
  }

  /* Send button: full width below the textarea */
  .pg-input-row {
    flex-direction: column;
  }
  .pg-input-row .btn-primary {
    align-self: stretch;
    width: 100%;
  }
}

@media (max-width: 768px) and (orientation: landscape) and (max-height: 500px) {
  /* Landscape phones: compact footer, system prompt hidden by default */
  .pg-body { padding: var(--space-2) var(--space-3); }
  .pg-system-wrap { display: none; }
  .pg-input { min-height: 44px; }
}
```

- [ ] **Step 4: Re-run the test — confirm GREEN**

```bash
cd /home/deployer/freegate && go test ./internal/delivery/ui/ -run TestPlaygroundCSSHasMobileRules -v
```

Expected: `PASS`.

---

## Task 2: Verify build + full test suite

**Files:** none (verification only)

- [ ] **Step 1: Build**

```bash
cd /home/deployer/freegate && go build ./...
```

Expected: clean exit.

- [ ] **Step 2: Vet**

```bash
cd /home/deployer/freegate && go vet ./...
```

Expected: clean exit.

- [ ] **Step 3: Full UI test suite**

```bash
cd /home/deployer/freegate && go test ./internal/delivery/ui/... -v
```

Expected: all tests pass (existing 4 + new 1 = 5).

- [ ] **Step 4: Confirm CSS design-system guardrail still passes**

The existing `TestPlaygroundCSSNoDesignViolations` must still pass — the new rules don't add border-radius, box-shadow, or non-mono font-family.

---

## Task 3: Commit

- [ ] **Step 1: Stage and commit**

```bash
cd /home/deployer/freegate && \
  git add web/static/css/app.css internal/delivery/ui/playground_test.go && \
  git status -sb && \
  git commit -m "fix(playground): improve mobile UI

- Wrap header controls on narrow screens (max-width 480px)
- iOS safe-area insets for notch + home indicator
- Touch targets >= 44x44px on mobile (WCAG 2.5.5)
- Input font-size 16px on mobile to prevent iOS auto-zoom
- Bumped msg-body / msg-meta / stream-toggle font sizes
- Send button full-width below textarea on tiny phones
- Hide system prompt in landscape phones (height <500px)
- Visible border on close button (no hover on touch)
- TestPlaygroundCSSHasMobileRules guardrail test"
```

---

## Verification Checklist

- [ ] CSS section contains the 6 required mobile rules (test asserts)
- [ ] `go build ./...` passes
- [ ] `go vet ./...` passes
- [ ] `go test ./internal/delivery/ui/...` all green (5 tests)
- [ ] Existing design-system guardrail still green (no border-radius, no box-shadow, no non-mono font)
- [ ] No Go code, no HTML template, no JS changes — minimal blast radius
- [ ] Desktop (≥769px) layout unchanged

# Chat Playground Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Add a TerminalUI-styled chat playground as a slide-over modal on the freegate dashboard so operators can test any free model the proxy serves without leaving the UI.

**Architecture:** A new HTML partial + vanilla JS module call the existing `POST /v1/chat/completions` endpoint directly from the browser (same origin, no CORS, no new Go code). Streaming uses `fetch` + `ReadableStream`; non-streaming reads the full JSON body. Conversation state persists in `localStorage` under `freegate.playground.v1`. Zero changes to the Go proxy, chat handler, or request recorder.

**Tech Stack:** Go (template loader + tests), vanilla JS (no framework), HTML/HTMX (existing), CSS (existing design tokens).

**Spec:** `docs/superpowers/specs/2026-06-06-chat-playground-design.md`

---

## File Structure

### New files (4)

| File | Responsibility |
|---|---|
| `web/templates/partials/playground_modal.html` | Modal markup (header, body, footer) — all IDs prefixed `pg-` |
| `web/static/js/playground.js` | Modal behavior: open/close, model load, localStorage, render, send (stream + non-stream), error handling |
| `internal/delivery/ui/playground_test.go` | All playground-specific Go tests: CSS guardrail, template loader, JS smoke, dashboard wiring |
| `docs/superpowers/plans/2026-06-06-chat-playground.md` | This file |

### Modified files (2)

| File | Change |
|---|---|
| `web/static/css/app.css` | Append a `/* Playground Modal */` section using existing CSS variables |
| `web/templates/dashboard.html` | Wrap `nav-brand` + new `> playground` button in `.nav-left`; add `<script src="/static/js/playground.js" defer>`; include the modal partial before `</body>`; no other layout changes |

### Unchanged

All Go code. The `loader.go` template loader already recurses into `partials/`, and `embed.go` already covers the full `static/` + `templates/` trees.

---

## Task 1: Add playground CSS with design-system guardrail test (TDD)

**Files:**
- Create: `internal/delivery/ui/playground_test.go`
- Modify: `web/static/css/app.css`

- [ ] **Step 1: Write the failing guardrail test**

Create `internal/delivery/ui/playground_test.go`:

```go
package ui

import (
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
	const cssPath = "../../web/static/css/app.css"

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
```

- [ ] **Step 2: Run the test, verify it skips**

```bash
cd /home/beni/Projects/go/lab/freegate && go test ./internal/delivery/ui -run TestPlaygroundCSSNoDesignViolations -v
```

Expected: `--- SKIP: TestPlaygroundCSSNoDesignViolations (0.00s)` (no playground CSS yet).

- [ ] **Step 3: Append the playground CSS to app.css**

Append this block to the end of `web/static/css/app.css` (after the last `}` of the existing file, no leading blank line required but a trailing newline is):

```css
/* ============================================
   Playground Modal
   ============================================ */

.pg-overlay {
  position: fixed;
  inset: 0;
  background: rgba(0, 0, 0, 0.8);
  display: none;
  align-items: stretch;
  justify-content: flex-end;
  z-index: 1000;
}
.pg-overlay.open { display: flex; }

.pg-panel {
  display: flex;
  flex-direction: column;
  width: 480px;
  max-width: 100vw;
  height: 100vh;
  background: var(--bg);
  border-left: 1px solid var(--primary);
}

.pg-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: var(--space-2);
  padding: var(--space-3) var(--space-4);
  border-bottom: 1px solid var(--border);
  background: var(--surface);
}
.pg-title {
  font-size: 14px;
  font-weight: 600;
  color: var(--primary);
  letter-spacing: 0.02em;
}
.pg-header-controls {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}

.pg-model {
  background: var(--bg);
  color: var(--text);
  font-family: var(--mono);
  font-size: 12px;
  border: 1px solid var(--border);
  padding: var(--space-1) var(--space-2);
  cursor: pointer;
  min-width: 160px;
  max-width: 220px;
}
.pg-model:hover { border-color: var(--border-hover); }
.pg-model:focus-visible { border-color: var(--primary); }
.pg-model:disabled { color: var(--text-faint); cursor: not-allowed; }

.pg-stream-toggle {
  display: inline-flex;
  align-items: center;
  gap: 4px;
  font-size: 12px;
  color: var(--text-dim);
  cursor: pointer;
  user-select: none;
}
.pg-stream-toggle input { cursor: pointer; }

.pg-close {
  background: transparent;
  border: 0;
  color: var(--text-dim);
  font-family: var(--mono);
  font-size: 18px;
  cursor: pointer;
  line-height: 1;
  padding: 0 var(--space-1);
}
.pg-close:hover { color: var(--primary); }

.pg-body {
  flex: 1 1 auto;
  overflow-y: auto;
  padding: var(--space-3) var(--space-4);
  display: flex;
  flex-direction: column;
  gap: var(--space-3);
}

.pg-empty {
  color: var(--text-faint);
  font-size: 13px;
  text-align: center;
  margin: auto;
}

.msg {
  border: 1px solid var(--border);
  background: var(--surface);
  padding: var(--space-3);
  display: flex;
  flex-direction: column;
  gap: 6px;
}
.msg-meta {
  font-size: 11px;
  color: var(--text-faint);
  letter-spacing: 0.02em;
  font-family: var(--mono);
}
.msg-body {
  font-family: var(--mono);
  font-size: 13px;
  color: var(--text);
  white-space: pre-wrap;
  overflow-wrap: break-word;
  margin: 0;
  line-height: 1.5;
}
.msg-user      { border-left: 2px solid var(--tertiary); }
.msg-user      .msg-meta::before { content: "> ";       color: var(--tertiary); }
.msg-assistant { border-left: 2px solid var(--primary); }
.msg-assistant .msg-meta::before { content: "$ ";       color: var(--primary); }
.msg-error     { border-left: 2px solid var(--error); }
.msg-error     .msg-meta::before { content: "! ";       color: var(--error); }
.msg-error     .msg-body         { color: var(--error); }

.pg-footer {
  border-top: 1px solid var(--border);
  background: var(--surface);
  padding: var(--space-3) var(--space-4);
  display: flex;
  flex-direction: column;
  gap: var(--space-2);
}

.pg-system-wrap {
  display: flex;
  flex-direction: column;
  gap: var(--space-1);
}
.pg-system-wrap.collapsed .pg-system { display: none; }
.pg-system-toggle {
  background: transparent;
  border: 0;
  color: var(--text-dim);
  font-family: var(--mono);
  font-size: 12px;
  text-align: left;
  cursor: pointer;
  padding: 0;
}
.pg-system-toggle:hover { color: var(--primary); }
.pg-system {
  background: var(--bg);
  color: var(--text);
  font-family: var(--mono);
  font-size: 13px;
  border: 1px solid var(--border);
  padding: var(--space-2);
  resize: vertical;
}
.pg-system:focus-visible { border-color: var(--primary); outline: none; }

.pg-input-row {
  display: flex;
  gap: var(--space-2);
  align-items: stretch;
}
.pg-input {
  flex: 1 1 auto;
  background: var(--bg);
  color: var(--text);
  font-family: var(--mono);
  font-size: 13px;
  border: 1px solid var(--border);
  padding: var(--space-2);
  resize: vertical;
  min-height: 64px;
}
.pg-input:focus-visible { border-color: var(--primary); outline: none; }
.pg-input-row .btn-primary { align-self: flex-end; }

.pg-nav-btn { font-size: 13px; }

.nav-left {
  display: flex;
  align-items: center;
  gap: var(--space-4);
}

@media (max-width: 768px) {
  .pg-panel { width: 100vw; }
  .pg-model { min-width: 120px; max-width: 160px; }
}
```

- [ ] **Step 4: Run the test, verify it passes**

```bash
cd /home/beni/Projects/go/lab/freegate && go test ./internal/delivery/ui -run TestPlaygroundCSSNoDesignViolations -v
```

Expected: `--- PASS: TestPlaygroundCSSNoDesignViolations (0.00s)`.

- [ ] **Step 5: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add web/static/css/app.css internal/delivery/ui/playground_test.go && git commit -m "feat(playground): add TerminalUI-styled modal styles with design guardrail

Adds a Playground Modal CSS block to app.css that reuses existing
design tokens (--primary, --bg, --border, --mono, etc.) and follows
the TerminalUI design system: zero border-radius, no box-shadows, mono
font everywhere. A new Go test scans the section for any forbidden
patterns and fails on regression."
```

---

## Task 2: Add modal HTML partial with template loader test (TDD)

**Files:**
- Create: `web/templates/partials/playground_modal.html`
- Modify: `internal/delivery/ui/playground_test.go`

- [ ] **Step 1: Add the failing template loader test**

Append to `internal/delivery/ui/playground_test.go`:

```go
import (
	"bytes"
	// ...existing imports
)

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
```

- [ ] **Step 2: Run the test, verify it fails**

```bash
cd /home/beni/Projects/go/lab/freegate && go test ./internal/delivery/ui -run TestPlaygroundModalTemplateLoads -v
```

Expected: `--- FAIL: TestPlaygroundModalTemplateLoads` with `execute: ... no template "partials/playground_modal.html"`.

- [ ] **Step 3: Create the modal HTML partial**

Create `web/templates/partials/playground_modal.html`:

```html
<div id="pg-overlay" class="pg-overlay" role="dialog" aria-modal="true" aria-labelledby="pg-title" style="display:none">
  <div id="pg-panel" class="pg-panel">
    <header class="pg-header">
      <span id="pg-title" class="pg-title">> playground</span>
      <div class="pg-header-controls">
        <select id="pg-model" class="pg-model" aria-label="Model">
          <option value="">// pick a model</option>
        </select>
        <label class="pg-stream-toggle" title="Stream responses">
          <input id="pg-stream" type="checkbox" checked> stream
        </label>
        <button id="pg-clear" class="btn-tertiary" title="Clear thread" type="button">× clear</button>
        <button id="pg-close" class="pg-close" aria-label="Close playground" type="button">&times;</button>
      </div>
    </header>
    <div id="pg-list" class="pg-body" role="log" aria-live="polite">
      <div id="pg-empty" class="pg-empty">// pick a model and say something</div>
    </div>
    <footer class="pg-footer">
      <div id="pg-system-wrap" class="pg-system-wrap collapsed">
        <button id="pg-system-toggle" class="pg-system-toggle" type="button" aria-expanded="false">// system prompt ▾</button>
        <textarea id="pg-system" class="pg-system" rows="2" placeholder="optional system prompt..."></textarea>
      </div>
      <div class="pg-input-row">
        <textarea id="pg-input" class="pg-input" rows="3" placeholder="type a message... (Cmd/Ctrl+Enter to send)"></textarea>
        <button id="pg-send" class="btn-primary" type="button">> send</button>
      </div>
    </footer>
  </div>
</div>
```

- [ ] **Step 4: Run the test, verify it passes**

```bash
cd /home/beni/Projects/go/lab/freegate && go test ./internal/delivery/ui -run TestPlaygroundModalTemplateLoads -v
```

Expected: `--- PASS: TestPlaygroundModalTemplateLoads (0.00s)`.

- [ ] **Step 5: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add web/templates/partials/playground_modal.html internal/delivery/ui/playground_test.go && git commit -m "feat(playground): add modal HTML partial

Adds the slide-over modal skeleton. All element IDs are prefixed
'pg-' to avoid collisions with existing dashboard elements. A new
Go test asserts the partial is loaded by the template loader and
contains all expected IDs."
```

---

## Task 3: Add playground.js with smoke test (TDD)

**Files:**
- Create: `web/static/js/playground.js`
- Modify: `internal/delivery/ui/playground_test.go`

- [ ] **Step 1: Add the failing JS smoke test**

Append to `internal/delivery/ui/playground_test.go`:

```go
// TestPlaygroundJSExists is a smoke test that catches gross omissions in
// the JS module. It does not execute the code — that happens in a real
// browser. It asserts the file exists and contains the function and
// identifier names the rest of the system depends on.
func TestPlaygroundJSExists(t *testing.T) {
	const jsPath = "../../web/static/js/playground.js"
	data, err := os.ReadFile(jsPath)
	if err != nil {
		t.Fatalf("read %s: %v", jsPath, err)
	}
	js := string(data)

	must := []string{
		"freegate.playground.v1",  // localStorage key
		"/v1/chat/completions",     // proxy endpoint
		"/v1/models",               // model list endpoint
		"function open(",
		"function close(",
		"function send(",
		"function load(",
		"function save(",
		"function loadModels(",
		"function streamResponse(",
		"function nonStreamResponse(",
		"window.fgPlayground",
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
```

- [ ] **Step 2: Run the test, verify it fails**

```bash
cd /home/beni/Projects/go/lab/freegate && go test ./internal/delivery/ui -run TestPlaygroundJSExists -v
```

Expected: `--- FAIL: TestPlaygroundJSExists` with `read ../../web/static/js/playground.js: no such file or directory`.

- [ ] **Step 3: Create the playground.js module**

Create `web/static/js/playground.js`:

```js
(function () {
  'use strict';

  // ----- Constants -----
  var STORAGE_KEY = 'freegate.playground.v1';
  var CHAT_URL = '/v1/chat/completions';
  var MODELS_URL = '/v1/models';

  // ----- State -----
  var state = {
    model: '',
    system: '',
    stream: true,
    messages: []
  };
  var inFlight = null;
  var modelsLoaded = false;

  // ----- DOM helpers -----
  function $(id) { return document.getElementById(id); }

  function fmtTime(ts) {
    if (!ts) return '';
    var d = new Date(ts);
    function pad(n) { return n < 10 ? '0' + n : '' + n; }
    return pad(d.getHours()) + ':' + pad(d.getMinutes()) + ':' + pad(d.getSeconds());
  }

  // ----- Persistence -----
  function isValidMessage(m) {
    if (!m || typeof m !== 'object') return false;
    if (m.role !== 'user' && m.role !== 'assistant') return false;
    if (typeof m.content !== 'string') return false;
    return true;
  }

  function load() {
    try {
      var raw = localStorage.getItem(STORAGE_KEY);
      if (!raw) return;
      var parsed = JSON.parse(raw);
      if (!parsed || typeof parsed !== 'object') return;
      if (typeof parsed.model === 'string') state.model = parsed.model;
      if (typeof parsed.system === 'string') state.system = parsed.system;
      state.stream = parsed.stream !== false;
      if (Array.isArray(parsed.messages)) {
        state.messages = parsed.messages.filter(isValidMessage);
      }
    } catch (e) {
      console.warn('[playground] failed to load thread:', e);
    }
  }

  function save() {
    try {
      localStorage.setItem(STORAGE_KEY, JSON.stringify(state));
    } catch (e) {
      if (e && e.name === 'QuotaExceededError') {
        console.warn('[playground] localStorage quota exceeded; thread not persisted');
      } else {
        console.warn('[playground] failed to save thread:', e);
      }
    }
  }

  // ----- Models -----
  function setModelOptions(items) {
    var sel = $('pg-model');
    sel.innerHTML = '';
    var placeholder = document.createElement('option');
    placeholder.value = '';
    if (items.length === 0) {
      placeholder.textContent = '// no models available';
    } else {
      placeholder.textContent = '// pick a model';
    }
    placeholder.disabled = true;
    sel.appendChild(placeholder);
    for (var i = 0; i < items.length; i++) {
      var opt = document.createElement('option');
      opt.value = items[i].id;
      opt.textContent = items[i].id;
      sel.appendChild(opt);
    }
    if (state.model && items.some(function (m) { return m.id === state.model; })) {
      sel.value = state.model;
    } else if (items.length > 0) {
      sel.value = items[0].id;
      state.model = items[0].id;
    } else {
      sel.value = '';
      state.model = '';
    }
    sel.disabled = items.length === 0;
  }

  function loadModels() {
    if (modelsLoaded) return Promise.resolve();
    return fetch(MODELS_URL)
      .then(function (resp) {
        if (!resp.ok) throw new Error('status ' + resp.status);
        return resp.json();
      })
      .then(function (data) {
        var items = [];
        if (data && Array.isArray(data.data)) {
          items = data.data;
        } else if (Array.isArray(data)) {
          items = data;
        }
        setModelOptions(items);
        modelsLoaded = true;
      })
      .catch(function (e) {
        console.warn('[playground] failed to load models:', e);
        setModelOptions([]);
      });
  }

  // ----- Rendering -----
  function buildMessageEl(m) {
    var wrap = document.createElement('div');
    wrap.className = 'msg ' + (m.role === 'user' ? 'msg-user' : 'msg-assistant');

    var meta = document.createElement('div');
    meta.className = 'msg-meta';
    var prefix = m.role === 'user' ? '> user' : '$ assistant';
    var parts = [prefix];
    if (m.ts) parts.push(fmtTime(m.ts));
    if (m.model) parts.push(m.model);
    if (typeof m.duration_ms === 'number') parts.push(m.duration_ms + 'ms');
    if (typeof m.tokens === 'number') parts.push(m.tokens + ' tok');
    meta.textContent = parts.join(' ');

    var body = document.createElement('pre');
    body.className = 'msg-body';
    body.textContent = m.content || '';

    wrap.appendChild(meta);
    wrap.appendChild(body);
    return wrap;
  }

  function renderList() {
    var list = $('pg-list');
    var empty = $('pg-empty');
    list.innerHTML = '';
    if (state.messages.length === 0) {
      empty.style.display = '';
      return;
    }
    empty.style.display = 'none';
    for (var i = 0; i < state.messages.length; i++) {
      list.appendChild(buildMessageEl(state.messages[i]));
    }
    list.scrollTop = list.scrollHeight;
  }

  function appendUserMessage(content) {
    var m = { role: 'user', content: content, ts: Date.now() };
    state.messages.push(m);
    save();
    var list = $('pg-list');
    $('pg-empty').style.display = 'none';
    list.appendChild(buildMessageEl(m));
    list.scrollTop = list.scrollHeight;
  }

  function createAssistantPlaceholder() {
    var m = { role: 'assistant', content: '', ts: Date.now(), model: state.model };
    state.messages.push(m);
    var list = $('pg-list');
    var wrap = document.createElement('div');
    wrap.className = 'msg msg-assistant msg-streaming';
    var meta = document.createElement('div');
    meta.className = 'msg-meta';
    meta.textContent = '$ assistant ' + (m.model || '');
    var body = document.createElement('pre');
    body.className = 'msg-body';
    body.textContent = '';
    wrap.appendChild(meta);
    wrap.appendChild(body);
    list.appendChild(wrap);
    list.scrollTop = list.scrollHeight;
    return { wrap: wrap, body: body, meta: meta, message: m };
  }

  function finalizeAssistant(handle, opts) {
    handle.wrap.classList.remove('msg-streaming');
    if (opts.error) {
      handle.wrap.classList.add('msg-error');
      handle.body.textContent = '! error: ' + opts.error;
    } else if (opts.truncated) {
      handle.body.textContent = (handle.body.textContent || '') + '\n! truncated';
    }
    var parts = ['$ assistant'];
    if (handle.message.model) parts.push(handle.message.model);
    if (typeof opts.duration_ms === 'number') parts.push(opts.duration_ms + 'ms');
    if (typeof opts.tokens === 'number') parts.push(opts.tokens + ' tok');
    handle.meta.textContent = parts.join(' ');
    handle.message.duration_ms = opts.duration_ms;
    handle.message.tokens = opts.tokens;
    if (opts.error) handle.message.error = true;
    save();
  }

  // ----- Request body -----
  function buildRequestBody(userText) {
    var msgs = [];
    if (state.system && state.system.trim()) {
      msgs.push({ role: 'system', content: state.system });
    }
    for (var i = 0; i < state.messages.length; i++) {
      var m = state.messages[i];
      if (m.role === 'user') {
        msgs.push({ role: m.role, content: m.content });
      } else if (m.role === 'assistant' && m.content && !m.error) {
        msgs.push({ role: m.role, content: m.content });
      }
    }
    return {
      model: state.model,
      messages: msgs,
      stream: !!state.stream
    };
  }

  function truncate(s, n) {
    s = String(s || '');
    return s.length > n ? s.slice(0, n) + '…' : s;
  }

  // ----- Send (stream + non-stream) -----
  function send() {
    if (inFlight) return;
    var input = $('pg-input');
    var text = input.value;
    if (!text || !text.trim()) return;
    if (!state.model) {
      console.warn('[playground] no model selected');
      return;
    }
    text = text.trim();
    input.value = '';
    appendUserMessage(text);
    var placeholder = createAssistantPlaceholder();
    var sendBtn = $('pg-send');
    sendBtn.disabled = true;
    inFlight = true;
    var t0 = performance.now();

    var opts = {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(buildRequestBody(text))
    };

    fetch(CHAT_URL, opts)
      .then(function (resp) {
        if (!resp.ok) {
          return resp.text().then(function (txt) {
            finalizeAssistant(placeholder, {
              error: resp.status + ' ' + truncate(txt || resp.statusText, 500),
              duration_ms: Math.round(performance.now() - t0)
            });
          });
        }
        if (state.stream && resp.body) {
          return streamResponse(resp, placeholder, t0);
        }
        return nonStreamResponse(resp, placeholder, t0);
      })
      .catch(function (e) {
        finalizeAssistant(placeholder, {
          error: (e && e.message) ? e.message : String(e),
          duration_ms: Math.round(performance.now() - t0)
        });
      })
      .then(function () {
        sendBtn.disabled = false;
        inFlight = null;
      });
  }

  function streamResponse(resp, placeholder, t0) {
    var reader = resp.body.getReader();
    var decoder = new TextDecoder('utf-8');
    var buffer = '';
    var tokens = 0;

    function pump() {
      return reader.read().then(function (r) {
        if (r.done) {
          // Stream ended without [DONE] / finish_reason
          finalizeAssistant(placeholder, {
            duration_ms: Math.round(performance.now() - t0),
            tokens: tokens,
            truncated: true
          });
          return;
        }
        buffer += decoder.decode(r.value, { stream: true });
        var nl;
        while ((nl = buffer.indexOf('\n')) !== -1) {
          var line = buffer.slice(0, nl).trim();
          buffer = buffer.slice(nl + 1);
          if (!line || !line.startsWith('data:')) continue;
          var payload = line.slice(5).trim();
          if (payload === '[DONE]') {
            finalizeAssistant(placeholder, {
              duration_ms: Math.round(performance.now() - t0),
              tokens: tokens
            });
            return;
          }
          var obj;
          try { obj = JSON.parse(payload); } catch (e) { continue; }
          var choice = obj.choices && obj.choices[0];
          if (!choice) continue;
          var delta = choice.delta || {};
          if (delta.content) {
            placeholder.body.textContent += delta.content;
            tokens++;
            $('pg-list').scrollTop = $('pg-list').scrollHeight;
          }
          if (obj.usage && typeof obj.usage.total_tokens === 'number') {
            tokens = obj.usage.total_tokens;
          }
          var finish = choice.finish_reason;
          if (finish === 'stop' || finish === 'length') {
            finalizeAssistant(placeholder, {
              duration_ms: Math.round(performance.now() - t0),
              tokens: tokens
            });
            return;
          }
        }
        return pump();
      });
    }

    return pump().catch(function (e) {
      finalizeAssistant(placeholder, {
        duration_ms: Math.round(performance.now() - t0),
        tokens: tokens,
        truncated: true
      });
    });
  }

  function nonStreamResponse(resp, placeholder, t0) {
    return resp.json().then(function (data) {
      var choice = data.choices && data.choices[0];
      var msg = choice && choice.message;
      var content = (msg && msg.content) || '';
      placeholder.body.textContent = content;
      var tokens = (data.usage && typeof data.usage.total_tokens === 'number')
        ? data.usage.total_tokens
        : null;
      finalizeAssistant(placeholder, {
        duration_ms: Math.round(performance.now() - t0),
        tokens: tokens
      });
    });
  }

  // ----- UI binding -----
  function open() {
    load();
    $('pg-overlay').style.display = 'flex';
    document.body.classList.add('modal-open');
    $('pg-system').value = state.system;
    $('pg-stream').checked = state.stream;
    renderList();
    loadModels().then(function () {
      $('pg-model').value = state.model || '';
      $('pg-input').focus();
    });
  }

  function close() {
    $('pg-overlay').style.display = 'none';
    document.body.classList.remove('modal-open');
  }

  function clear() {
    state.messages = [];
    save();
    renderList();
  }

  function bind() {
    var openBtn = document.getElementById('open-playground');
    if (openBtn) openBtn.addEventListener('click', open);

    $('pg-close').addEventListener('click', close);
    $('pg-overlay').addEventListener('click', function (e) {
      if (e.target === $('pg-overlay')) close();
    });
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && $('pg-overlay').style.display === 'flex') close();
    });

    $('pg-clear').addEventListener('click', function () {
      if (window.confirm('Clear playground thread?')) clear();
    });

    $('pg-model').addEventListener('change', function (e) {
      state.model = e.target.value;
      save();
    });
    $('pg-system').addEventListener('input', function (e) {
      state.system = e.target.value;
      save();
    });
    $('pg-stream').addEventListener('change', function (e) {
      state.stream = e.target.checked;
      save();
    });

    $('pg-send').addEventListener('click', send);
    $('pg-input').addEventListener('keydown', function (e) {
      if ((e.metaKey || e.ctrlKey) && e.key === 'Enter') {
        e.preventDefault();
        send();
      }
    });

    $('pg-system-toggle').addEventListener('click', function () {
      var wrap = $('pg-system-wrap');
      wrap.classList.toggle('collapsed');
      $('pg-system-toggle').setAttribute(
        'aria-expanded',
        wrap.classList.contains('collapsed') ? 'false' : 'true'
      );
    });
  }

  // ----- Bootstrap -----
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', bind);
  } else {
    bind();
  }

  // ----- Public surface -----
  window.fgPlayground = {
    open: open,
    close: close,
    clear: clear,
    isOpen: function () { return $('pg-overlay').style.display === 'flex'; }
  };
})();
```

- [ ] **Step 4: Run the test, verify it passes**

```bash
cd /home/beni/Projects/go/lab/freegate && go test ./internal/delivery/ui -run TestPlaygroundJSExists -v
```

Expected: `--- PASS: TestPlaygroundJSExists (0.00s)`.

- [ ] **Step 5: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add web/static/js/playground.js internal/delivery/ui/playground_test.go && git commit -m "feat(playground): add JS module for modal behavior

Adds playground.js: open/close, model loading from /v1/models,
localStorage persistence (key 'freegate.playground.v1'), message
rendering, send (streaming + non-streaming) via direct fetch to
/v1/chat/completions, error and truncation handling. A Go smoke
test catches gross omissions and forbidden patterns (eval, document.write)."
```

---

## Task 4: Wire up the dashboard with include-directive guardrail test (TDD)

**Files:**
- Modify: `web/templates/dashboard.html`
- Modify: `internal/delivery/ui/playground_test.go`

- [ ] **Step 1: Add the failing dashboard wiring test**

Append to `internal/delivery/ui/playground_test.go`:

```go
// TestDashboardWiresPlayground asserts that the dashboard template
// includes the playground modal partial, the playground.js script,
// and the open-playground button. This is a string-search guardrail
// that catches wiring regressions without running a browser.
func TestDashboardWiresPlayground(t *testing.T) {
	const tplPath = "../../web/templates/dashboard.html"
	data, err := os.ReadFile(tplPath)
	if err != nil {
		t.Fatalf("read %s: %v", tplPath, err)
	}
	body := string(data)

	must := []string{
		`id="open-playground"`,                              // open button
		`partials/playground_modal.html`,                    // modal include
		`<script src="/static/js/playground.js" defer></script>`, // js include
	}
	for _, want := range must {
		if !strings.Contains(body, want) {
			t.Errorf("dashboard.html missing %q", want)
		}
	}
}
```

- [ ] **Step 2: Run the test, verify it fails**

```bash
cd /home/beni/Projects/go/lab/freegate && go test ./internal/delivery/ui -run TestDashboardWiresPlayground -v
```

Expected: `--- FAIL: TestDashboardWiresPlayground` with `dashboard.html missing "id=\"open-playground\""`.

- [ ] **Step 3: Edit `web/templates/dashboard.html`**

Three edits, in order:

**Edit A** — replace the `<nav>` opening lines to add a `.nav-left` wrapper around brand + new button. Find:

```html
    <!-- Navigation -->
    <nav class="nav">
      <div class="nav-brand">
        <span class="brand-dot" id="status-dot"></span>
        freegate
      </div>
      <div class="nav-meta">
```

Replace with:

```html
    <!-- Navigation -->
    <nav class="nav">
      <div class="nav-left">
        <div class="nav-brand">
          <span class="brand-dot" id="status-dot"></span>
          freegate
        </div>
        <button id="open-playground" class="btn-tertiary pg-nav-btn" type="button">> playground</button>
      </div>
      <div class="nav-meta">
```

**Edit B** — add the `<script>` tag for playground.js. Find:

```html
  <script src="/static/js/htmx.min.js" defer></script>
  <script src="/static/js/chart.umd.js" defer></script>
```

Replace with:

```html
  <script src="/static/js/htmx.min.js" defer></script>
  <script src="/static/js/chart.umd.js" defer></script>
  <script src="/static/js/playground.js" defer></script>
```

**Edit C** — include the modal partial just before `</body>`. Find:

```html
</body>
</html>
```

Replace with:

```html
  {{template "partials/playground_modal.html" .}}

</body>
</html>
```

- [ ] **Step 4: Run the test, verify it passes**

```bash
cd /home/beni/Projects/go/lab/freegate && go test ./internal/delivery/ui -run TestDashboardWiresPlayground -v
```

Expected: `--- PASS: TestDashboardWiresPlayground (0.00s)`.

- [ ] **Step 5: Run the full test suite**

```bash
cd /home/beni/Projects/go/lab/freegate && make check
```

Expected: `fmt` clean, `go vet` clean, all `go test ./...` pass — including the new playground tests and the existing `TestDashboardRenders` (which asserts the dashboard body contains certain substrings; verify it still passes since we changed the nav structure but kept all the visible content).

- [ ] **Step 6: Commit**

```bash
cd /home/beni/Projects/go/lab/freegate && git add web/templates/dashboard.html internal/delivery/ui/playground_test.go && git commit -m "feat(playground): wire up dashboard

Wraps nav-brand and a new '> playground' button in a .nav-left
container, includes playground.js in the head, and includes the
modal partial just before </body>. A Go guardrail test asserts the
wiring is present."
```

---

## Task 5: End-to-end manual browser test

**Files:** none (validation only)

- [ ] **Step 1: Build and run the server**

```bash
cd /home/beni/Projects/go/lab/freegate && make run
```

Expected: server starts on `http://localhost:1234`, models load from upstreams.

- [ ] **Step 2: Open the dashboard in a browser**

Visit `http://localhost:1234/` and verify:
- The nav shows `freegate` on the left, then a `> playground` button next to it.
- The rest of the dashboard (stats, charts, models, requests) renders unchanged.

- [ ] **Step 3: Open / close / persistence**

- Click `> playground` → slide-over appears from the right.
- Click the backdrop → modal closes.
- Click `> playground` again → modal reopens; the empty state `// pick a model and say something` is shown.
- Press `Escape` → modal closes.
- Open it again, type a test message in the system prompt and a model from the dropdown, then **close without sending** and reopen → the system prompt and model selection are still there.
- **Refresh the page**, open playground → state is restored from `localStorage`.

- [ ] **Step 4: Streaming send (default)**

- Open the playground, ensure `stream` is checked.
- Type `hello` in the message box, click `> send` (or press `Cmd/Ctrl+Enter`).
- A `> user` bubble appears immediately.
- A `$ assistant` bubble appears with a streaming response (tokens arriving one by one).
- The meta line under the assistant bubble shows the model name and the duration in ms.
- Open the "Recent Requests" panel on the dashboard → the playground call appears there with a 200 status and the chosen model.

- [ ] **Step 5: Non-streaming send**

- Uncheck `stream`.
- Send another message.
- A `> user` bubble appears immediately.
- The `$ assistant` bubble appears in one shot (no incremental rendering).

- [ ] **Step 6: System prompt**

- Click `// system prompt ▾` to expand the system prompt area.
- Type `you are a terse assistant`.
- Send a message. Inspect the request via browser dev tools → the JSON body sent to `/v1/chat/completions` should have a `messages` array whose first element is `{role:"system", content:"you are a terse assistant"}`.

- [ ] **Step 7: Multi-turn**

- Send a follow-up message (e.g., `and 2*2?`) without clearing the thread.
- The user bubble appears, the assistant response is sent with the **prior** user and assistant messages as context (check the network panel).

- [ ] **Step 8: Clear thread**

- Click `× clear`. Confirm the dialog. The list empties. The model/system/stream settings are preserved.

- [ ] **Step 9: Error handling**

- Stop Tor (`docker compose stop tor` or `pkill tor`).
- Send a message. The user bubble appears, the assistant bubble shows `! error: 502 ...` (or similar upstream-error text) in red. The user message is **kept** in the list.

- [ ] **Step 10: Mobile responsive**

- Resize the browser window to `<768px` wide. Open the playground. The panel should be full-width. The model selector should still fit. Inputs and buttons should be at least 36px tall.

- [ ] **Step 11: TerminalUI fidelity**

- Click around every interactive element in the modal.
- Verify: no border-radius anywhere; no box-shadows; all text is monospace; user prefix `>` is cyan, assistant prefix `$` is green, error prefix `!` is red; hover/active states invert colors instantly (no easing); focus outline is the phosphor green.

- [ ] **Step 12: Stop the server and commit any final touches**

If everything passes, no code changes are needed. If a bug surfaced, fix it, run `make check`, and commit with `fix(playground): ...`.

---

## Self-Review

- **Spec coverage:** Every section of the spec maps to a task — Task 1 (CSS → design.md compliance), Task 2 (modal HTML → layout & IDs from spec), Task 3 (JS → open/close, persistence, send stream/non-stream, error handling, `localStorage` shape, system prompt, model selector, clear thread), Task 4 (wiring), Task 5 (validation). The "no Go changes" non-goal is preserved by construction.
- **Placeholder scan:** No TBDs. Every code block is complete. Every command is exact.
- **Type consistency:** All element IDs use the `pg-` prefix; all CSS classes match the spec; the `localStorage` shape (`{model, system, stream, messages[]}`) is identical between the `load`/`save` functions in Task 3 and the persistence section in the spec.
- **Frequent commits:** Each task ends with a commit. 4 implementation commits + the existing spec commit.

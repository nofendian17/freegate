# Chat Playground Design

**Date:** 2026-06-06
**Status:** Draft (pending user review)

## Purpose

Add an in-browser chat playground to the freegate dashboard so operators can
test any free model the proxy serves without leaving the UI or hand-crafting
`curl` commands. The playground is a quick "how does model X handle prompt Y"
tool — not a long-running chat client.

## Goals

- Pick a model from the live catalog, send a message, see the response in chat
  form.
- Stream responses by default (token-by-token feel) with a toggle to disable.
- Persist the active thread in `localStorage` so a reload resumes it.
- Match the **TerminalUI** design system (see `design.md`): dark canvas,
  phosphor green, JetBrains Mono, zero radius, sharp corners, CLI prefixes
  (`>`, `$`, `#`), instant transitions, no shadows.
- Make the playground activity visible in the existing request log / metrics
  so operators can see what they tested.

## Non-Goals (YAGNI)

- Multiple named threads / conversation list — one thread only.
- Server-side persistence — `localStorage` is the storage layer.
- Markdown rendering / syntax highlighting — assistant content is rendered as
  preformatted monospace text. Newlines preserved. No HTML injection.
- Message editing / branching / regeneration — append-only.
- Multi-modal inputs (images, files) — text only.
- Auth — playground is open like the dashboard.
- Touching the Go proxy code, the request recorder, or the chat handler.

## User Experience

### Opening

- A `> playground` button appears in the top nav of the dashboard, to the
  right of the `freegate` brand.
- Clicking it slides a panel in from the right (covers the right ~480px on
  desktop, full width on `<768px`).
- Clicking the backdrop or pressing `Escape` closes it.
- Closing does **not** clear the thread — the next open restores it.

### Layout

```
┌──────────────────────────────────────────────┐
│ > playground        [model ▼]  [☐ stream]  × │  <- header
├──────────────────────────────────────────────┤
│                                              │
│  > user                       12:04:31      │
│  what is 2+2?                                │
│  ─────────────────────────────────────       │
│  $ assistant   openrouter/owl-alpha   420ms  │
│  2 + 2 = 4.                                  │
│  ─────────────────────────────────────       │
│  > user                       12:04:48      │
│  and 2*2?                                    │
│  ─────────────────────────────────────       │
│  $ assistant   openrouter/owl-alpha   ...    │
│  2 * 2 = 4._                                │  <- blinking cursor (streaming)
│                                              │
│  (empty state when no messages:)             │
│  // pick a model and say something           │
├──────────────────────────────────────────────┤
│  // system prompt  ▾                          │  <- collapsible
│  ┌────────────────────────────────────────┐  │
│  │ you are a terse assistant              │  │
│  └────────────────────────────────────────┘  │
│  ┌────────────────────────────────────┐ [send]│
│  │ type a message...                   │       │
│  └────────────────────────────────────┘       │
└──────────────────────────────────────────────┘
```

### Controls

- **Model selector** — populated from `GET /v1/models`. Switching the model
  does **not** clear the thread; it just changes what the next message gets
  sent to. The model that produced each assistant reply is shown in the
  message meta line, so past replies are honest about their origin.
- **Stream toggle** — checked by default. When off, the request is sent with
  `stream: false` and the response renders in one shot.
- **System prompt** — collapsible textarea. Persisted in `localStorage` as
  part of the thread metadata; prepended to the messages array on every send.
- **Send button** — disabled while a request is in flight. Keyboard: `Cmd/Ctrl
  + Enter` also sends.
- **Clear thread** — small ghost button in the header (`× clear`) that wipes
  the messages (keeps model/system/stream settings).

### Streaming behavior

- On send: the user's message is appended immediately. A placeholder
  assistant bubble is appended with an empty body and a blinking `_` cursor.
- The browser `fetch`es `/v1/chat/completions` with `stream: true`.
- The response `ReadableStream` is read with a `TextDecoder` (utf-8,
  `stream: true`).
- Lines starting with `data: ` are parsed; the JSON payload's
  `choices[0].delta.content` is appended to the in-flight bubble. The cursor
  stays at the end while the stream is open.
- `data: [DONE]` closes the stream: cursor removed, meta line finalized with
  the last `usage` we have (or `—` if missing).
- Network / non-2xx errors: the in-flight bubble is replaced with a red `!
  error:` line showing the status + body (truncated to 500 chars), and the
  user message is **kept** so the user can retry.
- Mid-stream errors (SSE error events, or stream cut off): the bubble gets a
  `! truncated` suffix and the cursor is removed.

### Non-streaming behavior

- The user's message is appended immediately. A placeholder assistant bubble
  is appended.
- The browser `fetch`es `/v1/chat/completions` with `stream: false`.
- The full response is read, parsed, the assistant `message.content` is
  rendered into the bubble, and meta is finalized.
- Same error handling as streaming.

### Persistence

- **Key:** `freegate.playground.v1`
- **Shape:**
  ```json
  {
    "model": "openrouter/owl-alpha",
    "system": "you are a terse assistant",
    "stream": true,
    "messages": [
      { "role": "user",      "content": "what is 2+2?", "ts": 1749138271000 },
      { "role": "assistant", "content": "2 + 2 = 4.",   "ts": 1749138271420,
        "model": "openrouter/owl-alpha", "duration_ms": 420, "tokens": 17 }
    ]
  }
  ```
- Saved on every change (model switch, system prompt edit, message append).
- Loaded once when the modal opens for the first time per page load.
- A versioned key (`v1`) lets us evolve the schema later without breaking
  older threads.
- If the JSON is corrupt (manual edit, partial write), the playground falls
  back to an empty thread and logs a console warning. It does not crash the
  dashboard.

## Architecture & Data Flow

```
┌──────────────────────────────────────────────────────────────┐
│ Browser (dashboard at /)                                     │
│ ┌──────────────────┐    ┌────────────────────────────────┐  │
│ │ dashboard.html   │    │  playground modal (slide-over) │  │
│ │                  │    │                                │  │
│ │  ▶ open modal    │ ─► │  message list                  │  │
│ │                  │    │  system prompt (collapsible)   │  │
│ │                  │    │  input + send                  │  │
│ │                  │    │                                │  │
│ │                  │    │  localStorage: 1 thread        │  │
│ │                  │    │       messages[] + meta        │  │
│ └──────────────────┘    │       │ model, system, stream  │  │
│                         │       ▼                        │  │
│                         │  fetch('/v1/chat/completions', │  │
│                         │        { stream: true|false }) │  │
│                         └────┬───────────────────────────┘  │
└──────────────────────────────┼───────────────────────────────┘
                               │ POST  (same origin, no CORS)
                               ▼
┌──────────────────────────────────────────────────────────────┐
│ freegate server (Go) — UNCHANGED                             │
│                                                              │
│  POST /v1/chat/completions                                  │
│       │                                                      │
│       ├─► format detect (OpenAI/Claude/Gemini)              │
│       ├─► translate → OpenAI intermediate                   │
│       ├─► route to upstream (kilo/opencode)                 │
│       ├─► proxy via Tor SOCKS5                              │
│       │     stream  ──► SSE chunks                          │
│       │     !stream ──► JSON                                │
│       └─► recorder.log(req) ──► request log, metrics        │
└──────────────────────────────────────────────────────────────┘
```

The playground calls the **existing** chat handler end-to-end. Request log,
metrics, Tor routing, rate limits, retries, and reasoning normalization all
work without further wiring.

## Components & Files

### New

| File | Purpose |
|---|---|
| `web/templates/partials/playground_modal.html` | Modal skeleton — header, body, footer, empty state |
| `web/static/js/playground.js` | All playground behavior (vanilla JS, ~200 lines) |

### Modified

| File | Change |
|---|---|
| `web/templates/dashboard.html` | Add `> playground` nav button; include modal partial; include `playground.js`; add a `DOMContentLoaded` bootstrap script |
| `web/static/css/app.css` | Append playground styles (overlay, panel, header/body/footer, message bubbles, mobile media query, blinking cursor) |

### Unchanged

- All Go code (`cmd/`, `internal/`). The `loader.go` template loader already
  recurses into `partials/`, so the new template is auto-registered.
- The `embed.go` `//go:embed` directive already covers the full `static/`
  and `templates/` trees.

## Component responsibilities

### `playground_modal.html`

A single, self-contained block. The dashboard includes it once via
`{{template "partials/playground_modal.html" .}}` immediately before
`</body>`. Element IDs are namespaced (`#pg-...`) to avoid collisions with
existing dashboard elements.

Public IDs:
- `#pg-overlay` — full-screen click-to-close backdrop
- `#pg-panel` — slide-over panel
- `#pg-close` — close button (×)
- `#pg-clear` — clear-thread ghost button
- `#pg-model` — model `<select>`
- `#pg-stream` — stream `<input type="checkbox">`
- `#pg-system-wrap` — collapsible system prompt block
- `#pg-system` — system prompt `<textarea>`
- `#pg-list` — scrollable message list
- `#pg-empty` — empty-state row (hidden once a message exists)
- `#pg-input` — message `<textarea>`
- `#pg-send` — send button

### `playground.js`

Exposes a small module-style surface on `window.fgPlayground`:

```js
window.fgPlayground = {
  open(),           // open the modal, lazy-load thread
  close(),          // close, persist thread
  isOpen(),         // boolean
  clear(),          // wipe messages, keep settings
  // internal: send(), stream(), save(), load(), render(), escape()
};
```

Top-level flow:
1. On `DOMContentLoaded`: bind open/close/clear/ESC handlers, populate the
   model selector from `/v1/models`, register `Cmd/Ctrl+Enter` on the input.
2. On `open()`: lazy-load thread from `localStorage`; show overlay.
3. On send: append user bubble, persist, build request body, call
   `stream()` or `nonStream()` based on the toggle, persist on completion.

### `app.css` additions

Append at the end of the existing file (no override of existing rules).
Class names prefixed `pg-` for the modal layout and `msg-` for bubbles.
Colors drawn from existing CSS variables (`--primary`, `--tertiary`,
`--error`, `--text-dim`, `--border`, `--bg`, `--surface`) so the playground
stays in lockstep with the design tokens.

Key rules:
- `.pg-overlay` — `position: fixed; inset: 0; background: rgba(0,0,0,0.8);
  display: none;`; toggled to `flex` when open.
- `.pg-panel` — `position: fixed; top: 0; right: 0; height: 100vh; width:
  480px; max-width: 100vw; background: var(--bg); border-left: 1px solid
  var(--primary); display: flex; flex-direction: column;`.
- `.pg-header`, `.pg-body { flex: 1 1 auto; overflow-y: auto; }`,
  `.pg-footer`.
- `.msg` — block with `border: 1px solid var(--border); padding: 12px;`
  with a role-prefix character in a colored span.
- `.msg-user` — `border-color: var(--tertiary);` prefix `>` in cyan.
- `.msg-assistant` — `border-color: var(--primary);` prefix `$` in green.
- `.msg-error` — `border-color: var(--error);` prefix `!` in red.
- `.msg-meta` — 11px, `color: var(--text-faint);` line above each bubble.
- `.pg-cursor` — `display: inline-block; width: 0.6ch; background:
  var(--primary); animation: pg-blink 1s steps(1) infinite;` (or a static
  `_` for `prefers-reduced-motion`).
- Mobile: at `max-width: 768px`, the panel becomes `width: 100vw;`.

## Error handling

| Failure | Behavior |
|---|---|
| `/v1/models` fails on open | Selector shows `// no models available` option (disabled). Playground still usable, but send will 400 from the proxy. |
| Bad / corrupt `localStorage` thread | Catch `JSON.parse` error, log to console, start with empty thread. Do not break the dashboard. |
| Non-2xx on `POST /v1/chat/completions` | In-flight bubble becomes `! error: <status> <body truncated to 500 chars>`. User message kept. |
| Mid-stream SSE error or cut-off | In-flight bubble gets `! truncated` suffix; cursor removed. |
| User sends while a request is in flight | Send button disabled. `Cmd/Ctrl+Enter` ignored. No queueing. |
| `localStorage` quota exceeded (unlikely: <10KB) | Catch `QuotaExceededError`, log warning, keep in-memory thread for the session, drop persistence. |

## Testing

### Unit / integration (Go)

- Existing test suite (`make test`) must continue to pass — no Go changes
  mean no new Go tests, but this is a regression guardrail.
- Add a Go test in `internal/delivery/ui/` asserting that the new
  `partials/playground_modal.html` is loaded by the template loader.

### Manual test plan (browser)

Each item is a discrete pass/fail check.

1. **Open / close**
   - `> playground` button in nav opens the slide-over.
   - Clicking the backdrop closes it.
   - Pressing `Escape` closes it.
   - Reopening restores the prior thread (if any).

2. **Model selector**
   - Populated with the same list as `/v1/models`.
   - Switching the model updates the next request.
   - Selected model persists across reloads.

3. **Send (stream on, default)**
   - User bubble appears immediately.
   - Assistant bubble appears with a blinking cursor.
   - Tokens stream in; cursor stays at the end.
   - `data: [DONE]` removes the cursor and finalizes the meta line.

4. **Send (stream off)**
   - User bubble appears immediately.
   - Assistant bubble appears with no cursor.
   - Full response renders in one shot.

5. **System prompt**
   - Collapsible block toggles.
   - Setting it persists.
   - Sent as the first message in the array on every request.

6. **Persistence**
   - After a send + reload, all messages and the model are restored.
   - Corrupting the localStorage value doesn't break the dashboard.
   - `× clear` wipes messages but keeps model/system/stream.

7. **Errors**
   - 4xx/5xx from the proxy: in-flight bubble shows `! error: ...`.
   - User message is kept.
   - Disconnecting Tor mid-stream: bubble shows `! truncated`.

8. **Request log integration**
   - Playground sends appear in the "Recent Requests" panel with the chosen
     model and 200 status.

9. **TerminalUI fidelity**
   - No border-radius anywhere in the modal.
   - No box-shadows.
   - All text is JetBrains Mono.
   - User prefix is `>` in cyan, assistant prefix is `$` in green.
   - Hover/active states are inverted (no easing).

10. **Mobile**
    - Panel becomes full-width at `<768px`.
    - Touch targets are at least 36px tall.

## Open questions

None at draft time.

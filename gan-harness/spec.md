# Product Specification: Streaming Playground

> Generated from brief: "Enhance the freegate dashboard playground to support both streaming and non-streaming chat completion modes."

## Vision

The playground already works as a terminal-inspired chat UI for testing free LLM models through the freegate proxy. This feature adds a real-time streaming experience: when streaming is enabled, assistant responses appear token-by-token as the model generates them, with a blinking cursor indicating active generation. The non-streaming path remains exactly as-is for users who prefer the instant-completion feel.

The result is a playground that gives the user direct, visible feedback that the model is working, matches the terminal-as-a-tool aesthetic, and makes long responses feel faster by showing progress.

## Design Direction

- **Color palette**: `--primary: #00FF41; --bg: #0D0D0D; --surface: #141414; --border: #222222; --text: #CDCDCD; --text-dim: #8B8B8B; --text-faint: #555555; --error: #FF0040` — existing terminal theme, no changes.
- **Typography**: JetBrains Mono (monospace only) — existing, no changes.
- **Layout philosophy**: Side-panel modal (slides in from right), 480px wide, full-height. No change to layout.
- **Visual identity**: The blinking cursor (`_`) on the streaming assistant message is the key visual differentiator between streaming and non-streaming. The `msg-streaming` class already exists with a `pg-blink` animation — it just needs to be activated.
- **Anti-AI-slop directives**: No gradients, no rounded corners (all zero), no box-shadows, no sans-serif fonts. No loading spinners — the blinking cursor is the loading indicator. No "assistant is typing..." or animated dots — just the terminal cursor.

## Features (prioritized)

### Must-Have (Sprint 1)
1. **Enable stream toggle**: Remove `disabled` from the `<input id="pg-stream">` checkbox. Wire it to `state.stream` to persist the preference across sessions. Add a change handler so toggling the checkbox updates state immediately.
   - Acceptance: Checkbox is clickable. Toggling it persists the value. Reloading the playground restores the saved preference.

2. **Non-streaming path preserved**: When `state.stream === false`, the `send()` function must behave identically to the current code: `fetch()` -> `resp.text()` -> JSON parse -> display full response. No regressions.
   - Acceptance: With stream toggle OFF, existing non-streaming requests continue to work identically.

3. **Streaming path**: When `state.stream === true`:
   - The request body sets `stream: true`.
   - The `fetch()` response is consumed via `response.body.getReader()` (ReadableStream API).
   - Each SSE `data:` line is parsed incrementally.
   - The assistant bubble's content is updated in real-time as chunks arrive.
   - The `msg-streaming` class is present during streaming and removed when done.
   - The `[DONE]` signal terminates the stream normally.
   - Acceptance: Text appears character-by-character (or chunk-by-chunk) as the model generates.

4. **Stream error handling**: If the stream throws (network drop, timeout, HTTP error), the error is surfaced in the assistant bubble via the existing `finalizeAssistant()` error path. Partial content, if any, is preserved for debugging.
   - Acceptance: A streaming request interrupted mid-stream shows the partial content received so far plus an error indicator.

5. **Token usage from stream**: Parse `stream_options: {include_usage: true}` chunks that arrive after `[DONE]` in SSE. Extract `total_tokens` and display in the bubble metadata.
   - Acceptance: When the upstream includes usage data in the stream tail, the assistant message metadata shows token count.

### Should-Have (Sprint 2)
6. **HTTP error handling for streaming**: If the upstream returns HTTP 4xx/5xx before any SSE data, the response body is a non-streaming JSON error. The streaming path must detect non-SSE responses (Content-Type not `text/event-stream`, or status code >= 400) and fall back to the non-streaming error handler.
   - Acceptance: A 400 Bad Request with a JSON error body is displayed as an error bubble, not a stream parse failure.

7. **Abort controller**: Wire an `AbortController` to the streaming fetch so that the in-flight request can be cancelled. Add a "stop" button that replaces the send button during streaming.
   - Acceptance: While streaming, the send button becomes a stop button. Clicking it aborts the fetch and finalizes the assistant bubble with the partial content.

8. **Mobile streaming UI**: The `msg-streaming` cursor and real-time updates must work on mobile (iOS Safari, Chrome Android). The stop button must be a 44x44px touch target.
   - Acceptance: Streaming works on mobile viewports. The blinking cursor is visible. Stop button is tappable.

### Nice-to-Have (Sprint 3+)
9. **Stream timing**: Show elapsed time in the meta line during streaming (e.g., `$ assistant model-name 12.3s`), updating as the stream progresses.
10. **Copy partial content**: Allow copying content from a streaming bubble before it finishes.
11. **Auto-scroll behavior**: Auto-scroll the message list to the bottom as new tokens arrive, but only if the user hasn't scrolled up to read earlier content.
12. **Keyboard shortcut for stop**: Press Escape to stop an in-flight streaming response.

## Technical Approach

### SSE Parsing (vanilla JS, no dependencies)

The OpenAI SSE format is:

```
data: {"choices":[{"delta":{"content":"Hello"},"index":0}]}

data: {"choices":[{"delta":{"content":" world"},"index":0}]}

data: [DONE]
```

When `stream_options: {include_usage: true}` is set in the request, a final chunk may appear after `[DONE]`:

```
data: {"choices":[],"usage":{"prompt_tokens":10,"completion_tokens":20,"total_tokens":30}}
```

Parser function (`parseSSEChunk`):
- Maintain an internal `buffer` string across calls.
- Split on `\n\n` (SSE event boundary).
- For each complete event, extract the line after `data:`.
- If the value is `[DONE]`, signal stream end.
- Otherwise, try to JSON.parse and extract `choices[0].delta.content` and optional `usage`.
- Return unconsumed buffer for next call.

### ReadableStream Reader

```js
async function readStream(reader, decoder, onChunk, onDone, onError) {
  let buffer = '';
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      buffer = parseSSEChunks(buffer + decoder.decode(value, { stream: true }),
                               onChunk, onDone);
    }
    // Final decode for any remaining bytes
    parseSSEChunks(buffer + decoder.decode(), onChunk, onDone);
  } catch (err) {
    onError(err);
  }
}
```

### State Machine

- `idle` — no request in flight
- `streaming` — reader is active, assistant bubble is being filled
- `processing` — non-streaming fetch response is being parsed

### Error Recovery

- **Network drop**: `reader.read()` rejects -> catch -> call `onError` -> `finalizeAssistant({error: 'connection lost'})`
- **Aborted**: AbortError from AbortController -> `finalizeAssistant({error: 'stopped'})` with partial content
- **HTTP error before stream**: Detect `!resp.ok` before calling `getReader()` -> read body as text -> parse JSON error -> `finalizeAssistant({error})`
- **Malformed SSE**: If JSON.parse fails on a `data:` line, skip the chunk and continue rather than crashing

## File-by-File Changes

### `web/static/js/playground.js`

1. **Remove `disabled` from stream toggle** — no JS change needed, this is an HTML attribute removal.

2. **Add stream toggle change handler**: Wire `'change'` event on `$('pg-stream')` that updates `state.stream = checked` and calls `save()`.

3. **Modify `requestBody()`**: Use `state.stream` instead of hardcoded `false`:
   ```js
   function requestBody() {
     // ... same message building ...
     return {
       model: state.model,
       messages: msgs,
       stream: state.stream,
       stream_options: state.stream ? { include_usage: true } : undefined
     };
   }
   ```

4. **Add SSE parser functions** (new internal functions):
   - `parseSSEChunks(buffer, onChunk, onDone)` — stateful SSE parser
   - `handleStreamingSend()` — orchestrates the streaming fetch
   - Integrated into `send()` as a branch: `if (state.stream) { handleStreamingSend(evt); return; }`

5. **Modify `send()`**: Branch on `state.stream`:
   - `false`: existing non-streaming code path (unchanged)
   - `true`: new streaming path via `handleStreamingSend()`

6. **Add abort controller infrastructure**:
   - `state.abortController = null` — reset before each streaming request
   - In stop/abort handler: call `abortController.abort()` and `finalizeAssistant()`

7. **Keep `handleFetchResponse()` unchanged** — still used by the non-streaming path.

### `web/templates/partials/playground_modal.html`

1. **Remove `disabled` from stream toggle**:
   ```html
   <input id="pg-stream" type="checkbox"> stream
   ```

2. **Wire change handler on stream toggle**:
   ```html
   <input id="pg-stream" type="checkbox"
          hx-on:change="window.fgPlayground.onStreamToggle(event)"> stream
   ```

### `web/static/css/app.css`

1. **Add stop button styling** (new class `.pg-stop` or reuse `.btn-danger`):
   - Same dimensions as `.btn-primary`
   - Color: `var(--error)` or `var(--secondary)` for visual distinction
   - Display: shown only when `inFlight` and `state.stream` are true

2. **Minor mobile refinement** — ensure `.pg-stop` has 44px touch target on mobile.

No CSS changes needed for the streaming cursor — the existing `msg-streaming` CSS already handles it.

## Edge Cases

1. **Rapid toggle**: User toggles stream mode while a request is in-flight. The in-flight request should complete in its original mode. The toggle should be disabled while `inFlight === true`.

2. **Empty stream**: Server returns SSE with no delta content (e.g., `data: {"choices":[{"delta":{},"index":0}]}` then `[DONE]`). Should display an empty assistant bubble with no error.

3. **Stream with leading whitespace**: Delta content that is only whitespace should still be appended (some models emit leading newlines).

4. **Very long SSE lines**: The `TextDecoder` handles arbitrary-length lines. The buffer may grow large during a long stream — no explicit cap needed since `MAX_THREAD_BYTES` caps persistence.

5. **Multiple SSE events in one chunk**: A single `reader.read()` may return several SSE events concatenated. The parser must handle this correctly by splitting on `\n\n` boundaries.

6. **Split SSE event across chunks**: An SSE boundary may fall at the exact end of a chunk. The buffer carries over so the next chunk completes the parse.

7. **Non-streaming error during streaming request**: If `fetch()` returns a non-2xx status (e.g., 400 bad request, 429 rate limit), the body is likely JSON. Read the body via `resp.text()`, parse, and display via the error path.

8. **Browser without ReadableStream support**: Unlikely (all modern browsers since 2019), but the stream toggle should be hidden/disabled if `typeof ReadableStream === 'undefined'`. The noop stub in the current code can remain as a fallback.

9. **localStorage quota**: The thread may grow large during streaming. `save()` is not called per-chunk — only when `finalizeAssistant()` is called, to avoid thrashing.

10. **Concurrent sends**: `inFlight` guard prevents starting a new request while one is in progress. The streaming path uses the same guard.

## Evaluation Criteria

### Design Quality (weight: 0.3)
- Streaming cursor is visible, blinks at 1s interval, does not flicker on content update
- No visual difference between streaming and non-streaming bubbles after completion
- Stop button is visually distinct from send button
- Error bubbles during streaming look identical to error bubbles from non-streaming
- Mobile: cursor is visible, text reflows naturally, no horizontal scroll during streaming

### Originality (weight: 0.2)
- Clean, minimal SSE parser — not a copy-pasted library
- Thoughtful use of existing infrastructure (reuses `createAssistantPlaceholder`, `finalizeAssistant`, `msg-streaming` CSS)
- AbortController integration is clean (no leaked references)
- Token usage extraction from stream tail is correctly handled

### Craft (weight: 0.3)
- No new dependencies or build steps
- `handleFetchResponse()` is unmodified for the non-streaming path
- SSE parser handles split events, empty events, malformed JSON gracefully
- `save()` is not called per-chunk (performance)
- In-flight guard prevents concurrent sends in both modes
- Abort controller is properly cleaned up after stream ends
- Partial content is preserved on error/abort
- Error messages are user-facing, not technical stack traces

### Functionality (weight: 0.2)
- Stream toggle checkbox is clickable and persists
- Non-streaming requests work identically to before (regression test)
- Streaming requests show incremental content
- `[DONE]` signal terminates the stream
- Token usage from `include_usage` is displayed
- HTTP errors (4xx, 5xx) during streaming request show error bubble
- Abort/stop works mid-stream
- Empty stream produces empty assistant bubble
- Streaming + non-streaming toggle works across multiple requests
- All existing tests pass (no breaking changes)

## Sprint Plan

### Sprint 1: Core Streaming
- Goals: Enable the toggle, implement streaming reader, preserve non-streaming path
- Features: #1, #2, #3, #4, #5
- Definition of done: Streaming text appears incrementally. Non-streaming still works identically. Token counts from stream tail are parsed. Errors during streaming are displayed.

### Sprint 2: Polish and Abort
- Goals: Add stop button, handle HTTP errors gracefully, mobile testing
- Features: #6, #7, #8
- Definition of done: Stop button cancels stream. 4xx/5xx responses show proper error bubbles. Streaming works on mobile.

### Sprint 3: Nice-to-Have
- Goals: Live timing, auto-scroll sensitivity, keyboard shortcuts
- Features: #9, #10, #11, #12
- Definition of done: Polish touches that make the streaming experience delightful.

## Technical Stack

- Frontend: Vanilla JS (ES5-compatible), HTMX 2.0.4 (for model picker only), CSS custom properties
- Backend: Go (freegate proxy — no backend changes needed)
- Key libraries: None beyond what already exists (`fetch`, `ReadableStream`, `TextDecoder`)

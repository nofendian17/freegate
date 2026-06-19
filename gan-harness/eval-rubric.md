# Evaluation Rubric: Streaming Playground

## Scoring Rules

- Each criterion is scored on a **0-10 scale**.
- Weighted total = sum(category_score * weight).
- **Pass threshold: 7.0/10**

---

## Category 1: Functionality (weight: 0.4)

### 1.1 Stream toggle enablement
- **0**: Toggle is still `disabled` or not persisted
- **5**: Toggle is clickable, persists across page reload in localStorage
- **8**: Toggle correctly disabled while `inFlight === true` to prevent mode switching mid-request
- **10**: Toggle state is reflected in the request body JSON sent to the API

### 1.2 Non-streaming path preserved
- **0**: Non-streaming is broken (regression)
- **5**: Non-streaming requests complete with the same JSON response parsing as before
- **8**: Non-streaming error handling (4xx, network error) works identically
- **10**: Non-streaming path is literally the same `if` branch — `handleFetchResponse()` is unmodified

### 1.3 Streaming displays incremental content
- **0**: No content appears during streaming
- **5**: Content appears chunk-by-chunk in the assistant bubble
- **8**: Content updates are smooth — no flickering or text duplication
- **10**: Multiple rapid SSE chunks in a single reader.read() are all parsed and displayed correctly

### 1.4 Stream termination
- **0**: Stream never ends / hangs indefinitely
- **5**: `[DONE]` terminates the stream and finalizes the bubble
- **8**: Finalization removes `msg-streaming` class (cursor stops blinking)
- **10**: `stream_options: {include_usage: true}` tail chunk is parsed and token count is displayed in meta line

### 1.5 Error handling during streaming
- **0**: Stream errors cause unhandled exceptions or silent failures
- **5**: Network errors during streaming show an error state in the assistant bubble
- **8**: HTTP errors (4xx/5xx) before stream start are handled by reading response body as text and parsing JSON error
- **10**: Partial content received before error is preserved in the assistant bubble; error is appended or noted in meta

### 1.6 Abort/stop
- **0**: No stop mechanism
- **5**: Stop button appears during streaming, clicking it aborts the fetch
- **8**: Aborted response content (partial) is preserved in the bubble
- **10**: AbortController is cleaned up after stream ends (no memory leaks)

### 1.7 Empty stream
- **0**: Empty stream causes a crash or hang
- **5**: Empty stream (no delta, immediate `[DONE]`) produces an empty assistant bubble with no error
- **10**: Empty stream bubble is finalized correctly with `msg-streaming` removed

---

## Category 2: Craft (weight: 0.25)

### 2.1 No new dependencies
- **0**: A library or polyfill was added
- **5**: Vanilla JS only — uses `ReadableStream`, `TextDecoder`, native `fetch()`
- **10**: No build step, no npm, no CDN scripts

### 2.2 SSE parser quality
- **0**: No SSE parser — code breaks on multi-event chunks or split boundaries
- **5**: SSE parser correctly handles `data:` lines, `[DONE]`, and JSON delta extraction
- **8**: Parser handles split SSE boundaries (event split across two `reader.read()` calls)
- **10**: Parser handles malformed JSON in `data:` gracefully (skips the chunk, continues)

### 2.3 State management
- **0**: Race conditions or leaked references
- **5**: `inFlight` guard prevents concurrent sends
- **8**: `activeAssistantBubble` reference is correctly cleaned up on error, abort, and success
- **10: `save()` is NOT called per-chunk — only once in `finalizeAssistant()`. No localStorage thrashing.**

### 2.4 Error messages
- **0**: Technical errors (stack traces, `[object Object]`) shown to user
- **5**: User-facing error messages in the assistant bubble
- **8**: Error bullets include HTTP status code and truncated body
- **10**: Network errors distinguish between "connection lost", "aborted", and "server error"

### 2.5 Readability and maintainability
- **0**: Code is a tangled mess, streaming logic mixed with non-streaming
- **5**: Clear branching: `if (state.stream) { streamingPath() } else { nonStreamingPath() }`
- **8**: SSE parsing is a pure function with no side effects
- **10**: AbortController lifecycle is clearly bounded (created before fetch, cleaned up in `finally`)

---

## Category 3: Design (weight: 0.2)

### 3.1 Streaming cursor
- **0**: No cursor or wrong cursor animation
- **5**: `msg-streaming` CSS class is applied during streaming, removed on completion
- **8**: The `_` cursor blinks at 1s interval (existing `pg-blink` animation)
- **10**: Cursor does not cause layout shift — it's a `::after` pseudo-element, not real content

### 3.2 Stop button
- **0**: No stop button
- **5**: A stop/abort button replaces the send button during streaming
- **8**: Stop button uses a distinct visual style (e.g., `var(--error)` color)
- **10**: Stop button is a 44x44px touch target on mobile

### 3.3 Visual consistency
- **0**: Streaming bubbles look different from non-streaming after completion
- **5**: After `finalizeAssistant()`, a streamed bubble is visually identical to a non-streamed one
- **8**: Error bubbles from streaming are indistinguishable from error bubbles from non-streaming
- **10**: The blink animation respects `prefers-reduced-motion`

### 3.4 Mobile behavior
- **0**: Streaming breaks on mobile (text doesn't update, layout breaks)
- **5**: Streaming works on mobile viewports (768px, 480px breakpoints)
- **8**: Stop button meets 44px touch target
- **10**: iOS safe-area insets are respected during streaming (content not hidden behind notch/home indicator)

---

## Category 4: Originality (weight: 0.15)

### 4.1 Use of existing infrastructure
- **0**: Rewrote things that already existed
- **5**: Reuses `createAssistantPlaceholder()`, `finalizeAssistant()`, `beforeSend()` without modification
- **8**: Reuses `msg-streaming` CSS class (was already defined but unused)
- **10**: No changes to `handleFetchResponse()` — the non-streaming function signature is untouched

### 4.2 Clean SSE approach
- **0**: Copies an SSE library or polyfill
- **5**: Minimal, focused SSE parser (~20 lines)
- **8**: Parser uses a simple string buffer rather than complex state machine
- **10**: Parser handles both OpenAI-style (`data: {"choices":...}`) and eventual Anthropic-style (`data: {"type":"content_block_delta"}`) with minimal adaptation

### 4.3 Forward-looking design
- **0**: Only handles the happy path
- **5**: Handles `[DONE]` before any data (empty stream)
- **8**: Detects stream support at runtime and gracefully degrades (hides toggle if `!ReadableStream`)
- **10**: Code structure allows adding an "interleaved" mode (show partial responses but return full JSON) without refactoring

---

## Scoring Sheet

| Category | Weight | Score (0-10) | Weighted |
|---|---|---|---|
| Functionality | 0.40 | | |
| Craft | 0.25 | | |
| Design | 0.20 | | |
| Originality | 0.15 | | |
| **Total** | **1.00** | | **/ 10** |

**Pass threshold: 7.0/10**

### Quick Scoring Reference

- **10**: Production-quality, no notes
- **8**: Solid, one minor issue
- **6**: Works but has rough edges
- **4**: Broken or missing major pieces
- **2**: Barely functional
- **0**: Not attempted or completely broken

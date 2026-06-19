# Evaluation -- Iteration 001

## Scores

| Criterion | Score | Weight | Weighted |
|-----------|-------|--------|----------|
| Functionality | 9.5/10 | 0.40 | 3.80 |
| Craft | 10.0/10 | 0.25 | 2.50 |
| Design | 9.0/10 | 0.20 | 1.80 |
| Originality | 7.0/10 | 0.15 | 1.05 |
| **TOTAL** | | **1.00** | **9.15/10** |

## Verdict: PASS (threshold: 7.0)

This is a strong implementation. All Sprint 1 (must-have) and Sprint 2 (should-have) features are present and correctly implemented. The code is clean, well-structured, and makes thoughtful use of existing infrastructure. No regressions in existing functionality. The streaming path, SSE parser, abort controller, error handling, and mobile support are all production-quality.

---

## Critical Issues (must fix)

None.

---

## Major Issues (should fix)

1. **`streamDone` variable is dead code** -- `web/static/js/playground.js` lines 391, 413, 438: The `streamDone` variable is declared, reset in `handleStreamingSend()`, and set to `true` in `onStreamEvent()` when the `[DONE]` SSE signal is received, but it is never read anywhere. The stream termination logic relies entirely on the ReadableStream's `done` signal from `reader.read()`. This is dead code that will confuse future maintainers.
   - **How to fix**: Either (a) remove `streamDone` entirely if you intend to keep relying on `reader.read()` `done` for termination, or (b) actually drive termination from `[DONE]` by checking `streamDone` after each `parseSSEChunks` call and breaking out of the pump loop early. Option (b) is more robust because it doesn't depend on the server closing the TCP connection immediately after `[DONE]`.

2. **No runtime detection of ReadableStream support** -- `web/static/js/playground.js`: The spec explicitly calls this out as edge case #8: if the browser does not support `ReadableStream` (unlikely but possible in older Safari or polyfill-free environments), `resp.body.getReader()` will throw an unhandled error. The code should check `typeof ReadableStream === 'undefined'` and (a) hide or disable the stream toggle, and (b) fall back to non-streaming mode.
   - **How to fix**: In `handleStreamingSend()`, add an early guard:
     ```js
     if (typeof ReadableStream === 'undefined' || !resp.body || !resp.body.getReader) {
       // Fall back to non-streaming path for this request
       handleFetchResponse({ status: resp.status, body: await resp.text() }, streamStartTime);
       return;
     }
     ```

3. **Content-Type check missing for streaming response** -- Spec feature #6 requires detecting non-SSE responses by checking `Content-Type !== 'text/event-stream'` in addition to the status code check. Currently only `!resp.ok` is checked (line 455). A 200 OK that returns non-SSE content would attempt to parse it as a stream and fail.
   - **How to fix**: After the `resp.ok` check and before calling `getReader()`, add: `var contentType = resp.headers.get('content-type'); if (!contentType || contentType.indexOf('text/event-stream') === -1) { ... fall back ... }`

---

## Minor Issues (nice to fix)

1. **`streamDone` unused detail** (see Major Issue 1). If you keep it, at minimum add a comment. If you remove it, also clean up the `onStreamEvent` handling for the `done` type.

2. **`prefers-reduced-motion` cursor visibility** -- `web/static/css/app.css` lines 89-97: The global `prefers-reduced-motion` rule kills all animations with `!important`. For the streaming cursor (`::after` with `pg-blink`), this means the cursor blinks once very fast (0.01ms) and ends up invisible (the `50% { opacity: 0 }` keyframe ends at invisible). Users who prefer reduced motion will see no cursor at all, which removes the visual indicator that streaming is active.
   - **How to fix**: Add a specific rule for reduced motion that keeps the cursor visible without animation:
     ```css
     @media (prefers-reduced-motion: reduce) {
       .msg-streaming .msg-body::after {
         animation: none;
         opacity: 1;
       }
     }
     ```

3. **Non-streaming path `$('pg-stream').disabled = false` in `.then()`** -- `web/static/js/playground.js` lines 837-840: The non-streaming path re-enables the stream toggle in a `.then()` callback after `handleFetchResponse`. This works because all preceding `.then()` and `.catch()` handlers return resolved promises, but it's fragile -- if any preceding handler throws or returns a rejected promise, the toggle stays disabled. A `finally()` polyfill or a guard in `handleFetchResponse` would be more robust.

4. **`finalizeAssistant()` was modified** -- `web/static/js/playground.js` lines 573-583: The `preserveContent` option was added. This is backward-compatible (the default is `undefined` which is falsy), but it's a modification to a function the spec says should be reused unchanged. Consider if the `preserveContent` logic could be handled in the caller instead.

5. **SSE parser is ~40 lines, not ~20** -- Not a functional issue, but the parser could be more concise. The inner loop over `lines` with a `break` is slightly awkward since each SSE event in OpenAI format has exactly one `data:` line.

---

## What Improved Since Last Iteration

- This is the first implementation of streaming support. Everything is net-new functionality compared to the previous non-streaming-only codebase.

---

## What Regressed Since Last Iteration

- Nothing detected. All 7 existing tests pass. The non-streaming path is preserved with `handleFetchResponse()` unmodified. No lint or build issues.

---

## Specific Suggestions for Next Iteration

1. **Fix the three Major Issues above** -- Remove or wire up `streamDone`, add `ReadableStream` runtime detection with graceful fallback, add `Content-Type` header check for streaming responses.

2. **Add `Escape` keyboard shortcut for stop** -- Spec feature #12 (nice-to-have). The existing `keydown` listener at line 610 already handles Escape for closing the modal. Add a branch:
   ```js
   if (e.key === 'Escape' && inFlight && state.stream && state.abortController) {
     stopStreaming();
   }
   ```

3. **Add streaming timing to meta line** -- Spec feature #9 (nice-to-have). During streaming, update the assistant bubble's meta line with elapsed seconds (e.g., `$ assistant model-name 3.2s`). This gives live feedback on how long the generation has been running.

4. **Add auto-scroll sensitivity** -- Spec feature #11 (nice-to-have). The current `onStreamChunk` always scrolls to the bottom (line 408). Track whether the user has scrolled up and only auto-scroll if they're already at the bottom. This prevents fighting the user when they're reading earlier content.

5. **Add `Sprint 3` entry to spec** -- The nice-to-have features (#9-12) are defined but not scheduled. Consider promoting the Escape shortcut (#12) and auto-scroll (#11) to Sprint 2 polish if they address observed UX friction.

---

## Screenshots

N/A -- code-only evaluation mode. No browser screenshots were taken.

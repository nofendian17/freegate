(function () {
  'use strict';

  // ----- Constants -----
  var STORAGE_KEY = 'freegate.playground.v1';
  var SYSTEM_COLLAPSE_KEY = 'freegate.playground.systemCollapsed';
  var MAX_THREAD_BYTES = 100 * 1024; // cap persisted thread size; clear if exceeded

  // ----- State -----
  var state = {
    model: '',
    system: '',
    stream: true,
    messages: []
  };
  var inFlight = false;
  var activeAssistantBubble = null; // reference to the live assistant <pre> being filled
  var lastFocused = null; // element that opened the modal, for focus restoration

  // Pre-populate window.fgPlayground with no-op stubs so that any htmx
  // hx-on expression that fires before the rest of the IIFE completes
  // (e.g. an htmx:after-request from the model picker loading, or a
  // keydown on the textarea) finds a defined function instead of
  // throwing "window.fgPlayground.X is not a function". The real
  // implementations below overwrite these stubs. The hx-on attributes
  // in playground_modal.html do not need a defensive guard because of
  // this.
  //
  // We declare isOpen as a function expression inline because it does
  // not depend on the state that may not yet be initialised at the
  // time the stub is captured.
  window.fgPlayground = {
    open: function () {},
    close: function () {},
    clear: function () {},
    onInputKeydown: function () {},
    onModelsLoaded: function () {},
    onSystemInput: function () {},
    toggleSystem: function () {},
    onStreamToggle: function () {},
    stopStreaming: function () {},
    requestBody: function () { return { model: '', messages: [], stream: false }; },
    beforeSend: function () { return false; },
    send: function () {},
    handleFetchResponse: function () {},
    isOpen: function () { return false; }
  };

  // ----- DOM helpers -----
  function $(id) { return document.getElementById(id); }
  function fmtTime(ts) {
    if (!ts) return '';
    var d = new Date(ts);
    function pad(n) { return n < 10 ? '0' + n : '' + n; }
    return pad(d.getUTCHours()) + ':' + pad(d.getUTCMinutes()) + ':' + pad(d.getUTCSeconds());
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
        // Strip orphaned trailing user message (interrupted send)
        if (state.messages.length > 0) {
          var last = state.messages[state.messages.length - 1];
          if (last.role === 'user') {
            state.messages.pop();
          }
        }
      }
    } catch (e) {
      console.warn('[playground] failed to load thread:', e);
    }
  }

  function save() {
    try {
      var payload = JSON.stringify(state);
      if (payload.length > MAX_THREAD_BYTES) {
        console.warn('[playground] thread exceeds ' + MAX_THREAD_BYTES + ' bytes, clearing');
        state.messages = [];
      }
      localStorage.setItem(STORAGE_KEY, JSON.stringify(state));
    } catch (e) {
      if (e && e.name === 'QuotaExceededError') {
        console.warn('[playground] localStorage quota exceeded; thread not persisted');
      } else {
        console.warn('[playground] failed to save thread:', e);
      }
    }
  }

  // ----- Rendering -----
  function buildMessageEl(m) {
    var wrap = document.createElement('div');
    wrap.className = 'msg ' + (m.role === 'user' ? 'msg-user' : 'msg-assistant');
    wrap.setAttribute('data-role', m.role);

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
    // Snapshot the children before mutating: live HTMLCollection iteration
    // is not safe across removeChild in all browsers. We keep the
    // pg-empty placeholder in the DOM (just hide it) so subsequent calls
    // to $('pg-empty') still find it. Detaching + re-attaching it leaves
    // the variable bound to a detached node, and a second renderList
    // would dereference a null $('pg-empty') result.
    var children = Array.prototype.slice.call(list.children);
    for (var i = 0; i < children.length; i++) {
      if (children[i] !== empty) list.removeChild(children[i]);
    }
    if (state.messages.length === 0) {
      if (empty) empty.style.display = '';
      return;
    }
    if (empty) empty.style.display = 'none';
    for (var j = 0; j < state.messages.length; j++) {
      list.appendChild(buildMessageEl(state.messages[j]));
    }
    list.scrollTop = list.scrollHeight;
  }

  function appendUserMessage(content) {
    var m = { role: 'user', content: content, ts: Date.now() };
    state.messages.push(m);
    save();
    var list = $('pg-list');
    var empty = $('pg-empty');
    if (empty) empty.style.display = 'none';
    list.appendChild(buildMessageEl(m));
    list.scrollTop = list.scrollHeight;
  }

  function createAssistantPlaceholder() {
    var m = { role: 'assistant', content: '', ts: Date.now(), model: state.model };
    state.messages.push(m);
    var list = $('pg-list');
    var wrap = document.createElement('div');
    wrap.className = 'msg msg-assistant msg-streaming';
    wrap.setAttribute('data-role', 'assistant');
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
      if (opts.preserveContent) {
        // Preserve partial content received so far, append error indicator
        handle.body.textContent = (handle.body.textContent || '') + '\n! error: ' + opts.error;
        handle.message.content = handle.body.textContent;
      } else {
        handle.body.textContent = '! error: ' + opts.error;
        handle.message.content = '';
      }
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

  // ----- Public surface (called from HTMX attributes in the modal template) -----

  function open() {
    load();
    lastFocused = document.activeElement;
    $('pg-overlay').style.display = 'flex';
    document.body.classList.add('modal-open');
    $('pg-system').value = state.system;
    $('pg-stream').checked = state.stream;
    renderList();
    // Model options load via hx-get when the <select> intersects the viewport.
    // After they swap in, onModelsLoaded() restores state.model.
    $('pg-input').focus();
  }

  function close() {
    $('pg-overlay').style.display = 'none';
    document.body.classList.remove('modal-open');
    if (lastFocused && typeof lastFocused.focus === 'function') {
      lastFocused.focus();
    }
    lastFocused = null;
  }

  function clear() {
    if (!window.confirm('Clear playground thread?')) return;
    state.messages = [];
    save();
    renderList();
  }

  function onInputKeydown(evt) {
    if (evt.key === 'Enter' && !evt.shiftKey) {
      evt.preventDefault();
      $('pg-form').requestSubmit
        ? $('pg-form').requestSubmit()
        : $('pg-form').dispatchEvent(new Event('submit', { cancelable: true, bubbles: true }));
    }
  }

  function onModelsLoaded() {
    var sel = $('pg-model');
    if (!sel) return;
    if (state.model) {
      var found = false;
      for (var i = 0; i < sel.options.length; i++) {
        if (sel.options[i].value === state.model) { found = true; break; }
      }
      sel.value = found ? state.model : '';
      if (!found) state.model = '';
    }
    sel.addEventListener('change', function (e) {
      state.model = e.target.value;
      save();
    });
  }

  function onSystemInput(_evt) {
    state.system = $('pg-system').value;
    save();
  }

  function toggleSystem() {
    var wrap = $('pg-system-wrap');
    if (!wrap) return;
    var collapsed = wrap.classList.toggle('collapsed');
    try { localStorage.setItem(SYSTEM_COLLAPSE_KEY, collapsed ? '1' : '0'); } catch (e) {}
    var btn = $('pg-system-toggle');
    if (btn) btn.setAttribute('aria-expanded', collapsed ? 'false' : 'true');
  }

  function onStreamToggle(evt) {
    if (inFlight) {
      // Revert the checkbox — cannot switch modes mid-flight
      evt.target.checked = state.stream;
      return;
    }
    state.stream = evt.target.checked;
    save();
  }

  // Build the request body sent by the fetch call.
  function requestBody() {
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
    var body = {
      model: state.model,
      messages: msgs,
      stream: state.stream
    };
    if (state.stream) {
      body.stream_options = { include_usage: true };
    }
    return body;
  }

  function beforeSend() {
    // Called by send() before issuing the XHR. Returns true if the send
    // should proceed, false to cancel. Performs the optimistic UI work
    // (clears the input, appends the user bubble, creates the assistant
    // placeholder, locks the send button).
    if (inFlight) return false;
    var input = $('pg-input');
    var text = input.value;
    if (!text || !text.trim()) return false;
    if (!state.model) {
      console.warn('[playground] no model selected');
      return false;
    }
    text = text.trim();
    input.value = '';
    appendUserMessage(text);
    activeAssistantBubble = createAssistantPlaceholder();
    inFlight = true;
    $('pg-send').disabled = true;
    $('pg-stream').disabled = true;
    return true;
  }

  function truncate(s, n) {
    s = String(s || '');
    return s.length > n ? s.slice(0, n) + '…' : s;
  }

  // ----- SSE Parser (OpenAI streaming format) -----
  // Given a buffer string (may contain partial events), splits on '\n\n'
  // boundaries and invokes callbacks for complete events.
  // Returns the unconsumed tail of the buffer for the next call.
  function parseSSEChunks(buffer, onChunk, onEvent) {
    var parts = buffer.split('\n\n');
    // The last element may be an incomplete event; keep it as the new buffer
    var remaining = parts.pop();

    for (var i = 0; i < parts.length; i++) {
      var block = parts[i].trim();
      if (!block) continue;

      var lines = block.split('\n');
      for (var j = 0; j < lines.length; j++) {
        var line = lines[j];
        if (line.indexOf('data: ') === 0) {
          var payload = line.slice(6);
          if (payload === '[DONE]') {
            if (onEvent) onEvent({ type: 'done' });
          } else {
            try {
              var parsed = JSON.parse(payload);
              // Delta content chunk
              if (parsed.choices && parsed.choices[0]) {
                var delta = parsed.choices[0].delta;
                if (delta && typeof delta.content === 'string') {
                  if (onChunk) onChunk(delta.content);
                }
              }
              // Usage tail chunk (comes after [DONE] when include_usage is set)
              if (parsed.usage && typeof parsed.usage === 'object') {
                if (onEvent) onEvent({ type: 'usage', usage: parsed.usage });
              }
            } catch (e) {
              // Malformed JSON — skip the chunk silently
            }
          }
          // Each SSE event has exactly one data: line; stop processing this event
          break;
        }
      }
    }

    return remaining;
  }

  // ----- Streaming state -----
  var streamStartTime = 0;
  var streamUsage = null;

  function cleanupStreamUI() {
    state.abortController = null;
    inFlight = false;
    $('pg-send').disabled = false;
    $('pg-send').style.display = '';
    var stopEl = $('pg-stop');
    if (stopEl) stopEl.style.display = 'none';
    $('pg-stream').disabled = false;
  }

  function onStreamChunk(content) {
    if (!activeAssistantBubble) return;
    activeAssistantBubble.body.textContent += content;
    activeAssistantBubble.message.content += content;
    var list = $('pg-list');
    list.scrollTop = list.scrollHeight;
  }

  function onStreamEvent(evt) {
    if (evt.type === 'usage') {
      streamUsage = evt.usage;
    }
  }

  function finalizeStream(errorMsg) {
    if (!activeAssistantBubble) return;
    var now = window.performance ? performance.now() : Date.now();
    var opts = {
      duration_ms: Math.round(now - streamStartTime),
      tokens: streamUsage && streamUsage.total_tokens ? streamUsage.total_tokens : null
    };
    if (errorMsg) {
      opts.error = errorMsg;
      opts.preserveContent = true;
    }
    finalizeAssistant(activeAssistantBubble, opts);
    activeAssistantBubble = null;
    cleanupStreamUI();
  }

  function handleStreamingSend(t0) {
    streamStartTime = t0 || (window.performance ? performance.now() : Date.now());
    streamUsage = null;

    var controller = new AbortController();
    state.abortController = controller;

    // Swap send button for stop button
    $('pg-send').style.display = 'none';
    var stopEl = $('pg-stop');
    if (stopEl) stopEl.style.display = '';

    fetch('/v1/chat/completions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(requestBody()),
      signal: controller.signal
    })
    .then(function(resp) {
      if (!resp.ok) {
        // HTTP error before stream — read body as text, parse JSON error
        return resp.text().then(function(text) {
          if (!activeAssistantBubble) return;
          var errorBody = text;
          try {
            var errJson = JSON.parse(text);
            if (errJson.error && typeof errJson.error === 'object' && errJson.error.message) {
              errorBody = errJson.error.message;
            } else if (errJson.error && typeof errJson.error === 'string') {
              errorBody = errJson.error;
            } else if (errJson.error) {
              errorBody = JSON.stringify(errJson.error);
            }
          } catch (e) { /* use raw body */ }
          finalizeStream(resp.status + ' ' + truncate(errorBody, 500));
        });
      }

      // Feature-detect ReadableStream — fall back to non-streaming if unsupported
      if (typeof ReadableStream === 'undefined' || !resp.body || !resp.body.getReader) {
        return resp.text().then(function(text) {
          handleFetchResponse({ status: resp.status, body: text }, streamStartTime);
          cleanupStreamUI();
        });
      }

      // Verify the response is actually an SSE stream — fall back otherwise
      var ct = resp.headers.get('content-type');
      if (!ct || ct.indexOf('text/event-stream') === -1) {
        return resp.text().then(function(text) {
          handleFetchResponse({ status: resp.status, body: text }, streamStartTime);
          cleanupStreamUI();
        });
      }

      // Streaming path: consume the ReadableStream
      var reader = resp.body.getReader();
      var decoder = new TextDecoder();
      var buf = '';

      function pump() {
        return reader.read().then(function(result) {
          if (result.done) {
            // Final decode for any lingering bytes
            buf = parseSSEChunks(buf + decoder.decode(), onStreamChunk, onStreamEvent);
            finalizeStream(null);
            return;
          }

          buf = parseSSEChunks(
            buf + decoder.decode(result.value, { stream: true }),
            onStreamChunk,
            onStreamEvent
          );
          return pump();
        });
      }

      return pump();
    })
    .catch(function(err) {
      if (err && err.name === 'AbortError') {
        // User stopped — preserve partial content
        finalizeStream('stopped');
      } else {
        // Network error mid-stream
        var msg = err && err.message ? err.message : String(err);
        finalizeStream(msg);
      }
    });
  }

  function stopStreaming() {
    if (state.abortController) {
      state.abortController.abort();
      state.abortController = null;
    }
  }

  // Form submit handler — bound to the form via hx-on:submit. We use
  // fetch() directly rather than hx-post + hx-vals='js:...': the htmx
  // 2.0.4 js: expression evaluator chokes on member-access expressions
  // (see validation report 2026-06-07), and the shim already owns all
  // the request state. Direct fetch is the simpler path.
  function send(evt) {
    if (evt && typeof evt.preventDefault === 'function') evt.preventDefault();
    if (!beforeSend()) return;
    var t0 = window.performance ? performance.now() : Date.now();

    if (state.stream) {
      handleStreamingSend(t0);
      return;
    }

    // Non-streaming path (unchanged behaviour)
    fetch('/v1/chat/completions', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(requestBody())
    })
      .then(function (resp) {
        return resp.text().then(function (text) {
          return { status: resp.status, body: text };
        });
      })
      .then(function (result) { handleFetchResponse(result, t0); })
      .catch(function (err) {
        handleFetchResponse({ status: 0, body: 'network error: ' + (err && err.message ? err.message : String(err)) }, t0);
      })
      .then(function () {
        // Post-request cleanup for non-streaming path
        $('pg-stream').disabled = false;
      });
  }

  function handleFetchResponse(result, t0) {
    if (!activeAssistantBubble) {
      inFlight = false;
      $('pg-send').disabled = false;
      return;
    }
    var status = result.status;
    var raw = result.body || '';
    var now = (window.performance ? performance.now() : Date.now());

    if (status < 200 || status >= 300) {
      finalizeAssistant(activeAssistantBubble, {
        error: status + ' ' + truncate(raw, 500),
        duration_ms: Math.round(now - t0)
      });
    } else {
      var content = '';
      var tokens = null;
      try {
        var data = JSON.parse(raw);
        var choice = data.choices && data.choices[0];
        var msg = choice && choice.message;
        content = (msg && msg.content) || '';
        if (data.usage && typeof data.usage.total_tokens === 'number') {
          tokens = data.usage.total_tokens;
        }
      } catch (e) {
        finalizeAssistant(activeAssistantBubble, {
          error: 'invalid response: ' + truncate(raw, 200),
          duration_ms: Math.round(now - t0)
        });
        activeAssistantBubble = null;
        inFlight = false;
        $('pg-send').disabled = false;
        return;
      }
      activeAssistantBubble.body.textContent = content;
      activeAssistantBubble.message.content = content;
      finalizeAssistant(activeAssistantBubble, {
        duration_ms: Math.round(now - t0),
        tokens: tokens
      });
    }
    activeAssistantBubble = null;
    inFlight = false;
    $('pg-send').disabled = false;
  }

  // ----- Bootstrap -----
  function bindGlobal() {
    var openBtn = document.getElementById('open-playground');
    if (openBtn) openBtn.addEventListener('click', open);

    $('pg-overlay').addEventListener('click', function (e) {
      if (e.target === $('pg-overlay')) close();
    });
    document.addEventListener('keydown', function (e) {
      if (e.key === 'Escape' && $('pg-overlay').style.display === 'flex') close();
      if (e.key === 'Tab' && $('pg-overlay').style.display === 'flex') {
        var focusable = $('pg-panel').querySelectorAll(
          'button, [href], input, select, textarea, [tabindex]:not([tabindex="-1"])'
        );
        if (focusable.length === 0) return;
        var first = focusable[0];
        var last = focusable[focusable.length - 1];
        if (e.shiftKey && document.activeElement === first) {
          e.preventDefault();
          last.focus();
        } else if (!e.shiftKey && document.activeElement === last) {
          e.preventDefault();
          first.focus();
        }
      }
    });

    // Restore system-prompt collapse state
    try {
      if (localStorage.getItem(SYSTEM_COLLAPSE_KEY) === '0') {
        var wrap = $('pg-system-wrap');
        if (wrap) wrap.classList.remove('collapsed');
        var btn = $('pg-system-toggle');
        if (btn) btn.setAttribute('aria-expanded', 'true');
      }
    } catch (e) {}
  }

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', bindGlobal);
  } else {
    bindGlobal();
  }

  // Public surface used by hx-on:* in the modal template
  window.fgPlayground = {
    open: open,
    close: close,
    clear: clear,
    onInputKeydown: onInputKeydown,
    onModelsLoaded: onModelsLoaded,
    onSystemInput: onSystemInput,
    toggleSystem: toggleSystem,
    onStreamToggle: onStreamToggle,
    stopStreaming: stopStreaming,
    requestBody: requestBody,
    beforeSend: beforeSend,
    send: send,
    handleFetchResponse: handleFetchResponse,
    isOpen: function () { return $('pg-overlay').style.display === 'flex'; }
  };
})();

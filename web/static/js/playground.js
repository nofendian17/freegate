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

  // ----- Public surface (called from HTMX attributes in the modal template) -----

  function open() {
    load();
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

  // Build the request body sent by the HTMX form. Always non-streaming in
  // this rewrite (htmx-sse does not solve POST-to-SSE for our case). Streaming
  // can be added later as a follow-up.
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
    return {
      model: state.model,
      messages: msgs,
      stream: false
    };
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
    return true;
  }

  function truncate(s, n) {
    s = String(s || '');
    return s.length > n ? s.slice(0, n) + '…' : s;
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
    requestBody: requestBody,
    beforeSend: beforeSend,
    send: send,
    handleFetchResponse: handleFetchResponse,
    isOpen: function () { return $('pg-overlay').style.display === 'flex'; }
  };
})();

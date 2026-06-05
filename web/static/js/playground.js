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

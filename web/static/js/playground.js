// freegate playground — Alpine component + fetch/SSE client.
// Persists the thread in localStorage; streams via ReadableStream.
(function () {
  'use strict';

  var STORAGE_KEY = 'freegate.playground.v1';
  var MAX_THREAD_BYTES = 100 * 1024;

  function validMessage(m) {
    return m && typeof m === 'object' &&
      (m.role === 'user' || m.role === 'assistant') &&
      typeof m.content === 'string';
  }

  function fmtTime(ts) {
    if (!ts) return '';
    var d = new Date(ts);
    var p = function (n) { return (n < 10 ? '0' : '') + n; };
    return p(d.getUTCHours()) + ':' + p(d.getUTCMinutes()) + ':' + p(d.getUTCSeconds());
  }

  function truncate(s, n) {
    s = String(s || '');
    return s.length > n ? s.slice(0, n) + '…' : s;
  }

  // Splits an SSE buffer on '\n\n' boundaries; returns the unconsumed tail.
  function parseSSEChunks(buffer, onChunk, onEvent) {
    var parts = buffer.split('\n\n');
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
              if (parsed.choices && parsed.choices[0]) {
                var delta = parsed.choices[0].delta;
                if (delta && typeof delta.content === 'string' && onChunk) onChunk(delta.content);
              }
              if (parsed.usage && typeof parsed.usage === 'object' && onEvent) {
                onEvent({ type: 'usage', usage: parsed.usage });
              }
            } catch (e) { /* skip malformed chunk */ }
          }
          break; // one data line per event
        }
      }
    }
    return remaining;
  }

  document.addEventListener('alpine:init', function () {
    Alpine.data('playground', function () {
      return {
        open: false,
        models: [],
        model: '',
        system: '',
        showSystem: false,
        stream: true,
        input: '',
        messages: [],
        draft: '',
        streaming: false,
        busy: false,
        abort: null,
        usage: null,
        t0: 0,

        init: function () {
          this.load();
        },

        load: function () {
          try {
            var raw = localStorage.getItem(STORAGE_KEY);
            if (!raw) return;
            var parsed = JSON.parse(raw);
            if (!parsed || typeof parsed !== 'object') return;
            if (typeof parsed.model === 'string') this.model = parsed.model;
            if (typeof parsed.system === 'string') this.system = parsed.system;
            this.stream = parsed.stream !== false;
            if (Array.isArray(parsed.messages)) {
              this.messages = parsed.messages.filter(validMessage);
              if (this.messages.length && this.messages[this.messages.length - 1].role === 'user') {
                this.messages.pop(); // drop orphaned trailing user message
              }
            }
          } catch (e) { /* ignore corrupt storage */ }
        },

        save: function () {
          try {
            var payload = JSON.stringify({
              model: this.model,
              system: this.system,
              stream: this.stream,
              messages: this.messages,
            });
            if (payload.length > MAX_THREAD_BYTES) this.messages = [];
            localStorage.setItem(STORAGE_KEY, JSON.stringify({
              model: this.model,
              system: this.system,
              stream: this.stream,
              messages: this.messages,
            }));
          } catch (e) { /* quota or privacy mode — thread just won't persist */ }
        },

        loadModels: function () {
          var self = this;
          fetch('/v1/models', { credentials: 'same-origin' })
            .then(function (r) { return r.ok ? r.json() : null; })
            .then(function (body) {
              if (!body || !Array.isArray(body.data)) return;
              self.models = body.data.map(function (m) { return m.id; }).filter(Boolean);
              if (self.model && self.models.indexOf(self.model) < 0) self.model = '';
            })
            .catch(function () {});
        },

        meta: function (m) {
          var parts = [m.role === 'user' ? '> user' : '$ assistant'];
          if (m.ts) parts.push(fmtTime(m.ts));
          if (m.model) parts.push(m.model);
          if (typeof m.ms === 'number') parts.push(m.ms + 'ms');
          if (typeof m.tok === 'number') parts.push(m.tok + ' tok');
          return parts.join(' ');
        },

        close: function () {
          this.open = false;
        },

        clear: function () {
          if (!window.confirm('Clear playground thread?')) return;
          this.messages = [];
          this.save();
        },

        stop: function () {
          if (this.abort) this.abort.abort();
        },

        requestBody: function () {
          var msgs = [];
          if (this.system && this.system.trim()) msgs.push({ role: 'system', content: this.system });
          for (var i = 0; i < this.messages.length; i++) {
            var m = this.messages[i];
            if (m.role === 'user') msgs.push({ role: 'user', content: m.content });
            else if (m.role === 'assistant' && m.content && !m.err) {
              msgs.push({ role: 'assistant', content: m.content });
            }
          }
          var body = { model: this.model, messages: msgs, stream: this.stream };
          if (this.stream) body.stream_options = { include_usage: true };
          return body;
        },

        send: function () {
          if (this.busy || this.streaming) return;
          var text = (this.input || '').trim();
          if (!text || !this.model) return;
          this.input = '';
          this.messages.push({ role: 'user', content: text, ts: Date.now() });
          this.save();
          this.scroll();
          if (this.stream) this.sendStream();
          else this.sendOnce();
        },

        scroll: function () {
          var self = this;
          this.$nextTick(function () {
            var list = document.getElementById('pg-list');
            if (list) list.scrollTop = list.scrollHeight;
            void self;
          });
        },

        pushAssistant: function (content, opts) {
          opts = opts || {};
          this.messages.push({
            role: 'assistant',
            content: content,
            ts: Date.now(),
            model: this.model,
            ms: opts.ms,
            tok: opts.tok,
            err: !!opts.err,
          });
          this.save();
          this.scroll();
        },

        sendOnce: function () {
          var self = this;
          this.busy = true;
          var t0 = performance.now();
          fetch('/v1/chat/completions', {
            method: 'POST',
            credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(this.requestBody()),
          })
            .then(function (resp) {
              return resp.text().then(function (text) { return { status: resp.status, body: text }; });
            })
            .then(function (r) {
              var ms = Math.round(performance.now() - t0);
              if (r.status < 200 || r.status >= 300) {
                self.pushAssistant('! error: ' + r.status + ' ' + truncate(r.body, 500), { ms: ms, err: true });
                return;
              }
              try {
                var data = JSON.parse(r.body);
                var msg = data.choices && data.choices[0] && data.choices[0].message;
                self.pushAssistant((msg && msg.content) || '', {
                  ms: ms,
                  tok: data.usage && typeof data.usage.total_tokens === 'number' ? data.usage.total_tokens : undefined,
                });
              } catch (e) {
                self.pushAssistant('! error: invalid response ' + truncate(r.body, 200), { ms: ms, err: true });
              }
            })
            .catch(function (err) {
              self.pushAssistant('! error: network error ' + (err && err.message ? err.message : String(err)), { err: true });
            })
            .finally(function () { self.busy = false; });
        },

        sendStream: function () {
          var self = this;
          this.streaming = true;
          this.draft = '';
          this.usage = null;
          this.t0 = performance.now();
          var controller = new AbortController();
          this.abort = controller;
          this.scroll();

          fetch('/v1/chat/completions', {
            method: 'POST',
            credentials: 'same-origin',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify(this.requestBody()),
            signal: controller.signal,
          })
            .then(function (resp) {
              var ct = resp.headers.get('content-type') || '';
              if (!resp.ok || ct.indexOf('text/event-stream') === -1 || !resp.body || !resp.body.getReader) {
                return resp.text().then(function (text) {
                  var ms = Math.round(performance.now() - self.t0);
                  if (!resp.ok) {
                    var errBody = text;
                    try {
                      var ej = JSON.parse(text);
                      if (ej.error && ej.error.message) errBody = ej.error.message;
                      else if (typeof ej.error === 'string') errBody = ej.error;
                    } catch (e) { /* raw body */ }
                    self.pushAssistant('! error: ' + resp.status + ' ' + truncate(errBody, 500), { ms: ms, err: true });
                  } else {
                    try {
                      var data = JSON.parse(text);
                      var msg = data.choices && data.choices[0] && data.choices[0].message;
                      self.pushAssistant((msg && msg.content) || '', { ms: ms });
                    } catch (e) {
                      self.pushAssistant('! error: invalid response ' + truncate(text, 200), { ms: ms, err: true });
                    }
                  }
                  self.streaming = false;
                  self.abort = null;
                });
              }
              var reader = resp.body.getReader();
              var decoder = new TextDecoder();
              var buf = '';
              var pump = function () {
                return reader.read().then(function (result) {
                  if (result.done) {
                    parseSSEChunks(buf + decoder.decode(), function (c) {
                      self.draft += c;
                      self.scroll();
                    }, function (evt) {
                      if (evt.type === 'usage') self.usage = evt.usage;
                    });
                    var ms = Math.round(performance.now() - self.t0);
                    self.pushAssistant(self.draft, {
                      ms: ms,
                      tok: self.usage && self.usage.total_tokens ? self.usage.total_tokens : undefined,
                    });
                    self.draft = '';
                    self.streaming = false;
                    self.abort = null;
                    return;
                  }
                  buf = parseSSEChunks(buf + decoder.decode(result.value, { stream: true }), function (c) {
                    self.draft += c;
                    var list = document.getElementById('pg-list');
                    if (list) list.scrollTop = list.scrollHeight;
                  }, function (evt) {
                    if (evt.type === 'usage') self.usage = evt.usage;
                  });
                  return pump();
                });
              };
              return pump();
            })
            .catch(function (err) {
              var ms = Math.round(performance.now() - self.t0);
              if (err && err.name === 'AbortError') {
                self.pushAssistant(self.draft + '\n! stopped', { ms: ms, err: true });
              } else {
                self.pushAssistant((self.draft ? self.draft + '\n' : '') + '! error: ' + (err && err.message ? err.message : String(err)), { ms: ms, err: true });
              }
              self.draft = '';
              self.streaming = false;
              self.abort = null;
            });
        },
      };
    });
  });
})();

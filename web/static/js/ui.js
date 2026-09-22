// freegate shared UI helpers (loaded before page scripts).
// Exposes window.FG with the exact helpers providers.js and settings.js
// previously duplicated: esc, show, getJSON, withBusy.
(function () {
  'use strict';

  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"]/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c];
    });
  }

  // Error/output boxes keep their own margin in markup. show() only toggles
  // `hidden` plus the error/ok tone classes — never rewrites className — so
  // an empty box takes no space in the card layout.
  var ERR_TONE = ['border-ember', 'bg-paper', 'text-ember'];
  var OK_TONE = ['border-hairline', 'bg-surface-alt', 'text-ink-soft'];

  function show(el, msg, ok) {
    if (!el) return;
    el.textContent = msg || '';
    el.classList.toggle('hidden', !msg);
    // Swap tone only — never rewrite className, which would drop the
    // layout classes and the hidden flag set above.
    var remove = ok ? ERR_TONE : OK_TONE;
    var add = ok ? OK_TONE : ERR_TONE;
    for (var i = 0; i < remove.length; i++) el.classList.remove(remove[i]);
    for (var j = 0; j < add.length; j++) el.classList.add(add[j]);
  }

  // fetch + parse JSON, with HTTP status checked and typed errors.
  // Tolerates empty bodies (204 on DELETE).
  function getJSON(url, opts) {
    return fetch(url, Object.assign({ credentials: 'same-origin' }, opts || {}))
      .then(function (r) {
        if (r.status === 204) return { ok: true, body: null };
        return r.text().then(function (t) {
          var b = null;
          try {
            b = t ? JSON.parse(t) : null;
          } catch (e) {
            b = { raw: t };
          }
          return { ok: r.ok, status: r.status, body: b };
        });
      })
      .then(function (res) {
        if (!res.ok) {
          var msg = res.body && res.body.error && res.body.error.message
            ? res.body.error.message : JSON.stringify(res.body);
          throw new Error(msg);
        }
        return res.body;
      });
  }

  // Disable a button while its request is in flight; restores it after.
  // Rethrows the error so callers' .catch handlers still run.
  function withBusy(btn, label, fn) {
    if (btn.disabled) return Promise.reject(new Error('busy'));
    var orig = btn.textContent;
    btn.disabled = true;
    if (label) btn.textContent = label;
    var restore = function () {
      btn.disabled = false;
      if (label) btn.textContent = orig;
    };
    return fn().then(function (v) { restore(); return v; }, function (e) { restore(); throw e; });
  }

  window.FG = { esc: esc, show: show, getJSON: getJSON, withBusy: withBusy };
})();

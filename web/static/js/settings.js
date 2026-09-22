// freegate settings — client API key management.
// Shared helpers (esc, show, getJSON, withBusy) come from ui.js via FG.
(function () {
  'use strict';

  var esc = window.FG.esc;
  var show = window.FG.show;
  var getJSON = window.FG.getJSON;
  var withBusy = window.FG.withBusy;

  // Secrets are stored reversibly so they can be revealed again later.
  // List/get responses never carry them — only the per-key reveal path
  // returns the raw value, shown in the banner with a Copy button.
  var keyTable = document.getElementById('key-table');
  var keyErr = document.getElementById('key-err');
  var keyCreated = document.getElementById('key-created');
  var keyForm = document.getElementById('key-form');
  var keysCache = {};

  function keyLastUsed(k) {
    if (!k.last_used_at) return '—';
    var d = new Date(k.last_used_at);
    if (isNaN(d.getTime())) return '—';
    return d.toISOString().slice(0, 10);
  }

  function renderKeys(rows) {
    keysCache = {};
    if (!rows.length) {
      keyTable.innerHTML = '<tr><td colspan="6" class="px-5 py-8 text-center text-body text-mid-gray">// no client keys yet — use "New key"</td></tr>';
      return;
    }
    keyTable.innerHTML = rows.map(function (k) {
      keysCache[k.id] = k;
      return '<tr class="border-b border-hairline last:border-0 hover:bg-surface-alt">' +
        '<td class="px-5 py-3 font-mono text-caption">' + esc(k.name) + '</td>' +
        '<td class="px-5 py-3 font-mono text-caption text-mid-gray">' + esc(k.prefix || '—') + '</td>' +
        '<td class="px-5 py-3 tabular-nums text-caption text-mid-gray">' + (k.use_count || 0) + '</td>' +
        '<td class="px-5 py-3 text-caption text-mid-gray">' + keyLastUsed(k) + '</td>' +
        '<td class="px-5 py-3"><span class="inline-flex items-center rounded-2xl px-2 py-0.5 text-caption font-medium ' +
          (k.enabled ? 'bg-ink-soft text-surface-alt">on' : 'bg-canvas text-mid-gray">off') + '</span></td>' +
        '<td class="px-5 py-3"><span class="flex flex-wrap gap-2">' +
          '<button class="key-show rounded-2xl border border-hairline bg-paper px-3 py-1.5 text-caption font-medium text-ink hover:bg-canvas" type="button" data-id="' + k.id + '">Show</button>' +
          '<button class="key-edit rounded-2xl border border-hairline bg-paper px-3 py-1.5 text-caption font-medium text-ink hover:bg-canvas" type="button" data-id="' + k.id + '">Edit</button>' +
          '<button class="key-toggle rounded-2xl border border-hairline bg-paper px-3 py-1.5 text-caption font-medium text-ink hover:bg-canvas" type="button" data-id="' + k.id + '">' + (k.enabled ? 'Disable' : 'Enable') + '</button>' +
          '<button class="key-delete rounded-2xl px-3 py-1.5 text-caption font-medium text-ember hover:bg-canvas" type="button" data-id="' + k.id + '">Delete</button>' +
        '</span></td></tr>';
    }).join('');
  }

  function loadKeys() {
    return getJSON('/api/api-keys')
      .then(function (body) { renderKeys(body.data || body || []); })
      .catch(function (e) {
        keyTable.innerHTML = '<tr><td colspan="6" class="px-5 py-8 text-center text-body text-mid-gray">Load failed — ' + esc(e.message) + '</td></tr>';
        show(keyErr, 'load keys: ' + e.message);
      });
  }

  function resetKeyForm() {
    document.getElementById('kf-id').value = '';
    document.getElementById('kf-name').value = '';
    document.getElementById('kf-enabled').checked = true;
    keyForm.classList.add('hidden');
    keyForm.classList.remove('grid');
  }

  document.getElementById('key-new').addEventListener('click', function () {
    resetKeyForm();
    keyForm.classList.remove('hidden');
    keyForm.classList.add('grid');
    document.getElementById('kf-name').focus();
  });
  document.getElementById('key-cancel').addEventListener('click', resetKeyForm);

  document.getElementById('key-form').addEventListener('submit', function (e) {
    e.preventDefault();
    var id = document.getElementById('kf-id').value;
    var payload = {
      name: document.getElementById('kf-name').value.trim(),
      enabled: document.getElementById('kf-enabled').checked,
    };
    var method = id ? 'PUT' : 'POST';
    var url = id ? '/api/api-keys/' + id : '/api/api-keys';
    withBusy(document.getElementById('key-save'), 'Saving…', function () {
      return getJSON(url, { method: method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) })
        .then(function (b) {
          resetKeyForm();
          if (!id && b && b.api_key) {
            showCreatedKey(b.name, b.api_key);
          }
          loadKeys();
        });
    }).catch(function (e2) { show(keyErr, 'save key: ' + e2.message); });
  });

  // Secret banner with a Copy button. The listener is delegated on the
  // static container so it survives innerHTML rewrites. Shows a fresh
  // secret after create and revealed secrets via Show — both copyable.
  var createdKey = '';

  function showCreatedKey(name, raw, isReveal) {
    createdKey = raw;
    var lead = isReveal ? 'Key for <strong>' + esc(name) + '</strong>:' : 'New key for <strong>' + esc(name) + '</strong> — copy now:';
    keyCreated.innerHTML = lead + '<br>' +
      '<span class="mt-2 flex items-center gap-2"><code id="key-created-secret" class="select-all min-w-0 flex-1 break-all">' + esc(raw) + '</code>' +
      '<button id="key-copy" type="button" class="shrink-0 rounded-2xl border border-hairline bg-paper px-3 py-1.5 text-caption font-medium text-ink transition-colors hover:bg-canvas">Copy</button></span>';
    keyCreated.classList.remove('hidden');
  }

  keyCreated.addEventListener('click', function (e) {
    if (!e.target || e.target.id !== 'key-copy' || !createdKey) return;
    var btn = e.target;
    var done = function (ok) {
      btn.textContent = ok ? 'Copied' : 'Select + ⌘C';
      setTimeout(function () { btn.textContent = 'Copy'; }, 2000);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(createdKey).then(function () { done(true); }, function () { done(false); });
    } else {
      // Fallback for non-secure contexts (plain http on LAN): select the
      // code so the user copies with one keystroke.
      var code = document.getElementById('key-created-secret');
      if (code && window.getSelection) {
        var range = document.createRange();
        range.selectNodeContents(code);
        var sel = window.getSelection();
        sel.removeAllRanges();
        sel.addRange(range);
      }
      done(false);
    }
  });

  keyTable.addEventListener('click', function (e) {
    var shw = e.target.closest('.key-show');
    var edt = e.target.closest('.key-edit');
    var tog = e.target.closest('.key-toggle');
    var del = e.target.closest('.key-delete');
    if (shw) {
      var sk = keysCache[shw.dataset.id];
      if (!sk) return;
      withBusy(shw, 'Showing…', function () {
        return getJSON('/api/api-keys/' + shw.dataset.id + '/reveal', { method: 'POST' })
          .then(function (b) { showCreatedKey(sk.name, b.api_key, true); });
      }).catch(function (e2) { show(keyErr, 'reveal key: ' + e2.message); });
    } else if (edt) {
      var k = keysCache[edt.dataset.id];
      if (!k) return;
      document.getElementById('kf-id').value = k.id;
      document.getElementById('kf-name').value = k.name || '';
      document.getElementById('kf-enabled').checked = k.enabled !== false;
      keyForm.classList.remove('hidden');
      keyForm.classList.add('grid');
      document.getElementById('kf-name').focus();
    } else if (tog) {
      var cur = keysCache[tog.dataset.id];
      if (!cur) return;
      withBusy(tog, 'Saving…', function () {
        return getJSON('/api/api-keys/' + tog.dataset.id, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ name: cur.name, enabled: !cur.enabled }),
        }).then(loadKeys);
      }).catch(function (e2) { show(keyErr, 'toggle key: ' + e2.message); });
    } else if (del) {
      if (!confirm('Delete API key ' + del.dataset.id + '? Clients using it will be rejected immediately.')) return;
      withBusy(del, 'Deleting…', function () {
        return getJSON('/api/api-keys/' + del.dataset.id, { method: 'DELETE' }).then(loadKeys);
      }).catch(function (e2) { show(keyErr, 'delete key: ' + e2.message); });
    }
  });

  loadKeys();
})();

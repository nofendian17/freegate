// freegate providers — Alpine store + JSON admin API client.
// Tables render client-side (the /api/* endpoints already return JSON);
// Alpine owns modal/editor visibility and form state.
(function () {
  'use strict';

  // ----- helpers -----
  function esc(s) {
    return String(s == null ? '' : s).replace(/[&<>"]/g, function (c) {
      return { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;' }[c];
    });
  }

  // Error/output boxes keep their own margin in markup (#provider-err and
  // the combo boxes use mx-5 mt-4, editor boxes mb-4). show() only toggles
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

  function lines(id) {
    var el = document.getElementById(id);
    if (!el) return [];
    return el.value.split('\n').map(function (s) { return s.trim(); }).filter(Boolean);
  }

  // Lines without "=" (or with a blank key) are dropped from the map —
  // surface that instead of failing silently. Warns but does not block.
  function parseHeaders() {
    var out = {};
    var dropped = 0;
    lines('f-headers').forEach(function (l) {
      var i = l.indexOf('=');
      if (i > 0) out[l.slice(0, i).trim()] = l.slice(i + 1).trim();
      else dropped++;
    });
    if (dropped) show(providerFormErr, 'headers: ' + dropped + ' line(s) without "=" were ignored');
    return out;
  }

  // Human-readable Test/probe result; raw JSON is too noisy to read.
  function testSummary(b) {
    if (!b) return 'no response';
    if (!b.ok) return 'fail — ' + (b.error || ('status ' + b.status));
    var n = b.modelCount != null ? b.modelCount : (b.models || []).length;
    return 'ok — ' + n + ' models' + (b.latencyMs != null ? ' · ' + b.latencyMs + 'ms' : '');
  }

  function applyModelFilter() {
    var q = document.getElementById('f-models-filter').value.trim().toLowerCase();
    document.querySelectorAll('#f-models label').forEach(function (lab) {
      lab.classList.toggle('hidden', !!q && lab.textContent.toLowerCase().indexOf(q) < 0);
    });
  }

  // ----- alpine store -----
  document.addEventListener('alpine:init', function () {
    Alpine.store('ui', {
      providerEditor: false,
      poolModal: false,
    });
  });

  // ----- providers -----
  var providerTable = document.getElementById('provider-table');
  var providerErr = document.getElementById('provider-err');
  var providerOk = document.getElementById('provider-ok');
  var providerFormErr = document.getElementById('provider-form-err');
  var providerTestOut = document.getElementById('provider-test-out');
  var editor = document.getElementById('provider-modal');
  var providersCache = [];
  // null = legacy row (whole catalog routes until first save),
  // [] = explicit empty, undefined = new provider.
  var editingLegacyModels;
  var lastTrigger = null;

  function openEditor() { Alpine.store('ui').providerEditor = true; }
  function closeEditor() {
    Alpine.store('ui').providerEditor = false;
    if (lastTrigger && typeof lastTrigger.focus === 'function') lastTrigger.focus();
    lastTrigger = null;
  }

  // After save/delete the table re-renders, so the old trigger node is
  // stale: refocus the row's Edit button, else the New provider button.
  function restoreProviderFocus(id) {
    var btn = id ? providerTable.querySelector('.prov-edit[data-id="' + id + '"]') : null;
    (btn || document.getElementById('provider-new')).focus();
  }

  function providerModelsCell(p) {
    if (p.models == null) {
      return '<span class="inline-flex items-center rounded-2xl bg-canvas px-2 py-0.5 text-caption font-medium text-ink-soft">all — legacy</span>';
    }
    var n = (p.models || []).length;
    return n ? String(n) : '<span class="text-mid-gray">0 — no route</span>';
  }

  function renderProviders(rows) {
    providersCache = rows;
    if (!rows.length) {
      providerTable.innerHTML = '<tr><td colspan="6" class="px-5 py-8 text-center text-body text-mid-gray">// no custom providers yet — use "New provider"</td></tr>';
      return;
    }
    providerTable.innerHTML = rows.map(function (p) {
      return '<tr class="border-b border-hairline last:border-0 hover:bg-surface-alt">' +
        '<td class="px-5 py-3 font-mono text-caption">' + esc(p.name) + '</td>' +
        '<td class="max-w-64 truncate px-5 py-3 font-mono text-caption text-mid-gray">' + esc(p.base_url) + '</td>' +
        '<td class="px-5 py-3 text-caption text-mid-gray">' + providerModelsCell(p) + '</td>' +
        '<td class="px-5 py-3 tabular-nums text-caption text-mid-gray">' + (p.priority || 0) + '</td>' +
        '<td class="px-5 py-3"><span class="inline-flex items-center rounded-2xl px-2 py-0.5 text-caption font-medium ' +
          (p.enabled ? 'bg-ink-soft text-surface-alt">on' : 'bg-canvas text-mid-gray">off') + '</span></td>' +
        '<td class="px-5 py-3"><button class="prov-edit rounded-2xl border border-hairline bg-paper px-3 py-1.5 text-caption font-medium text-ink transition-colors hover:bg-canvas" type="button" data-id="' + p.id + '">Edit</button></td>' +
        '</tr>';
    }).join('');
  }

  function loadProviders() {
    return getJSON('/api/providers')
      .then(function (body) { renderProviders(body.data || body || []); })
      .catch(function (e) {
        providerTable.innerHTML = '<tr><td colspan="6" class="px-5 py-8 text-center text-body text-mid-gray">Load failed — ' + esc(e.message) + '</td></tr>';
        show(providerErr, 'load providers: ' + e.message);
      });
  }

  // Model checklist: `saved` is the stored selection (checked), `probe` the
  // catalog returned by Test (unchecked unless already saved).
  function renderModelChecks(saved, probe) {
    var box = document.getElementById('f-models');
    var keep = {};
    Array.prototype.forEach.call(box.querySelectorAll('input[type=checkbox]:checked'), function (c) { keep[c.value] = true; });
    (saved || []).forEach(function (id) { keep[id] = true; });
    var ids = [];
    (saved || []).concat(probe || []).forEach(function (id) {
      if (id && ids.indexOf(id) < 0) ids.push(id);
    });
    if (!ids.length) {
      box.innerHTML = '<span>Run Test to load the model list from the upstream.</span>';
      document.getElementById('f-models-filter').hidden = true;
      return;
    }
    document.getElementById('f-models-filter').hidden = false;
    box.innerHTML = ids.map(function (id) {
      return '<label class="flex items-center gap-2 font-mono"><input type="checkbox" class="size-4 accent-[#0a0a0a]" value="' + esc(id) + '"' +
        (keep[id] ? ' checked' : '') + '> ' + esc(id) + '</label>';
    }).join('');
    applyModelFilter();
  }

  function checkedModels() {
    return Array.prototype.map.call(
      document.getElementById('f-models').querySelectorAll('input[type=checkbox]:checked'),
      function (c) { return c.value; });
  }

  function openProviderModal(p) {
    p = p || {};
    document.getElementById('f-id').value = p.id || '';
    document.getElementById('f-name').value = p.name || '';
    document.getElementById('f-base-url').value = p.base_url || '';
    document.getElementById('f-api-keys').value = '';
    document.getElementById('f-headers').value = Object.entries(p.headers || {}).map(function (kv) { return kv[0] + '=' + kv[1]; }).join('\n');
    editingLegacyModels = ('models' in p) ? p.models : undefined;
    document.getElementById('f-legacy').classList.toggle('hidden', editingLegacyModels !== null);
    document.getElementById('f-api-keys-label').textContent = p.id
      ? 'API keys — one per line (blank keeps existing keys)'
      : 'API keys — one per line';
    document.getElementById('f-models-filter').value = '';
    renderModelChecks(p.models || [], null);
    document.getElementById('f-refresh').value = p.refresh_sec != null ? p.refresh_sec : 60;
    document.getElementById('f-priority').value = p.priority || 0;
    document.getElementById('f-enabled').checked = p.enabled !== false;
    document.getElementById('provider-delete').classList.toggle('hidden', !p.id);
    document.getElementById('provider-modal-title').textContent = p.id ? 'Edit provider' : 'New provider';
    show(providerFormErr, '');
    show(providerTestOut, '');
    openEditor();
    var name = document.getElementById('f-name');
    if (name) name.focus();
    if (editor) editor.scrollIntoView({ block: 'nearest' });
  }

  document.getElementById('provider-new').addEventListener('click', function () { lastTrigger = this; openProviderModal(); });
  document.getElementById('provider-modal-close').addEventListener('click', closeEditor);
  document.getElementById('f-models-filter').addEventListener('input', applyModelFilter);

  if (providerTable) {
    providerTable.addEventListener('click', function (e) {
      var btn = e.target.closest('.prov-edit');
      if (!btn || btn.disabled) return;
      lastTrigger = btn;
      withBusy(btn, 'Loading…', function () {
        return getJSON('/api/providers/' + btn.dataset.id).then(function (body) {
          openProviderModal(body.data || body);
        });
      }).catch(function (e2) { show(providerErr, 'load provider: ' + e2.message); });
    });
  }

  document.getElementById('provider-form').addEventListener('submit', function (e) {
    e.preventDefault();
    var models = checkedModels();
    if (!document.getElementById('f-id').value && !models.length) {
      show(providerFormErr, 'no models selected — run Test, check the models to route, then Save');
      return;
    }
    var saveBtn = document.getElementById('provider-save');
    var id = document.getElementById('f-id').value;
    var payload = {
      name: document.getElementById('f-name').value.trim(),
      base_url: document.getElementById('f-base-url').value.trim(),
      api_keys: lines('f-api-keys'),
      headers: parseHeaders(),
      models: models,
      refresh_sec: parseInt(document.getElementById('f-refresh').value, 10) || 0,
      priority: parseInt(document.getElementById('f-priority').value, 10) || 0,
      enabled: document.getElementById('f-enabled').checked,
    };
    var method = id ? 'PUT' : 'POST';
    var url = id ? '/api/providers/' + id : '/api/providers';
    withBusy(saveBtn, 'Saving…', function () {
      return getJSON(url, { method: method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) })
        .then(function () {
          closeEditor();
          loadTierEditor();
          return loadProviders().then(function () { restoreProviderFocus(id || null); });
        });
    }).catch(function (e2) { show(providerFormErr, 'save: ' + e2.message); });
  });

  document.getElementById('provider-test').addEventListener('click', function () {
    var id = document.getElementById('f-id').value;
    var url, opts;
    if (id) {
      url = '/api/providers/' + id + '/test';
      opts = { method: 'POST' };
    } else {
      // No id yet: probe the form values ad-hoc (stores nothing).
      url = '/api/providers/probe';
      opts = {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({
          base_url: document.getElementById('f-base-url').value.trim(),
          api_keys: lines('f-api-keys'),
          headers: parseHeaders(),
        }),
      };
    }
    withBusy(this, 'Testing…', function () {
      return getJSON(url, opts).then(function (b) {
        var probed = (b && b.models) || [];
        // New provider: everything is unchecked, so pre-check the catalog
        // (same as legacy) — saving a fresh probe with nothing checked is
        // almost never intended.
        var fresh = editingLegacyModels === null || editingLegacyModels === undefined;
        renderModelChecks(fresh ? probed : checkedModels(), probed);
        show(providerTestOut, testSummary(b), !!(b && b.ok));
        if (!(b && b.ok)) show(providerFormErr, testSummary(b));
        else show(providerFormErr, '');
      });
    }).catch(function (e) { show(providerTestOut, 'test: ' + e.message); });
  });

  document.getElementById('provider-delete').addEventListener('click', function () {
    var id = document.getElementById('f-id').value;
    if (!id) return;
    var name = document.getElementById('f-name').value.trim();
    if (!confirm('Delete provider "' + name + '"? Combos using it will lose that tier.')) return;
    withBusy(this, 'Deleting…', function () {
      return getJSON('/api/providers/' + id, { method: 'DELETE' }).then(function () {
        closeEditor();
        loadTierEditor();
        return loadProviders().then(function () { restoreProviderFocus(null); });
      });
    }).catch(function (e) { show(providerFormErr, 'delete: ' + e.message); });
  });

  // ----- pools -----
  var poolTable = document.getElementById('pool-table');
  var poolErr = document.getElementById('pool-err');
  var poolOk = document.getElementById('pool-ok');
  var poolFormErr = document.getElementById('pool-form-err');
  var poolDeployOut = document.getElementById('pool-deploy-out');
  var poolLastTrigger = null;

  function renderPools(rows) {
    if (!rows.length) {
      poolTable.innerHTML = '<tr><td colspan="5" class="px-5 py-8 text-center text-body text-mid-gray">// no proxy pools yet — use "New pool"</td></tr>';
      return;
    }
    poolTable.innerHTML = rows.map(function (p) {
      var status = p.test_status ? p.test_status : '—';
      return '<tr class="border-b border-hairline last:border-0 hover:bg-surface-alt">' +
        '<td class="px-5 py-3 font-mono text-caption">' + esc(p.name) + '</td>' +
        '<td class="max-w-64 truncate px-5 py-3 font-mono text-caption text-mid-gray">' + esc(p.proxy_url) + '</td>' +
        '<td class="px-5 py-3 text-caption ' + (p.test_status === 'error' ? 'text-ember' : 'text-mid-gray') + '">' + esc(status) + '</td>' +
        '<td class="px-5 py-3"><span class="inline-flex items-center rounded-2xl px-2 py-0.5 text-caption font-medium ' +
          (p.enabled ? 'bg-ink-soft text-surface-alt">on' : 'bg-canvas text-mid-gray">off') + '</span></td>' +
        '<td class="px-5 py-3"><span class="flex flex-wrap gap-2">' +
          '<button class="pool-edit rounded-2xl border border-hairline bg-paper px-3 py-1.5 text-caption font-medium text-ink hover:bg-canvas" type="button" data-id="' + p.id + '">Edit</button>' +
          '<button class="pool-test rounded-2xl border border-hairline bg-paper px-3 py-1.5 text-caption font-medium text-ink hover:bg-canvas" type="button" data-id="' + p.id + '">Test</button>' +
          '<button class="pool-delete rounded-2xl px-3 py-1.5 text-caption font-medium text-ember hover:bg-canvas" type="button" data-id="' + p.id + '">Delete</button>' +
        '</span></td></tr>';
    }).join('');
  }

  function loadPools() {
    return getJSON('/api/pools')
      .then(function (body) { renderPools(body.data || body || []); })
      .catch(function (e) {
        poolTable.innerHTML = '<tr><td colspan="5" class="px-5 py-8 text-center text-body text-mid-gray">Load failed — ' + esc(e.message) + '</td></tr>';
        show(poolErr, 'load pools: ' + e.message);
      });
  }

  function openPoolModal(p, deployMode) {
    p = p || {};
    if (!Alpine.store('ui').poolModal) poolLastTrigger = document.activeElement;
    document.getElementById('pf-id').value = p.id || '';
    document.getElementById('pf-name').value = p.name || '';
    document.getElementById('pf-proxy-url').value = p.proxy_url || '';
    document.getElementById('pf-no-proxy').value = p.no_proxy || '';
    document.getElementById('pf-enabled').checked = p.enabled !== false;
    document.getElementById('pf-strict').checked = !!p.strict_proxy;
    document.getElementById('pf-token').value = '';
    document.getElementById('pf-token-wrap').classList.toggle('hidden', !deployMode);
    document.getElementById('pf-token-wrap').classList.toggle('flex', !!deployMode);
    document.getElementById('pf-proxy-url-wrap').classList.toggle('hidden', !!deployMode);
    document.getElementById('pf-name').required = !deployMode;
    document.getElementById('pf-proxy-url').required = !deployMode;
    document.getElementById('pf-name-hint').classList.toggle('hidden', !deployMode);
    document.getElementById('pf-explainer').classList.toggle('hidden', !deployMode);
    document.getElementById('pool-deploy-go').classList.toggle('hidden', !deployMode);
    document.getElementById('pool-save').classList.toggle('hidden', !!deployMode);
    document.getElementById('pool-delete').classList.toggle('hidden', !p.id || !!deployMode);
    document.getElementById('pool-modal-title').textContent = deployMode ? 'Deploy relay to Vercel' : (p.id ? 'Edit pool' : 'New pool');
    show(poolFormErr, '');
    show(poolDeployOut, '');
    Alpine.store('ui').poolModal = true;
    document.getElementById('pool-modal-close').focus();
  }

  function closePoolModal() {
    Alpine.store('ui').poolModal = false;
    if (poolLastTrigger && typeof poolLastTrigger.focus === 'function') poolLastTrigger.focus();
    poolLastTrigger = null;
  }

  document.getElementById('pool-modal-close').addEventListener('click', closePoolModal);
  document.getElementById('pool-new').addEventListener('click', function () { openPoolModal({}, false); });
  document.getElementById('pool-deploy').addEventListener('click', function () { openPoolModal({}, true); });

  document.getElementById('pool-form').addEventListener('submit', function (e) {
    e.preventDefault();
    var id = document.getElementById('pf-id').value;
    var rawName = document.getElementById('pf-name').value.trim();
    var slug = rawName.toLowerCase().replace(/[^a-z0-9]+/g, '-').replace(/^-+|-+$/g, '').slice(0, 64);
    if (!slug) { show(poolFormErr, 'name must contain a-z0-9'); return; }
    var payload = {
      name: slug,
      proxy_url: document.getElementById('pf-proxy-url').value.trim(),
      no_proxy: document.getElementById('pf-no-proxy').value.trim(),
      enabled: document.getElementById('pf-enabled').checked,
      strict_proxy: document.getElementById('pf-strict').checked,
    };
    var method = id ? 'PUT' : 'POST';
    var url = id ? '/api/pools/' + id : '/api/pools';
    withBusy(document.getElementById('pool-save'), 'Saving…', function () {
      return getJSON(url, { method: method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) })
        .then(function () { closePoolModal(); loadPools(); });
    }).catch(function (e2) { show(poolFormErr, 'save pool: ' + e2.message); });
  });

  document.getElementById('pool-deploy-go').addEventListener('click', function () {
    var btn = this;
    var token = document.getElementById('pf-token').value.trim();
    var name = document.getElementById('pf-name').value.trim();
    if (!token) { show(poolFormErr, 'deploy: Vercel token is required'); return; }
    if (!name) { show(poolFormErr, 'deploy: project name is required'); return; }
    show(poolFormErr, '');
    show(poolDeployOut, 'Deploying to Vercel… (up to ~2 min)', true);
    withBusy(btn, 'Deploying…', function () {
      return getJSON('/api/pools/vercel-deploy', {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ vercel_token: token, project_name: name }),
      }).then(function (b) {
        document.getElementById('pf-token').value = '';
        var url = (b && (b.deploy_url || (b.pool && b.pool.proxy_url))) || '';
        show(poolDeployOut, 'Deployed: ' + url, true);
        loadPools();
      });
    }).catch(function (e) { show(poolDeployOut, 'deploy: ' + e.message); });
  });

  document.getElementById('pool-delete').addEventListener('click', function () {
    var id = document.getElementById('pf-id').value;
    if (!id) return;
    if (!confirm('Delete pool ' + id + '?')) return;
    withBusy(this, 'Deleting…', function () {
      return getJSON('/api/pools/' + id, { method: 'DELETE' }).then(function () {
        closePoolModal();
        loadPools();
      });
    }).catch(function (e) { show(poolFormErr, 'delete pool: ' + e.message); });
  });

  poolTable.addEventListener('click', function (e) {
    var edt = e.target.closest('.pool-edit');
    var tst = e.target.closest('.pool-test');
    var del = e.target.closest('.pool-delete');
    if (edt) {
      withBusy(edt, 'Loading…', function () {
        return getJSON('/api/pools/' + edt.dataset.id).then(function (body) {
          openPoolModal(body.data || body, false);
        });
      }).catch(function (e2) { show(poolErr, 'load pool: ' + e2.message); });
    } else if (tst) {
      withBusy(tst, 'Testing…', function () {
        return getJSON('/api/pools/' + tst.dataset.id + '/test', { method: 'POST' })
          .then(function (b) {
            if (b.ok) show(poolOk, 'ok ' + JSON.stringify(b), true);
            else show(poolErr, 'fail ' + JSON.stringify(b));
            loadPools();
          });
      }).catch(function (e2) { show(poolErr, 'test pool: ' + e2.message); });
    } else if (del) {
      if (!confirm('Delete pool ' + del.dataset.id + '?')) return;
      withBusy(del, 'Deleting…', function () {
        return getJSON('/api/pools/' + del.dataset.id, { method: 'DELETE' }).then(loadPools);
      }).catch(function (e2) { show(poolErr, 'delete pool: ' + e2.message); });
    }
  });

  // ----- combos -----
  // Errors surface in #combo-err (role=alert); #provider-err-combo is a
  // hidden alias kept for older test pins.
  var comboErr = document.getElementById('combo-err');
  var comboTestOut = document.getElementById('combo-test-out');
  var comboForm = document.getElementById('combo-form');
  var comboCancelBtn = document.getElementById('combo-cancel');
  var comboProviders = ['opencode', 'kilo', 'llm7'];
  var combosCache = {};

  function addTierRow(provider, model) {
    var wrap = document.getElementById('combo-tiers');
    if (provider && comboProviders.indexOf(provider) < 0) comboProviders.push(provider);
    var row = document.createElement('div');
    row.className = 'combo-row flex flex-wrap items-center gap-2';
    row.innerHTML =
      '<select class="combo-tier-provider rounded-2xl border border-hairline bg-canvas px-3 py-2 text-caption text-ink" name="tier_provider" aria-label="Tier provider">' +
        comboProviders.map(function (n) {
          return '<option value="' + esc(n) + '"' + (n === provider ? ' selected' : '') + '>' + esc(n) + '</option>';
        }).join('') +
      '</select>' +
      '<input class="combo-tier-model rounded-2xl border border-hairline bg-canvas px-3 py-2 font-mono text-caption text-ink placeholder:text-mid-gray" name="tier_model" placeholder="model (required)" value="' + esc(model || '') + '" aria-label="Tier model">' +
      '<button class="tier-up rounded-2xl px-3 py-1.5 text-caption text-mid-gray hover:bg-canvas hover:text-ink" type="button" aria-label="Move tier up">Up</button>' +
      '<button class="tier-down rounded-2xl px-3 py-1.5 text-caption text-mid-gray hover:bg-canvas hover:text-ink" type="button" aria-label="Move tier down">Down</button>' +
      '<button class="tier-remove rounded-2xl px-3 py-1.5 text-caption text-ember hover:bg-canvas" type="button" aria-label="Remove tier">Remove</button>';
    wrap.appendChild(row);
  }

  function loadTierEditor() {
    return getJSON('/api/providers')
      .then(function (body) {
        var rows = body.data || body || [];
        comboProviders = ['opencode', 'kilo', 'llm7'];
        rows.forEach(function (p) { comboProviders.push('custom:' + p.name); });
        var wrap = document.getElementById('combo-tiers');
        var kept = [];
        wrap.querySelectorAll('.combo-row').forEach(function (row) {
          kept.push({
            provider: row.querySelector('.combo-tier-provider').value,
            model: row.querySelector('.combo-tier-model').value,
          });
        });
        wrap.innerHTML = '';
        if (!kept.length) kept.push({ provider: 'opencode', model: '' });
        kept.forEach(function (t) { addTierRow(t.provider, t.model); });
      })
      .catch(function (e) { show(comboErr, 'load tier providers: ' + e.message); });
  }

  function tierLabel(t) {
    return t.model ? t.provider + ':' + t.model : t.provider;
  }

  function loadCombos() {
    return getJSON('/api/combos')
      .then(function (body) {
        var rows = body.data || body || [];
        combosCache = {};
        var list = document.getElementById('combo-list');
        if (!rows.length) {
          list.innerHTML = '<p class="p-5 text-body text-mid-gray">// no combos yet — use "New combo"</p>';
          return;
        }
        list.innerHTML = rows.map(function (c) {
          combosCache[c.id] = c;
          var tiers = (c.tiers || []).map(tierLabel).join(' → ');
          return '<div class="combo-row flex flex-wrap items-center gap-3 px-5 py-4" data-id="' + c.id + '">' +
            '<strong class="font-mono text-body text-ink">' + esc(c.name) + '</strong>' +
            '<span class="min-w-0 flex-1 truncate font-mono text-caption text-mid-gray">' + esc(tiers) + '</span>' +
            '<button class="combo-test rounded-2xl border border-hairline bg-paper px-3 py-1.5 text-caption font-medium text-ink hover:bg-canvas" type="button" data-id="' + c.id + '">Test</button>' +
            '<button class="combo-edit rounded-2xl border border-hairline bg-paper px-3 py-1.5 text-caption font-medium text-ink hover:bg-canvas" type="button" data-id="' + c.id + '">Edit</button>' +
            '<button class="combo-delete rounded-2xl px-3 py-1.5 text-caption font-medium text-ember hover:bg-canvas" type="button" data-id="' + c.id + '">Delete</button>' +
            '</div>';
        }).join('');
      })
      .catch(function (e) { show(comboErr, 'load combos: ' + e.message); });
  }

  function resetComboForm() {
    document.getElementById('combo-id').value = '';
    document.getElementById('combo-name').value = '';
    document.getElementById('combo-tiers').innerHTML = '';
    addTierRow('opencode', '');
    comboCancelBtn.hidden = true;
    comboForm.classList.add('hidden');
    comboForm.classList.remove('grid');
    show(comboTestOut, '');
  }

  document.getElementById('combo-new').addEventListener('click', function () {
    lastTrigger = this;
    resetComboForm();
    comboForm.classList.remove('hidden');
    comboForm.classList.add('grid');
    document.getElementById('combo-name').focus();
  });
  comboCancelBtn.addEventListener('click', resetComboForm);
  document.getElementById('combo-add-tier').addEventListener('click', function () { addTierRow('opencode', ''); });

  document.getElementById('combo-tiers').addEventListener('click', function (e) {
    var row = e.target.closest('.combo-row');
    if (!row) return;
    if (e.target.closest('.tier-remove')) {
      row.remove();
      if (!document.querySelectorAll('#combo-tiers .combo-row').length) addTierRow('opencode', '');
    } else if (e.target.closest('.tier-up')) {
      var prev = row.previousElementSibling;
      if (prev) row.parentNode.insertBefore(row, prev);
    } else if (e.target.closest('.tier-down')) {
      var next = row.nextElementSibling;
      if (next) row.parentNode.insertBefore(next, row);
    }
  });

  document.getElementById('combo-form').addEventListener('submit', function (e) {
    e.preventDefault();
    var tiers = [];
    document.querySelectorAll('#combo-tiers .combo-row').forEach(function (row) {
      var t = { provider: row.querySelector('.combo-tier-provider').value };
      var m = row.querySelector('.combo-tier-model').value.trim();
      if (m) t.model = m;
      tiers.push(t);
    });
    var id = document.getElementById('combo-id').value;
    var payload = { name: document.getElementById('combo-name').value.trim(), tiers: tiers };
    var method = id ? 'PUT' : 'POST';
    var url = id ? '/api/combos/' + id : '/api/combos';
    withBusy(document.getElementById('combo-save'), 'Saving…', function () {
      return getJSON(url, { method: method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(payload) })
        .then(function () { resetComboForm(); loadCombos(); });
    }).catch(function (e2) { show(comboErr, 'save combo: ' + e2.message); });
  });

  document.getElementById('combo-list').addEventListener('click', function (e) {
    var tst = e.target.closest('.combo-test');
    var edt = e.target.closest('.combo-edit');
    var del = e.target.closest('.combo-delete');
    if (tst) {
      withBusy(tst, 'Testing…', function () {
        return getJSON('/api/combos/' + tst.dataset.id + '/test', { method: 'POST' }).then(function (b) {
          var rows = (b.tiers || []).map(function (t) {
            var st = t.ok ? 'ok' : 'fail';
            var lat = t.latencyMs != null ? ' ' + t.latencyMs + 'ms' : '';
            var why = !t.ok && t.error ? ' ' + t.error : '';
            return t.provider + ': ' + st + lat + why;
          });
          if (b.ok) show(comboTestOut, 'ok\n' + rows.join('\n'), true);
          else show(comboErr, 'fail\n' + rows.join('\n'));
        });
      }).catch(function (e2) { show(comboTestOut, 'test combo: ' + e2.message); });
    } else if (edt) {
      var c = combosCache[edt.dataset.id];
      if (!c) return;
      lastTrigger = edt;
      document.getElementById('combo-id').value = c.id;
      document.getElementById('combo-name').value = c.name || '';
      var wrap = document.getElementById('combo-tiers');
      wrap.innerHTML = '';
      (c.tiers || []).forEach(function (t) { addTierRow(t.provider, t.model); });
      if (!(c.tiers || []).length) addTierRow('opencode', '');
      comboForm.classList.remove('hidden');
      comboForm.classList.add('grid');
      comboCancelBtn.hidden = false;
      document.getElementById('combo-name').focus();
    } else if (del) {
      if (!confirm('Delete combo ' + del.dataset.id + '?')) return;
      withBusy(del, 'Deleting…', function () {
        return getJSON('/api/combos/' + del.dataset.id, { method: 'DELETE' }).then(loadCombos);
      }).catch(function (e2) { show(comboErr, 'delete combo: ' + e2.message); });
    }
  });

  document.addEventListener('keydown', function (e) {
    if (e.key === 'Escape' && Alpine.store('ui').providerEditor) {
      closeEditor();
    }
  });

  loadProviders();
  loadPools();
  loadCombos();
  loadTierEditor();
})();

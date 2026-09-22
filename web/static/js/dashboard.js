// freegate dashboard — Alpine components + Chart.js timeseries polling.
// Data flows: HTMX polls the partial endpoints; Alpine handles modals,
// model testing, and the health badge.
(function () {
  'use strict';

  // ----- Shared modal state (Alpine global store, bound in modals.html) -----
  document.addEventListener('alpine:init', function () {
    Alpine.store('modal', {
      error: { open: false, text: '' },
      test: { open: false, title: 'Model test', detail: '', tone: '' },
    });

    Alpine.data('dashboard', function () {
      return {
        count: 0,
        init: function () {
          this.syncCount();
          localizeTimes();
          // HTMX swaps #requests-body every 5s; recount and relocalize.
          var self = this;
          document.body.addEventListener('htmx:afterSwap', function (e) {
            if (e.target && e.target.id === 'requests-body') { self.syncCount(); localizeTimes(); }
          });
          this.watchHealth();
        },
        syncCount: function () {
          var tbody = document.getElementById('requests-body');
          if (!tbody) return;
          var rows = tbody.querySelectorAll('tr');
          // The empty-state fragment renders a single colspan row.
          this.count = rows.length === 1 && rows[0].querySelector('td[colspan]') ? 0 : rows.length;
        },
        watchHealth: function () {
          var dot = document.getElementById('status-dot');
          var uptime = document.getElementById('uptime');
          var modelCount = document.getElementById('model-count');
          if (!dot && !uptime && !modelCount) return;
          var poll = function () {
            fetch('/api/health', { credentials: 'same-origin' })
              .then(function (r) { return r.ok ? r.json() : null; })
              .then(function (h) {
                if (!h) return;
                if (dot) dot.className = 'inline-block size-2 rounded-full ' + (h.ok ? 'bg-ink-soft' : 'bg-ember');
                if (uptime && h.uptime) uptime.textContent = h.uptime;
                if (modelCount) modelCount.textContent = h.model_count;
              })
              .catch(function () {});
          };
          poll();
          setInterval(poll, 5000);
        },
      };
    });
  });

  // ----- Local times -----
  // Server renders UTC text with an ISO datetime attr; the browser
  // rewrites it in the viewer's timezone (date + HH:MM:SS, 24h).
  function localizeTimes() {
    var els = document.querySelectorAll('[data-localtime]:not([data-localized])');
    for (var i = 0; i < els.length; i++) {
      var iso = els[i].getAttribute('datetime');
      if (!iso) continue;
      var d = new Date(iso);
      if (isNaN(d.getTime())) continue;
      var p = function (n) { return (n < 10 ? '0' : '') + n; };
      els[i].textContent = d.getFullYear() + '-' + p(d.getMonth() + 1) + '-' + p(d.getDate()) +
        ' ' + p(d.getHours()) + ':' + p(d.getMinutes()) + ':' + p(d.getSeconds());
      els[i].setAttribute('title', iso.replace('T', ' ').replace('Z', ' UTC'));
      els[i].setAttribute('data-localized', '1');
    }
  }
  localizeTimes();

  // ----- Timeseries chart (Chart.js) -----
  function timestamps(data) {
    return data.map(function (d) {
      var dt = new Date(d.ts);
      var p = function (n) { return (n < 10 ? '0' : '') + n; };
      return p(dt.getHours()) + ':' + p(dt.getMinutes()) + ':' + p(dt.getSeconds());
    });
  }

  function initChart() {
    var canvas = document.getElementById('ts-chart');
    if (!canvas || typeof Chart === 'undefined') return;
    var chart = null;
    var refresh = function () {
      fetch('/api/timeseries', { credentials: 'same-origin' })
        .then(function (r) { return r.ok ? r.json() : null; })
        .then(function (data) {
          if (!data || data.length < 2) return;
          var labels = timestamps(data);
          var values = [];
          for (var i = 1; i < data.length; i++) {
            values.push(Math.max(0, (data[i].total_requests - data[i - 1].total_requests) * 6));
          }
          if (chart) {
            chart.data.labels = labels.slice(1);
            chart.data.datasets[0].data = values;
            chart.update('none');
            return;
          }
          chart = new Chart(canvas, {
            type: 'line',
            data: {
              labels: labels.slice(1),
              datasets: [{
                label: 'req/min',
                data: values,
                borderColor: '#0a0a0a',
                backgroundColor: 'rgba(10,10,10,0.04)',
                borderWidth: 1.5,
                fill: true,
                tension: 0,
                pointRadius: 0,
                pointHoverRadius: 3,
                pointHoverBackgroundColor: '#0a0a0a',
              }],
            },
            options: {
              responsive: true,
              maintainAspectRatio: false,
              animation: false,
              plugins: { legend: { display: false } },
              scales: {
                x: {
                  ticks: { color: '#737373', maxTicksLimit: 8, font: { size: 11 } },
                  grid: { color: '#e5e5e5' },
                  border: { color: '#e5e5e5' },
                },
                y: {
                  beginAtZero: true,
                  ticks: { color: '#737373', font: { size: 11 } },
                  grid: { color: '#e5e5e5' },
                  border: { color: '#e5e5e5' },
                },
              },
            },
          });
        })
        .catch(function () {});
    };
    refresh();
    setInterval(refresh, 10000);
  }

  // ----- Model test -----
  // Delegated on document because HTMX swaps #models-body every 10s.
  var testInFlight = false;

  function parseTestResult(status, raw, ms) {
    var lines = ['status: ' + status + ' [' + ms + 'ms]'];
    var ok = status >= 200 && status < 300;
    try {
      var data = JSON.parse(raw);
      if (data.error && data.error.message) {
        lines.push('error: ' + data.error.message);
        ok = false;
      } else if (data.choices && data.choices[0] && data.choices[0].message) {
        var msg = data.choices[0].message;
        var content = msg.content || msg.reasoning || '';
        if (content) lines.push('content: ' + content);
        if (data.usage) {
          lines.push('tokens: ' + (data.usage.total_tokens || '?') +
            ' (in ' + (data.usage.prompt_tokens || 0) + ' / out ' + (data.usage.completion_tokens || 0) + ')');
        }
        if (msg.reasoning && msg.content) lines.push('reasoning: ' + msg.reasoning);
      } else {
        ok = false;
        lines.push('raw: ' + (raw.length > 300 ? raw.slice(0, 300) + '…' : raw));
      }
    } catch (err) {
      ok = false;
      lines.push('parse error: ' + err.message);
      if (raw) lines.push('raw: ' + (raw.length > 300 ? raw.slice(0, 300) + '…' : raw));
    }
    return { text: lines.join('\n'), ok: ok };
  }

  document.addEventListener('click', function (e) {
    var btn = e.target.closest('.test-btn');
    if (!btn || testInFlight) return;
    e.preventDefault();
    testInFlight = true;
    var model = btn.dataset.model;
    var t0 = Date.now();

    var store = window.Alpine && Alpine.store('modal');
    if (store) {
      store.test.title = 'Testing: ' + model;
      store.test.detail = 'Sending completion…';
      store.test.tone = '';
      store.test.open = true;
    } else {
      var titleEl = document.querySelector('#test-title');
      var detailEl = document.querySelector('#test-detail');
      if (titleEl) titleEl.textContent = 'Testing: ' + model;
      if (detailEl) detailEl.textContent = 'Sending completion…';
    }

    fetch('/v1/chat/completions', {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({
        model: model,
        messages: [{ role: 'user', content: 'Reply with exactly: OK' }],
        max_tokens: 20,
        stream: false,
      }),
    })
      .then(function (resp) {
        return resp.text().then(function (text) { return { status: resp.status, body: text }; });
      })
      .then(function (r) {
        var res = parseTestResult(r.status, r.body || '', Date.now() - t0);
        showTestResult(res);
      })
      .catch(function (err) {
        showTestResult({ text: 'network error: ' + (err && err.message ? err.message : String(err)), ok: false });
      })
      .finally(function () { testInFlight = false; });
  });

  function showTestResult(res) {
    var store = window.Alpine && Alpine.store('modal');
    if (store) {
      store.test.detail = res.text;
      store.test.tone = res.ok ? 'text-ink' : 'text-ember';
      return;
    }
    var detailEl = document.querySelector('#test-detail');
    if (detailEl) {
      detailEl.textContent = res.text;
      detailEl.className = 'test-body max-h-96 overflow-auto whitespace-pre-wrap break-words rounded-2xl bg-canvas p-4 font-mono text-caption ' + (res.ok ? 'text-ink' : 'text-ember');
    }
  }

  // ----- Truncated error → full error modal -----
  // Full error text travels in a JSON script block (rendered per fragment by
  // the server), indexed by the row's data-err-idx — never stuffed into a
  // DOM attribute, where quotes/newlines from upstream errors would mangle.
  document.addEventListener('click', function (e) {
    var link = e.target.closest('.error-link');
    if (!link) return;
    var idx = parseInt(link.dataset.errIdx, 10);
    var errs = [];
    var holder = link.closest('tbody');
    if (holder) {
      var script = holder.querySelector('script.req-errs');
      if (script) {
        try { errs = JSON.parse(script.textContent) || []; } catch (err) { errs = []; }
      }
    }
    var text = (idx >= 0 && idx < errs.length) ? errs[idx] : '';
    var store = window.Alpine && Alpine.store('modal');
    if (store) {
      store.error.text = text;
      store.error.open = true;
      return;
    }
    var detail = document.querySelector('#error-detail');
    if (detail) detail.textContent = text;
  });

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', initChart);
  } else {
    initChart();
  }
})();

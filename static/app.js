/* ── State ── */
let config = null;

/* ── API ── */
async function api(method, path, body) {
  const opts = { method, headers: {} };
  if (body) { opts.headers['Content-Type'] = 'application/json'; opts.body = JSON.stringify(body); }
  const r = await fetch(path, opts);
  if (!r.ok) { let msg; try { const e = await r.json(); msg = e.error || r.statusText; } catch { msg = r.statusText; } throw new Error(msg); }
  const ct = r.headers.get('content-type') || '';
  return ct.includes('json') ? r.json() : r.text();
}

/* ── Asset fallbacks ── */
(function() {
  var vid = document.getElementById('bg-video');
  if (vid) {
    vid.onerror = function() {
      document.getElementById('bg-video-wrap').style.display = 'none';
    };
  }
})();
(function() {
  var img = document.getElementById('brandImg');
  if (img) {
    img.onerror = function() {
      if (this.src.indexOf('logo.png') > -1) {
        this.style.display = 'none';
        var fb = document.getElementById('brandFallback');
        if (fb) fb.style.display = 'flex';
      } else {
        this.src = '/assets/logo.png';
      }
    };
    if (img.complete && img.naturalWidth === 0) img.onerror();
  }
})();

/* ── Init ── */
document.addEventListener('DOMContentLoaded', async () => {
  try { config = await api('GET', '/api/config'); } catch { config = { filters: [], last_run: '' }; }
  updateAll();
  refreshDashboard();
  refreshHistory();
});

/* ── Navigation ── */
function navigate(page) {
  document.querySelectorAll('.page').forEach(p => p.classList.remove('active'));
  document.querySelectorAll('.nav-item').forEach(n => n.classList.remove('active'));
  document.getElementById('page-' + page).classList.add('active');
  document.querySelector(`.nav-item[data-page="${page}"]`).classList.add('active');
  if (page === 'dashboard') refreshDashboard();
  if (page === 'history') refreshHistory();
}

/* ── Updates ── */
function updateAll() {
  const n = config.filters.length;
  document.getElementById('filterCount').textContent = n + ' domain' + (n !== 1 ? 's' : '');
  document.getElementById('filterSubtitle').textContent = n + ' target domain' + (n !== 1 ? 's' : '') + ' configured';
  document.getElementById('statFilters').textContent = n;
  document.getElementById('clearFiltersBtn').style.display = n ? '' : 'none';
  renderFilters();
  renderRunFilterSummary();
}

/* ── Dashboard ── */
async function refreshDashboard() {
  try {
    const runs = await api('GET', '/api/runs');
    document.getElementById('statRuns').textContent = runs.length;
    let files = 0, hits = 0;
    for (const r of runs) { files += r.files_scanned || 0; hits += r.total_hits || 0; }
    document.getElementById('statFiles').textContent = files.toLocaleString();
    document.getElementById('statHits').textContent = hits.toLocaleString();
    document.getElementById('dashSubtitle').textContent = runs.length
      ? 'Total: ' + files.toLocaleString() + ' files, ' + hits.toLocaleString() + ' hits across ' + runs.length + ' run' + (runs.length !== 1 ? 's' : '')
      : 'No runs yet. Start your first sort.';
    renderRecent(runs);
  } catch {
    document.getElementById('recentList').innerHTML = '<p class="text-muted">Could not load data.</p>';
  }
}

function renderRecent(runs) {
  const el = document.getElementById('recentList');
  if (!runs.length) {
    el.innerHTML = '<p class="text-muted" style="padding:12px 0">No runs yet — create one from the Run Sorter page.</p>';
    return;
  }
  el.innerHTML = runs.slice(0, 5).map(r => `
    <div class="history-row" onclick="viewRun('${r.run_id}')">
      <div>
        <div class="run-id">${esc(r.run_id)}</div>
        <div class="run-meta">
          <span>${r.files_scanned} files</span>
          <span>${r.elapsed_seconds.toFixed(1)}s</span>
        </div>
      </div>
      <span class="hit-count">${r.total_hits} hit${r.total_hits !== 1 ? 's' : ''}</span>
    </div>
  `).join('');
}

/* ── Filters ── */
function renderFilters() {
  const q = (document.getElementById('filterSearch').value || '').toLowerCase();
  const list = document.getElementById('filterList');
  const filtered = q ? config.filters.filter(d => d.includes(q)) : config.filters;
  if (!filtered.length) {
    list.innerHTML = '<p class="empty-state">' + (config.filters.length ? 'No domains match your search.' : 'No domains configured. Add one to get started.') + '</p>';
    return;
  }
  list.innerHTML = filtered.map(d => `
    <div class="filter-item">
      <span class="domain">${esc(d)}</span>
      <button class="remove-btn" title="Remove" onclick="removeFilter('${esc(d)}')">
        <svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>
      </button>
    </div>
  `).join('');
}

async function removeFilter(d) {
  config.filters = config.filters.filter(x => x !== d);
  await api('POST', '/api/config/filters', { filters: config.filters });
  updateAll();
  refreshDashboard();
}

function showAddFilterModal() {
  showModal('Add domain', `
    <div class="form-group">
      <label>Domain</label>
      <input type="text" id="newFilterInput" placeholder="login.live.com" style="margin-bottom:0">
    </div>
    <p class="form-hint">Enter a single domain. Subdomains will match automatically.</p>
  `, `
    <button class="btn btn-ghost" onclick="closeModal()">Cancel</button>
    <button class="btn btn-primary" onclick="addFilter()">Add</button>
  `);
  setTimeout(() => document.getElementById('newFilterInput')?.focus(), 100);
}

async function addFilter() {
  const d = document.getElementById('newFilterInput').value.trim().toLowerCase();
  if (!d) return;
  if (!config.filters.includes(d)) config.filters.push(d);
  closeModal();
  await api('POST', '/api/config/filters', { filters: config.filters });
  updateAll();
  refreshDashboard();
}

function showImportModal() {
  showModal('Import domains', `
    <div class="form-group">
      <label>Paste domains</label>
      <textarea id="importText" placeholder="login.live.com&#10;login.microsoftonline.com&#10;accounts.google.com"></textarea>
    </div>
    <p class="form-hint">One domain per line. Duplicates are ignored.</p>
  `, `
    <button class="btn btn-ghost" onclick="closeModal()">Cancel</button>
    <button class="btn btn-primary" onclick="importFilters()">Import</button>
  `);
}

async function importFilters() {
  const text = document.getElementById('importText').value;
  const lines = text.split('\n').map(l => l.trim().toLowerCase()).filter(l => l);
  let added = 0;
  for (const d of lines) { if (!config.filters.includes(d)) { config.filters.push(d); added++; } }
  closeModal();
  await api('POST', '/api/config/filters', { filters: config.filters });
  updateAll();
  refreshDashboard();
}

async function clearAllFilters() {
  if (!config.filters.length) return;
  config.filters = [];
  await api('POST', '/api/config/filters', { filters: [] });
  updateAll();
  refreshDashboard();
}

function renderRunFilterSummary() {
  const el = document.getElementById('runFilterSummary');
  if (!config || !config.filters.length) {
    el.innerHTML = '<span class="text-muted">No filters configured. Add some from the Filters page.</span>';
    return;
  }
  const shown = config.filters.slice(0, 30);
  el.innerHTML = shown.map(d => `<span class="tag">${esc(d)}</span>`).join('');
  if (config.filters.length > 30) el.innerHTML += `<span class="tag">+${config.filters.length - 30} more</span>`;
}

/* ── Run ── */
async function startRun() {
  const dir = document.getElementById('runSourceDir').value.trim();
  if (!dir) { alert('Enter a source directory path.'); return; }
  if (!config.filters.length) { alert('No filters configured. Add at least one target domain.'); return; }

  document.getElementById('startRunBtn').disabled = true;
  document.getElementById('startRunBtn').innerHTML = 'Processing...';
  document.getElementById('runProgressPanel').style.display = 'block';
  document.getElementById('runResultPanel').style.display = 'none';
  document.getElementById('progressFill').style.width = '0%';
  document.getElementById('progressFiles').textContent = '0 / 0 files';
  document.getElementById('progressHits').textContent = '0 hits';
  document.getElementById('progressStatus').textContent = 'Scanning...';

  let evtSource;
  try {
    evtSource = new EventSource('/api/run/progress?dir=' + encodeURIComponent(dir));
  } catch {
    document.getElementById('progressStatus').textContent = 'Connection failed';
    document.getElementById('startRunBtn').disabled = false;
    document.getElementById('startRunBtn').innerHTML = '<polygon points="5 3 19 12 5 21 5 3"/> Start sorting';
    return;
  }

  evtSource.onmessage = function(e) {
    let data;
    try { data = JSON.parse(e.data); } catch { return; }

    if (data.type === 'progress') {
      document.getElementById('progressFill').style.width = data.percent + '%';
      document.getElementById('progressFiles').textContent = data.processed + ' / ' + data.total + ' files';
      document.getElementById('progressHits').textContent = data.hits + ' hits';
      document.getElementById('progressStatus').textContent = 'Processing...';
    } else if (data.type === 'complete') {
      evtSource.close();
      document.getElementById('progressFill').style.width = '100%';
      document.getElementById('progressStatus').textContent = 'Done';
      document.getElementById('startRunBtn').disabled = false;
      document.getElementById('startRunBtn').innerHTML = '<polygon points="5 3 19 12 5 21 5 3"/> Start sorting';
      document.getElementById('runResultPanel').style.display = 'block';
      renderRunComplete(data);
      refreshDashboard();
      refreshHistory();
    } else if (data.type === 'error') {
      evtSource.close();
      document.getElementById('progressStatus').textContent = 'Error: ' + (data.message || 'unknown');
      document.getElementById('startRunBtn').disabled = false;
      document.getElementById('startRunBtn').innerHTML = '<polygon points="5 3 19 12 5 21 5 3"/> Start sorting';
    }
  };
  evtSource.onerror = function() {
    evtSource.close();
    document.getElementById('progressStatus').textContent = 'Connection lost';
    document.getElementById('startRunBtn').disabled = false;
    document.getElementById('startRunBtn').innerHTML = '<polygon points="5 3 19 12 5 21 5 3"/> Start sorting';
  };
}

function renderRunComplete(data) {
  const el = document.getElementById('runSummary');
  const breakdown = data.domain_breakdown ? Object.entries(data.domain_breakdown).sort((a, b) => b[1] - a[1]) : [];
  el.innerHTML = `
    <div class="summary-row"><span class="label">Run ID</span><span class="value">${esc(data.run_id)}</span></div>
    <div class="summary-row"><span class="label">Files scanned</span><span class="value">${data.files_scanned}</span></div>
    <div class="summary-row"><span class="label">Total cookies</span><span class="value">${data.total_cookies}</span></div>
    <div class="summary-row"><span class="label">Total hits</span><span class="value">${data.total_hits}</span></div>
    <div class="summary-row"><span class="label">Errors</span><span class="value">${data.errors}</span></div>
    <div class="summary-row"><span class="label">Time</span><span class="value">${data.elapsed_seconds.toFixed(1)}s</span></div>
    <div class="summary-row"><span class="label">Output</span><span class="value" style="font-size:11px;word-break:break-all">${esc(data.output_dir)}</span></div>
    ${breakdown.length ? `<div class="summary-heading">Hits by domain</div>
      ${breakdown.map(([d, c]) => `<div class="domain-row"><span>${esc(d)}</span><span class="hits">${c}</span></div>`).join('')}` : ''}
  `;
}

/* ── History ── */
async function refreshHistory() {
  const el = document.getElementById('historyList');
  try {
    const runs = await api('GET', '/api/runs');
    if (!runs.length) {
      el.innerHTML = '<p class="text-muted" style="padding:12px 0">No runs yet.</p>';
      return;
    }
    el.innerHTML = runs.map(r => `
      <div class="history-row" onclick="viewRun('${r.run_id}')">
        <div>
          <div class="run-id">${esc(r.run_id)}</div>
          <div class="run-meta">
            <span>${r.files_scanned} files</span>
            <span>${r.total_cookies} cookies</span>
            <span>${r.elapsed_seconds.toFixed(1)}s</span>
          </div>
        </div>
        <span class="hit-count">${r.total_hits} hit${r.total_hits !== 1 ? 's' : ''}</span>
      </div>
    `).join('');
  } catch {
    el.innerHTML = '<p class="text-muted" style="padding:12px 0">Error loading history.</p>';
  }
}

async function viewRun(runId) {
  let r;
  try { r = await api('GET', '/api/runs/' + runId); } catch { return; }
  const breakdown = r.domain_breakdown ? Object.entries(r.domain_breakdown).sort((a, b) => b[1] - a[1]) : [];
  showModal('Run: ' + runId, `
    <div class="summary-row"><span class="label">Run ID</span><span class="value">${esc(r.run_id)}</span></div>
    <div class="summary-row"><span class="label">Timestamp</span><span class="value">${r.timestamp || '—'}</span></div>
    <div class="summary-row"><span class="label">Source</span><span class="value" style="font-size:11px;word-break:break-all">${esc(r.source_directory || '')}</span></div>
    <div class="summary-row"><span class="label">Files scanned</span><span class="value">${r.files_scanned}</span></div>
    <div class="summary-row"><span class="label">Total cookies</span><span class="value">${r.total_cookies}</span></div>
    <div class="summary-row"><span class="label">Total hits</span><span class="value">${r.total_hits}</span></div>
    <div class="summary-row"><span class="label">Errors</span><span class="value">${r.errors}</span></div>
    <div class="summary-row"><span class="label">Time</span><span class="value">${r.elapsed_seconds.toFixed(1)}s</span></div>
    <div class="summary-row"><span class="label">Filters used</span><span class="value">${(r.filters_used || []).length}</span></div>
    ${breakdown.length ? `<div class="summary-heading">Hits by domain</div>
      ${breakdown.slice(0, 40).map(([d, c]) => `<div class="domain-row"><span>${esc(d)}</span><span class="hits">${c}</span></div>`).join('')}
      ${breakdown.length > 40 ? '<p class="text-muted" style="margin-top:8px">... and ' + (breakdown.length - 40) + ' more</p>' : ''}` : ''}
  `, `
    <button class="btn btn-primary" onclick="closeModal()">Close</button>
  `);
}

/* ── Modal ── */
function showModal(title, body, footer) {
  document.getElementById('modalTitle').textContent = title;
  document.getElementById('modalBody').innerHTML = body;
  document.getElementById('modalFooter').innerHTML = footer || '';
  document.getElementById('modal').style.display = 'flex';
}
function closeModal() { document.getElementById('modal').style.display = 'none'; }

/* ── Escape HTML ── */
function esc(s) {
  if (!s) return '';
  return String(s).replace(/&/g,'&amp;').replace(/</g,'&lt;').replace(/>/g,'&gt;').replace(/"/g,'&quot;').replace(/'/g,'&#039;');
}

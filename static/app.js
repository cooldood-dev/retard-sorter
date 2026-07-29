/* ── State ── */
let config = null;
let userName = '';

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
  if (vid) { vid.onerror = function() { document.getElementById('bg-video-wrap').style.display = 'none'; }; }
})();
(function() {
  var img = document.getElementById('brandImg');
  if (img) { img.onerror = function() { this.style.display='none'; document.getElementById('brandFallbackTop').style.display='flex'; }; if (img.complete && img.naturalWidth === 0) img.onerror(); }
})();

/* ── Init ── */
document.addEventListener('DOMContentLoaded', async () => {
  try { config = await api('GET', '/api/config'); } catch { config = { filters: [], last_run: '', proxy_mode: 'off', proxy_file: '' }; }
  var userData;
  try { userData = await api('GET', '/api/user'); } catch { userData = {}; }
  userName = (userData && userData.name) || '';
  if (!userName) {
    document.getElementById('firstStartup').style.display = 'flex';
  } else {
    document.getElementById('dashUserName').textContent = userName;
  }
  updateAll();
  renderProxySettings();
  refreshDashboard();
  refreshHistory();
  loadChangelog();
  initDock();
});

async function saveUserName() {
  var name = document.getElementById('userNameInput').value.trim();
  if (!name) { return; }
  try {
    await api('POST', '/api/user', { name: name });
    userName = name;
    document.getElementById('firstStartup').style.display = 'none';
    document.getElementById('dashUserName').textContent = userName;
    refreshDashboard();
  } catch (e) {
    alert('Failed to save name: ' + e.message);
  }
}

document.addEventListener('keydown', function(e) {
  if (e.key === 'Enter' && document.getElementById('firstStartup').style.display === 'flex') {
    saveUserName();
  }
});

/* ── Asset fallbacks ── */

/* ── Changelog ── */
async function loadChangelog() {
  try {
    var res = await api('GET', '/api/changelog');
    document.getElementById('changelogBody').textContent = res.content || 'No changelog available.';
  } catch {
    document.getElementById('changelogBody').innerHTML = '<p class="text-muted">Could not load changelog.</p>';
  }
}

/* ── NAVIGATION ── */
function navigate(page) {
  document.querySelectorAll('.page').forEach(p => p.classList.remove('active'));
  document.querySelectorAll('.dock-item').forEach(n => n.classList.remove('active'));
  document.getElementById('page-' + page).classList.add('active');
  var dockItem = document.querySelector('.dock-item[data-page="' + page + '"]');
  if (dockItem) dockItem.classList.add('active');
  if (page === 'dashboard') { refreshDashboard(); loadChangelog(); }
  if (page === 'run') renderProxySettings();
  if (page === 'history') refreshHistory();
}

/* ── DOCK MAGNIFICATION ── */
function initDock() {
  var dock = document.getElementById('dock');
  var inner = dock.querySelector('.dock-inner');
  var items = dock.querySelectorAll('.dock-item');
  var maxDist = 130;
  var maxMag = 0.4;
  var rafId = null;

  function smoothstep(t) { return t * t * (3 - 2 * t); }

  var basePad = 28;
  var baseGap = 20;
  var centers = [];
  function cacheCenters() {
    for (var i = 0; i < items.length; i++) {
      var rect = items[i].getBoundingClientRect();
      centers[i] = { x: rect.left + rect.width / 2, y: rect.top + rect.height / 2 };
    }
  }
  cacheCenters();
  window.addEventListener('resize', cacheCenters);

  function updateMagnification(mouseX, mouseY) {
    var peakMag = 0;
    for (var i = 0; i < items.length; i++) {
      var cx = centers[i].x;
      var cy = centers[i].y;
      var dx = mouseX - cx;
      var dy = mouseY - cy;
      var dist = Math.sqrt(dx * dx + dy * dy);
      var mag = 0;
      if (dist < maxDist) {
        var t = 1 - dist / maxDist;
        mag = maxMag * smoothstep(t);
      }
      if (mag > peakMag) peakMag = mag;
      var scale = 1 + mag;
      var lift = mag * 20;
      items[i].style.transform = 'scale(' + scale + ') translateY(' + (-lift) + 'px)';
      items[i].style.zIndex = Math.round(mag * 100);
    }
    var extra = peakMag * 24;
    inner.style.paddingLeft = (basePad + extra) + 'px';
    inner.style.paddingRight = (basePad + extra) + 'px';
    inner.style.gap = (baseGap + extra / 2) + 'px';
  }

  function resetMagnification() {
    for (var i = 0; i < items.length; i++) {
      items[i].style.transform = '';
      items[i].style.zIndex = '';
    }
    inner.style.paddingLeft = '';
    inner.style.paddingRight = '';
    inner.style.gap = '';
  }

  inner.addEventListener('mousemove', function(e) {
    if (rafId) cancelAnimationFrame(rafId);
    rafId = requestAnimationFrame(function() {
      updateMagnification(e.clientX, e.clientY);
    });
  });

  inner.addEventListener('mouseleave', function() {
    if (rafId) cancelAnimationFrame(rafId);
    resetMagnification();
  });

  inner.addEventListener('click', function(e) {
    var target = e.target.closest('.dock-item');
    if (target) {
      var page = target.getAttribute('data-page');
      navigate(page);
    }
  });
}

/* ── Updates ── */
function updateAll() {
  var n = config.filters.length;
  document.getElementById('filterSubtext').textContent = n + ' target domain' + (n !== 1 ? 's' : '') + ' configured';
  document.getElementById('clearFiltersBtn').style.display = n ? '' : 'none';
  renderFilters();
  renderRunFilterSummary();
}

/* ── DASHBOARD ── */
async function refreshDashboard() {
  try {
    var runs = await api('GET', '/api/runs');
    var filterCount = (config && config.filters ? config.filters.length : 0);
    document.getElementById('statFilters').textContent = filterCount;
    document.getElementById('statRuns').textContent = runs.length;
    var files = 0, hits = 0;
    for (var i = 0; i < runs.length; i++) { files += runs[i].files_scanned || 0; hits += runs[i].total_hits || 0; }
    document.getElementById('statFiles').textContent = files.toLocaleString();
    document.getElementById('statHits').textContent = hits.toLocaleString();
    document.getElementById('statHitsBadge').textContent = hits.toLocaleString();
    document.getElementById('statusFilters').textContent = filterCount;
    document.getElementById('statusRunsStat').textContent = runs.length;
    document.getElementById('statusHitsStat').textContent = hits.toLocaleString();
    document.getElementById('dashSubtext').textContent = runs.length
      ? 'Total: ' + files.toLocaleString() + ' files, ' + hits.toLocaleString() + ' hits across ' + runs.length + ' run' + (runs.length !== 1 ? 's' : '')
      : 'No runs yet. Start your first sort.';
    renderRecent(runs);
    loadChangelog();
  } catch {
    document.getElementById('recentList').innerHTML = '<p class="text-muted" style="padding:12px 0">Could not load data.</p>';
  }
}

function renderRecent(runs) {
  var el = document.getElementById('recentList');
  if (!runs.length) {
    el.innerHTML = '<p class="text-muted" style="padding:12px 0">No runs yet.</p>';
    return;
  }
  el.innerHTML = runs.slice(0, 6).map(function(r) { return (
    '<div class="list-item" onclick="viewRun(\'' + r.run_id + '\')">' +
      '<div class="list-radio"><svg width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><circle cx="12" cy="12" r="10"/><polyline points="12 6 12 12 16 14"/></svg></div>' +
      '<div class="list-body">' +
        '<div class="list-title">' + esc(r.run_id) + '</div>' +
        '<div class="list-sub">' + r.files_scanned + ' files, ' + r.elapsed_seconds.toFixed(1) + 's</div>' +
      '</div>' +
      '<span style="color:var(--accent);font-weight:600;font-family:var(--mono)">' + r.total_hits + ' hit' + (r.total_hits !== 1 ? 's' : '') + '</span>' +
      '<svg class="list-arrow" width="16" height="16" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><polyline points="9 18 15 12 9 6"/></svg>' +
    '</div>'
  ); }).join('');
}

/* ── FILTERS ── */
function renderFilters() {
  var q = (document.getElementById('filterSearch').value || '').toLowerCase();
  var list = document.getElementById('filterList');
  var filtered = q ? config.filters.filter(function(d) { return d.indexOf(q) !== -1; }) : config.filters;
  if (!filtered.length) {
    list.innerHTML = '<p class="empty-state">' + (config.filters.length ? 'No domains match your search.' : 'No domains configured. Add one to get started.') + '</p>';
    return;
  }
  list.innerHTML = filtered.map(function(d) { return (
    '<div class="filter-item">' +
      '<span class="domain">' + esc(d) + '</span>' +
      '<button class="remove-btn" title="Remove" onclick="removeFilter(\'' + esc(d) + '\')">' +
        '<svg width="15" height="15" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" stroke-linejoin="round"><line x1="18" y1="6" x2="6" y2="18"/><line x1="6" y1="6" x2="18" y2="18"/></svg>' +
      '</button>' +
    '</div>'
  ); }).join('');
}

async function removeFilter(d) {
  config.filters = config.filters.filter(function(x) { return x !== d; });
  await api('POST', '/api/config/filters', { filters: config.filters });
  updateAll();
  refreshDashboard();
}

function showAddFilterModal() {
  showModal('Add domain',
    '<div class="form-group">' +
      '<label>Domain</label>' +
      '<input type="text" id="newFilterInput" placeholder="login.live.com" style="margin-bottom:0">' +
    '</div>' +
    '<p class="form-hint">Enter a single domain. Subdomains will match automatically.</p>',
    '<button class="btn btn-ghost" onclick="closeModal()">Cancel</button>' +
    '<button class="btn btn-primary" onclick="addFilter()">Add</button>'
  );
  setTimeout(function() { var el = document.getElementById('newFilterInput'); if (el) el.focus(); }, 100);
}

async function addFilter() {
  var d = document.getElementById('newFilterInput').value.trim().toLowerCase();
  if (!d) return;
  if (config.filters.indexOf(d) === -1) config.filters.push(d);
  closeModal();
  await api('POST', '/api/config/filters', { filters: config.filters });
  updateAll();
  refreshDashboard();
}

function showImportModal() {
  showModal('Import domains',
    '<div class="form-group">' +
      '<label>Paste domains</label>' +
      '<textarea id="importText" placeholder="login.live.com\nlogin.microsoftonline.com\naccounts.google.com"></textarea>' +
    '</div>' +
    '<p class="form-hint">One domain per line. Duplicates are ignored.</p>',
    '<button class="btn btn-ghost" onclick="closeModal()">Cancel</button>' +
    '<button class="btn btn-primary" onclick="importFilters()">Import</button>'
  );
}

async function importFilters() {
  var text = document.getElementById('importText').value;
  var lines = text.split('\n').map(function(l) { return l.trim().toLowerCase(); }).filter(function(l) { return l; });
  var added = 0;
  for (var i = 0; i < lines.length; i++) {
    if (config.filters.indexOf(lines[i]) === -1) { config.filters.push(lines[i]); added++; }
  }
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
  var el = document.getElementById('runFilterSummary');
  if (!config || !config.filters.length) {
    el.innerHTML = '<span class="text-muted">No filters configured. Add some from the Filters page.</span>';
    return;
  }
  var shown = config.filters.slice(0, 30);
  el.innerHTML = shown.map(function(d) { return '<span class="tag">' + esc(d) + '</span>'; }).join('');
  if (config.filters.length > 30) el.innerHTML += '<span class="tag">+' + (config.filters.length - 30) + ' more</span>';
}

/* ── Section tabs (Run page) ── */
function switchSection(name) {
  document.querySelectorAll('.section-tab').forEach(function(t) { t.classList.remove('active'); });
  document.querySelectorAll('.section-panel').forEach(function(p) { p.classList.remove('active'); });
  document.querySelector('.section-tab[data-section="' + name + '"]').classList.add('active');
  document.getElementById('sec-' + name).classList.add('active');
}



/* ── Proxy ── */
function renderProxySettings() {
  if (!config) return;
  var mode = config.proxy_mode || 'off';
  document.querySelectorAll('input[name="proxyMode"]').forEach(function(r) { r.checked = r.value === mode; });
  var fg = document.getElementById('proxyFileGroup');
  if (fg) fg.style.display = mode === 'off' ? 'none' : '';
  var pathInput = document.getElementById('proxyFilePath');
  if (pathInput) pathInput.value = config.proxy_file || '';
  updateProxyStatus();
}

function updateProxyMode() {
  var mode = document.querySelector('input[name="proxyMode"]:checked');
  if (!mode) return;
  var fg = document.getElementById('proxyFileGroup');
  if (fg) fg.style.display = mode.value === 'off' ? 'none' : '';
  var pathInput = document.getElementById('proxyFilePath');
  var proxyFile = pathInput ? pathInput.value.trim() : '';
  api('POST', '/api/config/proxy', { proxy_mode: mode.value, proxy_file: proxyFile })
    .then(function() { return api('GET', '/api/config'); })
    .then(function(c) { config = c; })
    .catch(function(e) { console.error(e); });
}

function updateProxyStatus() {
  var el = document.getElementById('proxyStatus');
  if (!el) return;
  var mode = document.querySelector('input[name="proxyMode"]:checked');
  if (!mode || mode.value === 'off') { el.style.display = 'none'; return; }
  var pathInput = document.getElementById('proxyFilePath');
  var file = pathInput ? pathInput.value.trim() : '';
  if (!file) { el.style.display = 'none'; return; }
  el.style.display = 'block';
  el.textContent = 'Proxy file: ' + file;
}

function saveProxyPath() {
  var pathInput = document.getElementById('proxyFilePath');
  if (!pathInput) return;
  var path = pathInput.value.trim();
  var mode = document.querySelector('input[name="proxyMode"]:checked');
  var proxyMode = mode ? mode.value : 'off';
  api('POST', '/api/config/proxy', { proxy_mode: proxyMode, proxy_file: path })
    .then(function() { return api('GET', '/api/config'); })
    .then(function(c) { config = c; updateProxyStatus(); })
    .catch(function(e) { console.error(e); });
}

/* ── Run ── */
var runStartTime = 0;

function formatETA(ms) {
  if (ms <= 0 || !isFinite(ms)) return '—';
  var s = Math.ceil(ms / 1000);
  if (s < 60) return s + 's';
  var m = Math.floor(s / 60); s = s % 60;
  if (m < 60) return m + 'm ' + s + 's';
  var h = Math.floor(m / 60); m = m % 60;
  if (h < 24) return h + 'h ' + m + 'm';
  var d = Math.floor(h / 24); h = h % 24;
  return d + 'd ' + h + 'h';
}

// Moving average ETA with weighted recent samples
var etaSamples = [];
var etaLastUpdate = 0;

function updateETA(processed, total) {
  if (processed <= 0) {
    document.getElementById('etaTimer').textContent = '—';
    document.getElementById('etaElapsed').textContent = '0s';
    document.getElementById('etaSpeed').textContent = '0 files/s';
    return;
  }

  var now = Date.now();
  var elapsed = now - runStartTime;

  // Sample every 500ms to avoid noise
  if (now - etaLastUpdate >= 500 || etaSamples.length === 0) {
    etaSamples.push({ time: now, processed: processed });
    // Keep last 20 samples (10 seconds of data)
    if (etaSamples.length > 20) etaSamples.shift();
    etaLastUpdate = now;
  }

  // Calculate rate from recent samples (weighted average)
  var rate = 0;
  if (etaSamples.length >= 2) {
    var recentWeight = 0;
    var weightedSum = 0;
    for (var i = 1; i < etaSamples.length; i++) {
      var dt = etaSamples[i].time - etaSamples[i-1].time;
      var dp = etaSamples[i].processed - etaSamples[i-1].processed;
      if (dt > 0) {
        var instantRate = dp / dt;
        var weight = i; // More recent = higher weight
        weightedSum += instantRate * weight;
        recentWeight += weight;
      }
    }
    if (recentWeight > 0) {
      rate = weightedSum / recentWeight;
    }
  }

  // Fallback to overall average if no recent samples
  if (rate <= 0 && elapsed > 0) {
    rate = processed / elapsed;
  }

  var remaining = rate > 0 ? (total - processed) / rate : 0;
  var filesPerSec = rate * 1000;

  document.getElementById('etaTimer').textContent = formatETA(remaining);
  document.getElementById('etaElapsed').textContent = formatETA(elapsed);
  document.getElementById('etaSpeed').textContent = filesPerSec.toFixed(1) + ' files/s';
}

async function startRun() {
  var dir = document.getElementById('runSourceDir').value.trim();
  if (!dir) { alert('Enter a source directory path.'); return; }
  if (!config.filters.length) { alert('No filters configured. Add at least one target domain.'); return; }

  runStartTime = Date.now();
  etaSamples = [];
  etaLastUpdate = 0;
  document.getElementById('startRunBtn').disabled = true;
  document.getElementById('startRunBtn').innerHTML = 'Processing...';
  document.getElementById('runProgressPanel').style.display = 'block';
  document.getElementById('runResultPanel').style.display = 'none';
  document.getElementById('progressFill').style.width = '0%';
  document.getElementById('progressFiles').textContent = '0 / 0 files';
  document.getElementById('progressHits').textContent = '0 hits';
  document.getElementById('progressStatus').textContent = 'Scanning...';
  document.getElementById('etaTimer').textContent = '—';
  document.getElementById('etaCard').style.display = 'none';

  var evtSource;
  try {
    evtSource = new EventSource('/api/run/progress?dir=' + encodeURIComponent(dir));
  } catch {
    document.getElementById('progressStatus').textContent = 'Connection failed';
    document.getElementById('startRunBtn').disabled = false;
    document.getElementById('startRunBtn').innerHTML = 'Start sorting';
    return;
  }

  evtSource.onmessage = function(e) {
    var data;
    try { data = JSON.parse(e.data); } catch { return; }

    if (data.type === 'progress') {
      document.getElementById('progressFill').style.width = data.percent + '%';
      document.getElementById('progressFiles').textContent = data.processed + ' / ' + data.total + ' files';
      document.getElementById('progressHits').textContent = data.hits + ' hits';
      document.getElementById('etaCard').style.display = 'flex';
      updateETA(data.processed, data.total);
      if (data.check_status) {
        document.getElementById('progressTitle').textContent = 'Checking cookies';
        document.getElementById('checkProgress').style.display = 'block';
        document.getElementById('checkCount').textContent = data.checked + ' / ' + (data.check_total || '?') + ' checked';
        document.getElementById('checkValid').textContent = data.valid + ' valid';
        document.getElementById('checkInvalid').textContent = data.invalid + ' invalid';
        document.getElementById('checkFill').style.width = data.percent + '%';
        document.getElementById('progressStatus').textContent = data.check_status;
      } else {
        document.getElementById('progressStatus').textContent = 'Processing...';
      }
    } else if (data.type === 'complete') {
      evtSource.close();
      document.getElementById('progressTitle').textContent = 'Processing';
      document.getElementById('checkProgress').style.display = 'none';
      document.getElementById('progressFill').style.width = '100%';
      document.getElementById('etaCard').style.display = 'none';
      document.getElementById('progressStatus').textContent = 'Done';
      document.getElementById('startRunBtn').disabled = false;
      document.getElementById('startRunBtn').innerHTML = 'Start sorting';
      document.getElementById('runResultPanel').style.display = 'block';
      renderRunComplete(data);
      refreshDashboard();
      refreshHistory();
    } else if (data.type === 'error') {
      evtSource.close();
      document.getElementById('progressTitle').textContent = 'Processing';
      document.getElementById('checkProgress').style.display = 'none';
      document.getElementById('progressStatus').textContent = 'Error: ' + (data.message || 'unknown');
      document.getElementById('startRunBtn').disabled = false;
      document.getElementById('startRunBtn').innerHTML = 'Start sorting';
    }
  };
  evtSource.onerror = function() {
    evtSource.close();
    document.getElementById('progressStatus').textContent = 'Connection lost';
    document.getElementById('startRunBtn').disabled = false;
    document.getElementById('startRunBtn').innerHTML = 'Start sorting';
  };
}

function renderRunComplete(data) {
  var el = document.getElementById('runSummary');
  var entries = data.domain_breakdown ? Object.entries(data.domain_breakdown).sort(function(a, b) { return b[1] - a[1]; }) : [];
  var checkHtml = '';
  if (data.checked > 0) {
    checkHtml = '<div class="summary-row"><span class="label">Cookies checked</span><span class="value">' + data.checked + '</span></div>' +
      '<div class="summary-row"><span class="label">Valid</span><span class="value" style="color:var(--green)">' + data.valid + '</span></div>' +
      '<div class="summary-row"><span class="label">Invalid</span><span class="value" style="color:var(--red)">' + data.invalid + '</span></div>';
  }
  var domainHtml = '';
  if (entries.length) {
    domainHtml = '<div class="summary-heading">Hits by domain</div>' +
      entries.slice(0, 30).map(function(e) { return '<div class="domain-row"><span>' + esc(e[0]) + '</span><span class="hits">' + e[1] + '</span></div>'; }).join('');
    if (entries.length > 30) domainHtml += '<p class="text-muted" style="margin-top:8px">... and ' + (entries.length - 30) + ' more</p>';
  }
  el.innerHTML =
    '<div class="summary-row"><span class="label">Run ID</span><span class="value">' + esc(data.run_id) + '</span></div>' +
    '<div class="summary-row"><span class="label">Files scanned</span><span class="value">' + data.files_scanned + '</span></div>' +
    '<div class="summary-row"><span class="label">Total cookies</span><span class="value">' + data.total_cookies + '</span></div>' +
    '<div class="summary-row"><span class="label">Total hits</span><span class="value">' + data.total_hits + '</span></div>' +
    checkHtml +
    '<div class="summary-row"><span class="label">Errors</span><span class="value">' + data.errors + '</span></div>' +
    '<div class="summary-row"><span class="label">Time</span><span class="value">' + data.elapsed_seconds.toFixed(1) + 's</span></div>' +
    '<div class="summary-row"><span class="label">Output</span><span class="value" style="font-size:11px;word-break:break-all">' + esc(data.output_dir) + '</span></div>' +
    domainHtml;
}

/* ── History ── */
async function refreshHistory() {
  var el = document.getElementById('historyList');
  try {
    var runs = await api('GET', '/api/runs');
    if (!runs.length) {
      el.innerHTML = '<p class="text-muted" style="padding:12px 0">No runs yet.</p>';
      return;
    }
    el.innerHTML = runs.map(function(r) { return (
      '<div class="history-row" onclick="viewRun(\'' + r.run_id + '\')">' +
        '<div>' +
          '<div class="run-id">' + esc(r.run_id) + '</div>' +
          '<div class="run-meta">' +
            '<span>' + r.files_scanned + ' files</span>' +
            '<span>' + r.total_cookies + ' cookies</span>' +
            '<span>' + r.elapsed_seconds.toFixed(1) + 's</span>' +
          '</div>' +
        '</div>' +
        '<span class="hit-count">' + r.total_hits + ' hit' + (r.total_hits !== 1 ? 's' : '') + '</span>' +
      '</div>'
    ); }).join('');
  } catch {
    el.innerHTML = '<p class="text-muted" style="padding:12px 0">Error loading history.</p>';
  }
}

async function viewRun(runId) {
  var r;
  try { r = await api('GET', '/api/runs/' + runId); } catch { return; }
  var entries = r.domain_breakdown ? Object.entries(r.domain_breakdown).sort(function(a, b) { return b[1] - a[1]; }) : [];
  var domainHtml = '';
  if (entries.length) {
    domainHtml = '<div class="summary-heading">Hits by domain</div>' +
      entries.slice(0, 40).map(function(e) { return '<div class="domain-row"><span>' + esc(e[0]) + '</span><span class="hits">' + e[1] + '</span></div>'; }).join('');
    if (entries.length > 40) domainHtml += '<p class="text-muted" style="margin-top:8px">... and ' + (entries.length - 40) + ' more</p>';
  }
  showModal('Run: ' + runId,
    '<div class="summary-row"><span class="label">Run ID</span><span class="value">' + esc(r.run_id) + '</span></div>' +
    '<div class="summary-row"><span class="label">Timestamp</span><span class="value">' + (r.timestamp || '—') + '</span></div>' +
    '<div class="summary-row"><span class="label">Source</span><span class="value" style="font-size:11px;word-break:break-all">' + esc(r.source_directory || '') + '</span></div>' +
    '<div class="summary-row"><span class="label">Files scanned</span><span class="value">' + r.files_scanned + '</span></div>' +
    '<div class="summary-row"><span class="label">Total cookies</span><span class="value">' + r.total_cookies + '</span></div>' +
    '<div class="summary-row"><span class="label">Total hits</span><span class="value">' + r.total_hits + '</span></div>' +
    '<div class="summary-row"><span class="label">Errors</span><span class="value">' + r.errors + '</span></div>' +
    '<div class="summary-row"><span class="label">Time</span><span class="value">' + r.elapsed_seconds.toFixed(1) + 's</span></div>' +
    '<div class="summary-row"><span class="label">Filters used</span><span class="value">' + (r.filters_used || []).length + '</span></div>' +
    domainHtml,
    '<button class="btn btn-primary" onclick="closeModal()">Close</button>'
  );
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

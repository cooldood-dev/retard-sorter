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
  renderSortMode();
  renderHitsOutputDir();
  setCheckerMode('cookie');
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
  if (page === 'run') { renderProxySettings(); renderSortMode(); renderHitsOutputDir(); }
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
  var runClear = document.getElementById('runClearFiltersBtn');
  if (runClear) runClear.style.display = n ? '' : 'none';
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
  try {
    await api('POST', '/api/config/filters', { filters: config.filters });
    config = await api('GET', '/api/config');
    updateAll();
    refreshDashboard();
  } catch (e) {
    alert('Failed to remove filter: ' + e.message);
    config = await api('GET', '/api/config');
    updateAll();
  }
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
  try {
    await api('POST', '/api/config/filters', { filters: config.filters });
    config = await api('GET', '/api/config');
    updateAll();
    refreshDashboard();
  } catch (e) {
    alert('Failed to add filter: ' + e.message);
    config = await api('GET', '/api/config');
    updateAll();
  }
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
  try {
    await api('POST', '/api/config/filters', { filters: config.filters });
    config = await api('GET', '/api/config');
    updateAll();
    refreshDashboard();
  } catch (e) {
    alert('Failed to import filters: ' + e.message);
    config = await api('GET', '/api/config');
    updateAll();
  }
}

async function clearAllFilters() {
  if (!config.filters.length) return;
  if (!confirm('Remove all ' + config.filters.length + ' filter' + (config.filters.length !== 1 ? 's' : '') + '?')) return;
  config.filters = [];
  try {
    await api('POST', '/api/config/filters', { filters: [] });
    config = await api('GET', '/api/config');
    updateAll();
    refreshDashboard();
  } catch (e) {
    alert('Failed to clear filters: ' + e.message);
    config = await api('GET', '/api/config');
    updateAll();
  }
}

function renderRunFilterSummary() {
  var el = document.getElementById('runFilterSummary');
  if (!config || !config.filters.length) {
    el.innerHTML = '<span class="text-muted">No filters configured. Add a domain above.</span>';
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
  var label = document.getElementById('proxyFileLabel');
  if (label) {
    if (config.proxy_file && config.proxy_file.length) {
      var name = config.proxy_file.split(/[\\/]/).pop();
      label.textContent = name ? 'Loaded: ' + name : 'No file selected';
    } else {
      label.textContent = 'No file selected';
    }
  }
  updateProxyStatus();
}

function updateProxyMode() {
  var mode = document.querySelector('input[name="proxyMode"]:checked');
  if (!mode) return;
  var fg = document.getElementById('proxyFileGroup');
  if (fg) fg.style.display = mode.value === 'off' ? 'none' : '';
  api('POST', '/api/config/proxy', { proxy_mode: mode.value })
    .then(function() { return api('GET', '/api/config'); })
    .then(function(c) { config = c; })
    .catch(function(e) { console.error(e); });
}

function uploadProxyFile(input, labelId) {
  if (!input.files || !input.files.length) return;
  var file = input.files[0];
  var fd = new FormData();
  fd.append('file', file);
  var label = document.getElementById(labelId);
  if (label) label.textContent = 'Uploading...';
  var xhr = new XMLHttpRequest();
  xhr.open('POST', '/api/config/proxyfile');
  xhr.onload = function() {
    var ok = false;
    var count = 0;
    try { var r = JSON.parse(xhr.responseText); ok = r.status === 'ok'; count = r.count || 0; } catch (e) {}
    if (ok) {
      if (label) label.textContent = file.name + (count ? ' (' + count + ' proxies)' : '');
      api('GET', '/api/config').then(function(c) { config = c; renderProxySettings(); }).catch(function() {});
    } else {
      if (label) label.textContent = 'Upload failed';
    }
  };
  xhr.onerror = function() { if (label) label.textContent = 'Upload failed'; };
  xhr.send(fd);
}

function updateProxyStatus() {
  var el = document.getElementById('proxyStatus');
  if (!el) return;
  var mode = document.querySelector('input[name="proxyMode"]:checked');
  if (!mode || mode.value === 'off') { el.style.display = 'none'; return; }
  if (!config || !config.proxy_file) { el.style.display = 'none'; return; }
  el.style.display = 'block';
  el.textContent = 'Proxy file: ' + config.proxy_file.split(/[\\/]/).pop();
}

/* ── Sort mode ── */
function renderSortMode() {
  if (!config) return;
  var mode = config.sort_mode || 'cookie';
  document.querySelectorAll('input[name="sortMode"]').forEach(function(r) { r.checked = r.value === mode; });
  var isUlp = mode === 'ulp';
  var dirGroup = document.getElementById('runSourceDirGroup');
  var fileGroup = document.getElementById('ulpFileGroup');
  if (dirGroup) dirGroup.style.display = isUlp ? 'none' : '';
  if (fileGroup) fileGroup.style.display = isUlp ? '' : 'none';
  var hint = document.getElementById('runSourceHint');
  if (hint) hint.textContent = 'Full path to the folder containing Netscape cookie files';
  var input = document.getElementById('runSourceDir');
  if (input) input.placeholder = 'C:\\Users\\user\\cookies\\input';
}

function updateSortMode() {
  var el = document.querySelector('input[name="sortMode"]:checked');
  if (!el) return;
  api('POST', '/api/config/sortmode', { sort_mode: el.value })
    .then(function() { return api('GET', '/api/config'); })
    .then(function(c) { config = c; renderSortMode(); })
    .catch(function(e) { console.error(e); });
}

function updateULPFileLabel() {
  var input = document.getElementById('ulpFileInput');
  var label = document.getElementById('ulpFileLabel');
  if (!input || !label) return;
  var n = input.files.length;
  if (!n) { label.textContent = 'No file selected'; return; }
  var names = [];
  for (var i = 0; i < n && i < 3; i++) names.push(input.files[i].name);
  label.textContent = names.join(', ') + (n > 3 ? ' +' + (n - 3) + ' more' : '');
}

/* ── Hits output dir ── */
function renderHitsOutputDir() {
  if (!config) return;
  var el = document.getElementById('hitsOutputDir');
  if (el) el.value = config.hits_output_dir || '';
}

function saveHitsOutputDir() {
  var el = document.getElementById('hitsOutputDir');
  if (!el) return;
  api('POST', '/api/config/outputdir', { hits_output_dir: el.value })
    .then(function() { return api('GET', '/api/config'); })
    .then(function(c) { config = c; renderHitsOutputDir(); })
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

function prepareRunUI() {
  runStartTime = Date.now();
  etaSamples = [];
  etaLastUpdate = 0;
  document.getElementById('startRunBtn').disabled = true;
  document.getElementById('startRunBtn').innerHTML = 'Processing...';
  document.getElementById('runProgressPanel').style.display = 'block';
  document.getElementById('runResultPanel').style.display = 'none';
  document.getElementById('progressTitle').textContent = 'Processing';
  document.getElementById('checkProgress').style.display = 'none';
  document.getElementById('progressFill').style.width = '0%';
  document.getElementById('progressFiles').textContent = '0 / 0 files';
  document.getElementById('progressHits').textContent = '0 hits';
  document.getElementById('progressStatus').textContent = 'Scanning...';
  document.getElementById('etaTimer').textContent = '—';
  document.getElementById('etaCard').style.display = 'none';
}

function resetRunBtn() {
  document.getElementById('startRunBtn').disabled = false;
  document.getElementById('startRunBtn').innerHTML = 'Start sorting';
}

function handleRunEvent(data, done) {
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
    if (done) done();
    document.getElementById('progressFill').style.width = '100%';
    document.getElementById('etaCard').style.display = 'none';
    document.getElementById('progressStatus').textContent = 'Done';
    resetRunBtn();
    document.getElementById('runResultPanel').style.display = 'block';
    renderRunComplete(data);
    refreshDashboard();
    refreshHistory();
    automateChecker(data);
  } else if (data.type === 'error') {
    if (done) done();
    document.getElementById('progressStatus').textContent = 'Error: ' + (data.message || 'unknown');
    resetRunBtn();
  }
}

function automateChecker(data) {
  var el = document.getElementById('runAutomate');
  if (!el || !el.checked) return;
  if ((config.sort_mode || 'cookie') !== 'cookie') return;
  if (!data.output_dir || !(data.total_hits > 0)) return;
  var hitsDir = data.output_dir.replace(/\\/g, '/') + '/hits';
  document.getElementById('checkerSourceDir').value = hitsDir;
  document.getElementById('checkerMS').checked = true;
  document.getElementById('checkerMC').checked = false;
  setCheckerMode('cookie');
  switchSection('checker');
  startCheckerRun();
}

async function startRun() {
  if ((config.sort_mode || 'cookie') === 'ulp') { startULPRun(); return; }
  var dir = document.getElementById('runSourceDir').value.trim();
  if (!dir) { alert('Enter a source directory path.'); return; }
  if (!config.filters.length) { alert('No filters configured. Add at least one target domain.'); return; }

  prepareRunUI();

  var evtSource;
  try {
    evtSource = new EventSource('/api/run/progress?dir=' + encodeURIComponent(dir));
  } catch {
    document.getElementById('progressStatus').textContent = 'Connection failed';
    resetRunBtn();
    return;
  }

  evtSource.onmessage = function(e) {
    var data;
    try { data = JSON.parse(e.data); } catch { return; }
    handleRunEvent(data, function() { evtSource.close(); });
  };
  evtSource.onerror = function() {
    evtSource.close();
    document.getElementById('progressStatus').textContent = 'Connection lost';
    resetRunBtn();
  };
}

function startULPRun() {
  var input = document.getElementById('ulpFileInput');
  if (!input.files || !input.files.length) { alert('Select at least one ULP txt file.'); return; }
  if (!config.filters.length) { alert('No filters configured. Add at least one target domain.'); return; }

  prepareRunUI();

  var fd = new FormData();
  for (var i = 0; i < input.files.length; i++) fd.append('files', input.files[i]);

  var xhr = new XMLHttpRequest();
  xhr.open('POST', '/api/run/ulp');
  var lastIdx = 0;
  var done = false;

  function parseChunk() {
    var text = xhr.responseText || '';
    var i = lastIdx;
    for (;;) {
      var j = text.indexOf('\n\n', i);
      if (j === -1) break;
      var block = text.slice(i, j);
      i = j + 2;
      var m = block.match(/^data:\s*(.*)$/m);
      if (m) {
        try { handleRunEvent(JSON.parse(m[1]), function() { done = true; }); } catch (e) {}
      }
    }
    lastIdx = i;
  }

  xhr.onprogress = parseChunk;
  xhr.onload = function() { parseChunk(); if (!done) resetRunBtn(); };
  xhr.onerror = function() {
    if (!done) {
      document.getElementById('progressStatus').textContent = 'Connection failed';
      resetRunBtn();
    }
  };
  xhr.send(fd);
}

/* ── ACCOUNT CHECKER ── */

var checkerMode = 'cookie';

function setCheckerMode(m) {
  checkerMode = m;
  document.getElementById('modeCookie').classList.toggle('active', m === 'cookie');
  document.getElementById('modeULP').classList.toggle('active', m === 'ulp');
  document.getElementById('checkerSourceHint').textContent = m === 'ulp'
    ? 'Full path to the folder containing .txt files with email:pass accounts (one per line)'
    : 'Full path to the folder containing cookie .txt files (one account per file)';
}

function prepareCheckerUI() {
  document.getElementById('startCheckerBtn').disabled = true;
  document.getElementById('checkerProgressPanel').style.display = 'block';
  document.getElementById('checkerProgressFill').style.width = '0%';
  document.getElementById('checkerProgressMeta').textContent = '0 / 0 accounts';
  document.getElementById('checkerProgressStatus').textContent = 'Initializing...';
  document.getElementById('checkerValid').textContent = '0 valid MS';
  document.getElementById('checkerMCValid').textContent = '0 valid MC';
  document.getElementById('checkerInvalid').textContent = '0 invalid';
}

function resetCheckerBtn() {
  var b = document.getElementById('startCheckerBtn');
  if (b) b.disabled = false;
}

function handleCheckerEvent(data, done) {
  if (data.type === 'progress') {
    document.getElementById('checkerProgressFill').style.width = (data.percent || 0) + '%';
    document.getElementById('checkerProgressMeta').textContent = data.processed + ' / ' + data.total + ' accounts';
    document.getElementById('checkerValid').textContent = (data.valid || 0) + ' valid MS';
    document.getElementById('checkerMCValid').textContent = (data.minecraft_valid || 0) + ' valid MC';
    document.getElementById('checkerInvalid').textContent = (data.invalid || 0) + ' invalid';
    if (data.check_status) document.getElementById('checkerProgressStatus').textContent = data.check_status;
  } else if (data.type === 'complete') {
    if (done) done();
    document.getElementById('checkerProgressFill').style.width = '100%';
    var mcTxt = (data.minecraft_valid || 0) > 0 ? ', Minecraft valid: ' + data.minecraft_valid : '';
    document.getElementById('checkerProgressStatus').textContent = 'Done - Valid Microsoft: ' + data.valid + mcTxt + ', Invalid: ' + data.invalid;
    document.getElementById('checkerValid').textContent = (data.valid || 0) + ' valid MS';
    document.getElementById('checkerMCValid').textContent = (data.minecraft_valid || 0) + ' valid MC';
    document.getElementById('checkerInvalid').textContent = (data.invalid || 0) + ' invalid';
    resetCheckerBtn();
    refreshDashboard();
    refreshHistory();
  } else if (data.type === 'error') {
    if (done) done();
    document.getElementById('checkerProgressStatus').textContent = 'Error: ' + (data.message || 'unknown');
    resetCheckerBtn();
  }
}

function startCheckerRun() {
  var dir = document.getElementById('checkerSourceDir').value.trim();
  if (!dir) { alert('Enter a source directory path.'); return; }
  var ms = document.getElementById('checkerMS').checked;
  var mc = document.getElementById('checkerMC').checked;
  if (!ms && !mc) {
    if (!confirm('No validator selected. Sort accounts only (no validation)?')) return;
  }

  prepareCheckerUI();

  var evtSource;
  try {
    evtSource = new EventSource('/api/run/checker?dir=' + encodeURIComponent(dir) + '&mode=' + checkerMode + '&microsoft=' + ms + '&minecraft=' + mc);
  } catch {
    document.getElementById('checkerProgressStatus').textContent = 'Connection failed';
    resetCheckerBtn();
    return;
  }

  evtSource.onmessage = function(e) {
    var data;
    try { data = JSON.parse(e.data); } catch { return; }
    handleCheckerEvent(data, function() { evtSource.close(); });
  };
  evtSource.onerror = function() {
    evtSource.close();
    document.getElementById('checkerProgressStatus').textContent = 'Connection lost';
    resetCheckerBtn();
  };
}

function renderRunComplete(data) {  var el = document.getElementById('runSummary');  var entries = data.domain_breakdown ? Object.entries(data.domain_breakdown).sort(function(a, b) { return b[1] - a[1]; }) : [];
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
    '<div class="summary-row"><span class="label">Total lines</span><span class="value">' + data.total_cookies + '</span></div>' +
    '<div class="summary-row"><span class="label">Total hits</span><span class="value">' + data.total_hits + '</span></div>' +
    (data.unparsed ? '<div class="summary-row"><span class="label">Lines skipped</span><span class="value" style="color:var(--red)">' + data.unparsed + '</span></div>' : '') +
    checkHtml +
    '<div class="summary-row"><span class="label">Errors</span><span class="value">' + data.errors + '</span></div>' +
    '<div class="summary-row"><span class="label">Time</span><span class="value">' + data.elapsed_seconds.toFixed(1) + 's</span></div>' +
    '<div class="summary-row"><span class="label">Output</span><span class="value" style="font-size:11px;word-break:break-all">' + esc(data.output_dir) + '</span></div>' +
    domainHtml;
}

/* ── CONVERTOR ── */
var convertSource = null;

function uploadConvertInput(input) {
  if (!input.files || !input.files.length) return;
  var file = input.files[0];
  var fd = new FormData();
  fd.append('file', file);
  var label = document.getElementById('convertInputLabel');
  if (label) label.textContent = 'Uploading...';
  var xhr = new XMLHttpRequest();
  xhr.open('POST', '/api/convert/input');
  xhr.onload = function() {
    var ok = false;
    try { var r = JSON.parse(xhr.responseText); ok = r.status === 'ok'; } catch (e) {}
    if (ok) { convertSource = file.name; if (label) label.textContent = file.name; }
    else { if (label) label.textContent = 'Upload failed'; }
  };
  xhr.onerror = function() { if (label) label.textContent = 'Upload failed'; };
  xhr.send(fd);
}

function prepareConvertUI() {
  document.getElementById('startConvertBtn').disabled = true;
  document.getElementById('stopConvertBtn').style.display = '';
  document.getElementById('convertProgressPanel').style.display = 'block';
  document.getElementById('convertResultPanel').style.display = 'none';
  document.getElementById('convertProgressFill').style.width = '0%';
  document.getElementById('convertProgressMeta').textContent = '0 / 0 accounts';
  document.getElementById('convertProgressStatus').textContent = 'Initializing...';
  document.getElementById('convertSuccess').textContent = '0 converted';
  document.getElementById('convertMFA').textContent = '0 MFA';
  document.getElementById('convertInvalid').textContent = '0 invalid';
  document.getElementById('convertLocked').textContent = '0 locked';
  document.getElementById('convertCaptcha').textContent = '0 captcha';
  document.getElementById('convertFailed').textContent = '0 failed';
  document.getElementById('convertCurrent').textContent = '—';
  document.getElementById('convertRate').textContent = '0/min';
  document.getElementById('convertETA').textContent = '—';
}

function resetConvertBtn() {
  var b = document.getElementById('startConvertBtn');
  if (b) b.disabled = false;
  var s = document.getElementById('stopConvertBtn');
  if (s) s.style.display = 'none';
}

function handleConvertEvent(data, done) {
  if (data.type === 'progress') {
    var pct = data.total ? Math.round(data.processed / data.total * 100) : 0;
    document.getElementById('convertProgressFill').style.width = pct + '%';
    document.getElementById('convertProgressMeta').textContent = data.processed + ' / ' + data.total + ' accounts';
    document.getElementById('convertProgressStatus').textContent = data.processed + ' / ' + data.total + ' processed';
    document.getElementById('convertSuccess').textContent = (data.success || 0) + ' converted';
    document.getElementById('convertMFA').textContent = (data.mfa || 0) + ' MFA';
    document.getElementById('convertInvalid').textContent = (data.invalid || 0) + ' invalid';
    document.getElementById('convertLocked').textContent = (data.locked || 0) + ' locked';
    document.getElementById('convertCaptcha').textContent = (data.captcha || 0) + ' captcha';
    document.getElementById('convertFailed').textContent = (data.failed || 0) + ' failed';
    if (data.current) document.getElementById('convertCurrent').textContent = data.current;
    if (data.rate_per_min) document.getElementById('convertRate').textContent = data.rate_per_min + '/min';
    if (data.eta_seconds) document.getElementById('convertETA').textContent = formatETA(data.eta_seconds * 1000);
  } else if (data.type === 'done') {
    if (done) done();
    document.getElementById('convertProgressFill').style.width = '100%';
    if (data.dry_run) {
      document.getElementById('convertProgressStatus').textContent = 'Dry run complete';
      var msg = '<div class="summary-row"><span class="label">Dry run</span><span class="value">OK</span></div>' +
        '<div class="summary-row"><span class="label">Total lines</span><span class="value">' + data.total + '</span></div>' +
        (data.unparsed ? '<div class="summary-row"><span class="label">Skipped</span><span class="value" style="color:var(--red)">' + data.unparsed + '</span></div>' : '') +
        '<div class="summary-row"><span class="label">Duplicates removed</span><span class="value">' + data.duplicate + '</span></div>';
      document.getElementById('convertSummary').innerHTML = msg;
    } else {
      document.getElementById('convertProgressStatus').textContent = 'Done - Converted: ' + data.success + ', Failed: ' + data.failed;
      var html = '<div class="summary-row"><span class="label">Accounts converted</span><span class="value" style="color:var(--green)">' + data.success + '</span></div>' +
        '<div class="summary-row"><span class="label">MFA / locked / captcha</span><span class="value">' + (data.mfa || 0) + ' / ' + (data.locked || 0) + ' / ' + (data.captcha || 0) + '</span></div>' +
        '<div class="summary-row"><span class="label">Invalid</span><span class="value" style="color:var(--red)">' + data.invalid + '</span></div>' +
        '<div class="summary-row"><span class="label">Failed</span><span class="value" style="color:var(--red)">' + data.failed + '</span></div>' +
        '<div class="summary-row"><span class="label">Output folder</span><span class="value" style="font-size:11px;word-break:break-all">' + esc(data.out_dir) + '</span></div>' +
        '<div class="summary-row"><span class="label">CSV log</span><span class="value" style="font-size:11px;word-break:break-all">' + esc(data.csv_path) + '</span></div>' +
        '<div class="summary-row"><span class="label">Failed list</span><span class="value" style="font-size:11px;word-break:break-all">' + esc(data.fail_path) + '</span></div>';
      document.getElementById('convertSummary').innerHTML = html;
    }
    document.getElementById('convertResultPanel').style.display = 'block';
  } else if (data.type === 'error') {
    if (done) done();
    document.getElementById('convertProgressStatus').textContent = 'Error: ' + (data.message || 'unknown');
    resetConvertBtn();
  }
}

function startConvertRun() {
  if (!convertSource) { alert('Upload an email:pass list first.'); return; }
  var concurrency = parseInt(document.getElementById('convertConcurrency').value || '25', 10);
  if (concurrency < 1 || concurrency > 50) { alert('Workers must be between 1 and 50.'); return; }
  var prefix = document.getElementById('convertPrefix').value.trim() || 'retard';
  var dry = document.getElementById('convertDryRun').checked ? '&dryrun=1' : '';
  var val = document.getElementById('convertValidate').checked ? '' : '&validate=false';
  var px = document.getElementById('convertProxyless').checked ? '&proxyless=1' : '';

  prepareConvertUI();

  var evtSource;
  try {
    evtSource = new EventSource('/api/convert/run?concurrency=' + concurrency + '&prefix=' + encodeURIComponent(prefix) + dry + val + px);
  } catch {
    document.getElementById('convertProgressStatus').textContent = 'Connection failed';
    resetConvertBtn();
    return;
  }

  evtSource.onmessage = function(e) {
    var data;
    try { data = JSON.parse(e.data); } catch { return; }
    handleConvertEvent(data, function() { evtSource.close(); });
  };
  evtSource.onerror = function() {
    evtSource.close();
    document.getElementById('convertProgressStatus').textContent = 'Connection lost';
    resetConvertBtn();
  };
}

function stopConvertRun() {
  api('POST', '/api/convert/stop').catch(function() {});
  document.getElementById('convertProgressStatus').textContent = 'Stopping...';
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

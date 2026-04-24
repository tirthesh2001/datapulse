let availableDates = [];
let selectedDates = new Set();
let mode = 'range';
let currentDatesKey = '';
let widgetConfig = null;
let availableSources = null;

let dpSupabase = null;
let accessToken = null;
let lastSyncAt = null;

function getBoot() {
  try { return window.__DP_BOOT__ || {}; } catch (e) { return {}; }
}

function authHeaders() {
  const h = {};
  if (accessToken) h['Authorization'] = 'Bearer ' + accessToken;
  return h;
}

async function apiFetch(url, opts = {}) {
  opts.headers = { ...(opts.headers || {}), ...authHeaders() };
  const ctrl = new AbortController();
  const timer = setTimeout(() => ctrl.abort(), 120000);
  if (!opts.signal) opts.signal = ctrl.signal;
  let res;
  try {
    res = await fetch(url, opts);
  } catch (e) {
    const banner = document.getElementById('coldStartBanner');
    if (banner) banner.style.display = 'flex';
    throw e;
  } finally {
    clearTimeout(timer);
  }
  const banner = document.getElementById('coldStartBanner');
  if (banner) {
    if (res.status === 502 || res.status === 503) banner.style.display = 'flex';
    else banner.style.display = 'none';
  }
  return res;
}

function keyActivate(ev) {
  if (ev.key === 'Enter' || ev.key === ' ') {
    ev.preventDefault();
    ev.target.click();
  }
}

function syncUrlState() {
  const params = new URLSearchParams();
  params.set('mode', mode);
  if (mode === 'range') {
    const s = document.getElementById('startDate')?.value;
    const e = document.getElementById('endDate')?.value;
    if (s) params.set('start', s);
    if (e) params.set('end', e);
  } else {
    const dates = Array.from(selectedDates).sort();
    if (dates.length) params.set('dates', dates.join(','));
  }
  const q = params.toString();
  history.replaceState(null, '', q ? '?' + q : window.location.pathname);
}

function readUrlState() {
  const p = new URLSearchParams(window.location.search);
  const m = p.get('mode');
  if (m === 'pick' || m === 'range') {
    mode = m;
    document.getElementById('modeRange')?.classList.toggle('active', m === 'range');
    document.getElementById('modePick')?.classList.toggle('active', m === 'pick');
    document.getElementById('rangeControls').style.display = m === 'range' ? 'flex' : 'none';
    document.getElementById('pickControls').style.display = m === 'pick' ? 'block' : 'none';
  }
  if (p.get('start')) document.getElementById('startDate').value = p.get('start');
  if (p.get('end')) document.getElementById('endDate').value = p.get('end');
  const ds = p.get('dates');
  if (ds && mode === 'pick') {
    ds.split(',').forEach(d => { if (d) selectedDates.add(d); });
  }
}

function applyPreset(preset) {
  if (!availableDates.length) return;
  const minD = availableDates[0];
  const maxD = availableDates[availableDates.length - 1];
  if (preset === '7d') {
    const end = maxD;
    const startObj = new Date(end + 'T12:00:00');
    startObj.setDate(startObj.getDate() - 6);
    let start = startObj.toISOString().slice(0, 10);
    if (start < minD) start = minD;
    document.getElementById('startDate').value = start;
    document.getElementById('endDate').value = end;
    setMode('range');
    onRangeChange();
  } else if (preset === 'month') {
    const end = new Date(maxD + 'T12:00:00');
    const start = new Date(end.getFullYear(), end.getMonth(), 1);
    let s = start.toISOString().slice(0, 10);
    if (s < minD) s = minD;
    document.getElementById('startDate').value = s;
    document.getElementById('endDate').value = maxD;
    setMode('range');
    onRangeChange();
  } else if (preset === 'prevmonth') {
    const end = new Date(maxD + 'T12:00:00');
    const firstThis = new Date(end.getFullYear(), end.getMonth(), 1);
    const lastPrev = new Date(firstThis);
    lastPrev.setDate(0);
    const firstPrev = new Date(lastPrev.getFullYear(), lastPrev.getMonth(), 1);
    let s = firstPrev.toISOString().slice(0, 10);
    let e = lastPrev.toISOString().slice(0, 10);
    if (s < minD) s = minD;
    if (e > maxD) e = maxD;
    document.getElementById('startDate').value = s;
    document.getElementById('endDate').value = e;
    setMode('range');
    onRangeChange();
  }
  syncUrlState();
}

function toggleDensity() {
  const on = document.getElementById('densityToggle')?.checked;
  document.documentElement.setAttribute('data-density', on ? 'compact' : 'comfortable');
  localStorage.setItem('dp-density', on ? 'compact' : 'comfortable');
}

function updateFreshnessBadge() {
  const el = document.getElementById('freshnessBadge');
  if (!el) return;
  const n = availableDates.length;
  let t = lastSyncAt ? new Date(lastSyncAt).toLocaleString() : '—';
  el.textContent = `Reports loaded: ${n} day(s) · Last sync: ${t}`;
}

async function initAuthUI() {
  const boot = getBoot();
  const bar = document.getElementById('authBar');
  if (!boot.authEnabled) {
    if (bar) bar.style.display = 'none';
    return;
  }
  if (bar) bar.style.display = 'flex';
  if (typeof supabase !== 'undefined' && boot.supabaseURL && boot.supabaseAnon) {
    dpSupabase = supabase.createClient(boot.supabaseURL, boot.supabaseAnon);
    const { data: { session } } = await dpSupabase.auth.getSession();
    if (session) accessToken = session.access_token;
    dpSupabase.auth.onAuthStateChange((_evt, sess) => {
      accessToken = sess ? sess.access_token : null;
      updateAuthBar();
      loadWidgetConfig();
    });
  }
  updateAuthBar();
  document.getElementById('authSend')?.addEventListener('click', async () => {
    const email = document.getElementById('authEmail')?.value?.trim();
    if (!email || !dpSupabase) return;
    const { error } = await dpSupabase.auth.signInWithOtp({ email, options: { emailRedirectTo: window.location.origin } });
    if (error) showToast(error.message, true);
    else showToast('Check your email for the sign-in link');
  });
  document.getElementById('authOut')?.addEventListener('click', async () => {
    if (dpSupabase) await dpSupabase.auth.signOut();
    accessToken = null;
    updateAuthBar();
  });
}

function updateAuthBar() {
  const boot = getBoot();
  if (!boot.authEnabled) return;
  const st = document.getElementById('authStatus');
  const out = document.getElementById('authOut');
  const em = document.getElementById('authEmail');
  const sn = document.getElementById('authSend');
  if (accessToken) {
    if (st) st.textContent = 'Signed in';
    if (out) out.style.display = 'inline-block';
    if (em) em.style.display = 'none';
    if (sn) sn.style.display = 'none';
  } else {
    if (st) st.textContent = 'Sign in to save your personal dashboard layout';
    if (out) out.style.display = 'none';
    if (em) em.style.display = 'inline-block';
    if (sn) sn.style.display = 'inline-block';
  }
}

/* ── Init ────────────────────────────────────────────────── */
function initApp(dates) {
  availableDates = dates || [];
  const boot = getBoot();
  if (boot.serverTime) lastSyncAt = boot.serverTime;
  loadPrefs();
  const dens = localStorage.getItem('dp-density');
  const dt = document.getElementById('densityToggle');
  if (dens === 'compact' && dt) {
    dt.checked = true;
    document.documentElement.setAttribute('data-density', 'compact');
  }
  readUrlState();
  setupRangeInputs();
  updatePickFilter();
  if (availableDates.length > 0) {
    const minD = availableDates[0];
    const maxD = availableDates[availableDates.length - 1];
    if (!document.getElementById('startDate').value) document.getElementById('startDate').value = minD;
    if (!document.getElementById('endDate').value) document.getElementById('endDate').value = maxD;
    if (mode === 'range') onRangeChange();
    if (mode === 'pick' && selectedDates.size > 0) {
      renderDateChips();
      loadTables();
    }
  }
  initAuthUI();
  loadWidgetConfig();
  updateFreshnessBadge();
  document.getElementById('coldStartRetry')?.addEventListener('click', () => {
    if (selectedDates.size) loadTables();
    else refreshData();
  });
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape') closeModal();
  });
}

/* ── Theme & Color ───────────────────────────────────────── */
function loadPrefs() {
  const dark = localStorage.getItem('dp-dark') === '1';
  const color = localStorage.getItem('dp-color') || 'blue';
  applyTheme(dark);
  setColor(color);
  document.getElementById('darkToggle').checked = dark;
}

function toggleDarkMode(on) {
  localStorage.setItem('dp-dark', on ? '1' : '0');
  applyTheme(on);
}

function applyTheme(dark) {
  document.documentElement.setAttribute('data-theme', dark ? 'dark' : 'light');
}

function setColor(c) {
  document.documentElement.setAttribute('data-color', c);
  localStorage.setItem('dp-color', c);
  document.querySelectorAll('.color-swatch').forEach(el => {
    el.classList.toggle('active', el.dataset.c === c);
  });
}

function toggleSettings(e) {
  e.stopPropagation();
  document.getElementById('settingsPanel').classList.toggle('open');
}
document.addEventListener('click', e => {
  const p = document.getElementById('settingsPanel');
  if (p && !e.target.closest('.settings-wrap')) p.classList.remove('open');
});

/* ── Page Tabs (Dashboard / Reports) ─────────────────────── */
function switchPage(page) {
  document.getElementById('pageDashboard').style.display = page === 'dashboard' ? '' : 'none';
  document.getElementById('pageReports').style.display = page === 'reports' ? '' : 'none';
  document.getElementById('tabDashboard').classList.toggle('active', page === 'dashboard');
  document.getElementById('tabReports').classList.toggle('active', page === 'reports');
}

/* ── Mode Toggle ─────────────────────────────────────────── */
function setMode(m) {
  mode = m;
  document.getElementById('modeRange').classList.toggle('active', m === 'range');
  document.getElementById('modePick').classList.toggle('active', m === 'pick');
  document.getElementById('rangeControls').style.display = m === 'range' ? 'flex' : 'none';
  document.getElementById('pickControls').style.display = m === 'pick' ? 'block' : 'none';
  if (m === 'range') {
    onRangeChange();
  } else {
    selectedDates.clear();
    currentDatesKey = '';
    updatePickFilter();
    renderDateChips();
  }
  syncUrlState();
}

/* ── Calendar Date Inputs (Range View) ───────────────────── */
function setupRangeInputs() {
  const startEl = document.getElementById('startDate');
  const endEl = document.getElementById('endDate');
  if (availableDates.length > 0) {
    startEl.min = availableDates[0];
    startEl.max = availableDates[availableDates.length - 1];
    endEl.min = availableDates[0];
    endEl.max = availableDates[availableDates.length - 1];
  }
}

function onRangeChange() {
  if (mode !== 'range') return;
  const start = document.getElementById('startDate').value;
  const end = document.getElementById('endDate').value;
  selectedDates.clear();
  availableDates.forEach(d => { if (d >= start && d <= end) selectedDates.add(d); });
  syncUrlState();
  loadTables();
}

/* ── Month/Year Filter + Date Chips (Pick View) ──────────── */
function getAvailableMonths() {
  const months = new Map();
  availableDates.forEach(d => {
    const key = d.substring(0, 7);
    if (!months.has(key)) {
      const dt = new Date(d + 'T00:00:00');
      months.set(key, dt.toLocaleDateString('en-IN', { month: 'long', year: 'numeric' }));
    }
  });
  return months;
}

function updatePickFilter() {
  const sel = document.getElementById('pickMonth');
  if (!sel) return;
  const months = getAvailableMonths();
  sel.innerHTML = '<option value="all">All Months</option>';
  months.forEach((label, key) => {
    sel.innerHTML += `<option value="${key}">${label}</option>`;
  });
}

function onPickMonthChange() {
  selectedDates.clear();
  currentDatesKey = '';
  renderDateChips();
}

function renderDateChips() {
  const c = document.getElementById('dateChips');
  if (!c) return;
  c.innerHTML = '';
  const monthFilter = document.getElementById('pickMonth')?.value || 'all';
  const filtered = availableDates.filter(d => monthFilter === 'all' || d.startsWith(monthFilter));

  filtered.forEach(d => {
    const chip = document.createElement('span');
    chip.textContent = fmtDate(d);
    chip.className = 'date-chip' + (selectedDates.has(d) ? ' selected' : '');
    chip.onclick = () => toggleDate(d);
    c.appendChild(chip);
  });

  if (filtered.length === 0) {
    c.innerHTML = '<span style="font-size:13px;color:var(--dp-muted)">No reports for this month</span>';
  }
}

function toggleDate(d) {
  selectedDates.has(d) ? selectedDates.delete(d) : selectedDates.add(d);
  renderDateChips();
  syncUrlState();
  if (selectedDates.size > 0) loadTables();
}

/* ── Load Tables (always auto-generates) ─────────────────── */
async function loadTables() {
  if (mode === 'range') {
    const start = document.getElementById('startDate').value;
    const end = document.getElementById('endDate').value;
    selectedDates.clear();
    availableDates.forEach(d => { if (d >= start && d <= end) selectedDates.add(d); });
  }
  const dates = Array.from(selectedDates).sort();
  const loader = document.getElementById('loader');
  if (!dates.length) {
    if (loader) loader.classList.remove('active');
    return;
  }

  const key = dates.join(',');
  if (key === currentDatesKey) return;
  currentDatesKey = key;

  const box = document.getElementById('tablesContainer');
  box.innerHTML = '';
  loader.classList.add('active');

  try {
    const res = await apiFetch('/tables', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({ dates: key }).toString()
    });
    if (!res.ok) throw new Error(await res.text() || 'Failed to load tables');
    box.innerHTML = await res.text();
  } catch (err) {
    box.innerHTML = `<div class="msg-err">${esc(err.message)}</div>`;
    currentDatesKey = '';
  } finally {
    loader.classList.remove('active');
  }
}

/* ── Refresh ─────────────────────────────────────────────── */
async function refreshData() {
  const btn = document.getElementById('refreshBtn');
  btn.classList.add('spinning');

  try {
    const res = await apiFetch('/refresh', { method: 'POST' });
    if (!res.ok) throw new Error('Refresh failed');
    const data = await res.json();
    availableDates = data.dates || [];
    lastSyncAt = data.at || new Date().toISOString();
    selectedDates.clear();
    currentDatesKey = '';
    setupRangeInputs();
    updatePickFilter();
    renderDateChips();
    if (availableDates.length > 0) {
      document.getElementById('startDate').value = availableDates[0];
      document.getElementById('endDate').value = availableDates[availableDates.length - 1];
      if (mode === 'range') onRangeChange();
    }
    updateFreshnessBadge();
    showToast('Data refreshed — ' + availableDates.length + ' report(s)');
  } catch (err) {
    showToast('Refresh failed: ' + err.message, true);
  } finally {
    btn.classList.remove('spinning');
  }
}

/* ── Export PNG ───────────────────────────────────────────── */
async function exportPNG(elId, filename) {
  const el = document.getElementById(elId);
  if (!el || typeof html2canvas === 'undefined') return;
  const dark = document.documentElement.getAttribute('data-theme') === 'dark';

  const actionsEls = el.querySelectorAll('.insights-actions, .widget-actions');
  actionsEls.forEach(a => a.style.display = 'none');

  try {
    const canvas = await html2canvas(el, {
      backgroundColor: dark ? '#1e293b' : '#ffffff',
      scale: 2,
      useCORS: true
    });
    const a = document.createElement('a');
    a.download = (filename || 'export') + '.png';
    a.href = canvas.toDataURL('image/png');
    a.click();
  } finally {
    actionsEls.forEach(a => a.style.display = '');
  }
}

/* ── Insights ────────────────────────────────────────────── */
async function loadInsights() {
  const dates = Array.from(selectedDates).sort();
  if (!dates.length) return;
  const box = document.getElementById('insightsContainer');
  box.innerHTML = '<div class="dp-loader active"><div class="spinner"></div><p>Analyzing data…</p></div>';

  try {
    const tmpl = document.getElementById('insightTemplate')?.value || 'executive';
    const res = await apiFetch('/insights', {
      method: 'POST',
      headers: { 'Content-Type': 'application/x-www-form-urlencoded' },
      body: new URLSearchParams({ dates: dates.join(','), template: tmpl }).toString()
    });
    if (!res.ok) throw new Error(await res.text() || 'Failed');
    box.innerHTML = await res.text();
  } catch (err) {
    box.innerHTML = `<div class="msg-err">${esc(err.message)}</div>`;
  }
}

function copyInsights() {
  const el = document.getElementById('insightsContent');
  if (!el) return;
  navigator.clipboard.writeText(el.innerText).then(() => {
    const btns = document.querySelectorAll('.insights-actions .action-btn');
    if (btns[0]) {
      const orig = btns[0].innerHTML;
      btns[0].innerHTML = '&#10003; Copied';
      setTimeout(() => { btns[0].innerHTML = orig; }, 1500);
    }
  });
}

/* ══════════════════════════════════════════════════════════
   WIDGET CONFIGURATION & EDITOR
   ══════════════════════════════════════════════════════════ */

async function loadWidgetConfig() {
  try {
    const res = await apiFetch('/api/config');
    if (res.ok) widgetConfig = await res.json();
  } catch (e) { /* ignore */ }
}

async function loadSources() {
  if (availableSources) return availableSources;
  try {
    const res = await apiFetch('/api/sources');
    if (res.ok) availableSources = await res.json();
  } catch (e) { /* ignore */ }
  return availableSources || {};
}

async function saveWidgetConfig() {
  try {
    const res = await apiFetch('/api/config', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(widgetConfig)
    });
    if (!res.ok) {
      const msg = await res.text();
      showToast('Save failed: ' + msg, true);
      return false;
    }
    showToast('Configuration saved');
    currentDatesKey = '';
    if (selectedDates.size > 0) loadTables();
    return true;
  } catch (err) {
    showToast('Save failed: ' + err.message, true);
    return false;
  }
}

/* ── Widget Editor Modal ─────────────────────────────────── */

function openWidgetEditor(widgetId) {
  if (!widgetConfig) return;
  const widget = widgetConfig.widgets.find(w => w.id === widgetId);
  if (!widget) return;

  document.getElementById('modalTitle').textContent = 'Edit Widget: ' + widget.label;

  let html = `
    <div class="form-group">
      <label>Widget Label</label>
      <input type="text" id="editWidgetLabel" value="${esc(widget.label)}" class="form-input">
    </div>
    <div class="form-group">
      <label>Visible</label>
      <input type="checkbox" role="switch" id="editWidgetVisible" ${widget.visible ? 'checked' : ''}>
    </div>
  `;

  widget.sections.forEach((sec, si) => {
    html += `
      <div class="editor-section">
        <h4>Section: ${esc(sec.source)}</h4>
        ${sec.label ? `<div class="form-group"><label>Section Label</label><input type="text" class="form-input sec-label" data-si="${si}" value="${esc(sec.label)}"></div>` : ''}
        <div class="form-group">
          <label>Columns</label>
          <div class="column-list" id="colList-${si}">
            ${sec.columns.map((col, ci) => `
              <div class="column-item">
                <input type="text" class="form-input col-label" data-si="${si}" data-ci="${ci}" value="${esc(col.label)}">
                <span class="col-key">${esc(col.key)}</span>
              </div>
            `).join('')}
          </div>
        </div>
        ${sec.limit ? `<div class="form-group"><label>Row Limit</label><input type="number" class="form-input sec-limit" data-si="${si}" value="${sec.limit}" min="1"></div>` : ''}
      </div>
    `;
  });

  document.getElementById('modalBody').innerHTML = html;
  document.getElementById('modalFooter').innerHTML = `
    <span class="action-btn danger-btn" onclick="removeWidget('${esc(widgetId)}')" role="button">Remove Widget</span>
    <span class="generate-btn" onclick="saveWidgetEdit('${esc(widgetId)}')" role="button">Save Changes</span>
  `;

  openModal();
}

async function saveWidgetEdit(widgetId) {
  const widget = widgetConfig.widgets.find(w => w.id === widgetId);
  if (!widget) return;

  widget.label = document.getElementById('editWidgetLabel').value.trim();
  widget.visible = document.getElementById('editWidgetVisible').checked;

  document.querySelectorAll('.sec-label').forEach(el => {
    const si = parseInt(el.dataset.si);
    if (widget.sections[si]) widget.sections[si].label = el.value.trim();
  });

  document.querySelectorAll('.col-label').forEach(el => {
    const si = parseInt(el.dataset.si);
    const ci = parseInt(el.dataset.ci);
    if (widget.sections[si] && widget.sections[si].columns[ci]) {
      widget.sections[si].columns[ci].label = el.value.trim();
    }
  });

  document.querySelectorAll('.sec-limit').forEach(el => {
    const si = parseInt(el.dataset.si);
    if (widget.sections[si]) widget.sections[si].limit = parseInt(el.value) || 0;
  });

  const ok = await saveWidgetConfig();
  if (ok) closeModal();
}

async function removeWidget(widgetId) {
  if (!confirm('Remove this widget from the dashboard?')) return;
  widgetConfig.widgets = widgetConfig.widgets.filter(w => w.id !== widgetId);
  const ok = await saveWidgetConfig();
  if (ok) closeModal();
}

/* ── Add Widget Modal ────────────────────────────────────── */

async function openAddWidget() {
  const sources = await loadSources();
  if (!sources || Object.keys(sources).length === 0) {
    showToast('No data sources available. Refresh data first.', true);
    return;
  }

  document.getElementById('modalTitle').textContent = 'Add Widget';

  let html = `
    <div class="form-group">
      <label>Widget Label</label>
      <input type="text" id="newWidgetLabel" class="form-input" placeholder="My Widget">
    </div>
    <div class="form-group">
      <label>Data Source</label>
      <select id="newWidgetSource" class="form-input" onchange="onSourceSelect()">
        <option value="">Select a source…</option>
        ${Object.keys(sources).sort().map(s => `<option value="${esc(s)}">${esc(s)}</option>`).join('')}
      </select>
    </div>
    <div id="sourceColumns" class="form-group" style="display:none">
      <label>Columns (check to include)</label>
      <div id="sourceColList"></div>
    </div>
    <div class="form-group">
      <label><input type="checkbox" id="newWidgetAgg"> Aggregate rows (sum across dates)</label>
    </div>
    <div class="form-group">
      <label>Row Limit (0 = no limit)</label>
      <input type="number" id="newWidgetLimit" class="form-input" value="0" min="0">
    </div>
  `;

  document.getElementById('modalBody').innerHTML = html;
  document.getElementById('modalFooter').innerHTML = `
    <span class="generate-btn" onclick="saveNewWidget()" role="button">Add Widget</span>
  `;

  openModal();
}

function onSourceSelect() {
  const source = document.getElementById('newWidgetSource').value;
  const colDiv = document.getElementById('sourceColumns');
  const colList = document.getElementById('sourceColList');

  if (!source || !availableSources || !availableSources[source]) {
    colDiv.style.display = 'none';
    return;
  }

  const headers = availableSources[source];
  colDiv.style.display = 'block';
  colList.innerHTML = `
    <div class="column-item">
      <label><input type="checkbox" class="src-col" value="_date" checked> Date</label>
    </div>
    ${headers.map(h => `
      <div class="column-item">
        <label><input type="checkbox" class="src-col" value="${esc(h)}" checked> ${esc(h)}</label>
      </div>
    `).join('')}
  `;

  if (!document.getElementById('newWidgetLabel').value.trim()) {
    document.getElementById('newWidgetLabel').value = source.split(' ').map(
      w => w.charAt(0).toUpperCase() + w.slice(1).toLowerCase()
    ).join(' ');
  }
}

async function saveNewWidget() {
  const label = document.getElementById('newWidgetLabel').value.trim();
  const source = document.getElementById('newWidgetSource').value;
  const aggregate = document.getElementById('newWidgetAgg').checked;
  const limit = parseInt(document.getElementById('newWidgetLimit').value) || 0;

  if (!label) { showToast('Please enter a widget label', true); return; }
  if (!source) { showToast('Please select a data source', true); return; }

  const checkedCols = document.querySelectorAll('.src-col:checked');
  if (checkedCols.length === 0) { showToast('Select at least one column', true); return; }

  const columns = [];
  checkedCols.forEach(cb => {
    const key = cb.value;
    const lbl = key === '_date' ? 'Date' : key;
    const col = { key, label: lbl };
    if (key !== '_date' && key !== '_rank') col.format = 'K';
    columns.push(col);
  });

  const id = 'widget-' + Date.now();
  const section = { source, columns };
  if (aggregate) section.aggregate = true;
  if (limit > 0) section.limit = limit;

  const widget = { id, label, visible: true, sections: [section] };

  if (!widgetConfig) widgetConfig = { widgets: [] };
  widgetConfig.widgets.push(widget);

  const ok = await saveWidgetConfig();
  if (ok) closeModal();
}

/* ── Modal Helpers ───────────────────────────────────────── */

function openModal() {
  document.getElementById('modalOverlay').classList.add('open');
  document.body.style.overflow = 'hidden';
}

function closeModal(e) {
  if (e && e.target !== document.getElementById('modalOverlay')) return;
  document.getElementById('modalOverlay').classList.remove('open');
  document.body.style.overflow = '';
}

/* ── Toast ───────────────────────────────────────────────── */

function showToast(msg, isError) {
  const t = document.getElementById('toast');
  t.textContent = msg;
  t.className = 'toast show' + (isError ? ' error' : '');
  setTimeout(() => { t.className = 'toast'; }, 3000);
}

/* ══════════════════════════════════════════════════════════
   FILE UPLOAD
   ══════════════════════════════════════════════════════════ */

let pendingUploadFile = null;
let pendingUploadDate = null;

async function openUploadHistoryModal() {
  document.getElementById('modalTitle').textContent = 'Upload history';
  document.getElementById('modalBody').innerHTML = '<div class="dp-loader active"><div class="spinner"></div><p>Loading…</p></div>';
  document.getElementById('modalFooter').innerHTML = '';
  openModal();
  try {
    const res = await apiFetch('/api/uploads');
    const data = await res.json();
    if (!res.ok) {
      document.getElementById('modalBody').innerHTML =
        `<div class="msg-err">${esc(data.error || 'Failed to load upload history')}</div>`;
      return;
    }
    if (!Array.isArray(data) || data.length === 0) {
      document.getElementById('modalBody').innerHTML = '<p style="color:var(--dp-muted)">No uploads recorded.</p>';
      return;
    }
    const rows = data.map(u => {
      const t = u.uploaded_at ? new Date(u.uploaded_at).toLocaleString() : '—';
      return `<tr><td>${esc(u.filename || '')}</td><td>${esc(u.report_date || '')}</td><td class="num">${esc(String(u.size_raw ?? ''))}</td><td>${esc(t)}</td></tr>`;
    }).join('');
    document.getElementById('modalBody').innerHTML =
      `<div class="table-wrap upload-history-wrap"><table><thead><tr><th>File</th><th>Report date</th><th>Size (bytes)</th><th>Uploaded</th></tr></thead><tbody>${rows}</tbody></table></div>`;
  } catch (e) {
    document.getElementById('modalBody').innerHTML = `<div class="msg-err">${esc(e.message)}</div>`;
  }
}

function openUploadModal() {
  document.getElementById('modalTitle').textContent = 'Upload EML Report';
  document.getElementById('modalBody').innerHTML = `
    <div class="upload-zone" id="uploadZone">
      <svg width="40" height="40" fill="none" stroke="currentColor" viewBox="0 0 24 24" style="color:var(--dp-muted)">
        <path stroke-linecap="round" stroke-linejoin="round" stroke-width="1.5" d="M7 16a4 4 0 01-.88-7.903A5 5 0 1115.9 6L16 6a5 5 0 011 9.9M15 13l-3-3m0 0l-3 3m3-3v12"/>
      </svg>
      <p>Drag & drop <strong>.eml</strong> file(s) here<br><small>or click to browse (multiple allowed)</small></p>
      <input type="file" id="uploadFileInput" accept=".eml" multiple style="display:none" onchange="handleFileSelect(this.files)">
    </div>
    <div id="uploadStatus" style="margin-top:12px"></div>
  `;
  document.getElementById('modalFooter').innerHTML = '';

  openModal();

  const zone = document.getElementById('uploadZone');
  zone.onclick = () => document.getElementById('uploadFileInput').click();
  zone.ondragover = (e) => { e.preventDefault(); zone.classList.add('drag-over'); };
  zone.ondragleave = () => zone.classList.remove('drag-over');
  zone.ondrop = (e) => {
    e.preventDefault();
    zone.classList.remove('drag-over');
    handleFileSelect(e.dataTransfer.files);
  };
}

function handleFileSelect(files) {
  if (!files || files.length === 0) return;
  const list = Array.from(files).filter(f => f.name.toLowerCase().endsWith('.eml'));
  if (list.length === 0) {
    showToast('Only .eml files are accepted', true);
    return;
  }
  if (list.length === 1) {
    uploadFile(list[0], false);
    return;
  }
  uploadBulkSequential(list);
}

async function uploadBulkSequential(files) {
  const statusEl = document.getElementById('uploadStatus');
  let ok = 0;
  let fail = 0;
  for (let i = 0; i < files.length; i++) {
    const file = files[i];
    statusEl.innerHTML = `<div class="bulk-progress">Uploading ${i + 1} / ${files.length}: <strong>${esc(file.name)}</strong>…</div>`;
    const form = new FormData();
    form.append('file', file);
    try {
      const res = await apiFetch('/upload', { method: 'POST', body: form });
      const data = await res.json();
      if (data.error) {
        fail++;
        statusEl.innerHTML += `<div class="msg-err">${esc(data.error)}</div>`;
        continue;
      }
      if (data.conflict) {
        fail++;
        statusEl.innerHTML += `<div class="msg-err">Skipped (date conflict): ${esc(file.name)} — upload alone to replace</div>`;
        continue;
      }
      ok++;
      if (data.dates) {
        availableDates = data.dates;
        setupRangeInputs();
        updatePickFilter();
        renderDateChips();
      }
    } catch (err) {
      fail++;
      statusEl.innerHTML += `<div class="msg-err">${esc(err.message)}</div>`;
    }
  }
  statusEl.innerHTML += `<div class="msg-ok">Finished: ${ok} uploaded, ${fail} skipped or failed</div>`;
  showToast(`Bulk upload: ${ok} ok, ${fail} skipped/failed`);
  setTimeout(() => {
    closeModal();
    if (ok > 0) refreshData();
  }, 900);
}

async function uploadFile(file, replace) {
  const statusEl = document.getElementById('uploadStatus');
  statusEl.innerHTML = '<div class="dp-loader active" style="padding:12px 0"><div class="spinner"></div><p>Uploading…</p></div>';
  document.getElementById('modalFooter').innerHTML = '';

  const form = new FormData();
  form.append('file', file);
  if (replace) form.append('replace', 'true');

  try {
    const res = await apiFetch('/upload', { method: 'POST', body: form });
    const data = await res.json();

    if (data.error) {
      statusEl.innerHTML = `<div class="msg-err">${esc(data.error)}</div>`;
      return;
    }

    if (data.conflict) {
      pendingUploadFile = file;
      pendingUploadDate = data.date;
      statusEl.innerHTML = `
        <div class="upload-conflict">
          <p><strong>Date conflict:</strong> A report for <strong>${esc(data.date)}</strong> already exists.</p>
          <p>Existing: <code>${esc(data.existingFile)}</code><br>New: <code>${esc(data.newFile)}</code></p>
        </div>
      `;
      document.getElementById('modalFooter').innerHTML = `
        <span class="action-btn" onclick="closeModal()" role="button">Cancel</span>
        <span class="generate-btn" onclick="confirmReplace()" role="button">Replace Existing</span>
      `;
      return;
    }

    statusEl.innerHTML = `<div class="msg-ok">Uploaded <strong>${esc(data.file)}</strong> for ${esc(data.date)}</div>`;
    if (data.dates) {
      availableDates = data.dates;
      setupRangeInputs();
      updatePickFilter();
      renderDateChips();
    }
    showToast('Report uploaded for ' + data.date);
    setTimeout(() => {
      closeModal();
      refreshData();
    }, 800);
  } catch (err) {
    statusEl.innerHTML = `<div class="msg-err">Upload failed: ${esc(err.message)}</div>`;
  }
}

async function confirmReplace() {
  if (pendingUploadFile) {
    await uploadFile(pendingUploadFile, true);
    pendingUploadFile = null;
    pendingUploadDate = null;
  }
}

/* ── Util ────────────────────────────────────────────────── */
function fmtDate(iso) {
  const d = new Date(iso + 'T00:00:00');
  return d.toLocaleDateString('en-IN', { day: 'numeric', month: 'short', year: 'numeric' });
}
function esc(s) {
  const d = document.createElement('div'); d.textContent = s; return d.innerHTML;
}

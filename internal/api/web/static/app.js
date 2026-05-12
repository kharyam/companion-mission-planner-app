// kam-transfer admin UI — vanilla JS, no build step.
// Calls the same JSON+multipart API that KAM Mission Planner uses.

const $ = (id) => document.getElementById(id);

const state = {
  selectedDevice: null,
  selectedSlot: null,
  file: null,
};

// ---------- toast/log ----------

function toast(kind, title, detail) {
  const el = document.createElement('div');
  el.className = `toast ${kind}`;
  el.innerHTML = `<div class="toast-title"></div><div class="toast-detail"></div>`;
  el.querySelector('.toast-title').textContent = title;
  if (detail) el.querySelector('.toast-detail').textContent = detail;
  $('log').appendChild(el);
  setTimeout(() => {
    el.style.opacity = '0';
    el.style.transition = 'opacity 300ms ease';
    setTimeout(() => el.remove(), 300);
  }, kind === 'bad' ? 8000 : 4500);
}

// ---------- api helpers ----------

async function api(path, opts = {}) {
  const res = await fetch(path, opts);
  const text = await res.text();
  let body;
  try { body = text ? JSON.parse(text) : null; } catch { body = { _raw: text }; }
  if (!res.ok) {
    const err = body?.error || { code: 'HTTP_' + res.status, message: text || res.statusText };
    throw err;
  }
  return body;
}

function bytesHuman(n) {
  if (!n && n !== 0) return '—';
  if (n < 1024) return n + ' B';
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + ' KB';
  return (n / 1024 / 1024).toFixed(2) + ' MB';
}

function timeHuman(iso) {
  if (!iso || iso === '0001-01-01T00:00:00Z') return '—';
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleString();
}

// ---------- health ----------

async function pollHealth() {
  try {
    const h = await api('/api/health');
    $('health-dot').className = 'dot ok';
    $('health-label').textContent = 'online';
    $('version-label').textContent = h.version || '';
  } catch (e) {
    $('health-dot').className = 'dot bad';
    $('health-label').textContent = 'offline';
  }
}

// ---------- devices ----------

async function loadDevices() {
  const list = $('device-list');
  $('devices-panel').classList.add('loading');
  list.innerHTML = '<li class="placeholder loading">Scanning USB bus…</li>';
  try {
    const { devices } = await api('/api/devices');
    list.innerHTML = '';
    if (!devices?.length) {
      list.innerHTML = '<li class="placeholder">no devices found — plug a controller in then refresh</li>';
      return;
    }
    for (const d of devices) {
      const li = document.createElement('li');
      li.dataset.id = d.id;
      const stateBadge = d.authorized
        ? `<span class="badge ok">${d.state || 'online'}</span>`
        : `<span class="badge warn">${d.state || 'pending'}</span>`;
      const transportBadge = `<span class="badge ${d.connectionType}">${(d.connectionType || '').toUpperCase()}</span>`;
      const djiBadge = d.djiFlyDetected
        ? `<span class="badge ok">DJI Fly</span>`
        : `<span class="badge bad">no DJI Fly</span>`;
      li.innerHTML = `
        <div>
          <div class="dev-name">${escapeHTML(d.model || 'DJI device')}</div>
          <div class="dev-id">${escapeHTML(d.id)}</div>
          ${d.hint ? `<div class="dev-meta dim">${escapeHTML(d.hint)}</div>` : ''}
        </div>
        <div class="dev-meta">
          ${transportBadge}
          ${stateBadge}
          ${djiBadge}
        </div>
      `;
      li.addEventListener('click', () => selectDevice(d));
      if (state.selectedDevice?.id === d.id) li.classList.add('selected');
      list.appendChild(li);
    }
  } catch (e) {
    list.innerHTML = '';
    toast('bad', 'Could not list devices', e.message || e.code);
  } finally {
    $('devices-panel').classList.remove('loading');
  }
}

async function selectDevice(d) {
  state.selectedDevice = d;
  state.selectedSlot = null;
  for (const li of $('device-list').children) li.classList.toggle('selected', li.dataset.id === d.id);
  $('slots-panel').classList.remove('hidden');
  $('transfer-panel').classList.add('hidden');
  $('slots-device-name').textContent = `on ${d.model || d.id}`;
  await loadSlots();
}

// ---------- slots ----------

async function loadSlots() {
  if (!state.selectedDevice) return;
  const list = $('slot-list');
  $('slots-panel').classList.add('loading');
  list.innerHTML = '<li class="placeholder loading">Walking waypoint folder on device…</li>';
  try {
    const { slots } = await api(`/api/devices/${encodeURIComponent(state.selectedDevice.id)}/slots`);
    list.innerHTML = '';
    if (!slots?.length) {
      list.innerHTML = '<li class="placeholder">no slots — create placeholder missions in DJI Fly first</li>';
      return;
    }
    for (const s of slots) {
      const li = document.createElement('li');
      li.dataset.guid = s.guid;
      // Cache-bust on lastModified so re-uploads visibly refresh.
      const cacheBust = s.lastModified ? `?v=${encodeURIComponent(s.lastModified)}` : '';
      const thumb = s.previewAvailable
        ? `<img class="slot-thumb" src="${s.previewUrl}${cacheBust}" alt="" loading="lazy">`
        : `<div class="slot-thumb slot-thumb-empty">no preview</div>`;
      li.innerHTML = `
        ${thumb}
        <div class="slot-info">
          <div class="slot-name-row">
            <span class="slot-name" data-role="name">${escapeHTML(s.name || 'Slot')}</span>
            <button type="button" class="rename-btn" title="Rename slot" data-role="rename">✎</button>
            <button type="button" class="rename-btn" title="Regenerate preview from on-device KMZ" data-role="regen">↻</button>
            <button type="button" class="rename-btn" title="Push per-waypoint images (overwrites any drone photos)" data-role="wp">⎙</button>
          </div>
          <div class="slot-guid">${escapeHTML(s.guid)}</div>
        </div>
        <div class="slot-meta">
          <span>${bytesHuman(s.fileSize)}</span>
          <span>${timeHuman(s.lastModified)}</span>
        </div>
      `;
      li.addEventListener('click', (e) => {
        // pencil click handled separately
        if (e.target.dataset?.role === 'rename') return;
        selectSlot(s);
      });
      li.querySelector('[data-role="rename"]').addEventListener('click', (e) => {
        e.stopPropagation();
        startRename(li, s);
      });
      li.querySelector('[data-role="regen"]').addEventListener('click', async (e) => {
        e.stopPropagation();
        await regeneratePreview(s, e.currentTarget);
      });
      li.querySelector('[data-role="wp"]').addEventListener('click', async (e) => {
        e.stopPropagation();
        await pushWaypointImages(s, e.currentTarget);
      });
      if (state.selectedSlot?.guid === s.guid) li.classList.add('selected');
      list.appendChild(li);
    }
  } catch (e) {
    list.innerHTML = '';
    toast('bad', 'Could not list slots', e.message || e.code);
  } finally {
    $('slots-panel').classList.remove('loading');
  }
}

// ---------- push waypoint images ----------

async function pushWaypointImages(slot, btn) {
  const original = btn.textContent;
  btn.textContent = '⋯';
  btn.disabled = true;
  try {
    const body = await api(`/api/devices/${encodeURIComponent(state.selectedDevice.id)}/slots/${encodeURIComponent(slot.guid)}/waypoint-images`, {
      method: 'POST',
    });
    toast('ok', `Pushed ${body.count} waypoint image${body.count === 1 ? '' : 's'}`,
      'Visible in DJI Fly mission editor next to each waypoint');
  } catch (err) {
    toast('bad', err.code || 'Push failed', err.message || JSON.stringify(err));
  } finally {
    btn.textContent = original;
    btn.disabled = false;
  }
}

// ---------- regenerate preview ----------

async function regeneratePreview(slot, btn) {
  const originalText = btn.textContent;
  btn.textContent = '⋯';
  btn.disabled = true;
  try {
    await api(`/api/devices/${encodeURIComponent(state.selectedDevice.id)}/slots/${encodeURIComponent(slot.guid)}/preview/regenerate`, {
      method: 'POST',
    });
    toast('ok', 'Preview regenerated', 'Pushed fresh JPEG to the device');
    await loadSlots(); // re-fetch list to bust thumbnail cache
  } catch (err) {
    toast('bad', err.code || 'Regen failed', err.message || JSON.stringify(err));
  } finally {
    btn.textContent = originalText;
    btn.disabled = false;
  }
}

// ---------- rename ----------

function startRename(li, slot) {
  const nameEl = li.querySelector('[data-role="name"]');
  const current = nameEl.textContent;
  const input = document.createElement('input');
  input.type = 'text';
  input.value = current;
  input.className = 'slot-name-edit';
  input.maxLength = 80;
  nameEl.replaceWith(input);
  input.focus();
  input.select();

  let resolved = false;
  const finish = async (save) => {
    if (resolved) return;
    resolved = true;
    const newName = input.value.trim();
    const restore = document.createElement('span');
    restore.className = 'slot-name';
    restore.dataset.role = 'name';
    restore.textContent = save ? (newName || 'Slot') : current;
    input.replaceWith(restore);
    if (!save || newName === current) return;
    try {
      if (newName) {
        await api(`/api/devices/${encodeURIComponent(state.selectedDevice.id)}/slots/${encodeURIComponent(slot.guid)}/name`, {
          method: 'PUT',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ name: newName }),
        });
      } else {
        await api(`/api/devices/${encodeURIComponent(state.selectedDevice.id)}/slots/${encodeURIComponent(slot.guid)}/name`, {
          method: 'DELETE',
        });
      }
      toast('ok', 'Slot renamed', newName || '(cleared)');
      slot.name = newName; // local cache
    } catch (err) {
      toast('bad', 'Rename failed', err.message || err.code);
      restore.textContent = current;
    }
  };
  input.addEventListener('keydown', (e) => {
    if (e.key === 'Enter') { e.preventDefault(); finish(true); }
    else if (e.key === 'Escape') { e.preventDefault(); finish(false); }
  });
  input.addEventListener('blur', () => finish(true));
}

function selectSlot(s) {
  state.selectedSlot = s;
  for (const li of $('slot-list').children) li.classList.toggle('selected', li.dataset.guid === s.guid);
  $('transfer-panel').classList.remove('hidden');
  $('transfer-target').textContent = `→ ${s.guid}`;
  updateTransferButton();
  $('transfer-panel').scrollIntoView({ behavior: 'smooth', block: 'nearest' });
}

// ---------- transfer ----------

async function pickFile(file) {
  if (!file) return;
  if (!/\.kmz$/i.test(file.name)) {
    toast('warn', 'That doesn’t look like a KMZ', file.name);
  }
  state.file = file;
  $('file-meta').textContent = `${file.name} · ${bytesHuman(file.size)}`;
  updateTransferButton();
  await autoInspect(file);
}

// autoInspect uploads the picked KMZ to /api/kmz/inspect and auto-fills
// the previewMetadata textarea with the extracted waypoints + name.
// Silently no-ops if the user has typed their own JSON already.
async function autoInspect(file) {
  const ta = $('preview-metadata');
  if (ta.value.trim()) return; // don't clobber user's input

  const fd = new FormData();
  fd.append('kmz', file);
  try {
    const res = await fetch('/api/kmz/inspect', { method: 'POST', body: fd });
    const body = await res.json().catch(() => ({}));
    if (!res.ok) {
      toast('warn', 'KMZ inspect failed', body?.error?.message || res.statusText);
      return;
    }
    const payload = {
      name: body.name,
      date: body.date,
      waypoints: body.waypoints,
    };
    // Strip null/empty fields so the JSON stays tidy.
    Object.keys(payload).forEach(k => {
      if (payload[k] == null || (Array.isArray(payload[k]) && payload[k].length === 0)) {
        delete payload[k];
      }
    });
    ta.value = JSON.stringify(payload, null, 2);
    // Also pre-fill the mission name if user hasn't typed one.
    if (!$('mission-name').value.trim() && body.name) {
      $('mission-name').value = body.name;
    }
    toast('ok', `Parsed ${body.count} waypoint${body.count === 1 ? '' : 's'}`,
      body.source ? `from ${body.source}` : null);
  } catch (err) {
    toast('warn', 'KMZ inspect threw', err.message);
  }
}

function updateTransferButton() {
  const ready = state.file && state.selectedDevice && state.selectedSlot;
  $('transfer-button').disabled = !ready;
  $('transfer-hint').textContent = ready
    ? 'ready'
    : state.selectedSlot
      ? 'pick a file to enable'
      : 'pick a slot first';
}

async function submitTransfer(e) {
  e.preventDefault();
  if (!state.file || !state.selectedDevice || !state.selectedSlot) return;

  const url = `/api/devices/${encodeURIComponent(state.selectedDevice.id)}/slots/${encodeURIComponent(state.selectedSlot.guid)}/transfer`;
  const fd = new FormData();
  fd.append('kmz', state.file);

  const name = $('mission-name').value.trim();
  if (name) fd.append('name', name);

  const previewRaw = $('preview-metadata').value.trim();
  if (previewRaw) {
    try {
      JSON.parse(previewRaw); // validate before sending
      fd.append('previewMetadata', previewRaw);
    } catch (err) {
      toast('bad', 'previewMetadata is not valid JSON', err.message);
      return;
    }
  }

  if ($('push-wp-images').checked) {
    fd.append('pushWaypointImages', 'true');
  }

  const btn = $('transfer-button');
  btn.disabled = true;
  btn.classList.add('loading');
  btn.textContent = 'Transferring';
  $('transfer-hint').textContent = `${state.file.name} → ${state.selectedSlot.guid.slice(0, 8)}…`;

  const startedAt = performance.now();
  try {
    const res = await fetch(url, { method: 'POST', body: fd });
    const body = await res.json().catch(() => ({}));
    if (!res.ok) {
      const err = body.error || { message: res.statusText };
      throw err;
    }
    const elapsed = Math.round(performance.now() - startedAt);
    showModal('ok', 'Transfer complete', {
      File: state.file.name,
      Slot: state.selectedSlot.guid,
      'Device': state.selectedDevice.id,
      Size: bytesHuman(body.fileSize),
      Elapsed: `${elapsed} ms`,
      At: body.transferredAt || new Date().toISOString(),
    });
    await loadSlots();
  } catch (err) {
    showModal('bad', err.code || 'Transfer failed', {
      Reason: err.message || JSON.stringify(err),
      Slot: state.selectedSlot.guid,
      File: state.file?.name || '(none)',
    });
  } finally {
    btn.classList.remove('loading');
    btn.textContent = 'Transfer';
    updateTransferButton();
  }
}

// ---------- modal ----------

function showModal(kind, title, details) {
  const backdrop = document.createElement('div');
  backdrop.className = 'modal-backdrop';
  const icon = kind === 'ok' ? '✓' : '✕';
  const subtitle = kind === 'ok'
    ? 'The KMZ landed on the controller. Open DJI Fly to verify the slot.'
    : 'Something went wrong before the bytes landed.';
  const tip = kind === 'ok' ? `
    <div class="modal-tip">
      <strong>To keep this preview visible in DJI Fly:</strong> tap the slot
      and fly directly. If you open the slot's editor and press <em>Save</em>,
      DJI Fly regenerates the thumbnail and overwrites this one. Use the
      <span class="kbd">↻</span> button next to the slot to re-push afterwards.
    </div>
  ` : '';
  backdrop.innerHTML = `
    <div class="modal ${kind}" role="dialog" aria-modal="true" aria-labelledby="modal-title">
      <div class="modal-icon">${icon}</div>
      <h3 class="modal-title" id="modal-title"></h3>
      <p class="modal-subtitle"></p>
      <div class="modal-detail"><dl></dl></div>
      ${tip}
      <div class="modal-actions">
        <button type="button" class="primary modal-close">OK</button>
      </div>
    </div>
  `;
  backdrop.querySelector('.modal-title').textContent = title;
  backdrop.querySelector('.modal-subtitle').textContent = subtitle;
  const dl = backdrop.querySelector('dl');
  for (const [k, v] of Object.entries(details)) {
    const dt = document.createElement('dt'); dt.textContent = k;
    const dd = document.createElement('dd'); dd.textContent = v;
    dl.append(dt, dd);
  }
  const close = () => backdrop.remove();
  backdrop.querySelector('.modal-close').addEventListener('click', close);
  backdrop.addEventListener('click', (e) => { if (e.target === backdrop) close(); });
  document.addEventListener('keydown', function onKey(e) {
    if (e.key === 'Escape') { close(); document.removeEventListener('keydown', onKey); }
  });
  document.body.appendChild(backdrop);
  backdrop.querySelector('.modal-close').focus();
}

// ---------- dropzone ----------

function wireDropzone() {
  const dz = $('dropzone');
  const input = $('kmz-file');
  dz.addEventListener('click', () => input.click());
  input.addEventListener('change', () => pickFile(input.files[0]));
  ['dragenter', 'dragover'].forEach(ev => dz.addEventListener(ev, e => {
    e.preventDefault();
    dz.classList.add('dragover');
  }));
  ['dragleave', 'drop'].forEach(ev => dz.addEventListener(ev, e => {
    e.preventDefault();
    dz.classList.remove('dragover');
  }));
  dz.addEventListener('drop', e => {
    e.preventDefault();
    if (e.dataTransfer.files.length) pickFile(e.dataTransfer.files[0]);
  });
}

// ---------- websocket events ----------

function wireEvents() {
  const proto = location.protocol === 'https:' ? 'wss:' : 'ws:';
  const ws = new WebSocket(`${proto}//${location.host}/api/events`);
  ws.addEventListener('message', (m) => {
    let ev;
    try { ev = JSON.parse(m.data); } catch { return; }
    toast('info', ev.type, ev.deviceId ? `device ${ev.deviceId.slice(0, 12)}…` : '');
    if (ev.type?.startsWith('device.')) loadDevices();
    if (ev.type === 'transfer.completed' && state.selectedDevice) loadSlots();
  });
  ws.addEventListener('close', () => setTimeout(wireEvents, 2000));
  ws.addEventListener('error', () => {});
}

// ---------- misc ----------

function escapeHTML(s) {
  return String(s ?? '').replace(/[&<>"']/g, c => ({
    '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;'
  }[c]));
}

// ---------- bootstrap ----------

window.addEventListener('DOMContentLoaded', async () => {
  wireDropzone();
  $('transfer-form').addEventListener('submit', submitTransfer);
  $('refresh-devices').addEventListener('click', loadDevices);
  $('refresh-slots').addEventListener('click', loadSlots);

  await pollHealth();
  setInterval(pollHealth, 10000);
  await loadDevices();
  wireEvents();
});

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

// The server's auth middleware accepts the token via:
//   - X-KAM-Token header (preferred for REST — keeps it out of URLs/logs)
//   - Authorization: Bearer …
//   - ?token=… query (used by the WebSocket since browsers can't set
//     headers on the WS handshake)
// We cache it in sessionStorage so the URL ?token=… is only needed
// once per tab. The bootstrap captures it on load.
const TOKEN_KEY = 'kamToken';

function getToken() {
  try { return sessionStorage.getItem(TOKEN_KEY) || ''; } catch { return ''; }
}

function setToken(t) {
  try {
    if (t) sessionStorage.setItem(TOKEN_KEY, t);
    else sessionStorage.removeItem(TOKEN_KEY);
  } catch {}
}

// captureTokenFromURL pulls ?token=… out of the current URL, stores it,
// and strips it from the address bar so it doesn't sit in history or
// get copied into a bookmark.
function captureTokenFromURL() {
  const url = new URL(location.href);
  const t = url.searchParams.get('token');
  if (t) {
    setToken(t);
    url.searchParams.delete('token');
    history.replaceState(null, '', url.toString());
  }
}

// withAuth returns a copy of opts with X-KAM-Token merged into headers.
// Use for every fetch the UI makes — both api() and the direct fetch
// calls for FormData uploads (which don't go through api()).
function withAuth(opts = {}) {
  const t = getToken();
  if (!t) return opts;
  const headers = new Headers(opts.headers || {});
  headers.set('X-KAM-Token', t);
  return { ...opts, headers };
}

// withAuthURL appends ?token=… (or &token=…) to a URL. Required for
// browser-driven loads that we can't put a header on: <img src>,
// <a href> downloads, and the WebSocket. The server's auth middleware
// accepts the token via query as well as header for exactly this case.
function withAuthURL(url) {
  const t = getToken();
  if (!t) return url;
  const sep = url.includes('?') ? '&' : '?';
  return `${url}${sep}token=${encodeURIComponent(t)}`;
}

async function api(path, opts = {}) {
  const res = await fetch(path, withAuth(opts));
  if (res.status === 401) {
    // Token is missing or wrong — drop the stale value and reprompt.
    setToken('');
    await promptForToken('Session token rejected (401). Paste a valid token to continue.');
    // After the user supplies one, retry once.
    const retry = await fetch(path, withAuth(opts));
    const retryText = await retry.text();
    let retryBody;
    try { retryBody = retryText ? JSON.parse(retryText) : null; } catch { retryBody = { _raw: retryText }; }
    if (!retry.ok) {
      const err = retryBody?.error || { code: 'HTTP_' + retry.status, message: retryText || retry.statusText };
      throw err;
    }
    return retryBody;
  }
  const text = await res.text();
  let body;
  try { body = text ? JSON.parse(text) : null; } catch { body = { _raw: text }; }
  if (!res.ok) {
    const err = body?.error || { code: 'HTTP_' + res.status, message: text || res.statusText };
    throw err;
  }
  return body;
}

// promptForToken shows a modal asking the user to paste a token.
// Empty submission is allowed — if the server has auth disabled the
// middleware ignores the token, and if it's enabled api()'s own 401
// handler will re-prompt. Backdrop click / Escape are not offered
// because the surrounding UI is non-functional without an answer.
function promptForToken(message) {
  return new Promise((resolve) => {
    const backdrop = document.createElement('div');
    backdrop.className = 'modal-backdrop';
    backdrop.innerHTML = `
      <div class="modal" role="dialog" aria-modal="true">
        <h3 class="modal-title">Authentication token</h3>
        <p class="modal-subtitle"></p>
        <input type="password" class="token-input" autocomplete="off" spellcheck="false"
               style="width:100%;padding:8px;margin-top:8px;font-family:monospace">
        <div class="modal-actions" style="margin-top:12px;display:flex;gap:8px;justify-content:flex-end">
          <button type="button" class="ghost skip">No auth</button>
          <button type="button" class="primary save">Save</button>
        </div>
      </div>
    `;
    backdrop.querySelector('.modal-subtitle').textContent = message
      || 'Paste the auth token configured in the server’s config.yaml (auth.token). If your server runs without auth, choose "No auth".';
    document.body.appendChild(backdrop);
    const input = backdrop.querySelector('.token-input');
    const finish = (v) => {
      setToken(v || '');
      backdrop.remove();
      resolve(v || '');
    };
    backdrop.querySelector('.save').addEventListener('click', () => finish(input.value.trim()));
    backdrop.querySelector('.skip').addEventListener('click', () => finish(''));
    input.addEventListener('keydown', (e) => { if (e.key === 'Enter') finish(input.value.trim()); });
    input.focus();
  });
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
    renderBattery(h.battery);
  } catch (e) {
    $('health-dot').className = 'dot bad';
    $('health-label').textContent = 'offline';
    renderBattery(null);
  }
}

// ---------- system panel ----------

const MIRROR_REFRESH_MS = 5000;
let mirrorPage = 'status';
let mirrorTimer = 0;

async function pollSystem() {
  try {
    const s = await api('/api/system');
    $('sys-host').textContent = s.hostname || '—';
    setTailscaleRow(s.tailscale);
    $('sys-net').textContent = formatNet(s.net);
    $('sys-cpu').textContent = (typeof s.cpuTempC === 'number' && s.cpuTempC > 0)
      ? `${s.cpuTempC.toFixed(1)} °C`
      : '—';
    $('sys-uptime').textContent = formatUptime(s.uptimeSeconds);
    $('sys-version').textContent = s.version || '—';
    $('sys-battery').textContent = formatBattery(s.battery);
    $('system-shutdown').classList.toggle('hidden', !s.shutdownAllowed);
  } catch (e) {
    // Don't toast on every failed poll — pollHealth will already shout.
  }
}

// setTailscaleRow shows the Tailscale row only when the daemon reports
// an active tailscale interface; for non-Tailscale hosts the row stays
// hidden so the panel doesn't carry a permanent "—".
function setTailscaleRow(t) {
  const show = !!(t && t.ip);
  for (const el of document.querySelectorAll('.row-tailscale')) {
    el.classList.toggle('hidden', !show);
  }
  if (show) {
    const iface = t.iface ? ` (${t.iface})` : '';
    $('sys-tailscale').textContent = `${t.ip}${iface}`;
  }
}

function formatNet(n) {
  if (!n || !n.up) return 'no network';
  const kind = n.wireless ? 'Wi-Fi' : 'Wired';
  // For Wi-Fi, lead with the network name; the interface is always
  // wlan0 and adds little. Wired keeps showing the interface.
  if (n.wireless && n.ssid) {
    return `${kind}   ${n.ssid}   ${n.ip || '—'}`;
  }
  const iface = n.iface ? ` (${n.iface})` : '';
  return `${kind}   ${n.ip || '—'}${iface}`;
}

function formatUptime(s) {
  if (!Number.isFinite(s) || s <= 0) return '—';
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  if (h > 0) return `${h}h ${String(m).padStart(2, '0')}m`;
  if (m > 0) return `${m}m`;
  return `${Math.floor(s)}s`;
}

function formatBattery(b) {
  if (!b) return 'no battery board detected';
  const pct = Math.round(b.percent ?? 0);
  const volts = (typeof b.volts === 'number') ? `   ${b.volts.toFixed(2)} V` : '';
  const src = b.externalPower ? '   external power' : '   on battery';
  return `${pct}%${volts}${src}`;
}

// refreshMirror flips the display-preview img src to a fresh URL,
// cache-busted so the browser actually re-fetches the rendered page.
function refreshMirror() {
  const img = $('display-mirror-img');
  const url = withAuthURL(`/api/system/display.png?page=${encodeURIComponent(mirrorPage)}&t=${Date.now()}`);
  img.src = url;
}

function setMirrorPage(page) {
  mirrorPage = page;
  for (const btn of $('display-mirror-pages').querySelectorAll('button')) {
    btn.classList.toggle('active', btn.dataset.page === page);
  }
  refreshMirror();
}

async function requestShutdown() {
  if (!confirm('Power off the Pi? The daemon will become unreachable until the device is powered back on.')) return;
  try {
    await api('/api/system/shutdown', { method: 'POST' });
    toast('ok', 'Shutdown requested', 'the Pi will power off shortly');
  } catch (e) {
    toast('bad', 'Could not shut down', e.message || e.code);
  }
}

// renderBattery shows or hides the topbar battery widget based on the
// /api/health response. The widget appears only when the server has a
// PiSugar reading; on desktop/dev hosts the field is absent and the
// widget stays hidden.
function renderBattery(b) {
  const el = $('battery');
  if (!b) { el.classList.add('hidden'); return; }
  el.classList.remove('hidden');
  const pct = Math.max(0, Math.min(100, Math.round(b.percent ?? 0)));
  $('batt-fill').style.width = pct + '%';
  el.classList.toggle('batt-low', pct <= 15);
  el.classList.toggle('batt-warn', pct > 15 && pct <= 35);
  $('batt-pct').textContent = pct + '%';
  $('batt-bolt').hidden = !b.externalPower;
  const volts = (typeof b.volts === 'number') ? b.volts.toFixed(2) + 'V' : '';
  const src = b.externalPower ? 'external power' : 'on battery';
  el.title = ['Battery', pct + '%', volts, src].filter(Boolean).join(' · ');
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
      // The kind badge tells the user what they'll get on click: a
      // controller opens the slots/transfer flow, a camera/drone opens
      // the media gallery. Until classification lands, kind is unknown.
      let kindBadge;
      if (d.kind === 'camera') {
        kindBadge = `<span class="badge ok">Camera</span>`;
      } else if (d.kind === 'controller') {
        kindBadge = d.djiFlyDetected
          ? `<span class="badge ok">DJI Fly</span>`
          : `<span class="badge bad">no DJI Fly</span>`;
      } else {
        kindBadge = `<span class="badge warn">identifying…</span>`;
      }
      li.innerHTML = `
        <div>
          <div class="dev-name">${escapeHTML(d.model || 'MTP device')}</div>
          <div class="dev-id">${escapeHTML(d.id)}</div>
          ${d.hint ? `<div class="dev-meta dim">${escapeHTML(d.hint)}</div>` : ''}
        </div>
        <div class="dev-meta">
          ${transportBadge}
          ${stateBadge}
          ${kindBadge}
        </div>
      `;
      li.addEventListener('click', () => selectDevice(d));
      if (state.selectedDevice?.id === d.id) li.classList.add('selected');
      list.appendChild(li);
    }
    // Re-sync the open panel with fresh device data. This is what makes
    // the "identifying…" → controller/camera transition seamless: when
    // background classification finishes the server emits device.refreshed,
    // the WebSocket handler re-runs loadDevices, and a changed kind
    // re-opens the correct panel without the user clicking again.
    if (state.selectedDevice) {
      const fresh = devices.find(d => d.id === state.selectedDevice.id);
      if (fresh && fresh.kind !== state.selectedDevice.kind) {
        selectDevice(fresh);
      } else if (fresh) {
        state.selectedDevice = fresh;
      }
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
  if (d.kind === 'camera') {
    // Camera/drone: media gallery instead of the slots/transfer flow.
    $('slots-panel').classList.add('hidden');
    $('transfer-panel').classList.add('hidden');
    $('media-panel').classList.remove('hidden');
    $('media-device-name').textContent = `on ${d.model || d.id}`;
    await loadMedia();
    return;
  }
  // Controller (or still-unknown): slots + transfer flow.
  $('media-panel').classList.add('hidden');
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
      li.draggable = true;
      // Cache-bust on lastModified so re-uploads visibly refresh.
      const cacheBust = s.lastModified ? `?v=${encodeURIComponent(s.lastModified)}` : '';
      const thumb = s.previewAvailable
        ? `<img class="slot-thumb" src="${withAuthURL(s.previewUrl + cacheBust)}" alt="" loading="lazy">`
        : `<div class="slot-thumb slot-thumb-empty">no preview</div>`;
      const managed = s.managed !== false; // default true if backend somehow missed it
      const writeDisabled = managed ? '' : ' disabled';
      li.classList.toggle('unmanaged', !managed);
      li.innerHTML = `
        <label class="managed-toggle" title="Managed: include this slot in batch operations and allow write actions">
          <input type="checkbox" data-role="managed" ${managed ? 'checked' : ''}>
        </label>
        ${thumb}
        <div class="slot-info">
          <div class="slot-name-row">
            <span class="slot-name" data-role="name">${escapeHTML(s.name || 'Slot')}</span>
            <button type="button" class="rename-btn" title="Rename slot" data-role="rename"${writeDisabled}>✎</button>
            <button type="button" class="rename-btn" title="Download current KMZ" data-role="download">⤓</button>
            <button type="button" class="rename-btn" title="Regenerate preview from on-device KMZ" data-role="regen"${writeDisabled}>↻</button>
            <button type="button" class="rename-btn" title="Push per-waypoint images (overwrites any drone photos)" data-role="wp"${writeDisabled}>⎙</button>
            <button type="button" class="rename-btn danger" title="Clear slot (replace mission with a placeholder)" data-role="clear"${writeDisabled}>✕</button>
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
      li.querySelector('[data-role="clear"]').addEventListener('click', async (e) => {
        e.stopPropagation();
        await clearSlotConfirm(s);
      });
      li.querySelector('[data-role="download"]').addEventListener('click', (e) => {
        e.stopPropagation();
        downloadKMZ(s);
      });
      li.querySelector('[data-role="managed"]').addEventListener('click', (e) => {
        e.stopPropagation();
      });
      li.querySelector('[data-role="managed"]').addEventListener('change', async (e) => {
        const newVal = e.target.checked;
        try {
          await api(`/api/devices/${encodeURIComponent(state.selectedDevice.id)}/slots/${encodeURIComponent(s.guid)}/managed`, {
            method: 'PUT',
            headers: { 'Content-Type': 'application/json' },
            body: JSON.stringify({ managed: newVal }),
          });
          s.managed = newVal;
          li.classList.toggle('unmanaged', !newVal);
          li.querySelectorAll('.rename-btn:not([data-role="download"])').forEach(b => b.disabled = !newVal);
          toast('ok', newVal ? 'Now managed' : 'Now unmanaged', s.guid.slice(0, 8));
        } catch (err) {
          e.target.checked = !newVal;
          toast('bad', 'Could not save managed flag', err.message || err.code);
        }
      });
      if (state.selectedSlot?.guid === s.guid) li.classList.add('selected');
      list.appendChild(li);
    }
    wireSlotDrag(list);
  } catch (e) {
    list.innerHTML = '';
    toast('bad', 'Could not list slots', e.message || e.code);
  } finally {
    $('slots-panel').classList.remove('loading');
  }
}

// ---------- media (camera / drone) ----------

async function loadMedia() {
  if (!state.selectedDevice) return;
  const grid = $('media-grid');
  $('media-panel').classList.add('loading');
  grid.innerHTML = '<div class="media-placeholder loading">Scanning camera storage…</div>';
  try {
    const { items } = await api(`/api/devices/${encodeURIComponent(state.selectedDevice.id)}/media`);
    grid.innerHTML = '';
    if (!items?.length) {
      grid.innerHTML = '<div class="media-placeholder">no photos or videos found on this device</div>';
      return;
    }
    for (const m of items) grid.appendChild(renderMediaTile(m));
  } catch (e) {
    grid.innerHTML = '';
    toast('bad', 'Could not list media', e.message || e.code);
  } finally {
    $('media-panel').classList.remove('loading');
  }
}

function renderMediaTile(m) {
  const isVideo = m.kind === 'video';
  const tile = document.createElement('div');
  tile.className = 'media-tile';
  tile.dataset.id = m.id;

  const thumb = document.createElement('div');
  thumb.className = 'media-thumb';
  const img = document.createElement('img');
  img.loading = 'lazy';
  img.alt = m.name;
  img.src = withAuthURL(m.thumbnailUrl);
  // A 404 here is expected when the device serves no thumbnail — fall
  // back to an icon glyph rather than a broken-image box.
  img.addEventListener('error', () => {
    img.remove();
    const icon = document.createElement('span');
    icon.className = 'media-thumb-icon';
    icon.textContent = isVideo ? '🎬' : '🖼';
    thumb.prepend(icon);
  });
  thumb.appendChild(img);
  if (isVideo) {
    const play = document.createElement('span');
    play.className = 'media-play';
    play.textContent = '▶';
    thumb.appendChild(play);
  }

  const info = document.createElement('div');
  info.className = 'media-info';
  info.innerHTML = `
    <div class="media-name"></div>
    <div class="media-meta">
      <span class="badge ${isVideo ? 'mtp' : 'ok'}">${isVideo ? 'Video' : 'Photo'}</span>
      <span>${bytesHuman(m.size)}</span>
      <span>${timeHuman(m.modifiedAt)}</span>
    </div>
  `;
  const nameEl = info.querySelector('.media-name');
  nameEl.textContent = m.name;
  nameEl.title = m.name;

  const dl = document.createElement('button');
  dl.type = 'button';
  dl.className = 'rename-btn media-download';
  dl.title = 'Download original';
  dl.textContent = '⤓';
  dl.addEventListener('click', (e) => { e.stopPropagation(); downloadMedia(m); });

  tile.append(thumb, info, dl);
  tile.addEventListener('click', () => openMediaLightbox(m));
  return tile;
}

// downloadMedia triggers a browser save of the full original file. The
// on-device name rides in ?name= so the server's Content-Disposition
// keeps the original filename; the token rides in the query because an
// anchor can't carry custom headers.
function downloadMedia(m) {
  const a = document.createElement('a');
  a.href = withAuthURL(`${m.downloadUrl}?name=${encodeURIComponent(m.name)}`);
  a.download = m.name;
  document.body.appendChild(a);
  a.click();
  setTimeout(() => a.remove(), 0);
}

// openMediaLightbox shows the item full-size: a photo as its original
// image, a video via its low-res .LRF proxy in an HTML5 player (the same
// proxy DJI Fly plays for smooth scrubbing). The full original is always
// one click away via "Download original".
function openMediaLightbox(m) {
  const isVideo = m.kind === 'video';
  const backdrop = document.createElement('div');
  backdrop.className = 'modal-backdrop lightbox-backdrop';

  let stage;
  if (isVideo && m.previewUrl) {
    stage = document.createElement('video');
    stage.src = withAuthURL(m.previewUrl);
    stage.controls = true;
    stage.autoplay = true;
    stage.className = 'lightbox-media';
    // Show the poster frame while the clip loads / if playback stalls.
    if (m.thumbnailUrl) stage.poster = withAuthURL(m.thumbnailUrl);
    // The browser may not be able to decode the clip — most often an
    // HEVC/H.265 original, which many browsers can't play. Fall back to
    // a download prompt rather than a silent black player.
    stage.addEventListener('error', () => {
      const fb = document.createElement('div');
      fb.className = 'lightbox-fallback';
      fb.textContent = 'This video can’t be played in the browser — likely an HEVC/H.265 codec. Download the original to view it.';
      stage.replaceWith(fb);
    });
  } else if (isVideo) {
    stage = document.createElement('div');
    stage.className = 'lightbox-fallback';
    stage.textContent = 'No preview proxy on device — download the original to view.';
  } else {
    stage = document.createElement('img');
    stage.src = withAuthURL(m.downloadUrl);
    stage.alt = m.name;
    stage.className = 'lightbox-media';
    stage.addEventListener('error', () => {
      const fb = document.createElement('div');
      fb.className = 'lightbox-fallback';
      fb.textContent = 'This format can’t be shown in the browser — download the original to view.';
      stage.replaceWith(fb);
    });
  }

  const box = document.createElement('div');
  box.className = 'lightbox';
  box.innerHTML = `
    <div class="lightbox-head">
      <span class="lightbox-name"></span>
      <div class="lightbox-actions">
        <button type="button" class="ghost lb-download">⤓ Download original</button>
        <button type="button" class="ghost lb-close">Close</button>
      </div>
    </div>
  `;
  box.querySelector('.lightbox-name').textContent = m.name;
  box.appendChild(stage);
  backdrop.appendChild(box);
  document.body.appendChild(backdrop);

  const close = () => {
    if (stage.tagName === 'VIDEO') { try { stage.pause(); } catch {} }
    backdrop.remove();
    document.removeEventListener('keydown', onKey);
  };
  function onKey(e) { if (e.key === 'Escape') close(); }
  box.querySelector('.lb-close').addEventListener('click', close);
  box.querySelector('.lb-download').addEventListener('click', () => downloadMedia(m));
  backdrop.addEventListener('click', (e) => { if (e.target === backdrop) close(); });
  document.addEventListener('keydown', onKey);
}

// ---------- working modal ----------

// withWorkingModal shows a non-dismissable modal with a spinner + label
// while fn runs. On success replaces it with a success modal; on
// failure replaces with an error modal. Returns whatever fn resolves to
// (or rethrows fn's error so callers can chain).
async function withWorkingModal({ title, subtitle, successTitle, successDetail }, fn) {
  const modal = openWorkingModal(title, subtitle);
  try {
    const result = await fn();
    modal.close();
    if (successTitle) {
      showModal('ok', successTitle, successDetail || {});
    }
    return result;
  } catch (err) {
    modal.close();
    showModal('bad', err.code || 'Failed', {
      Reason: err.message || JSON.stringify(err),
    });
    throw err;
  }
}

function openWorkingModal(title, subtitle) {
  const backdrop = document.createElement('div');
  backdrop.className = 'modal-backdrop working-backdrop';
  backdrop.innerHTML = `
    <div class="modal working" role="dialog" aria-modal="true" aria-busy="true">
      <div class="working-spinner"></div>
      <h3 class="modal-title"></h3>
      <p class="modal-subtitle"></p>
      <div class="modal-progress" style="display:none"></div>
    </div>
  `;
  backdrop.querySelector('.modal-title').textContent = title;
  if (subtitle) backdrop.querySelector('.modal-subtitle').textContent = subtitle;
  const progressEl = backdrop.querySelector('.modal-progress');
  document.body.appendChild(backdrop);
  return {
    close: () => backdrop.remove(),
    setProgress: (msg) => {
      if (!msg) {
        progressEl.style.display = 'none';
      } else {
        progressEl.style.display = 'inline-block';
        progressEl.textContent = msg;
      }
    },
    setTitle: (t) => { backdrop.querySelector('.modal-title').textContent = t; },
  };
}

// ---------- drag-and-drop reorder ----------

function wireSlotDrag(list) {
  let dragged = null;

  list.querySelectorAll('li[data-guid]').forEach(li => {
    li.addEventListener('dragstart', (e) => {
      dragged = li;
      li.classList.add('dragging');
      e.dataTransfer.effectAllowed = 'move';
      // Required for Firefox to actually fire drop events
      try { e.dataTransfer.setData('text/plain', li.dataset.guid); } catch {}
    });
    li.addEventListener('dragend', () => {
      li.classList.remove('dragging');
      list.querySelectorAll('.drop-target').forEach(el => el.classList.remove('drop-target'));
      dragged = null;
    });
    li.addEventListener('dragover', (e) => {
      if (!dragged || dragged === li) return;
      e.preventDefault();
      e.dataTransfer.dropEffect = 'move';
      // Visual hint: insert above or below depending on mouse position
      const rect = li.getBoundingClientRect();
      const before = e.clientY < rect.top + rect.height / 2;
      list.querySelectorAll('.drop-target').forEach(el => el.classList.remove('drop-target'));
      li.classList.add(before ? 'drop-target-before' : 'drop-target-after');
      li.classList.add('drop-target');
    });
    li.addEventListener('dragleave', () => {
      li.classList.remove('drop-target', 'drop-target-before', 'drop-target-after');
    });
    li.addEventListener('drop', async (e) => {
      e.preventDefault();
      if (!dragged || dragged === li) return;
      const rect = li.getBoundingClientRect();
      const before = e.clientY < rect.top + rect.height / 2;
      if (before) list.insertBefore(dragged, li);
      else list.insertBefore(dragged, li.nextSibling);
      li.classList.remove('drop-target', 'drop-target-before', 'drop-target-after');
      await persistSlotOrder();
    });
  });
}

async function persistSlotOrder() {
  if (!state.selectedDevice) return;
  const order = Array.from(document.querySelectorAll('#slot-list li[data-guid]')).map(li => li.dataset.guid);
  try {
    await api(`/api/devices/${encodeURIComponent(state.selectedDevice.id)}/slot-order`, {
      method: 'PUT',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ order }),
    });
    toast('ok', 'Order saved', `${order.length} slots`);
  } catch (err) {
    toast('bad', 'Could not save order', err.message || err.code);
  }
}

// ---------- batch helpers ----------

// runBatch loops over an array of slots calling fn(slot) for each and
// updating the working modal's progress line. fn returns a result and
// throws on failure; failures are accumulated and reported at the end
// without aborting the rest of the batch.
async function runBatch({ title, action, slots }, fn) {
  if (!slots.length) {
    toast('warn', 'Nothing to do', 'No slots on this device');
    return;
  }
  const modal = openWorkingModal(title, `Processing ${slots.length} slot${slots.length === 1 ? '' : 's'}…`);
  const ok = [], failed = [];
  for (let i = 0; i < slots.length; i++) {
    const s = slots[i];
    modal.setProgress(`${i + 1} / ${slots.length} — ${s.guid.slice(0, 8)} (${s.name || ''})`);
    try {
      await fn(s);
      ok.push(s);
    } catch (err) {
      failed.push({ slot: s, err });
    }
  }
  modal.close();
  if (failed.length === 0) {
    showModal('ok', `${action} all done`, {
      Succeeded: ok.length,
      Failed: 0,
    });
  } else {
    const detail = { Succeeded: ok.length, Failed: failed.length };
    failed.slice(0, 3).forEach((f, i) => {
      detail[`Error ${i + 1}`] = `${f.slot.guid.slice(0, 8)}: ${f.err.message || f.err.code || ''}`;
    });
    showModal('bad', `${action} finished with errors`, detail);
  }
  await loadSlots();
}

async function batchRegenerateAllPreviews() {
  const slots = Array.from(document.querySelectorAll('#slot-list li[data-guid]:not(.unmanaged)')).map(li => {
    return { guid: li.dataset.guid, name: li.querySelector('.slot-name')?.textContent };
  });
  await runBatch({
    title: 'Regenerating all previews',
    action: 'Preview regen',
    slots,
  }, (s) =>
    api(`/api/devices/${encodeURIComponent(state.selectedDevice.id)}/slots/${encodeURIComponent(s.guid)}/preview/regenerate`, {
      method: 'POST',
    })
  );
}

async function batchPushAllWaypointImages() {
  const slots = Array.from(document.querySelectorAll('#slot-list li[data-guid]:not(.unmanaged)')).map(li => {
    return { guid: li.dataset.guid, name: li.querySelector('.slot-name')?.textContent };
  });
  await runBatch({
    title: 'Pushing waypoint images for all slots',
    action: 'Waypoint image push',
    slots,
  }, (s) =>
    api(`/api/devices/${encodeURIComponent(state.selectedDevice.id)}/slots/${encodeURIComponent(s.guid)}/waypoint-images`, {
      method: 'POST',
    })
  );
}

// ---------- download KMZ ----------

function downloadKMZ(slot) {
  const name = (slot.name || '').trim();
  const params = new URLSearchParams();
  if (name) params.set('name', name);
  const qs = params.toString() ? `?${params.toString()}` : '';
  const url = `/api/devices/${encodeURIComponent(state.selectedDevice.id)}/slots/${encodeURIComponent(slot.guid)}/kmz${qs}`;
  // Triggering the download via a hidden anchor lets the browser's
  // Save dialog fire without leaving the page. Tokens travel in the
  // query string because anchors can't carry custom headers.
  const a = document.createElement('a');
  a.href = withAuthURL(url);
  a.download = ''; // server's Content-Disposition wins
  document.body.appendChild(a);
  a.click();
  setTimeout(() => a.remove(), 0);
}

// ---------- clear slot ----------

async function clearSlotConfirm(slot) {
  const confirmed = await openConfirmModal({
    title: 'Clear this slot?',
    body: `The mission, preview, and per-waypoint images for slot ${slot.guid.slice(0, 8)} will be replaced with a placeholder. The slot itself remains in DJI Fly's list — only its contents are reset. Saved name is also cleared.`,
    danger: 'Clear',
    cancel: 'Keep mission',
  });
  if (!confirmed) return;
  await withWorkingModal({
    title: 'Clearing slot',
    subtitle: `Replacing KMZ, wiping images, deleting preview for ${slot.guid.slice(0, 8)}…`,
  }, async () =>
    api(`/api/devices/${encodeURIComponent(state.selectedDevice.id)}/slots/${encodeURIComponent(slot.guid)}`, {
      method: 'DELETE',
    })
  ).catch(() => null);
  await loadSlots();
}

function openConfirmModal({ title, body, danger, cancel }) {
  return new Promise((resolve) => {
    const backdrop = document.createElement('div');
    backdrop.className = 'modal-backdrop';
    backdrop.innerHTML = `
      <div class="modal bad" role="dialog" aria-modal="true">
        <div class="modal-icon">!</div>
        <h3 class="modal-title"></h3>
        <p class="modal-subtitle"></p>
        <div class="modal-actions">
          <button type="button" class="ghost cancel">Cancel</button>
          <button type="button" class="primary confirm">Confirm</button>
        </div>
      </div>
    `;
    backdrop.querySelector('.modal-title').textContent = title;
    backdrop.querySelector('.modal-subtitle').textContent = body;
    backdrop.querySelector('.cancel').textContent = cancel || 'Cancel';
    backdrop.querySelector('.confirm').textContent = danger || 'Confirm';
    document.body.appendChild(backdrop);
    const close = (val) => { backdrop.remove(); resolve(val); };
    backdrop.querySelector('.cancel').addEventListener('click', () => close(false));
    backdrop.querySelector('.confirm').addEventListener('click', () => close(true));
    backdrop.addEventListener('click', (e) => { if (e.target === backdrop) close(false); });
    document.addEventListener('keydown', function onKey(e) {
      if (e.key === 'Escape') { close(false); document.removeEventListener('keydown', onKey); }
    });
    backdrop.querySelector('.confirm').focus();
  });
}

// ---------- push waypoint images ----------

async function pushWaypointImages(slot) {
  const result = await withWorkingModal({
    title: 'Pushing per-waypoint images',
    subtitle: 'Rendering and uploading one satellite tile per waypoint…',
  }, async () =>
    api(`/api/devices/${encodeURIComponent(state.selectedDevice.id)}/slots/${encodeURIComponent(slot.guid)}/waypoint-images`, {
      method: 'POST',
    })
  ).catch(() => null);
  if (result) {
    showModal('ok', `Pushed ${result.count} waypoint image${result.count === 1 ? '' : 's'}`, {
      Slot: slot.guid,
      Count: result.count,
      At: result.at,
    });
  }
}

// ---------- regenerate preview ----------

async function regeneratePreview(slot) {
  const result = await withWorkingModal({
    title: 'Regenerating preview',
    subtitle: 'Reading KMZ from device, rendering ESRI satellite tile, pushing back…',
  }, async () =>
    api(`/api/devices/${encodeURIComponent(state.selectedDevice.id)}/slots/${encodeURIComponent(slot.guid)}/preview/regenerate`, {
      method: 'POST',
    })
  ).catch(() => null);
  if (result) {
    await loadSlots();
    showModal('ok', 'Preview regenerated', {
      Slot: slot.guid,
      At: result.at,
    });
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
  const switching = state.selectedSlot && state.selectedSlot.guid !== s.guid;
  state.selectedSlot = s;
  for (const li of $('slot-list').children) li.classList.toggle('selected', li.dataset.guid === s.guid);
  $('transfer-panel').classList.remove('hidden');
  $('transfer-target').textContent = `→ ${s.guid}`;
  // Switching slots invalidates the prior staged transfer; clear so
  // the user doesn't accidentally push the previous slot's KMZ here.
  if (switching) resetTransferForm();
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

// autoInspect uploads the picked KMZ to /api/kmz/inspect and refreshes
// the previewMetadata textarea + mission name with the new file's
// metadata. Always overwrites — picking a new file means the user
// wants its metadata, not the previous file's.
async function autoInspect(file) {
  const ta = $('preview-metadata');
  const nameInput = $('mission-name');

  const fd = new FormData();
  fd.append('kmz', file);
  try {
    const res = await fetch('/api/kmz/inspect', withAuth({ method: 'POST', body: fd }));
    const body = await res.json().catch(() => ({}));
    if (!res.ok) {
      toast('warn', 'KMZ inspect failed', body?.error?.message || res.statusText);
      ta.value = '';
      nameInput.value = '';
      return;
    }
    const payload = {
      name: body.name,
      date: body.date,
      waypoints: body.waypoints,
    };
    Object.keys(payload).forEach(k => {
      if (payload[k] == null || (Array.isArray(payload[k]) && payload[k].length === 0)) {
        delete payload[k];
      }
    });
    ta.value = JSON.stringify(payload, null, 2);
    nameInput.value = body.name || '';
    const actionNote = body.actionCount
      ? `${body.actionCount} with action${body.actionCount === 1 ? '' : 's'}`
      : 'all navigation';
    toast('ok', `Parsed ${body.count} waypoint${body.count === 1 ? '' : 's'}`,
      `${actionNote}${body.source ? ` · from ${body.source}` : ''}`);
  } catch (err) {
    toast('warn', 'KMZ inspect threw', err.message);
  }
}

// resetTransferForm clears the file, name, and previewMetadata fields
// so the next pick starts clean. Called after a successful transfer
// and when switching slots — either action invalidates the prior
// form state.
function resetTransferForm() {
  state.file = null;
  const fileInput = $('kmz-file');
  if (fileInput) fileInput.value = '';
  $('file-meta').textContent = 'no file selected';
  $('mission-name').value = '';
  $('preview-metadata').value = '';
  $('push-wp-images').checked = true;
  updateTransferButton();
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

  const stagedFile = state.file;
  const stagedSlot = state.selectedSlot;
  const stagedDevice = state.selectedDevice;

  // Non-dismissable modal blocks UI while bytes are in flight. MTP is
  // serialized through a mutex so concurrent UI clicks would just
  // queue, but giving the user no clickable surface during the upload
  // makes the active state obvious.
  const modal = openWorkingModal(
    'Transferring KMZ',
    `${stagedFile.name} → ${stagedSlot.guid.slice(0, 8)}`,
  );
  if ($('push-wp-images').checked) {
    modal.setProgress('Also queuing 8× per-waypoint image push…');
  }

  const startedAt = performance.now();
  try {
    const res = await fetch(url, withAuth({ method: 'POST', body: fd }));
    const body = await res.json().catch(() => ({}));
    if (!res.ok) {
      const err = body.error || { message: res.statusText };
      throw err;
    }
    const elapsed = Math.round(performance.now() - startedAt);
    modal.close();
    showModal('ok', 'Transfer complete', {
      File: stagedFile.name,
      Slot: stagedSlot.guid,
      'Device': stagedDevice.id,
      Size: bytesHuman(body.fileSize),
      Elapsed: `${elapsed} ms`,
      At: body.transferredAt || new Date().toISOString(),
    });
    resetTransferForm();
    await loadSlots();
  } catch (err) {
    modal.close();
    showModal('bad', err.code || 'Transfer failed', {
      Reason: err.message || JSON.stringify(err),
      Slot: stagedSlot.guid,
      File: stagedFile?.name || '(none)',
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
  // Browsers can't set custom headers on a WebSocket handshake, so the
  // server accepts the token via query string here (auth middleware in
  // server.go falls back to ?token=…).
  const ws = new WebSocket(withAuthURL(`${proto}//${location.host}/api/events`));
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
  // Token first — every other call depends on it.
  // 1. ?token=… in the URL wins (single-shot bootstrap).
  // 2. Otherwise reuse whatever's in sessionStorage from earlier in
  //    this tab.
  // 3. Otherwise prompt. The modal accepts an empty submission for
  //    servers running with auth disabled (config.auth.token == ""),
  //    in which case the middleware ignores the header anyway.
  captureTokenFromURL();
  if (!getToken()) {
    await promptForToken();
  }

  wireDropzone();
  $('transfer-form').addEventListener('submit', submitTransfer);
  $('refresh-devices').addEventListener('click', loadDevices);
  $('refresh-slots').addEventListener('click', loadSlots);
  $('refresh-media').addEventListener('click', loadMedia);

  $('batch-regen-previews').addEventListener('click', batchRegenerateAllPreviews);
  $('batch-push-wp').addEventListener('click', batchPushAllWaypointImages);

  await pollHealth();
  setInterval(pollHealth, 10000);

  // System panel: telemetry + display mirror + shutdown.
  for (const btn of $('display-mirror-pages').querySelectorAll('button')) {
    btn.addEventListener('click', () => setMirrorPage(btn.dataset.page));
  }
  $('system-refresh').addEventListener('click', () => { pollSystem(); refreshMirror(); });
  $('system-shutdown').addEventListener('click', requestShutdown);
  await pollSystem();
  setInterval(pollSystem, 10000);
  refreshMirror();
  mirrorTimer = setInterval(refreshMirror, MIRROR_REFRESH_MS);

  await loadDevices();
  wireEvents();
});

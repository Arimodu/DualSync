'use strict';
// DualSync control panel. Talks only to the same-origin /api/* contract in api/openapi.yaml.

const $ = id => document.getElementById(id);
function h(tag, props, ...kids) {
  const e = document.createElement(tag);
  for (const [k, v] of Object.entries(props || {})) {
    if (v == null || v === false) continue;
    if (k === 'class') e.className = v;
    else if (k === 'text') e.textContent = v;
    else if (k.startsWith('on')) e.addEventListener(k.slice(2), v);
    else if (k in e && typeof e[k] !== 'function') e[k] = v;
    else e.setAttribute(k, v === true ? '' : v);
  }
  for (const k of kids.flat()) if (k != null && k !== false) e.append(k instanceof Node ? k : String(k));
  return e;
}
const SIDES = ['sub', 'dub'];
const SIDE_LABEL = {sub: 'Sub', dub: 'Dub'};
const PHASE_LABEL = {none: 'No media', loading: 'Loading', ready: 'Ready', playing: 'Playing', paused: 'Paused', resuming: 'Resuming', seeking: 'Seeking', buffering: 'Buffering', ended: 'Ended'};
const basename = p => (p || '').split('/').filter(Boolean).pop() || '';
const dirname = p => (p || '').split('/').slice(0, -1).join('/');
function clock(sec) {
  sec = Math.max(0, Math.floor(sec || 0));
  const hh = Math.floor(sec / 3600), mm = Math.floor(sec / 60) % 60, ss = sec % 60;
  const two = n => String(n).padStart(2, '0');
  return hh ? `${hh}:${two(mm)}:${two(ss)}` : `${two(mm)}:${two(ss)}`;
}

// ---------- HTTP ----------
class ApiError extends Error {
  constructor(message, code, status) { super(message); this.code = code; this.status = status; }
}
async function api(path, body) {
  const opts = body === undefined ? {} : {method: 'POST', headers: {'Content-Type': 'application/json'}, body: JSON.stringify(body)};
  let res;
  try { res = await fetch(path, opts); }
  catch { throw new ApiError('The DualSync server could not be reached.', 'NETWORK', 0); }
  let data = null;
  try { data = await res.json(); } catch { /* non-JSON body */ }
  if (!res.ok) throw new ApiError(data?.error || `Request failed (HTTP ${res.status})`, data?.code || 'HTTP_' + res.status, res.status);
  return data;
}
// A POST whose outcome is unknown must not be retried blindly; refresh status instead.
const uncertain = e => e.code === 'NETWORK' || (e.status >= 500 && e.code === 'HTTP_' + e.status);

// ---------- State ----------
const S = {status: null, rev: -1, at: 0, online: null, pending: null, errors: [], notice: '', musicMoving: false, lowerRevs: 0};

function accept(s) {
  if (!s || typeof s.revision !== 'number' || s.revision < S.rev) return false;
  const prev = S.status;
  S.musicMoving = !!(prev && s.music.active && prev.music.path === s.music.path && s.music.position !== prev.music.position);
  S.rev = s.revision; S.status = s; S.at = performance.now();
  render();
  return true;
}
function showError(text, key = 'request') {
  S.errors = S.errors.filter(e => e.key !== key);
  if (text) S.errors.push({key, text});
  renderBanners();
}
function showNotice(text) { S.notice = text; renderBanners(); }

let polling = false, pollAgain = false, pollTimer = 0, pollFails = 0;
async function poll() {
  if (polling) { pollAgain = true; return; }
  polling = true; clearTimeout(pollTimer);
  try {
    const s = await api('/api/status');
    S.online = true; pollFails = 0;
    // A restarted backend starts its revision counter again; accept after repeated lower revisions.
    if (s.revision < S.rev) { if (++S.lowerRevs >= 3) { S.rev = -1; S.lowerRevs = 0; } }
    else S.lowerRevs = 0;
    if (!accept(s)) render();
  } catch {
    S.online = false; pollFails++;
    render();
  } finally {
    polling = false;
    if (pollAgain) { pollAgain = false; poll(); }
    else pollTimer = setTimeout(poll, S.online ? 500 : Math.min(5000, 1000 * pollFails));
  }
}

async function post(label, path, body) {
  if (S.pending) return null;
  S.pending = label; render();
  try {
    const data = await api(path, body);
    showError('');
    return data;
  } catch (e) {
    if (uncertain(e)) {
      showError(`${e.message} The result of “${label}” is unknown; status was refreshed instead of retrying.`);
      poll();
    } else showError(e.message);
    return null;
  } finally { S.pending = null; render(); }
}
async function command(action, value, label = action) {
  const s = await post(label, '/api/control', value === undefined ? {action} : {action, value});
  if (s) accept(s);
  return s;
}

// ---------- Probe cache ----------
const probes = new Map();
function probe(path) {
  if (!probes.has(path)) {
    const p = api('/api/library/probe?path=' + encodeURIComponent(path));
    p.catch(() => probes.delete(path));
    probes.set(path, p);
  }
  return probes.get(path);
}
const probed = new Map(); // path -> resolved probe, for synchronous rendering
function probeInto(path) {
  if (!path || probed.has(path)) return;
  probed.set(path, null);
  probe(path).then(p => { probed.set(path, p); render(); }, () => { probed.set(path, false); render(); });
}

function trackLabel(t) {
  if (!t) return '';
  const parts = [`#${t.index}`, t.language || 'und'];
  if (t.title) parts.push(t.title);
  parts.push(t.codec + (t.channels ? ` ${t.channels}ch` : ''));
  return parts.join(' · ');
}
function describeChoice(p, kind, value) {
  if (value === -1) return kind === 'audio' ? 'Silence' : 'Off';
  if (value == null) return 'Automatic';
  const t = p?.tracks.find(x => x.index === value);
  return t ? trackLabel(t) : `#${value}`;
}

// ---------- Rendering: header, banners ----------
function renderBanners() {
  const box = $('banners'), s = S.status, list = [];
  if (S.online === false) list.push(['bad', 'Connection to DualSync lost. Retrying…']);
  if (s && !s.diagnostics.supported) list.push(['warn', 'This host is not qualified for encoding (unsupported configuration). ' + (s.diagnostics.warning || 'See Encoder diagnostics.')]);
  else if (s?.diagnostics.warning) list.push(['warn', s.diagnostics.warning]);
  if (s?.error) list.push(['bad', s.error]);
  for (const e of S.errors) list.push(['bad', e.text, e.key]);
  if (S.notice) list.push(['info', S.notice, 'notice']);
  const sig = JSON.stringify(list);
  if (box.dataset.sig === sig) return;
  box.dataset.sig = sig;
  box.replaceChildren(...list.map(([cls, text, key]) => h('div', {class: 'banner ' + cls, role: cls === 'bad' ? 'alert' : null},
    h('span', {text}),
    key ? h('button', {class: 'subtle', text: 'Dismiss', onclick: () => key === 'notice' ? showNotice('') : showError('', key)}) : null)));
}

function render() {
  renderBanners();
  const s = S.status;
  $('conn').className = 'conn ' + (S.online ? 'on' : S.online === false ? 'off' : '');
  if (!s) return;
  const running = s.mode !== 'stopped', m = s.media, phase = m.phase;
  const pill = $('phasePill');
  pill.textContent = S.online === false ? 'Offline' : !running ? (phase === 'none' ? 'Stopped' : `Stopped · ${PHASE_LABEL[phase]}`) : PHASE_LABEL[phase];
  pill.className = 'pill ' + (S.online === false ? 'bad' : phase === 'playing' ? 'playing' : ['buffering', 'loading', 'seeking', 'resuming'].includes(phase) ? 'warn' : '');
  renderStreams(s, running);
  renderPlayer(s, running);
  renderMusic(s, running);
  renderSettings(s, running);
  renderDiagnostics(s, running);
  renderPlaylist();
  tick();
}

function renderStreams(s, running) {
  const st = s.settings, configured = !!(st.sub_server && st.dub_server && st.stream_path);
  let live = 0;
  for (const side of SIDES) {
    const o = s.outputs[side], box = $('out-' + side);
    const server = st[side + '_server'];
    box.querySelector('.dest').textContent = server ? `${server}/${st.stream_path}` : 'Server not configured';
    let state = 'Stopped', cls = 'output';
    if (o) {
      if (o.ready) { state = 'Live'; cls += ' live'; live++; }
      else { state = o.restarts ? 'Reconnecting' : 'Connecting'; cls += ' trouble'; }
    }
    box.className = cls;
    box.querySelector('.state').textContent = state;
    const facts = o ? [['Encoder', o.encoder.toUpperCase()], ['Output clock', clock(o.out_time)], ['Queue', `${o.queued_frames}/8 frames`], ['Restarts', o.restarts], ['PID', o.pid || '—']] : [];
    box.querySelector('.facts').replaceChildren(...facts.flatMap(([k, v]) => [h('dt', {text: k}), h('dd', {text: v})]));
    box.querySelector('.err').textContent = o?.error || '';
  }
  const chip = $('streamsState');
  chip.textContent = !running ? 'Stopped' : live === 2 ? 'Both live' : `${live}/2 live`;
  chip.className = 'chip ' + (!running ? '' : live === 2 ? 'ok' : 'warn');
  $('start').disabled = !!S.pending || running || !configured;
  $('stop').disabled = !!S.pending || !running;
  $('start').textContent = S.pending === 'Start streams' ? 'Starting…' : 'Start streams';
  $('stop').textContent = S.pending === 'Stop streams' ? 'Stopping…' : 'Stop streams';
  $('streamsHint').textContent = !configured ? 'Set both servers and the stream path in Stream settings first.'
    : running ? 'Pause, seek and media changes keep both publishers connected. Only Stop disconnects.'
    : 'Streams can start with or without selected media.';
}

let itemSig = '';
function renderPlayer(s, running) {
  const m = s.media, phase = m.phase, item = m.items[m.index];
  const chip = $('phaseChip');
  chip.textContent = PHASE_LABEL[phase];
  chip.className = 'chip ' + (phase === 'playing' ? 'ok' : ['buffering', 'loading', 'seeking', 'resuming'].includes(phase) ? 'warn' : '');
  $('mediaTitle').textContent = phase === 'none' ? 'Nothing selected' : (item?.title || m.title || basename(item?.sub.path) || 'Untitled');

  const op = m.operation, opBox = $('operation');
  opBox.hidden = !(op.state === 'running' || op.state === 'failed');
  opBox.className = 'operation' + (op.state === 'failed' ? ' failed' : '');
  if (op.state === 'running') opBox.replaceChildren(h('span', {class: 'spinner'}), 'Preparing the selection…');
  else if (op.state === 'failed') opBox.textContent = 'Loading failed: ' + (op.error || s.error || 'unknown error');

  const sig = JSON.stringify(m.items.map(i => i.title || i.sub.path));
  if (sig !== itemSig) {
    itemSig = sig;
    $('item').replaceChildren(...m.items.map((it, i) => h('option', {value: i, text: `${i + 1}. ${it.title || basename(it.sub.path)}`})));
  }
  $('itemField').hidden = m.total < 2;
  if (document.activeElement !== $('item')) $('item').value = m.index;
  const settled = phase !== 'none' && phase !== 'loading';
  $('item').disabled = !!S.pending || !settled;

  const play = $('play');
  play.textContent = phase === 'paused' ? 'Resume' : phase === 'ended' ? 'Replay' : 'Play';
  play.disabled = !!S.pending || !running || !['ready', 'paused', 'ended'].includes(phase);
  $('pause').disabled = !!S.pending || !['playing', 'resuming', 'buffering'].includes(phase);
  for (const b of document.querySelectorAll('[data-seek]')) b.disabled = !!S.pending || !settled;
  $('unload').disabled = !!S.pending || phase === 'none';

  $('playerNote').textContent =
    phase === 'none' ? 'Choose media from the library, then load it.' :
    phase === 'loading' ? 'Loading the selection. Playback will wait for Play.' :
    phase === 'buffering' ? 'Waiting for both outputs. The position is held until they catch up.' :
    phase === 'ended' ? 'Playlist finished. Replay restarts the final item.' :
    !running ? 'Start streams to play.' :
    phase === 'ready' ? 'Ready. Press Play to start both outputs together.' :
    phase === 'paused' ? 'Paused. Idle music fades in while paused.' :
    phase === 'playing' ? 'Both outputs are playing from one source clock.' : '';

  const tracks = $('currentTracks');
  if (!item) { tracks.replaceChildren(); return; }
  const rows = SIDES.map(side => {
    const src = item[side];
    probeInto(src.path);
    const p = probed.get(src.path);
    const file = m.mode === 'paired' ? ` · ${basename(src.path)}` : '';
    return h('div', null, h('b', {text: SIDE_LABEL[side]}),
      `Audio: ${describeChoice(p, 'audio', src.audio)} · Subtitles: ${describeChoice(p, 'subtitle', src.subtitle)}${file}`);
  });
  tracks.replaceChildren(...rows);
}

function renderMusic(s, running) {
  const mu = s.music, st = s.settings;
  const chip = $('musicChip');
  chip.textContent = mu.active ? `Playing · ${Math.round(mu.gain * 100)}% gain` : st.music_folder ? 'Silent' : 'Off';
  chip.className = 'chip ' + (mu.active && mu.gain > 0 ? 'ok' : '');
  const name = mu.title || basename(mu.path);
  $('musicName').textContent = mu.active ? name || 'Unknown track' : st.music_folder ? 'Idle music plays while paused or without media.' : 'No music folder configured.';
  $('musicArtist').textContent = mu.active ? [mu.artist || mu.album_artist, mu.album].filter(Boolean).join(' — ') : '';
  const img = $('cover');
  if (mu.active && mu.cover_url) {
    if (img.getAttribute('src') !== mu.cover_url) { img.hidden = false; img.src = mu.cover_url; }
  } else { img.hidden = true; img.removeAttribute('src'); }
  $('coverFallback').hidden = !img.hidden;
  $('musicNext').disabled = !!S.pending || !running || !st.music_folder;
}
$('cover').addEventListener('error', () => { $('cover').hidden = true; $('coverFallback').hidden = false; });

// Interpolate positions between polls for display only.
function tick() {
  const s = S.status;
  if (!s) return;
  const dt = (performance.now() - S.at) / 1000, m = s.media;
  const pos = m.phase === 'playing' && S.online ? Math.min(m.duration || Infinity, m.position + dt) : m.position;
  $('clockPos').textContent = clock(pos);
  $('clockDur').textContent = '/ ' + clock(m.duration);
  $('mediaBar').style.width = m.duration ? `${Math.min(100, pos / m.duration * 100)}%` : '0';
  const mu = s.music;
  const mpos = S.musicMoving && S.online ? Math.min(mu.duration || Infinity, mu.position + dt) : mu.position;
  $('musicBar').style.width = mu.active && mu.duration ? `${Math.min(100, mpos / mu.duration * 100)}%` : '0';
  $('musicTime').textContent = mu.active && mu.duration ? `${clock(mpos)} / ${clock(mu.duration)}` : '';
}
setInterval(tick, 250);

// ---------- Controls ----------
$('start').onclick = () => command('start', undefined, 'Start streams');
$('stop').onclick = () => command('stop', undefined, 'Stop streams');
$('play').onclick = () => command('play', undefined, 'Play');
$('pause').onclick = () => command('pause', undefined, 'Pause');
$('unload').onclick = () => command('unload', undefined, 'Unload');
$('musicNext').onclick = () => command('music_next', undefined, 'Next song');
for (const b of document.querySelectorAll('[data-seek]')) b.onclick = () => command('seek', Number(b.dataset.seek), 'Seek');
$('item').onchange = e => command('episode', Number(e.target.value) + 1, 'Change item');

// ---------- Library browser ----------
const L = {path: '', q: '', entries: [], next: 0, indexing: false, truncated: false, loading: false, token: 0, error: '', retry: 0};

async function browse(reset) {
  const token = ++L.token;
  clearTimeout(L.retry);
  if (reset) { L.entries = []; L.next = 0; }
  if (L.next < 0) return;
  L.loading = true; L.error = ''; renderLibrary();
  const qs = new URLSearchParams({path: L.path, cursor: L.next, limit: 100});
  if (L.q) qs.set('q', L.q);
  try {
    const r = await api('/api/library?' + qs);
    if (token !== L.token) return;
    L.entries.push(...r.entries);
    L.next = r.next_cursor; L.indexing = r.indexing; L.truncated = r.truncated;
    // Partial search results while indexing: restart from cursor zero until indexing finishes.
    if (r.indexing && L.q) L.retry = setTimeout(() => browse(true), 2000);
  } catch (e) {
    if (token !== L.token) return;
    L.error = e.message;
  } finally {
    if (token === L.token) { L.loading = false; renderLibrary(); }
  }
}
function go(path) { L.path = path; L.q = ''; $('search').value = ''; browse(true); }

let searchTimer = 0;
$('search').addEventListener('input', e => {
  clearTimeout(searchTimer);
  searchTimer = setTimeout(() => { L.q = e.target.value.trim(); browse(true); }, 300);
});
$('more').onclick = () => browse(false);

function renderLibrary() {
  const parts = L.path ? L.path.split('/') : [];
  const crumbs = [h('button', {class: parts.length ? 'link' : 'here link', text: 'Library', onclick: () => go('')})];
  parts.forEach((p, i) => {
    crumbs.push(h('span', {class: 'sep', text: '/'}));
    const target = parts.slice(0, i + 1).join('/');
    crumbs.push(i === parts.length - 1 ? h('span', {class: 'here', text: p}) : h('button', {class: 'link', text: p, onclick: () => go(target)}));
  });
  $('crumbs').replaceChildren(...crumbs);
  $('search').placeholder = L.path ? `Search in ${basename(L.path)}` : 'Search the library';

  const notice = $('libNotice');
  const msgs = [];
  if (L.error) msgs.push(L.error);
  if (L.q && L.indexing) msgs.push('Indexing the library — results are partial and will refresh.');
  if (L.truncated) msgs.push('The search index reached its 200,000-entry limit; some files are not searchable.');
  notice.hidden = !msgs.length;
  notice.textContent = msgs.join(' ');

  const list = $('entries');
  const rows = L.entries.map(entryRow);
  if (!rows.length) rows.push(h('li', {class: 'empty'}, L.loading ? h('span', null, h('span', {class: 'spinner'}), 'Loading…') : L.q ? 'No matches.' : 'This folder has no folders or videos.'));
  list.replaceChildren(...rows);
  $('more').hidden = L.next < 0 || L.loading || !L.entries.length;
}

function entryRow(e) {
  const nameKids = [e.name];
  if (L.q) { const d = dirname(e.path); if (d !== L.path) nameKids.push(h('small', {text: d || '/'})); }
  const name = e.directory
    ? h('button', {class: 'name dir', title: 'Open folder', onclick: () => go(e.path)}, ...nameKids)
    : h('span', {class: 'name file'}, ...nameKids);
  const picks = B.mode === 'single'
    ? [pickButton(e, 'sub', 'Select')]
    : [pickButton(e, 'sub', 'Sub'), pickButton(e, 'dub', 'Dub')];
  const sel = SIDES.some(side => B.src[side]?.path === e.path);
  return h('li', {class: sel ? 'sel' : ''}, h('span', {class: 'ico', text: e.directory ? '📁' : '🎞'}), name, h('span', {class: 'pick'}, ...picks));
}
function pickButton(e, side, text) {
  const on = B.src[side]?.path === e.path;
  return h('button', {class: 'subtle' + (on ? ' on' : ''), text: on ? text + ' ✓' : text,
    title: e.directory ? `Use this folder${B.mode === 'paired' ? ' for ' + SIDE_LABEL[side] : ''}` : `Use this file${B.mode === 'paired' ? ' for ' + SIDE_LABEL[side] : ''}`,
    onclick: () => choose(side, on ? null : {path: e.path, directory: e.directory, name: e.name})});
}

// ---------- Selection / playlist builder ----------
const B = {
  mode: 'single', src: {sub: null, dub: null},
  items: [], unmatched: [], ambiguous: [], start: 0,
  previewing: false, previewError: '', previewToken: 0,
  ref: 0, refProbe: {sub: null, dub: null}, refError: '',
  choice: {sub: {audio: null, subtitle: null}, dub: {audio: null, subtitle: null}},
  mapping: null, confirm: false, loading: false, editing: -1,
};

for (const b of document.querySelectorAll('.segmented [data-mode]')) b.onclick = () => setMode(b.dataset.mode);
function setMode(mode) {
  if (B.mode === mode) return;
  B.mode = mode;
  for (const b of document.querySelectorAll('.segmented [data-mode]')) b.setAttribute('aria-checked', String(b.dataset.mode === mode));
  if (mode === 'single') B.src.dub = null;
  renderSlots(); renderLibrary();
  preview();
}
function choose(side, src) {
  B.src[side] = src;
  renderSlots(); renderLibrary();
  preview();
}
function renderSlots() {
  const slots = B.mode === 'single' ? [['sub', 'Source']] : [['sub', 'Sub'], ['dub', 'Dub']];
  $('slots').replaceChildren(...slots.map(([side, tag]) => {
    const src = B.src[side];
    return h('div', {class: 'slot' + (src ? ' set' : '')}, h('span', {class: 'tag', text: tag}),
      h('span', {class: 'val'}, src ? [src.name, h('div', {class: 'muted', text: (src.directory ? 'Folder · ' : 'File · ') + (dirname(src.path) || 'library root')})]
        : h('span', {class: 'muted', text: B.mode === 'single' ? 'Pick a folder (playlist) or a single video below.' : `Pick the ${tag.toLowerCase()} folder or file below.`})),
      src ? h('button', {class: 'subtle compact', text: 'Clear', onclick: () => choose(side, null)}) : null);
  }));
}

function resetPlaylist() {
  B.previewToken++;
  Object.assign(B, {items: [], unmatched: [], ambiguous: [], start: 0, previewing: false, previewError: '', ref: 0,
    refProbe: {sub: null, dub: null}, refError: '', mapping: null, confirm: false, editing: -1,
    choice: {sub: {audio: null, subtitle: null}, dub: {audio: null, subtitle: null}}});
  renderPlaylist();
}

async function preview() {
  resetPlaylist();
  const {sub, dub} = B.src;
  if (!sub || (B.mode === 'paired' && !dub)) return;
  const token = B.previewToken;
  B.previewing = true; renderPlaylist();
  try {
    let r;
    if (B.mode === 'single') {
      r = sub.directory ? await api('/api/library/playlist', {sub_folder: sub.path})
        : {items: [{title: sub.name, sub: {path: sub.path}, dub: {path: sub.path}}], unmatched: [], ambiguous: []};
    } else if (sub.directory && dub.directory) {
      r = await api('/api/library/playlist', {sub_folder: sub.path, dub_folder: dub.path});
    } else if (!sub.directory && !dub.directory) {
      r = {items: [{title: sub.name, sub: {path: sub.path}, dub: {path: dub.path}}], unmatched: [], ambiguous: []};
    } else throw new ApiError('Choose two folders or two files for separate sub and dub sources.', 'INVALID_ARGUMENT', 400);
    if (token !== B.previewToken) return;
    B.items = r.items.map(it => ({title: it.title || basename(it.sub.path), sub: it.sub.path, dub: it.dub.path, manual: null}));
    B.unmatched = r.unmatched || []; B.ambiguous = r.ambiguous || [];
    if (!B.items.length) B.previewError = 'No playable videos were found' + (B.mode === 'paired' ? ' that pair by season/episode.' : ' in this folder.');
  } catch (e) {
    if (token !== B.previewToken) return;
    B.previewError = e.message;
  } finally {
    if (token === B.previewToken) { B.previewing = false; renderPlaylist(); }
  }
  if (B.items.length) loadReference(0);
}

async function loadReference(i) {
  B.ref = i; B.refError = ''; B.refProbe = {sub: null, dub: null};
  B.choice = {sub: {audio: null, subtitle: null}, dub: {audio: null, subtitle: null}};
  invalidateMapping();
  const it = B.items[i], token = B.previewToken;
  try {
    const [ps, pd] = await Promise.all([probe(it.sub), probe(it.dub)]);
    if (token !== B.previewToken || B.ref !== i) return;
    B.refProbe = {sub: ps, dub: pd};
  } catch (e) {
    if (token !== B.previewToken || B.ref !== i) return;
    B.refError = `Could not read tracks from ${basename(it.sub)}: ${e.message}`;
  }
  renderPlaylist();
}
function invalidateMapping() {
  B.mapping = null; B.confirm = false;
  for (const it of B.items) delete it.resolved;
}

function trackOptions(p, side, kind, value) {
  const tracks = (p?.tracks || []).filter(t => t.kind === kind && !t.attached);
  const rec = tracks.find(t => t.recommended === side);
  const auto = kind === 'audio' ? (rec ? trackLabel(rec) : tracks[0] ? `first audio, ${trackLabel(tracks[0])}` : 'none found')
    : (rec ? trackLabel(rec) : 'Off');
  const opts = [h('option', {value: 'auto', text: `Automatic (${auto})`})];
  for (const t of tracks) opts.push(h('option', {value: t.index, text: trackLabel(t) + (t.recommended ? ` — suggested for ${SIDE_LABEL[t.recommended]}` : '')}));
  opts.push(h('option', {value: -1, text: kind === 'audio' ? 'Silence' : 'Off'}));
  for (const o of opts) o.selected = (value == null ? 'auto' : String(value)) === o.value;
  return opts;
}
const parseChoice = v => v === 'auto' ? null : Number(v);

function sideEditor(side, p, value, onchange, disabled) {
  const field = kind => h('div', {class: 'field'}, h('label', {text: kind === 'audio' ? 'Audio' : 'Burned-in subtitles'}),
    h('select', {disabled, onchange: e => onchange(kind, parseChoice(e.target.value))}, ...trackOptions(p, side, kind, value[kind])));
  return [field('audio'), field('subtitle')];
}

function renderPlaylist() {
  const panel = $('playlistPanel');
  const has = B.previewing || B.previewError || B.items.length;
  panel.hidden = !has;
  if (!has) return;
  const n = B.items.length;
  $('playlistCount').textContent = B.previewing ? 'Reading…' : `${n} item${n === 1 ? '' : 's'}`;

  // Preview notes: errors, unmatched / ambiguous files.
  const notes = [];
  if (B.previewing) notes.push(h('div', {class: 'notes info'}, h('span', {class: 'spinner'}), 'Reading the folder…'));
  if (B.previewError) notes.push(h('div', {class: 'notes bad', text: B.previewError}));
  if (B.unmatched.length) notes.push(h('div', {class: 'notes'}, `${B.unmatched.length} file(s) could not be paired and were left out:`, h('ul', null, ...B.unmatched.slice(0, 20).map(u => h('li', {text: basename(u) || u}))), B.unmatched.length > 20 ? `…and ${B.unmatched.length - 20} more.` : null));
  if (B.ambiguous.length) notes.push(h('div', {class: 'notes'}, 'Duplicate episode identifiers were skipped:', h('ul', null, ...B.ambiguous.slice(0, 20).map(u => h('li', {text: basename(u) || u})))));
  if (n > 500) notes.push(h('div', {class: 'notes bad', text: `A playlist can hold at most 500 items; remove ${n - 500}.`}));
  $('previewNotes').replaceChildren(...notes);

  renderTrackChooser();
  renderItems();

  const ln = [];
  if (B.mapping?.running) ln.push(h('div', {class: 'notes info'}, h('span', {class: 'spinner'}), `Matching tracks in every file… ${B.mapping.done}/${B.mapping.total}`));
  else if (B.confirm) ln.push(h('div', {class: 'notes', text: `${B.items.filter(i => i.resolved?.warn.length).length} item(s) could not match the chosen tracks and will use automatic selection where noted. Review the list, then load again to confirm.`}));
  $('loadNotes').replaceChildren(...ln);

  const s = S.status;
  const load = $('load');
  load.disabled = B.loading || B.previewing || !n || n > 500 || !!B.mapping?.running || !s;
  load.textContent = B.loading ? 'Loading…' : B.confirm ? 'Load anyway' : s && s.media.phase !== 'none' ? 'Replace current selection' : 'Load selection';
}

function renderTrackChooser() {
  const box = $('trackChooser');
  if (!B.items.length) { box.replaceChildren(); return; }
  const kids = [];
  if (B.items.length > 1) {
    kids.push(h('div', {class: 'field ref'}, h('label', {text: 'Choose tracks using this item; they are matched by language and name in every other item'}),
      h('select', {onchange: e => loadReference(Number(e.target.value))},
        ...B.items.map((it, i) => h('option', {value: i, text: `${i + 1}. ${it.title}`, selected: i === B.ref})))));
  }
  if (B.refError) kids.push(h('div', {class: 'notes bad ref', text: B.refError}));
  const loadingRef = !B.refProbe.sub && !B.refError;
  for (const side of SIDES) {
    kids.push(h('div', {class: 'track-side'}, h('h3', {text: `${SIDE_LABEL[side]} output`}),
      loadingRef ? h('div', {class: 'muted small'}, h('span', {class: 'spinner'}), 'Reading tracks (up to 30 s)…')
        : sideEditor(side, B.refProbe[side], B.choice[side], (kind, v) => { B.choice[side][kind] = v; invalidateMapping(); renderPlaylist(); }, B.loading)));
  }
  box.replaceChildren(...kids);
}

function renderItems() {
  const paired = B.mode === 'paired';
  $('items').replaceChildren(...B.items.map((it, i) => {
    const badges = [];
    if (it.manual) badges.push(h('span', {class: 'badge ok', text: 'custom tracks'}));
    else if (it.resolved) badges.push(h('span', {class: 'badge ' + (it.resolved.warn.length ? 'warn' : 'ok'), text: it.resolved.warn.length ? 'check tracks' : 'tracks matched'}));
    if (i === B.start) badges.push(h('span', {class: 'badge ok', text: 'starts here'}));
    const sub = paired ? [h('small', {text: 'Sub: ' + basename(it.sub)}), h('small', {text: 'Dub: ' + basename(it.dub)})] : [];
    const row = h('div', {class: 'item-row'},
      h('span', {class: 'num', text: i + 1}),
      h('span', {class: 'ttl'}, it.title, ...badges, ...sub),
      h('span', {class: 'btns'},
        h('button', {class: 'subtle' + (i === B.start ? ' on' : ''), text: 'Start', title: 'Start playback from this item', disabled: B.loading, onclick: () => { B.start = i; renderPlaylist(); }}),
        h('button', {class: 'subtle' + (B.editing === i ? ' on' : ''), text: 'Tracks', title: 'Choose tracks for this item only', disabled: B.loading, onclick: () => toggleEdit(i)}),
        h('button', {class: 'subtle', text: '✕', title: 'Remove from playlist', 'aria-label': `Remove ${it.title}`, disabled: B.loading || B.items.length < 2, onclick: () => removeItem(i)})));
    const kids = [row];
    if (it.resolved?.warn.length) kids.push(h('div', {class: 'item-note', text: it.resolved.warn.join(' ')}));
    if (B.editing === i) kids.push(itemEditor(it));
    return h('li', {class: i === B.start ? 'start' : ''}, ...kids);
  }));
}

function removeItem(i) {
  B.items.splice(i, 1);
  if (i < B.start) B.start--;
  B.start = Math.min(B.start, B.items.length - 1);
  if (B.editing === i) B.editing = -1; else if (B.editing > i) B.editing--;
  if (B.ref === i) loadReference(0); else if (B.ref > i) B.ref--;
  invalidateMapping(); renderPlaylist();
}

function toggleEdit(i) {
  B.editing = B.editing === i ? -1 : i;
  if (B.editing === i) { probeInto(B.items[i].sub); probeInto(B.items[i].dub); }
  renderPlaylist();
}
function itemEditor(it) {
  const ps = probed.get(it.sub), pd = probed.get(it.dub);
  if (!ps || !pd) {
    // probeInto may have failed and been cleared; surface that rather than spinning forever.
    const failed = ps === false || pd === false;
    return h('div', {class: 'item-edit'}, h('div', {class: 'wide muted small'}, failed ? 'Could not read this item’s tracks.' : [h('span', {class: 'spinner'}), 'Reading tracks…']));
  }
  const cur = it.manual || it.resolved || null;
  const val = side => cur ? {audio: cur[side].audio, subtitle: cur[side].subtitle} : {audio: null, subtitle: null};
  const edit = side => (kind, v) => {
    it.manual = it.manual || {sub: val('sub'), dub: val('dub')};
    it.manual[side][kind] = v;
    renderPlaylist();
  };
  return h('div', {class: 'item-edit'},
    ...SIDES.map(side => h('div', {class: 'track-side'}, h('h3', {text: SIDE_LABEL[side]}), sideEditor(side, side === 'sub' ? ps : pd, val(side), edit(side), B.loading))),
    h('div', {class: 'wide'},
      it.manual ? h('button', {class: 'subtle compact', text: 'Use playlist tracks', onclick: () => { it.manual = null; renderPlaylist(); }}) : h('span', {class: 'muted small', text: 'Changing a track here overrides the playlist choice for this item only.'})));
}

// Map a track chosen in the reference file onto another file's tracks.
function matchTrack(refTracks, value, tracks, kind) {
  if (value == null || value === -1) return {value, warn: ''};
  const r = refTracks.find(t => t.index === value);
  const name = kind === 'audio' ? 'audio' : 'subtitle';
  if (!r) return {value: null, warn: `Chosen ${name} track missing.`};
  const same = (a, b) => (a || '').toLowerCase() === (b || '').toLowerCase();
  const cands = tracks.filter(t => t.kind === r.kind && !t.attached);
  let m = cands.filter(t => same(t.language, r.language) && same(t.title, r.title));
  if (m.length === 1) return {value: m[0].index, warn: ''};
  if (m.length > 1) { const x = m.find(t => t.index === r.index); if (x) return {value: x.index, warn: ''}; }
  m = cands.filter(t => same(t.language, r.language) && t.codec === r.codec);
  if (m.length === 1) return {value: m[0].index, warn: ''};
  const byIndex = cands.find(t => t.index === r.index && same(t.language, r.language));
  if (byIndex) return {value: byIndex.index, warn: ''};
  return {value: null, warn: `No ${name} track like “${trackLabel(r)}”; automatic selection used.`};
}

async function mapTracks() {
  const explicit = SIDES.some(side => B.choice[side].audio != null || B.choice[side].subtitle != null);
  const todo = B.items.filter(it => !it.manual && !it.resolved);
  if (!explicit || !todo.length) return true;
  const refTracks = {sub: B.refProbe.sub?.tracks || [], dub: B.refProbe.dub?.tracks || []};
  const token = B.previewToken;
  B.mapping = {running: true, done: 0, total: todo.length};
  renderPlaylist();
  let next = 0;
  async function worker() {
    while (next < todo.length) {
      const it = todo[next++];
      const resolved = {sub: {}, dub: {}, warn: []};
      for (const side of SIDES) {
        let tracks = [];
        try { tracks = (await probe(it[side])).tracks; }
        catch (e) { resolved.warn.push(`${SIDE_LABEL[side]}: could not read tracks (${e.message}); automatic selection used.`); }
        for (const kind of ['audio', 'subtitle']) {
          const r = tracks.length ? matchTrack(refTracks[side], B.choice[side][kind], tracks, kind) : {value: null, warn: ''};
          resolved[side][kind] = r.value;
          if (r.warn) resolved.warn.push(`${SIDE_LABEL[side]}: ${r.warn}`);
        }
      }
      if (token !== B.previewToken) return;
      it.resolved = resolved;
      B.mapping.done++;
      renderPlaylist();
    }
  }
  await Promise.all([worker(), worker(), worker()]);
  if (token !== B.previewToken) return false;
  B.mapping = null;
  const warned = B.items.some(it => !it.manual && it.resolved?.warn.length);
  if (warned) { B.confirm = true; renderPlaylist(); return false; }
  return true;
}

$('load').onclick = async () => {
  if (B.loading || !B.items.length) return;
  if (!(await mapTracks())) return;
  const explicit = SIDES.some(side => B.choice[side].audio != null || B.choice[side].subtitle != null);
  const source = (it, side) => {
    const out = {path: it[side]};
    const pick = it.manual?.[side] || (explicit ? it.resolved?.[side] : null);
    if (pick?.audio != null) out.audio = pick.audio;
    if (pick?.subtitle != null) out.subtitle = pick.subtitle;
    return out;
  };
  const body = {
    mode: B.mode, start_index: B.start,
    title: B.src.sub?.directory ? B.src.sub.name : B.items[0].title,
    items: B.items.map(it => ({title: it.title, sub: source(it, 'sub'), dub: source(it, 'dub')})),
  };
  B.loading = true; renderPlaylist();
  const s = await post('Load selection', '/api/media/load', body);
  B.loading = false;
  if (s) { B.confirm = false; accept(s); showNotice('Selection sent. It will be Ready once both outputs are prepared; press Play to start.'); }
  renderPlaylist();
};

// ---------- Settings ----------
const FIELDS = ['sub_server', 'dub_server', 'stream_path', 'sub_username', 'dub_username', 'music_folder', 'music_volume'];
let settingsDirty = false;
function fillSettings(st) {
  for (const f of FIELDS) $(f).value = st[f];
  $('volumeLabel').textContent = st.music_volume;
  for (const side of SIDES) { $(side + '_password').value = ''; }
  settingsDirty = false;
}
function renderSettings(s, running) {
  const st = s.settings;
  if (!settingsDirty) fillSettings(st);
  for (const side of SIDES) {
    $(side + '_password').placeholder = st[side + '_password_set'] ? 'Saved — leave blank to keep' : 'Not set';
    $(side + '_password_hint').textContent = st[side + '_password_set'] ? 'A password is stored. Blank keeps it.' : '';
  }
  for (const el of $('settingsForm').elements) el.disabled = running || !!S.pending;
  $('resetSettings').disabled = !settingsDirty || !!S.pending;
  $('settingsLock').textContent = running ? 'Stop streams to change destinations, credentials or music. The HUD can change while streaming.' : 'Changes apply when you press Apply settings.';
  const hud = $('hud_enabled');
  hud.checked = st.hud_enabled;
  hud.disabled = !!S.pending;
}
$('settingsForm').addEventListener('input', () => { settingsDirty = true; $('resetSettings').disabled = false; });
$('music_volume').addEventListener('input', e => $('volumeLabel').textContent = e.target.value);
$('resetSettings').onclick = () => S.status && fillSettings(S.status.settings);
$('useFolder').onclick = () => { $('music_folder').value = L.path; settingsDirty = true; };
$('settingsForm').addEventListener('submit', async e => {
  e.preventDefault();
  const st = S.status?.settings;
  if (!st) return;
  const patch = {};
  for (const f of FIELDS) {
    const v = f === 'music_volume' ? Number($(f).value) : $(f).value.trim();
    if (v !== st[f]) patch[f] = v;
  }
  for (const side of SIDES) { const p = $(side + '_password').value; if (p) patch[side + '_password'] = p; }
  if (!Object.keys(patch).length) { settingsDirty = false; render(); return; }
  const s = await post('Apply settings', '/api/settings', patch);
  if (s) { settingsDirty = false; accept(s); showNotice('Settings saved.'); }
});
$('hud_enabled').onchange = async e => {
  const s = await post('HUD', '/api/settings', {hud_enabled: e.target.checked});
  if (s) accept(s); else render();
};
$('rescan').onclick = async () => {
  const s = await command('rescan', undefined, 'Rescan');
  if (s) {
    probes.clear(); probed.clear();
    showNotice('Library rescan started. Folder previews below were refreshed; search results update once indexing finishes.');
    browse(true);
    if (B.src.sub) preview();
  }
};

// ---------- Diagnostics ----------
let diagSig = '';
function renderDiagnostics(s, running) {
  const d = s.diagnostics;
  const chip = $('diagChip');
  chip.textContent = d.running ? 'Running…' : d.supported ? (d.selected.kind ? `Qualified · ${d.selected.kind.toUpperCase()}` : 'Qualified') : 'Unsupported';
  chip.className = 'chip ' + (d.running ? 'warn' : d.supported ? 'ok' : 'bad');
  $('runDiag').disabled = !!S.pending || d.running || running;
  $('runDiag').textContent = d.running ? 'Qualification running…' : 'Run encoder qualification';
  const sig = JSON.stringify(d);
  if (sig === diagSig) return;
  diagSig = sig;
  const cpu = d.cpu;
  const facts = [
    ['Status', d.running ? 'Running a qualification test' : d.supported ? 'Supported' : 'Not supported — running anyway is an explicit unsupported configuration'],
    ['Selected encoder', d.selected.kind ? `${d.selected.kind.toUpperCase()}${d.selected.device ? ` (device ${d.selected.device})` : ''}` : 'None'],
    ['CPU fallback', d.cpu_fallback ? 'Qualified' : 'Not qualified'],
    ['CPU', `${cpu.usable_vcpus} usable ${cpu.virtual ? 'vCPUs' : 'cores'}${cpu.virtual ? ` · ${cpu.virtualization || 'virtual machine'}` : ''}${cpu.physical_cores ? ` · ${cpu.physical_cores} physical cores` : ''} · budget ${cpu.budget}${cpu.eligible ? '' : ' · below fallback threshold'}`],
    ['Basis', cpu.basis],
    ['Missing tools', d.missing.length ? d.missing.join(', ') : 'None'],
    ['FFmpeg', d.ffmpeg || 'Unknown'],
    ['Tested', d.created ? new Date(d.created).toLocaleString() : 'Never'],
  ];
  if (d.warning) facts.push(['Warning', d.warning]);
  const table = d.tests.length ? h('div', {class: 'table-wrap'}, h('table', null,
    h('thead', null, h('tr', null, ...['Candidate', 'Type', 'Result', 'fps/output', 'Error'].map(t => h('th', {text: t})))),
    h('tbody', null, ...d.tests.map(t => h('tr', null,
      h('td', {text: t.name || '—'}),
      h('td', {text: (t.encoder.kind || '—').toUpperCase() + (t.encoder.device ? ` ${t.encoder.device}` : '')}),
      h('td', null, h('span', {class: 'badge ' + (t.passed ? 'ok' : t.tested ? 'bad' : ''), text: !t.available ? 'unavailable' : !t.tested ? 'not tested' : t.passed ? 'passed' : 'failed'})),
      h('td', {class: 'num', text: t.tested ? t.fps_per_output.toFixed(1) : '—'}),
      h('td', {text: t.error || ''})))))) : h('p', {class: 'muted', text: 'No candidate results recorded.'});
  $('diagBody').replaceChildren(h('dl', {class: 'diag-grid'}, ...facts.flatMap(([k, v]) => [h('dt', {text: k}), h('dd', {text: v})])), table);
}
$('runDiag').onclick = async () => {
  const d = await post('Diagnostics', '/api/diagnostics', {});
  if (d) { showNotice('Encoder qualification started. This can take several minutes.'); poll(); }
};

// ---------- Boot ----------
renderSlots();
browse(true);
poll();

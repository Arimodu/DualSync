'use strict';
// DualSync twin console. Talks only to the same-origin /api/* contract in api/openapi.yaml.

// ---------- helpers ----------
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
// replaceChildren() would print null as the text "null", so empty children are dropped first.
function fill(el, ...kids) { el.replaceChildren(...kids.flat().filter(k => k != null && k !== false)); }
const SVGNS = 'http://www.w3.org/2000/svg';
function icon(id) {
  const s = document.createElementNS(SVGNS, 'svg'), u = document.createElementNS(SVGNS, 'use');
  u.setAttribute('href', '#' + id); s.append(u); s.setAttribute('aria-hidden', 'true');
  return s;
}
const SIDES = ['sub', 'dub'];
const SIDE = {sub: 'Sub', dub: 'Dub'};
const MARK = {sub: '字幕', dub: '吹替'};
const PHASE = {none: 'No media', loading: 'Loading', ready: 'Ready', playing: 'Playing', paused: 'Paused', resuming: 'Resuming', seeking: 'Seeking', buffering: 'Buffering', ended: 'Finished'};
const basename = p => (p || '').split('/').filter(Boolean).pop() || '';
const dirname = p => (p || '').split('/').slice(0, -1).join('/');
function clock(sec) {
  sec = Math.max(0, Math.floor(sec || 0));
  const hh = Math.floor(sec / 3600), mm = Math.floor(sec / 60) % 60, ss = String(sec % 60).padStart(2, '0');
  return hh ? `${hh}:${String(mm).padStart(2, '0')}:${ss}` : `${mm}:${ss}`;
}
// "[Group] Show - S04E01 [1080p] [ABCD].mkv" -> "Show - S04E01"
function niceTitle(name) {
  const raw = basename(name);
  const s = raw.replace(/\.[a-z0-9]{2,4}$/i, '')
    .replace(/\[[^\]]*\]|\([^)]*\)|\{[^}]*\}/g, ' ')
    .replace(/_+/g, ' ').replace(/\s+/g, ' ')
    .replace(/^[\s\-–—.]+|[\s\-–—.]+$/g, '');
  return s || raw;
}
// Shared leading words of all titles, used to split "Series - E01" into series and episode.
function commonPrefix(titles) {
  if (titles.length < 2) return '';
  let p = titles[0];
  for (const t of titles) while (p && !t.startsWith(p)) p = p.slice(0, -1);
  const cut = p.search(/[\s\-–—]*[^\s\-–—]*$/);
  p = p.slice(0, cut).replace(/[\s\-–—]+$/, '');
  return p.length >= 4 ? p : '';
}
const ISO3 = {jpn: 'ja', eng: 'en', ger: 'de', deu: 'de', fre: 'fr', fra: 'fr', spa: 'es', ita: 'it', por: 'pt', rus: 'ru', chi: 'zh', zho: 'zh', kor: 'ko', pol: 'pl', ces: 'cs', cze: 'cs', dut: 'nl', nld: 'nl', ara: 'ar', hin: 'hi', tha: 'th', vie: 'vi', ind: 'id', tur: 'tr', swe: 'sv', nor: 'no', dan: 'da', fin: 'fi', hun: 'hu', ukr: 'uk'};
const langNames = (() => { try { return new Intl.DisplayNames(['en'], {type: 'language'}); } catch { return null; } })();
function langName(code) {
  if (!code || code === 'und') return '';
  const c = ISO3[code.toLowerCase()] || code;
  try { const n = langNames?.of(c); if (n && n !== c) return n; } catch { /* unknown code */ }
  return code;
}
function trackLabel(t) {
  if (!t) return '';
  const lang = langName(t.language);
  const head = [lang, t.title && t.title !== lang ? t.title : ''].filter(Boolean).join(', ') || `Track ${t.index}`;
  return `${head} (${t.codec}${t.channels ? ` ${t.channels}ch` : ''})`;
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
  try { data = await res.json(); } catch { /* not JSON */ }
  if (!res.ok) throw new ApiError(data?.error || `Request failed (HTTP ${res.status}).`, data?.code || 'HTTP_' + res.status, res.status);
  return data;
}
// A command whose outcome is unknown is never retried; status is refreshed instead.
const uncertain = e => e.code === 'NETWORK' || (e.status >= 500 && e.code === 'HTTP_' + e.status);

// ---------- state ----------
const S = {status: null, rev: -1, at: 0, online: null, pending: null, errors: [], musicMoving: false, lowerRevs: 0};

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
let toastTimer = 0;
function toast(text) {
  const t = $('toast');
  t.textContent = text; t.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(() => { t.hidden = true; }, 5000);
}

let polling = false, pollAgain = false, pollTimer = 0, pollFails = 0;
async function poll() {
  if (polling) { pollAgain = true; return; }
  polling = true; clearTimeout(pollTimer);
  try {
    const s = await api('/api/status');
    S.online = true; pollFails = 0;
    // A restarted backend starts counting revisions again; accept it after repeated lower revisions.
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
      showError(`${e.message} It is unknown whether “${label}” happened, so it was not retried. Check the console before trying again.`);
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

// ---------- views ----------
const VIEWS = ['console', 'library', 'streams', 'system'];
function route() {
  const v = VIEWS.includes(location.hash.slice(1)) ? location.hash.slice(1) : 'console';
  for (const name of VIEWS) $('v-' + name).hidden = name !== v;
  for (const t of document.querySelectorAll('.tab')) {
    if (t.dataset.view === v) t.setAttribute('aria-current', 'page'); else t.removeAttribute('aria-current');
  }
  if (v === 'console') scrollEpisodes = true;
  render();
}
addEventListener('hashchange', route);

// ---------- rendering ----------
function renderBanners() {
  const box = $('banners'), s = S.status, list = [];
  if (S.online === false) list.push(['', 'Lost connection to DualSync. Retrying.']);
  if (s && !s.diagnostics.supported) list.push(['warn', 'This computer is not qualified to encode both streams. ' + (s.diagnostics.warning || 'See System for details.')]);
  else if (s?.diagnostics.warning) list.push(['warn', s.diagnostics.warning]);
  if (s?.error) list.push(['', s.error]);
  for (const e of S.errors) list.push(['', e.text, e.key]);
  const sig = JSON.stringify(list);
  if (box.dataset.sig === sig) return;
  box.dataset.sig = sig;
  box.replaceChildren(...list.map(([cls, text, key]) => h('div', {class: 'banner ' + cls, role: cls ? null : 'alert'},
    h('span', {text}),
    key ? h('button', {text: 'Dismiss', onclick: () => showError('', key)}) : null)));
}

function render() {
  renderBanners();
  const conn = $('conn');
  conn.className = 'conn ' + (S.online ? 'on' : S.online === false ? 'off' : '');
  conn.querySelector('span').textContent = S.online ? 'Connected' : S.online === false ? 'Offline' : 'Connecting';
  const s = S.status;
  if (!s) return;
  const running = s.mode !== 'stopped';
  renderConsole(s, running);
  renderMusic(s, running);
  renderLibrary();
  renderBuilder();
  renderSettings(s, running);
  renderSystem(s, running);
  tick();
}

// What is left after the shared series name, unless that is only an episode number.
function episodeLabels(titles, prefix) {
  return titles.map(t => {
    const rest = prefix ? t.slice(prefix.length).replace(/^[\s\-–—:]+/, '') : t;
    return /^(s\d+\s*)?(e|ep|episode)?\.?\s*\d+(v\d)?$/i.test(rest) ? '' : rest;
  });
}
// Loaded items carry Plex metadata when matched; otherwise names come from file names, and failing
// that from a Plex lookup of the show, season and episode the file names describe.
function episodeNames(m) {
  const titles = m.items.map(it => niceTitle(it.title || it.sub.path || ''));
  const prefix = commonPrefix(titles);
  const fileNames = episodeLabels(titles, prefix);
  const meta = m.metadata?.matched ? m.metadata : null;
  const looked = EN.sig === itemsSig(m) ? EN.names : [];
  return {
    series: meta ? (meta.show || meta.title) : prefix || (m.title ? niceTitle(m.title) : ''),
    // The cards already number the episodes, so matched items show just the episode title.
    names: m.items.map((it, i) => it.metadata?.matched ? it.metadata.title : fileNames[i] || looked[i] || ''),
  };
}

// ---------- episode names for file selections, looked up on Plex ----------
// Display only: nothing about the loaded selection changes. Runs once per selection.
const EN = {sig: '', names: [], running: false};
const itemsSig = m => m.items.map(it => (it.sub.path || it.sub.plex?.id || '') + '|' + (it.dub.path || it.dub.plex?.id || '')).join('\n');

// "Show - S04E01", "Show S4 - 01" or "Show - 01" (season from a "Season 4" folder, else 1).
function parseEpisode(path) {
  if (!path) return null;
  const t = niceTitle(path);
  let m = t.match(/^(.*?)[\s\-–_.]*S(\d{1,2})\s*E(\d{1,3})\b/i) || t.match(/^(.*?)\s+S(\d{1,2})\s*-\s*(\d{1,3})\b/i);
  if (m) return {series: m[1].replace(/[\s\-–—_.]+$/, ''), season: +m[2], episode: +m[3]};
  m = t.match(/^(.*?)\s*-\s*(\d{1,3})$/);
  if (m) { const sm = path.match(/season\s*(\d+)/i); return {series: m[1].trim(), season: sm ? +sm[1] : 1, episode: +m[2]}; }
  return null;
}
const normTitle = t => t.toLowerCase().replace(/[^a-z0-9]+/g, ' ').trim();

async function lookupEpisodeNames(m) {
  const sig = itemsSig(m);
  EN.sig = sig; EN.names = []; EN.running = true;
  try {
    const parsed = m.items.map(it => [parseEpisode(it.sub.path), parseEpisode(it.dub.path)]);
    const series = [...new Set(parsed.flat().filter(Boolean).map(p => p.series).filter(x => x.length >= 3))];
    if (!series.length) return;
    const libs = (await api('/api/plex/libraries')).libraries.filter(l => l.type === 'show');
    const shows = new Map(); // series name -> Plex show id
    for (const name of series) {
      for (const lib of libs) {
        const r = await api(`/api/plex/videos?${new URLSearchParams({library_id: lib.id, q: name, limit: 10})}`);
        const hits = r.entries.filter(e => e.type === 'show');
        const hit = hits.find(e => normTitle(e.title) === normTitle(name)) || hits[0];
        if (hit) { shows.set(name, hit.id); break; }
      }
    }
    if (!shows.size) return;
    const episodes = new Map(); // "show:season:episode" -> title
    const seasonsFor = new Map();
    for (const [i, pair] of parsed.entries()) {
      for (const p of pair) {
        if (!p || !shows.has(p.series)) continue;
        const showId = shows.get(p.series), key = `${showId}:${p.season}`;
        if (!seasonsFor.has(key)) {
          seasonsFor.set(key, (async () => {
            const seasons = (await api(`/api/plex/videos?parent_id=${showId}&limit=200`)).entries;
            const season = seasons.find(x => x.type === 'season' && x.season === p.season);
            if (!season) return;
            for (let cursor = 0; cursor >= 0;) {
              const r = await api(`/api/plex/videos?${new URLSearchParams({parent_id: season.id, cursor, limit: 200})}`);
              for (const e of r.entries) if (e.type === 'episode') episodes.set(`${key}:${e.episode}`, e.title);
              cursor = r.next_cursor;
            }
          })());
        }
        await seasonsFor.get(key);
        const title = episodes.get(`${key}:${p.episode}`);
        if (title && EN.sig === sig) { EN.names[i] = title; break; }
      }
    }
  } catch { /* names are a nicety; leave the cards as they are */ }
  finally { if (EN.sig === sig) { EN.running = false; epSig = ''; render(); } }
}
const HEALTH_HINT = [
  'The eight bars are this stream’s encoder queue: frames waiting to be encoded and sent to its server.',
  'Empty, or one or two filled, is normal. The encoder and the connection are keeping up.',
  'More bars filling means this stream is falling behind, because the encoder is busy or the connection to its server is slow.',
  'When all eight fill, playback holds on both streams and viewers see Buffering until it catches up.',
  'The text names the encoder in use (NVENC is the graphics card, CPU is the processor) and how many times the connection to the server had to be reopened.',
].join('\n\n');

for (const hint of document.querySelectorAll('.health .help')) {
  hint.dataset.hint = HEALTH_HINT;
  hint.setAttribute('aria-label', HEALTH_HINT);
}

let epSig = '', scrollEpisodes = true, lastIndex = -1;
function renderConsole(s, running) {
  const m = s.media, phase = m.phase, st = s.settings;
  const configured = !!(st.sub_server && st.dub_server && st.stream_path);
  if (m.items.length && plexReady() && EN.sig !== itemsSig(m) && m.items.some(it => !it.metadata?.matched)) lookupEpisodeNames(m);
  const {series, names} = episodeNames(m);

  // header
  const poster = $('poster'), posterUrl = phase !== 'none' && m.metadata?.poster_url;
  if (posterUrl) { if (poster.getAttribute('src') !== posterUrl) poster.src = posterUrl; poster.hidden = false; }
  else { poster.hidden = true; poster.removeAttribute('src'); }
  if (phase === 'none') {
    $('series').textContent = 'Nothing loaded';
    $('headline').textContent = 'Choose an episode in the library';
  } else {
    $('series').textContent = series || 'Selection';
    $('headline').textContent = `Episode ${m.index + 1} of ${m.total}${names[m.index] ? `: ${names[m.index]}` : ''}`;
  }
  let live = 0;
  for (const side of SIDES) if (s.outputs[side]?.ready) live++;
  const onAir = $('onAir');
  onAir.className = 'on-air ' + (!running ? '' : live === 2 ? 'live' : 'warn');
  onAir.querySelector('span').textContent = !running ? 'Streams stopped' : live === 2 ? 'Live' : `${live} of 2 streams live`;
  const sb = $('streamBtn');
  sb.className = 'btn ' + (running ? 'stop' : 'go');
  sb.querySelector('span').textContent = S.pending === 'Start streams' ? 'Starting' : S.pending === 'Stop streams' ? 'Stopping' : running ? 'Stop streams' : 'Start streams';
  sb.disabled = !!S.pending || (!running && !configured);

  // load operation
  const op = m.operation, opBox = $('operation');
  opBox.hidden = !(op.state === 'running' || op.state === 'failed');
  opBox.className = 'operation' + (op.state === 'failed' ? ' failed' : '');
  if (op.state === 'running') opBox.replaceChildren(h('span', {class: 'spinner'}), 'Preparing the selection on both channels');
  else if (op.state === 'failed') opBox.textContent = 'Loading failed. ' + (op.error || s.error || 'The server gave no reason.');

  // channels
  const item = m.items[m.index];
  for (const side of SIDES) {
    const o = s.outputs[side], ch = $('ch-' + side);
    let state = 'Stopped', cls = 'channel ' + side;
    if (o) {
      if (o.ready) { state = 'Live'; cls += ' live'; }
      else if (o.error) { state = o.restarts ? 'Reconnecting' : 'Failing'; cls += ' fault'; }
      else { state = 'Connecting'; cls += ' connecting'; }
    }
    ch.className = cls;
    ch.querySelector('.state').textContent = state;
    // The address lives on the Streams screen; here it only appears when something is missing.
    ch.querySelector('.dest-line').textContent = st[side + '_server'] ? '' : 'Server not set. Add it in Streams.';

    const tracks = ch.querySelector('.tracks');
    if (item) {
      const src = item[side];
      probeInto(src);
      const p = probedOf(src);
      const describe = (kind, v) => {
        if (v === -1) return [kind === 'audio' ? 'Silence' : 'Off', ''];
        if (v == null) return ['Automatic', ''];
        const t = p?.tracks.find(x => x.index === v);
        if (!t) return [p === null ? 'Reading…' : `Track ${v}`, ''];
        const lang = langName(t.language) || `Track ${t.index}`;
        return [lang, [t.title && t.title !== lang ? t.title : '', `${t.codec}${t.channels ? ` ${t.channels}ch` : ''}`].filter(Boolean).join(', ')];
      };
      const row = (label, [b, em]) => h('div', {class: 'track'}, h('small', {text: label}), h('b', {text: b}), em ? h('em', {text: em}) : null);
      const fileLine = src.plex
        ? h('div', {class: 'track'}, h('small', {text: 'Source'}), h('em', {text: src.transport === 'plex' ? 'Plex, streamed from the server' : 'Plex, read from local storage'}))
        : m.mode === 'paired' ? h('div', {class: 'track'}, h('small', {text: 'File'}), h('em', {text: niceTitle(src.path)})) : null;
      fill(tracks, row('Audio', describe('audio', src.audio)), row('Subtitles', describe('subtitle', src.subtitle)), fileLine);
    } else tracks.replaceChildren(h('div', {class: 'track'}, h('small', {text: 'Nothing loaded'})));

    // Updated in place, never rebuilt, so the help mark under the pointer keeps its tooltip open.
    const health = ch.querySelector('.health');
    health.hidden = !o;
    if (o) {
      const q = health.querySelector('.queue');
      q.setAttribute('aria-label', `Queue ${o.queued_frames} of 8 frames`);
      q.querySelectorAll('i').forEach((bar, i) => bar.classList.toggle('f', i < o.queued_frames));
      health.querySelector('.enc').textContent = `${o.encoder.toUpperCase()}, ${o.restarts ? `${o.restarts} restart${o.restarts === 1 ? '' : 's'}` : 'no restarts'}`;
      health.querySelector('.err').textContent = o.error || '';
    }
  }

  // spine
  const ph = $('phase');
  ph.className = 'phase ' + (phase === 'playing' ? 'ok' : ['buffering', 'loading', 'seeking', 'resuming'].includes(phase) ? 'warn' : '');
  ph.querySelector('span').textContent = PHASE[phase];
  const a = s.outputs.sub, b = s.outputs.dub;
  $('sync').replaceChildren(...(a && b ? ['Outputs ', h('b', {text: `${Math.abs(a.out_time - b.out_time).toFixed(1)} s`}), ' apart'] : []));

  // transport
  const settled = phase !== 'none' && phase !== 'loading';
  const pausable = ['playing', 'resuming', 'buffering'].includes(phase);
  const play = $('kPlay');
  play.querySelector('use').setAttribute('href', pausable ? '#i-pause' : phase === 'ended' ? '#i-replay' : '#i-play');
  play.querySelector('span').textContent = pausable ? 'Pause' : phase === 'paused' ? 'Resume' : phase === 'ended' ? 'Replay' : 'Play';
  play.disabled = !!S.pending || !(pausable || (running && ['ready', 'paused', 'ended'].includes(phase)));
  for (const k of ['kBack', 'kFwd', 'kSkip']) $(k).disabled = !!S.pending || !settled;
  $('kUnload').disabled = !!S.pending || phase === 'none';
  $('consoleNote').textContent =
    !configured && !running ? 'Add both servers in Streams before starting.' :
    phase === 'none' ? (running ? 'Both streams are showing the idle screen. Choose an episode in the library.' : '') :
    phase === 'loading' ? 'Loading. Playback waits for you to press Play.' :
    !running && ['ready', 'paused', 'ended'].includes(phase) ? 'Start streams to play.' :
    phase === 'buffering' ? 'Holding the position until both channels catch up.' :
    phase === 'ended' ? 'Playlist finished. Replay restarts the last episode.' : '';

  // episodes
  const sig = JSON.stringify([names, m.index]);
  if (sig !== epSig) {
    epSig = sig;
    const eps = $('eps');
    if (!m.items.length) eps.replaceChildren(h('div', {class: 'empty', text: 'Load a folder or a video from the library to fill this list.'}));
    else eps.replaceChildren(...m.items.map((it, i) => h('button', {
      class: 'ep' + (i === m.index ? ' now' : i < m.index ? ' past' : ''), 'aria-current': i === m.index ? 'true' : null,
      title: niceTitle(it.title || it.sub.path),
      onclick: () => { if (i !== S.status.media.index) command('episode', i + 1, 'Change episode'); },
    }, h('b', {text: `E${String(i + 1).padStart(2, '0')}`}), names[i] ? h('span', {text: names[i]}) : null)));
    if (m.index !== lastIndex) scrollEpisodes = true;
    lastIndex = m.index;
  }
  for (const e of document.querySelectorAll('#eps .ep')) e.disabled = !!S.pending || !settled;
  if (scrollEpisodes && !$('v-console').hidden) {
    const eps = $('eps'), now = eps.querySelector('.now');
    if (now) eps.scrollLeft = now.offsetLeft - eps.offsetLeft - (eps.clientWidth - now.offsetWidth) / 2;
    scrollEpisodes = false;
  }
}

function musicSourceLabel(s) {
  const st = s.settings, mu = s.music;
  if (st.music_source === 'off') return 'Music off';
  if (st.music_source === 'plex') return `${mu.playlist_title || plexTitle(st.plex_playlist_id) || 'Playlist'} on Plex`;
  return st.music_folder ? `${basename(st.music_folder)} folder` : 'No folder chosen';
}
function songTitle(x) { return x.title || niceTitle(x.path.startsWith('plex:') ? '' : x.path) || 'Unknown song'; }

let upSig = '';
function renderMusic(s, running) {
  const mu = s.music, st = s.settings, off = st.music_source === 'off';
  $('musicHead').textContent = mu.active && mu.gain > 0 ? `Idle music, playing at ${Math.round(mu.gain * 100)}%` : 'Idle music';
  $('musicSource').textContent = musicSourceLabel(s);
  $('mTitle').textContent = mu.active ? songTitle(mu)
    : off ? 'Music is off'
    : mu.preparation_state === 'preparing' ? 'Getting songs ready'
    : 'Plays while paused or idle';
  $('mArtist').textContent = mu.active ? [mu.album_artist || mu.artist, mu.album].filter(Boolean).join(', ')
    : off ? 'Turn it on in Streams.' : '';
  const img = $('cover');
  if (mu.active && mu.cover_url) { if (img.getAttribute('src') !== mu.cover_url) img.src = mu.cover_url; img.hidden = false; }
  else { img.hidden = true; img.removeAttribute('src'); }
  $('artNone').style.display = img.hidden ? '' : 'none';
  $('musicNext').disabled = !!S.pending || !running || off || !(mu.active || mu.ready_count > 0);
  if (!M.volDragging) { $('consoleVol').value = st.music_volume; $('consoleVolLabel').textContent = st.music_volume + '%'; }

  const up = mu.upcoming || [];
  const sig = JSON.stringify([off, up, mu.ready_count, mu.preparation_state, mu.error]);
  if (sig === upSig) return;
  upSig = sig;
  const rows = [];
  if (!off && up.length) {
    rows.push(h('h3', {text: 'Up next'}));
    up.slice(0, 3).forEach((x, i) => {
      const thumb = h('div', {class: 'thumb'});
      if (x.cover_url) thumb.append(h('img', {src: x.cover_url, alt: '', onerror: e => e.target.replaceWith(icon('i-note'))}));
      else thumb.append(icon('i-note'));
      const ready = i < mu.ready_count;
      rows.push(h('div', {class: 'song' + (ready ? '' : ' wait')}, thumb,
        h('div', null, h('b', {text: songTitle(x)}), h('span', {text: x.artist || ''})),
        h('em', {text: ready ? (x.duration ? clock(x.duration) : '') : 'Preparing'})));
    });
  }
  if (!off && mu.preparation_state === 'preparing' && up.length < 3) rows.push(h('div', {class: 'reading'}, h('span', {class: 'spinner'}), 'Preparing the next songs'));
  if (!off && mu.preparation_state === 'failed') rows.push(h('div', {class: 'note bad', text: mu.error || 'No songs could be prepared. Check the music source in Streams.'}));
  fill($('upNext'), rows);
}
$('cover').addEventListener('error', () => { $('cover').hidden = true; $('artNone').style.display = ''; });

// Positions advance between polls for display only, then snap back to the server.
function tick() {
  const s = S.status;
  if (!s) return;
  const dt = (performance.now() - S.at) / 1000, m = s.media;
  const pos = m.phase === 'playing' && S.online ? Math.min(m.duration || Infinity, m.position + dt) : m.position;
  const pct = m.duration ? Math.min(100, pos / m.duration * 100) : 0;
  $('clock').textContent = clock(pos);
  $('total').textContent = 'of ' + clock(m.duration);
  $('tlDone').style.width = pct + '%';
  $('tlHead').style.left = pct + '%';
  $('tlEnd').textContent = clock(m.duration);
  $('tlNote').textContent = m.duration ? `${clock(Math.max(0, m.duration - pos))} left` : '';
  const mu = s.music;
  const mpos = S.musicMoving && S.online ? Math.min(mu.duration || Infinity, mu.position + dt) : mu.position;
  $('mBar').style.width = mu.active && mu.duration ? `${Math.min(100, mpos / mu.duration * 100)}%` : '0';
  $('mTime').textContent = mu.active ? (mu.duration ? `${clock(mpos)} of ${clock(mu.duration)}` : clock(mpos)) : '';
}
setInterval(tick, 250);

// ---------- console controls ----------
$('streamBtn').onclick = () => S.status && (S.status.mode === 'stopped' ? command('start', undefined, 'Start streams') : command('stop', undefined, 'Stop streams'));
$('kPlay').onclick = () => {
  const ph = S.status?.media.phase;
  if (['playing', 'resuming', 'buffering'].includes(ph)) command('pause', undefined, 'Pause');
  else command('play', undefined, 'Play');
};
for (const id of ['kBack', 'kFwd', 'kSkip']) $(id).onclick = () => command('seek', Number($(id).dataset.seek), 'Seek');
$('kUnload').onclick = () => command('unload', undefined, 'Unload');
$('musicNext').onclick = () => command('music_next', undefined, 'Next song');

// ---------- library browser: files and Plex ----------
// One browser, two sources. Files come from the media root; Plex from the configured server.
const L = {source: 'files', path: '', q: '', entries: [], next: 0, indexing: false, truncated: false, loading: false, token: 0, error: '', retry: 0,
  // Plex: libraries at the top, then a stack of opened collections (library, show, season).
  libraries: null, stack: []};

const plexReady = () => !!(S.status?.settings.plex_server && S.status.settings.plex_token_set);

async function browse(reset) {
  const token = ++L.token;
  clearTimeout(L.retry);
  if (reset) { L.entries = []; L.next = 0; }
  if (L.next < 0) return;
  L.loading = true; L.error = ''; renderLibrary();
  try {
    if (L.source === 'files') {
      const qs = new URLSearchParams({path: L.path, cursor: L.next, limit: 100});
      if (L.q) qs.set('q', L.q);
      const r = await api('/api/library?' + qs);
      if (token !== L.token) return;
      L.entries.push(...r.entries.map(e => ({...e, kind: 'file'})));
      L.next = r.next_cursor; L.indexing = r.indexing; L.truncated = r.truncated;
      // Search results are partial while indexing; start again from the top until it finishes.
      if (r.indexing && L.q) L.retry = setTimeout(() => browse(true), 2000);
    } else if (!L.stack.length) {
      if (!plexReady()) { L.libraries = null; L.next = -1; return; }
      const r = await api('/api/plex/libraries');
      if (token !== L.token) return;
      L.libraries = r.libraries;
      L.entries = r.libraries.map(l => ({kind: 'plex', type: 'library', id: l.id, title: l.title, libType: l.type}));
      L.next = -1;
    } else {
      const top = L.stack[L.stack.length - 1];
      const qs = new URLSearchParams({cursor: L.next, limit: 100});
      qs.set(top.type === 'library' ? 'library_id' : 'parent_id', top.id);
      if (L.q) qs.set('q', L.q);
      const r = await api('/api/plex/videos?' + qs);
      if (token !== L.token) return;
      const show = L.stack.find(x => x.type === 'show');
      L.entries.push(...r.entries.map(e => ({...e, kind: 'plex', show: show?.title || '', parentTitle: top.title})));
      L.next = r.next_cursor;
    }
  } catch (e) {
    if (token === L.token) L.error = e.message;
  } finally {
    if (token === L.token) { L.loading = false; renderLibrary(); }
  }
}
function resetSearch() { L.q = ''; $('search').value = ''; $('rows').scrollTop = 0; }
function go(path) { L.path = path; resetSearch(); browse(true); }
function openPlex(stack) { L.stack = stack; resetSearch(); browse(true); }
function setSource(src) {
  if (L.source === src) return;
  L.source = src; L.error = '';
  for (const b of document.querySelectorAll('[data-libsrc]')) b.setAttribute('aria-pressed', String(b.dataset.libsrc === src));
  resetSearch(); browse(true);
}
for (const b of document.querySelectorAll('[data-libsrc]')) b.onclick = () => setSource(b.dataset.libsrc);
let searchTimer = 0;
$('search').addEventListener('input', e => {
  clearTimeout(searchTimer);
  searchTimer = setTimeout(() => { L.q = e.target.value.trim(); browse(true); }, 300);
});
$('more').onclick = () => browse(false);
$('upBtn').onclick = () => L.source === 'files' ? go(dirname(L.path)) : openPlex(L.stack.slice(0, -1));

const srcKeyOf = src => !src ? '' : src.kind === 'plex' ? 'p:' + src.id : 'f:' + src.path;
const entryKey = e => e.kind === 'plex' ? 'p:' + e.id : 'f:' + e.path;

let libSig = '';
function renderLibrary() {
  const sig = JSON.stringify([L.source, L.path, L.stack, L.q, L.entries.length, L.loading, L.error, L.indexing, L.truncated, L.next,
    B.mode, srcKeyOf(B.src.sub), srcKeyOf(B.src.dub), plexReady()]);
  if (sig === libSig) return;
  libSig = sig;
  const files = L.source === 'files';
  const crumbs = [];
  if (files) {
    const parts = L.path ? L.path.split('/') : [];
    crumbs.push(parts.length ? h('button', {text: 'Library', onclick: () => go('')}) : h('b', {text: 'Library'}));
    parts.forEach((p, i) => {
      crumbs.push(h('span', {text: '/', 'aria-hidden': 'true'}));
      const target = parts.slice(0, i + 1).join('/');
      crumbs.push(i === parts.length - 1 ? h('b', {text: p}) : h('button', {text: p, onclick: () => go(target)}));
    });
  } else {
    crumbs.push(L.stack.length ? h('button', {text: 'Plex', onclick: () => openPlex([])}) : h('b', {text: 'Plex'}));
    L.stack.forEach((x, i) => {
      crumbs.push(h('span', {text: '/', 'aria-hidden': 'true'}));
      crumbs.push(i === L.stack.length - 1 ? h('b', {text: x.title}) : h('button', {text: x.title, onclick: () => openPlex(L.stack.slice(0, i + 1))}));
    });
  }
  fill($('crumbs'), crumbs);
  $('upBtn').disabled = files ? !L.path : !L.stack.length;
  const here = files ? (L.path ? basename(L.path) : '') : (L.stack[L.stack.length - 1]?.title || '');
  $('search').hidden = !files && !L.stack.length;
  $('search').placeholder = here ? `Search in ${here}` : 'Search the library';

  const msgs = [];
  if (L.error) msgs.push(L.error);
  if (files && L.q && L.indexing) msgs.push('Still indexing the library. Results will fill in as it finishes.');
  if (files && L.truncated) msgs.push('The library has more than 200,000 entries, so some files cannot be found by search.');
  $('libNotice').hidden = !msgs.length;
  $('libNotice').textContent = msgs.join(' ');

  const rows = L.entries.map(entryRow);
  if (!rows.length) {
    const empty = L.loading ? 'Loading…'
      : !files && !plexReady() ? 'Connect your Plex server first: Streams, Idle music, Plex playlist.'
      : L.q ? `Nothing matches “${L.q}”.`
      : files ? 'This folder has no folders or videos.' : 'Nothing here.';
    rows.push(h('div', {class: 'empty'}, empty));
  }
  fill($('rows'), rows);
  $('more').hidden = L.next < 0 || L.loading || !L.entries.length;
}

// A Plex entry's name as a viewer would say it.
function plexEntryName(e) {
  if (e.type === 'episode') return `${e.episode ? `E${String(e.episode).padStart(2, '0')} ` : ''}${e.title}`;
  return e.title;
}
function entryRow(e) {
  let name, ico, src = null, open = null, sub = 'Video';
  if (e.kind === 'file') {
    ico = e.directory ? 'i-folder' : 'i-film';
    sub = L.q ? (dirname(e.path) || 'Library') : e.directory ? 'Folder' : 'Video';
    name = e.name;
    src = {kind: 'file', path: e.path, directory: e.directory, name: e.name};
    if (e.directory) open = () => go(e.path);
  } else {
    name = plexEntryName(e);
    if (e.type === 'library') {
      ico = 'i-library'; sub = e.libType === 'movie' ? 'Movie library' : 'Show library';
      open = () => openPlex([{type: 'library', id: e.id, title: e.title}]);
    } else if (e.type === 'show') {
      ico = 'i-folder'; sub = 'Show';
      open = () => openPlex([...L.stack, {type: 'show', id: e.id, title: e.title}]);
    } else if (e.type === 'season') {
      ico = 'i-folder'; sub = `Season of ${e.show || e.parentTitle}`;
      open = () => openPlex([...L.stack, {type: 'season', id: e.id, title: e.title}]);
      src = {kind: 'plex', id: e.id, type: 'season', directory: true, name: `${e.show || e.parentTitle} ${e.title}`, where: e.show || e.parentTitle};
    } else {
      ico = 'i-film'; sub = e.type === 'movie' ? 'Movie' : `${e.show || e.parentTitle}${e.season ? `, season ${e.season}` : ''}`;
      src = {kind: 'plex', id: e.id, type: e.type, directory: false, name, where: e.show || e.parentTitle};
    }
  }
  const nm = open
    ? h('button', {class: 'nm dir', title: 'Open', onclick: open}, name, h('small', {text: sub}))
    : h('span', {class: 'nm file'}, name, h('small', {text: sub}));
  const key = entryKey(e);
  const isSub = srcKeyOf(B.src.sub) === key, isDub = srcKeyOf(B.src.dub) === key;
  const pick = !src ? [] : B.mode === 'single'
    ? [h('button', {class: 'pill both' + (isSub ? ' on' : ''), text: isSub ? 'Chosen' : 'Choose', 'aria-pressed': String(isSub), onclick: () => choose('sub', isSub ? null : src)})]
    : [h('button', {class: 'pill sub' + (isSub ? ' on' : ''), text: 'Sub', 'aria-pressed': String(isSub), title: 'Use for the Sub channel', onclick: () => choose('sub', isSub ? null : src)}),
       h('button', {class: 'pill dub' + (isDub ? ' on' : ''), text: 'Dub', 'aria-pressed': String(isDub), title: 'Use for the Dub channel', onclick: () => choose('dub', isDub ? null : src)})];
  const cls = 'row' + (B.mode === 'single' ? (isSub ? ' sel-both' : '') : isSub && isDub ? ' sel-both' : isSub ? ' sel-sub' : isDub ? ' sel-dub' : '');
  return h('div', {class: cls}, icon(ico), nm, h('div', {class: 'use'}, ...pick));
}

// ---------- sources: files and Plex items ----------
// A playlist side is either {path} or {plex:{id, media_id, part_id, server_id}}. Plex items need a
// version (media_id/part_id) before they can be probed or loaded.
const refKey = ref => ref.path != null ? 'f:' + ref.path : `p:${ref.plex.id}:${ref.plex.media_id || ''}:${ref.plex.part_id || ''}`;
const refLabel = ref => ref.path != null ? basename(ref.path) : 'Plex';

const plexDetails = new Map();
function details(id) {
  if (!plexDetails.has(id)) {
    const p = api('/api/plex/video?id=' + encodeURIComponent(id));
    p.catch(() => plexDetails.delete(id));
    plexDetails.set(id, p);
  }
  return plexDetails.get(id);
}
const loadableVersions = d => d.versions.filter(v => v.parts.length === 1);
function versionLabel(v) {
  const size = v.parts[0]?.size ? `, ${(v.parts[0].size / 1e9).toFixed(1)} GB` : '';
  return `${v.resolution ? (/^\d+$/.test(v.resolution) ? v.resolution + 'p' : v.resolution) : 'Unknown size'} ${v.video_codec.toUpperCase()}, ${v.audio_codec.toUpperCase()}${size}`;
}
function withVersion(ref, d, v) {
  return {plex: {id: ref.plex.id, media_id: v.id, part_id: v.parts[0].id, server_id: d.server_id}};
}
// Episode label from Plex metadata: "S01E03 Along Came an Elf", or the movie title and year.
function metaLabel(m) {
  if (!m) return '';
  if (m.type === 'episode') return `${m.season || m.episode ? `S${String(m.season).padStart(2, '0')}E${String(m.episode).padStart(2, '0')} ` : ''}${m.title}`;
  return m.year ? `${m.title} (${m.year})` : m.title;
}

const probes = new Map();
// probe resolves to {tracks, ...} for any source; Plex results also carry metadata and transport.
function probe(ref) {
  const key = refKey(ref);
  if (!probes.has(key)) {
    let p;
    if (ref.path != null) p = api('/api/library/probe?path=' + encodeURIComponent(ref.path));
    else if (!ref.plex.media_id) p = details(ref.plex.id).then(d => {
      const v = loadableVersions(d);
      if (!v.length) throw new ApiError('This Plex item only has multi-part versions, which cannot be loaded.', 'INVALID_ARGUMENT', 400);
      return probe(withVersion(ref, d, v[0]));
    });
    else {
      const qs = new URLSearchParams({id: ref.plex.id, media_id: ref.plex.media_id, part_id: ref.plex.part_id});
      if (ref.plex.server_id) qs.set('server_id', ref.plex.server_id);
      p = api('/api/plex/video/probe?' + qs).then(r => ({...r.probe, metadata: r.metadata, transport: r.source?.transport || ''}));
    }
    p.catch(() => probes.delete(key));
    probes.set(key, p);
  }
  return probes.get(key);
}
const probed = new Map(); // refKey -> probe result, null while reading, false on failure
function probeInto(ref) {
  if (!ref) return;
  const key = refKey(ref);
  if (probed.has(key)) return;
  probed.set(key, null);
  probe(ref).then(p => { probed.set(key, p); render(); }, () => { probed.set(key, false); render(); });
}
const probedOf = ref => ref ? probed.get(refKey(ref)) : undefined;

// ---------- playlist builder ----------
const B = {
  mode: 'single', src: {sub: null, dub: null},
  items: [], unmatched: [], ambiguous: [], start: 0,
  previewing: false, previewError: '', token: 0, resolving: null,
  ref: 0, refProbe: {sub: null, dub: null}, refError: '',
  choice: {sub: {audio: null, subtitle: null}, dub: {audio: null, subtitle: null}},
  mapping: null, confirm: false, loading: false, editing: -1, metaSource: null,
};
for (const b of document.querySelectorAll('.mode [data-mode]')) b.onclick = () => setMode(b.dataset.mode);
function setMode(mode) {
  if (B.mode === mode) return;
  B.mode = mode;
  for (const b of document.querySelectorAll('.mode [data-mode]')) b.setAttribute('aria-pressed', String(b.dataset.mode === mode));
  if (mode === 'single') B.src.dub = null;
  preview();
}
function choose(side, src) { B.src[side] = src; preview(); }

function resetPlaylist() {
  B.token++;
  Object.assign(B, {items: [], unmatched: [], ambiguous: [], start: 0, previewing: false, previewError: '', ref: 0, resolving: null,
    refProbe: {sub: null, dub: null}, refError: '', mapping: null, confirm: false, editing: -1, metaSource: null,
    choice: {sub: {audio: null, subtitle: null}, dub: {audio: null, subtitle: null}}});
}
const collectionOf = src => src.kind === 'plex' ? {plex_season_id: src.id} : {folder: src.path};
const itemRefOf = src => src.kind === 'plex' ? {plex: {id: src.id}} : {path: src.path};

async function preview() {
  resetPlaylist(); render();
  const {sub, dub} = B.src;
  if (!sub || (B.mode === 'paired' && !dub)) return;
  const token = B.token;
  B.previewing = true; renderBuilder();
  try {
    let r;
    const pairedSrc = B.mode === 'paired' ? dub : sub;
    if (sub.directory && pairedSrc.directory) {
      r = await api('/api/media/preview', B.mode === 'paired' ? {sub: collectionOf(sub), dub: collectionOf(dub)} : {sub: collectionOf(sub)});
    } else if (!sub.directory && !pairedSrc.directory) {
      r = {items: [{title: '', sub: itemRefOf(sub), dub: itemRefOf(pairedSrc)}], unmatched: [], ambiguous: []};
    } else throw new ApiError('Choose two collections or two single videos. A folder or season cannot be paired with one video.', 'INVALID_ARGUMENT', 400);
    if (token !== B.token) return;
    const ref = s => s.path != null ? {path: s.path} : {plex: {id: s.plex.id, ...(s.plex.server_id ? {server_id: s.plex.server_id} : {})}};
    B.items = r.items.map(it => ({title: it.title || (it.sub.path != null ? basename(it.sub.path) : ''), sub: ref(it.sub), dub: ref(it.dub), manual: null,
      info: {sub: null, dub: null}, versions: {sub: null, dub: null}}));
    B.unmatched = r.unmatched || []; B.ambiguous = r.ambiguous || [];
    if (!B.items.length) B.previewError = B.mode === 'paired' ? 'No episodes in these sources share a season and episode number.' : 'There are no videos here.';
  } catch (e) {
    if (token === B.token) B.previewError = e.message;
  } finally {
    if (token === B.token) { B.previewing = false; render(); }
  }
  if (!B.items.length) return;
  await resolvePlex(token);
  if (token === B.token) loadReference(0);
}

// Read Plex details for every Plex side: labels, artwork and versions. Single-version items are pinned
// straight away; items with several versions default to the first and offer a choice.
async function resolvePlex(token) {
  const todo = [];
  B.items.forEach(it => SIDES.forEach(side => { if (it[side].plex) todo.push([it, side]); }));
  if (!todo.length) return;
  B.resolving = {done: 0, total: todo.length}; renderBuilder();
  let next = 0;
  async function worker() {
    while (next < todo.length) {
      const [it, side] = todo[next++];
      try {
        const d = await details(it[side].plex.id);
        if (token !== B.token) return;
        const v = loadableVersions(d);
        it.info[side] = d.metadata;
        it.versions[side] = {all: d.versions, loadable: v, server_id: d.server_id};
        if (v.length) it[side] = withVersion(it[side], d, v[0]);
        else it.blocked = 'Only multi-part versions exist, which cannot be loaded. Remove this episode.';
      } catch (e) {
        if (token !== B.token) return;
        it.blocked = `Could not read Plex details: ${e.message}`;
      }
      B.resolving.done++;
      renderBuilder();
    }
  }
  await Promise.all([worker(), worker(), worker(), worker()]);
  if (token === B.token) { B.resolving = null; renderBuilder(); }
}

function setVersion(it, side, mediaId) {
  const vs = it.versions[side];
  const v = vs.loadable.find(x => x.id === mediaId);
  const sides = B.mode === 'single' ? SIDES : [side];
  for (const s of sides) it[s] = withVersion(it[s], vs, v);
  delete it.resolved;
  B.confirm = false;
  if (B.items[B.ref] === it) loadReference(B.ref); else renderBuilder();
}

async function loadReference(i) {
  B.ref = i; B.refError = ''; B.refProbe = {sub: null, dub: null};
  B.choice = {sub: {audio: null, subtitle: null}, dub: {audio: null, subtitle: null}};
  invalidateMapping(); renderBuilder();
  const it = B.items[i], token = B.token;
  try {
    const [ps, pd] = await Promise.all([probe(it.sub), probe(it.dub)]);
    if (token !== B.token || B.ref !== i) return;
    B.refProbe = {sub: ps, dub: pd};
  } catch (e) {
    if (token !== B.token || B.ref !== i) return;
    B.refError = `Could not read the tracks of ${itemName(it, i)}. ${e.message}`;
  }
  renderBuilder();
}
function invalidateMapping() {
  B.mapping = null; B.confirm = false;
  for (const it of B.items) delete it.resolved;
}

// Matched metadata on both sides that shares no identity means the user must pick whose labels to use.
function metadataConflict(it) {
  const a = it.info.sub, b = it.info.dub;
  if (B.mode === 'single' || !a?.matched || !b?.matched || a.id === b.id) return false;
  const ids = new Set(a.identifiers);
  return !b.identifiers.some(x => ids.has(x));
}
// The metadata an item will be labelled with: the chosen side when the two disagree, else the first match.
function itemMeta(it) {
  if (B.metaSource && metadataConflict(it)) return it.info[B.metaSource];
  return (it.info.sub?.matched ? it.info.sub : null) || (it.info.dub?.matched ? it.info.dub : null) || it.info.sub || it.info.dub;
}
function itemName(it, i) {
  const m = itemMeta(it);
  return m?.matched ? metaLabel(m) : niceTitle(it.title || refLabel(it.sub)) || `Episode ${i + 1}`;
}

function trackOptions(p, side, kind, value) {
  const tracks = (p?.tracks || []).filter(t => t.kind === kind && !t.attached);
  const rec = tracks.find(t => t.recommended === side);
  const auto = kind === 'audio' ? (rec ? trackLabel(rec) : tracks[0] ? trackLabel(tracks[0]) : 'none found') : (rec ? trackLabel(rec) : 'off');
  const opts = [h('option', {value: 'auto', text: `Automatic: ${auto}`})];
  for (const t of tracks) opts.push(h('option', {value: t.index, text: trackLabel(t)}));
  opts.push(h('option', {value: -1, text: kind === 'audio' ? 'Silence' : 'Off'}));
  const want = value == null ? 'auto' : String(value);
  for (const o of opts) o.selected = o.value === want;
  return opts;
}
const parseChoice = v => v === 'auto' ? null : Number(v);
let selId = 0;
function sideColumn(side, p, value, onchange, title = SIDE[side]) {
  const field = kind => {
    const id = 'sel' + (++selId);
    return h('div', {class: 'sel'}, h('label', {for: id, text: kind === 'audio' ? 'Audio' : 'Subtitles burned in'}),
      h('select', {id, disabled: B.loading, onchange: e => onchange(kind, parseChoice(e.target.value))}, ...trackOptions(p, side, kind, value[kind])));
  };
  return h('div', {class: 'col ' + side}, h('h3', {text: title}), field('audio'), field('subtitle'));
}

function slotText(src) {
  if (src.kind === 'plex') return [src.name, src.type === 'season' ? `Plex season, ${src.where}` : src.type === 'movie' ? 'Plex movie' : `Plex episode, ${src.where}`];
  return [src.name, (src.directory ? 'Folder in ' : 'Video in ') + (dirname(src.path) || 'the library root')];
}

let builderSig = '';
function renderBuilder() {
  const single = B.mode === 'single';
  const slotDefs = single ? [['sub', 'both', 'Source', 'Pick a folder, a Plex season or one video.']] :
    [['sub', 'sub', 'Sub source', 'Pick the subtitled folder, season or video.'], ['dub', 'dub', 'Dub source', 'Pick the dubbed folder, season or video.']];
  const s = S.status;
  const editIt = B.items[B.editing];
  const sig = JSON.stringify([slotDefs, B.src, B.items.map(i => [i.title, i.sub, i.dub, i.manual, i.resolved, i.info, i.blocked]), B.unmatched, B.ambiguous, B.start,
    B.previewing, B.previewError, B.resolving, B.ref, !!B.refProbe.sub, B.refError, B.choice, B.mapping, B.confirm, B.loading, B.editing, B.metaSource,
    s?.media.phase, editIt ? [probedOf(editIt.sub), probedOf(editIt.dub)].map(x => x === null ? 'r' : x === false ? 'f' : x ? 'y' : 'n') : 0]);
  if (sig === builderSig) return;
  builderSig = sig;
  selId = 0;

  $('slots').className = 'slots' + (single ? ' single' : '');
  fill($('slots'), slotDefs.map(([side, cls, label, hint]) => {
    const src = B.src[side];
    const [title, where] = src ? slotText(src) : ['', hint];
    return h('div', {class: `slot ${cls}${src ? ' set' : ''}`},
      h('span', {class: 'k', 'aria-hidden': 'true', text: single ? '双' : MARK[side]}),
      h('div', {class: 'v'}, h('small', {text: label}), src ? h('b', {text: title}) : null, h('div', {class: 'hint', text: where})),
      src ? h('button', {class: 'btn', 'aria-label': `Clear ${label.toLowerCase()}`, title: 'Clear', onclick: () => choose(side, null)}, icon('i-x')) : null);
  }));

  const body = [];
  const n = B.items.length;
  if (!B.src.sub && !B.previewing) {
    body.push(h('div', {class: 'builder-empty'}, h('b', {text: single ? 'Choose a folder, a season or a video' : 'Choose a Sub and a Dub source'}),
      single ? 'Each video needs Japanese and English audio inside the file. You pick the tracks for each channel next.'
             : 'Each side can be a folder, a Plex season or one video. Episodes are paired by season and episode number.'));
  } else if (B.previewing) {
    body.push(h('div', {class: 'reading'}, h('span', {class: 'spinner'}), 'Reading the episodes…'));
  }
  if (B.resolving) body.push(h('div', {class: 'reading'}, h('span', {class: 'spinner'}), `Reading Plex details, ${B.resolving.done} of ${B.resolving.total}`));
  if (B.previewError) body.push(h('div', {class: 'note bad', text: B.previewError}));
  if (B.unmatched.length) body.push(h('div', {class: 'note'}, `${B.unmatched.length} video${B.unmatched.length === 1 ? ' has' : 's have'} no partner and ${B.unmatched.length === 1 ? 'was' : 'were'} left out:`,
    h('ul', null, B.unmatched.slice(0, 12).map(u => h('li', {text: basename(u) || u}))), B.unmatched.length > 12 ? `And ${B.unmatched.length - 12} more.` : null));
  if (B.ambiguous.length) body.push(h('div', {class: 'note'}, 'These share an episode number with another video and were left out:',
    h('ul', null, B.ambiguous.slice(0, 12).map(u => h('li', {text: basename(u) || u})))));
  if (n > 500) body.push(h('div', {class: 'note bad', text: `A playlist holds up to 500 episodes. Remove ${n - 500} to load it.`}));

  if (n) {
    // show artwork and summary from Plex when available
    const meta = B.items.map(itemMeta).find(m => m?.poster_url);
    if (meta) body.push(h('div', {class: 'plex-head'}, h('img', {src: meta.poster_url, alt: '', onerror: e => e.target.remove()}),
      h('div', null, h('b', {text: meta.show || meta.title}), h('span', {text: [meta.type === 'episode' && meta.season ? `Season ${meta.season}` : '', meta.year || ''].filter(Boolean).join(', ')}),
        meta.summary && n === 1 ? h('p', {text: meta.summary}) : null)));

    // track choice
    const pick = [];
    if (n > 1) {
      pick.push(h('div', {class: 'sel ref'}, h('label', {for: 'refSel', text: 'Pick tracks from this episode. Matching tracks are used in every other episode.'}),
        h('select', {id: 'refSel', disabled: B.loading, onchange: e => loadReference(Number(e.target.value))},
          B.items.map((it, i) => h('option', {value: i, text: `${i + 1}. ${itemName(it, i)}`, selected: i === B.ref})))));
    }
    if (B.refError) pick.push(h('div', {class: 'note bad ref', text: B.refError}));
    else if (!B.refProbe.sub) pick.push(h('div', {class: 'reading ref'}, h('span', {class: 'spinner'}), 'Reading tracks. A first read can take up to 45 seconds.'));
    else for (const side of SIDES) pick.push(sideColumn(side, B.refProbe[side], B.choice[side], (kind, v) => { B.choice[side][kind] = v; invalidateMapping(); renderBuilder(); }));
    body.push(h('div', {class: 'pick'}, pick));

    // metadata source, only when both sides are matched to different titles
    const conflicts = B.items.filter(metadataConflict).length;
    if (conflicts) body.push(h('div', {class: 'note meta-choice'},
      h('span', {text: `The Sub and Dub sides match different titles on Plex for ${conflicts} episode${conflicts === 1 ? '' : 's'}. Which side's names and artwork should the streams use?`}),
      h('div', {class: 'acts'}, SIDES.map(side => h('button', {type: 'button', class: 'pill ' + side + (B.metaSource === side ? ' on' : ''), text: SIDE[side],
        'aria-pressed': String(B.metaSource === side), onclick: () => { B.metaSource = side; renderBuilder(); }})))));

    // episode list
    const fileNames = (() => { const t = B.items.map(i => niceTitle(i.title)); return episodeLabels(t, commonPrefix(t)); })();
    body.push(h('div', {class: 'pairs'}, B.items.map((it, i) => pairRow(it, i, itemMeta(it)?.matched ? metaLabel(itemMeta(it)) : fileNames[i]))));

    if (B.mapping) body.push(h('div', {class: 'reading'}, h('span', {class: 'spinner'}), `Matching tracks in every episode, ${B.mapping.done} of ${B.mapping.total}`));
    else if (B.confirm) {
      const bad = B.items.filter(i => !i.manual && i.resolved?.warn.length).length;
      body.push(h('div', {class: 'note', text: `${bad} episode${bad === 1 ? '' : 's'} could not match your tracks and will use automatic choices instead. Check the notes in the list, then load again.`}));
    }
    const blocked = B.items.filter(i => i.blocked).length;
    const needMeta = conflicts && !B.metaSource;
    const replacing = s && s.media.phase !== 'none';
    body.push(h('div', {class: 'loadbar'},
      h('span', {text: blocked ? `${blocked} episode${blocked === 1 ? '' : 's'} cannot be loaded. Remove ${blocked === 1 ? 'it' : 'them'} first.`
        : needMeta ? 'Choose which side supplies the episode names first.'
        : `${replacing ? 'Replaces what is loaded now. ' : ''}Starts paused at episode ${B.start + 1}. Streams stay connected.`}),
      h('button', {class: 'btn go', id: 'loadBtn', disabled: B.loading || B.previewing || !!B.resolving || !!B.mapping || n > 500 || !s || !!blocked || !!needMeta, onclick: loadSelection,
        text: B.loading ? 'Loading' : B.confirm ? 'Load anyway' : `Load ${n} episode${n === 1 ? '' : 's'}`})));
  }
  fill($('builderBody'), body);
}

function versionPicker(it, side) {
  const vs = it.versions[side];
  if (!vs || vs.loadable.length < 2) return null;
  const id = 'ver' + (++selId);
  return h('label', {class: 'ver ' + (B.mode === 'single' ? 'both' : side), for: id},
    B.mode === 'single' ? 'Version' : `${SIDE[side]} version`,
    h('select', {id, disabled: B.loading, onchange: e => setVersion(it, side, e.target.value)},
      vs.loadable.map(v => h('option', {value: v.id, text: versionLabel(v), selected: it[side].plex.media_id === v.id}))));
}
function sideName(it, side) {
  const ref = it[side];
  if (ref.path != null) return basename(ref.path);
  const m = it.info[side];
  if (!m) return 'Plex, reading…';
  // The row title already shows the matched name; repeating it here adds nothing.
  return itemMeta(it) === m && m.matched ? 'From Plex' : `Plex: ${metaLabel(m)}`;
}
function pairRow(it, i, name) {
  const paired = B.mode === 'paired';
  const warn = !it.manual && it.resolved?.warn.length ? it.resolved.warn.join(' ') : '';
  const versions = paired ? SIDES.map(side => versionPicker(it, side)) : [versionPicker(it, 'sub')];
  const names = h('div', {class: 'names'}, name || !paired ? h('span', {class: 't', text: name || niceTitle(it.title) || `Episode ${i + 1}`}) : null,
    paired ? h('span', {class: 's', text: sideName(it, 'sub')}) : null,
    paired ? h('span', {class: 'd', text: sideName(it, 'dub')}) : null,
    versions.some(Boolean) ? h('div', {class: 'vers'}, versions) : null,
    it.manual ? h('span', {class: 's', text: 'Custom tracks for this episode'}) : null,
    it.blocked ? h('span', {class: 'w', text: it.blocked}) : null,
    warn ? h('span', {class: 'w', text: warn}) : null);
  const row = h('div', {class: 'pair-row'}, h('b', {text: String(i + 1).padStart(2, '0')}), names,
    h('div', {class: 'acts'},
      h('button', {class: i === B.start ? 'on' : '', text: i === B.start ? 'Starts here' : 'Start here', disabled: B.loading, onclick: () => { B.start = i; renderBuilder(); }}),
      h('button', {class: B.editing === i ? 'on' : '', text: 'Tracks', title: 'Choose tracks for this episode only', disabled: B.loading || !!it.blocked, onclick: () => toggleEdit(i)}),
      h('button', {class: 'x', 'aria-label': `Remove episode ${i + 1}`, title: 'Remove', disabled: B.loading || B.items.length < 2, onclick: () => removeItem(i)}, icon('i-x'))));
  return h('div', {class: 'pair' + (i === B.start ? ' first' : '')}, row, B.editing === i ? itemEditor(it) : null);
}
function removeItem(i) {
  B.items.splice(i, 1);
  if (i < B.start) B.start--;
  B.start = Math.min(B.start, B.items.length - 1);
  if (B.editing === i) B.editing = -1; else if (B.editing > i) B.editing--;
  if (B.ref === i) loadReference(0); else if (B.ref > i) B.ref--;
  invalidateMapping(); renderBuilder();
}
function toggleEdit(i) {
  B.editing = B.editing === i ? -1 : i;
  if (B.editing === i) { probeInto(B.items[i].sub); probeInto(B.items[i].dub); }
  renderBuilder();
}
function itemEditor(it) {
  const ps = probedOf(it.sub), pd = probedOf(it.dub);
  if (!ps || !pd) {
    const failed = ps === false || pd === false;
    return h('div', {class: 'edit'}, h('div', {class: 'wide'}, failed ? 'Could not read the tracks of this episode.' : [h('span', {class: 'spinner'}), 'Reading tracks…']));
  }
  const cur = it.manual || it.resolved || null;
  const val = side => cur ? {audio: cur[side].audio, subtitle: cur[side].subtitle} : {audio: null, subtitle: null};
  const edit = side => (kind, v) => {
    it.manual = it.manual || {sub: val('sub'), dub: val('dub')};
    it.manual[side][kind] = v;
    renderBuilder();
  };
  return h('div', {class: 'edit'},
    SIDES.map(side => sideColumn(side, side === 'sub' ? ps : pd, val(side), edit(side))),
    h('div', {class: 'wide'}, it.manual
      ? h('button', {class: 'btn', text: 'Use the playlist tracks', onclick: () => { it.manual = null; renderBuilder(); }})
      : 'Changing a track here only affects this episode.'));
}

// Map a track chosen in the reference episode onto another episode's tracks.
function matchTrack(refTracks, value, tracks, kind) {
  if (value == null || value === -1) return {value, warn: ''};
  const r = refTracks.find(t => t.index === value);
  const what = kind === 'audio' ? 'audio' : 'subtitle';
  if (!r) return {value: null, warn: `The chosen ${what} track is missing.`};
  const same = (a, b) => (a || '').toLowerCase() === (b || '').toLowerCase();
  const cands = tracks.filter(t => t.kind === r.kind && !t.attached);
  let m = cands.filter(t => same(t.language, r.language) && same(t.title, r.title));
  if (m.length === 1) return {value: m[0].index, warn: ''};
  if (m.length > 1) { const x = m.find(t => t.index === r.index); if (x) return {value: x.index, warn: ''}; }
  m = cands.filter(t => same(t.language, r.language) && t.codec === r.codec);
  if (m.length === 1) return {value: m[0].index, warn: ''};
  const byIndex = cands.find(t => t.index === r.index && same(t.language, r.language));
  if (byIndex) return {value: byIndex.index, warn: ''};
  return {value: null, warn: `No ${what} track like ${trackLabel(r)}.`};
}
async function mapTracks() {
  const explicit = SIDES.some(side => B.choice[side].audio != null || B.choice[side].subtitle != null);
  const todo = B.items.filter(it => !it.manual && !it.resolved);
  if (!explicit || !todo.length) return true;
  const refTracks = {sub: B.refProbe.sub?.tracks || [], dub: B.refProbe.dub?.tracks || []};
  const token = B.token;
  B.mapping = {done: 0, total: todo.length};
  renderBuilder();
  let next = 0;
  async function worker() {
    while (next < todo.length) {
      const it = todo[next++];
      const resolved = {sub: {}, dub: {}, warn: []};
      for (const side of SIDES) {
        let tracks = [];
        try { tracks = (await probe(it[side])).tracks; }
        catch (e) { resolved.warn.push(`${SIDE[side]}: could not read tracks (${e.message}).`); }
        for (const kind of ['audio', 'subtitle']) {
          const r = tracks.length ? matchTrack(refTracks[side], B.choice[side][kind], tracks, kind) : {value: null, warn: ''};
          resolved[side][kind] = r.value;
          if (r.warn) resolved.warn.push(`${SIDE[side]}: ${r.warn}`);
        }
      }
      if (token !== B.token) return;
      it.resolved = resolved;
      B.mapping.done++;
      renderBuilder();
    }
  }
  await Promise.all([worker(), worker(), worker()]);
  if (token !== B.token) return false;
  B.mapping = null;
  if (B.items.some(it => !it.manual && it.resolved?.warn.length)) { B.confirm = true; renderBuilder(); return false; }
  return true;
}
// "Show/Season 4" reads better as "Show Season 4" than as "Season 4".
function selectionTitle(src, items) {
  if (src?.kind === 'plex') {
    const m = items.map(itemMeta).find(x => x?.matched);
    if (src.type === 'season') return m?.show ? `${m.show}${m.season ? ` Season ${m.season}` : ''}` : src.name;
    return m ? (m.show || m.title) : src.name;
  }
  if (!src?.directory) return '';
  const parent = basename(dirname(src.path));
  return /^(season|series|s)\s*\d+$/i.test(src.name) && parent ? `${parent} ${src.name}` : src.name;
}
async function loadSelection() {
  if (B.loading || !B.items.length) return;
  if (!(await mapTracks())) return;
  const explicit = SIDES.some(side => B.choice[side].audio != null || B.choice[side].subtitle != null);
  const source = (it, side) => {
    const ref = it[side];
    const out = ref.path != null ? {path: ref.path} : {plex: {...ref.plex}};
    const pick = it.manual?.[side] || (explicit ? it.resolved?.[side] : null);
    if (pick?.audio != null) out.audio = pick.audio;
    if (pick?.subtitle != null) out.subtitle = pick.subtitle;
    return out;
  };
  const n = B.items.length;
  const body = {
    mode: B.mode, start_index: B.start,
    title: selectionTitle(B.metaSource === 'dub' && B.items.some(metadataConflict) ? B.src.dub : B.src.sub, B.items) || itemName(B.items[0], 0),
    items: B.items.map(it => {
      const item = {sub: source(it, 'sub'), dub: source(it, 'dub')};
      // Matched Plex items are labelled by the backend from metadata; a title here would override that.
      if (!itemMeta(it)?.matched && it.title) item.title = it.title;
      if (metadataConflict(it)) item.metadata_source = B.metaSource;
      return item;
    }),
  };
  B.loading = true; renderBuilder();
  const s = await post('Load', '/api/media/load', body);
  B.loading = false;
  if (s) {
    B.confirm = false; accept(s);
    toast(`Loading ${n} episode${n === 1 ? '' : 's'}. Press Play when it shows Ready.`);
    location.hash = 'console';
  }
  renderBuilder();
}

// ---------- streams (settings) ----------
const FIELDS = ['sub_server', 'dub_server', 'stream_path', 'sub_username', 'dub_username'];
const FIELD_NAME = {sub_server: 'Sub server', dub_server: 'Dub server', stream_path: 'stream path', sub_username: 'Sub username', dub_username: 'Dub username', music_folder: 'music folder', music_volume: 'music volume', sub_password: 'Sub password', dub_password: 'Dub password'};
let dirty = false;
function fillSettings(st) {
  for (const f of FIELDS) $(f).value = st[f];
  for (const side of SIDES) $(side + '_password').value = '';
  dirty = false;
}
function changes(st) {
  const patch = {};
  for (const f of FIELDS) {
    const v = $(f).value.trim();
    if (v !== st[f]) patch[f] = v;
  }
  for (const side of SIDES) { const p = $(side + '_password').value; if (p) patch[side + '_password'] = p; }
  return patch;
}
function renderSettings(s, running) {
  const st = s.settings;
  if (!dirty) fillSettings(st);
  for (const side of SIDES) $(side + '_password').placeholder = st[side + '_password_set'] ? 'Saved. Leave blank to keep it.' : 'No password';
  for (const el of $('settingsForm').querySelectorAll('input')) el.disabled = running || !!S.pending;
  $('lockNote').textContent = running ? 'Stop streams to edit destinations' : '';
  const ch = Object.keys(changes(st));
  $('dirtyNote').textContent = ch.length ? `Unsaved changes: ${ch.map(k => FIELD_NAME[k]).join(', ')}` : 'All changes saved';
  $('save').disabled = !ch.length || running || !!S.pending;
  $('discard').disabled = !ch.length || !!S.pending;
  $('hud').setAttribute('aria-checked', String(st.hud_enabled));
  $('hud').disabled = !!S.pending;
  $('rescan').disabled = !!S.pending;
  renderMusicSettings(s);
}
$('settingsForm').addEventListener('input', () => { dirty = true; render(); });
$('discard').onclick = () => { if (S.status) { fillSettings(S.status.settings); render(); } };
$('settingsForm').addEventListener('submit', async e => {
  e.preventDefault();
  const st = S.status?.settings;
  if (!st) return;
  const patch = changes(st);
  if (!Object.keys(patch).length) return;
  const s = await post('Save streams', '/api/settings', patch);
  if (s) { dirty = false; accept(s); toast('Streams saved.'); }
});
$('hud').onclick = async () => {
  const on = $('hud').getAttribute('aria-checked') !== 'true';
  $('hud').setAttribute('aria-checked', String(on));
  if (!(await quietPatch({hud_enabled: on}))) render();
};
$('rescan').onclick = async () => {
  const s = await command('rescan', undefined, 'Rescan');
  if (s) {
    probes.clear(); probed.clear(); plexDetails.clear();
    toast('Rescanning the library. Search results update when indexing finishes.');
    browse(true);
    if (B.src.sub) preview();
  }
};

// ---------- music source ----------
// Music settings apply immediately, also while streaming. The backend keeps the current song
// until the new source has audio ready.
const M = {view: null, folderDirty: false, plexDirty: false, volDragging: false,
  playlists: null, listing: false, error: '', preview: null, previewId: '', previewing: false};
function plexTitle(id) { return M.playlists?.find(p => p.id === id)?.title || ''; }

// Volume, shuffle and the progress ring save without the global busy lock. Setting a value twice is
// harmless, and holding the lock would dim every control on the page while the request runs.
async function quietPatch(patch) {
  try {
    const s = await api('/api/settings', patch);
    showError('');
    accept(s);
    return s;
  } catch (e) {
    showError(e.message);
    poll();
    return null;
  }
}

async function musicPatch(label, patch, done) {
  const s = await post(label, '/api/settings', patch);
  if (s) { accept(s); if (done) toast(done); }
  return s;
}

let musicSig = '';
function renderMusicSettings(s) {
  const st = s.settings, src = st.music_source;
  if (!M.view) M.view = src;
  for (const b of document.querySelectorAll('[data-src]')) {
    b.setAttribute('aria-pressed', String(b.dataset.src === M.view));
    const tag = b.querySelector('.inuse');
    if (b.dataset.src === src && !tag) b.firstChild.after(h('span', {class: 'inuse', text: 'In use'}));
    else if (b.dataset.src !== src && tag) tag.remove();
  }
  $('srcFolder').hidden = M.view !== 'folder';
  $('srcPlex').hidden = M.view !== 'plex';
  $('srcOff').hidden = M.view !== 'off';
  $('musicInUse').textContent = src === 'off' ? 'Music off' : 'Using ' + musicSourceLabel(s);

  if (!M.folderDirty && document.activeElement !== $('music_folder')) $('music_folder').value = st.music_folder;
  const folder = $('music_folder').value.trim();
  const sameFolder = src === 'folder' && folder === st.music_folder;
  $('applyFolder').disabled = !!S.pending || !folder || sameFolder;
  $('applyFolder').textContent = sameFolder ? 'Playing this folder' : 'Play this folder';
  $('folderNote').textContent = folder ? (sameFolder ? 'Songs come from this folder.' : 'The current song finishes its fade before the new folder starts.') : 'Choose a folder in the library, then press Use library folder.';
  $('useFolder').disabled = !!S.pending || !L.path;
  $('useFolder').title = L.path ? `Use ${L.path}` : 'Open a folder in the library first';

  if (!M.plexDirty && document.activeElement !== $('plex_server')) $('plex_server').value = st.plex_server;
  $('plex_token').placeholder = st.plex_token_set ? 'Saved. Leave blank to keep it.' : 'Paste your Plex token';
  $('plexConnect').disabled = !!S.pending || M.listing || !$('plex_server').value.trim() || (!st.plex_token_set && !$('plex_token').value);
  $('plexConnect').textContent = M.listing ? 'Loading playlists' : M.playlists ? 'Reload playlists' : 'Show playlists';
  $('applyOff').disabled = !!S.pending || src === 'off';

  $('shuffle').setAttribute('aria-checked', String(st.music_shuffle));
  $('shuffle').disabled = !!S.pending;
  if (!M.volDragging) { $('music_volume').value = st.music_volume; $('volLabel').textContent = st.music_volume; }
  $('musicRefresh').disabled = !!S.pending || src === 'off';

  const sig = JSON.stringify([M.playlists, M.error, M.preview, M.previewId, M.previewing, src, st.plex_playlist_id, S.pending]);
  if (sig === musicSig) return;
  musicSig = sig;
  $('plexError').hidden = !M.error;
  $('plexError').textContent = M.error;
  fill($('playlists'), (M.playlists || []).map(p => {
    const inUse = src === 'plex' && st.plex_playlist_id === p.id;
    const count = p.smart && !p.count ? 'Smart playlist' : `${p.count} song${p.count === 1 ? '' : 's'}${p.smart ? ', smart' : ''}`;
    return h('button', {type: 'button', class: 'pl' + (inUse ? ' inuse' : ''), 'aria-pressed': String(M.previewId === p.id), onclick: () => previewPlaylist(p.id)},
      h('b', {text: p.title}), h('span', {text: inUse ? `In use, ${count.toLowerCase()}` : count}));
  }));
  if (M.playlists && !M.playlists.length) fill($('playlists'), h('div', {class: 'note info', text: 'This Plex server has no audio playlists for this token.'}));
  const box = $('plPreview');
  if (M.previewing) fill(box, h('div', {class: 'reading'}, h('span', {class: 'spinner'}), 'Reading the playlist'));
  else if (M.preview) {
    const p = M.playlists?.find(x => x.id === M.previewId);
    const inUse = src === 'plex' && st.plex_playlist_id === M.previewId;
    const tracks = M.preview.tracks;
    fill(box, h('div', {class: 'pl-preview'},
      h('h3', {text: `${p?.title || 'Playlist'}, ${tracks.length} song${tracks.length === 1 ? '' : 's'}`}),
      h('div', {class: 'tracks-list'}, tracks.slice(0, 50).map(t => h('div', {class: 'song'},
        h('div', {class: 'thumb'}, icon('i-note')),
        h('div', null, h('b', {text: t.title || 'Unknown song'}), h('span', {text: [t.artist || t.album_artist, t.album].filter(Boolean).join(', ')})),
        h('em', {text: t.duration ? clock(t.duration) : ''})))),
      tracks.length > 50 ? h('span', {class: 'hint', text: `And ${tracks.length - 50} more.`}) : null,
      h('div', {class: 'savebar'}, h('span', {text: tracks.length ? 'Songs on this server are used directly. Others are copied from Plex when needed.' : 'This playlist is empty.'}),
        h('button', {type: 'button', class: 'btn go', disabled: !!S.pending || inUse || !tracks.length, text: inUse ? 'Playing this playlist' : 'Play this playlist',
          onclick: () => musicPatch('Play playlist', {music_source: 'plex', plex_playlist_id: M.previewId}, `Switching to ${p?.title || 'the playlist'}. The current song plays until the first new one is ready.`)}))));
  } else fill(box);
}

for (const b of document.querySelectorAll('[data-src]')) b.onclick = () => {
  M.view = b.dataset.src;
  if (M.view === 'plex' && !M.playlists && !M.listing && S.status?.settings.plex_server && S.status.settings.plex_token_set) loadPlaylists();
  render();
};
$('music_folder').addEventListener('input', () => { M.folderDirty = true; render(); });
$('useFolder').onclick = () => { $('music_folder').value = L.path; M.folderDirty = true; render(); };
$('applyFolder').onclick = async () => {
  const folder = $('music_folder').value.trim();
  if (await musicPatch('Play folder', {music_source: 'folder', music_folder: folder}, `Switching to ${basename(folder)}.`)) M.folderDirty = false;
};
$('applyOff').onclick = () => musicPatch('Music off', {music_source: 'off'}, 'Idle music is off.');
$('shuffle').onclick = async () => {
  const on = $('shuffle').getAttribute('aria-checked') !== 'true';
  $('shuffle').setAttribute('aria-checked', String(on));
  if (!(await quietPatch({music_shuffle: on}))) render();
};
// Both volume sliders (Streams and the console) save on release.
for (const [id, label] of [['music_volume', 'volLabel'], ['consoleVol', 'consoleVolLabel']]) {
  $(id).addEventListener('input', e => { M.volDragging = true; $(label).textContent = e.target.value + (id === 'consoleVol' ? '%' : ''); });
  $(id).addEventListener('change', async e => {
    await quietPatch({music_volume: Number(e.target.value)});
    M.volDragging = false; render();
  });
}

$('musicRefresh').onclick = async () => { if (await command('music_refresh', undefined, 'Refresh songs')) toast('Refreshing the song list.'); };
for (const id of ['plex_server', 'plex_token']) $(id).addEventListener('input', () => { M.plexDirty = true; render(); });
$('plexConnect').onclick = async () => {
  const st = S.status?.settings;
  if (!st) return;
  const patch = {};
  const server = $('plex_server').value.trim(), token = $('plex_token').value;
  if (server !== st.plex_server) patch.plex_server = server;
  if (token) patch.plex_token = token;
  if (Object.keys(patch).length && !(await musicPatch('Plex connection', patch))) return;
  $('plex_token').value = ''; M.plexDirty = false;
  loadPlaylists();
};
async function loadPlaylists() {
  M.listing = true; M.error = ''; render();
  try {
    M.playlists = (await api('/api/plex/playlists')).playlists;
    const current = S.status?.settings.plex_playlist_id;
    if (!M.previewId && current && M.playlists.some(p => p.id === current)) previewPlaylist(current);
  } catch (e) { M.error = e.message; M.playlists = null; }
  M.listing = false; render();
}
async function previewPlaylist(id) {
  M.previewId = id; M.previewing = true; M.preview = null; M.error = ''; render();
  try {
    const r = await api('/api/plex/playlist?id=' + encodeURIComponent(id));
    if (M.previewId === id) M.preview = r;
  } catch (e) { if (M.previewId === id) M.error = e.message; }
  if (M.previewId === id) { M.previewing = false; render(); }
}

// ---------- system ----------
let sysSig = '';
function renderSystem(s, running) {
  const d = s.diagnostics;
  $('runDiag').disabled = !!S.pending || d.running || running;
  $('runDiag').textContent = d.running ? 'Qualification running' : 'Run qualification again';
  $('runDiag').title = running ? 'Stop streams first' : '';
  const sig = JSON.stringify([d, s.renderer]);
  if (sig === sysSig) return;
  sysSig = sig;
  const best = d.tests.find(t => t.encoder.kind === d.selected.kind && t.encoder.device === d.selected.device);
  const verdict = h('div', {class: 'box verdict ' + (d.supported ? 'ok' : 'bad')},
    d.running ? h('div', {class: 'big'}, 'Running qualification') :
    h('div', {class: 'big'}, h('em', {text: d.supported ? 'Qualified' : 'Not qualified'}), d.supported && d.selected.kind ? ` on ${best?.name || d.selected.kind.toUpperCase()}` : ''),
    h('p', {text: d.running ? 'Two real encodes are running. This can take several minutes. Nothing is published.'
      : [d.cpu_fallback ? 'CPU fallback is also qualified.' : 'CPU fallback is not qualified.', d.created ? `Tested ${new Date(d.created).toLocaleString()}.` : 'Never tested.'].join(' ')}));
  const NEED = 29.97, top = Math.max(45, ...d.tests.map(t => t.fps_per_output || 0));
  const table = d.tests.length ? h('div', {class: 'box tbl'}, h('table', null,
    h('thead', null, h('tr', null, ...['Encoder', 'Result', 'Frames per second per output', `Needs ${NEED}`].map(t => h('th', {text: t})))),
    h('tbody', null, ...d.tests.map(t => h('tr', null,
      h('td', null, t.name || t.encoder.kind.toUpperCase(), t.error ? h('div', {class: 'err', style: 'color:var(--beni);font-size:var(--s-1)', text: t.error}) : null),
      h('td', {text: !t.available ? 'Not available' : !t.tested ? 'Not tested' : t.passed ? 'Passed' : 'Failed'}),
      h('td', {class: 'n', text: t.tested ? t.fps_per_output.toFixed(1) : '—'}),
      h('td', null, t.tested ? h('div', {class: 'meter', title: `${t.fps_per_output.toFixed(1)} of ${NEED} needed`},
        h('i', {class: t.fps_per_output >= NEED ? '' : 'low', style: `width:${Math.min(100, t.fps_per_output / top * 100)}%`}),
        h('u', {style: `left:${NEED / top * 100}%`})) : null)))))) : h('div', {class: 'box', text: 'No encoders have been tested yet.'});
  const cpu = d.cpu;
  const facts = [
    ['CPU', `${cpu.usable_vcpus} usable ${cpu.virtual ? 'virtual CPUs' : 'cores'}${cpu.virtual ? ` in a ${cpu.virtualization ? cpu.virtualization.toUpperCase() + ' ' : ''}virtual machine` : ''}${cpu.physical_cores ? `, ${cpu.physical_cores} physical cores` : ''}`],
    ['How it was counted', cpu.basis],
    ['Missing tools', d.missing.length ? d.missing.join(', ') : 'None'],
    ['FFmpeg', (d.ffmpeg || 'Unknown').replace(/ Copyright.*$/, '')],
    ['Overlay drawing', s.renderer.samples ? `${s.renderer.p95_ms.toFixed(1)} ms at the 95th percentile, out of 41.7 ms per frame` : 'Not measured yet. Start streams to measure.'],
  ];
  const extra = [];
  if (d.warning) extra.push(h('div', {class: 'note', text: d.warning}));
  $('sysBody').replaceChildren(verdict, table, ...extra,
    h('div', {class: 'box'}, h('dl', {class: 'facts'}, ...facts.flatMap(([k, v]) => [h('dt', {text: k}), h('dd', {text: v})]))));
}
$('runDiag').onclick = async () => {
  const d = await post('Qualification', '/api/diagnostics', {});
  if (d) { toast('Qualification started. It can take several minutes.'); poll(); }
};

// ---------- boot ----------
route();
browse(true);
poll();

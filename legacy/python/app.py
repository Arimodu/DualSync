#!/usr/bin/env python3
import argparse
import collections
import ipaddress
import json
import logging
import math
import os
from pathlib import Path
import queue
import random
import re
import signal
import subprocess
import threading
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from urllib.parse import quote, urlencode, urlsplit, urlunsplit

WIDTH, HEIGHT = 1920, 1080
FPS = 24000 / 1001
TICK = 1 / FPS
FRAME_SIZE = WIDTH * HEIGHT * 3 // 2
SAMPLES = 2002
AUDIO_SIZE = SAMPLES * 2 * 2
SILENCE = bytes(AUDIO_SIZE)
ROOT = Path(__file__).resolve().parent
MUSIC_EXTENSIONS = {'.flac', '.mp3', '.m4a', '.aac', '.ogg', '.opus', '.wav', '.aiff', '.alac'}
DEFAULTS = {
    'sub_server': 'rtmp://127.0.0.1:1935',
    'dub_server': 'rtmp://10.0.0.3:1935',
    'stream_path': 'app/727',
    'sub_username': '', 'sub_password': '',
    'dub_username': 'dualsync', 'dub_password': '',
    'sub_folder': '/mnt/data-vault/Media/Anime/[SubsPlease] Tensei Shitara Slime Datta Ken (1080p)/Season 4',
    'dub_folder': '/mnt/data-vault/TensuraDub',
    'music_folder': '/mnt/data-vault/Arimodu/Music',
    'idle_image': str(ROOT / 'idle.svg'),
    'music_volume': 25,
}


def media_path(value):
    value = str(value).strip()
    if re.match(r'^Z:[\\/]', value, re.I):
        value = '/mnt/data-vault/' + value[3:].replace('\\', '/')
    path = Path(value)
    if not path.is_absolute():
        raise ValueError('Media paths must be absolute Zeus paths or Z:\\ paths.')
    return str(path.resolve())


def validate_settings(settings):
    result = dict(settings)
    for side in ('sub', 'dub'):
        base = urlsplit(str(result[side + '_server']).strip())
        if base.scheme not in ('rtmp', 'rtmps', 'rtsp', 'rtsps') or not base.hostname:
            raise ValueError('Server URLs must start with rtmp://, rtmps://, rtsp:// or rtsps://.')
        if base.path not in ('', '/') or base.query or base.fragment or base.username:
            raise ValueError('Use a server base URL without a stream path or embedded credentials.')
        base.port
        result[side + '_server'] = urlunsplit((base.scheme, base.netloc, '', '', ''))
    path = str(result['stream_path']).strip().strip('/')
    if not re.fullmatch(r'[A-Za-z0-9][A-Za-z0-9_./-]*', path) or any(p in ('', '.', '..') for p in path.split('/')):
        raise ValueError('Stream path must contain letters, numbers, underscores, hyphens and optional / separators.')
    result['stream_path'] = path
    volume = float(result['music_volume'])
    if not math.isfinite(volume) or not 0 <= volume <= 100:
        raise ValueError('Music volume must be between 0 and 100.')
    result['music_volume'] = volume
    for key in ('sub_folder', 'dub_folder', 'music_folder', 'idle_image'):
        result[key] = media_path(result[key])
        p = Path(result[key])
        if not (p.is_file() if key == 'idle_image' else p.is_dir()):
            raise ValueError(f'{key}: path does not exist or has the wrong type.')
    for side in ('sub', 'dub'):
        for suffix in ('_username', '_password'):
            key = side + suffix
            result[key] = str(result[key])
            if any(ord(c) < 32 for c in result[key]):
                raise ValueError('Credentials cannot contain control characters.')
    return result


def publish_url(settings, side):
    base = urlsplit(settings[side + '_server'])
    auth, query = '', 'dualsync=1'
    if settings[side + '_username']:
        if base.scheme.startswith('rtmp'):
            query = urlencode({'dualsync': '1', 'user': settings[side + '_username'], 'pass': settings[side + '_password']})
        else:
            auth = quote(settings[side + '_username'], safe='') + ':' + quote(settings[side + '_password'], safe='') + '@'
    return urlunsplit((base.scheme, auth + base.netloc, '/' + settings['stream_path'], query, ''))


def redact(text):
    text = re.sub(r'(rtsps?://|rtmps?://)[^\s/@]+@', r'\1[credentials]@', str(text))
    return re.sub(r'([?&](?:pass|password|token)=)[^&\s]+', r'\1[redacted]', text, flags=re.I)


def save_json(path, value):
    path.parent.mkdir(parents=True, exist_ok=True)
    temp = path.with_suffix('.tmp')
    with open(temp, 'w', encoding='utf-8') as f:
        json.dump(value, f, indent=2, ensure_ascii=False)
        f.write('\n')
    os.chmod(temp, 0o600)
    os.replace(temp, path)


def ffprobe(path):
    result = subprocess.run(
        ['ffprobe', '-v', 'error', '-show_format', '-show_streams', '-of', 'json', str(path)],
        capture_output=True, text=True, timeout=30, check=True)
    return json.loads(result.stdout)


def scan_pairs(settings):
    sides = {}
    for side in ('sub', 'dub'):
        episodes = {}
        for path in sorted(Path(settings[side + '_folder']).glob('*.mkv')):
            match = re.search(r'S04E(\d+)|S4\s*-\s*(\d+)', path.name, re.I)
            if not match:
                continue
            number = int(next(g for g in match.groups() if g))
            if number in episodes:
                raise ValueError(f'Duplicate {side} episode {number}; move the unwanted version out of the folder.')
            episodes[number] = path
        sides[side] = episodes
    pairs = []
    for number in sorted(sides['sub'].keys() & sides['dub'].keys()):
        pair = {'episode': number}
        for side in ('sub', 'dub'):
            path = sides[side][number]
            metadata = ffprobe(path)
            streams = metadata['streams']
            audio = [s for s in streams if s['codec_type'] == 'audio']
            language = 'jpn' if side == 'sub' else 'eng'
            selected = next((s for s in audio if s.get('tags', {}).get('language') == language), audio[0] if audio else None)
            if selected is None or not any(s['codec_type'] == 'video' for s in streams):
                raise ValueError(f'Episode {number} {side} lacks video or audio.')
            subtitles = [s for s in streams if s['codec_type'] == 'subtitle']
            if side == 'sub':
                subtitle = next((i for i, s in enumerate(subtitles) if s.get('tags', {}).get('language') == 'eng'), None)
            else:
                subtitle = next((i for i, s in enumerate(subtitles) if 'sign' in s.get('tags', {}).get('title', '').lower()), None)
            if subtitle is None:
                raise ValueError(f'Episode {number} {side} lacks the required English subtitle/signs track.')
            pair[side] = {'file': str(path), 'duration': float(metadata['format']['duration']),
                          'audio_index': selected['index'], 'subtitle_index': subtitle}
        pair['duration'] = max(pair['sub']['duration'], pair['dub']['duration'])
        pairs.append(pair)
    return pairs


class RawReader:
    def __init__(self, arguments, size, pad=False):
        self.size, self.pad = size, pad
        self.queue = queue.Queue(maxsize=12)
        self.closed = threading.Event()
        self.done = False
        self.error = ''
        self.lines = collections.deque(maxlen=8)
        arguments = arguments[:-1] + ['-thread_queue_size', '2', arguments[-1]]
        self.process = subprocess.Popen(['ffmpeg', '-hide_banner', '-nostdin', '-loglevel', 'warning'] + arguments,
                                        stdout=subprocess.PIPE, stderr=subprocess.PIPE, bufsize=0)
        threading.Thread(target=self._read, daemon=True).start()
        threading.Thread(target=self._stderr, daemon=True).start()

    def _stderr(self):
        for line in self.process.stderr:
            self.lines.append(redact(line.decode(errors='replace').strip()))

    def _read(self):
        try:
            while not self.closed.is_set():
                chunks, remaining = [], self.size
                while remaining and not self.closed.is_set():
                    data = self.process.stdout.read(remaining)
                    if not data:
                        break
                    chunks.append(data)
                    remaining -= len(data)
                if remaining and (not chunks or not self.pad):
                    break
                data = b''.join(chunks)
                if remaining:
                    data += bytes(remaining)
                while not self.closed.is_set():
                    try:
                        self.queue.put(data, timeout=0.1)
                        break
                    except queue.Full:
                        pass
                if remaining:
                    break
            if not self.closed.is_set():
                code = self.process.wait(timeout=5)
                if code:
                    self.error = '\n'.join(list(self.lines)) or f'FFmpeg decoder exited with code {code}.'
        except Exception as e:
            if not self.closed.is_set():
                self.error = str(e)
        finally:
            self.done = True

    def ready(self):
        return not self.queue.empty() or self.done

    def take(self):
        try:
            return self.queue.get_nowait()
        except queue.Empty:
            return None

    @property
    def eof(self):
        return self.done and self.queue.empty()

    def close(self):
        self.closed.set()
        if self.process.poll() is None:
            self.process.kill()
        try:
            self.process.wait(timeout=.1)
        except subprocess.TimeoutExpired:
            threading.Thread(target=self.process.wait, daemon=True).start()


def filter_filename(path):
    option = str(path).replace('\\', '\\\\').replace("'", "\\'").replace(':', '\\:')
    return ''.join('\\' + c if c in "\\'[],;" else c for c in option)


def video_reader(source, position):
    path = source['file']
    escaped = filter_filename(path)
    vf = (f"subtitles=filename={escaped}:si={source['subtitle_index']},"
          f'setpts=PTS-STARTPTS,fps=24000/1001,scale={WIDTH}:{HEIGHT}:force_original_aspect_ratio=decrease,'
          f'pad={WIDTH}:{HEIGHT}:(ow-iw)/2:(oh-ih)/2,format=yuv420p')
    return RawReader(['-threads', '2', '-filter_threads', '2', '-ss', str(position), '-copyts', '-i', path,
                      '-map', '0:v:0', '-an', '-sn', '-vf', vf, '-f', 'rawvideo', 'pipe:1'], FRAME_SIZE)


def audio_reader(path, position=0, index=None, volume=1):
    return RawReader(['-threads', '1', '-ss', str(position), '-i', str(path), '-map', f'0:{index}' if index is not None else '0:a:0',
                      '-vn', '-sn', '-af', f'aresample=48000,volume={volume}', '-ar', '48000', '-ac', '2',
                      '-c:a', 'pcm_s16le', '-f', 's16le', 'pipe:1'], AUDIO_SIZE, pad=True)


class Side:
    def __init__(self, source, position):
        self.video = video_reader(source, position)
        try:
            self.audio = audio_reader(source['file'], position, source['audio_index'])
        except Exception:
            self.video.close()
            raise
        self.last = None

    def ready(self):
        return self.video.ready() and self.audio.ready()

    @property
    def eof(self):
        return self.video.eof and self.audio.eof

    @property
    def error(self):
        return self.video.error or self.audio.error

    def take(self, idle):
        if self.eof:
            return idle, SILENCE
        frame = self.video.take()
        if frame is not None:
            self.last = frame
        return frame or self.last or idle, self.audio.take() or SILENCE

    def close(self):
        self.video.close()
        self.audio.close()


class IdleVisual:
    def __init__(self, path):
        self.reader = None
        vf = (f'scale={WIDTH}:{HEIGHT}:force_original_aspect_ratio=decrease,'
              f'pad={WIDTH}:{HEIGHT}:(ow-iw)/2:(oh-ih)/2,format=yuv420p')
        result = subprocess.run(['ffmpeg', '-hide_banner', '-loglevel', 'error', '-i', path, '-vf', vf,
                                 '-frames:v', '1', '-f', 'rawvideo', 'pipe:1'], capture_output=True, timeout=15)
        if result.returncode or len(result.stdout) != FRAME_SIZE:
            raise ValueError('Idle image cannot be decoded: ' + result.stderr.decode(errors='replace')[-500:])
        self.last = result.stdout
        if Path(path).suffix.lower() in ('.gif', '.webp', '.mp4', '.webm'):
            self.reader = RawReader(['-threads', '1', '-filter_threads', '1', '-stream_loop', '-1', '-i', path,
                                     '-an', '-vf', 'fps=24000/1001,' + vf, '-f', 'rawvideo', 'pipe:1'], FRAME_SIZE)

    def take(self):
        if self.reader:
            self.last = self.reader.take() or self.last
        return self.last

    def close(self):
        if self.reader:
            self.reader.close()


class Publisher:
    def __init__(self, settings, side):
        self.side = side
        self.queues = [queue.Queue(maxsize=8), queue.Queue(maxsize=8)]
        self.closed = threading.Event()
        self.lines = collections.deque(maxlen=6)
        self.started = self.progress_at = time.monotonic()
        self.written = [self.started, self.started]
        self.ready = False
        self.out_time = 0
        self.retry_at = 0
        self.restart_count = 0
        self.error = ''
        pipes = [os.pipe(), os.pipe()]
        url = publish_url(settings, side)
        args = ['ffmpeg', '-hide_banner', '-nostdin', '-loglevel', 'info', '-nostats', '-stats_period', '0.5',
                '-progress', 'pipe:1', '-thread_queue_size', '8', '-probesize', '32', '-analyzeduration', '0',
                '-f', 'rawvideo', '-pixel_format', 'yuv420p', '-video_size', f'{WIDTH}x{HEIGHT}',
                '-framerate', '24000/1001', '-i', f'pipe:{pipes[0][0]}',
                '-thread_queue_size', '8', '-probesize', '32', '-analyzeduration', '0',
                '-f', 's16le', '-ar', '48000', '-ac', '2', '-i', f'pipe:{pipes[1][0]}',
                '-map', '0:v:0', '-map', '1:a:0', '-c:v', 'h264_nvenc', '-preset', 'p4', '-tune', 'ull',
                '-rc', 'vbr', '-b:v', '8M', '-maxrate', '12M', '-bufsize', '16M', '-g', '48', '-bf', '0',
                '-c:a', 'aac', '-b:a', '192k', '-ar', '48000', '-ac', '2', '-rw_timeout', '10000000']
        if urlsplit(url).scheme.startswith('rtsp'):
            args += ['-rtsp_transport', 'tcp', '-muxdelay', '0.1', '-f', 'rtsp', url]
        else:
            args += ['-flvflags', 'no_duration_filesize', '-f', 'flv', url]
        try:
            self.process = subprocess.Popen(args, pass_fds=tuple(p[0] for p in pipes),
                                            stdout=subprocess.PIPE, stderr=subprocess.PIPE, bufsize=0)
        except Exception:
            for pair in pipes:
                for fd in pair:
                    os.close(fd)
            raise
        for i, (read_fd, write_fd) in enumerate(pipes):
            os.close(read_fd)
            threading.Thread(target=self._write, args=(i, write_fd), daemon=True).start()
        threading.Thread(target=self._progress, daemon=True).start()
        threading.Thread(target=self._stderr, daemon=True).start()

    def _write(self, i, fd):
        try:
            while not self.closed.is_set():
                try:
                    data = memoryview(self.queues[i].get(timeout=0.1))
                except queue.Empty:
                    continue
                while data and not self.closed.is_set():
                    data = data[os.write(fd, data):]
                    self.written[i] = time.monotonic()
        except OSError:
            pass
        finally:
            os.close(fd)

    def _progress(self):
        for line in self.process.stdout:
            value = line.decode(errors='replace').strip()
            if value.startswith('out_time_us='):
                try:
                    self.out_time = int(value.split('=', 1)[1]) / 1_000_000
                    self.ready = self.out_time > 0
                    self.progress_at = time.monotonic()
                except ValueError:
                    pass

    def _stderr(self):
        for line in self.process.stderr:
            self.lines.append(redact(line.decode(errors='replace').strip()))

    def can_accept(self):
        return self.process.poll() is None and not any(q.full() for q in self.queues)

    def enqueue(self, frame, audio):
        if self.can_accept():
            self.queues[0].put_nowait(frame)
            self.queues[1].put_nowait(audio)
            return True
        return False

    def failed(self, now):
        if self.process.poll() is not None:
            self.error = '\n'.join(list(self.lines)) or 'Publisher disconnected.'
            return True
        if now - self.progress_at > 20 or (self.ready and now - min(self.written) > 5):
            self.error = 'Publisher stopped accepting media; reconnecting.'
            return True
        return False

    def close(self):
        self.closed.set()
        if self.process.poll() is None:
            self.process.kill()
        self.process.wait(timeout=5)


class Engine:
    def __init__(self, state_dir):
        self.state_dir = Path(state_dir)
        settings_path = self.state_dir / 'settings.json'
        self.settings = validate_settings(DEFAULTS | (json.loads(settings_path.read_text()) if settings_path.exists() else {}))
        self.lock = threading.RLock()
        self.pairs = scan_pairs(self.settings)
        state_path = self.state_dir / 'position.json'
        saved = json.loads(state_path.read_text()) if state_path.exists() else {}
        self.index = next((i for i, p in enumerate(self.pairs) if p['episode'] == saved.get('episode')), 0)
        self.position = max(0, min(float(saved.get('position', 0)), self.pair['duration'])) if self.pairs else 0
        self.publishers = {}
        self.sides = {}
        self.visual = None
        self.music = None
        self.music_track = ''
        self.music_bag = []
        self.music_failures = 0
        self.music_disabled = False
        self.music_context = ''
        self.mode = 'stopped'
        self.paused_at = None
        self.error = ''
        self.buffering = False
        self.closed = threading.Event()
        self.last_save = time.monotonic()
        save_json(settings_path, self.settings)
        threading.Thread(target=self._run, daemon=True).start()

    @property
    def pair(self):
        return self.pairs[self.index] if self.pairs else None

    def _save_position(self):
        save_json(self.state_dir / 'position.json', {'episode': self.pair['episode'] if self.pair else None,
                                                    'position': self.position})
        self.last_save = time.monotonic()

    def _close_sides(self):
        for side in self.sides.values():
            side.close()
        self.sides.clear()

    def _load_sides(self):
        self._close_sides()
        if not self.pair:
            raise ValueError('No paired episodes found. Add files, then rescan.')
        try:
            for name in ('sub', 'dub'):
                source = self.pair[name]
                if self.position < source['duration']:
                    self.sides[name] = Side(source, self.position)
        except Exception:
            self._close_sides()
            raise

    def _close_music(self):
        if self.music:
            self.music.close()
        self.music = None
        self.music_track = ''

    def _music_audio(self):
        if self.music_disabled:
            return SILENCE
        if self.music and self.music.eof:
            if self.music.error:
                self.music_failures += 1
                logging.warning('Music skipped: %s', self.music.error)
            else:
                self.music_failures = 0
            self._close_music()
        if not self.music:
            if self.music_failures >= 5:
                self.music_disabled = True
                self.error = 'Five music files failed consecutively; idle music disabled for this session.'
                return SILENCE
            if not self.music_bag:
                folder = Path(self.settings['music_folder'])
                self.music_bag = [p for p in folder.rglob('*') if p.is_file() and p.suffix.lower() in MUSIC_EXTENSIONS
                                  and not any(part.startswith('.') or part.upper() == '$RECYCLE.BIN' for part in p.relative_to(folder).parts)]
                random.shuffle(self.music_bag)
            if not self.music_bag:
                return SILENCE
            path = self.music_bag.pop()
            self.music = audio_reader(path, volume=self.settings['music_volume'] / 100)
            self.music_track = str(path.relative_to(self.settings['music_folder']))
        return self.music.take() or SILENCE

    def command(self, action, value=None):
        with self.lock:
            if action == 'start':
                if self.mode != 'stopped':
                    return
                self.error = ''
                self.visual = IdleVisual(self.settings['idle_image'])
                try:
                    for side in ('sub', 'dub'):
                        self.publishers[side] = Publisher(self.settings, side)
                except Exception:
                    for p in self.publishers.values():
                        p.close()
                    self.publishers.clear()
                    self.visual.close()
                    self.visual = None
                    raise
                self.mode = 'idle'
                self.music_context = 'startup'
                self.music_disabled = False
                self.music_failures = 0
            elif action == 'stop':
                self._close_sides()
                self._close_music()
                for p in self.publishers.values():
                    p.close()
                self.publishers.clear()
                if self.visual:
                    self.visual.close()
                self.visual = None
                self.mode = 'stopped'
                self.paused_at = None
                self._save_position()
            elif action == 'play':
                if self.mode == 'stopped':
                    raise ValueError('Start streams first.')
                if self.mode == 'playing':
                    return
                self.error = ''
                if not self.sides:
                    if self.pair and self.position >= self.pair['duration']:
                        self.position = 0
                    self._load_sides()
                self._close_music()
                self.music_context = ''
                self.mode = 'playing'
                self.paused_at = None
            elif action == 'pause':
                if self.mode != 'playing':
                    return
                self.mode = 'paused'
                self.paused_at = time.monotonic()
                self._save_position()
            elif action == 'seek':
                offset = float(value)
                if offset not in (-10, 10, 80):
                    raise ValueError('Seek offset must be -10, 10 or 80 seconds.')
                if not self.pair:
                    raise ValueError('No paired episodes found.')
                self.position = max(0, min(self.position + offset, self.pair['duration'] - TICK))
                if self.mode in ('playing', 'paused'):
                    self._load_sides()
                self._save_position()
            elif action == 'episode':
                new_index = next((i for i, p in enumerate(self.pairs) if p['episode'] == int(value)), None)
                if new_index is None:
                    raise ValueError('Episode is not paired or is unavailable.')
                self.index, self.position = new_index, 0
                self._close_sides()
                if self.mode in ('playing', 'paused'):
                    self._load_sides()
                self._save_position()
            elif action == 'rescan':
                if self.mode != 'stopped':
                    raise ValueError('Stop streams before rescanning files.')
                episode = self.pair['episode'] if self.pair else None
                pairs = scan_pairs(self.settings)
                self.index = next((i for i, p in enumerate(pairs) if p['episode'] == episode), 0)
                self.pairs = pairs
                self.position = min(self.position, self.pair['duration']) if self.pair else 0
                self._save_position()
            elif action == 'music_next':
                self._close_music()
                self.music_disabled = False
                self.music_failures = 0
            else:
                raise ValueError('Unknown command.')

    def update_settings(self, changes):
        with self.lock:
            if self.mode != 'stopped':
                raise ValueError('Stop streams before applying settings.')
            if not set(changes).issubset(DEFAULTS):
                raise ValueError('Unknown settings field.')
            settings = validate_settings(self.settings | changes)
            visual = IdleVisual(settings['idle_image'])
            visual.close()
            pairs = scan_pairs(settings)
            old_episode = self.pair['episode'] if self.pair else None
            self.index = next((i for i, p in enumerate(pairs) if p['episode'] == old_episode), 0)
            self.settings, self.pairs = settings, pairs
            self.position = min(self.position, self.pair['duration']) if self.pair else 0
            self.music_bag.clear()
            save_json(self.state_dir / 'settings.json', self.settings)
            self._save_position()

    def status(self):
        with self.lock:
            settings = {k: v for k, v in self.settings.items() if not k.endswith('_password')}
            settings.update({s + '_password_set': bool(self.settings[s + '_password']) for s in ('sub', 'dub')})
            return {'mode': self.mode, 'buffering': self.buffering, 'episode': self.pair['episode'] if self.pair else None,
                    'position': round(self.position, 3), 'duration': self.pair['duration'] if self.pair else 0,
                    'episodes': [{'episode': p['episode'], 'duration': p['duration']} for p in self.pairs],
                    'settings': settings, 'music_track': self.music_track, 'error': self.error,
                    'pause_seconds': round(time.monotonic() - self.paused_at, 1) if self.paused_at else 0,
                    'outputs': {s: {'ready': p.ready and p.process.poll() is None, 'pid': p.process.pid,
                                    'out_time': p.out_time, 'restarts': p.restart_count, 'error': p.error,
                                    'queued_frames': p.queues[0].qsize()} for s, p in self.publishers.items()}}

    def _tick(self, now):
        if not self.publishers:
            self.buffering = False
            return
        for side, publisher in list(self.publishers.items()):
            if publisher.failed(now) and now >= publisher.retry_at:
                error, count = publisher.error, publisher.restart_count + 1
                publisher.close()
                replacement = Publisher(self.settings, side)
                replacement.error, replacement.restart_count = error, count
                replacement.retry_at = now + 5
                self.publishers[side] = replacement
                logging.warning('%s publisher reconnect: %s', side, error)
        idle = self.visual.take()
        outputs_ready = all(p.ready and p.can_accept() for p in self.publishers.values())
        content_ready = all(s.ready() for s in self.sides.values())
        self.buffering = self.mode == 'playing' and not (outputs_ready and content_ready)
        if self.mode == 'playing' and any(s.error for s in self.sides.values()):
            self.error = next(s.error for s in self.sides.values() if s.error)
            self._close_sides()
            self.mode = 'paused'
            self.paused_at = now
        if self.mode == 'playing' and outputs_ready and content_ready:
            if all(s.eof for s in self.sides.values()):
                self._close_sides()
                if self.index + 1 < len(self.pairs):
                    self.index += 1
                    self.position = 0
                    self._load_sides()
                else:
                    self.mode = 'ended'
                    self.position = self.pair['duration']
                self._save_position()
                frames = {s: (idle, SILENCE) for s in self.publishers}
            else:
                frames = {s: self.sides[s].take(idle) if s in self.sides else (idle, SILENCE) for s in self.publishers}
                self.position = min(self.position + TICK, self.pair['duration'])
        else:
            music_due = self.mode == 'idle' and self.music_context == 'startup'
            music_due |= self.mode == 'paused' and self.paused_at is not None and now - self.paused_at >= 60
            audio = self._music_audio() if music_due else SILENCE
            frames = {s: (idle, audio) for s in self.publishers}
        for s, publisher in self.publishers.items():
            publisher.enqueue(*frames[s])
        if now - self.last_save >= 5:
            self._save_position()

    def _run(self):
        deadline = time.monotonic()
        while not self.closed.is_set():
            try:
                with self.lock:
                    self._tick(time.monotonic())
            except Exception as e:
                logging.exception('Playback engine error')
                with self.lock:
                    self.error = redact(e)
                    if self.mode == 'playing':
                        self.mode = 'paused'
                        self.paused_at = time.monotonic()
            deadline += TICK
            now = time.monotonic()
            if deadline < now - TICK:
                deadline = now
            self.closed.wait(max(0, deadline - now))

    def close(self):
        self.closed.set()
        self.command('stop')


class Handler(BaseHTTPRequestHandler):
    engine = None
    server_version = 'DualSync/1'

    def log_message(self, *args):
        pass

    def send_data(self, code, data, kind='application/json'):
        self.send_response(code)
        self.send_header('Content-Type', kind)
        self.send_header('Content-Length', str(len(data)))
        self.send_header('Cache-Control', 'no-store')
        self.send_header('X-Content-Type-Options', 'nosniff')
        self.send_header('Content-Security-Policy', "default-src 'self'; script-src 'self' 'unsafe-inline'; style-src 'self' 'unsafe-inline'; frame-ancestors 'self'")
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if self.path == '/':
            self.send_data(200, (ROOT / 'index.html').read_bytes(), 'text/html; charset=utf-8')
        elif self.path == '/api/status':
            self.send_data(200, json.dumps(self.engine.status()).encode())
        else:
            self.send_data(404, b'{"error":"Not found"}')

    def do_POST(self):
        try:
            address = ipaddress.ip_address(self.client_address[0])
            if not (address.is_private or address.is_loopback):
                raise ValueError('Control access requires a private network address.')
            origin = self.headers.get('Origin')
            if origin and urlsplit(origin).netloc != self.headers.get('Host'):
                raise ValueError('Cross-origin control requests are not allowed.')
            if self.headers.get('Content-Type', '').split(';')[0] != 'application/json':
                raise ValueError('Use application/json requests.')
            length = int(self.headers.get('Content-Length', 0))
            if not 0 < length <= 16384:
                raise ValueError('Invalid request size.')
            body = json.loads(self.rfile.read(length))
            if self.path == '/api/control':
                self.engine.command(body['action'], body.get('value'))
            elif self.path == '/api/settings':
                self.engine.update_settings(body)
            else:
                self.send_data(404, b'{"error":"Not found"}')
                return
            self.send_data(200, json.dumps(self.engine.status()).encode())
        except (ValueError, KeyError, TypeError, subprocess.SubprocessError) as e:
            self.send_data(400, json.dumps({'error': redact(e)}).encode())
        except Exception as e:
            logging.exception('Control request failed')
            self.send_data(500, json.dumps({'error': redact(e)}).encode())


def main():
    parser = argparse.ArgumentParser()
    parser.add_argument('--state-dir', default='/var/lib/dualsync')
    parser.add_argument('--bind', default='172.16.0.52')
    parser.add_argument('--port', default=8090, type=int)
    args = parser.parse_args()
    logging.basicConfig(level=logging.INFO, format='%(asctime)s %(levelname)s %(message)s')
    engine = Engine(args.state_dir)
    Handler.engine = engine
    server = ThreadingHTTPServer((args.bind, args.port), Handler)
    server.daemon_threads = True

    def shutdown(*_):
        engine.close()
        threading.Thread(target=server.shutdown, daemon=True).start()

    signal.signal(signal.SIGTERM, shutdown)
    signal.signal(signal.SIGINT, shutdown)
    logging.info('Panel listening at http://%s:%d; streams stopped.', args.bind, args.port)
    try:
        server.serve_forever()
    finally:
        engine.close()
        server.server_close()


if __name__ == '__main__':
    main()

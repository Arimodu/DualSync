import json
from pathlib import Path
import tempfile
import unittest
from unittest.mock import Mock
from urllib.parse import parse_qs, urlsplit

import app


class FakePublisher:
    def __init__(self, ready=True):
        self.ready = ready
        self.frames = []

    def failed(self, now):
        return False

    def can_accept(self):
        return True

    def enqueue(self, frame, audio):
        self.frames.append((frame, audio))


class FakeSide:
    def __init__(self, ticks):
        self.remaining = ticks
        self.error = ''

    def ready(self):
        return True

    @property
    def eof(self):
        return self.remaining == 0

    def take(self, idle):
        if self.eof:
            return idle, app.SILENCE
        self.remaining -= 1
        return b'video', b'episode-audio'


def engine():
    e = app.Engine.__new__(app.Engine)
    e.publishers = {s: FakePublisher() for s in ('sub', 'dub')}
    e.sides = {'sub': FakeSide(3), 'dub': FakeSide(7)}
    e.visual = Mock()
    e.visual.take.return_value = b'idle'
    e.pairs = [{'episode': 1, 'duration': 10}, {'episode': 2, 'duration': 10}]
    e.index = 0
    e.position = 0
    e.mode = 'playing'
    e.paused_at = None
    e.music_context = ''
    e.last_save = 1000
    e.error = ''
    e._save_position = Mock()
    e._close_sides = Mock(side_effect=lambda: e.sides.clear())
    e._load_sides = Mock(side_effect=lambda: e.sides.update({s: FakeSide(2) for s in ('sub', 'dub')}))
    e._music_audio = Mock(return_value=b'music')
    return e


class TimelineTests(unittest.TestCase):
    def test_short_side_waits_and_next_episode_starts_after_both(self):
        e = engine()
        for i in range(7):
            e._tick(10 + i * app.TICK)
            self.assertEqual(e.index, 0)
        self.assertEqual(e.publishers['sub'].frames[3:], [(b'idle', app.SILENCE)] * 4)
        self.assertTrue(all(frame == b'video' for frame, audio in e.publishers['dub'].frames))
        e._tick(11)
        self.assertEqual(e.index, 1)
        self.assertEqual(e.position, 0)

    def test_publisher_not_ready_holds_both_sources(self):
        e = engine()
        e.publishers['dub'].ready = False
        e._tick(10)
        self.assertEqual(e.position, 0)
        self.assertEqual(e.sides['sub'].remaining, 3)
        self.assertEqual(e.sides['dub'].remaining, 7)
        self.assertTrue(e.buffering)

    def test_decoder_buffering_holds_both_sources(self):
        e = engine()
        e.sides['dub'].ready = lambda: False
        e._tick(10)
        self.assertEqual(e.position, 0)
        self.assertEqual(e.sides['sub'].remaining, 3)

    def test_pause_music_threshold_and_held_position(self):
        e = engine()
        e.mode = 'paused'
        e.paused_at = 1
        e._tick(60.99)
        e._music_audio.assert_not_called()
        e._tick(61)
        e._music_audio.assert_called_once()
        self.assertEqual(e.position, 0)
        self.assertEqual(e.publishers['sub'].frames[-1], (b'idle', b'music'))
        self.assertEqual(e.publishers['dub'].frames[-1], (b'idle', b'music'))

    def test_startup_music_and_end_silence(self):
        e = engine()
        e.mode, e.music_context = 'idle', 'startup'
        e._tick(10)
        self.assertEqual(e.publishers['sub'].frames[-1][1], b'music')
        e.mode = 'ended'
        e._tick(11)
        self.assertEqual(e.publishers['sub'].frames[-1][1], app.SILENCE)

    def test_last_pair_ends_without_closing_publishers(self):
        e = engine()
        e.index = 1
        e.sides = {s: FakeSide(0) for s in ('sub', 'dub')}
        old = dict(e.publishers)
        e._tick(10)
        self.assertEqual(e.mode, 'ended')
        self.assertEqual(e.publishers, old)

    def test_decoder_error_pauses_without_closing_publishers(self):
        e = engine()
        e.sides['dub'].error = 'Decode failed'
        old = dict(e.publishers)
        e._tick(10)
        self.assertEqual(e.mode, 'paused')
        self.assertEqual(e.position, 0)
        self.assertEqual(e.publishers, old)
        self.assertEqual(e.error, 'Decode failed')

    def test_audio_frame_counts_use_same_exact_duration(self):
        self.assertAlmostEqual(app.SAMPLES / 48000, app.TICK, places=12)


class ConfigurationTests(unittest.TestCase):
    def test_windows_redirected_share_path(self):
        self.assertEqual(app.media_path('Z:\\Arimodu\\Music'), '/mnt/data-vault/Arimodu/Music')

    def test_rtmp_auth_query_marker_and_escaping(self):
        settings = app.DEFAULTS | {'dub_password': 'a+b/=&?'}
        url = urlsplit(app.publish_url(settings, 'dub'))
        self.assertIsNone(url.username)
        self.assertEqual(parse_qs(url.query), {'dualsync': ['1'], 'user': ['dualsync'], 'pass': ['a+b/=&?']})

    def test_rtsp_auth_and_marker(self):
        settings = app.DEFAULTS | {'dub_server': 'rtsp://10.0.0.3:554', 'dub_password': 'a+b/=&?'}
        url = app.publish_url(settings, 'dub')
        self.assertIn('dualsync:a%2Bb%2F%3D%26%3F@', url)
        self.assertEqual(parse_qs(urlsplit(url).query), {'dualsync': ['1']})

    def test_credentials_redacted_from_errors(self):
        self.assertNotIn('secret', app.redact('rtsp://user:secret@host/path'))
        self.assertNotIn('secret', app.redact('rtmp://host/path?user=x&pass=secret'))

    def test_invalid_path_and_nonfinite_volume_rejected(self):
        for change in [{'stream_path': 'app/../other'}, {'stream_path': 'app//727'},
                       {'stream_path': 'app/727?pass=test'}, {'music_volume': float('nan')},
                       {'sub_server': 'rtmp://host/app/727'}]:
            with self.subTest(change=change), self.assertRaises(ValueError):
                app.validate_settings(app.DEFAULTS | change)

    def test_saved_credentials_file_private(self):
        with tempfile.TemporaryDirectory() as directory:
            path = Path(directory) / 'settings.json'
            app.save_json(path, {'password': 'test'})
            self.assertEqual(path.stat().st_mode & 0o777, 0o600)
            self.assertEqual(json.loads(path.read_text()), {'password': 'test'})
            self.assertFalse(path.with_suffix('.tmp').exists())


if __name__ == '__main__':
    unittest.main()

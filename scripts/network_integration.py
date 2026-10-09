import json
import select
import socket
import subprocess
import threading
import time
import urllib.request
from pathlib import Path

base = 'http://127.0.0.1:18090/api/'
out = Path('/var/lib/dualsync-dev/verification')
enabled = threading.Event()
enabled.set()
connections = []
lock = threading.Lock()
listener = socket.socket()
listener.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
listener.bind(('127.0.0.1', 19450))
listener.listen()

def forward(client):
    remote = socket.create_connection(('10.0.0.3', 554), timeout=5)
    remote.settimeout(None)
    pair = [client, remote]
    with lock:
        connections.extend(pair)
    try:
        while enabled.is_set():
            ready, _, _ = select.select(pair, [], [], 1)
            for source in ready:
                data = source.recv(65536)
                if not data:
                    return
                pair[1 if source is client else 0].sendall(data)
    except OSError:
        pass
    finally:
        with lock:
            for peer in pair:
                if peer in connections:
                    connections.remove(peer)
                peer.close()

def accept():
    while True:
        try:
            client, _ = listener.accept()
        except OSError:
            return
        if enabled.is_set():
            threading.Thread(target=forward, args=(client,), daemon=True).start()
        else:
            client.close()

threading.Thread(target=accept, daemon=True).start()

def api(path, data=None):
    request = urllib.request.Request(base + path,
        data=json.dumps(data).encode() if data is not None else None,
        headers={'Content-Type': 'application/json'})
    return json.load(urllib.request.urlopen(request, timeout=40))

def wait(predicate, timeout=120):
    deadline = time.monotonic() + timeout
    while time.monotonic() < deadline:
        status = api('status')
        if predicate(status):
            return status
        time.sleep(.5)
    raise RuntimeError('integration timeout: ' + json.dumps(status['outputs']))

status = api('status')
restore = {key: status['settings'][key] for key in ['sub_server', 'dub_server', 'hud_enabled']}
try:
    api('control', {'action': 'stop'})
    api('settings', {'sub_server': 'rtsp://127.0.0.1:554',
        'dub_server': 'rtsp://127.0.0.1:19450', 'hud_enabled': True})
    api('control', {'action': 'start'})
    wait(lambda s: len(s['outputs']) == 2 and all(v['ready'] for v in s['outputs'].values()))
    item = json.loads((out / 'selected-item.json').read_text())
    api('media/load', {'mode': 'paired', 'items': [item]})
    wait(lambda s: s['media']['phase'] == 'ready')
    api('control', {'action': 'play'})
    before = wait(lambda s: s['media']['phase'] == 'playing')
    healthy_pid = before['outputs']['sub']['pid']
    failed_pid = before['outputs']['dub']['pid']
    time.sleep(3)
    enabled.clear()
    with lock:
        for peer in connections:
            try:
                peer.shutdown(socket.SHUT_RDWR)
            except OSError:
                pass
    outage = wait(lambda s: s['media']['phase'] == 'buffering', 30)
    held = outage['position']
    time.sleep(5)
    assert api('status')['position'] == held, 'source clock advanced during outage'
    assert api('status')['outputs']['sub']['pid'] == healthy_pid, 'healthy publisher replaced'
    enabled.set()
    recovered = wait(lambda s: s['media']['phase'] == 'playing' and s['outputs']['dub']['ready'], 40)
    assert recovered['outputs']['sub']['pid'] == healthy_pid
    assert recovered['outputs']['dub']['pid'] != failed_pid
    assert recovered['outputs']['dub']['restarts'] > 0
    api('control', {'action': 'pause'})
    time.sleep(6)
    paused = api('status')
    assert paused['music']['gain'] > .24, 'pause music fade failed'
    subprocess.run(['ffmpeg', '-v', 'error', '-rtsp_transport', 'tcp', '-i',
        'rtsp://127.0.0.1:554/validation/dualsync-go', '-frames:v', '1', '-y',
        str(out / 'pause.png')], check=True, timeout=20)
    api('control', {'action': 'play'})
    resumed = wait(lambda s: s['media']['phase'] == 'playing')
    assert resumed['music']['gain'] == 0
    subprocess.run(['ffmpeg', '-v', 'error', '-rtsp_transport', 'tcp', '-i',
        'rtsp://127.0.0.1:554/validation/dualsync-go', '-frames:v', '1', '-y',
        str(out / 'hud.png')], check=True, timeout=20)
    profiles = []
    for phase in ['playing', 'paused', 'none']:
        if phase == 'paused':
            api('control', {'action': 'pause'})
        if phase == 'none':
            api('media/unload', {})
        first = api('status')
        started = time.monotonic()
        time.sleep(45)
        last = api('status')
        elapsed = time.monotonic() - started
        rates = {key: (last['outputs'][key]['out_time'] - value['out_time']) / elapsed * 24000 / 1001
            for key, value in first['outputs'].items()}
        profiles.append({'phase': phase, 'fps': rates, 'renderer': last['renderer']})
        assert all(rate >= 23.4 for rate in rates.values()), 'output rate below target: ' + str(rates)
        assert last['renderer']['p95_ms'] < 41.7, 'compositor exceeded frame budget'
    (out / 'network.json').write_text(json.dumps({'passed': True,
        'healthy_pid': healthy_pid, 'old_failed_pid': failed_pid,
        'recovered_pid': recovered['outputs']['dub']['pid'],
        'held_source_position': held, 'pause_music_gain': paused['music']['gain'], 'profiles': profiles}, indent=2))
    print('RTSP publishing, isolated friend outage, recovery, pause music and HUD passed')
finally:
    api('control', {'action': 'stop'})
    api('settings', restore)
    enabled.clear()
    listener.close()

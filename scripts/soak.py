import json
import resource
import time
import urllib.request
from pathlib import Path

base = 'http://127.0.0.1:18090/api/'
out = Path('/var/lib/dualsync-dev/verification')
out.mkdir(exist_ok=True)

def api(path, data=None):
    request = urllib.request.Request(base + path,
        data=json.dumps(data).encode() if data is not None else None,
        headers={'Content-Type': 'application/json'})
    return json.load(urllib.request.urlopen(request, timeout=40))

def settled(phase):
    deadline = time.monotonic() + 120
    while time.monotonic() < deadline:
        status = api('status')
        if status['media']['phase'] == phase:
            return status
        if status['media']['operation']['state'] == 'failed':
            raise RuntimeError(status['media']['operation']['error'])
        time.sleep(.5)
    raise RuntimeError('state timeout: ' + phase)

start = api('status')
api('control', {'action': 'pause'})
api('control', {'action': 'episode', 'value': start['episode']})
settled('paused')
api('control', {'action': 'play'})
start = settled('playing')
pids = {key: value['pid'] for key, value in start['outputs'].items()}
index = start['media']['index']
samples = []
begin = time.monotonic()
while time.monotonic() - begin < start['media']['duration'] + 180:
    status = api('status')
    outputs = status['outputs']
    assert {key: value['pid'] for key, value in outputs.items()} == pids, 'publisher reconnected'
    assert all(value['restarts'] == 0 for value in outputs.values()), 'publisher restarted'
    assert all(value['queued_frames'] <= 8 for value in outputs.values()), 'unbounded queue'
    sample = {'wall_seconds': time.monotonic() - begin, 'phase': status['media']['phase'],
        'index': status['media']['index'], 'source_seconds': status['position'],
        'outputs': outputs, 'renderer': status['renderer']}
    samples.append(sample)
    (out / 'soak-progress.json').write_text(json.dumps(sample, indent=2))
    if status['media']['index'] != index or status['media']['phase'] == 'ended':
        break
    time.sleep(10)
else:
    raise RuntimeError('episode did not complete at sustained output rate')
api('control', {'action': 'pause'})
settled('paused')
held = api('status')['position']
time.sleep(6)
assert api('status')['position'] == held, 'pause source clock advanced'
for offset in [-10, 10, 80]:
    api('control', {'action': 'seek', 'value': offset})
    settled('paused')
assert {key: value['pid'] for key, value in api('status')['outputs'].items()} == pids
api('control', {'action': 'play'})
settled('playing')
api('media/unload', {})
assert api('status')['media']['phase'] == 'none'
assert {key: value['pid'] for key, value in api('status')['outputs'].items()} == pids
api('control', {'action': 'stop'})
(out / 'soak.json').write_text(json.dumps({'passed': True, 'samples': samples,
    'elapsed_seconds': time.monotonic() - begin, 'publisher_pids': pids}, indent=2))
print(json.dumps({'passed': True, 'elapsed_seconds': time.monotonic() - begin, 'samples': len(samples)}))

# Zeus deployment

The Go release 0.4.0 replaced the Python service on 9 October 2026.

On 13 October 2026, the backend was updated with automatic requalification after
a fingerprint change and preservation of prepared songs during automatic Plex
music refresh. See `STARTUP_REPORT.md` for the investigation and regression tests.

A subsequent music update preserves shuffle history across automatic catalog
refreshes. Every unique song is selected once before a new cycle starts; prepared
songs count as selected and keep their queue order. Regression tests cover repeated
catalog refreshes, duplicate entries, cycle boundaries, catalog changes and a
one-song source. Queue and Plex integration tests passed with the race detector;
`go vet ./...` also passed.

Music controls now include `music_play` and `music_pause`. `pause_silent` pauses
media and holds music without advancing its position; normal `pause` resumes
music. The API exposes the explicit hold as `music.paused`. The full race suite,
OpenAPI response checks and vet passed before this backend update. The frontend
was retained; `FRONTEND_HANDOFF.md` defines the new actions for its next update.

The panel and API are available at `http://172.16.0.52:8090`. The enabled
`dualsync.service` starts the backend at boot. Publishing still starts manually.

Runtime files are installed under `/opt/dualsync`. Private configuration is in
`/etc/dualsync/config.json`; private settings, playback state and caches are in
`/var/lib/dualsync`. The media root is `/mnt/data-vault`.

The production destinations, credentials and shared path `app/72` were retained
from Python. The tested Go selection, source position, HUD and Plex music
settings were retained from staging. The selected playlist has 23 items; item
2 was restored at 398.60995833341565 seconds. Music remains on Plex playlist
6332. Publishing was stopped before migration and remains stopped after testing.

The former port 18090 service, `dualsync-dev.service`, is stopped and disabled.
The build source and Go toolchain remain under `/opt/dualsync-dev` for future
development and testing. Development source is maintained in
`D:\source\Personal-Projects\DualSync`.

## Python archive

The old runtime, saved settings, playback state and systemd unit are archived at:

```text
/mnt/data-vault/DataGraveyard/DualSync/20261009T195402Z-python/python-implementation.tar.gz
```

SHA-256:

```text
c23788f261fe4f4cf3c4287c3387c2c01679c92fa0c772cce1c3f47578f92e29
```

The archive is private because its saved settings contain credentials. Its
directory also contains `SHA256SUMS`. Archive readback verified the original
Python entry point and saved settings before the old runtime was removed.

## Verification

- The latest backend passed `go test -race ./... -count=1 -timeout=3m` and
  `go vet ./...` on Zeus.
- The production unit passed systemd validation and is active and enabled.
- The served frontend matches the packaged frontend files.
- Both production destinations accepted NVENC publishers without reconnects
  during the startup check. Stopping the test retained the selected position.
- The existing hardware qualification remains valid: dual-output NVENC measured
  104.1858 fps per output; CPU fallback measured 71.7724 fps per output.

The portable archive is built with `scripts/build-release.sh 0.4.0`. Generated
archives, binaries and private runtime state are excluded from Git. Historical
reports describe their original test runs; this file records the current
deployment.

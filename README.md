# DualSync

DualSync publishes two independent video/audio feeds to RTMP or RTSP servers while keeping their source positions on one clock. FFmpeg publisher processes remain connected through pause, seek, media replacement and playlist changes. Go owns playback state, the HTTP API, music and broadcast composition; FFmpeg decodes, burns subtitles and encodes H.264/AAC.

The bundled `webroot` is a dependency-free static control panel (`index.html`, `app.css`, `app.js`) with the media library browser, playlist preview, per-output track selection, playback, music, settings and encoder diagnostics. See `api/openapi.yaml` for the implemented contract. The source repository also contains `FRONTEND_HANDOFF.md` with development environment details; that machine-specific document is excluded from releases.

## Install

Extract the Linux amd64 archive where the service should live, then run:

```sh
tar -xzf dualsync-0.4.0-linux-amd64.tar.gz
cd dualsync
sudo ./install.sh
```

The installer asks for media root, listener, service account and whether to serve the bundled frontend. It installs in the extracted directory and writes a systemd unit with absolute paths. Keep that directory in place. To relocate, stop the service, move the directory and rerun the installer there. Existing configuration is preserved. There is no Docker dependency.

For a noninteractive installation:

```sh
sudo ./install.sh --noninteractive --media-root /srv/media \
  --listen 0.0.0.0:8090 --service-user dualsync
```

The installer requires systemd, FFmpeg/ffprobe and readable installation/media directories. It does not install packages or GPU drivers. The service account receives existing video/render group memberships; parent directories still need traversal permission. Configuration is stored under `/etc/dualsync`, and private credentials, playback state, artwork and diagnostic results under `/var/lib/dualsync`. The panel starts on boot; streams start manually.

Every available hardware candidate is tested using two concurrent 30-second 1080p H.264 encodes through the publishing input path. Encoded files must contain 720 frames and decode without errors. Qualification requires at least 1.25 times the target throughput per output. Preference is NVENC, then Intel QSV, then Intel/AMD VAAPI. CPU fallback also requires more than four usable physical cores on bare metal or more than four usable allocated vCPUs in a VM, respecting affinity and CPU quota. VM host physical cores are reported unknown. A working GPU does not need to satisfy the CPU fallback threshold.

If no candidate qualifies, installation requires explicit unsupported acknowledgement. Noninteractive installation needs `--allow-unsupported`. Unsupported status remains in the API. Changed hardware, driver or FFmpeg invalidates the saved qualification; run diagnostics again. Initial qualification does not establish long-term stability on every GPU. Intel/AMD devices require actual hardware testing.

## Run and configure

```sh
./bin/dualsync --media-root /srv/media --listen 127.0.0.1:8090 \
  --state-dir /var/lib/dualsync
./bin/dualsync doctor --encoding-test --json
```

`--config FILE` loads JSON configuration. CLI flags override it. Defaults for `webroot` and `assets` come from the executable's installation directory, not the current working directory. `--webroot DIR` selects alternate frontend files. `--encoder nvenc|qsv|vaapi|cpu` and `--device DEVICE` override automatic selection; the chosen candidate must be qualified unless unsupported operation is explicitly allowed.

Configure destinations through `/api/settings` while streams are stopped. Use two RTMP or RTSP base URLs and one shared stream path. Passwords are write-only. Folder-media and music paths are relative to the configured library root. Plex playlist music prefers readable local paths within that root and otherwise fetches original media from Plex. The backend rejects traversal and symlink escapes. It supports single-file dual audio, independently paired files, text subtitle burn-in and PGS/DVD bitmap subtitle burn-in.

The output format is 1920x1080 at 24000/1001 fps, H.264 at 8 Mbps, stereo AAC at 48 kHz/192 kbps. Source alignment tolerance is ten seconds; receiver buffers can add different delays. A network or encoder failure can require the failed publisher to reconnect. Explicit Stop streams closes both publishers.

Plex video APIs support movie/show lookup, season playlists, independent media versions and mixed Plex/storage outputs. Matched metadata supplies titles, episode numbers, summaries and artwork. Local files within the media root are preferred; unavailable files use Plex original-media HTTP byte ranges. Credentials remain server-side. Initial remote subtitle/font preparation can read the whole source, then seeks reuse the cached result. Multipart versions and Plex video playlists are unsupported. Frontend controls for these APIs are delivered separately; see the frontend handoff and OpenAPI contract.

To serve the frontend externally, start the backend with `--no-webui` and proxy `/api/*` under the frontend's own origin. For example, with Caddy:

```caddyfile
:8090 {
    handle /api/* {
        reverse_proxy 127.0.0.1:8091
    }
    handle {
        root * /srv/dualsync/webroot
        file_server
    }
}
```

Bind the backend to loopback port 8091 in this example. There is no CORS support or mandatory Caddy dependency. Keep the reverse proxy restricted to trusted private-network clients; the backend sees the proxy's source address.

Idle music keeps three upcoming songs prepared. Ready skips preserve gain and crossfade over 250 ms. Music source, shuffle and volume can change while publishing. Plex connection settings use a server URL, write-only token and playlist ID; `/api/plex/playlists` and `/api/plex/playlist?id=ID` provide selection data. The current panel does not yet include the Plex selector. The frontend handoff documents the required controls.

## Development

Source lives in the Windows development repository; builds and tests run on Zeus. Preserve the Python production runtime while the new frontend is integrated. `legacy/python` contains the migration baseline. `bin/dualsync migrate --python-state DIR --media-root ROOT --state-dir NEW_STATE` imports private settings and playlist position into a separate state directory. The command does not modify the old state.

```sh
go test -race ./...
go vet ./...
bash scripts/build-release.sh
```

The build script creates `dist/dualsync-0.3.0-linux-amd64.tar.gz` plus its SHA-256 file. The archive contains no saved credentials or personal server configuration. Automated HTTP tests validate JSON request/response schemas from the OpenAPI document. Native encoder and real server tests are separate from deterministic API tests. Test results and remaining hardware coverage are recorded in the source repository's `VALIDATION.md`.

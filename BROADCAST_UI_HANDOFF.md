# Broadcast UI handoff for Claude

Improve the UI drawn into DualSync's video streams. The user finds its current appearance unsatisfactory. You own the visual design and can edit the Go compositor to implement it. This document identifies the implementation and its playback constraints; it does not prescribe a replacement layout or style.

Work in `D:\source\Personal-Projects\DualSync`. Builds, tests and live rendering run on Zeus. The Windows repository is the source of truth; `/opt/dualsync-dev/src` on Zeus is its staging checkout.

Production update, 9 October 2026: Go runs from `/opt/dualsync` as `dualsync.service` on port 8090. `dualsync-dev.service` is retired. The source checkout remains available for builds, but rendering experiments require an isolated service and publishing path.

## Where the streamed UI is defined

The primary file is **`internal/dualsync/compositor.go`**. It draws pixels in native Go. Changing `webroot/app.css`, `app.js` or `index.html` changes the browser control panel, not the broadcast image. The compositor is compiled into the backend binary, so its changes require a rebuild and staging service restart.

| Symbol or file | Responsibility |
| --- | --- |
| `Scene` in `compositor.go` | Inputs for one output frame: phase, title, playlist index/total, source position/duration, output clock, video frame, prepared blur, pause age, music and HUD flag |
| `MusicInfo` in `compositor.go` | Music metadata, position/duration/gain, decoded artwork and artwork URL |
| `Renderer.Render` | Selects the scene, draws state labels/title/item count, pause icon and loading/seeking/buffering spinner, then adds the music card |
| `Renderer.gradient`, `updatePalette` | Moving idle background and colors sampled from album art; current gradient renders at 240×135 then scales to output size, with a two-second palette transition |
| `Renderer.musicCard` | Artwork, title, album artist, album and music progress; currently 700×144 at offset (64,64), with 112×112 artwork |
| `Renderer.ring` | Optional playing HUD: 96×96 episode progress ring and play icon, bottom-right with 48 px margin |
| `PauseBlur` | Prepares the paused frame's blur and dimming, with stronger blur toward the center |
| `Renderer.text`, `center`, `face` | Font loading, sizing and positioning; current text truncation counts runes rather than fitting available width |
| `roundedRect`, `blendLayer`, `rgbYUV`, `scalePlane` | Shape drawing, RGBA layer compositing, color conversion and scaling |
| `Renderer.layer`, `cacheKey`, `cache` | Cached artwork/text layers and reusable frames; review cache keys when adding dynamic visual properties |
| `internal/dualsync/engine.go` | Constructs a `Scene` separately for Sub and Dub, calls `Render`, then enqueues video/audio for persistent publishers |
| `Engine.prepareBlurLocked` | Captures each output's paused frame and computes `PauseBlur` asynchronously, rejecting stale results |
| `internal/dualsync/music.go` | Loads music metadata and artwork outside the render loop; artwork is currently decoded to 112×112 here |
| `internal/dualsync/config.go` | Output constants: 1920×1080, 24000/1001 fps and `FrameBytes` |
| `assets/fonts/NotoSansJP.ttf`, `assets/fonts/OFL.txt` | Bundled font and license; `NewRenderer` uses it when available, with Go Regular as fallback |

`Scene.Index` is zero-based. Display item numbers as `Index + 1`. Position, duration, clock and pause age are in seconds. `Scene.Clock` is the advancing output clock; source position stops while paused or buffering. Use the output clock for animations that must keep moving during pause.

## Scene behavior to preserve

- Playing with HUD disabled returns the decoded source frame unchanged. With HUD enabled, copy the frame before drawing; never mutate the decoder's source buffer. Keep the episode HUD optional and at approximately 50% opacity.
- No selection, loading, Ready and playlist completion use the animated gradient. When music is active, show artwork, music title, album artist and progress. Handle missing artwork, missing metadata and unknown duration.
- Pause uses each output's own frozen video frame, not a shared frame. Preserve the center-heavy blur transition, currently 500 ms, and display pause state, media title and playlist position.
- The engine renders `resuming` through the paused scene while music fades out. Do not change audio fade timing to implement visual changes.
- Seeking and buffering show their state and stay silent. The engine also suppresses music during loading that will continue playing. A shorter finished output uses `waiting` while the other finishes; it must remain silent.
- Stop streams closes the publishers. All other visual and playback transitions retain the publisher connections.

The intended idle background is a slowly moving gradient influenced by album artwork. The original inspiration was iPixelGalaxy's Spicy Lyrics fork. Preserve the requested information and state behavior; choose typography, spacing, artwork treatment, icons and animation details yourself.

## Rendering constraints

Frames are contiguous limited-range BT.709 **YUV420p**, not RGBA: full-size Y plane followed by quarter-size U and V planes. Keep the output length equal to `FrameBytes`. RGBA layers use Go's premultiplied alpha convention; `blendLayer` converts and blends them into YUV. Review both color and alpha handling if replacing those helpers.

One renderer is used sequentially for both outputs. Its face/layer caches are not protected for concurrent `Render` calls. Keep rendering serial unless you also make those caches safe. Cache entries must distinguish the output-dependent properties they contain. Bound new caches and animation state.

Keep font parsing, asset decoding, filesystem access, FFmpeg processes and blur preparation outside the per-frame publishing path. If larger artwork is needed, adjust decoding in `music.go` rather than repeatedly loading images from `Render`. Bundle new assets with licenses under `assets`; resolve them through `Config.Assets`, not a machine-specific path.

The frame interval is about 41.7 ms. `/api/status` exposes `renderer.p95_ms` and `renderer.samples`; check the metric while both outputs run. Previous profiles in `VALIDATION.md` are historical reference, not results for your changes. Measure sustained output clocks, queues and memory along with renderer time.

## Build and staging

SSH access: `root@172.16.0.52`. Go is `/opt/dualsync-dev/toolchain/go/bin/go`. Staging is `dualsync-dev.service`, configured by `/var/lib/dualsync-dev/config.json`, with binary `/opt/dualsync-dev/src/bin/dualsync`. Production is the separate Python `dualsync.service` on port 8090. Deploy only to staging.

Copy only the files you changed from the Windows repository into the corresponding paths under `/opt/dualsync-dev/src`. For example, from PowerShell:

```powershell
scp internal/dualsync/compositor.go root@172.16.0.52:/opt/dualsync-dev/src/internal/dualsync/compositor.go
```

Repeat for changed helpers, tests or assets. On Zeus, run:

```bash
cd /opt/dualsync-dev/src
export PATH=/opt/dualsync-dev/toolchain/go/bin:$PATH
gofmt -w internal/dualsync/compositor.go
go test -race -timeout 90s ./...
go vet ./...
go build -o bin/dualsync.next ./cmd/dualsync
```

Include other changed Go files in formatting, and copy formatting changes back into the Windows repository. The module requires Go 1.26 or later.

Before restarting, check `/api/status` at the staging panel. If the user is testing active streams, coordinate the restart with them. Stop only staging streams through the panel, then activate the built binary:

```bash
systemctl stop dualsync-dev
mv /opt/dualsync-dev/src/bin/dualsync.next /opt/dualsync-dev/src/bin/dualsync
systemctl start dualsync-dev
systemctl is-active dualsync-dev
```

The panel is http://172.16.0.52:18090/. Publishers start manually. Keep the shared publishing path at `validation/dualsync-go` and retain saved credentials without displaying or copying them into documentation.

Watch both outputs in VLC or another RTSP player:

```text
Sub: rtsp://172.16.0.52:554/validation/dualsync-go
Dub: rtsp://10.0.0.3:554/validation/dualsync-go
```

The staging library root is `/mnt/data-vault`, equivalent to `Z:\`. Music is `Arimodu/Music`. The real Tensura selection pairs `Media/Anime/[SubsPlease] Tensei Shitara Slime Datta Ken (1080p)/Season 4` with `TensuraDub`. Additional fixtures remain under `DualSync-Test`. `FRONTEND_TEST_SETUP.md` contains the test flow.

## Verification and delivery

Start with `TestRendererPassThroughHUDAndPause` in `internal/dualsync/engine_test.go`. Extend coverage for meaningful rendering invariants introduced by your changes. Add a deterministic preview test/tool if useful: construct representative `Scene` values, render YUV frames and convert them to PNG with the same BT.709 range. Include long English/Japanese titles, absent artwork, short and unknown durations, each idle phase, pause transition samples and HUD progress at 0%, 50% and 100%.

Inspect actual encoded captures as well as preview images. For a single captured frame on Zeus:

```bash
ffmpeg -rtsp_transport tcp -i rtsp://127.0.0.1:554/validation/dualsync-go -frames:v 1 -y /tmp/dualsync-broadcast-preview.png
```

For animation, capture a short video and inspect the transition. FFmpeg RTSP stream-copy captures need `-copyinkf` to retain the stream's AAC audio packets. Existing `scripts/soak.py` and `scripts/network_integration.py` mutate live staging state and destinations; read them before running, rather than using them as visual preview tools.

Check both outputs through pause, resume, seek, item changes, idle music and waiting. Verify publisher PIDs remain unchanged through playback controls, playing video remains unchanged with HUD off, and both encoders maintain output rate without growing queues.

Deliver changes in the Windows repository and a `BROADCAST_UI_REPORT.md` describing modified files, staging deployment status, captured preview locations, test results and any remaining issues. Leave production untouched. If a release is requested, `scripts/build-release.sh` packages the backend, webroot and assets together.

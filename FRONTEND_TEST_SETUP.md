# Combined frontend and backend test

Historical test setup. On 9 October 2026, the combined Go service moved to production at http://172.16.0.52:8090/ and port 18090 was retired. The instructions below describe the earlier isolated validation deployment, not the current production service.

The combined panel is available at http://172.16.0.52:18090/. The staging service is `dualsync-dev.service`. Production remains on port 8090; its service and publishing configuration were not changed.

The paired Tensura playlist from the real folders is loaded at 00:00 in Ready. Streams are stopped, so press **Start streams**, open both playback URLs, then press **Play**.

| Output | Playback URL |
| --- | --- |
| Sub | `rtsp://172.16.0.52:554/validation/dualsync-go` |
| Dub | `rtsp://10.0.0.3:554/validation/dualsync-go` |

Open these URLs in VLC or another RTSP player. Both outputs publish over RTMP to the existing MediaMTX servers and can be read over RTSP. Keep the shared stream path at `validation/dualsync-go` during testing. Port 554 is intentional on these servers.

## Test flow

1. Start streams and wait for both output cards to show Live. Before Play, check the idle background and music.
2. Play the loaded selection. Sub should use Japanese audio with English subtitles; Dub should use English audio with English Signs.
3. Pause for at least five seconds. Check each output's frozen frame, blur, pause text and music fade. Resume should fade music out before episode audio resumes.
4. Try −10s, +10s and +80s while playing and paused. Watch the source position and confirm each output's PID stays unchanged.
5. Change the playlist item while playing, then while paused. Toggle the HUD under Stream settings while watching both outputs.
6. Stop streams before changing connection or music settings. Leave the shared publishing path unchanged. Stop streams when finished.

The library root is `/mnt/data-vault`, corresponding to the Windows `Z:\` network drive. Music comes from `Arimodu/Music`. The loaded playlist pairs `Media/Anime/[SubsPlease] Tensei Shitara Slime Datta Ken (1080p)/Season 4` with `TensuraDub`.

The earlier fixture library remains under `DualSync-Test` within this root:

| Folder | Purpose |
| --- | --- |
| `Paired/Sub`, `Paired/Dub` | Three Tensura episode pairs, plus an unmatched Sub file |
| `Pairing edge cases/Sub`, `Pairing edge cases/Dub` | Duplicate episode identifier and an unmatched item |
| `Single/Tensura`, `Single/Fire Force`, `Single/Amagi` | Single-file track selection |
| `Bitmap subtitles` | Brave Witches fixtures |
| `Edge cases` | Synthetic video with English audio and no subtitles |
| `Music` | Embedded artwork tracks and one synthetic track without artwork |

For another selection, browse the real library or `DualSync-Test`, choose single or paired mode, select the source folders/files and inspect the playlist preview. Loading replaces the selection and waits for Play.

## Verification completed on 2026-10-07

The new frontend was served directly by the Go backend without a proxy. A headless Chromium check exercised paired folder selection, unmatched-file reporting, Load to Ready, Start, Play, Pause, +80s while paused, Resume, live HUD enablement and Stop. Both actual outputs showed Live using NVENC. No page JavaScript errors were observed. Screenshot: `verification/combined-ui.png`.

Go race tests and `go vet ./...` passed on Zeus. A new automated handler test checks the packaged HTML, JavaScript and CSS responses, their content types and cache revalidation. Static responses now use `Cache-Control: no-cache` so browsers revalidate files after upgrades.

The combined check did not repeat the previous full-episode soak or hardware qualification. −10s, +10s, item changes, mobile browsers, failure recovery and a diagnostics run through this frontend remain useful user checks.

Release archive: `dist/dualsync-0.2.1-linux-amd64.tar.gz`, with a SHA-256 file alongside it. It includes the new static webroot.

## Answers to FRONTEND_REPORT.md

- `status.diagnostics.running` is set to true before a diagnostics job starts and remains true until its result replaces the report. Polling `/api/status` is sufficient.
- `Load.title` is accepted as the selection/playlist title. The current `media.title` displayed by status is the current item's title.
- `music_next` has no media-phase restriction. It can be sent during playback; the music position otherwise remains held while media plays.
- `rescan` is permitted while streaming. It invalidates library caches without closing publishers.
- Preview item titles currently retain filenames, including extensions. No title formatting change was made for this test.

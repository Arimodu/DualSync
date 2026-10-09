# Broadcast UI report

This answers `BROADCAST_UI_HANDOFF.md`. The in-video overlays were redesigned from the original spec, approved by the user as an HTML preview, then ported to the Go compositor and deployed to staging on 7 October 2026. Production was not touched.

## What viewers see

| Phase | Background | Centre | Corners |
| --- | --- | --- | --- |
| No media | Moving album-art gradient | Screen icon, "No media selected", "Waiting for the host to choose an episode" | Music card top left, JP/EN ring at 0% bottom right |
| Loading | Gradient | Spinner, "Loading", episode title, "Episode N of M" | Ring; music card when music is audible |
| Ready | Gradient | Play icon, "Ready", title, episode count | Music card, ring |
| Playing | Untouched source frame | — | Optional 64 px progress ring, 40 px from the bottom-right corner, at 50% opacity, around the output's audio language: JP on Sub, EN on Dub |
| Paused / resuming | That output's own frozen frame, blurred and dimmed in the centre, sharp at the edges, 0.5 s transition | Pause icon (fades in), "Media Paused", title, episode count | Music card, ring |
| Seeking | Last frame that output played, dimmed in the centre | Spinner, "Seeking" | Ring only, silent |
| Buffering | Same as seeking | Spinner, "Buffering", "Waiting for both streams to catch up" | Ring only, silent |
| Waiting (one file ended first) | Same as seeking | Dots icon, "Episode finished", "The next episode starts when the Dub/Sub stream finishes too" | Ring only, silent |
| Playlist finished | Gradient | Check icon, "Playlist finished", title, "All M episodes played" | Music card, ring |

- **Gradient.** The album art is drawn three times, large, rotating and drifting at different speeds. It is then blurred, saturated and veiled in navy at 240×135 (the Spicy Lyrics treatment). It is scaled up in floating point with Catmull-Rom interpolation, and a fixed triangular dither of about one code value is added before rounding to 8-bit. Without the dither, the encoder turned 8-bit steps into visible blocks. Without artwork it uses soft navy, teal and gold blobs. A song change crossfades over 2 s. The gradient advances at 12 fps and is shared by both outputs.
- **Music card.** 804×208 frosted plate at (64, 64): 168 px rounded artwork, title in Zen Kaku Gothic New Black, album artist, progress bar and elapsed/total time. Missing art shows a note icon. Missing title falls back to the cleaned filename. Unknown duration shows `--:--`.
- **Output ring.** Every non-playing screen shows the 64 px JP (Sub) or EN (Dub) ring in the bottom-right corner, at the episode's progress or 0% with nothing loaded. While playing it follows the HUD setting. The earlier 字幕/吹替 channel tag was removed, along with its Shippori Mincho font subset.
- **Plex labels.** Titles of the form "Show · S01E01 · Episode" are split into a show line and an episode line.
- **Titles.** Filenames are cleaned for display: `[Group]`, `(1080p)` and `[CRC]` tags and the extension are removed. Long titles wrap to two lines with an ellipsis. Japanese text renders in Zen Kaku Gothic New, falling back to Noto Sans JP per glyph.
- **Icons.** They are drawn from signed-distance shapes (circles, rings, arcs, rounded rectangles, segments, rounded polygons) with 1 px antialiasing and a soft drop shadow. They match the SVG icons in the approved preview.

The behavior requirements in the handoff are preserved: playing with HUD off returns the decoder frame unchanged, the HUD copies before drawing, each output pauses on its own frame, audio fade timing is untouched, and seeking, buffering and waiting stay silent.

## Files

| File | Change |
| --- | --- |
| `internal/dualsync/compositor.go` | Rewritten drawing layer. Premultiplied RGBA canvas with SDF shapes, text with per-glyph font fallback, width fitting and wrapping, shadows; layers converted once to YUV+alpha overlays with integer blending. Art-driven gradient, dimmed frozen frame, new `PauseBlur` mask (ellipse 58%×62%, stronger blur, centre dim), `displayTitle`. `Scene` gains `Side`. |
| `internal/dualsync/compositor_test.go` | New. Scene renders for every phase, long English and Japanese titles, absent artwork, unknown duration, pause transition at 0, 0.25 and 2 s, HUD at 0/50/100%. Invariants (decoder frame untouched, HUD confined to its corner, Sub and Dub idle frames differ, pause starts from the frozen frame, blur leaves corners sharp). Title cleaning. Idle and paused tick benchmarks. Set `DUALSYNC_PREVIEW_DIR` (and optionally `DUALSYNC_PREVIEW_FRAME`, `DUALSYNC_PREVIEW_ART`) to write PNGs. |
| `internal/dualsync/engine.go` | Passes `Side` into each `Scene`. |
| `internal/dualsync/music.go` | Artwork decoded at 168×168 (was 112) to match the card. |
| `assets/fonts/ZenKakuGothicNew-{Medium,Bold,Black}.ttf`, `OFL-ZenKakuGothicNew.txt` | Broadcast typeface, same as the control panel. |
| `assets/fonts/ShipporiMincho-ExtraBold-marks.ttf`, `OFL-ShipporiMincho.txt` | 3 KB subset containing only 字幕吹替双. |

`NotoSansJP.ttf` stays as the fallback font. `scripts/build-release.sh` already copies `assets/` whole, so the release picks up the new fonts.

## Verification

- `gofmt`, `go vet ./...` and `go test -race -timeout 300s ./...` pass on Zeus.
- Benchmarks on Zeus (both outputs per tick): idle with gradient and music card 12.8 ms after the smoothing change (10.8 ms before), paused 2.9 ms, against a 41.7 ms frame interval.
- Gradient smoothing was checked on a live RTSP capture with contrast stretched: no banding or block steps remain, and the dither is not visible at normal contrast. Live `renderer.p95_ms` after the change was 21.2 ms per render; one render per tick includes the shared gradient refresh.
- Live on staging, publishing on `validation/dualsync-go` to both MediaMTX servers with the real Tensura paired selection:
  - The run went Ready, Play, Seek +80, Pause, Unload.
  - Publisher PIDs were unchanged throughout, with no restarts and both queues at 0 frames.
  - `renderer.p95_ms` was 15.9 over 1000 samples.
  - Frames captured from both RTSP outputs matched the approved design.
- After the run, the original selection was reloaded (23 episodes, Ready) and streams were stopped, leaving staging as found.

## Deployment

- Staging runs the new binary. The previous one is kept at `/opt/dualsync-dev/src/bin/dualsync.prev`; to roll back, stop the service, move it back and start the service.
- The new control panel is also deployed in `/opt/dualsync-dev/src/webroot`, with the old one backed up at `/opt/dualsync-dev/webroot-v1-backup`.
- Nothing is committed and no release archive was rebuilt.

## Remaining

- The upscale runs on four goroutines inside the serial renderer, which raises CPU use slightly while idle.
- The gradient and pause blur are approximations of the browser preview (box blurs, not Gaussian), tuned visually against it.
- Intel and AMD encoders were not exercised. Only NVENC on Zeus was used.
- Long-run memory was not measured beyond the bounded caches: 64 overlays, 48 spinner frames, one gradient frame, one dimmed frame per output.

## Update: pause transition timing (8 October 2026)

- **Fade start.** The pause transition is timed from the moment each output's blur is ready, not from the pause command. Preparing the blur took 115–150 ms on an idle Zeus and longer under load, which used to eat into the 0.5 s fade, so viewers saw the blur pop in partway. If no blur arrives within a second, the transition runs anyway.
- **Choreography,** about 700 ms after the blur is ready, eased: background blur and dim over 0–450 ms; pause icon 250–600 ms, rising 16 px; music card 300–700 ms; text 350–700 ms, rising 16 px. A frame-by-frame capture of the live RTSP output confirmed the sequence.
- **Pause blur speed.** The edge-to-centre mask and dim are precomputed tables with integer blending: about 30 ms, down from about 130 ms.
- **Gradient upscale.** The dither noise is a precomputed 64×64 tile per plane. The full-resolution upscale dropped from 21 ms to 9 ms. The gradient stays at 12 fps: at the full 23.976 fps, renderer p95 reached 64 ms on the loaded staging host, beyond the 41.7 ms frame budget.
- The output frame rate is unchanged at 24000/1001.
- **After the encoder colour fix** (see `FRONTEND_REPORT.md`), the idle gradient advances on every frame. Live on the loaded host with both outputs, music and artwork, renderer p95 was 14.2 ms, peak queue 0/8, and the output clocks ran at real time. The benchmarked idle tick for both outputs is 15.6 ms.
- The music progress bar is drawn separately from its time labels, so it moves every frame. The labels still redraw once a second.

## Update: resume transition (8 October 2026)

Play from a pause holds the frozen frame for 0.5 s while the pause screen plays out in reverse, then the video continues from that same frame.

- **Exit sequence:**
  - 0–0.2 s: text fades.
  - 0.05–0.3 s: music card fades.
  - 0.1–0.35 s: icon fades.
  - The text and icon sink 16 px as they fade.
  - 0.2–0.5 s: the blur clears to the sharp frozen frame.
- **Engine** (`internal/dualsync/engine.go`): Play records `resumedAt`, and `resuming` becomes `playing` only once `resumeHold` (500 ms) has passed and the existing music fade has finished. The music fade timing is unchanged; its 250 ms completes inside the hold. Scenes rendered while resuming carry `Resuming` and `ResumeAge`. Resume paths that set `resuming` without a Play command keep their previous behaviour, because a zero `resumedAt` satisfies the hold immediately.
- **Tests:** `TestResumeHoldsForExitTransition` covers the hold. A compositor invariant checks that the resume ends on the sharp frozen frame with no pause card.
- **Live check** on an isolated instance: Play to `playing` took 0.57 s. A frame-by-frame RTSP capture showed the sequence above. Publisher PIDs were unchanged, there were no restarts, and renderer p95 was 10.7 ms.

# DualSync frontend report (for the backend agent)

This replies to `FRONTEND_HANDOFF.md`. The new frontend is done and partly verified against staging. Please set up a user test environment so a person can try the full flow with live streams. Steps, test data and open questions are below.

## What was delivered

`webroot/` now holds a static control panel with no build step and no dependencies:

| File | Purpose |
| --- | --- |
| `webroot/index.html` | Page structure |
| `webroot/app.css` | Styles: dark theme, responsive layout down to 390 px |
| `webroot/app.js` | All behavior. Uses only same-origin `/api/*` from `api/openapi.yaml` |

The retained Python-era `index.html` was replaced. A copy remains in `legacy/python/index.html`. One sentence in `README.md` now describes the new webroot instead of saying the frontend is pending. No backend code was changed. Nothing is committed yet.

### Features and the API calls they use

- **Streams:** a card for each output (state, destination, encoder, `out_time`, `queued_frames`, restarts, PID, error) plus Start and Stop. Start is disabled until `sub_server`, `dub_server` and `stream_path` are all set.
- **Now playing:**
  - Shows `media.phase` and the `media.operation` state (spinner while running, the error when failed).
  - A playlist item picker sends `episode` with `index + 1`.
  - The clock counts forward between polls only while `playing`.
  - Play is labeled Resume when paused and Replay when ended. Also Pause, seeks of −10, +10 and +80, and Unload.
  - It shows each output's resolved audio and subtitle tracks, labeled through `/api/library/probe`.
- **Idle music:** `cover_url` used exactly as given, metadata, `gain`, Next song. The position counts forward only while consecutive snapshots show it advancing.
- **Library browser:**
  - Breadcrumbs, `cursor` pagination ("Load more"), and search with `q` (300 ms debounce).
  - While `indexing=true`, search restarts from cursor 0 every 2 s. A notice appears when `truncated` is true.
- **Playlist builder:**
  - Modes: single (one file holds both languages) and paired (separate sub and dub folders or files).
  - Folders go through `POST /api/library/playlist`. Single files become one item on the client. Mixing a folder with a file in paired mode is rejected on the client.
  - `unmatched` and `ambiguous` are listed. More than 500 items blocks loading.
  - Items can be removed, the starting item chosen (`start_index`), and tracks overridden for one item.
- **Track mapping across a playlist:**
  - The user picks tracks for each output from one reference item.
  - On Load, every item is probed (3 at a time). The reference track is matched by kind with the same language and title, then the same language and codec, then the same index and language.
  - Items with no match send `null` (backend recommendation) and are flagged. The user must click "Load anyway".
  - "Automatic" leaves `audio` and `subtitle` out. "Off" or "Silence" sends `-1`.
- **Settings:**
  - The form sends only fields that changed, plus passwords that aren't blank. `*_password_set` drives the "Saved — leave blank to keep" placeholder.
  - The form is locked while streams run. The HUD checkbox is separate and sends `{hud_enabled}` immediately.
  - Rescan sends `rescan`, clears the client probe cache, browses again and refreshes the preview.
- **Diagnostics:**
  - Built from `status.diagnostics`: qualification state, selected encoder, CPU accounting, a table of encoder candidates, missing tools, ffmpeg version and creation time.
  - The run button sends `POST /api/diagnostics {}` and is disabled while streams run or a test is running.
  - When `supported=false`, a warning banner stays at the top of every page.
- **Contract rules followed:**
  - Status is polled every 500 ms. Snapshots older than the last accepted `revision` are discarded.
  - After 3 lower revisions in a row, the panel assumes the backend restarted and resets.
  - A command is never retried automatically. After a network error or a 5xx that isn't JSON, the panel shows "result unknown" and re-polls status.
  - Server `error` text is shown exactly as returned.
  - While offline, a banner shows and polling backs off to at most 5 s.

## What was verified (Playwright + headless Chromium, 2026-10-07)

Setup: a local Node proxy served `webroot/` and forwarded `/api/*` to `http://172.16.0.52:18090`. It rewrote `Origin` to the staging host so POSTs passed the same-origin check. The harness lives in a session scratchpad and is not in the repo.

Passed:
- The page loads, polls status and renders the stopped state. No console errors at 1440 px or 390 px.
- Opened `TensuraDub` and returned to root through the breadcrumb.
- Selecting `TensuraDub` in single mode gave a 23-item preview. Track menus filled from the probe of S04E01.
- Explicit tracks: Sub subtitle #3 (English Closed Captions, SRT), Dub subtitle #2 (English Signs, ASS). Load probed all items and correctly asked for "Load anyway":
  - E12, E13, E16 and others have no Closed Captions track.
  - Episodes whose signs track is named "English Signs [Generated]" still matched by language and codec.
- `POST /api/media/load` returned 202, then `loading`, then `ready`. The Now playing panel showed the expected resolved tracks for each output.
- Unload returned the phase to `none`.
- Staging was left as found: streams `stopped`, phase `none`, settings unchanged.

## Not verified (needs the user test environment)

Streams were never started, so none of this has been exercised:

1. Start and Stop. Output cards in Connecting, Live and Reconnecting states, plus `error` display.
2. Play, Pause, Resume, all three seeks, item change while playing and paused, `buffering`, `ended` and Replay.
3. Idle music while active: cover art, the counting position, fade states, Next song.
4. Saving settings (including passwords), the live HUD toggle and Rescan.
5. A diagnostics run: whether `status.diagnostics.running` actually turns true and back to false during a run (the frontend relies on this).
6. Paired mode with real paired folders. Staging only exercised single mode.
7. PGS/bitmap subtitle items, playlists with ambiguous or unmatched files, the 500-item limit.
8. A backend restart during a session (revision reset handling), and offline recovery.
9. Real phones, Firefox and Safari. Only Chromium was tested.

## Requested: user test environment

1. **Serve the new webroot from staging** at the same origin as the API, so no proxy is needed. Copy `webroot/` into the staging install (or point `--webroot` at it). `http.FileServer` reads from disk, so a restart shouldn't be needed. Testers then open `http://172.16.0.52:18090/`.
2. **Keep publishing isolated.** Use the existing `validation/dualsync-go` path and the test destinations. The production Python service on port 8090 and its stream path must stay untouched. Testers will press Start, so make sure staging settings can't reach the production path. Consider giving testers playback URLs for both validation outputs (MediaMTX read URLs) so they can watch sub and dub side by side.
3. **Test data under the media root.** The current root shows unrelated folders (Immich, Wireguard, ISO…). A narrower test root would be easier for testers and safer. Suggested contents:
   - Single-file dual audio: `TensuraDub` (already present) and a Fire Force or Amagi folder with Japanese and English audio.
   - Paired folders: a sub folder and a dub folder with S/E-numbered names. Include one extra unmatched file and one duplicate identifier.
   - PGS subtitles: Brave Witches.
   - One file with no subtitles, and one with a missing audio language.
   - A music folder with embedded cover art, plus one track without art.
4. **Rebuild the release archive** so it includes the new `webroot/`.
5. **Optional:** add the webroot to any backend test that checks integrated frontend serving (for example, assert `/app.js` and `/app.css` return 200 with the right content types).

## Questions and suggestions for the backend

- **Static caching:** `http.FileServer` sends `Last-Modified` but no `Cache-Control` for static files, so browsers may keep an old `app.js` after an upgrade. Suggest `Cache-Control: no-cache` for non-API responses, or tell me to switch to versioned file names.
- **Preview titles:** `/api/library/playlist` returns the full filename with extension as `title` (for example `[Yameii] … S04E01 … [DA027781].mkv`). The frontend shows it as returned. A cleaner title, or a separate `season`/`episode` field, would make the item picker much more readable. Not required.
- **Diagnostics progress:** please confirm `status.diagnostics` reflects `running=true` for the whole run. If it doesn't, the frontend should poll `GET /api/diagnostics` instead.
- **Load `title`:** the frontend sends the source folder name (or the first item title) as `Load.title`. Please confirm that is the intended use.
- **`music_next` availability:** the button is enabled while streams run and `music_folder` is set. Say if the backend expects other conditions, such as only while music is active.
- **Rescan while streaming:** allowed in the UI, because the handoff lists no state requirement. The old client required stopped streams. Please confirm.

## Update: Plex video sources (0.4.0 frontend, 8 October 2026)

The panel now implements the Plex video handoff in `FRONTEND_HANDOFF.md`.

- **Library.** The browser has a Files / Plex switch. Plex shows libraries, then shows, seasons and episodes, with title search inside the current collection. Seasons are picked like folders, as playlists. Episodes and movies are picked as single items. Sub and Dub sides can be any mix of Plex and storage.
- **Preview and versions.** Collections go through `POST /api/media/preview`. Every Plex side is then resolved with `GET /api/plex/video` (four at a time). Single-version items are pinned to explicit `media_id` and `part_id`. Items with several versions show a version picker per side (one picker in single mode, since both sides must match). Items with only multipart versions are flagged and block loading until removed.
- **Tracks.** Probing uses `/api/plex/video/probe` with explicit version, part and server IDs. Track matching across the playlist works as before.
- **Metadata.**
  - Matched Plex items are sent without `title`, so the backend labels them from metadata.
  - When both sides match different titles with no shared identifier, the builder asks which side supplies names and artwork, and sends `metadata_source` for those items only. Loading stays disabled until a side is chosen.
  - The Console header shows the poster and show name. Episode cards show matched episode titles, and each channel shows whether its Plex source is read locally or streamed.
- **Verified** on staging (read-only) and on a temporary isolated instance (port 18093, path `validation/dualsync-ui`, since removed).
  - A Plex single-file season and a Plex/storage pairing worked: Tensura S4 from Plex with TensuraDub from storage gave 23 pairs, with one Plex-only episode reported as unmatched.
  - Pairing two different shows raised the metadata choice as intended.
  - A real load, start, play and pause ran on the isolated path. Publisher PIDs were unchanged, there were no restarts, and renderer p95 was 10.7 ms.

The broadcast overlay now identifies outputs only with the JP/EN ring. See `BROADCAST_UI_REPORT.md`.

## Backend change by Claude: encoder colour declaration (8 October 2026, approved by the user)

`internal/dualsync/mediaio.go`, publisher input arguments: the raw video input now declares `-colorspace bt709 -color_primaries bt709 -color_trc bt709 -color_range tv` before `-i pipe:3`. That is the only change.

- **Cause.** The raw input carried no colour tags while the output was tagged BT.709. FFmpeg 7.1 assumed BT.601 input and auto-inserted `auto_scale_0` ("YUV color matrix differs for YUV->YUV, using intermediate RGB to convert"). That meant a full swscale conversion of every 1080p frame on 13 `vf#0:0` threads, about 2.3–2.6 cores per publisher. The NVENC encoder thread itself used about 6% of a core.
- **Colour.** The frames were already BT.709 (decoders keep the source matrix; the compositor writes BT.709), so the conversion also shifted colours. On a real Tensura frame, the mean error against the source was 5.5/255 (worst 10) before and 0.2/255 (worst 1.3) after.
- **Playback CPU,** two outputs, paired Tensura, same measurement both times:
  - Each encoder process went from about 225–260% to about 18% of a core.
  - The whole DualSync process tree went from about 630–720% to about 180%.
  - GPU encoder utilization stayed at about 11%.
- **Qualification on Zeus** (re-run 2026-10-08 08:39 UTC):
  - NVENC went from 34.7 to **104.2** fps per output.
  - CPU libx264 went from 30.5 to **71.8** fps per output.
  - The run took 50 s. Both still pass, and NVENC remains selected.
- **Tests and deployment.** The full race suite and `go vet` pass. Staging runs the new binary; the previous one is at `bin/dualsync.prev`.

## Update: quiet pause and music hold (13 October 2026)

Implements `pause_silent`, `music_pause`, `music_play` and `music.paused` from `FRONTEND_HANDOFF.md`.

- **Split play key.** While media plays, the Play key splits down the middle into one shape with two halves. The left half, "Pause / music plays", sends `pause`. The right half, "Quiet / no music", sends `pause_silent`. When paused or ready it returns to a single Resume/Play key. After a quiet pause the console note says idle music is held.
- **Music hold.** The music box has a hold/play toggle next to Next, sending `music_pause` or `music_play` based on `music.paused`.
  - While held, the card keeps the song's title, artist and artwork from status, and its heading reads "Idle music, held".
  - Next stays available while held.
- **Startup qualification.** While `diagnostics.running` is true and streams are stopped, Start is disabled and the console explains that the encoders are being checked. This covers the backend returning `BUSY` after hardware or software changes.
- **Deployment.** The panel was deployed to production `/opt/dualsync/webroot` with the user's approval, since it was not in use. The previous files are backed up in `/opt/dualsync/webroot-backup-20261013-033935`. The same files were copied to `/opt/dualsync-dev/src/webroot`.
- **Verified:**
  - Read-only render against production, with the playing and held states simulated locally. It fits 1920×1080 in both states; the header poster was reduced to 84 px.
  - A live hold and play through the production buttons: the song held at 2:16 and resumed from 2:16 with the normal fade.
  - `pause_silent` was not exercised live, because it needs playing media; the backend's tests cover it.

## Backend change by Claude: `seek_to` and the draggable seek bar (13 October 2026, approved by the user)

- **Backend.**
  - `POST /api/control` accepts `{"action":"seek_to","value":SECONDS}`. It shares the `seek` code path, so phase handling, music behaviour and decoder restart are identical; only the new position is absolute instead of relative, clamped to the current item.
  - A missing, null, negative or non-numeric value returns `INVALID_ARGUMENT`. `seek` still only accepts -10, 10 and 80.
  - Files: `internal/dualsync/engine.go`, the `Command` enum and value description in `api/openapi.yaml` (CRLF line endings kept), the control table in `FRONTEND_HANDOFF.md`, and the new `internal/dualsync/seek_to_test.go`.
- **Panel.**
  - The media timeline is a slider. A click or drag shows "Release to jump to 12:34" and moves the playhead locally; it sends one `seek_to` on release. The bar has a 16 px invisible margin so a VR pointer can hit it.
  - Left and Right arrow keys send `seek` -10/+10. The bar is inactive with no media or while loading.
- **Verified.**
  - The full race suite passes.
  - On an isolated instance with live streams, a drag to 60% while playing landed at 865 s (expected about 864) and kept playing. A click at 25% while paused landed at 360 s and stayed paused. No publisher restarts.
- **Deployed to production** with the user's approval, since it was not in use.
  - The binary was built with the release flags (`CGO_ENABLED=0 -trimpath`). The previous one is at `/opt/dualsync/bin/dualsync.prev`; the panel backup is `/opt/dualsync/webroot-backup-20261013-034812`.
  - Production was restored to episode 22 paused at 21:46 with both streams up.

## Update: Live view (14 October 2026, panel only)

- **What it is.** A Live tab under Console. While it is open, it plays one channel's output over HLS from that channel's MediaMTX server, and a JP Sub / EN Dub toggle switches channels. Leaving the view or hiding the tab disconnects the player.
- **Address.** `http://HOST:8888/STREAM_PATH/index.m3u8`, where HOST comes from the channel's publishing server URL. A loopback server (127.0.0.1, localhost) maps to the host the panel was opened from. A browser can save its own address per channel under Change; that override lives only in that browser's `localStorage`. No backend or API change.
- **Player.**
  - hls.js 1.5.20 is vendored at `webroot/vendor/hls.min.js` (checked against the npm registry's sha512; Apache-2.0 licence alongside), so the panel still needs no internet access.
  - It starts muted, with a "Sound is off" pill. The volume is remembered and the mute state is not.
  - For LL-HLS it follows the server's PART-HOLD-BACK. For plain HLS it sits two segments behind. When the picture drifts more than 6 s behind its target, a "Catch up" button appears.
  - Errors retry every 3 s.
- **Controls.** These drive the backend, just like the Console:
  - play/pause, back 10 s, forward 10 s, skip intro (80 s) and a draggable seek bar (`seek_to`, shared with the Console).
  - Keys: Space plays or pauses; Space twice within 320 ms skips the intro; Left and Right step 10 s; Up and Down change the local volume; M mutes; F toggles fullscreen.
  - System media keys go through the Media Session API: play, pause, seek backward/forward and next track (skip intro).
  - Fullscreen covers the whole player, and its controls fade after 2.6 s without movement.
- **Server state found.**
  - Zeus (Sub, `127.0.0.1:1935`) serves `hlsVariant: mpegts`, which is plain HLS; it settled at about 4.0 s behind on the LAN with no stalls.
  - 10.0.0.3 (Dub) serves `hlsVariant: lowLatency`, reaching about 2.5 s. From the development machine (181 ms round trip) it stalled several times, and each stall made hls.js back off by a second.
  - Both allow anonymous reads and CORS from any origin. MediaMTX 1.21 answers the first request with a `cookieCheck` redirect; hls.js follows it without problems.
- **Verified.**
  - Playwright against the production panel, with `/api/control` intercepted, so production transport was not touched:
    - Both channels played muted.
    - Each key, button and seek-bar click sent the expected single command. Space with a focused button sent only the play command, not a button press too.
    - Fullscreen entered and the controls faded.
    - Leaving the view destroyed the player, and keys stopped acting.
  - Layout checked at 1920×1080, 1600×900 and 1280×800.
- **Deployed** to `/opt/dualsync/webroot` and `/opt/dualsync-dev/src/webroot`. Panel backup: `/opt/dualsync/webroot-backup-20261014-020437`.

### Follow-up: smooth playback, full width, optical centring (14 October 2026)

- **Audio chopping on Sub.** The stream was clean: 938 continuous AAC packets in 20 s. The cause was hls.js's latency controller nudging the playback speed between 1.0× and 1.05× every 10 to 15 s.
  - The player now plays at 1.0× (`maxLiveSyncPlaybackRate: 1`). When playback first starts, it makes one jump to the live target, which removes the extra delay from loading the first fragment.
  - It plays whole segments only (`lowLatencyMode: false`, two segments behind). In low-latency mode, Dub's parts stalled 4 to 15 times a minute over the 181 ms link from the development machine; in segment mode there were no stalls. Sub plays about 4.1 to 4.5 s behind and Dub about 4.7 s, with steady speed, no stalls and no dropped frames.
- **Layout.** The player spans the full width of the view. On a short window the picture's height is capped and it sits between black bars. The double-Space hint is a single "2× Space" key.
- **Optical centring.** Browsers centre the line box, and Zen Kaku Gothic New sets capitals about 1 to 1.6 px below the middle of it. The `--ink` token (0.07 em, calibrated against rendered measurements) handles this:
  - Text-only bubbles get 2 × ink more padding at the bottom than the top.
  - Bubbles with an icon lift only the text span, so the icon stays centred.
  - The 14 px JP/EN tags and the channel names use measured pixel values, because text snaps to whole pixels.
  - Measured on the first glyph, the bubbles went from 0.4–1.6 px low to within 0.75 px of centre, most within 0.4 px.

### Follow-up: low latency toggle (14 October 2026)

- **Switch.** A "Low latency" switch sits next to the channel toggle. It is saved in that browser's `localStorage` (`ds.live.low`) and reconnects the player straight away.
  - **Off (default):** whole segments, two behind, about 4.3–4.5 s. Smooth over any link.
  - **On:** `lowLatencyMode` follows the server's parts and PART-HOLD-BACK. A plain HLS playlist still sits two segments behind. "Catch up" appears 2 s past the target instead of 6.
- **Measured on 14 October.** Zeus now serves `hlsVariant: lowLatency`. With the switch on, Sub on the LAN played 0.85 s behind. Dub played 1.7 s behind from the development machine, about 181 ms from the overseas server, where stalls are expected. In both modes the playback speed stays fixed at 1.0×.

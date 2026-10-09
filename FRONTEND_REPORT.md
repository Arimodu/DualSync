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

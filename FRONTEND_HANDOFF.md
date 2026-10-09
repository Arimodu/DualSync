# DualSync frontend API handoff

The backend publishes two independent H.264/AAC outputs using one source clock. Selected media and publisher connections have separate lifetimes. The frontend communicates with the JSON HTTP API and supplies static files in `webroot/`.

The production API is `http://172.16.0.52:8090/api/status`. Go replaced Python on 9 October 2026. The old staging service on port 18090 is retired. Use an isolated listener and publishing path for development tests; production uses the saved `app/72` path.

## Contract and hosting

`api/openapi.yaml` is the authoritative OpenAPI 3.1 contract. The backend exposes the same document at `/api/openapi.yaml`. The file uses JSON syntax, which is valid YAML. It can be imported into Swagger Editor or another OpenAPI 3.1 client.

Use relative `/api/...` URLs. The default backend serves `webroot` and the API from one HTTP origin. `--no-webui` disables static frontend serving while retaining the API. An external server must proxy `/api/*` to the backend under the frontend's own origin. There is no CORS support. Access is restricted to private or loopback clients; proxy access control must preserve that restriction.

POST requests require `Content-Type: application/json`. The body limit is 512 KiB. Unknown JSON fields are rejected. Requests from another Origin are rejected. All times are seconds, volume settings are percentages from 0 to 100, and music gain is a multiplier from 0 to 1. Paths are relative to the configured media root and use forward slashes. Absolute paths, traversal and symlink escapes fail.

No endpoint returns stored passwords. `sub_password_set` and `dub_password_set` indicate whether credentials exist. Blank or omitted passwords preserve current values; there is no password clearing operation in this version.

## Browse, select and load

1. `GET /api/library?path=&cursor=0&limit=100` lists directories and video files. Each entry has `path`, `name` and `directory`. Follow `next_cursor`; `-1` means the last page. Maximum limit is 200.
2. Add `q=` for recursive filename and folder search within `path`. Indexing runs asynchronously. While `indexing=true`, results are partial. Repeat from cursor zero once indexing finishes. `truncated=true` means the 200,000-entry index limit was reached.
3. `GET /api/library/probe?path=...` returns duration, metadata and tracks. A cold probe can take up to 30 seconds. Track `index` is the absolute FFmpeg stream index, not its position in the returned list. `recommended` is `sub`, `dub` or empty. Track `kind` is video, audio or subtitle.
4. `POST /api/library/playlist` previews a folder playlist. Send `{"sub_folder":"Anime/Show"}` for single-file mode, or also provide `dub_folder` for paired folders. Preview returns `items`, unmatched entries and duplicate identifiers. Pairing uses season/episode identifiers. Entries without reliable identifiers are not silently matched by position. Correct the returned item list before loading when necessary.
5. `POST /api/media/load` replaces the selection asynchronously. It resets source position to zero and enters Loading, then Ready. Loading never starts playback automatically.

Example single-file load with different audio/subtitle mappings:

```json
{
  "mode": "single",
  "start_index": 0,
  "items": [{
    "title": "Episode 1",
    "sub": {"path": "Anime/Show/S01E01.mkv", "audio": 1, "subtitle": 3},
    "dub": {"path": "Anime/Show/S01E01.mkv", "audio": 2, "subtitle": 4}
  }]
}
```

For paired mode, use `"mode":"paired"` and different paths for `sub` and `dub`. Those output names are API identifiers; tracks remain independently configurable. `audio` and `subtitle` accept an absolute stream index, `-1` for silence/Off, or null/omission for recommendations. Single mode requires identical paths in both outputs. Provide 1 to 500 items. Indices can change between files; probe each item before applying explicit mappings across a playlist.

The load response is HTTP 202 with a status snapshot. Poll `media.operation`: `running`, `complete` or `failed`. Its `id` identifies the current operation. A newer load or unload cancels the older operation. Validation that needs probing can fail asynchronously; inspect `media.operation.error` and the top-level `error`.

## Status and controls

`GET /api/status` returns the complete current state. `revision` increases as the server advances. Discard snapshots older than the last accepted revision, including poll responses that arrive after a command response. A 500 ms polling interval is sufficient for control state. Music and playback positions are snapshots; interpolate only for display, then reconcile with the server.

`media.phase` is one of `none`, `loading`, `ready`, `playing`, `paused`, `resuming`, `seeking`, `buffering` or `ended`. `media.index` is zero-based; `media.total` is the playlist count. `media.items` includes resolved track mappings. The legacy `episode` field and episode command use one-based numbering. The top-level legacy `mode` summarizes publisher/playback state; use `media.phase` for new integrations.

`outputs` is empty while streams are stopped. Otherwise, `sub` and `dub` expose `ready`, `pid`, `out_time`, `restarts`, `error`, `queued_frames` and `encoder`. Connection recovery can replace one publisher without replacing the healthy publisher. `out_time` is the output clock, not the media position. Source position freezes during buffering and recovery.

Send commands to `POST /api/control`:

| Action | Value | Required state and result |
| --- | --- | --- |
| `start` | omitted | Both destinations configured; qualified encoder or explicit unsupported configuration. Opens both publishers, including when no media is selected. |
| `stop` | omitted | Closes publishers and stops music. Selected media remains. |
| `play` | omitted | Streams started and media ready/paused/ended. Music fades out before episode audio resumes. Ended playback restarts the final selected item. |
| `pause` | omitted | Pauses playing/resuming/buffering media. Repeated pause is harmless. |
| `seek` | `-10`, `10` or `80` | Media selected. Clamped to the current item. Preserves playing/paused/ready behavior. |
| `episode` | integer starting at 1 | Available playlist item. Preserves playback state. |
| `unload` | omitted | Clears selection while publishers remain connected. |
| `music_next` | omitted | Selects another idle music track; does not change media selection. |
| `rescan` | omitted | Invalidates probe caches and rebuilds the search index asynchronously. Preview folders again to refresh playlist choices. |

`POST /api/media/unload` with `{}` is equivalent to the unload command. There is no separate Stop media action. Pause, seek, unload and episode changes retain publisher connections. If one file ends first, that output becomes idle and silent until both files finish. Both then advance together; the last playlist item enters Ended.

## Settings, music and diagnostics

`POST /api/settings` accepts a partial update. Fields are `sub_server`, `dub_server`, one shared `stream_path`, per-output usernames/passwords, `music_folder`, `music_volume` and `hud_enabled`. Server fields are base RTMP/RTSP URLs without paths, embedded credentials or query parameters. `stream_path` provides the shared relative publishing path. Music folders are root-relative directories. HUD, music and Plex settings can change while streams are running; destination settings require Stop streams. See the 0.3.0 section below for added fields.

Music status includes active, title, artist, album_artist, album, position, duration, gain, path and cover_url. Use `cover_url` verbatim when present. Missing artwork gives an empty URL. Music fades in over five seconds on pause/idle and fades out over 250 ms on resume. Its position freezes during playback and seeking; the same song resumes at the next pause. Both outputs share music samples.

`GET /api/diagnostics` returns encoder qualification, CPU accounting, candidate results, missing tools and warning text. `supported=false` must remain visible to users. `POST /api/diagnostics` with `{}` requires stopped streams and no existing diagnostic job. HTTP 202 returns `running=true`; poll until false. Tests can take several minutes and exercise two real encoding jobs. They do not start network publishers.

An HTTP error returns `{"code":"...","error":"..."}`. Common codes include INVALID_JSON, INVALID_ARGUMENT, INVALID_PATH, OUTSIDE_ROOT, NOT_FOUND, CROSS_ORIGIN, PRIVATE_NETWORK_REQUIRED, STREAMS_STOPPED, STREAMS_RUNNING, NO_MEDIA, MEDIA_NOT_READY, DESTINATIONS_REQUIRED, UNSUPPORTED_HOST, BUSY and PUBLISHER_FAILED. Show the server message. Do not automatically retry control commands after an uncertain network result; fetch status first. GET requests can be retried. A load operation's failure is reported in status rather than changing its earlier 202 response.

## Delivery and verification

The completed static frontend lives in `webroot/` and includes storage and Plex video controls. No frontend build server is required by the backend. Rebuilding the archive includes the supplied webroot without changing backend code. The installed release serves `/opt/dualsync/webroot`; development source remains in the Windows repository and `/opt/dualsync-dev/src` on Zeus.

Backend tests run on Zeus:

```sh
/opt/dualsync-dev/toolchain/go/bin/go -C /opt/dualsync-dev/src test -race ./...
```

The tests exercise HTTP handlers with request/response schemas from OpenAPI, deterministic publisher/decoder substitutes, filesystem confinement, track selection, cancellation and real FFmpeg audio decoders. Real server validation uses isolated publishing paths. Do not alter production service or destination paths during frontend development.
# Music queue and Plex integration (0.3.0)

The backend now keeps three upcoming songs prepared. Claude remains responsible for frontend controls. Use the endpoints below through the same-origin API. No frontend layout or styling is prescribed.

Saved settings add:

| Field | Type / behavior |
| --- | --- |
| `music_source` | `folder`, `plex`, or `off`; existing installations use `folder` |
| `music_shuffle` | Boolean; defaults to true. False follows folder enumeration or Plex playlist order |
| `plex_server` | HTTP(S) base URL, without credentials, query or path |
| `plex_token` | Write-only string in settings updates. A blank value preserves the saved token |
| `plex_token_set` | Read-only Boolean in public settings |
| `plex_playlist_id` | String ID returned by playlist listing |

Update these through `POST /api/settings`. Music and Plex fields, music volume and HUD can be updated while publishing; destination fields still require stopped streams. Source or playlist changes keep the old song until replacement audio is ready, then switch immediately. Switching while media plays holds the new idle song at zero. Volume changes do not rebuild the queue. Shuffle changes rebuild upcoming songs while preserving the current song. Ordered refresh continues after the current track when it is still present.

`GET /api/plex/playlists` returns `{ "playlists": [{ "id": "123", "title": "Example", "smart": true, "count": 42 }] }`. A smart playlist's reported count can be zero even when it has tracks; preview is authoritative.

`GET /api/plex/playlist?id=123` returns `{ "tracks": [...] }`. Each track contains `id`, `title`, `artist`, `album_artist`, `album`, `duration` in seconds and `file`. The file is an absolute path as seen by Plex, not a DualSync library-relative selection. The backend resolves local paths safely or fetches original media from Plex; the frontend must not construct authenticated media URLs.

Listing/preview calls require configured server and token. `PLEX_FAILED` with HTTP 502 reports upstream failures without exposing credentials. Selection requires all three connection/token/playlist settings, which can be sent together:

```json
{"music_source":"plex","plex_playlist_id":"123","music_shuffle":true}
```

Keep `music_folder` saved when selecting Plex, so switching back to `folder` restores the previous folder. `off` disables music. Enable Next based on the selected source rather than only checking `music_folder`.

`POST /api/control` with `{"action":"music_refresh"}` refreshes source contents and upcoming songs while preserving the current song. Plex sources also refresh every five minutes. `music_next` remains the skip command, never retried automatically. It promotes queued audio without resetting gain and uses a 250 ms audible crossfade. The existing five-second idle fade and 250 ms fade before episode playback remain.

`status.music` retains its previous fields and adds:

- `source`, `playlist_id`: selected source and playlist ID.
- `playlist_title`: current track's playlist name, blank for folder music.
- `ready_count`: fully prepared upcoming songs, 0 through 3, excluding current song.
- `preparation_state`: `idle`, `preparing`, `ready`, or `failed`.
- `error`: preparation/skip error or warning; also included in top-level status error.
- `upcoming`: ordered objects with `path`, `title`, `artist`, `duration` and `cover_url`. Plex paths are opaque `plex:TRACK_ID` identifiers, not filesystem paths.

`preparing` can coexist with ready songs while the queue refills. During source replacement, selected source/ID can change before the old current song changes. Ready queue entries stay at zero until promoted. If every track fails, preparation stops; Refresh or a source change retries. Stop streams closes current and queued decoders. Next is fastest when `ready_count > 0`; cold source preparation still takes time.

The OpenAPI schemas document every response. Automated handler tests cover listing, preview, live selection, redaction and refresh. Gated real-server test command on Zeus:

```bash
cd /opt/dualsync-dev/src
DUALSYNC_PLEX_LIVE=1 /opt/dualsync-dev/toolchain/go/bin/go test -v -run TestLivePlexMusicQueue ./internal/dualsync
```

## Plex video sources (0.4.0)

The backend supports Plex/Plex and Plex/storage selection. Claude owns frontend controls. Continue using same-origin `/api` requests and the OpenAPI document. No layout or styling changes are prescribed here.

Video access uses the existing `plex_server` and write-only `plex_token` settings. It does not require Plex music or a music playlist. Movies and TV seasons are supported; Plex video playlists and multipart video versions are excluded.

### Selection flow

| Request | Result |
| --- | --- |
| `GET /api/plex/libraries` | `{libraries:[{id,title,type}]}`; type is `movie` or `show` |
| `GET /api/plex/videos?library_id=1&q=Example&cursor=0&limit=100` | Library titles, filtered by title; `{entries,next_cursor}` |
| `GET /api/plex/videos?parent_id=123` | Show seasons or season episodes; same pagination |
| `GET /api/plex/video?id=123` | `{server_id,metadata,versions}` |
| `GET /api/plex/video/probe?id=123&media_id=456&part_id=789&server_id=SERVER_ID` | `{probe,source,metadata}`; verified FFmpeg stream indices |
| `POST /api/media/preview` | Season/folder playlist preview, using the existing `{items,unmatched,ambiguous}` response |

Library and parent IDs are alternatives. Entries contain `id`, `type`, `title`, `season` and `episode`. Cursor is an item offset; `next_cursor=-1` marks completion. Limit is 1 through 200, default 100. Search filters the current collection's titles; browse a show and then its season to select episodes.

Details expose every version as `{id,resolution,video_codec,audio_codec,parts}`. Each part has `{id,name,size}`; size is bytes. Select an explicit version when there is more than one. Multipart versions cannot load. IDs are strings. A version ID belongs to one item, so never reuse it across a whole season without checking each item.

Probe before selecting tracks. `probe.tracks` uses the existing Track schema and absolute FFmpeg indices, not Plex stream IDs. Cold probing can take up to 45 seconds without blocking playback controls. Returned `source` contains a resolved Plex reference and `transport`, but leaves audio/subtitle/video selection unset. Apply independent selections for Sub and Dub. Keep the returned server identity in subsequent requests.

Source objects now accept exactly one of `path` or `plex`. `plex` contains required `id` and optional `media_id`, `part_id`, `server_id`. Storage sources retain root-relative paths. Optional `video` selects a non-attached video stream by absolute index. Existing `audio` and `subtitle` behavior is unchanged, including null recommendations and -1 silence/Off. Returned `transport` is `local` or `plex`; it is diagnostic and does not force a transport.

Single-file Plex example:

```json
{
  "mode": "single",
  "items": [{
    "sub": {"plex": {"id": "123", "media_id": "456", "part_id": "789"}, "audio": 2, "subtitle": 4},
    "dub": {"plex": {"id": "123", "media_id": "456", "part_id": "789"}, "audio": 1, "subtitle": 3}
  }]
}
```

For two Plex files/versions, use paired mode and independent references. For mixed mode, replace either source with `{"path":"Anime/Example/S01E01.mkv","audio":1,"subtitle":-1}`. Single mode requires identical source references or identical storage paths; track selections can differ.

Season/storage preview:

```json
{"sub":{"plex_season_id":"123"},"dub":{"folder":"ExampleDub"}}
```

Each collection accepts `folder` or `plex_season_id`. Omit `dub` to reuse each file for both outputs. A blank `folder` selects the storage root. Pairing uses season/episode numbers; files with episode-only names inherit a season only when the combined selection has exactly one explicit season. Preview titles remain blank so loading can use matched Plex labels. Resolve missing/duplicate entries, then probe each item's version and tracks before submitting the resulting items to `/api/media/load`.

### Metadata and state

Metadata contains `id`, `matched`, `type`, `title`, `show`, `season`, `episode`, `year`, `summary`, `identifiers`, `cover_url` and `poster_url`. Missing text is blank, missing numbers are zero, and missing artwork has blank URLs. `matched` means a recognized Plex/IMDb/TMDb/TVDb identity is present; local or agent-none identities do not qualify. Fetch artwork URLs verbatim. They include a server identity guard and return image data, never Plex credentials.

When at least one output has matched Plex metadata, the loaded item and `status.media.metadata` use it. Episode labels include show, SxxExx and episode title. A supplied nonempty item `title` overrides the generated label. Unmatched items retain ordinary/manual labels and omit loaded matched metadata.

Compare both matched selections' identifiers before loading. If they have no shared identity, ask which source supplies metadata and set item `metadata_source` to `sub` or `dub`. Choosing a source without matched metadata fails. The backend also checks conflicts during asynchronous loading. Status contains each loaded item's metadata and source mappings.

Loading still returns 202, enters Loading, then Ready, and requires Play. Poll operation ID/state/error as before. Asynchronous failures appear in `media.operation.error`; immediate failures use the existing `{code,error}` envelope. Added codes are `AMBIGUOUS_VERSION`, `PLEX_SERVER_CHANGED` and `METADATA_CONFLICT`.

Readable local files inside the media root are preferred. Otherwise the backend reads Plex's original media using authenticated byte ranges. The frontend must not construct media URLs or download videos. First remote subtitle/font preparation can read the entire original and takes up to ten minutes; subsequent seeks reuse the result. There is no Plex transcoding or full-video download cache.

Interrupted remote playback enters Buffering, holds both source positions and retries decoders after two seconds. Streams stay connected. Initial preparation failure remains a failed load. A changed Plex server cannot silently reuse another server's IDs; browse/select again. Active playback retains its original connection snapshot.

### Verification

Run from `/opt/dualsync-dev/src` on Zeus:

```sh
/opt/dualsync-dev/toolchain/go/bin/go test -race ./...
DUALSYNC_PLEX_VIDEO_LIVE=1 /opt/dualsync-dev/toolchain/go/bin/go test -v -run TestLivePlexVideoOriginal ./internal/dualsync
```

The gated test uses private development Plex settings and a known dual-audio item. It verifies local and forced original-HTTP decoding at multiple seek positions. Port 8090 is production. Port 18090 is retired; run frontend integration on an isolated instance with a separate publishing path.

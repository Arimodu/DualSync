# Prepared music queue and Plex integration

Implemented for DualSync 0.3.0 and deployed to Zeus staging at http://172.16.0.52:18090/. Production remains unchanged. Claude's frontend work is described in the new music/Plex section of `FRONTEND_HANDOFF.md`.

The backend prepares three upcoming songs, including artwork or placeholder and buffered decoded audio. Next promotes a ready song without resetting gain, then refills the queue. Audible skips use a 250 ms crossfade. Existing idle/resume fades and held music position during media playback remain. Source selection changes take effect when replacement audio is ready; shuffle and Refresh preserve current audio. Stop and Off close queued decoders.

Plex uses audio playlists visible to the saved token, including smart playlists. Readable local files within the media root are preferred. Otherwise the backend downloads original media from Plex using an authentication header, caches it locally and decodes that file. Tokens are write-only and do not appear in public settings or FFmpeg URLs. Completed Plex cache files are limited to 1 GiB; individual media downloads are limited to 256 MiB. Artwork downloads have a 10 MiB limit. A music worker prepares entries sequentially; source replacement cancels the old worker. PCM queues remain bounded.

Endpoints and settings are documented in OpenAPI and the frontend handoff:

- `GET /api/plex/playlists`
- `GET /api/plex/playlist?id=PLAYLIST_ID`
- `POST /api/settings`: music source/shuffle and Plex connection/playlist settings, permitted while streaming.
- `POST /api/control`: `music_next` and `music_refresh`.
- `status.music`: selected source, playlist, preparation state, ready count, upcoming metadata and errors.

Zeus staging has a privately saved connection to its existing Plex server. Folder music remains the selected source. Selecting a Plex playlist currently requires the API until Claude adds the panel selector. The packaged example contains no Plex token or server address.

## Verification

The complete Go race suite passed on Zeus. Targeted music/Plex/settings race tests passed after final validation and refresh changes. `go vet ./...` and OpenAPI specification validation passed.

Automated checks cover prepared promotion, preserved gain, 250 ms crossfade completion, repeated rapid skips, held position, Plex pagination, token redaction, invalid IDs, live settings, refresh, Off/Stop cleanup and forced download fallback. Existing tests continue to verify five-second fade-in and complete fade-out before episode playback.

The gated real-Plex test listed playlists, read a real playlist, filled current plus three ready songs and forced original-media fallback by making the local path unavailable. Three ready promotions measured 0.008, 0.018 and 0.002 ms inside the music engine. These are promotion costs, not cold source loading or browser/network measurements.

A live HTTP Next on the staging service returned in 0.64 ms. The next song appeared in status after 31.8 ms, with gain still 0.25 and both publisher PIDs unchanged. The queue refilled asynchronously. Six audio playlists were available through the staging API.

The final staging build also switched from folder music to a real Plex playlist while publishing, filled three upcoming songs, then restored folder music. Publisher PIDs remained unchanged through both source changes. Staging was left paused on episode 2 at 316.445 seconds, with three upcoming folder songs ready. Renderer p95 measured 19.54 ms during that check.

The frontend was not changed for this feature. Cold enumeration, metadata/artwork preparation or a Plex download can still delay the first ready song. Fast-skip measurements apply when the queue has prepared entries.

Release: `dist/dualsync-0.3.0-linux-amd64.tar.gz` and its SHA-256 file. Shell sources use LF line endings for Linux installation.

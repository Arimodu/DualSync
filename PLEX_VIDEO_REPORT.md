# Plex video backend validation

DualSync 0.4.0 adds movie/show browsing, season previews, Plex version selection, independent video/audio/subtitle tracks, mixed Plex/storage sources and matched metadata. Frontend controls remain Claude's responsibility; the integration contract is in `FRONTEND_HANDOFF.md` and `api/openapi.yaml`.

## Tests on Zeus

- Full Go race suite passed. Targeted race checks covered the final recovery-control changes, source schemas and HTTP handlers. Go vet and OpenAPI 3.1 validation passed.
- Automated tests covered Plex/Plex and Plex/storage loading, version ambiguity, multipart rejection, server identity changes, metadata conflicts, unmatched identities, season gaps, video-track indices and publisher continuity.
- HTTP transport checks covered authenticated GET/HEAD, byte ranges, cancellation and rejection of servers that ignore ranges. A real FFmpeg fixture verified ASS burn-in by comparing frames with subtitles enabled/disabled and checked English audio selection against its 880 Hz fixture tone.
- Cache tests checked the 1 GiB subtitle-cache limit, eviction of inactive files and protection of selected media. Preparation uses two workers, a 256 MiB per-file limit and cancellation tied to the media selection.
- Recovery tests simulated decoder interruption, held source position and preserved publisher objects. Pause and Stop override retry behavior; restarting playback rebuilds missing decoders. No live Plex outage was induced.

## Real media and publishers

The dual-audio Plex item used for validation was *I've Been Killing Slimes for 300 Years and Maxed Out My Level*, episode 1. Sub selected Japanese audio and English dialogue subtitles; Dub selected English audio and Signs subtitles.

Local and forced original-HTTP decoding passed at 0, 70, 80 and 160 seconds. In the clean decoder run, first-frame times were 0.14–0.43 seconds. Remote subtitle/font preparation was performed from the original file and reused for later seeks. These are fixture measurements, not a limit for all codecs or file sizes.

An isolated backend used port 18092 and `validation/dualsync-plex-check`, leaving production publishing paths untouched. Both NVENC publishers remained connected through pause, resume and all seek buttons. Both MediaMTX receivers decoded H.264 video and AAC audio without reported errors.

The clean controller run measured HTTP command responses at 2.1–18.2 ms and remote seeks at 0.84–2.22 seconds. Renderer p95 was 18.49 ms. An earlier run overlapped four publishers with the full race suite, raised Zeus load average to 37 and produced 10–12-second seeks; that run does not represent the clean latency measurements.

Mixed-source validation used a temporary copy of that same original file. Plex/storage and storage/Plex playlist entries loaded with the correct transport mappings. Episode switching took 0.71 seconds. Loading, switching episodes and unloading preserved both publisher PIDs.

A mounted Brave Witches PGS fixture was served through a mock Plex metadata server and the production authenticated range proxy. Original-HTTP bitmap decoding passed after seeks to 80 and 70 seconds, with first frames in 0.35–0.41 seconds. The fixture was not added to the real Plex library.

## Staging and delivery

Staging remains at `http://172.16.0.52:18090`; production Python service on port 8090 remains unchanged. The update preserved episode 2 at 368.329708 seconds, selected playlist and Plex music settings. Streams were stopped before cutover and remain manually started.

The portable release is `dist/dualsync-0.4.0-linux-amd64.tar.gz`. It contains the backend, existing frontend webroot, assets, installer and OpenAPI document. The archive was checked for the live Plex/publisher credentials and personal server addresses. Machine-specific handoff and validation reports are excluded.

The isolated service, private test configuration, temporary media copy and transfer archives were removed. Existing source media was not changed. A full-episode soak and a live upstream outage were not repeated in this feature validation.

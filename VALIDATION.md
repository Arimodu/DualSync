# Backend validation

Tests ran on Zeus on 7 October 2026. Production `dualsync.service` remained the Python runtime. All network publishing used `validation/dualsync-go`; the occupied production path was not used.

## Automated checks

`go test -race -timeout 90s ./...` and `go vet ./...` passed. HTTP tests validate successful requests and JSON responses against the embedded OpenAPI schemas. The OpenAPI 3.1 document also passed `openapi-spec-validator`.

Coverage includes browsing/search pagination and indexing, Unicode paths, traversal and symlink escapes, invalid tracks, subtitle Off, load/unload cancellation, settings persistence and redaction, concurrent commands, unsupported-host rules, CPU topology/affinity/quota accounting, Ready after restart, integrated frontend serving, API-only mode and a real same-origin reverse proxy.

Playback tests verify synchronized controls, all three seek offsets, publisher continuity, unequal-duration waiting, auto-next and independent publisher recovery. A transition test covers Pause while the next episode is loading. Music tests verify the five-second fade to configured gain, 250 ms fade-out and held song position during playback. Compositor tests cover pass-through, HUD source-frame preservation and pause blur.

Real FFmpeg tests encode AAC packets, decode different audio selections and burn ASS subtitles before and after seeks, including filenames with apostrophes, spaces, commas, brackets and semicolons. Mounted-media integration passed for Fire Force and Amagi Brilliant Park with Japanese/English audio and separate ASS selections, and Brave Witches with PGS subtitles at an 80-second seek.

## Real servers and performance

Both actual MediaMTX servers received RTMP and RTSP H.264/AAC publishing. Captures confirmed 1920x1080, 24000/1001 fps metadata and stereo 48 kHz AAC. Eight-second RTSP captures decoded 1,536,000 audio bytes per output. The outputs had different PCM hashes and RMS levels of 5544 and 5254, respectively. The sub capture showed burned English dialogue; the dub capture used the selected Signs track.

A complete 1440-second Tensura episode advanced to the next item with the same two publisher PIDs. Pause/resume, all seek offsets and unload retained those publishers. Across 155 recorded samples, the largest difference between publisher output clocks was 1.627 seconds, with queues never exceeding eight frames. This measures server-side output clocks; viewer buffering remains outside the shared source clock.

Two installer encoding benchmarks ran during that episode test and caused about 94 seconds of source buffering. The complete test, including transitions and transport checks, took 1690 seconds. It established continuity under resource contention, not a claim that four concurrent encodes can run at full speed on Zeus.

Separate 45-second profiles with the normal two publishers produced these results. Rate estimates use FFmpeg's half-second progress updates and therefore include endpoint sampling error.

| Scene | Estimated fps per output | Compositor p95 |
| --- | --- | --- |
| Playing with HUD | 24.00 / 24.00 | 1.54 ms |
| Paused, blur and music card | 24.02 / 24.00 | 14.23 ms |
| No media, moving gradient and music | 23.60 / 23.62 | 16.64 ms |

All measured compositor p95 values in those profiles were below the 41.7 ms frame interval. A main-process RSS snapshot during the episode was approximately 256 MiB. Decoder/publisher queues and render caches have fixed entry limits; this snapshot is not a multi-day memory-leak test.

An isolated TCP proxy interrupted only the friend's test RTSP connection. Source position froze at 3.670 seconds for the outage. The healthy publisher kept its PID; the failed publisher reconnected and playback resumed. Pause music reached gain 0.25, and resume returned music gain to zero. Captured pause/HUD images were inspected.

FFmpeg stream-copy capture initially omitted AAC packets from RTSP recordings. Audio-only decoding and capture with `-copyinkf` verified that audio was present. The capture command was corrected; no server configuration change was needed.

## Qualification and installation

The RTX 2080 SUPER passed the dual 30-second encoding/decode test as the `dualsync` account. Initial throughput was 35.1 fps per output; the final isolated qualification measured 34.7 fps, above the required 29.97 fps. CPU libx264 failed an earlier run at 27.3 fps, then passed the final isolated run at 30.5 fps and qualified as fallback. Qualification is sensitive to competing workloads. The KVM guest correctly reported 12 usable vCPUs and unknown host physical cores. Audio verification also required at least 99% of the expected decoded PCM sample count.

The extracted archive installed into a directory containing spaces. The generated unit passed systemd validation and served its API from that location. Installer rerun preserved configuration bytes. Relocation followed by unit regeneration started the service from the new directory, with bundled webroot/fonts resolved from the executable location. Unsupported acknowledgement was exercised while competing encoding jobs deliberately prevented qualification.

The source repository includes `verification/soak.json`, `network.json`, `audio.json`, `diagnostics.json` and inspected images. These test artifacts and all private runtime state are excluded from the release archive.

Intel QSV, Intel/AMD VAAPI and DVD subtitle decoding still require real fixtures/hardware. PGS bitmap support was tested. New frontend browser/headset validation belongs to the frontend integration stage. Production cutover remains deferred until that stage passes.

# Playback control latency

Measured on Zeus on 7 October 2026, against the real Tensura files on `/mnt/data-vault`.

The main delay was the FFmpeg `subtitles` filter opening the original MKV and reading its packets to initialize subtitle events. Every seek and episode change starts fresh video decoders, so this work was repeated even when the media and selected tracks had not changed. FFmpeg's implementation contains the initialization loop in [vf_subtitles.c](https://github.com/FFmpeg/FFmpeg/blob/n7.1/libavfilter/vf_subtitles.c).

| First decoded frame at 300 seconds | Sub | Dub |
| --- | --- | --- |
| Without subtitle burn-in | 0.768 s | 0.861 s |
| Subtitle filter reading original MKV | 11.213 s | 9.628 s |
| Subtitle filter reading local subtitle container | 1.000 s | 0.797 s |

Audio startup measured 0.346 s for Sub and 0.401 s for Dub. The live compositor's playing p95 was about 1.4 ms when inspected, so the broadcast UI redesign was not the measured decoder startup bottleneck.

## Change

`internal/dualsync/subtitles.go` caches a subtitle-only Matroska container under the backend state directory's `subtitles` folder. It retains all subtitle streams and attachments, allowing the existing subtitle ordinal and embedded fonts to remain usable. Stream copy preserves subtitle timestamps. Cache keys include the source path, device/inode, size and modification time.

`engine.go` prepares the current pair concurrently and warms the following episode after decoder startup. Preparation has two worker slots, a 90-second extraction timeout and deduplication for concurrent requests to the same file. Completed files are renamed into place. Failed preparation falls back to the original subtitle path. Bitmap subtitle decoding remains on its existing overlay path.

`mediaio.go` uses the cached container only for text subtitle initialization; video and audio continue to decode from the original media. The compositor, audio fades and persistent publisher behavior were retained.

The first uncached selection still needs extraction. Separate extraction measurements were 6.701 s for Sub and 3.173 s for Dub. These are different runs from the first-frame measurements above. Arbitrary jumps to an episode that has not been warmed can still incur this first-use cost. Completed caches persist across backend restarts.

## Validation and staging

Go race tests and `go vet ./...` passed on Zeus. The subtitle fixture test compares cached and original rendered frames at 0.5 and 3 seconds, verifies cache reuse and verifies invalidation after the media changes. This fixture also covers escaped filenames.

The fix is deployed to `dualsync-dev.service` at http://172.16.0.52:18090/. Production `dualsync.service` was not restarted or changed. The staging restart retained the selected episode and saved position, then restored publishing and pause state.

Two actual paused seeks on the updated staging backend measured:

| Command | HTTP response | Decoder ready |
| --- | --- | --- |
| +10 seconds | 0.019 s | 1.270 s |
| −10 seconds | 0.018 s | 0.999 s |

Publisher PIDs remained 168114 and 168115 across both seeks. Staging was left on episode 2, paused at 316.319 seconds. Receiver buffering can add delay beyond these server-side measurements.

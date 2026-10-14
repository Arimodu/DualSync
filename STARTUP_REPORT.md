# Startup qualification and music queue investigation

These changes were deployed to the production backend on 13 October 2026.
The frontend, private configuration and saved media selection were retained.

## Qualification

The saved qualification fingerprint includes the kernel version, NVIDIA driver
and device identity, CPU budget, FFmpeg version and available encoders. A server
upgrade can invalidate that fingerprint. Previously, startup marked the saved
report unsupported and required a manual benchmark.

Startup now launches background qualification when an existing fingerprint has
changed. The API reports that job as running. Publishing waits for the benchmark
to finish, so it cannot overlap network publishing or another diagnostics job.
Successful results are saved for the next restart. Unchanged results are reused.
Failed tests remain unsupported. Fresh installations still use the installer or
diagnostics endpoint for their initial qualification.

## Upcoming music

The apparent random refresh came from a five-minute Plex timer in `Music.Tick`.
It called `Configure`, canceled the preparation worker and closed every queued
decoder. The replacement worker shuffled a new list even though the user had
not skipped or changed sources. The current song continued, but Next up changed.

That timer has been removed. The existing worker refreshes Plex catalog data when
it needs another song after five minutes, while already prepared songs retain
their order and decoders. Explicit Refresh and source or shuffle changes retain
their existing queue-rebuild behavior.

## Regression coverage

Tests cover automatic qualification after a fingerprint change, saved results,
rejection of publishing and duplicate jobs during qualification, reuse of an
unchanged qualification, and retention of the prepared Plex queue across ticks.
Tests use controlled qualification results; they do not rerun the production GPU
benchmark or change its qualification.

On Zeus, the full `go test -race ./... -count=1 -timeout=3m` suite and
`go vet ./...` passed. Production PID 8555 remained running with its existing
qualification and Plex music settings throughout the investigation before deployment.

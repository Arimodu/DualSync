#!/usr/bin/env bash
set -euo pipefail
root=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd -P)
cd "$root"
version=${1:-0.4.0}
[[ $version =~ ^[0-9]+\.[0-9]+\.[0-9]+$ ]] || { echo 'Version must be numeric major.minor.patch.' >&2; exit 1; }
mkdir -p dist
stage=$(mktemp -d)
trap 'rm -rf -- "$stage"' EXIT
mkdir -p "$stage/dualsync/bin"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -o "$stage/dualsync/bin/dualsync" ./cmd/dualsync
cp -a webroot assets api "$stage/dualsync/"
cp install.sh dualsync.service.in config.example.json README.md "$stage/dualsync/"
chmod 755 "$stage/dualsync/install.sh" "$stage/dualsync/bin/dualsync"
tar -czf "dist/dualsync-$version-linux-amd64.tar.gz" -C "$stage" dualsync
sha256sum "dist/dualsync-$version-linux-amd64.tar.gz" > "dist/dualsync-$version-linux-amd64.tar.gz.sha256"

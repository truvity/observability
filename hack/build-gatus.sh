#!/usr/bin/env bash
# Builds the Gatus binaries the EC2 status box installs, from the upstream tag
# pkg/statusbox/ec2/pins.go pins as GatusVersion, and writes them to
# build/gatus/gatus_<tag>_linux_<arch> for the release to attach (see
# .goreleaser.yaml).
#
# Run by goreleaser's `before` hooks. Needs git and a Go toolchain new enough
# for Gatus's go.mod (the release job runs under devbox, which has one).
#
#   hack/build-gatus.sh [outdir]
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
out="${1:-$root/build/gatus}"

tag="$(sed -n 's/^[[:space:]]*GatusVersion = "\(v[0-9][0-9.]*\)"$/\1/p' "$root/pkg/statusbox/ec2/pins.go")"
[ -n "$tag" ] || { echo "build-gatus.sh: no GatusVersion in pkg/statusbox/ec2/pins.go" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

echo "build-gatus.sh: building Gatus $tag" >&2
git clone --quiet --depth 1 --branch "$tag" https://github.com/TwiN/gatus "$work/gatus"

mkdir -p "$out"
for arch in arm64 amd64; do
  # CGO off: Gatus uses the pure-Go SQLite driver, so the binary is static and
  # runs on any Linux of the architecture.
  (cd "$work/gatus" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags "-s -w" -o "$out/gatus_${tag}_linux_${arch}" .)
done

ls -l "$out" >&2

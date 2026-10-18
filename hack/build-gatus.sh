#!/usr/bin/env bash
# Builds the Gatus binaries the EC2 status box installs, from the repository
# and the pinned commit SHA pkg/statusbox/ec2/pins.go holds as GatusRepo and
# GatusRef, and writes them to build/gatus/gatus_<GatusVersion>_linux_<arch>
# for the release to attach (see .goreleaser.yaml).
#
# Temporary fork, D698/D716: GatusRepo is github.com/truvity/gatus (upstream
# v5.37.0 + opt-in security.oidc.pkce). Return to upstream when it releases PKCE.
# The ref is a commit SHA, not a moving branch, and the build refuses to run
# unless the checked-out commit is exactly that SHA.
#
# Run by goreleaser's `before` hooks. Needs git and a Go toolchain new enough
# for Gatus's go.mod (the release job runs under devbox, which has one).
#
#   hack/build-gatus.sh [outdir]
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
out="${1:-$root/build/gatus}"

pins="$root/pkg/statusbox/ec2/pins.go"
pin() {
  local v
  v="$(sed -n "s/^[[:space:]]*$1 *= \"\([^\"]*\)\"\$/\1/p" "$pins")"
  [ -n "$v" ] || { echo "build-gatus.sh: no $1 in pkg/statusbox/ec2/pins.go" >&2; exit 1; }
  printf '%s' "$v"
}
tag="$(pin GatusVersion)"
repo="$(pin GatusRepo)"
ref="$(pin GatusRef)"
case "$tag" in v[0-9]*) ;; *) echo "build-gatus.sh: GatusVersion $tag is not a version tag" >&2; exit 1 ;; esac
case "$ref" in
  *[!0-9a-f]* | "") echo "build-gatus.sh: GatusRef must be a full commit SHA, got $ref" >&2; exit 1 ;;
esac
[ "${#ref}" -eq 40 ] || { echo "build-gatus.sh: GatusRef must be a full 40-hex commit SHA, got $ref" >&2; exit 1; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

echo "build-gatus.sh: building Gatus $tag from $repo @ $ref" >&2
git init --quiet "$work/gatus"
git -C "$work/gatus" fetch --quiet --depth 1 "$repo" "$ref"
git -C "$work/gatus" checkout --quiet --detach FETCH_HEAD
head="$(git -C "$work/gatus" rev-parse HEAD)"
[ "$head" = "$ref" ] || { echo "build-gatus.sh: checked out $head, expected the pinned $ref" >&2; exit 1; }

mkdir -p "$out"
for arch in arm64 amd64; do
  # CGO off: Gatus uses the pure-Go SQLite driver, so the binary is static and
  # runs on any Linux of the architecture.
  (cd "$work/gatus" && CGO_ENABLED=0 GOOS=linux GOARCH="$arch" go build -trimpath -ldflags "-s -w" -o "$out/gatus_${tag}_linux_${arch}" .)
done

ls -l "$out" >&2

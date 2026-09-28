#!/usr/bin/env bash
# Runs setup.sh's install_container_runtime function ALONE, inside a
# plain `debian:12` container — the actual OS the Lightsail blueprint
# boots (see docs/statusbox.md's "Immutable, by construction" and the
# blueprint it names), not the Ubuntu runner hack/statusbox-ci.sh's own
# job happens to run on.
#
# WHY THIS EXISTS: hack/statusbox-ci.sh runs the WHOLE, unmodified
# setup.sh against a fixture, which is exactly right for everything it
# tests — except the one step it cannot: which apt packages exist on the
# distribution the box actually runs. An Ubuntu runner accepts
# `docker-compose-v2`; Debian 12 does not, and setup.sh used to ask for
# it anyway, failing with exit 100 under `set -euo pipefail` before
# tailscale was ever set up (see CHANGELOG 0.7.4). That gap survived
# every existing check because nothing in CI had ever run this function
# on Debian. This does.
#
# This does NOT run all of setup.sh: no manifest is staged, nothing
# joins a tailnet, no compose file is written, no systemd unit exists in
# the container to enable anything against. It proves exactly one thing,
# cheaply, on a plain debian:12 image, on every PR: the packages
# install_container_runtime asks apt for actually exist on Debian, and
# `docker compose version` works afterwards as a CLI. The daemon is
# never started and does not need to be — see setup.sh's own guarded
# dispatch (`setup.sh <function>`) for how this calls the function
# alone, and install_container_runtime's own tail for why it tolerates
# a systemd-less container.
#
# Needs Docker, to run the debian:12 container itself — the same
# requirement `just apply`/`just reconcile`/`just statusbox` already
# have; deliberately NOT part of `check` for the same reason.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"

echo "hack/statusbox-debian-ci.sh: running install_container_runtime inside debian:12"
docker run --rm \
  --pull=always \
  -v "$root/setup.sh:/setup.sh:ro" \
  debian:12 \
  bash -c '
    set -euo pipefail
    /setup.sh install_container_runtime
    echo "--- docker compose version ---"
    docker compose version
  '

echo "hack/statusbox-debian-ci.sh: docker + the compose plugin install cleanly on debian:12, OK"

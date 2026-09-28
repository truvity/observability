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

# select_data_disk_device (setup.sh, Bug B's device-identification half)
# is a pure function of `lsblk -J -b -o NAME,TYPE,MOUNTPOINT` piped on
# stdin — see its own doc comment — precisely so it can be exercised
# here against fixed fixture trees instead of a real disk, which this
# container (like the one above) does not have and cannot easily fake. A
# real box's disk shows up as either an xvdf-shaped device (older
# bundles) or an nvme-shaped one (current-generation bundles) — see
# pkg/statusbox/lightsail's diskPath doc comment — so both are fixtured
# here, alongside the "not yet attached" and "more than one candidate"
# cases wait_for_data_disk has to tell apart (see that function's own
# doc comment for why they are handled differently: one is retried, the
# other fails immediately).
echo "hack/statusbox-debian-ci.sh: running select_data_disk_device fixtures inside debian:12"
docker run --rm \
  --pull=always \
  -v "$root/setup.sh:/setup.sh:ro" \
  debian:12 \
  bash -c '
    set -euo pipefail
    apt-get update -y >/dev/null
    apt-get install -y --no-install-recommends jq >/dev/null

    check_device() {
      local desc="$1" want="$2" got
      shift 2
      got="$("$@")"
      if [ "$got" != "$want" ]; then
        echo "FAIL: $desc: expected \"$want\", got \"$got\"" >&2
        exit 1
      fi
      echo "OK: $desc -> $got"
    }

    check_exit_code() {
      local desc="$1" want="$2" rc=0
      shift 2
      "$@" >/dev/null 2>&1 || rc=$?
      if [ "$rc" != "$want" ]; then
        echo "FAIL: $desc: expected exit $want, got $rc" >&2
        exit 1
      fi
      echo "OK: $desc -> exit $want"
    }

    # An older-generation bundle: root on a partitioned /dev/xvda, the
    # attached data disk surfaced as the whole, unpartitioned /dev/xvdf,
    # exactly the shape pkg/statusbox/lightsail diskPath names.
    xvdf_json="$(cat <<JSON
{"blockdevices":[
  {"name":"xvda","type":"disk","mountpoint":null,"children":[
    {"name":"xvda1","type":"part","mountpoint":"/boot"},
    {"name":"xvda2","type":"part","mountpoint":"/"}
  ]},
  {"name":"xvdf","type":"disk","mountpoint":null}
]}
JSON
)"
    echo "$xvdf_json" | check_device "xvdf case" "/dev/xvdf" /setup.sh select_data_disk_device

    # A current-generation bundle: the diskPath Lightsail was configured
    # with (/dev/xvdf) never appears at all — the kernel names both
    # disks as NVMe devices instead, the exact substitution this
    # function exists to see past.
    nvme_json="$(cat <<JSON
{"blockdevices":[
  {"name":"nvme0n1","type":"disk","mountpoint":null,"children":[
    {"name":"nvme0n1p1","type":"part","mountpoint":"/boot/efi"},
    {"name":"nvme0n1p2","type":"part","mountpoint":"/"}
  ]},
  {"name":"nvme1n1","type":"disk","mountpoint":null}
]}
JSON
)"
    echo "$nvme_json" | check_device "nvme case" "/dev/nvme1n1" /setup.sh select_data_disk_device

    # Freshly booted, before Bug A own ordering hot-attaches the disk a
    # few minutes in: no candidate yet. Retryable, see wait_for_data_disk,
    # so exit 1, not a hard failure.
    not_yet_json="$(cat <<JSON
{"blockdevices":[
  {"name":"nvme0n1","type":"disk","mountpoint":null,"children":[
    {"name":"nvme0n1p1","type":"part","mountpoint":"/"}
  ]}
]}
JSON
)"
    echo "$not_yet_json" | check_exit_code "not-yet-attached case" 1 /setup.sh select_data_disk_device

    # More than one candidate: this package never attaches more than one
    # extra disk, so this means something wait_for_data_disk cannot
    # understand is attached. Not retryable, exit 2, because guessing
    # wrong means mkfs on the wrong disk.
    ambiguous_json="$(cat <<JSON
{"blockdevices":[
  {"name":"nvme0n1","type":"disk","mountpoint":null,"children":[
    {"name":"nvme0n1p1","type":"part","mountpoint":"/"}
  ]},
  {"name":"nvme1n1","type":"disk","mountpoint":null},
  {"name":"nvme2n1","type":"disk","mountpoint":null}
]}
JSON
)"
    echo "$ambiguous_json" | check_exit_code "ambiguous case" 2 /setup.sh select_data_disk_device
  '

echo "hack/statusbox-debian-ci.sh: select_data_disk_device identifies the right disk on both bundle shapes, OK"

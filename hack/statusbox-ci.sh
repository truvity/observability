#!/usr/bin/env bash
# Runs setup.sh — the release asset pkg/statusbox.CloudInit tells a box to
# fetch, verify by checksum, and execute — against a small fixture, on
# whatever host this script is given, and requires every instance to
# answer /health.
#
# WHY THIS EXISTS, stated in the imperative because it is the whole
# reason for the file: the first run of this script must not be on the
# box, on a bad day. setup.sh installs a container runtime, joins a
# private network, installs a tunnel daemon and writes a systemd unit —
# every one of those is a step that works on the author's laptop and
# fails on a fresh image for a reason nobody anticipated, and a watcher
# that fails to boot is the one failure this repository has no second
# vantage to catch. So this exercises the UNMODIFIED release asset, not a
# rewritten or parameterised stand-in, on a plain Ubuntu machine — the
# closest free approximation to the Debian box it really targets that a
# GitHub-hosted runner offers.
#
# What this does NOT exercise, named so nobody assumes it was covered:
# tailscale, `tailscale serve` and cloudflared are all skipped when
# TS_AUTHKEY / TUNNEL_TOKEN are unset (see setup.sh's own
# setup_tailscale/serve_private_instances/setup_cloudflared), because a
# CI job has no tailnet to join and no tunnel to authenticate to — a real
# key or token would either fail outside the estate's tailnet/account or
# require one to be minted for CI to burn. What IS exercised is
# everything else: the container runtime, unpacking the staged instance
# configs, setup_trusted_cas unpacking a staged extra-CA bundle and
# write_compose wiring it into every service (SSL_CERT_DIR and the
# read-only mount — NOT whether Gatus's own TLS stack actually trusts
# it, which needs a real HTTPS server and a real Gatus container; see
# hack/statusbox-ca-proof.sh for that), the compose file, the systemd
# unit, and every instance actually answering its own HTTP endpoint once
# started that way — and,
# since this host is real (unlike hack/statusbox-debian-ci.sh's
# container), the real /data pipeline too: setup.sh's own
# STATUSBOX_DATA_DISK_OVERRIDE test seam (see wait_for_data_disk's doc
# comment) points it at a loop device backed by a plain file below,
# standing in for the disk pkg/statusbox/lightsail attaches on a real
# box, so mount_data_disk's actual mkfs/fstab/mount runs for real here.
# Device SELECTION — telling that disk apart from the root one by shape
# alone — is exercised separately, against fixture lsblk JSON, by
# hack/statusbox-debian-ci.sh; this script's job is everything selection
# is not: format-if-empty, the fstab entry, the mount, and the systemd
# unit's RequiresMountsFor.
#
#   hack/statusbox-ci.sh        run the fixture, tear down
#   KEEP=1 hack/statusbox-ci.sh leave the containers and unit running
set -euo pipefail

# setup.sh runs as root on a real box — cloud-init is root already — and
# does the same here: apt-get, systemctl and /opt/statusbox all need it.
# A hosted CI runner's default user is not root but does have
# passwordless sudo, so this re-execs itself under sudo rather than
# asking the Justfile recipe or the CI workflow to know that; `-E` keeps
# KEEP visible to the re-exec.
if [ "$(id -u)" != 0 ]; then
  exec sudo -E "$0" "$@"
fi

root="$(cd "$(dirname "$0")/.." && pwd)"
keep="${KEEP:-0}"
work="$(mktemp -d -t statusbox-ci.XXXXXX)"

# The stand-in for the attached data disk: a 128MB file, loop-attached,
# never formatted here — setup.sh's own mount_data_disk has to be the
# one that finds it empty (via blkid) and runs mkfs.ext4, or this would
# not be testing that check at all.
loop_backing="$work/data-disk.img"
truncate -s 128M "$loop_backing"
loop_device="$(losetup -f --show "$loop_backing")"
echo "hack/statusbox-ci.sh: standing in for the attached disk with $loop_device ($loop_backing)"

cleanup() {
  if [ "$keep" != 1 ]; then
    systemctl stop statusbox >/dev/null 2>&1 || true
    systemctl disable statusbox >/dev/null 2>&1 || true
    rm -f /etc/systemd/system/statusbox.service
    systemctl daemon-reload >/dev/null 2>&1 || true
    umount /data >/dev/null 2>&1 || true
    sed -i '/^LABEL=statusbox-data /d' /etc/fstab 2>/dev/null || true
    losetup -d "$loop_device" >/dev/null 2>&1 || true
    rm -rf /opt/statusbox /data
  fi
  rm -rf "$work"
}
trap cleanup EXIT

for tool in docker gzip base64 losetup mkfs.ext4 blkid findmnt openssl diff; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "hack/statusbox-ci.sh needs $tool, which is not on PATH" >&2
    exit 1
  }
done

# The fixture: two instances, the same shape pkg/statusbox stages onto a
# real box — a manifest and one gzip+base64 blob per instance's Config —
# built by hand here instead of through pkg/statusbox.CloudInit, because
# this script's job is setup.sh's OWN logic (the unpacking, the compose
# file, the systemd unit), not the Pulumi renderer upstream of it; that
# renderer already has its own golden test.
#
# Each Config carries both an `endpoints:` entry and an
# `external-endpoints:` entry with a token: Gatus refuses to start on a
# configuration with external-endpoints alone ("should contain at least
# one endpoint or suite"), and refuses an external-endpoint with no
# token — both learned by actually running this fixture through Gatus
# rather than assumed, which is exactly what this script exists to keep
# true.
mkdir -p "$work/staged"

for name in example-co ops; do
  cat > "$work/$name.yaml" <<EOF
endpoints:
  - name: ${name}-self-check
    url: "tcp://127.0.0.1:8080"
    interval: 30s
    conditions:
      - "[CONNECTED] == true"
external-endpoints:
  - name: ${name}-heartbeat
    group: ${name}
    token: "test-heartbeat-token"
    heartbeat:
      interval: 5m
EOF
  gzip -c "$work/$name.yaml" | base64 -w0 > "$work/staged/$name.yaml.gz.b64"
done

# A throwaway CA — never anything real, generated fresh for this run and
# discarded with $work — stands in for statusbox.Args.TrustedCAs: this
# proves setup_trusted_cas and write_compose end to end (the file lands
# under $trusted_ca_dir, the compose file mounts it and sets
# SSL_CERT_DIR), the same way the rest of this fixture proves setup.sh's
# OTHER staged artefacts. It is NOT what proves Gatus's own TLS
# verification actually trusts it or that a public probe still works —
# that needs a real HTTPS server and a real Gatus container, which is
# exactly what hack/statusbox-ca-proof.sh exists to run in Docker.
openssl req -x509 -newkey ec -pkeyopt ec_paramgen_curve:P-256 -nodes \
  -keyout "$work/ca.key" -out "$work/ca.pem" -days 1 \
  -subj "/CN=statusbox-ci-test-root" >/dev/null 2>&1
gzip -c "$work/ca.pem" | base64 -w0 > "$work/staged/trusted-cas.pem.gz.b64"

cat > "$work/staged/manifest.json" <<'EOF'
{"instances":[{"name":"example-co","port":18081,"public":true},{"name":"ops","port":18084,"public":false}],"trustedCAs":true}
EOF

echo "staging fixture into /opt/statusbox"
mkdir -p /opt/statusbox/staged
cp "$work/staged/"* /opt/statusbox/staged/

echo "running setup.sh (unmodified, from this checkout)"
# A STATUSBOX_ENV_ variable exercises write_env's OTHER branch (see
# statusbox.Secrets.Env): the same .env file ALERT_URL_* lands in, under
# the name a Config would reference verbatim (no prefix), proving the
# prefix strip actually happens rather than assumed from the Go-level
# unit tests alone.
STATUSBOX_ENV_STATUSBOX_CI_ENV_TEST="ci-env-test-value" \
  STATUSBOX_DATA_DISK_OVERRIDE="$loop_device" \
  "$root/setup.sh"

if ! grep -qx 'STATUSBOX_CI_ENV_TEST=ci-env-test-value' /opt/statusbox/.env; then
  echo "setup.sh's write_env did not carry STATUSBOX_ENV_STATUSBOX_CI_ENV_TEST into /opt/statusbox/.env as STATUSBOX_CI_ENV_TEST" >&2
  cat /opt/statusbox/.env >&2 || true
  exit 1
fi
echo "write_env: STATUSBOX_ENV_* carried into .env under its stripped name, OK"

# write_compose's own explicit `dns: [100.100.100.100]` is what makes a
# container's own DNS resolution deterministic regardless of which kind
# of bridge network compose creates or which DNS-manager mode tailscaled
# picked on the host (see write_compose's doc comment) — TS_AUTHKEY is
# unset in this fixture (see the header above), so setup_tailscale itself
# never runs here, but write_compose runs unconditionally and this is the
# one place that can prove its OUTPUT still carries the line every real
# box depends on.
echo "checking write_compose carries the tailnet resolver into every instance"
if [ "$(grep -c '^\s*- 100\.100\.100\.100$' /opt/statusbox/docker-compose.yml)" != 2 ]; then
  echo "expected exactly two services (one per fixture instance) with dns: [100.100.100.100] in /opt/statusbox/docker-compose.yml" >&2
  cat /opt/statusbox/docker-compose.yml >&2
  exit 1
fi
echo "write_compose: every instance's dns: points at the tailnet resolver, OK"

echo "checking setup_trusted_cas unpacked the staged bundle"
if [ ! -f /opt/statusbox/ca/extra-roots.pem ]; then
  echo "expected setup_trusted_cas to unpack /opt/statusbox/staged/trusted-cas.pem.gz.b64 into /opt/statusbox/ca/extra-roots.pem" >&2
  exit 1
fi
if ! diff -q "$work/ca.pem" /opt/statusbox/ca/extra-roots.pem >/dev/null; then
  echo "/opt/statusbox/ca/extra-roots.pem does not match the staged fixture CA" >&2
  exit 1
fi
echo "setup_trusted_cas: extra-roots.pem unpacked and matches the staged fixture, OK"

echo "checking write_compose mounts the trusted-CA directory and sets SSL_CERT_DIR"
if [ "$(grep -c '^\s*SSL_CERT_DIR: /etc/ssl/extra-ca$' /opt/statusbox/docker-compose.yml)" != 2 ]; then
  echo "expected exactly two services (one per fixture instance) with SSL_CERT_DIR: /etc/ssl/extra-ca in /opt/statusbox/docker-compose.yml" >&2
  cat /opt/statusbox/docker-compose.yml >&2
  exit 1
fi
if [ "$(grep -c '^\s*- /opt/statusbox/ca:/etc/ssl/extra-ca:ro$' /opt/statusbox/docker-compose.yml)" != 2 ]; then
  echo "expected exactly two services with the /opt/statusbox/ca:/etc/ssl/extra-ca:ro read-only mount in /opt/statusbox/docker-compose.yml" >&2
  cat /opt/statusbox/docker-compose.yml >&2
  exit 1
fi
echo "write_compose: every instance mounts the trusted-CA directory read-only and sets SSL_CERT_DIR, OK"

echo "checking setup_data_disk actually formatted, fstab'd and mounted $loop_device onto /data"
if ! mountpoint -q /data; then
  echo "/data is not a mountpoint after setup.sh ran" >&2
  exit 1
fi
if [ "$(findmnt -no SOURCE /data)" != "$loop_device" ]; then
  echo "/data is mounted, but not from $loop_device: $(findmnt -no SOURCE /data)" >&2
  exit 1
fi
if [ "$(blkid -o value -s LABEL "$loop_device")" != "statusbox-data" ]; then
  echo "$loop_device was not formatted with label statusbox-data" >&2
  blkid "$loop_device" >&2 || true
  exit 1
fi
if ! grep -qx 'LABEL=statusbox-data /data ext4 defaults,nofail 0 2' /etc/fstab; then
  echo "no matching LABEL=statusbox-data fstab entry" >&2
  grep statusbox-data /etc/fstab >&2 || true
  exit 1
fi
if ! grep -qx 'RequiresMountsFor=/data' /etc/systemd/system/statusbox.service; then
  echo "statusbox.service does not carry RequiresMountsFor=/data" >&2
  cat /etc/systemd/system/statusbox.service >&2
  exit 1
fi
echo "setup_data_disk: formatted, fstab'd, mounted; statusbox.service requires it, OK"

echo "checking mount_data_disk never reformats a disk that already has a filesystem"
marker_file=/data/example-co/ci-marker
if [ ! -d /data/example-co ]; then
  echo "expected unpack_instances to have created /data/example-co on the mounted disk" >&2
  exit 1
fi
echo "pre-existing-data marker" > "$marker_file"
"$root/setup.sh" mount_data_disk "$loop_device"
if [ "$(cat "$marker_file")" != "pre-existing-data marker" ]; then
  echo "mount_data_disk re-ran against an already-formatted disk and lost data that was on it" >&2
  exit 1
fi
echo "mount_data_disk: re-run against an already-formatted, already-mounted disk is a no-op, OK"

echo "checking mount_data_disk on the reboot path: unmounted, but already labelled"
umount /data
"$root/setup.sh" mount_data_disk "$loop_device"
if ! mountpoint -q /data; then
  echo "mount_data_disk did not remount an already-formatted, already-fstab'd disk" >&2
  exit 1
fi
if [ "$(cat "$marker_file")" != "pre-existing-data marker" ]; then
  echo "mount_data_disk reformatted a disk that already carried a filesystem, on the reboot path" >&2
  exit 1
fi
echo "mount_data_disk: an unmounted-but-already-labelled disk is just remounted, never reformatted, OK"

echo "waiting for every instance to answer /health"
ports=(18081 18084)
for port in "${ports[@]}"; do
  ok=0
  for _ in $(seq 1 30); do
    if code="$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:${port}/health" 2>/dev/null)" && [ "$code" = "200" ]; then
      ok=1
      break
    fi
    sleep 2
  done
  if [ "$ok" != 1 ]; then
    echo "instance on port $port never answered 200 on /health" >&2
    systemctl status statusbox --no-pager >&2 || true
    docker compose -f /opt/statusbox/docker-compose.yml logs >&2 || true
    exit 1
  fi
  echo "port $port: /health OK"
done

echo "statusbox CI: both instances answered /health"

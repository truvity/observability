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
# tailscale and cloudflared are skipped when TS_AUTHKEY / TUNNEL_TOKEN are
# unset (see setup.sh's own setup_tailscale/setup_cloudflared), because a
# CI job has no tailnet to join and no tunnel to authenticate to — a real
# key or token would either fail outside the estate's tailnet/account or
# require one to be minted for CI to burn. What IS exercised is
# everything else: the container runtime, unpacking the staged instance
# configs, the compose file, the systemd unit, and every instance
# actually answering its own HTTP endpoint once started that way.
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

cleanup() {
  if [ "$keep" != 1 ]; then
    systemctl stop statusbox >/dev/null 2>&1 || true
    systemctl disable statusbox >/dev/null 2>&1 || true
    rm -f /etc/systemd/system/statusbox.service
    systemctl daemon-reload >/dev/null 2>&1 || true
    rm -rf /opt/statusbox /data
  fi
  rm -rf "$work"
}
trap cleanup EXIT

for tool in docker gzip base64; do
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

cat > "$work/staged/manifest.json" <<'EOF'
{"instances":[{"name":"example-co","port":18081,"public":true},{"name":"ops","port":18084,"public":false}]}
EOF

echo "staging fixture into /opt/statusbox"
mkdir -p /opt/statusbox/staged
cp "$work/staged/"* /opt/statusbox/staged/

echo "running setup.sh (unmodified, from this checkout)"
"$root/setup.sh"

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

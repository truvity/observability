#!/usr/bin/env bash
# setup.sh — turns a freshly booted, otherwise empty Debian box into the
# watcher outside: see docs/statusbox.md and docs/doctrine.md ("The
# watcher lives outside").
#
# This is a RELEASE ASSET, not a script anyone runs from a checkout. A
# box's cloud-init (rendered by pkg/statusbox.CloudInit) downloads this
# exact file from this release's assets, checks its sha256 against the
# one CloudInit baked in at deploy time, and only then executes it — see
# CloudInit's doc comment for why that check exists. Nothing in this
# script re-derives or re-checks that hash: verifying its OWN checksum is
# the caller's job, once, before the first line here ever runs.
#
# It knows no hostname. A Public instance's hostname lives in the
# tunnel's ingress rules, which are the consuming estate's own edge
# configuration to own — see statusbox.Args.Hostnames's doc comment. All
# this script promises is that every instance listens on
# 127.0.0.1:<port>, which is the address any ingress rule (owned
# elsewhere, changed independently, never rendered here) can point at.
#
# It knows no secret it did not receive as an already-exported
# environment variable: TS_AUTHKEY, TUNNEL_TOKEN, and any ALERT_URL_*
# CloudInit staged. It reads no SSM parameter, no credential store, no
# instance-role — that would be a second credential the box holds beyond
# the one-shot tailnet key, and docs/doctrine.md is explicit about why a
# watcher should not hold one: it is one more way for the thing it
# watches to take the watcher down. The only inputs this script reads
# from disk are /opt/statusbox/staged/manifest.json and this script's own
# sibling *.yaml.gz.b64 files, all written by cloud-init before this
# script is ever invoked.
#
# IDEMPOTENT: every step below checks before it acts, so re-running this
# script (a re-provision, a manual re-run while debugging) is safe. It is
# not expected to run more than once in the box's life — a configuration
# change replaces the instance rather than re-running this script on it,
# see docs/statusbox.md ("Immutable, by construction") — but "not
# expected to" is not "not tested to": CI runs this exact file, unmodified,
# against a fixture on a plain Ubuntu runner (see .github/workflows/ci.yaml
# and hack/statusbox-ci.sh) specifically so that the first time it really
# runs is not on a box, on a bad day.
set -euo pipefail

# The image is pinned, not `:latest`: a box replaced next month (a
# Version bump, a Config change — see "Immutable, by construction") must
# run the same Gatus an operator already knows the behaviour of, not
# whatever a floating tag resolved to that day. Bumping it is a setup.sh
# change in a new release of this repository, same as any other decision
# here.
gatus_image="twinproduction/gatus:v5.37.0"

root=/opt/statusbox
staged="$root/staged"
instances_dir="$root/instances"
data_root=/data

log() { printf '[setup.sh] %s\n' "$1"; }

require_staged() {
  [ -f "$staged/manifest.json" ] || {
    echo "setup.sh: $staged/manifest.json is missing. This script is a release asset that expects cloud-init to have staged a manifest and one *.yaml.gz.b64 file per instance under $staged before running it — it is not meant to be run against an empty directory." >&2
    exit 1
  }
}

install_container_runtime() {
  local need_apt_update=1

  if command -v docker >/dev/null 2>&1; then
    log "docker already installed, skipping"
  else
    log "installing docker"
    apt-get update -y
    need_apt_update=0
    apt-get install -y --no-install-recommends docker.io
    systemctl enable --now docker
  fi

  # A base image can carry the docker ENGINE without the compose plugin —
  # checked separately, because `command -v docker` above says nothing
  # about it: a docker that cannot run `docker compose` is a docker this
  # script cannot start anything with, and the two packages come from
  # different apt sources on Debian and Ubuntu alike.
  if docker compose version >/dev/null 2>&1; then
    log "docker compose already installed, skipping"
    return
  fi
  log "installing the docker compose plugin"
  [ "$need_apt_update" = 1 ] && apt-get update -y
  apt-get install -y --no-install-recommends docker-compose-v2
}

install_jq() {
  command -v jq >/dev/null 2>&1 && return
  log "installing jq"
  apt-get update -y
  apt-get install -y --no-install-recommends jq
}

# Tailscale joins the estate's private network: SSH, the ops page, and
# the install's push into gatus-ops all travel over it. TS_AUTHKEY is
# one-shot — CloudInit stages it once, and this function is written to
# use it at most once per box for exactly that reason: `tailscale
# status` is checked FIRST, and `tailscale up` never runs a second time
# on a box already joined.
setup_tailscale() {
  if [ -z "${TS_AUTHKEY:-}" ]; then
    log "TS_AUTHKEY is not set, skipping tailscale (expected in a test fixture; a real box always carries one)"
    return
  fi
  if ! command -v tailscale >/dev/null 2>&1; then
    log "installing tailscale"
    curl -fsSL https://tailscale.com/install.sh | sh
  fi
  if tailscale status >/dev/null 2>&1; then
    log "tailscale already joined, skipping 'tailscale up'"
    return
  fi
  log "joining the tailnet"
  tailscale up --authkey="$TS_AUTHKEY" --ssh --accept-dns=false
}

# cloudflared carries every Public instance's ingress rule through one
# tunnel. TUNNEL_TOKEN is only set when at least one instance is Public —
# see statusbox.Args.validate — so a box with nothing public simply never
# installs it.
setup_cloudflared() {
  if [ -z "${TUNNEL_TOKEN:-}" ]; then
    log "TUNNEL_TOKEN is not set, skipping cloudflared (expected when no instance is Public, or in a test fixture)"
    return
  fi
  if ! command -v cloudflared >/dev/null 2>&1; then
    log "installing cloudflared"
    arch="$(dpkg --print-architecture)"
    curl -fsSL -o /usr/local/bin/cloudflared \
      "https://github.com/cloudflare/cloudflared/releases/latest/download/cloudflared-linux-${arch}"
    chmod +x /usr/local/bin/cloudflared
  fi
  if systemctl is-active --quiet cloudflared 2>/dev/null; then
    log "cloudflared service already installed and running, skipping"
    return
  fi
  log "installing the cloudflared service"
  cloudflared service install "$TUNNEL_TOKEN"
}

# unpack_instances turns the staged manifest and *.yaml.gz.b64 blobs
# CloudInit produced into real files: one config.yaml per instance under
# $instances_dir, and one directory per instance under $data_root for its
# SQLite file. This is the "unpacks the instance configs" step
# docs/statusbox.md assigns to setup.sh rather than to cloud-init — see
# CloudInit's own doc comment for why the split is there.
unpack_instances() {
  mkdir -p "$instances_dir"
  local name
  while IFS= read -r name; do
    mkdir -p "$instances_dir/$name" "$data_root/$name"
    base64 -d "$staged/$name.yaml.gz.b64" | gunzip > "$instances_dir/$name/config.yaml"
  done < <(jq -r '.instances[].name' "$staged/manifest.json")
}

# write_compose renders one Gatus service per instance. Each is published
# at 127.0.0.1:<port>, on loopback only — never 0.0.0.0 — because the
# only two things ever meant to reach it are cloudflared and the tailnet,
# both of which run ON this box, and the instance's own
# aws.lightsail.InstancePublicPorts firewall is declared with an empty
# port list regardless (see pkg/statusbox/lightsail), so a loopback bind
# here is defence that does not depend on that firewall being correct.
write_compose() {
  {
    echo "services:"
    jq -c '.instances[]' "$staged/manifest.json" | while IFS= read -r inst; do
      name="$(jq -r '.name' <<<"$inst")"
      port="$(jq -r '.port' <<<"$inst")"
      cat <<COMPOSE
  gatus-${name}:
    image: ${gatus_image}
    container_name: gatus-${name}
    restart: unless-stopped
    ports:
      - "127.0.0.1:${port}:8080"
    environment:
      GATUS_CONFIG_PATH: /config/config.yaml
    env_file:
      - .env
    volumes:
      - ${instances_dir}/${name}/config.yaml:/config/config.yaml:ro
      - ${data_root}/${name}:/data
COMPOSE
    done
  } > "$root/docker-compose.yml"
}

# write_env carries every ALERT_URL_* variable CloudInit staged into the
# containers' own environment, so a Config can reference
# ${ALERT_URL_<KEY>} using Gatus's own environment-variable substitution
# without that value ever being typed into the Config itself — see
# statusbox.Secrets.AlertURLs's doc comment.
#
# It writes a .env file rather than exporting these into the compose
# file's own text: docker compose loads a .env file beside
# docker-compose.yml for `${...}` substitution WITHIN that file, which is
# a different thing from a container's own environment and does nothing
# on its own — every service's `env_file: [.env]` (see write_compose) is
# what actually puts each variable into gatus's process, and it loads
# every ALERT_URL_* key this writes without write_compose having to name
# any of them, since AlertURLs's keys are the estate's own and unknown
# here.
write_env() {
  local env_file="$root/.env"
  : > "$env_file"
  chmod 600 "$env_file"
  while IFS='=' read -r key value; do
    printf '%s=%s\n' "$key" "$value" >> "$env_file"
  done < <(env | grep '^ALERT_URL_' || true)
}

write_systemd_unit() {
  cat > /etc/systemd/system/statusbox.service <<UNIT
[Unit]
Description=statusbox: the watcher outside (Gatus instances)
After=docker.service network-online.target
Requires=docker.service
Wants=network-online.target

[Service]
WorkingDirectory=${root}
ExecStart=/usr/bin/docker compose up --remove-orphans
ExecStop=/usr/bin/docker compose down
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
UNIT
  systemctl daemon-reload
  systemctl enable --now statusbox
}

main() {
  require_staged
  install_container_runtime
  install_jq
  setup_tailscale
  setup_cloudflared
  unpack_instances
  write_compose
  write_env
  write_systemd_unit
  log "done: $(jq -r '.instances | length' "$staged/manifest.json") instance(s) started"
}

main "$@"

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
# It knows no PUBLIC hostname. A Public instance's hostname lives in the
# tunnel's ingress rules, which are the consuming estate's own edge
# configuration to own — see statusbox.Args.Hostnames's doc comment. All
# this script promises is that every instance listens on
# 127.0.0.1:<port>, which is the address any ingress rule (owned
# elsewhere, changed independently, never rendered here) can point at.
# It DOES carry the box's own tailnet device name, TS_HOSTNAME — a
# different fact (statusbox.Args.Hostname's own doc comment): a stable
# name for the box itself, not a page it serves.
#
# It knows no secret it did not receive as an already-exported
# environment variable: TS_AUTHKEY, TUNNEL_TOKEN, any ALERT_URL_*, and
# any STATUSBOX_ENV_* CloudInit staged. It reads no SSM parameter, no
# credential store, no instance-role — that would be a second credential
# the box holds beyond the one-shot tailnet key, and docs/doctrine.md is
# explicit about why a watcher should not hold one: it is one more way
# for the thing it watches to take the watcher down. The only inputs
# this script reads
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
  # Both docker itself and the compose plugin, in one check: a docker
  # that cannot run `docker compose` is a docker this script cannot start
  # anything with, so there is nothing worth doing with docker alone.
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    log "docker and the compose plugin already installed, skipping"
    return
  fi

  # Docker's OWN apt repository, not the distribution's docker.io: the
  # package that actually ships a compose plugin on Debian is
  # docker-compose-plugin, from this repository — docker-compose-v2 (what
  # used to be installed here) is not a Debian package at all, only an
  # Ubuntu one, so this used to fail with exit 100 on every real box (see
  # CHANGELOG 0.7.4). The blueprint this repository targets is Debian
  # only (see docs/statusbox.md), so there is deliberately no
  # distribution branch here — `$VERSION_CODENAME` from /etc/os-release
  # is Debian's own codename (`bookworm` for 12), which is all the
  # repository line below needs to be correct on whichever Debian release
  # the box actually boots.
  log "installing docker from Docker's own apt repository"
  apt-get update -y
  # A minimal image (this script's own debian:12 CI check included, see
  # hack/statusbox-debian-ci.sh) carries none of these: curl to fetch the
  # signing key, gnupg for the keyring tooling underneath it, and
  # ca-certificates so that fetch itself is verified.
  apt-get install -y --no-install-recommends ca-certificates curl gnupg

  install -m 0755 -d /etc/apt/keyrings
  curl -fsSL https://download.docker.com/linux/debian/gpg -o /etc/apt/keyrings/docker.asc
  chmod a+r /etc/apt/keyrings/docker.asc

  # shellcheck disable=SC1091
  . /etc/os-release
  echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/debian ${VERSION_CODENAME} stable" \
    > /etc/apt/sources.list.d/docker.list

  apt-get update -y
  apt-get install -y --no-install-recommends docker-ce docker-ce-cli containerd.io docker-compose-plugin

  # Best-effort: a minimal container this function is exercised against
  # (see hack/statusbox-debian-ci.sh) has no systemd and no `docker`
  # daemon to start at all — proving the packages installed and that
  # `docker compose version` works as a CLI is that check's whole job,
  # and it does not need the daemon running to do it. A real box always
  # has systemd, and this is where the daemon actually starts on one.
  systemctl enable --now docker >/dev/null 2>&1 || true
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
  tailscale up --authkey="$TS_AUTHKEY" --hostname="${TS_HOSTNAME:-statusbox}" --ssh --accept-dns=false
}

# serve_private_instances is how the ONE private instance (Public: false
# in the manifest — gatus-ops, and any other instance meant to be reached
# only over the private network) actually becomes reachable from the
# tailnet, not just joined to it. Before this function existed,
# `setup_tailscale` put the box on the tailnet but every Gatus container
# still only listened on 127.0.0.1 (see write_compose) — nothing
# forwarded a tailnet peer's connection to that loopback port, so the
# install's Alertmanager had no path to gatus-ops's external-endpoint API
# for the deadman push described in docs/statusbox.md ("internal →
# status"), despite the box appearing joined and healthy.
#
# The tailnet-side port is always 80, not the instance's own Port: an
# operator then loads the private page at plain `http://<Hostname>/` —
# no port to remember or paste — the same way MagicDNS already lets them
# reach the box by name alone. Plain HTTP, not `--https`, is deliberate
# here too: the tailnet is WireGuard-encrypted end to end, so a second TLS
# termination in front of a page nothing outside the tailnet can even
# address buys nothing.
#
# Serving on 80 is only safe because AT MOST ONE instance is ever
# Public: false — pkg/statusbox.Args.validate refuses a manifest with two
# or more private instances precisely because they cannot both claim
# port 80 on this box. This loop still walks every instance rather than
# assuming which one is private, so it keeps working unchanged if that
# refusal is ever loosened to name the private instance explicitly
# instead of counting it.
#
# `tailscale serve --tcp` registers a forward inside tailscaled's own
# userspace networking: it does not open a second host socket and so
# never competes with write_compose's `127.0.0.1:<port>` bind, which is
# why binding tailnet-side 80 here never collides with any instance's own
# loopback Port, whatever that Port is.
#
# A Public instance (a company's status page) is reached through
# cloudflared alone and is never registered here — the smallest tailnet
# surface this box can have is none of the public pages on it at all.
#
# WHO on the tailnet may then reach the forwarded port is an ACL
# decision, not one this script makes: see the estate's own tailnet
# policy (outside this repository) for the grant that lets only the
# install's egress identity reach `tag:statusbox` on port 80, and nothing
# else.
serve_private_instances() {
  if [ -z "${TS_AUTHKEY:-}" ]; then
    log "TS_AUTHKEY is not set, skipping tailscale serve (expected in a test fixture; a real box always joins the tailnet)"
    return
  fi
  local inst name port public
  while IFS= read -r inst; do
    name="$(jq -r '.name' <<<"$inst")"
    port="$(jq -r '.port' <<<"$inst")"
    public="$(jq -r '.public' <<<"$inst")"
    [ "$public" = "true" ] && continue
    log "serving ${name} to the tailnet on :80 (tcp forward to 127.0.0.1:${port})"
    tailscale serve --bg --tcp=80 "tcp://127.0.0.1:${port}"
  done < <(jq -c '.instances[]' "$staged/manifest.json")
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

# write_env carries every ALERT_URL_* variable, and every STATUSBOX_ENV_*
# variable, CloudInit staged into the containers' own environment, so a
# Config can reference ${ALERT_URL_<KEY>} or ${<NAME>} using Gatus's own
# environment-variable substitution without that value ever being typed
# into the Config itself — see statusbox.Secrets.AlertURLs's and
# statusbox.Secrets.Env's doc comments.
#
# It writes a .env file rather than exporting these into the compose
# file's own text: docker compose loads a .env file beside
# docker-compose.yml for `${...}` substitution WITHIN that file, which is
# a different thing from a container's own environment and does nothing
# on its own — every service's `env_file: [.env]` (see write_compose) is
# what actually puts each variable into gatus's process, and it loads
# every key this writes without write_compose having to name any of
# them, since both AlertURLs's and Env's keys are the estate's own and
# unknown here.
#
# A STATUSBOX_ENV_<NAME> variable is staged, not <NAME> itself (see
# statusbox.render): this loop tells "a Secrets.Env entry CloudInit
# staged for a container" apart from every OTHER environment variable
# already present in THIS script's own shell (PATH, HOME,
# STATUSBOX_VERSION, ...) by that prefix alone, so it has to strip the
# prefix back off before writing the real name — the one a Config
# actually references — into .env.
write_env() {
  local env_file="$root/.env"
  : > "$env_file"
  chmod 600 "$env_file"
  while IFS='=' read -r key value; do
    printf '%s=%s\n' "$key" "$value" >> "$env_file"
  done < <(env | grep '^ALERT_URL_' || true)
  while IFS='=' read -r key value; do
    printf '%s=%s\n' "${key#STATUSBOX_ENV_}" "$value" >> "$env_file"
  done < <(env | grep '^STATUSBOX_ENV_' || true)
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
  serve_private_instances
  setup_cloudflared
  unpack_instances
  write_compose
  write_env
  write_systemd_unit
  log "done: $(jq -r '.instances | length' "$staged/manifest.json") instance(s) started"
}

# Called with one argument naming a function defined above, that
# function runs ALONE and setup.sh exits — nothing else here runs, no
# manifest is required. This is how hack/statusbox-debian-ci.sh proves
# install_container_runtime against a plain debian:12 container by
# itself, without staging a fixture or joining anything: the same gap
# that let `docker-compose-v2` (Ubuntu-only) reach this function
# unnoticed was hack/statusbox-ci.sh only ever running the WHOLE script
# on an Ubuntu runner (see CHANGELOG 0.7.4). Cloud-init always invokes
# this script with no arguments (see pkg/statusbox.render), so this
# branch never fires on a real box.
if [ $# -gt 0 ] && declare -F "$1" >/dev/null 2>&1; then
  "$@"
  exit 0
fi

main "$@"

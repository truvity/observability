#!/usr/bin/env bash
# statusbox-setup: turns an Amazon Linux 2023 instance into the watcher outside
# (the EC2 backend; see docs/statusbox.md, "EC2 backend").
#
# This file is embedded in pkg/statusbox/ec2 and written to
# /usr/local/sbin/statusbox-setup by the instance's user-data. The user-data
# also writes /etc/statusbox/params.sh (everything this script needs to know:
# names, ports, pinned download URLs and checksums, SSM parameter NAMES, and
# the S3 key + sha256 of every Gatus configuration). The configurations are
# S3 objects, not user-data (16 KiB does not hold an estate's catalogue); the
# install phase fetches each with the instance role and refuses a digest that
# differs. No secret is ever in either: secrets are read from SSM Parameter
# Store through the instance role, at boot, into /run (tmpfs), and never
# written to the root disk.
#
# Two phases:
#
#   install  first boot only (the user-data runs it): swap, journald cap,
#            the pinned binaries (checksum verified), the Gatus configurations
#            (fetched from S3, digest verified; a failure abandons the
#            launch, so a bad configuration never serves), the unit files.
#            Then it starts statusbox-boot.service, which runs the second
#            phase. A warm-pool instance does all of this at its first boot
#            too.
#   boot     EVERY boot (statusbox-boot.service): decides from the Auto
#            Scaling group's target lifecycle state whether this instance is
#            going in service. A warm-pool instance (Warmed:*) only completes
#            its lifecycle hook and stays quiet; nothing is restored and no
#            Gatus runs. An instance going InService reads the secrets,
#            restores each SQLite database from the Litestream replica, starts
#            Gatus under `litestream replicate`, starts cloudflared, completes
#            the hook and turns on the health timer (and, when a ping URL
#            parameter is configured, the dead-man ping timer).
#
# That ordering is what keeps the replica single-writer: the replica is only
# ever written by the one InService instance, and a warm instance never
# restores or replicates.
#
# STATUSBOX_ROOT prefixes every path this script writes, so a container can
# run the file-producing functions without touching its own /etc.
set -euo pipefail

ROOT="${STATUSBOX_ROOT:-}"
PARAMS="$ROOT/etc/statusbox/params.sh"
STAGED="$ROOT/opt/statusbox/staged"
ETC="$ROOT/etc/statusbox"
LIB="$ROOT/var/lib/statusbox"
RUN="$ROOT/run/statusbox"
BIN="$ROOT/usr/local/bin"
SBIN="$ROOT/usr/local/sbin"
UNITS="$ROOT/etc/systemd/system"
SVC_USER=statusbox

log() { printf '%s statusbox-setup: %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$*" >&2; }

load_params() {
  # shellcheck disable=SC1090
  . "$PARAMS"
}

# ---------------------------------------------------------------- IMDS (v2)

imds_token() {
  curl -fsS -m 5 -X PUT "http://169.254.169.254/latest/api/token" -H "X-aws-ec2-metadata-token-ttl-seconds: 300"
}

imds() {
  curl -fsS -m 5 -H "X-aws-ec2-metadata-token: $(imds_token)" "http://169.254.169.254/latest/$1"
}

# target_state prints the Auto Scaling group's target lifecycle state for this
# instance: InService, Warmed:Stopped, ... An empty answer (no such path) means
# the instance is not in a group at all, which a cold start under test is.
target_state() {
  imds meta-data/autoscaling/target-lifecycle-state 2>/dev/null || true
}

complete_hook() {
  local result="$1" iid region
  iid="$(imds meta-data/instance-id)"
  region="$(imds meta-data/placement/region)"
  aws autoscaling complete-lifecycle-action \
    --lifecycle-hook-name "$SB_HOOK_NAME" \
    --auto-scaling-group-name "$SB_ASG_NAME" \
    --instance-id "$iid" \
    --lifecycle-action-result "$result" \
    --region "$region" || log "complete-lifecycle-action ($result) failed; the hook times out by itself"
}

# ------------------------------------------------------------------ install

setup_swap() {
  local f="$ROOT/swapfile" free
  if swapon --show=NAME --noheadings 2>/dev/null | grep -qx "/swapfile"; then
    log "swap: already active"
    return 0
  fi
  free="$(df --output=avail -BM "$ROOT/" | tail -n 1 | tr -dc '0-9')"
  if [ -z "$free" ] || [ "$free" -lt 2048 ]; then
    log "swap: skipped, ${free:-unknown} MiB free"
    return 0
  fi
  rm -f "$f"
  if ! { fallocate -l 1024M "$f" 2>/dev/null || dd if=/dev/zero of="$f" bs=1M count=1024 status=none; } || ! chmod 0600 "$f" || ! mkswap "$f" >/dev/null || ! swapon "$f"; then
    log "swap: could not create $f, continuing without"
    rm -f "$f"
    return 0
  fi
  grep -qs '^/swapfile[[:space:]]' "$ROOT/etc/fstab" || echo "/swapfile none swap sw 0 0" >>"$ROOT/etc/fstab"
  log "swap: 1024 MiB active"
}

setup_journald() {
  mkdir -p "$ROOT/etc/systemd/journald.conf.d"
  cat >"$ROOT/etc/systemd/journald.conf.d/99-statusbox.conf" <<'EOF'
[Journal]
Storage=persistent
SystemMaxUse=100M
EOF
  systemctl restart systemd-journald 2>/dev/null || true
}

# fetch_verify URL DEST SHA256: download, then refuse anything whose digest is
# not the pinned one. The digest comes from this repository's own release
# checksums (Gatus) or is pinned in the Go package (Litestream, cloudflared).
fetch_verify() {
  local url="$1" dest="$2" want="$3" i
  for i in 1 2 3 4 5; do
    if curl -fsSL -o "$dest.part" "$url" && printf '%s  %s\n' "$want" "$dest.part" | sha256sum -c - >/dev/null 2>&1; then
      mv "$dest.part" "$dest"
      return 0
    fi
    log "download of $url failed or did not match its checksum (attempt $i)"
    rm -f "$dest.part"
    sleep 5
  done
  return 1
}

# fetch_configs downloads every SB_CONFIGS entry (name:sha256:key) from the
# bucket with the instance role and stages it as $STAGED/<name>, but only if
# its sha256 is the one in params.sh. Nothing unverified is ever staged. Any
# failure after the retries is fatal to the caller.
fetch_configs() {
  local entry name want key i got
  mkdir -p "$STAGED"
  for entry in "${SB_CONFIGS[@]}"; do
    IFS=: read -r name want key <<<"$entry"
    for i in 1 2 3 4 5; do
      if aws s3 cp --only-show-errors --cli-connect-timeout 10 --cli-read-timeout 30 "s3://$SB_BUCKET/$key" "$STAGED/$name.part" \
        && got="$(sha256sum "$STAGED/$name.part" | cut -d' ' -f1)" && [ "$got" = "$want" ]; then
        mv "$STAGED/$name.part" "$STAGED/$name"
        break
      fi
      log "fetch of $key failed or did not match its sha256 (attempt $i)"
      rm -f "$STAGED/$name.part"
      if [ "$i" = 5 ]; then
        return 1
      fi
      sleep "${STATUSBOX_RETRY_SLEEP:-5}"
    done
  done
}

# arch_key maps uname -m to the key the params use: arm64 or amd64.
arch_key() {
  case "$(uname -m)" in
    aarch64 | arm64) echo arm64 ;;
    x86_64 | amd64) echo amd64 ;;
    *) log "unsupported architecture $(uname -m)"; return 1 ;;
  esac
}

install_binaries() {
  local a tmp
  a="$(arch_key)"
  mkdir -p "$BIN"
  tmp="$(mktemp -d)"

  local -n gatus_url="SB_GATUS_URL_$a" gatus_sha="SB_GATUS_SHA_$a"
  local -n ls_url="SB_LITESTREAM_URL_$a" ls_sha="SB_LITESTREAM_SHA_$a"
  local -n cf_url="SB_CLOUDFLARED_URL_$a" cf_sha="SB_CLOUDFLARED_SHA_$a"

  if [ ! -x "$BIN/gatus" ]; then
    fetch_verify "$gatus_url" "$tmp/gatus" "$gatus_sha"
    install -m 0755 "$tmp/gatus" "$BIN/gatus"
  fi
  if [ ! -x "$BIN/litestream" ]; then
    fetch_verify "$ls_url" "$tmp/litestream.tar.gz" "$ls_sha"
    tar -xzf "$tmp/litestream.tar.gz" -C "$tmp" litestream
    install -m 0755 "$tmp/litestream" "$BIN/litestream"
  fi
  if [ -n "$SB_TUNNEL_PARAM" ] && [ ! -x "$BIN/cloudflared" ]; then
    fetch_verify "$cf_url" "$tmp/cloudflared" "$cf_sha"
    install -m 0755 "$tmp/cloudflared" "$BIN/cloudflared"
  fi
  rm -rf "$tmp"
}

ensure_user() {
  getent group "$SVC_USER" >/dev/null || groupadd --system "$SVC_USER"
  id -u "$SVC_USER" >/dev/null 2>&1 || useradd --system --no-create-home --shell /sbin/nologin -g "$SVC_USER" "$SVC_USER"
}

# instance_fields splits one SB_INSTANCES entry, name:port:public:dbfile.
instance_fields() {
  IFS=: read -r INST_NAME INST_PORT INST_PUBLIC INST_DB <<<"$1"
}

unpack_instances() {
  local entry
  mkdir -p "$ETC/instances" "$LIB"
  for entry in "${SB_INSTANCES[@]}"; do
    instance_fields "$entry"
    mkdir -p "$ETC/instances/$INST_NAME" "$LIB/$INST_NAME"
    cp "$STAGED/$INST_NAME" "$ETC/instances/$INST_NAME/config.yaml"
    # Gatus merges every *.yaml in its config directory, later names winning:
    # the listener is the backend's to place, not the estate's Config's. A
    # public instance listens on loopback only (cloudflared dials it there);
    # the private one listens on every interface so a peer in the VPC can
    # reach it where the security group admits it.
    local address=127.0.0.1
    [ "$INST_PUBLIC" = "true" ] || address=0.0.0.0
    cat >"$ETC/instances/$INST_NAME/zz-web.yaml" <<EOF
web:
  address: $address
  port: $INST_PORT
EOF
    chown -R "$SVC_USER:$SVC_USER" "$LIB/$INST_NAME" 2>/dev/null || true
    # A port below 1024 (the private page on plain HTTP port 80) needs the
    # bind capability for the unprivileged Gatus; the unit keeps NoNewPrivileges
    # and gets nothing else, and only that instance's unit gets it.
    if [ "$INST_PORT" -lt 1024 ]; then
      mkdir -p "$UNITS/gatus@$INST_NAME.service.d"
      cat >"$UNITS/gatus@$INST_NAME.service.d/10-low-port.conf" <<'EOF2'
[Service]
AmbientCapabilities=CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_BIND_SERVICE
EOF2
    fi
  done
}

setup_trusted_cas() {
  [ -f "$STAGED/trusted-cas" ] || return 0
  mkdir -p "$ETC/ca"
  cp "$STAGED/trusted-cas" "$ETC/ca/extra-roots.pem"
  chmod 0644 "$ETC/ca/extra-roots.pem"
}

# write_common_env: non-secret environment shared by every unit. SSL_CERT_DIR is
# an addition to Go's default trust store, never a replacement.
write_common_env() {
  local region="$1"
  mkdir -p "$ETC"
  {
    echo "AWS_REGION=$region"
    echo "AWS_DEFAULT_REGION=$region"
    [ -f "$ETC/ca/extra-roots.pem" ] && echo "SSL_CERT_DIR=/etc/statusbox/ca"
    true
  } >"$ETC/common.env"
}

write_litestream_configs() {
  local entry
  for entry in "${SB_INSTANCES[@]}"; do
    instance_fields "$entry"
    cat >"$ETC/litestream-$INST_NAME.yml" <<EOF
dbs:
  - path: /data/$INST_DB
    replica:
      type: s3
      bucket: $SB_BUCKET
      path: ${SB_PREFIX:+$SB_PREFIX/}$INST_NAME
      sync-interval: 60s
EOF
  done
}

# write_ping_units: the dead-man ping, only when SB_PING_PARAM is set. The URL
# is a secret and is in neither unit: the service runs `statusbox-setup ping`,
# which reads it from a root-only file under /run.
write_ping_units() {
  [ -n "$SB_PING_PARAM" ] || return 0
  cat >"$UNITS/statusbox-ping.service" <<'EOF'
[Unit]
Description=statusbox dead-man ping

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/statusbox-setup ping
EOF

  cat >"$UNITS/statusbox-ping.timer" <<'EOF'
[Unit]
Description=statusbox dead-man ping, every minute

[Timer]
OnActiveSec=5s
OnUnitActiveSec=60s
AccuracySec=1s

[Install]
WantedBy=timers.target
EOF
}

write_units() {
  mkdir -p "$UNITS"
  write_ping_units
  cat >"$UNITS/gatus@.service" <<'EOF'
[Unit]
Description=statusbox Gatus instance %i (under litestream replicate)
After=network-online.target
Wants=network-online.target

[Service]
User=statusbox
Group=statusbox
EnvironmentFile=/etc/statusbox/common.env
EnvironmentFile=-/run/statusbox/gatus.env
Environment=GATUS_CONFIG_PATH=/etc/statusbox/instances/%i
BindPaths=/var/lib/statusbox/%i:/data
ExecStart=/usr/local/bin/litestream replicate -config /etc/statusbox/litestream-%i.yml -exec /usr/local/bin/gatus
Restart=always
RestartSec=5
NoNewPrivileges=yes
PrivateTmp=yes

# Deliberately no [Install]: only statusbox-boot starts a Gatus, and only after
# the database was restored, so a start from the warm pool can never write the
# replica.
EOF

  cat >"$UNITS/cloudflared.service" <<'EOF'
[Unit]
Description=statusbox cloudflared tunnel
After=network-online.target
Wants=network-online.target

[Service]
User=statusbox
Group=statusbox
EnvironmentFile=/etc/statusbox/common.env
EnvironmentFile=/run/statusbox/tunnel.env
ExecStart=/usr/local/bin/cloudflared --no-autoupdate tunnel run
Restart=always
RestartSec=5
NoNewPrivileges=yes
PrivateTmp=yes
EOF

  cat >"$UNITS/statusbox-boot.service" <<'EOF'
[Unit]
Description=statusbox boot: restore, start, complete the lifecycle hook
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
RemainAfterExit=yes
TimeoutStartSec=900
ExecStart=/usr/local/sbin/statusbox-setup boot

[Install]
WantedBy=multi-user.target
EOF

  cat >"$UNITS/statusbox-health.service" <<'EOF'
[Unit]
Description=statusbox local health check

[Service]
Type=oneshot
ExecStart=/usr/local/sbin/statusbox-setup health
EOF

  cat >"$UNITS/statusbox-health.timer" <<'EOF'
[Unit]
Description=statusbox local health check, every minute

[Timer]
OnBootSec=2min
OnUnitActiveSec=1min

[Install]
WantedBy=timers.target
EOF
}

install_phase() {
  load_params
  log "install: start"
  ensure_user
  setup_swap
  setup_journald
  install_binaries
  # Before anything is unpacked or started: a configuration that cannot be
  # fetched and verified abandons the launch (the group replaces the instance;
  # a bad one never goes InService) instead of serving whatever is on disk.
  AWS_REGION="$(imds meta-data/placement/region)"
  export AWS_REGION
  if ! fetch_configs; then
    log "install: the configurations could not be fetched and verified; abandoning the launch"
    complete_hook ABANDON
    return 1
  fi
  unpack_instances
  setup_trusted_cas
  write_common_env "$(imds meta-data/placement/region)"
  write_litestream_configs
  write_units
  systemctl daemon-reload
  # statusbox-boot is enabled for every later boot; the timer is NOT: only a
  # boot that went InService turns the health check on.
  systemctl enable statusbox-boot.service
  mkdir -p "$ROOT/var/lib/statusbox"
  touch "$ROOT/var/lib/statusbox/installed"
  log "install: done, running the boot phase"
  systemctl start statusbox-boot.service
}

# --------------------------------------------------------------------- boot

# ssm_get NAME: the decrypted value of one parameter. The value goes to stdout
# of this function only, captured by the caller into a file in /run; it is
# never logged.
ssm_get() {
  local name="$1" i v
  for i in 1 2 3 4 5 6; do
    if v="$(aws ssm get-parameter --name "$name" --with-decryption --query Parameter.Value --output text --region "$AWS_REGION")"; then
      printf '%s' "$v"
      return 0
    fi
    sleep 5
  done
  log "could not read SSM parameter $name"
  return 1
}

# env_line NAME VALUE: one EnvironmentFile line. systemd takes a double-quoted
# value with backslash escapes, so a value with spaces, quotes or a backslash
# survives; a newline cannot, and is refused.
env_line() {
  local name="$1" value="$2"
  case "$value" in *$'\n'*) log "value of $name contains a newline"; return 1 ;; esac
  value="${value//\\/\\\\}"
  value="${value//\"/\\\"}"
  printf '%s="%s"\n' "$name" "$value"
}

fetch_secrets() {
  local pair name param value
  mkdir -p "$RUN"
  chmod 0700 "$RUN"
  (
    set -e
    umask 077
    : >"$RUN/gatus.env"
    for pair in "${SB_ENV_PARAMS[@]}"; do
      name="${pair%%=*}"
      param="${pair#*=}"
      value="$(ssm_get "$param")"
      env_line "$name" "$value" >>"$RUN/gatus.env"
    done
    if [ -n "$SB_TUNNEL_PARAM" ]; then
      value="$(ssm_get "$SB_TUNNEL_PARAM")"
      env_line TUNNEL_TOKEN "$value" >"$RUN/tunnel.env"
    fi
    if [ -n "$SB_PING_PARAM" ]; then
      value="$(ssm_get "$SB_PING_PARAM")"
      case "$value" in *$'\n'*) log "the ping URL contains a newline"; return 1 ;; esac
      printf '%s' "$value" >"$RUN/ping.url"
    fi
  )
}

# restore_instance: bring the local database to the replica's state, if a
# replica exists. The restore goes to a temporary file and replaces the local
# database only if it produced one, so a first-ever start (no replica) keeps
# whatever is on disk, and a failed restore never leaves half a database.
# Litestream's own bookkeeping directory goes with the old database: it
# describes a database that is no longer there.
restore_instance() {
  local dir="$LIB/$INST_NAME" tmp
  tmp="$dir/$INST_DB.restore"
  rm -f "$tmp"
  litestream_bin="$BIN/litestream"
  "$litestream_bin" restore -config "$ETC/litestream-$INST_NAME.yml" -if-replica-exists -o "$tmp" "/data/$INST_DB"
  if [ -f "$tmp" ]; then
    rm -rf "$dir/$INST_DB" "$dir/$INST_DB-wal" "$dir/$INST_DB-shm" "$dir/.$INST_DB-litestream"
    mv "$tmp" "$dir/$INST_DB"
    log "restored $INST_NAME from the replica"
  else
    log "no replica for $INST_NAME yet; keeping the local database, if any"
  fi
  chown -R "$SVC_USER:$SVC_USER" "$dir"
}

wait_healthy() {
  local entry i
  for entry in "${SB_INSTANCES[@]}"; do
    instance_fields "$entry"
    for i in $(seq 1 60); do
      curl -fsS -m 3 -o /dev/null "http://127.0.0.1:$INST_PORT/health" && break
      [ "$i" = 60 ] && { log "gatus@$INST_NAME did not answer /health in time"; return 1; }
      sleep 2
    done
  done
}

go_in_service() {
  local entry
  AWS_REGION="$(imds meta-data/placement/region)"
  export AWS_REGION
  fetch_secrets
  for entry in "${SB_INSTANCES[@]}"; do
    instance_fields "$entry"
    restore_instance
  done
  for entry in "${SB_INSTANCES[@]}"; do
    instance_fields "$entry"
    systemctl start "gatus@$INST_NAME.service"
  done
  [ -z "$SB_TUNNEL_PARAM" ] || systemctl start cloudflared.service
  wait_healthy
  systemctl start statusbox-health.timer
  [ -z "$SB_PING_PARAM" ] || systemctl start statusbox-ping.timer
}

boot_phase() {
  local state rc
  load_params
  state="$(target_state)"
  log "boot: target lifecycle state '${state:-none}'"
  case "$state" in
    Warmed:*)
      # Installed and parked: nothing restored, nothing running. The hook is
      # completed so the instance can be stopped into the warm pool.
      complete_hook CONTINUE
      ;;
    InService | "")
      # go_in_service runs with errexit on, in a subshell that is NOT part of
      # an `if` condition (bash ignores errexit inside one): a failing step
      # must stop it, not be skipped.
      set +e
      (set -e; go_in_service)
      rc=$?
      set -e
      if [ "$rc" = 0 ]; then
        complete_hook CONTINUE
      else
        log "boot: failed to go in service; abandoning the launch"
        complete_hook ABANDON
        return 1
      fi
      ;;
    *)
      log "boot: nothing to do in state $state"
      ;;
  esac
}

# ------------------------------------------------------------------- health

# health_phase runs from the timer. It is a no-op until both Gatus and (when
# there is a tunnel) cloudflared are healthy; once either has been failing for
# SB_HEALTH_MINUTES in a row, it asks the Auto Scaling group to replace the
# instance, which is the only recovery a stateless box has. Restart=always
# handles the quick failures before that.
health_phase() {
  local entry ok=1 since now iid region
  load_params
  for entry in "${SB_INSTANCES[@]}"; do
    instance_fields "$entry"
    systemctl is-active --quiet "gatus@$INST_NAME.service" || ok=0
    curl -fsS -m 5 -o /dev/null "http://127.0.0.1:$INST_PORT/health" || ok=0
  done
  if [ -n "$SB_TUNNEL_PARAM" ]; then
    systemctl is-active --quiet cloudflared.service || ok=0
  fi
  mkdir -p "$RUN"
  if [ "$ok" = 1 ]; then
    rm -f "$RUN/unhealthy-since"
    return 0
  fi
  now="$(date +%s)"
  [ -f "$RUN/unhealthy-since" ] || echo "$now" >"$RUN/unhealthy-since"
  since="$(cat "$RUN/unhealthy-since")"
  log "health: failing since $since"
  if [ $((now - since)) -ge $((SB_HEALTH_MINUTES * 60)) ]; then
    iid="$(imds meta-data/instance-id)"
    region="$(imds meta-data/placement/region)"
    log "health: failing for over $SB_HEALTH_MINUTES minutes; marking $iid Unhealthy"
    aws autoscaling set-instance-health --instance-id "$iid" --health-status Unhealthy --region "$region" || log "set-instance-health failed"
  fi
}

# ------------------------------------------------------------------- ping

# ping_phase runs from the timer, every minute. It is the box's dead-man
# switch: a GET to the ping URL while every local Gatus answers /health with
# 200, a GET to <url>/fail when one does not. If the box (or its network) is
# gone, the pings stop and the external service alerts. The URL is a secret:
# it is read from a root-only file, handed to curl on stdin (never on the
# command line, where `ps` shows it), and never logged.
ping_phase() {
  local entry ok=1 url
  load_params
  [ -n "$SB_PING_PARAM" ] || return 0
  [ -s "$RUN/ping.url" ] || { log "ping: no ping URL on this boot"; return 0; }
  url="$(head -n 1 "$RUN/ping.url")"
  url="${url%/}"
  for entry in "${SB_INSTANCES[@]}"; do
    instance_fields "$entry"
    curl -fsS -m 5 -o /dev/null "http://127.0.0.1:$INST_PORT/health" 2>/dev/null || ok=0
  done
  [ "$ok" = 1 ] || url="$url/fail"
  if printf 'url = "%s"\n' "$url" | curl -K - -fsS -m 10 --connect-timeout 5 --retry 3 --retry-delay 2 -o /dev/null 2>/dev/null; then
    [ "$ok" = 1 ] || log "ping: sent the failure ping, a local Gatus is not healthy"
  else
    log "ping: the ping request failed"
    return 1
  fi
}

main() {
  case "${1:-}" in
    install) install_phase ;;
    boot) boot_phase ;;
    health) health_phase ;;
    ping) ping_phase ;;
    *)
      echo "usage: statusbox-setup install|boot|health|ping" >&2
      return 2
      ;;
  esac
}

# Sourced for a test (STATUSBOX_SOURCE_ONLY=1) the functions are defined and
# nothing runs.
if [ -z "${STATUSBOX_SOURCE_ONLY:-}" ]; then
  main "$@"
fi

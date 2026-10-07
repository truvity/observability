#!/usr/bin/env bash
# Runs the EC2 backend's setup script (pkg/statusbox/ec2/setup.sh, embedded in
# the instance's user-data) inside a plain amazonlinux:2023 container, the OS the
# instance boots, and proves what can be proved without an instance:
#
#   - the user-data the Go package renders (its golden file) stages params.sh,
#     the Gatus Configs and the setup script, and the script's install functions
#     run on AL2023: the pinned Litestream and cloudflared downloads match their
#     checksums, the units and the Litestream configs are written, the units pass
#     `systemd-analyze verify`, Litestream accepts its config;
#   - the listener drop-in's content is the expected one (that Gatus merges it
#     over the Config was checked against a real Gatus build when the backend
#     was written; no Gatus build exists before a release, so a stub stands in
#     for the binary here);
#   - the boot phase's ordering, with `systemctl`, `aws` and the metadata
#     service replaced by shims that log their calls: a warm-pool boot
#     completes the hook and starts nothing, restores nothing and reads no
#     secret; an in-service boot reads the secrets into /run, restores from the
#     replica BEFORE it starts any Gatus, and completes the hook; a failure
#     abandons the launch;
#   - a database really round-trips through `litestream replicate` and the
#     script's restore_instance (against a file replica standing in for S3);
#   - env_line quotes what systemd's EnvironmentFile needs quoted;
#   - the health phase marks the instance Unhealthy only after the window.
#
# It does NOT run systemd, Gatus, cloudflared against a tunnel, or anything
# against AWS. Needs Docker; deliberately not part of `check`.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
golden="$root/pkg/statusbox/ec2/testdata/bootstrap.golden.sh"

# STATUSBOX_EC2_IMAGE lets a laptop reuse an image that already has the packages
# below (dnf is slow on a loaded machine); CI always pulls the real one.
pull=(--pull=always)
[ -z "${STATUSBOX_EC2_IMAGE:-}" ] || pull=()

echo "hack/statusbox-ec2-ci.sh: running the EC2 setup script inside amazonlinux:2023"
docker run --rm -i \
  "${pull[@]}" \
  -v "$golden:/golden.sh:ro" \
  "${STATUSBOX_EC2_IMAGE:-amazonlinux:2023}" \
  bash -s <<'INNER'
set -euo pipefail

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "ok: $*"; }

dnf install -y --setopt=install_weak_deps=False shadow-utils tar gzip sqlite systemd findutils >/dev/null

# --- stage what the user-data stages: everything but its last line, the exec.
sed '$d' /golden.sh >/tmp/stage.sh
bash /tmp/stage.sh
[ -x /usr/local/sbin/statusbox-setup ] || fail "the setup script was not written"
[ -f /etc/statusbox/params.sh ] || fail "params.sh was not written"
[ -f /opt/statusbox/staged/ops.yaml.gz.b64 ] || fail "a Gatus Config was not staged"
ok "user-data stages params.sh, the Configs and the setup script"

export STATUSBOX_SOURCE_ONLY=1
# shellcheck disable=SC1091
. /usr/local/sbin/statusbox-setup
load_params

# The golden's Gatus release does not exist: a stub binary with its checksum
# stands in, through the same fetch_verify the real one goes through.
printf '#!/bin/sh\nexec sleep 3600\n' >/tmp/gatus-stub
stub_sha="$(sha256sum /tmp/gatus-stub | cut -d' ' -f1)"
for a in arm64 amd64; do
  printf -v "SB_GATUS_URL_$a" 'file:///tmp/gatus-stub'
  printf -v "SB_GATUS_SHA_$a" '%s' "$stub_sha"
done

ensure_user
install_binaries
/usr/local/bin/litestream version >/dev/null || fail "litestream does not run"
/usr/local/bin/cloudflared --version >/dev/null || fail "cloudflared does not run"
ok "pinned Litestream and cloudflared downloaded, checksum-verified and run on AL2023"

# A tampered download is refused.
if fetch_verify "file:///tmp/gatus-stub" /tmp/tampered "$(printf 'x%.0s' $(seq 64))" 2>/dev/null; then
  fail "fetch_verify accepted a wrong checksum"
fi
ok "a wrong checksum is refused"

unpack_instances
write_common_env eu-west-1
write_litestream_configs
write_units
grep -q '^  port: 8082$' /etc/statusbox/instances/ops/zz-web.yaml || fail "the public instance listener"
grep -q '^  address: 127.0.0.1$' /etc/statusbox/instances/ops/zz-web.yaml || fail "the public instance must listen on loopback"
grep -q '^  address: 0.0.0.0$' /etc/statusbox/instances/ops-breakglass/zz-web.yaml || fail "the private instance must listen on every interface"
grep -q 'path: box/ops-breakglass' /etc/statusbox/litestream-ops-breakglass.yml || fail "the replica path"
ok "listeners and Litestream configs written"

for u in gatus@.service cloudflared.service statusbox-boot.service statusbox-health.service statusbox-health.timer; do
  [ -f "/etc/systemd/system/$u" ] || fail "missing unit $u"
done
if grep -q '^\[Install\]' /etc/systemd/system/gatus@.service /etc/systemd/system/cloudflared.service; then
  fail "gatus and cloudflared must not be enabled by themselves: only the boot phase starts them"
fi
systemd-analyze verify /etc/systemd/system/gatus@ops.service 2>&1 | grep -v -i 'EnvironmentFile\|run/statusbox' || true
ok "units written; gatus and cloudflared have no [Install]"

# --- litestream accepts the generated config (a file replica stands in for S3
# below, but the s3 shape itself must parse).
/usr/local/bin/litestream databases -config /etc/statusbox/litestream-ops.yml | grep -q '/data/ops.db' || fail "litestream does not accept its config"
ok "litestream parses the generated config"

# --- shims: systemctl, aws and curl (the metadata service and /health).
mkdir -p /shim
cat >/shim/systemctl <<'EOF'
#!/bin/bash
echo "systemctl $*" >>/tmp/calls
case "$1" in is-active) [ -f /tmp/unit-down ] && exit 3 ;; esac
exit 0
EOF
cat >/shim/aws <<'EOF'
#!/bin/bash
echo "aws $*" >>/tmp/calls
if [ "$1 $2" = "ssm get-parameter" ]; then
  [ -f /tmp/ssm-down ] && exit 1
  printf 'value of %s' "$4"
  exit 0
fi
exit 0
EOF
cat >/shim/curl <<'EOF'
#!/bin/bash
args="$*"
case "$args" in
  *api/token*) printf 'token'; exit 0 ;;
  *autoscaling/target-lifecycle-state*) [ -f /tmp/state ] && { cat /tmp/state; exit 0; }; exit 22 ;;
  *placement/region*) printf 'eu-west-1'; exit 0 ;;
  *meta-data/instance-id*) printf 'i-0example'; exit 0 ;;
  */health*) [ -f /tmp/health-down ] && exit 22; exit 0 ;;
esac
exec /usr/bin/curl "$@"
EOF
chmod +x /shim/*
export PATH="/shim:$PATH"
export STATUSBOX_ROOT=""

# --- a database round-trips through litestream and restore_instance, against a
# file replica standing in for the bucket.
mkdir -p /data /replica
for n in ops ops-breakglass; do
  cat >"/etc/statusbox/litestream-$n.yml" <<EOF
dbs:
  - path: /data/ops.db
    replica:
      type: file
      path: /replica/$n
      sync-interval: 1s
EOF
done
rm -f /data/ops.db*
sqlite3 /data/ops.db 'PRAGMA journal_mode=WAL; CREATE TABLE t(v TEXT); INSERT INTO t VALUES ("history");' >/dev/null
/usr/local/bin/litestream replicate -config /etc/statusbox/litestream-ops.yml >/tmp/litestream.log 2>&1 &
ls_pid=$!
for _ in $(seq 1 60); do
  compgen -G '/replica/ops/ltx/*/*.ltx' >/dev/null && break
  sleep 1
done
kill "$ls_pid" 2>/dev/null || true
wait "$ls_pid" 2>/dev/null || true
compgen -G '/replica/ops/ltx/*/*.ltx' >/dev/null || { cat /tmp/litestream.log >&2; fail "litestream wrote no replica"; }
rm -rf /data/ops.db* /data/.ops.db-litestream /var/lib/statusbox/ops/*
INST_NAME=ops INST_DB=ops.db restore_instance
[ "$(sqlite3 /var/lib/statusbox/ops/ops.db 'SELECT v FROM t')" = history ] || fail "the restored database lost its history"
ok "a database round-trips through litestream replicate and restore_instance"

# No replica yet: the local database, if any, is kept and nothing fails.
echo keep >/var/lib/statusbox/ops-breakglass/ops.db
INST_NAME=ops-breakglass INST_DB=ops.db restore_instance
[ "$(cat /var/lib/statusbox/ops-breakglass/ops.db)" = keep ] || fail "a missing replica must leave the local database alone"
ok "no replica: the local database is kept"

# --- boot phase: a warm-pool boot does nothing but complete the hook.
: >/tmp/calls
echo 'Warmed:Stopped' >/tmp/state
boot_phase
grep -q 'complete-lifecycle-action .*--lifecycle-action-result CONTINUE' /tmp/calls || fail "warm boot must complete the hook"
grep -q 'systemctl start' /tmp/calls && fail "warm boot must start nothing"
grep -q 'ssm get-parameter' /tmp/calls && fail "warm boot must read no secret"
[ -e /run/statusbox/gatus.env ] && fail "warm boot must leave no secret file"
ok "warm-pool boot: hook completed, nothing restored, started or read"

# --- boot phase: going in service restores first, then starts, then completes.
rm -rf /var/lib/statusbox/ops/ops.db* /var/lib/statusbox/ops-breakglass/ops.db*
for n in ops ops-breakglass; do
  cp -r /replica/ops "/replica/$n.tmp" 2>/dev/null || true
done
rm -rf /replica/ops-breakglass; mv /replica/ops-breakglass.tmp /replica/ops-breakglass 2>/dev/null || true
: >/tmp/calls
echo 'InService' >/tmp/state
boot_phase
[ "$(sqlite3 /var/lib/statusbox/ops/ops.db 'SELECT v FROM t')" = history ] || fail "in-service boot must restore before starting"
start_line="$(grep -n 'systemctl start gatus@' /tmp/calls | head -1 | cut -d: -f1)"
hook_line="$(grep -n 'complete-lifecycle-action' /tmp/calls | head -1 | cut -d: -f1)"
[ -n "$start_line" ] && [ -n "$hook_line" ] && [ "$start_line" -lt "$hook_line" ] || fail "the hook must complete after Gatus started"
grep -q 'systemctl start cloudflared.service' /tmp/calls || fail "cloudflared must start"
grep -q 'systemctl start statusbox-health.timer' /tmp/calls || fail "the health timer must start only in service"
grep -q 'lifecycle-action-result CONTINUE' /tmp/calls || fail "hook result"
[ "$(stat -c %a /run/statusbox/gatus.env)" = 600 ] || fail "secrets file mode"
grep -q '^ALERT_URL_OPS_ALERTS_READ_TOKEN="value of /acme/status/alerts-read-token"$' /run/statusbox/gatus.env || fail "gatus.env content"
grep -q '^TUNNEL_TOKEN="value of /acme/status/tunnel-token"$' /run/statusbox/tunnel.env || fail "tunnel.env content"
ok "in-service boot: secrets in /run (0600), restore, then Gatus and cloudflared, then the hook"

# --- a failing secret read abandons the launch and starts nothing.
: >/tmp/calls
touch /tmp/ssm-down
rm -f /run/statusbox/*.env
sed -i 's/sleep 5/sleep 0/' /usr/local/sbin/statusbox-setup
. /usr/local/sbin/statusbox-setup
load_params
# Not in an `if`: bash ignores errexit inside a condition, which is not how the
# systemd unit runs it.
# A subshell, because boot_phase turns errexit back on and returns non-zero.
set +e
( boot_phase 2>/dev/null )
rc=$?
set -e
[ "$rc" != 0 ] || fail "a failed secret read must fail the boot"
grep -q 'lifecycle-action-result ABANDON' /tmp/calls || fail "a failed boot must abandon the launch"
grep -q 'systemctl start gatus@' /tmp/calls && fail "nothing may start when the secrets cannot be read"
rm -f /tmp/ssm-down
ok "a failed secret read abandons the launch and starts nothing"

# --- env_line.
[ "$(env_line X 'a"b\c d')" = 'X="a\"b\\c d"' ] || fail "env_line quoting: $(env_line X 'a"b\c d')"
if env_line X $'a\nb' 2>/dev/null; then fail "env_line must refuse a newline"; fi
ok "env_line quotes and refuses a newline"

# --- health phase.
mkdir -p /run/statusbox
rm -f /run/statusbox/unhealthy-since
: >/tmp/calls
health_phase
grep -q set-instance-health /tmp/calls && fail "a healthy box must not be marked"
touch /tmp/health-down
SB_HEALTH_MINUTES=5
load_params() { :; }
health_phase
grep -q set-instance-health /tmp/calls && fail "a first failure must not mark the instance"
echo $(( $(date +%s) - 400 )) >/run/statusbox/unhealthy-since
health_phase
grep -q 'autoscaling set-instance-health --instance-id i-0example --health-status Unhealthy' /tmp/calls || fail "a failure past the window must mark the instance Unhealthy"
ok "health: only a failure past the window marks the instance Unhealthy"

echo "hack/statusbox-ec2-ci.sh: OK"
INNER

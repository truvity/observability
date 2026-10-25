#!/usr/bin/env bash
# Runs the EC2 backend's setup script (pkg/statusbox/ec2/setup.sh, an S3 object
# the instance's user-data fetches) inside a plain amazonlinux:2023 container,
# the OS the instance boots, and proves what can be proved without an instance:
#
#   - params.sh, taken from the user-data the Go package renders (its golden
#     file), is what the script is run with; fetch_configs stages the Gatus
#     Configs from a directory standing in for the bucket (a shim `aws`), and
#     refuses a wrong digest, and the script's install functions run on AL2023:
#     the pinned Litestream download matches its checksum, the units and the
#     Litestream configs are written, the units pass `systemd-analyze verify`,
#     Litestream accepts its config;
#   - the listener drop-in's content is the expected one (that Gatus merges it
#     over the Config was checked against a real Gatus build when the backend
#     was written; no Gatus build exists before a release, so a stub stands in
#     for the binary here);
#   - the in-service phase's ordering, with `systemctl`, `aws` and the metadata
#     service replaced by shims that log their calls: it reads the secrets into
#     /run, restores from the replica BEFORE it starts any Gatus, and calls
#     neither the lifecycle hook nor the tunnel (github.com/truvity/asg owns
#     both); a failed secret read fails the phase and starts nothing;
#   - the check phase (the launch's ready check and the watchdog's) fails when
#     a Gatus is down or does not answer /health;
#   - a database really round-trips through `litestream replicate` and the
#     script's restore_instance (against a file replica standing in for S3);
#   - env_line quotes what systemd's EnvironmentFile needs quoted.
#
# What the library does (the warm-pool decision, the hook, the watchdog's
# window, the tunnel's phases) is proved by its own tests, which run the
# generated scripts under bash against fake commands.
#
# It does NOT run systemd, Gatus, cloudflared against a tunnel, or anything
# against AWS. Needs Docker; deliberately not part of `check`.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
golden="$root/pkg/statusbox/ec2/testdata/userdata.golden.yaml"
setup="$root/pkg/statusbox/ec2/setup.sh"

# STATUSBOX_EC2_IMAGE lets a laptop reuse an image that already has the packages
# below (dnf is slow on a loaded machine); CI always pulls the real one.
pull=(--pull=always)
[ -z "${STATUSBOX_EC2_IMAGE:-}" ] || pull=()

echo "hack/statusbox-ec2-ci.sh: running the EC2 setup script inside amazonlinux:2023"
docker run --rm -i \
  "${pull[@]}" \
  -v "$golden:/golden.yaml:ro" \
  -v "$setup:/setup.sh:ro" \
  "${STATUSBOX_EC2_IMAGE:-amazonlinux:2023}" \
  bash -s <<'INNER'
set -euo pipefail

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "ok: $*"; }

dnf install -y --setopt=install_weak_deps=False shadow-utils tar gzip sqlite systemd findutils >/dev/null

# --- the S3 bucket and the metadata service, as shims for the fetches below.
mkdir -p /early /bucket/box/config
cat >/early/aws <<'SHIM'
#!/bin/bash
echo "aws $*" >>/tmp/early-calls
[ "$1 $2" = "s3 cp" ] || exit 0
for a in "$@"; do case "$a" in s3://*) src="${a#s3://*/}" ;; *) dest="$a" ;; esac; done
cp "/bucket/$src" "$dest"
SHIM
cat >/early/curl <<'SHIM'
#!/bin/bash
for a in "$@"; do url="$a"; done
case "$url" in
*/api/token) echo tok ;;
*/placement/region) echo eu-west-1 ;;
*/instance-id) echo i-0example ;;
*) exit 22 ;;
esac
SHIM
printf '#!/bin/sh\nexit 0\n' >/early/sleep
chmod +x /early/*
PATH_ORIG="$PATH"

# --- params.sh as the user-data writes it: the block of the golden's
# write_files entry, de-indented. The script itself is installed where the plan
# puts it.
mkdir -p /etc/statusbox
awk '/^  - path: \/etc\/statusbox\/params.sh$/ {f=1; next}
     f && /^    content: \|$/ {c=1; next}
     f && c && /^      / {sub(/^      /, ""); print; next}
     f && c && /^$/ {print; next}
     f && c {exit}' /golden.yaml >/etc/statusbox/params.sh
[ -s /etc/statusbox/params.sh ] || fail "the golden carries no params.sh"
grep -q '^SB_INSTANCES=' /etc/statusbox/params.sh || fail "params.sh has no instances"
install -D -m 0755 /setup.sh /usr/local/sbin/statusbox-setup.sh
ok "params.sh taken from the user-data golden; the setup script installed where the plan puts it"

export STATUSBOX_SOURCE_ONLY=1
# shellcheck disable=SC1091
. /usr/local/sbin/statusbox-setup.sh
load_params

# The Configs are objects too: serve placeholders under their own digests and
# let fetch_configs stage them; a digest it was not given is refused.
SB_CONFIGS=()
for n in ops ops-breakglass; do
  printf 'storage:\n  type: sqlite\n  path: /data/ops.db\n# %s\n' "$n" >"/tmp/$n.yaml"
  sha="$(sha256sum "/tmp/$n.yaml" | cut -d' ' -f1)"
  cp "/tmp/$n.yaml" "/bucket/box/config/$sha.yaml"
  SB_CONFIGS+=("$n:$sha:box/config/$sha.yaml")
done
PATH="/early:$PATH_ORIG" fetch_configs
{ [ -f /opt/statusbox/staged/ops ] && [ -f /opt/statusbox/staged/ops-breakglass ]; } || fail "a Gatus Config was not staged"
ok "fetch_configs stages the verified Configs"
rm -f /opt/statusbox/staged/ops
SB_CONFIGS=("ops:$(printf 'x%.0s' $(seq 64)):box/config/$sha.yaml")
if PATH="/early:$PATH_ORIG" fetch_configs 2>/dev/null; then
  fail "fetch_configs accepted a wrong digest"
fi
[ ! -e /opt/statusbox/staged/ops ] || fail "an unverified Config was staged"
cp "/tmp/ops.yaml" /opt/statusbox/staged/ops
ok "a Config with the wrong digest is refused and not staged"
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
ok "pinned Litestream downloaded, checksum-verified and run on AL2023"

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

[ -f /etc/systemd/system/gatus@.service ] || fail "missing unit gatus@.service"
for u in cloudflared.service statusbox-boot.service statusbox-health.service statusbox-health.timer; do
  [ ! -e "/etc/systemd/system/$u" ] || fail "$u moved to github.com/truvity/asg and must not be written here"
done
if grep -q '^\[Install\]' /etc/systemd/system/gatus@.service; then
  fail "gatus must not be enabled by itself: only the in-service phase starts it"
fi
systemd-analyze verify /etc/systemd/system/gatus@ops.service 2>&1 | grep -v -i 'EnvironmentFile\|run/statusbox' || true
ok "units written; gatus has no [Install]"

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

# --- in-service phase: restore first, then start; no hook, no tunnel.
rm -rf /var/lib/statusbox/ops/ops.db* /var/lib/statusbox/ops-breakglass/ops.db*
for n in ops ops-breakglass; do
  cp -r /replica/ops "/replica/$n.tmp" 2>/dev/null || true
done
rm -rf /replica/ops-breakglass; mv /replica/ops-breakglass.tmp /replica/ops-breakglass 2>/dev/null || true
: >/tmp/calls
in_service_phase
[ "$(sqlite3 /var/lib/statusbox/ops/ops.db 'SELECT v FROM t')" = history ] || fail "the in-service phase must restore before starting"
grep -q 'systemctl start gatus@' /tmp/calls || fail "Gatus must start"
grep -q 'complete-lifecycle-action' /tmp/calls && fail "the lifecycle hook is the library's, not the setup script's"
grep -q 'cloudflared' /tmp/calls && fail "the tunnel is the library's module, not the setup script's"
[ "$(stat -c %a /run/statusbox/gatus.env)" = 600 ] || fail "secrets file mode"
grep -q '^ALERT_URL_OPS_ALERTS_READ_TOKEN="value of /acme/status/alerts-read-token"$' /run/statusbox/gatus.env || fail "gatus.env content"
[ ! -e /run/statusbox/tunnel.env ] || fail "the tunnel token is the tunnel module's"
ok "in-service phase: secrets in /run (0600), restore, then Gatus; no hook and no tunnel here"

# --- check phase: the ready check and the watchdog's.
check_phase || fail "a healthy box must pass the check"
touch /tmp/health-down
if (check_phase 2>/dev/null); then fail "a Gatus that does not answer /health must fail the check"; fi
rm -f /tmp/health-down
touch /tmp/unit-down
if (check_phase 2>/dev/null); then fail "a stopped Gatus unit must fail the check"; fi
rm -f /tmp/unit-down
ok "check: a down or silent Gatus fails it"

# --- a failing secret read fails the phase and starts nothing.
: >/tmp/calls
touch /tmp/ssm-down
rm -f /run/statusbox/*.env
sed -i 's/sleep 5/sleep 0/' /usr/local/sbin/statusbox-setup.sh
. /usr/local/sbin/statusbox-setup.sh
load_params
# Not in an `if`: bash ignores errexit inside a condition, which is not how the
# library's in-service step runs it (bash -e).
set +e
( set -e; in_service_phase 2>/dev/null )
rc=$?
set -e
[ "$rc" != 0 ] || fail "a failed secret read must fail the in-service phase"
grep -q 'systemctl start gatus@' /tmp/calls && fail "nothing may start when the secrets cannot be read"
rm -f /tmp/ssm-down
ok "a failed secret read fails the phase (the library then abandons the launch) and starts nothing"

# --- env_line.
[ "$(env_line X 'a"b\c d')" = 'X="a\"b\\c d"' ] || fail "env_line quoting: $(env_line X 'a"b\c d')"
if env_line X $'a\nb' 2>/dev/null; then fail "env_line must refuse a newline"; fi
ok "env_line quotes and refuses a newline"

echo "hack/statusbox-ec2-ci.sh: OK"
INNER

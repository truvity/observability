#!/usr/bin/env bash
# hack/platform-alerts-suspended-proof.sh — REAL proof, against a real
# victoria-metrics binary, that charts/platform-alerts' two backup rules
# (CronJobNotSucceeding, BackupJobFailed) skip a SUSPENDED CronJob, still
# fire on an active one, and still fire when kube_cronjob_spec_suspend is
# ABSENT — and that the expressions 0.10.0 rendered do not skip it.
#
# The bug: suspending a CronJob is a decision written into its spec, and
# both rules fired on one for as long as it stayed suspended — the age of
# its last success grows forever, and its last failed Job stays its newest
# forever. The fix (`groups.backups.ignoreSuspended`, default true) is an
# `unless ... kube_cronjob_spec_suspend == 1`, never an `and ... == 0`, so
# a cluster whose kube-state-metrics does not export the suspend series
# keeps the old behaviour instead of going silent. The third scenario of
# each rule below is that guarantee.
#
# tests/backupjobfailed_test.go pins both rendered strings against edits;
# neither it nor any Go test here can EVALUATE a join (see
# hack/platform-alerts-newest-job-proof.sh's own header), so this is the
# other half: synthetic kube-state-metrics series imported into the
# VictoriaMetrics version charts/observability-stack pins, and each
# expression — read off the real render, both with the fix and with
# `ignoreSuspended: false` — queried with /api/v1/query.
#
#   CronJobNotSucceeding            last success   suspend   expect
#     cj-active-stale               3 days ago     0         FIRE
#     cj-suspended-stale            3 days ago     1         silent (old: FIRE)
#     cj-no-suspend-series          3 days ago     absent    FIRE
#     cj-active-fresh               1 hour ago     0         silent
#
#   BackupJobFailed (newest Job)    newest failed  suspend   expect
#     bj-active                     yes            0         FIRE
#     bj-suspended                  yes            1         silent (old: FIRE)
#     bj-no-suspend-series          yes            absent    FIRE
#
# Needs Docker, curl, helm and python3 with PyYAML. Deliberately NOT part
# of `check` or CI, for the same reason as the newest-job proof: a
# run-by-hand proof for this fix; the golden and the Go test are the gate.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d -t platform-alerts-suspended-proof.XXXXXX)"
container=platform-alerts-suspended-proof-vm
me="hack/platform-alerts-suspended-proof.sh"

cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

for tool in docker curl python3 helm; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "$me needs $tool, which is not on PATH" >&2
    exit 1
  }
done
python3 -c 'import yaml' 2>/dev/null || {
  echo "$me needs python3's PyYAML (import yaml)" >&2
  exit 1
}

vm_tgz=("$root"/charts/observability-stack/charts/victoria-metrics-k8s-stack-*.tgz)
[ -e "${vm_tgz[0]}" ] || {
  echo "$me: no vendored victoria-metrics-k8s-stack — run 'just vendor observability-stack'" >&2
  exit 1
}
vm_version="$(tar -xzOf "${vm_tgz[0]}" victoria-metrics-k8s-stack/Chart.yaml | awk '/^appVersion:/ { print $2 }')"
echo "$me: using victoria-metrics:$vm_version, the version charts/observability-stack pins"

cat > "$work/extract.py" <<'PYEOF'
import sys, yaml
alert = sys.argv[1]
for doc in yaml.safe_load_all(sys.stdin.read()):
    if not doc or doc.get("kind") != "VMRule":
        continue
    for group in doc["spec"]["groups"]:
        if group["name"] != "platform-alerts.backups":
            continue
        for rule in group["rules"]:
            if rule.get("alert") == alert:
                sys.stdout.write(rule["expr"])
                sys.exit(0)
sys.exit(alert + " not found in the rendered platform-alerts.backups group")
PYEOF

new_render="$(helm template x "$root/charts/platform-alerts" --values "$root/tests/cases/platform-alerts/minimal/values.yaml")"
old_render="$(helm template x "$root/charts/platform-alerts" --values "$root/tests/cases/platform-alerts/suspended-not-ignored/values.yaml")"
declare -A expr
for alert in CronJobNotSucceeding BackupJobFailed; do
  expr["new-$alert"]="$(python3 "$work/extract.py" "$alert" <<<"$new_render")"
  expr["old-$alert"]="$(python3 "$work/extract.py" "$alert" <<<"$old_render")"
  for v in new old; do
    [ -n "${expr[$v-$alert]}" ] || { echo "$me: empty $v $alert expression" >&2; exit 1; }
    echo "=== $alert, $v ==="
    echo "${expr[$v-$alert]}"
    echo
  done
done

docker run -d --name "$container" -p 127.0.0.1::8428 \
  "victoriametrics/victoria-metrics:$vm_version" -retentionPeriod=100y >/dev/null

port=""
for _ in $(seq 1 30); do
  port="$(docker port "$container" 8428/tcp 2>/dev/null | cut -d: -f2 || true)"
  [ -n "$port" ] && curl -fsS "http://127.0.0.1:$port/health" >/dev/null 2>&1 && break
  port=""
  sleep 1
done
[ -n "$port" ] || {
  echo "$me: victoria-metrics never became healthy" >&2
  docker logs "$container" >&2 || true
  exit 1
}

now="$(date +%s)"
now_ms="$((now * 1000))"
day=86400
hour=3600

# kube_job_created / kube_job_owner / kube_job_status_failed as in the
# newest-job proof; one Job per CronJob is enough here, since "newest" is
# that proof's subject and not this one's.
{
  for cj in cj-active-stale:$((now - 3 * day)):0 cj-suspended-stale:$((now - 3 * day)):1 \
            cj-no-suspend-series:$((now - 3 * day)):- cj-active-fresh:$((now - hour)):0; do
    IFS=: read -r name last suspend <<<"$cj"
    echo "kube_cronjob_status_last_successful_time{namespace=\"ns-$name\",cronjob=\"$name\"} $last $now_ms"
    [ "$suspend" = - ] || echo "kube_cronjob_spec_suspend{namespace=\"ns-$name\",cronjob=\"$name\"} $suspend $now_ms"
  done
  for bj in bj-active:0 bj-suspended:1 bj-no-suspend-series:-; do
    IFS=: read -r name suspend <<<"$bj"
    job="$name-28130000"
    echo "kube_job_created{namespace=\"ns-$name\",job_name=\"$job\"} $((now - hour)) $now_ms"
    echo "kube_job_owner{namespace=\"ns-$name\",job_name=\"$job\",owner_kind=\"CronJob\",owner_name=\"$name\",owner_is_controller=\"true\"} 1 $now_ms"
    echo "kube_job_status_failed{namespace=\"ns-$name\",job_name=\"$job\"} 1 $now_ms"
    # The CronJob's own last success is recent, so CronJobNotSucceeding
    # stays out of the BackupJobFailed scenarios.
    echo "kube_cronjob_status_last_successful_time{namespace=\"ns-$name\",cronjob=\"$name\"} $((now - hour)) $now_ms"
    [ "$suspend" = - ] || echo "kube_cronjob_spec_suspend{namespace=\"ns-$name\",cronjob=\"$name\"} $suspend $now_ms"
  done
} > "$work/series.prom"

curl -fsS -X POST "http://127.0.0.1:$port/api/v1/import/prometheus" --data-binary "@$work/series.prom" >/dev/null

count() {
  curl -fsS "http://127.0.0.1:$port/api/v1/query" \
    --data-urlencode "query=count({__name__=~\"kube_.*\"})" --data-urlencode "time=$now" \
    | python3 -c 'import json, sys; d=json.load(sys.stdin)["data"]["result"]; print(d[0]["value"][1] if d else 0)'
}
want="$(wc -l <"$work/series.prom" | tr -d ' ')"
got=0
for _ in $(seq 1 30); do
  got="$(count)"
  [ "$got" = "$want" ] && break
  sleep 1
done
[ "$got" = "$want" ] || { echo "$me: only $got/$want series queryable after 30s" >&2; exit 1; }

query() { # expr -> sorted "namespace" lines of the result
  curl -fsS "http://127.0.0.1:$port/api/v1/query" \
    --data-urlencode "query=$1" --data-urlencode "time=$now" \
    | python3 -c '
import json, sys
body = json.load(sys.stdin)
if body.get("status") != "success":
    sys.exit("query failed: " + json.dumps(body))
print("\n".join(sorted(r["metric"].get("namespace", "?") for r in body["data"]["result"])))
'
}

fail=0
expect() { # result namespace fire|silent description
  if grep -qx "$2" <<<"$1"; then fired=fire; else fired=silent; fi
  if [ "$fired" = "$3" ]; then
    echo "ok    $4"
  else
    echo "FAIL  $4 — expected $3, got $fired" >&2
    fail=1
  fi
}

for v in new old; do
  cj="$(query "${expr[$v-CronJobNotSucceeding]}")"
  bj="$(query "${expr[$v-BackupJobFailed]}")"
  echo
  echo "--- $v expressions ---"
  echo "CronJobNotSucceeding fired for: ${cj:-<nothing>}" | tr '\n' ' '; echo
  echo "BackupJobFailed fired for:      ${bj:-<nothing>}" | tr '\n' ' '; echo
  if [ "$v" = new ]; then suspended=silent; else suspended=fire; fi
  expect "$cj" ns-cj-active-stale fire "$v CronJobNotSucceeding: active, stale"
  expect "$cj" ns-cj-suspended-stale "$suspended" "$v CronJobNotSucceeding: SUSPENDED, stale"
  expect "$cj" ns-cj-no-suspend-series fire "$v CronJobNotSucceeding: no suspend series, stale"
  expect "$cj" ns-cj-active-fresh silent "$v CronJobNotSucceeding: active, fresh"
  expect "$bj" ns-bj-active fire "$v BackupJobFailed: active, newest failed"
  expect "$bj" ns-bj-suspended "$suspended" "$v BackupJobFailed: SUSPENDED, newest failed"
  expect "$bj" ns-bj-no-suspend-series fire "$v BackupJobFailed: no suspend series, newest failed"
done

echo
if [ "$fail" = 0 ]; then
  echo "$me: OK — both rules skip a suspended CronJob, still fire on an active one and on one whose suspend series is absent, and the 0.10.0 expressions reproduce the bug"
fi
exit $fail

#!/usr/bin/env bash
# hack/platform-alerts-newest-job-proof.sh — REAL proof, against a real
# victoria-metrics binary, that BackupJobFailed
# (charts/platform-alerts/templates/vmrule.yaml) now reads only the
# NEWEST Job of each CronJob, and that the expression this repository
# renders before this fix does not.
#
# tests/backupjobfailed_test.go pins the exact MetricsQL string this
# chart renders, which catches an accidental EDIT to it — but a JOIN
# cannot be evaluated by this repository's own hold-window model
# (vmalertHoldWindow in tests/kargo_alerts_test.go models a single
# boolean series through vmalert's pending/firing state machine; it has
# no concept of matching label sets across several metrics, which is the
# entire mechanism this fix depends on). So this script is the other
# half: a real victoria-metrics — the same binary vmalert queries,
# pinned to the exact version charts/observability-stack's own
# victoria-metrics-k8s-stack dependency ships — with synthetic
# kube-state-metrics series imported for four CronJobs, one per
# scenario, and both the OLD and the NEW expression run against them
# with /api/v1/query.
#
# The bug this fix closes, reproduced here rather than merely described:
# a CronJob's failedJobsHistoryLimit keeps a failed Job object around
# long after a later run of the same CronJob succeeded, and the OLD
# expression — `max by (namespace, job_name) (kube_job_status_failed{...})
# > 0`, with no `for:` — reads every retained Job equally, so that old
# failure keeps firing forever. Four CronJobs, each with an "old" Job
# (created a week ago, failed) and — for three of them — a "new" Job
# (created recently):
#
#   nightly-report   old failed, new SUCCEEDED   -> must NOT fire
#   snapshot-age      old failed, new FAILED       -> MUST fire, on the new Job
#   root-generation   old failed, new RUNNING       -> must NOT fire
#   (standalone)      one Job, no CronJob owner, FAILED
#
# The fourth is not a CronJob at all: charts/platform-alerts/values.yaml
# documents the scoping choice this fix makes — BackupJobFailed now reads
# only kube_job_owner{owner_kind="CronJob"}-owned Jobs, because "newest"
# presupposes a schedule a standalone Job does not have, and the rule's
# whole intent is backup CronJobs. The OLD expression fires on it; the
# NEW one does not, on purpose, and this script asserts that too rather
# than treating it as an oversight.
#
# Needs Docker, curl and python3 (with PyYAML, which every python3 this
# repository has met so far already carries — the same way
# hack/statusbox-ec2-ci.sh needs openssl, curl and jq without either
# being a devbox package). Deliberately NOT part of `check` or CI, the
# same reason hack/statusbox-ec2-ci.sh's own Docker requirement is
# not: a one-off, run-by-hand proof for this fix, not a regression gate
# — the golden and tests/backupjobfailed_test.go are that gate.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d -t platform-alerts-newest-job-proof.XXXXXX)"
container=platform-alerts-newest-job-proof-vm

cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

for tool in docker curl python3; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "hack/platform-alerts-newest-job-proof.sh needs $tool, which is not on PATH" >&2
    exit 1
  }
done
python3 -c 'import yaml' 2>/dev/null || {
  echo "hack/platform-alerts-newest-job-proof.sh needs python3's PyYAML (import yaml)" >&2
  exit 1
}

# The version this chart actually pins, read from the vendored archive
# rather than restated by hand here — the same reason `just lint` reads
# `helm dependency list`'s STATUS column instead of trusting Chart.yaml:
# a number written twice is a number that drifts.
vm_tgz=("$root"/charts/observability-stack/charts/victoria-metrics-k8s-stack-*.tgz)
[ -e "${vm_tgz[0]}" ] || {
  echo "hack/platform-alerts-newest-job-proof.sh: no vendored victoria-metrics-k8s-stack under charts/observability-stack/charts/ — run 'just vendor observability-stack'" >&2
  exit 1
}
vm_version="$(tar -xzOf "${vm_tgz[0]}" victoria-metrics-k8s-stack/Chart.yaml | awk '/^appVersion:/ { print $2 }')"
echo "hack/platform-alerts-newest-job-proof.sh: victoria-metrics-k8s-stack pins VictoriaMetrics $vm_version — using the same image tag"

echo "hack/platform-alerts-newest-job-proof.sh: rendering charts/platform-alerts (minimal case: namespaceSelector=\".*\")"
rendered="$(helm template x "$root/charts/platform-alerts" --values "$root/tests/cases/platform-alerts/minimal/values.yaml")"

cat > "$work/extract_expr.py" <<'PYEOF'
import sys, yaml

rendered = sys.stdin.read()
for doc in yaml.safe_load_all(rendered):
    if not doc or doc.get("kind") != "VMRule":
        continue
    for group in doc["spec"]["groups"]:
        if group["name"] != "platform-alerts.backups":
            continue
        for rule in group["rules"]:
            if rule.get("alert") == "BackupJobFailed":
                sys.stdout.write(rule["expr"])
                sys.exit(0)
sys.exit("BackupJobFailed not found in the rendered platform-alerts.backups group")
PYEOF
new_expr="$(python3 "$work/extract_expr.py" <<<"$rendered")"
[ -n "$new_expr" ] || {
  echo "hack/platform-alerts-newest-job-proof.sh: failed to extract BackupJobFailed's expr from the render" >&2
  exit 1
}

echo
echo "=== NEW expression (this fix, read back off the real render) ==="
echo "$new_expr"
echo

# The expression this chart rendered BEFORE this fix — the bug this
# script reproduces. Not read from the render (this worktree no longer
# has it); this is what tests/golden/platform-alerts/*.yaml held prior to
# this commit, and what `git show` of the commit before this one still
# shows.
old_expr='max by (namespace, job_name) (
  kube_job_status_failed{namespace=~".*"}
) > 0'

echo "=== OLD expression (before this fix, for comparison) ==="
echo "$old_expr"
echo

echo "hack/platform-alerts-newest-job-proof.sh: starting victoria-metrics:$vm_version"
docker run -d --name "$container" -p 127.0.0.1::8428 \
  "victoriametrics/victoria-metrics:$vm_version" -retentionPeriod=100y >/dev/null

port=""
for _ in $(seq 1 30); do
  port="$(docker port "$container" 8428/tcp 2>/dev/null | cut -d: -f2 || true)"
  [ -n "$port" ] && curl -fsS "http://127.0.0.1:$port/health" >/dev/null 2>&1 && break
  sleep 1
done
[ -n "$port" ] || {
  echo "hack/platform-alerts-newest-job-proof.sh: victoria-metrics never became healthy" >&2
  docker logs "$container" >&2 || true
  exit 1
}
echo "hack/platform-alerts-newest-job-proof.sh: victoria-metrics healthy on 127.0.0.1:$port"

# One fixed "now" for every sample AND every query, so the proof does not
# depend on how long the rest of the script takes to run — the timestamp
# is data, not a clock.
now="$(date +%s)"
now_ms="$((now * 1000))"
day=86400
hour=3600

# Four CronJobs (generic names — this is a public repository), each an
# "old" Job created a week ago that failed, plus what its newest run did.
# kube_job_created carries the timestamp "newest" is decided by;
# kube_job_owner is the join key that scopes the rule to CronJob-owned
# Jobs; kube_job_status_failed is the number of failed pods, unchanged
# in shape by this fix — see docs/kube-state-metrics.md for why the
# chart's own KSM relabel chain leaves all three exactly this shaped in
# a real cluster (namespace and job_name are not among the four labels
# that relabel chain rewrites).
cat > "$work/series.prom" <<EOF
# nightly-report: old failed, new SUCCEEDED -> must NOT fire
kube_job_created{namespace="ns-nightly-report",job_name="nightly-report-28123456",cronjob="nightly-report"} $((now - 7 * day)) $now_ms
kube_job_created{namespace="ns-nightly-report",job_name="nightly-report-28129876",cronjob="nightly-report"} $((now - 1 * day)) $now_ms
kube_job_owner{namespace="ns-nightly-report",job_name="nightly-report-28123456",owner_kind="CronJob",owner_name="nightly-report",owner_is_controller="true"} 1 $now_ms
kube_job_owner{namespace="ns-nightly-report",job_name="nightly-report-28129876",owner_kind="CronJob",owner_name="nightly-report",owner_is_controller="true"} 1 $now_ms
kube_job_status_failed{namespace="ns-nightly-report",job_name="nightly-report-28123456"} 1 $now_ms
kube_job_status_failed{namespace="ns-nightly-report",job_name="nightly-report-28129876"} 0 $now_ms

# snapshot-age: old failed, new FAILED -> MUST fire, on the new Job
kube_job_created{namespace="ns-snapshot-age",job_name="snapshot-age-28123456",cronjob="snapshot-age"} $((now - 7 * day)) $now_ms
kube_job_created{namespace="ns-snapshot-age",job_name="snapshot-age-28129999",cronjob="snapshot-age"} $((now - 1 * hour)) $now_ms
kube_job_owner{namespace="ns-snapshot-age",job_name="snapshot-age-28123456",owner_kind="CronJob",owner_name="snapshot-age",owner_is_controller="true"} 1 $now_ms
kube_job_owner{namespace="ns-snapshot-age",job_name="snapshot-age-28129999",owner_kind="CronJob",owner_name="snapshot-age",owner_is_controller="true"} 1 $now_ms
kube_job_status_failed{namespace="ns-snapshot-age",job_name="snapshot-age-28123456"} 1 $now_ms
kube_job_status_failed{namespace="ns-snapshot-age",job_name="snapshot-age-28129999"} 1 $now_ms

# root-generation: old failed, new RUNNING (not yet failed or succeeded) -> must NOT fire
kube_job_created{namespace="ns-root-generation",job_name="root-generation-28123456",cronjob="root-generation"} $((now - 7 * day)) $now_ms
kube_job_created{namespace="ns-root-generation",job_name="root-generation-28130001",cronjob="root-generation"} $((now - 600)) $now_ms
kube_job_owner{namespace="ns-root-generation",job_name="root-generation-28123456",owner_kind="CronJob",owner_name="root-generation",owner_is_controller="true"} 1 $now_ms
kube_job_owner{namespace="ns-root-generation",job_name="root-generation-28130001",owner_kind="CronJob",owner_name="root-generation",owner_is_controller="true"} 1 $now_ms
kube_job_status_failed{namespace="ns-root-generation",job_name="root-generation-28123456"} 1 $now_ms
kube_job_status_failed{namespace="ns-root-generation",job_name="root-generation-28130001"} 0 $now_ms

# standalone: one manually created Job, no CronJob owner at all, FAILED.
# No kube_job_owner series for it on purpose.
kube_job_created{namespace="ns-standalone",job_name="one-off-migration"} $((now - 1 * day)) $now_ms
kube_job_status_failed{namespace="ns-standalone",job_name="one-off-migration"} 1 $now_ms
EOF

echo "hack/platform-alerts-newest-job-proof.sh: importing synthetic kube-state-metrics series for 4 CronJobs (import/prometheus)"
curl -fsS -X POST "http://127.0.0.1:$port/api/v1/import/prometheus" --data-binary "@$work/series.prom" >/dev/null

# The first samples of a brand-new metric name need their index entries
# built before an instant query at their own timestamp can see them —
# observed directly against this same image: an /api/v1/query for
# `time=<the sample's own timestamp>` returns empty immediately after
# import and succeeds a couple of seconds later, for that identical
# query. Not a staleness window (the data is not stale, it is brand
# new); this is indexing catching up. Poll rather than guess a sleep.
echo "hack/platform-alerts-newest-job-proof.sh: waiting for the import to become queryable"
for _ in $(seq 1 30); do
  count="$(curl -fsS "http://127.0.0.1:$port/api/v1/query" \
    --data-urlencode 'query=count(kube_job_status_failed)' \
    --data-urlencode "time=$now" \
    | python3 -c 'import json, sys; d=json.load(sys.stdin)["data"]["result"]; print(d[0]["value"][1] if d else 0)')"
  [ "$count" = "7" ] && break
  sleep 1
done
[ "$count" = "7" ] || {
  echo "hack/platform-alerts-newest-job-proof.sh: only $count/7 kube_job_status_failed series queryable after 30s — aborting" >&2
  exit 1
}

query() { # expr -> the vector result's "namespace/job_name=value" lines, sorted
  curl -fsS "http://127.0.0.1:$port/api/v1/query" \
    --data-urlencode "query=$1" \
    --data-urlencode "time=$now" \
    | python3 -c '
import json, sys
data = json.load(sys.stdin)["data"]["result"]
lines = sorted(
    "{}/{}={}".format(r["metric"].get("namespace"), r["metric"].get("job_name"), r["value"][1])
    for r in data
)
print("\n".join(lines))
'
}

echo
echo "=== querying OLD expression (reproduces the bug) ==="
old_result="$(query "$old_expr")"
echo "${old_result:-<no results>}"

echo
echo "=== querying NEW expression (this fix) ==="
new_result="$(query "$new_expr")"
echo "${new_result:-<no results>}"
echo

fail=0

assert_contains() { # haystack needle description
  case "$1" in
  *"$2"*) ;;
  *)
    echo "FAIL: $3 — expected to find \"$2\"" >&2
    fail=1
    ;;
  esac
}
assert_not_contains() { # haystack needle description
  case "$1" in
  *"$2"*)
    echo "FAIL: $3 — expected NOT to find \"$2\"" >&2
    fail=1
    ;;
  *) ;;
  esac
}

echo "--- OLD expression: reproduces the bug ---"
assert_contains "$old_result" "ns-nightly-report/nightly-report-28123456=1" \
  "OLD did not fire on the week-old, already-fixed nightly-report failure"
assert_contains "$old_result" "ns-root-generation/root-generation-28123456=1" \
  "OLD did not fire on the week-old, already-fixed root-generation failure"
assert_contains "$old_result" "ns-standalone/one-off-migration=1" \
  "OLD did not fire on the standalone Job (sanity: this dataset should reproduce today's behaviour for it)"

echo "--- NEW expression: the four scenarios this fix specifies ---"
assert_not_contains "$new_result" "ns-nightly-report" \
  "NEW fired for nightly-report even though its newest run SUCCEEDED"
assert_contains "$new_result" "ns-snapshot-age/snapshot-age-28129999=1" \
  "NEW did not fire on snapshot-age's newest run, which failed"
assert_not_contains "$new_result" "ns-snapshot-age/snapshot-age-28123456" \
  "NEW fired on snapshot-age's OLD, superseded Job as well as its newest — the join is not narrowing to one Job per CronJob"
assert_not_contains "$new_result" "ns-root-generation" \
  "NEW fired for root-generation even though its newest run is still RUNNING, not failed"
assert_not_contains "$new_result" "ns-standalone" \
  "NEW fired for the standalone Job — this fix scopes BackupJobFailed to kube_job_owner{owner_kind=\"CronJob\"}, so a Job with no CronJob owner must no longer raise it (see charts/platform-alerts/templates/vmrule.yaml's own comment on the choice)"

echo
if [ "$fail" = 0 ]; then
  echo "hack/platform-alerts-newest-job-proof.sh: OK — the OLD expression reproduces the bug (fires on stale, already-fixed failures, and on a standalone Job), and the NEW expression fires only on the newest FAILED Job of each CronJob-owned backup, clearing on a later success or a later run merely being under way, and no longer covering standalone Jobs at all"
fi
exit $fail

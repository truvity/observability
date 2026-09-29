#!/usr/bin/env bash
# hack/platform-alerts-cluster-proof.sh — REAL proof, against a real
# victoria-metrics binary, that charts/platform-alerts' joins are
# cluster-aware (`clusterLabel`, default k8s_cluster_name), and that the
# expressions rendered before this fix are not.
#
# The bug: one metrics store holds several clusters' series, told apart
# only by the cluster label, and two clusters can share a namespace, a Job
# name and a PVC name. A rule that joins WITHOUT the label matches series
# from both clusters: `* on (...) group_left` / `/ on (...)` fail the whole
# query with a duplicate-series error (HTTP 422 — vmalert reports the rule
# health `err` and it never fires), and `and on` / `max by` silently pair
# or merge across clusters.
#
# Two clusters, cluster-a and cluster-b, share every name below. Seeded so
# that ONLY cluster-b is in trouble:
#
#   BackupJobFailed            Job platform/janitor-1: failed on b, not on a
#   CronJobNotSucceeding       CronJob platform/janitor: last success fresh
#                              on a, 3 days old on b
#   VolumeSmallerThanClaimed   PVC platform/data: mounted at 10% of the
#                              claim on b, 97% on a
#   KargoPromotionErrored      Stage/Promotion platform/prod: Errored on a
#                              but its Stage recovered there; Stage still
#                              stuck on b (a false pair across clusters)
#
#   expect, NEW: exactly one alert per rule (BackupJobFailed,
#   CronJobNotSucceeding, VolumeSmallerThanClaimed) carrying
#   k8s_cluster_name="cluster-b"; KargoPromotionErrored silent (cluster-a's
#   promotion is not stuck, cluster-b has no Errored promotion).
#   expect, OLD (clusterLabel: ""): BackupJobFailed and
#   VolumeSmallerThanClaimed FAIL with a duplicate-series error;
#   CronJobNotSucceeding stays SILENT (cluster-a's fresh success masks b);
#   KargoPromotionErrored FIRES wrongly on cluster-a.
#
# Then the same series with NO cluster label at all (a single-cluster
# store): NEW must still fire on every rule — a label absent on both sides
# matches on empty.
#
# Finally one deliberately mixed case, documented in values.yaml: the label
# on one side of a join only. That does not match under the new
# expressions; a store either stamps the label on everything or on nothing
# (remote emitters force it on every series), and `clusterLabel: ""` is the
# opt-out for one that does not.
#
# Needs Docker, curl, helm and python3 with PyYAML. Deliberately NOT part of
# `check` or CI, like the other platform-alerts proofs: run by hand.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d -t platform-alerts-cluster-proof.XXXXXX)"
container=platform-alerts-cluster-proof-vm
me="hack/platform-alerts-cluster-proof.sh"

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
        for rule in group["rules"]:
            if rule.get("alert") == alert:
                sys.stdout.write(rule["expr"])
                sys.exit(0)
sys.exit(alert + " not found in the render")
PYEOF

new_render="$(helm template x "$root/charts/platform-alerts" \
  --values "$root/tests/cases/platform-alerts/minimal/values.yaml" --set groups.kargo.enabled=true)"
old_render="$(helm template x "$root/charts/platform-alerts" \
  --values "$root/tests/cases/platform-alerts/minimal/values.yaml" --set groups.kargo.enabled=true --set clusterLabel=)"
alerts=(BackupJobFailed CronJobNotSucceeding VolumeSmallerThanClaimed KargoPromotionErrored)
declare -A expr
for alert in "${alerts[@]}"; do
  for v in new old; do
    render="$new_render"; [ "$v" = old ] && render="$old_render"
    expr["$v-$alert"]="$(python3 "$work/extract.py" "$alert" <<<"$render")"
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
  port="$(docker port "$container" 8428/tcp 2>/dev/null | head -1 | cut -d: -f2 || true)"
  [ -n "$port" ] && curl -fsS "http://127.0.0.1:$port/health" >/dev/null 2>&1 && break
  port=""
  sleep 1
done
[ -n "$port" ] || { echo "$me: victoria-metrics never became healthy" >&2; exit 1; }

now="$(date +%s)"
ms="$((now * 1000))"
day=86400
hour=3600

# series CLUSTERLABELS — the kube-state-metrics / kubelet series for one
# cluster. $1 is the label fragment placed on every series (`k8s_cluster_name=
# "cluster-b",` or empty); $2 the cluster's role: healthy or broken.
series() {
  local c="$1" role="$2" failed=0 last=$((now - hour)) cap=970 stagecond=1 phase=0
  [ "$role" = broken ] && { failed=1; last=$((now - 3 * day)); cap=100; stagecond=0; }
  # cluster-a (healthy) has an Errored promotion whose Stage has recovered;
  # cluster-b (broken) has a stuck Stage and no Errored promotion.
  [ "$role" = healthy ] && phase=1
  cat <<PROM
kube_job_created{${c}namespace="platform",job_name="janitor-1"} $((now - hour)) $ms
kube_job_owner{${c}namespace="platform",job_name="janitor-1",owner_kind="CronJob",owner_name="janitor",owner_is_controller="true"} 1 $ms
kube_job_status_failed{${c}namespace="platform",job_name="janitor-1"} $failed $ms
kube_cronjob_status_last_successful_time{${c}namespace="platform",cronjob="janitor"} $last $ms
kubelet_volume_stats_capacity_bytes{${c}namespace="platform",persistentvolumeclaim="data"} $cap $ms
kube_persistentvolumeclaim_resource_requests_storage_bytes{${c}namespace="platform",persistentvolumeclaim="data"} 1000 $ms
kargo_stage_condition{${c}namespace="platform",stage="prod",type="Ready",reason="LastPromotionErrored"} $stagecond $ms
kargo_promotion_phase{${c}namespace="platform",stage="prod",promotion="prod-1",phase="Errored"} $phase $ms
PROM
}

count_all() {
  curl -fsS "http://127.0.0.1:$port/api/v1/query" \
    --data-urlencode 'query=count({__name__=~"kube_.*|kubelet_.*|kargo_.*"})' --data-urlencode "time=$now" \
    | python3 -c 'import json, sys; d=json.load(sys.stdin)["data"]["result"]; print(d[0]["value"][1] if d else 0)'
}
load() { # file prom-lines
  curl -fsS -X POST "http://127.0.0.1:$port/api/v1/import/prometheus" --data-binary "@$1" >/dev/null
  local want got=0
  want="$(wc -l <"$1" | tr -d ' ')"
  for _ in $(seq 1 30); do
    got="$(count_all)"; [ "$got" = "$want" ] && return 0; sleep 1
  done
  echo "$me: only $got/$want series queryable" >&2; exit 1
}
wipe() {
  curl -fsS -X POST "http://127.0.0.1:$port/api/v1/admin/tsdb/delete_series" \
    --data-urlencode 'match[]={__name__=~"kube_.*|kubelet_.*|kargo_.*"}' >/dev/null
  curl -fsS "http://127.0.0.1:$port/internal/resetRollupResultCache" >/dev/null || true
  sleep 1
}

query() { # expr -> "ERR <http> <message>" | "<cluster>" lines | "<none>"
  curl -sS -w '\n%{http_code}' "http://127.0.0.1:$port/api/v1/query" \
    --data-urlencode "query=$1" --data-urlencode "time=$now" \
    | python3 -c '
import json, sys
raw = sys.stdin.read()
body, _, code = raw.rpartition("\n")
try:
    d = json.loads(body)
except ValueError:
    d = {}
if code != "200" or d.get("status") != "success":
    import re
    msg = str(d.get("error", body)).replace("\n", " ")
    m = re.search(r"(duplicate[^;]*|many-to-many[^;]*)", msg)
    print("ERR %s %s" % (code, m.group(1)[:200] if m else msg[-200:]))
else:
    r = d["data"]["result"]
    print("\n".join("%s/%s" % (x["metric"].get("k8s_cluster_name", "<no-cluster-label>"), x["metric"].get("namespace", "?")) for x in r) or "<none>")
'
}

fail=0
check() { # phase alert want
  local phase="$1" alert="$2" want="$3" v="$4" got
  got="$(query "${expr[$v-$alert]}" | sort | tr '\n' ' ' | sed 's/ $//')"
  case "$want" in
    ERR) if [[ "$got" == ERR\ 422* && "$got" == *uplicate* ]]; then ok=1; else ok=0; fi ;;
    *)   if [ "$got" = "$want" ]; then ok=1; else ok=0; fi ;;
  esac
  if [ "$ok" = 1 ]; then echo "ok    [$phase/$v] $alert -> $got"; else echo "FAIL  [$phase/$v] $alert -> $got (wanted $want)" >&2; fail=1; fi
}

echo "##### two clusters share every name; only cluster-b is in trouble"
{ series 'k8s_cluster_name="cluster-a",' healthy; series 'k8s_cluster_name="cluster-b",' broken; } > "$work/two.prom"
load "$work/two.prom"
for a in BackupJobFailed VolumeSmallerThanClaimed; do
  check two-clusters "$a" "ERR" old
  check two-clusters "$a" "cluster-b/platform" new
done
check two-clusters CronJobNotSucceeding "<none>" old
check two-clusters CronJobNotSucceeding "cluster-b/platform" new
check two-clusters KargoPromotionErrored "cluster-a/platform" old
check two-clusters KargoPromotionErrored "<none>" new

echo
echo "##### single cluster, no cluster label anywhere: the broken one, alone"
wipe
series '' broken > "$work/one.prom"
# the healthy-side Errored promotion is not needed here: give the single
# cluster one Errored promotion whose Stage is stuck, so the Kargo pair fires.
sed -i 's/^\(kargo_promotion_phase.*Errored"} \)0 /\11 /' "$work/one.prom"
load "$work/one.prom"
for a in "${alerts[@]}"; do
  check single-cluster "$a" "<no-cluster-label>/platform" new
  check single-cluster "$a" "<no-cluster-label>/platform" old
done

echo
echo "##### mixed: label on the claim side only (documented, not supported)"
wipe
{ series '' broken | grep -v '^kube_persistentvolumeclaim'; \
  echo "kube_persistentvolumeclaim_resource_requests_storage_bytes{k8s_cluster_name=\"cluster-b\",namespace=\"platform\",persistentvolumeclaim=\"data\"} 1000 $ms"; } > "$work/mixed.prom"
load "$work/mixed.prom"
check mixed VolumeSmallerThanClaimed "<none>" new
echo "      (old expression, for comparison: $(query "${expr[old-VolumeSmallerThanClaimed]}"))"

echo
if [ "$fail" = 0 ]; then
  echo "$me: OK — every join is per cluster: two clusters sharing names error out under the old expressions and fire per cluster under the new, and a single-cluster store still fires"
fi
exit $fail

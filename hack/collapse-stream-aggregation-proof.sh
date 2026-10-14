#!/usr/bin/env bash
# hack/collapse-stream-aggregation-proof.sh — REAL proof, against the pinned
# vmagent and VictoriaMetrics single binaries, that `metrics.scrape.collapse`
# of charts/observability-emitters does what docs/reference.md says:
#
#   - the gauges of two pods of a collapsed namespace come out as ONE series
#     holding their SUM, with no `pod`, `uid`, `container_id` ...;
#   - their counters come out as ONE series that rose by the sum of both pods'
#     increases (one pod restarting from zero does not lower it);
#   - a timestamp gauge comes out as the larger of the two;
#   - the same metric in another namespace, and an application's own metric in
#     the collapsed namespace, are stored untouched;
#   - the node-level series of two nodes of a collapsing pool come out as one
#     per pool (summed, no `node`/`instance`), while a pod-level series on such
#     a node, `up`, and a node of another pool are stored untouched;
#   - a tenancy label (`k8s_cluster_name`) is never merged away.
#
# The rules are read off the real render (helm template of the `collapse`
# golden case), not retyped; only the window is shortened (to 5s, from 1m).
#
# Needs docker, curl, python3 (PyYAML) and helm. Not part of `check` or CI: a
# run-by-hand proof.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d -t collapse-proof.XXXXXX)"
vm=collapse-proof-vm
va=collapse-proof-vmagent
cleanup() {
  docker rm -f "$vm" "$va" >/dev/null 2>&1 || true
  rm -rf "$work"
}
[ -n "${KEEP:-}" ] || trap cleanup EXIT

for tool in docker curl python3 helm; do
  command -v "$tool" >/dev/null 2>&1 || { echo "needs $tool on PATH" >&2; exit 1; }
done

helm template x "$root/charts/observability-emitters" --namespace default \
  -f "$root/tests/cases/observability-emitters/collapse/values.yaml" > "$work/render.yaml"
python3 - "$work" <<'PY'
import sys, yaml
work = sys.argv[1]
for d in yaml.safe_load_all(open(work + "/render.yaml")):
    if d and d.get("kind") == "VMAgent":
        rules = d["spec"]["streamAggrConfig"]["rules"]
        for r in rules:
            r["interval"] = "5s"
            r["dedup_interval"] = "5s"
        yaml.safe_dump(rules, open(work + "/rules.yaml", "w"))
        break
else:
    raise SystemExit("no VMAgent in the render")
PY

docker run -d --name "$vm" -p 127.0.0.1:18428:8428 victoriametrics/victoria-metrics:v1.152.0 -search.latencyOffset=0s >/dev/null
vmip="$(docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$vm")"
docker run -d --name "$va" -p 127.0.0.1:18429:8429 \
  -v "$work/rules.yaml:/rules.yaml:ro" victoriametrics/vmagent:v1.152.0 \
  -remoteWrite.url="http://$vmip:8428/api/v1/write" -streamAggr.config=/rules.yaml \
  -remoteWrite.flushInterval=1s >/dev/null
for _ in $(seq 1 30); do curl -sf -m 2 http://127.0.0.1:18429/health >/dev/null 2>&1 && break; sleep 1; done

push() { curl -sf -m 10 --data-binary @- http://127.0.0.1:18429/api/v1/import/prometheus; }
round() { # $1 = counter for pod a, $2 = counter for pod b
  push <<EOF
container_memory_working_set_bytes{namespace="arc-runners-7",pod="runner-a",uid="u1",container_id="c1",id="/a",image="img",container="runner",k8s_cluster_name="c1",node="n1",instance="n1:10250"} 100
container_memory_working_set_bytes{namespace="arc-runners-7",pod="runner-b",uid="u2",container_id="c2",id="/b",image="img",container="runner",k8s_cluster_name="c1",node="n2",instance="n2:10250"} 200
container_memory_working_set_bytes{namespace="arc-runners-7",pod="runner-b",container="runner",k8s_cluster_name="c2"} 50
container_cpu_usage_seconds_total{namespace="arc-runners-7",pod="runner-a",uid="u1",container="runner",k8s_cluster_name="c1"} $1
container_cpu_usage_seconds_total{namespace="arc-runners-7",pod="runner-b",uid="u2",container="runner",k8s_cluster_name="c1"} $2
kube_pod_start_time{namespace="arc-runners-7",pod="runner-a",uid="u1",k8s_cluster_name="c1"} 1000
kube_pod_start_time{namespace="arc-runners-7",pod="runner-b",uid="u2",k8s_cluster_name="c1"} 2000
container_memory_working_set_bytes{namespace="team-a",pod="web-0",container="app",k8s_cluster_name="c1"} 7
my_app_requests_total{namespace="arc-runners-7",pod="runner-a",k8s_cluster_name="c1"} 1
kubelet_running_pods{nodepool="ci-pool",node="n1",instance="n1:10250",job="kubelet",k8s_cluster_name="c1"} 3
kubelet_running_pods{nodepool="ci-pool",node="n2",instance="n2:10250",job="kubelet",k8s_cluster_name="c1"} 4
kubelet_running_pods{node="n3",instance="n3:10250",job="kubelet",k8s_cluster_name="c1"} 9
container_memory_working_set_bytes{nodepool="ci-pool",namespace="kube-system",pod="proxy-x",container="proxy",node="n1",k8s_cluster_name="c1"} 11
up{nodepool="ci-pool",node="n1",instance="n1:10250",job="kubelet",k8s_cluster_name="c1"} 1
EOF
}
round 10 5; sleep 6; round 20 8; sleep 6; round 25 2; sleep 8   # pod b restarts at the third round

vmq() { curl -sf -m 10 -G "http://127.0.0.1:18428/api/v1/query" --data-urlencode "query=$1"; }
sleep 3
fail=0
check() { # name, query, expected count of series, python predicate over the result list
  local out
  # last_over_time: the store ends a series' lookback soon after its last
  # sample, and the windows here are seconds.
  out="$(vmq "${FN:-last_over_time}($2[15m])")"
  if ! python3 -c '
import json, sys
r = json.loads(sys.argv[1])["data"]["result"]
n, pred = int(sys.argv[2]), sys.argv[3]
ok = len(r) == n and eval(pred)
if not ok: print(json.dumps(r, indent=1))
sys.exit(0 if ok else 1)' "$out" "$3" "$4"; then
    echo "FAIL: $1" >&2; fail=1
  else
    echo "ok:   $1"
  fi
}
# A sum_samples window with no input reports 0 until the staleness interval
# runs out, so the gauge sum is read as the maximum over the run.
FN=max_over_time check "pod gauges of a collapsed namespace sum into one series per tenant label" \
  'container_memory_working_set_bytes{namespace="arc-runners-7"}' 2 \
  'sorted(float(x["value"][1]) for x in r) == [50.0, 300.0] and all("pod" not in x["metric"] and "uid" not in x["metric"] and "container_id" not in x["metric"] and "node" not in x["metric"] for x in r)'
check "the tenancy label survives: the two clusters stay two series" \
  'container_memory_working_set_bytes{namespace="arc-runners-7"}' 2 'sorted(x["metric"]["k8s_cluster_name"] for x in r) == ["c1", "c2"]'
check "counters: one series, rose by the sum of increases, a restart is not a drop" \
  'container_cpu_usage_seconds_total{namespace="arc-runners-7"}' 1 \
  '15 <= float(r[0]["value"][1]) <= 20 and "pod" not in r[0]["metric"]'
if [ "$(vmq 'resets(container_cpu_usage_seconds_total{namespace="arc-runners-7"}[15m])' | python3 -c 'import json,sys; print(json.load(sys.stdin)["data"]["result"][0]["value"][1])')" = 0 ]; then
  echo "ok:   counters: the sum never fell (pod b restarted from zero)"
else
  echo "FAIL: counters: the sum fell" >&2; fail=1
fi
check "timestamp gauges: max" \
  'kube_pod_start_time{namespace="arc-runners-7"}' 1 'float(r[0]["value"][1]) == 2000.0'
check "another namespace is untouched" \
  'container_memory_working_set_bytes{namespace="team-a"}' 1 'r[0]["metric"]["pod"] == "web-0"'
check "an application metric in the collapsed namespace is untouched" \
  'my_app_requests_total{namespace="arc-runners-7"}' 1 'r[0]["metric"]["pod"] == "runner-a"'
check "node-level series of the pool: one, summed, no node/instance" \
  'kubelet_running_pods{nodepool="ci-pool"}' 1 \
  'float(r[0]["value"][1]) == 7.0 and "node" not in r[0]["metric"] and "instance" not in r[0]["metric"]'
check "a node of another pool is untouched" \
  'kubelet_running_pods{node="n3"}' 1 'float(r[0]["value"][1]) == 9.0'
check "a pod-level series on a pool node is untouched" \
  'container_memory_working_set_bytes{pod="proxy-x"}' 1 'r[0]["metric"]["node"] == "n1"'
check "up is untouched" 'up{node="n1"}' 1 'True'
exit $fail

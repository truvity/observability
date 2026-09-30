#!/usr/bin/env bash
# hack/k8s-stack-cluster-label-proof.sh — REAL proof that the vendored
# victoria-metrics-k8s-stack's default recording rules carry the cluster
# identity as `k8s_cluster_name`, and that upstream's own `cluster` form
# does not.
#
# The defect: upstream's default rules join and aggregate on a label named
# `cluster` (`* on (namespace, pod, cluster) group_left`, `by (namespace,
# cluster)`). Every series in this chart's stores carries the cluster as
# `k8s_cluster_name` and none carries `cluster`, so the recorded series lost
# their cluster identity and, on a store several clusters write to, the
# same namespace and pod matched across clusters.
#
# Three steps, none of them a stand-in:
#   1. Render charts/observability-stack and read the sync job's own
#      ConfigMap: `common.clusterLabel` must be `k8s_cluster_name`.
#   2. Run the vendored sync job's REAL image (the tag the chart renders)
#      against that config and a throwaway fake API server that records
#      what the job applies. The job fetches upstream's rule files over the
#      network and rewrites them at run time; the recorded VMRule for
#      node_namespace_pod_container:container_cpu_usage_seconds_total:sum_irate
#      is read back from what it applied. Run once with the label this
#      chart sets and once with upstream's `cluster`.
#   3. Seed two clusters that share a namespace and a pod name into a real
#      victoria-metrics (the vendored appVersion) and evaluate both
#      expressions.
#
# Needs Docker, curl, helm, python3 with PyYAML, and network access to
# raw.githubusercontent.com and ghcr.io. Deliberately NOT part of `check` or
# CI: run by hand.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d -t k8s-stack-cluster-label-proof.XXXXXX)"
vm=k8s-stack-cluster-label-proof-vm
me="hack/k8s-stack-cluster-label-proof.sh"
api_pid=""
cleanup() {
  docker rm -f "$vm" >/dev/null 2>&1 || true
  [ -n "$api_pid" ] && kill "$api_pid" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

for tool in docker curl python3 helm; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$me needs $tool on PATH" >&2; exit 1; }
done
python3 -c 'import yaml' 2>/dev/null || { echo "$me needs python3's PyYAML" >&2; exit 1; }

vm_tgz=("$root"/charts/observability-stack/charts/victoria-metrics-k8s-stack-*.tgz)
vm_version="$(tar -xzOf "${vm_tgz[0]}" victoria-metrics-k8s-stack/Chart.yaml | awk '/^appVersion:/ { print $2 }')"
echo "$me: victoria-metrics:$vm_version"

echo "##### 1. the sync job's config, rendered"
helm template x "$root/charts/observability-stack" \
  --values "$root/tests/cases/observability-stack/minimal/values.yaml" > "$work/render.yaml"
python3 - "$work" <<'PYEOF'
import sys, yaml
work = sys.argv[1]
for d in yaml.safe_load_all(open(work + "/render.yaml")):
    if d and d["kind"] == "ConfigMap" and d["metadata"]["name"].endswith("sync-job-config"):
        open(work + "/config.yaml", "w").write(d["data"]["config.yaml"])
    if d and d["kind"] == "Job" and "sync-job" in d["metadata"]["name"]:
        c = d["spec"]["template"]["spec"]["containers"][0]
        open(work + "/image", "w").write(c["image"])
PYEOF
sed -n '/^common:/,/^dashboards:/p' "$work/config.yaml" | head -3
grep -q '^  clusterLabel: "k8s_cluster_name"$' "$work/config.yaml" \
  || { echo "FAIL: rendered sync-job config does not carry k8s_cluster_name" >&2; exit 1; }
image="$(cat "$work/image")"
echo "sync job image: $image"

echo
echo "##### 2. what the real sync job applies"
cat > "$work/fakeapi.py" <<'PYEOF'
import http.server, json, sys
class H(http.server.BaseHTTPRequestHandler):
    def _r(self, code, body):
        b = json.dumps(body).encode()
        self.send_response(code); self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def do_GET(self):
        if self.path.split("?")[0].endswith("s"):
            return self._r(200, {"kind": "List", "apiVersion": "v1", "items": [], "metadata": {}})
        self._r(404, {"kind": "Status", "apiVersion": "v1", "status": "Failure", "reason": "NotFound", "code": 404, "message": "nf"})
    def _w(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        open(sys.argv[2], "ab").write(json.dumps({"path": self.path, "body": body.decode()}).encode() + b"\n")
        try: self._r(200, json.loads(body))
        except Exception: self._r(200, {})
    do_POST = do_PUT = do_PATCH = _w
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
PYEOF
cat > "$work/kubeconfig" <<'EOF'
apiVersion: v1
kind: Config
clusters: [{name: c, cluster: {server: "http://127.0.0.1:18081"}}]
users: [{name: u, user: {token: t}}]
contexts: [{name: c, context: {cluster: c, user: u}}]
current-context: c
EOF
record=node_namespace_pod_container:container_cpu_usage_seconds_total:sum_irate
declare -A expr
for label in k8s_cluster_name cluster; do
  : > "$work/writes-$label.jsonl"
  python3 "$work/fakeapi.py" 18081 "$work/writes-$label.jsonl" & api_pid=$!
  sleep 1
  mkdir -p "$work/etc-$label"
  sed "s/^  clusterLabel: .*/  clusterLabel: \"$label\"/" "$work/config.yaml" > "$work/etc-$label/config.yaml"
  docker run --rm --network host -v "$work/etc-$label:/etc/config:ro" -v "$work/kubeconfig:/kc:ro" \
    -e KUBECONFIG=/kc "$image" 2>&1 | tail -1
  kill "$api_pid"; api_pid=""
  expr[$label]="$(python3 - "$work/writes-$label.jsonl" "$record" <<'PYEOF'
import json, sys
for line in open(sys.argv[1]):
    d = json.loads(json.loads(line)["body"])
    for g in d["spec"]["groups"]:
        for r in g["rules"]:
            if r.get("record") == sys.argv[2]:
                sys.stdout.write(" ".join(r["expr"].split()))
                sys.exit(0)
sys.exit("record rule not applied")
PYEOF
  )"
  echo "--- clusterLabel: $label"
  echo "${expr[$label]}"
done
[[ "${expr[k8s_cluster_name]}" == *"on(k8s_cluster_name,namespace,pod)"* && "${expr[k8s_cluster_name]}" != *"cluster,"* ]] \
  || { echo "FAIL: the rule was not rewritten to k8s_cluster_name" >&2; exit 1; }
[[ "${expr[cluster]}" == *"on(cluster,namespace,pod)"* ]] \
  || { echo "FAIL: upstream's own form is not the cluster form" >&2; exit 1; }

echo
echo "##### 3. two clusters sharing namespace and pod names, on a real store"
docker run -d --name "$vm" -p 127.0.0.1::8428 "victoriametrics/victoria-metrics:$vm_version" -retentionPeriod=100y >/dev/null
port=""
for _ in $(seq 1 30); do
  port="$(docker port "$vm" 8428/tcp 2>/dev/null | head -1 | cut -d: -f2 || true)"
  [ -n "$port" ] && curl -fsS "http://127.0.0.1:$port/health" >/dev/null 2>&1 && break
  port=""; sleep 1
done
[ -n "$port" ] || { echo "$me: victoria-metrics never became healthy" >&2; exit 1; }
# The store hides the newest 30s of data from instant queries
# (-search.latencyOffset), so the two samples sit a minute and two minutes
# back and the queries are evaluated at the later one.
now="$(( $(date +%s) - 60 ))"
cad='job="kubelet",metrics_path="/metrics/cadvisor",image="img",namespace="app",pod="web-0",container="web"'
# cluster-a burns 1 core on node-a1, cluster-b 3 cores on node-b1.
{
  for c in a:1:node-a1 b:3:node-b1; do
    IFS=: read -r n rate node <<<"$c"
    echo "container_cpu_usage_seconds_total{k8s_cluster_name=\"cluster-$n\",$cad} 0 $(( (now - 60) * 1000 ))"
    echo "container_cpu_usage_seconds_total{k8s_cluster_name=\"cluster-$n\",$cad} $((rate * 60)) $((now * 1000))"
    echo "kube_pod_info{k8s_cluster_name=\"cluster-$n\",namespace=\"app\",pod=\"web-0\",node=\"$node\"} 1 $((now * 1000))"
  done
} > "$work/seed.prom"
curl -fsS -X POST "http://127.0.0.1:$port/api/v1/import/prometheus" --data-binary "@$work/seed.prom" >/dev/null
n=0
for _ in $(seq 1 30); do
  n="$(curl -fsS "http://127.0.0.1:$port/api/v1/query" --data-urlencode 'query=count({__name__=~"container_cpu_usage_seconds_total|kube_pod_info"})' --data-urlencode "time=$now" \
    | python3 -c 'import json,sys; r=json.load(sys.stdin)["data"]["result"]; print(r[0]["value"][1] if r else 0)')"
  [ "$n" = 4 ] && break; sleep 1
done
[ "$n" = 4 ] || { echo "$me: seeded series never became queryable ($n/4)" >&2; exit 1; }

show() { # expr -> one line per series, or ERR
  curl -sS "http://127.0.0.1:$port/api/v1/query" --data-urlencode "query=$1" --data-urlencode "time=$now" \
  | python3 -c '
import json, sys
d = json.load(sys.stdin)
if d.get("status") != "success":
    print("ERR", str(d.get("error"))[:200]); sys.exit()
for x in sorted(d["data"]["result"], key=lambda x: str(x["metric"])):
    m = x["metric"]
    print("  k8s_cluster_name=%s node=%s cpu=%.2f" % (m.get("k8s_cluster_name", "<absent>"), m.get("node", "?"), float(x["value"][1])))
print("  (%d series)" % len(d["data"]["result"]))'
}
fail=0
echo "rewritten (k8s_cluster_name):"; new="$(show "${expr[k8s_cluster_name]}")"; echo "$new"
echo "upstream's (cluster):";          old="$(show "${expr[cluster]}")"; echo "$old"
grep -q 'k8s_cluster_name=cluster-a node=node-a1 cpu=1.00' <<<"$new" \
  && grep -q 'k8s_cluster_name=cluster-b node=node-b1 cpu=3.00' <<<"$new" \
  && grep -q '(2 series)' <<<"$new" \
  || { echo "FAIL: the rewritten rule is not per cluster" >&2; fail=1; }
if grep -q '(2 series)' <<<"$old" && ! grep -q '<absent>' <<<"$old"; then
  echo "FAIL: the cluster form was expected to lose the cluster identity" >&2; fail=1
fi
echo
[ "$fail" = 0 ] && echo "$me: OK — the rewritten rule returns one series per cluster carrying k8s_cluster_name; the cluster form collapses the two clusters into one series with no cluster identity (or errors)"
exit $fail

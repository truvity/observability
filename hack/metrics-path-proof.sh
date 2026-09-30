#!/usr/bin/env bash
# hack/metrics-path-proof.sh — REAL proof, against the pinned vmagent and
# VictoriaMetrics single binaries, that the kubelet and cadvisor jobs of
# charts/observability-emitters write the `metrics_path` label the
# kube-prometheus dashboards select on, and that the shipped kubelet
# dashboard's own `cluster` variable then returns a value.
#
# The bug this guards: the kubelet dashboard selects every query, its
# `cluster` variable included, on `up{job="kubelet", metrics_path="/metrics"}`.
# The chart's node jobs wrote no `metrics_path`, so the variable was empty
# and every panel read "No data" with the series sitting in the store.
#
# The same run also proves the cadvisor series' `job` label (0.13.0): the
# cadvisor scrape stores `job="kubelet", metrics_path="/metrics/cadvisor"`,
# the kube-prometheus convention, so that
#   - the kubernetes-mixin recording rule
#     `node_namespace_pod_container:container_cpu_usage_seconds_total:sum_irate`
#     (fetched verbatim from the source the k8s-stack's sync job pulls its
#     default rules from, kube-prometheus's kubernetesControlPlane rule
#     file; the stack chart vendors none of them, and its `cluster` label
#     is renamed to the chart's `k8s_cluster_name` exactly as the stack
#     does) returns data, and returns none in the control case (the
#     `cadvisorAsKubeletJob: false` render, `job="cadvisor"`);
#   - the kubelet dashboard's "Running Kubelets" query still counts one per
#     node although `up{job="kubelet"}` now carries two `metrics_path`s.
#
# Two containers' worth of real software, four cases:
#   fixture  - a python http.server answering GET /metrics (a kubelet
#              series) and GET /metrics/cadvisor (a cadvisor series).
#   vmagent  - the image charts/observability-emitters pins, scraping the
#              fixture at BOTH paths with the chart's own rendered
#              relabel_configs and metric_relabel_configs (extracted from
#              `helm template`, not retyped), file_sd standing in for the
#              node discovery there is no cluster to provide.
#   vmsingle - the version charts/observability-stack pins.
# Case "chart" is the render as it is. Case "control" is the same render
# with the metrics_path step removed (what shipped before), and must show
# the empty variable: a proof that cannot fail proves nothing. Case
# "oldjob" is the opt-out render (`cadvisorAsKubeletJob: false`, byte for
# byte the 0.12.x render): the recording rule must return nothing there.
#
# Needs Docker, curl, python3 (with PyYAML) and helm. Deliberately NOT part
# of `check` or CI, like the other hack/*-proof.sh: a run-by-hand proof.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d -t metrics-path-proof.XXXXXX)"
net=metrics-path-proof-net
cleanup() {
  docker ps -aq --filter "label=metrics-path-proof" | xargs -r docker rm -f >/dev/null 2>&1 || true
  docker network rm "$net" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

for tool in docker curl python3 helm; do
  command -v "$tool" >/dev/null 2>&1 || { echo "hack/metrics-path-proof.sh needs $tool on PATH" >&2; exit 1; }
done
python3 -c 'import yaml' 2>/dev/null || { echo "hack/metrics-path-proof.sh needs python3's PyYAML" >&2; exit 1; }

vmagent_tag="$(awk '/^metrics:/{m=1} m && /^ *tag: /{print $2; exit}' "$root/charts/observability-emitters/values.yaml")"
vm_tgz=("$root"/charts/observability-stack/charts/victoria-metrics-k8s-stack-*.tgz)
vmsingle_tag="$(tar -xzOf "${vm_tgz[0]}" victoria-metrics-k8s-stack/Chart.yaml | awk '/^appVersion:/ { print $2 }')"
echo "vmagent $vmagent_tag (the chart's pin), victoria-metrics $vmsingle_tag (the stack's pin)"

helm template x "$root/charts/observability-emitters" \
  --values "$root/tests/cases/observability-emitters/minimal/values.yaml" > "$work/render.yaml"
helm template x "$root/charts/observability-emitters" \
  --values "$root/tests/cases/observability-emitters/minimal/values.yaml" \
  --set metrics.scrape.cadvisorAsKubeletJob=false > "$work/render-oldjob.yaml"

# The recording rule and the dashboard query the cases are judged by.
rule_url=https://raw.githubusercontent.com/prometheus-operator/kube-prometheus/main/manifests/kubernetesControlPlane-prometheusRule.yaml
curl -fsS -m 60 "$rule_url" -o "$work/rules.yaml" || { echo "could not fetch $rule_url" >&2; exit 1; }
rule_expr="$(python3 - "$work/rules.yaml" <<'PYEOF'
import re, sys, yaml
want = "node_namespace_pod_container:container_cpu_usage_seconds_total:sum_irate"
for g in yaml.safe_load(open(sys.argv[1]))["spec"]["groups"]:
    for r in g["rules"]:
        if r.get("record") == want:
            print(re.sub(r"\bcluster\b", "k8s_cluster_name", r["expr"]).strip())
            sys.exit(0)
sys.exit("rule not found")
PYEOF
)"
echo "recording rule (upstream, cluster label renamed to k8s_cluster_name as the stack's sync job does):"
echo "$rule_expr" | sed 's/^/  /'
running_kubelets="$(python3 - "$root/charts/observability-dashboards/dashboards/kubelet.json" <<'PYEOF'
import json, sys
d = json.load(open(sys.argv[1]))
def walk(ps):
    for p in ps:
        yield p
        yield from walk(p.get("panels", []))
p = next(p for p in walk(d["panels"]) if p.get("title") == "Running Kubelets")
print(p["targets"][0]["expr"].replace("$cluster", "example-cluster"))
PYEOF
)"
echo "kubelet dashboard 'Running Kubelets': $running_kubelets"

# The two node jobs, as rendered: everything but service discovery and TLS.
python3 - "$work" <<'PYEOF'
import sys, yaml, json
work = sys.argv[1]
def load(name):
    agent = next(d for d in yaml.safe_load_all(open(work + "/" + name)) if d and d.get("kind") == "VMAgent")
    return {j["job_name"]: j for j in yaml.safe_load(agent["spec"]["inlineScrapeConfig"])}
jobs = load("render.yaml")
oldjobs = load("render-oldjob.yaml")
def fixture_job(j, strip):
    relabel = [s for s in j["relabel_configs"] if not (strip and s.get("target_label") == "metrics_path")]
    return {
        "job_name": j["job_name"], "scrape_interval": "2s", "scheme": "http",
        "metrics_path": j.get("metrics_path", "/metrics"),
        "honor_labels": j["honor_labels"],
        "file_sd_configs": [{"files": ["/config/targets.yml"]}],
        "relabel_configs": relabel,
        "metric_relabel_configs": j["metric_relabel_configs"],
    }
for case, strip, js in (("chart", False, jobs), ("control", True, jobs), ("oldjob", False, oldjobs)):
    cfg = {"scrape_configs": [fixture_job(js["kubelet"], strip), fixture_job(js["cadvisor"], strip)]}
    yaml.safe_dump(cfg, open("%s/scrape-%s.yml" % (work, case), "w"), sort_keys=False)
print("rendered relabel_configs of the kubelet job (chart case):")
print(yaml.safe_dump(fixture_job(jobs["kubelet"], False)["relabel_configs"], sort_keys=False))
PYEOF

# The dashboard's own variable, read from the shipped JSON, not retyped.
cluster_query="$(python3 - "$root/charts/observability-dashboards/dashboards/kubelet.json" <<'PYEOF'
import json, sys, re
d = json.load(open(sys.argv[1]))
v = next(v for v in d["templating"]["list"] if v["name"] == "cluster")
q = v.get("definition") or v["query"]
print(q["query"] if isinstance(q, dict) else q)
PYEOF
)"
selector="$(sed -E 's/^label_values\((.*),[[:space:]]*k8s_cluster_name\)$/\1/' <<<"$cluster_query")"
echo "kubelet dashboard variable \$cluster: $cluster_query"
echo "  -> selector: $selector"

cat > "$work/metrics.txt" <<'EOM'
# TYPE kubelet_running_pods gauge
kubelet_running_pods 7
# TYPE kubelet_node_name gauge
kubelet_node_name{node="node-a"} 1
EOM
cat > "$work/cadvisor.txt" <<'EOM'
# TYPE container_cpu_usage_seconds_total counter
container_cpu_usage_seconds_total{namespace="team-a",pod="web-0",container="app",image="registry.example/app:1"} 12.5
EOM
cat > "$work/server.py" <<'EOM'
import http.server
FILES = {"/metrics": "/fixture/metrics.txt", "/metrics/cadvisor": "/fixture/cadvisor.txt"}
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        f = FILES.get(self.path)
        if not f:
            self.send_response(404); self.end_headers(); return
        body = open(f, "rb").read()
        self.send_response(200)
        self.send_header("Content-Type", "text/plain; version=0.0.4")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers(); self.wfile.write(body)
    def log_message(self, *a): pass
import threading
# Two "nodes": the same fixture on two ports, so a count of kubelets can be
# told from a count of scrape paths.
threading.Thread(target=http.server.HTTPServer(("0.0.0.0", 8081), H).serve_forever, daemon=True).start()
http.server.HTTPServer(("0.0.0.0", 8080), H).serve_forever()
EOM

docker network create "$net" >/dev/null
docker run -d --label metrics-path-proof --name mpp-fixture --network "$net" \
  -v "$work/metrics.txt:/fixture/metrics.txt:ro" -v "$work/cadvisor.txt:/fixture/cadvisor.txt:ro" \
  -v "$work/server.py:/server.py:ro" python:3-alpine python3 /server.py >/dev/null
fixture_ip="$(docker inspect mpp-fixture --format "{{(index .NetworkSettings.Networks \"$net\").IPAddress}}")"
echo "- targets: [\"$fixture_ip:8080\", \"$fixture_ip:8081\"]" > "$work/targets.yml"

qcurl() { docker run --rm --label metrics-path-proof --network "$net" curlimages/curl:latest -fsS "$@"; }

fail=0
run_case() {
  local case="$1" want="$2" vm="mpp-vm-$1" ag="mpp-agent-$1"
  docker run -d --label metrics-path-proof --name "$vm" --network "$net" \
    "victoriametrics/victoria-metrics:$vmsingle_tag" -retentionPeriod=100y >/dev/null
  local vm_ip; vm_ip="$(docker inspect "$vm" --format "{{(index .NetworkSettings.Networks \"$net\").IPAddress}}")"
  for _ in $(seq 1 30); do qcurl "http://$vm_ip:8428/health" >/dev/null 2>&1 && break; sleep 1; done
  docker run -d --label metrics-path-proof --name "$ag" --network "$net" \
    -v "$work/scrape-$case.yml:/config/scrape.yml:ro" -v "$work/targets.yml:/config/targets.yml:ro" \
    "victoriametrics/vmagent:$vmagent_tag" -promscrape.config=/config/scrape.yml \
    -remoteWrite.url="http://$vm_ip:8428/api/v1/write" >/dev/null
  for _ in $(seq 1 40); do
    n="$(qcurl -G "http://$vm_ip:8428/api/v1/series" --data-urlencode 'match[]={job=~"kubelet|cadvisor",__name__=~"up|kubelet_running_pods|kubelet_node_name|container_cpu_usage_seconds_total"}' \
      | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["data"]))' 2>/dev/null || echo 0)"
    [ "$n" -ge 10 ] && break; sleep 1
  done
  # kube-state-metrics' pod row the recording rule joins on, and enough
  # scrapes (2s interval) for irate() to have two samples. The store hides
  # its newest 30s from queries (-search.latencyOffset), so wait that out.
  printf 'kube_pod_info{k8s_cluster_name="example-cluster",namespace="team-a",pod="web-0",node="node-a"} 1\n' \
    | docker run -i --rm --label metrics-path-proof --network "$net" curlimages/curl:latest -fsS --data-binary @- "http://$vm_ip:8428/api/v1/import/prometheus"
  sleep 40
  echo
  echo "=== case $case: stored series (job, metrics_path, k8s_cluster_name) ==="
  qcurl -G "http://$vm_ip:8428/api/v1/series" --data-urlencode 'match[]={job=~"kubelet|cadvisor",__name__=~"up|kubelet_running_pods|kubelet_node_name|container_cpu_usage_seconds_total"}' \
    | python3 -c '
import json,sys
for s in sorted(json.load(sys.stdin)["data"], key=lambda s:(s["job"],s["__name__"])):
    print("  %-40s job=%-9s metrics_path=%-18s k8s_cluster_name=%s" % (s["__name__"], s["job"], s.get("metrics_path","<absent>"), s.get("k8s_cluster_name","<absent>")))'
  echo "=== case $case: the dashboard's cluster variable ==="
  got="$(qcurl -G "http://$vm_ip:8428/api/v1/label/k8s_cluster_name/values" --data-urlencode "match[]=$selector" \
    | python3 -c 'import json,sys; print(",".join(json.load(sys.stdin)["data"]))')"
  echo "  label_values($selector, k8s_cluster_name) = [${got}]"
  if [ "$case" = chart ]; then
    paths="$(qcurl -G "http://$vm_ip:8428/api/v1/label/metrics_path/values" | python3 -c 'import json,sys; print(",".join(sorted(json.load(sys.stdin)["data"])))')"
    echo "  stored metrics_path values: [$paths]"
    [ "$paths" = "/metrics,/metrics/cadvisor" ] && echo "PASS: metrics_path is /metrics for kubelet and /metrics/cadvisor for cadvisor" \
      || { echo "FAIL: unexpected metrics_path values" >&2; fail=1; }
  fi
  if [ "$got" = "$want" ]; then echo "PASS: cluster variable = [$got]"; else echo "FAIL: cluster variable is [$got], wanted [$want]" >&2; fail=1; fi

  [ "$case" = control ] && return 0
  # `--data-urlencode` with -G issues an instant query at the store's "now".
  echo "=== case $case: where container_cpu_usage_seconds_total is stored ==="
  qcurl -G "http://$vm_ip:8428/api/v1/series" --data-urlencode 'match[]=container_cpu_usage_seconds_total' \
    | python3 -c '
import json,sys
for s in json.load(sys.stdin)["data"]:
    print("  container_cpu_usage_seconds_total job=%s metrics_path=%s instance=%s image=%s" % (s["job"], s.get("metrics_path"), s["instance"], s.get("image")))'
  stored="$(qcurl -G "http://$vm_ip:8428/api/v1/series" --data-urlencode 'match[]=container_cpu_usage_seconds_total' \
    | python3 -c 'import json,sys; print(",".join(sorted({s["job"]+"|"+s.get("metrics_path","") for s in json.load(sys.stdin)["data"]})))')"
  echo "=== case $case: up per (job, metrics_path) ==="
  qcurl -G "http://$vm_ip:8428/api/v1/query" --data-urlencode 'query=count by (job, metrics_path) (up)' \
    | python3 -c '
import json,sys
for r in sorted(json.load(sys.stdin)["data"]["result"], key=lambda r: (r["metric"]["job"], r["metric"]["metrics_path"])):
    print("  up job=%s metrics_path=%s count=%s" % (r["metric"]["job"], r["metric"]["metrics_path"], r["value"][1]))'
  local rows
  rows="$(qcurl -G "http://$vm_ip:8428/api/v1/query" --data-urlencode "query=$rule_expr" \
    | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["data"]["result"]))')"
  echo "=== case $case: the upstream recording rule's expression returns $rows series ==="
  running="$(qcurl -G "http://$vm_ip:8428/api/v1/query" --data-urlencode "query=$running_kubelets" \
    | python3 -c 'import json,sys; r=json.load(sys.stdin)["data"]["result"]; print(r[0]["value"][1] if r else "none")')"
  echo "=== case $case: Running Kubelets (two nodes in the fixture) = $running ==="
  if [ "$case" = chart ]; then
    [ "$stored" = "kubelet|/metrics/cadvisor" ] && echo "PASS: cadvisor series stored as job=kubelet, metrics_path=/metrics/cadvisor" || { echo "FAIL: stored as [$stored]" >&2; fail=1; }
    [ "$rows" -ge 1 ] && echo "PASS: the recording rule returns data ($rows series; the two fixture nodes share one pod row, so they fold into one)" || { echo "FAIL: the recording rule returned $rows series" >&2; fail=1; }
  else
    [ "$stored" = "cadvisor|/metrics/cadvisor" ] && echo "PASS: control stores job=cadvisor" || { echo "FAIL: control stored as [$stored]" >&2; fail=1; }
    [ "$rows" = 0 ] && echo "PASS: control: the recording rule returns nothing" || { echo "FAIL: control rule returned $rows series" >&2; fail=1; }
  fi
  [ "$running" = 2 ] && echo "PASS: Running Kubelets counts one per node (2), not one per scrape path" || { echo "FAIL: Running Kubelets = $running, wanted 2" >&2; fail=1; }
}

run_case chart "example-cluster"
run_case control ""
run_case oldjob "example-cluster"

echo
if [ "$fail" = 0 ]; then
  echo "hack/metrics-path-proof.sh: PASS — the rendered jobs store metrics_path, the kubelet dashboard's cluster variable returns the cluster, and without the relabel it is empty."
else
  echo "hack/metrics-path-proof.sh: FAIL" >&2; exit 1
fi

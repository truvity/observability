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
# Two containers' worth of real software, three cases:
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
# the empty variable: a proof that cannot fail proves nothing.
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

# The two node jobs, as rendered: everything but service discovery and TLS.
python3 - "$work" <<'PYEOF'
import sys, yaml, json
work = sys.argv[1]
agent = next(d for d in yaml.safe_load_all(open(work + "/render.yaml")) if d and d.get("kind") == "VMAgent")
jobs = {j["job_name"]: j for j in yaml.safe_load(agent["spec"]["inlineScrapeConfig"])}
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
for case, strip in (("chart", False), ("control", True)):
    cfg = {"scrape_configs": [fixture_job(jobs["kubelet"], strip), fixture_job(jobs["cadvisor"], strip)]}
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
EOM
cat > "$work/cadvisor.txt" <<'EOM'
# TYPE container_cpu_usage_seconds_total counter
container_cpu_usage_seconds_total{namespace="team-a",pod="web-0",container="app"} 12.5
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
http.server.HTTPServer(("0.0.0.0", 8080), H).serve_forever()
EOM

docker network create "$net" >/dev/null
docker run -d --label metrics-path-proof --name mpp-fixture --network "$net" \
  -v "$work/metrics.txt:/fixture/metrics.txt:ro" -v "$work/cadvisor.txt:/fixture/cadvisor.txt:ro" \
  -v "$work/server.py:/server.py:ro" python:3-alpine python3 /server.py >/dev/null
fixture_ip="$(docker inspect mpp-fixture --format "{{(index .NetworkSettings.Networks \"$net\").IPAddress}}")"
echo "- targets: [\"$fixture_ip:8080\"]" > "$work/targets.yml"

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
    n="$(qcurl -G "http://$vm_ip:8428/api/v1/series" --data-urlencode 'match[]={job=~"kubelet|cadvisor",__name__=~"up|kubelet_running_pods|container_cpu_usage_seconds_total"}' \
      | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["data"]))' 2>/dev/null || echo 0)"
    [ "$n" -ge 4 ] && break; sleep 1
  done
  echo
  echo "=== case $case: stored series (job, metrics_path, k8s_cluster_name) ==="
  qcurl -G "http://$vm_ip:8428/api/v1/series" --data-urlencode 'match[]={job=~"kubelet|cadvisor",__name__=~"up|kubelet_running_pods|container_cpu_usage_seconds_total"}' \
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
}

run_case chart "example-cluster"
run_case control ""

echo
if [ "$fail" = 0 ]; then
  echo "hack/metrics-path-proof.sh: PASS — the rendered jobs store metrics_path, the kubelet dashboard's cluster variable returns the cluster, and without the relabel it is empty."
else
  echo "hack/metrics-path-proof.sh: FAIL" >&2; exit 1
fi

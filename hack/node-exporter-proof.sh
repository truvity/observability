#!/usr/bin/env bash
# hack/node-exporter-proof.sh — REAL proof, against the real node-exporter,
# vmagent, VictoriaMetrics and vmalert binaries, that
# charts/observability-emitters' node-exporter (`nodeExporter.enabled`)
# stores its series the way the node-exporter dashboard and the k8s-stack's
# node recording rules select on them, and that upstream's own `node.rules`
# and `kube-prometheus-node-recording.rules` then record data.
#
# The defect this guards is the quiet kind. A node-exporter that is Ready,
# scraped and stored under the wrong labels is invisible: the dashboard's
# pickers are empty and the two recording groups select `job="node-exporter"`,
# join on `namespace`/`pod` and group on `node`, `instance` and the cluster
# label, so every rule records nothing while every series sits in the store.
#
# What runs, none of it a stand-in:
#   node-exporter - the image and the EXACT args the chart's DaemonSet renders
#                   (`helm template`, read back, not retyped), with the same
#                   /proc, /sys and root mounts, hostPID and non-root user. The
#                   container listens on a Docker network address instead of
#                   the node's, which is all `hostNetwork` changes for a scrape.
#   vmagent       - the image charts/observability-emitters pins, scraping it
#                   with the chart's RENDERED relabel steps: the default scrape
#                   class (cluster, tier), the ServiceMonitor's relabelings and
#                   metricRelabelings, and the agent's global metric relabel.
#                   There is no cluster, so no VictoriaMetrics operator to
#                   convert the ServiceMonitor: file_sd carries the labels its
#                   converter stamps on every endpoints target (`job` = the
#                   Service, `namespace`, `pod`, `container`, `service`,
#                   `endpoint`, `instance` = ip:port) and the service-discovery
#                   meta labels the relabel steps read.
#   vmsingle      - the version charts/observability-stack pins.
#   sync job      - the vendored k8s-stack's REAL sync-job image, run against
#                   the stack chart's rendered config and a throwaway fake API
#                   server, as hack/k8s-stack-cluster-label-proof.sh does: it
#                   fetches upstream's rule files and rewrites their cluster
#                   label to `k8s_cluster_name`. The two groups are read back
#                   from what it applied.
#   vmalert       - the same version as the store, evaluating those two groups
#                   against it and writing what they record back.
#
# Finding worth knowing before enabling the recording groups. `node.rules`
# joins on `(k8s_cluster_name, namespace, pod)` and groups by `node` and the
# cluster, and every output carries the cluster. `kube-prometheus-node-
# recording.rules` does not: its `instance:*:rate:sum` group by `instance`
# alone and `cluster:node_cpu:sum_rate5m` / `:ratio` aggregate with no `by`
# at all, and the stack's cluster-label rewrite (0.14.2) can only rename a
# `cluster` the expression already names. On a store several clusters write
# to, those five outputs merge across clusters (node names are not unique
# across clusters). The proof asserts exactly that set.
#
# Two cases. "chart" is the render as it is. "control" is the same render
# with the chart's `job` step removed (the operator's Service-named job): the
# four `node.rules` outputs that read node-exporter, which select
# `job="node-exporter"`, must record NOTHING there, because a proof that
# cannot fail proves nothing.
#
# Also reported: how many series one node holds with the chart's collector
# set and with upstream's defaults, on the machine this runs on.
#
# Needs Docker, curl, helm, python3 with PyYAML, and network access to
# raw.githubusercontent.com, quay.io and ghcr.io. Deliberately NOT part of
# `check` or CI: run by hand.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
me="hack/node-exporter-proof.sh"
work="$(mktemp -d -t node-exporter-proof.XXXXXX)"
net=node-exporter-proof-net
api_pid=""
cleanup() {
  docker ps -aq --filter "label=node-exporter-proof" | xargs -r docker rm -f >/dev/null 2>&1 || true
  docker network rm "$net" >/dev/null 2>&1 || true
  [ -n "$api_pid" ] && kill "$api_pid" 2>/dev/null || true
  rm -rf "$work"
}
trap cleanup EXIT

for tool in docker curl python3 helm; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$me needs $tool on PATH" >&2; exit 1; }
done
python3 -c 'import yaml' 2>/dev/null || { echo "$me needs python3's PyYAML" >&2; exit 1; }

vmagent_tag="$(awk '/^metrics:/{m=1} m && /^ *tag: /{print $2; exit}' "$root/charts/observability-emitters/values.yaml")"
vm_tgz=("$root"/charts/observability-stack/charts/victoria-metrics-k8s-stack-*.tgz)
vm_tag="$(tar -xzOf "${vm_tgz[0]}" victoria-metrics-k8s-stack/Chart.yaml | awk '/^appVersion:/ { print $2 }')"
echo "vmagent $vmagent_tag (the chart's pin), victoria-metrics and vmalert $vm_tag (the stack's pin)"

echo
echo "##### 1. the chart's render"
helm template x "$root/charts/observability-emitters" --namespace observability \
  --values "$root/tests/cases/observability-emitters/node-exporter/values.yaml" > "$work/render.yaml"
helm template x "$root/charts/observability-stack" \
  --values "$root/tests/cases/observability-stack/minimal/values.yaml" > "$work/stack.yaml"
python3 - "$work" <<'PYEOF'
import sys, yaml, json
work = sys.argv[1]
docs = [d for d in yaml.safe_load_all(open(work + "/render.yaml")) if d]
ds = next(d for d in docs if d["kind"] == "DaemonSet" and "node-exporter" in d["metadata"]["name"])
pod = ds["spec"]["template"]["spec"]
c = next(c for c in pod["containers"] if c["name"] == "node-exporter")
args = [a.replace("$(HOST_IP)", "0.0.0.0") for a in c["args"]]
open(work + "/ne-image", "w").write(c["image"])
json.dump(args, open(work + "/ne-args.json", "w"))
print("node-exporter image:", c["image"])
print("hostNetwork=%s hostPID=%s priorityClassName=%s tolerations=%s" % (pod["hostNetwork"], pod["hostPID"], pod["priorityClassName"], pod["tolerations"]))
print("resources:", c["resources"])
print("affinity:", json.dumps(pod["affinity"]["nodeAffinity"]["requiredDuringSchedulingIgnoredDuringExecution"]["nodeSelectorTerms"]))
print("collector args: %d, --collector.disable-defaults=%s" % (len(args), "--collector.disable-defaults" in args))

agent = next(d for d in docs if d["kind"] == "VMAgent")
cls = agent["spec"]["scrapeClasses"][0]
sm = next(d for d in docs if d["kind"] == "ServiceMonitor" and "node-exporter" in d["metadata"]["name"])
ep = sm["spec"]["endpoints"][0]
def snake(items):
    out = []
    for i in items:
        i = dict(i)
        if "sourceLabels" in i: i["source_labels"] = i.pop("sourceLabels")
        if "targetLabel" in i: i["target_label"] = i.pop("targetLabel")
        out.append(i)
    return out
class_relabel = snake(cls["relabelConfigs"])
relabel = snake(ep["relabelings"])
metric = snake(ep["metricRelabelings"]) + snake(agent["spec"]["globalScrapeMetricRelabelConfigs"])
print("rendered ServiceMonitor relabelings:")
print(yaml.safe_dump(ep["relabelings"], sort_keys=False))
print("rendered ServiceMonitor metricRelabelings:")
print(yaml.safe_dump(ep["metricRelabelings"], sort_keys=False))

def cfg(drop_job):
    rel = class_relabel + [r for r in relabel if not (drop_job and r.get("target_label") == "job")]
    return {"scrape_configs": [{
        "job_name": "serviceMonitor/observability/x-prometheus-node-exporter/0",
        "scrape_interval": "2s", "honor_labels": False,
        "file_sd_configs": [{"files": ["/config/targets.yml"]}],
        "relabel_configs": rel, "metric_relabel_configs": metric}]}
yaml.safe_dump(cfg(False), open(work + "/scrape-chart.yml", "w"), sort_keys=False)
yaml.safe_dump(cfg(True), open(work + "/scrape-control.yml", "w"), sort_keys=False)

# The stack's sync-job config and image, for the recording rules.
for d in yaml.safe_load_all(open(work + "/stack.yaml")):
    if d and d["kind"] == "ConfigMap" and d["metadata"]["name"].endswith("sync-job-config"):
        open(work + "/sync-config.yaml", "w").write(d["data"]["config.yaml"])
    if d and d["kind"] == "Job" and "sync-job" in d["metadata"]["name"]:
        open(work + "/sync-image", "w").write(d["spec"]["template"]["spec"]["containers"][0]["image"])
PYEOF
ne_image="$(cat "$work/ne-image")"
grep -q '^  clusterLabel: "k8s_cluster_name"$' "$work/sync-config.yaml" \
  || { echo "FAIL: the stack's sync job does not rewrite the rules to k8s_cluster_name" >&2; exit 1; }

echo "##### 2. the recording rules, as the stack's real sync job applies them"
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
clusters: [{name: c, cluster: {server: "http://127.0.0.1:18082"}}]
users: [{name: u, user: {token: t}}]
contexts: [{name: c, context: {cluster: c, user: u}}]
current-context: c
EOF
: > "$work/writes.jsonl"
python3 "$work/fakeapi.py" 18082 "$work/writes.jsonl" & api_pid=$!
sleep 1
mkdir -p "$work/etc" && cp "$work/sync-config.yaml" "$work/etc/config.yaml"
docker run --rm --label node-exporter-proof --network host -v "$work/etc:/etc/config:ro" -v "$work/kubeconfig:/kc:ro" \
  -e KUBECONFIG=/kc "$(cat "$work/sync-image")" 2>&1 | tail -1
kill "$api_pid"; api_pid=""
python3 - "$work" <<'PYEOF'
import json, sys, yaml
work = sys.argv[1]
want = ("node.rules", "kube-prometheus-node-recording.rules")
groups = []
for line in open(work + "/writes.jsonl"):
    d = json.loads(json.loads(line)["body"])
    for g in d.get("spec", {}).get("groups", []):
        if g["name"] in want:
            g = dict(g)
            g.pop("interval", None)  # evaluate at vmalert's own 5s
            groups.append(g)
assert sorted(g["name"] for g in groups) == sorted(want), "the sync job applied %s" % [g["name"] for g in groups]
yaml.safe_dump({"groups": groups}, open(work + "/rules.yml", "w"), sort_keys=False)
for g in groups:
    print("group", g["name"])
    for r in g["rules"]:
        print("  %-46s %s" % (r.get("record"), " ".join(r["expr"].split())[:140]))
PYEOF

echo
echo "##### 3. real node-exporter, the chart's args"
docker network create "$net" >/dev/null
ne_args="$(python3 - "$work" <<'PYEOF'
import json, shlex, sys
print(" ".join(shlex.quote(a) for a in json.load(open(sys.argv[1] + "/ne-args.json"))))
PYEOF
)"
mounts=(-v /proc:/host/proc:ro -v /sys:/host/sys:ro -v /:/host/root:ro,rslave)
# shellcheck disable=SC2086
eval docker run -d --label node-exporter-proof --network "$net" --pid host --user 65534:65534 "${mounts[@]}" \
  --name npf-exporter "$ne_image" $ne_args >/dev/null
qcurl() { docker run --rm --label node-exporter-proof --network "$net" curlimages/curl:latest -fsS "$@"; }
for _ in $(seq 1 30); do qcurl "http://npf-exporter:9100/metrics" -o /dev/null 2>/dev/null && break; sleep 1; done
exporter_ip="$(docker inspect npf-exporter --format "{{(index .NetworkSettings.Networks \"$net\").IPAddress}}")"
lean="$(qcurl "http://npf-exporter:9100/metrics" | grep -vc '^#')"
docker run -d --label node-exporter-proof --network "$net" --pid host "${mounts[@]}" --name npf-defaults "$ne_image" \
  --path.procfs=/host/proc --path.sysfs=/host/sys --path.rootfs=/host/root >/dev/null
for _ in $(seq 1 30); do qcurl "http://npf-defaults:9100/metrics" -o /dev/null 2>/dev/null && break; sleep 1; done
defaults="$(qcurl "http://npf-defaults:9100/metrics" | grep -vc '^#')"
echo "series on this machine ($(nproc) CPUs): chart's collector set = $lean, upstream's defaults = $defaults"
docker rm -f npf-defaults >/dev/null

printf '%s\n' 'kube_pod_info{job="kube-state-metrics",k8s_cluster_name="example-cluster",namespace="observability",pod="x-prometheus-node-exporter-abc12",node="node-a"} 1' > "$work/kube_pod_info.prom"

fail=0
run_case() {
  local case="$1" vm="npf-vm-$1" ag="npf-agent-$1" al="npf-alert-$1"
  docker run -d --label node-exporter-proof --name "$vm" --network "$net" \
    "victoriametrics/victoria-metrics:$vm_tag" -retentionPeriod=100y >/dev/null
  local vm_ip; vm_ip="$(docker inspect "$vm" --format "{{(index .NetworkSettings.Networks \"$net\").IPAddress}}")"
  for _ in $(seq 1 30); do qcurl "http://$vm_ip:8428/health" >/dev/null 2>&1 && break; sleep 1; done
  cat > "$work/targets.yml" <<EOF
- targets: ["$exporter_ip:9100"]
  labels:
    job: x-prometheus-node-exporter
    instance: "$exporter_ip:9100"
    namespace: observability
    pod: x-prometheus-node-exporter-abc12
    container: node-exporter
    service: x-prometheus-node-exporter
    endpoint: metrics
    __meta_kubernetes_namespace: observability
    __meta_kubernetes_pod_node_name: node-a
    __meta_kubernetes_pod_label_app_kubernetes_io_instance: x
EOF
  docker run -d --label node-exporter-proof --name "$ag" --network "$net" \
    -v "$work/scrape-$case.yml:/config/scrape.yml:ro" -v "$work/targets.yml:/config/targets.yml:ro" \
    "victoriametrics/vmagent:$vmagent_tag" -promscrape.config=/config/scrape.yml \
    -remoteWrite.url="http://$vm_ip:8428/api/v1/write" >/dev/null
  # kube-state-metrics' row for the exporter's own pod, which node.rules joins on.
  docker run -i --rm --label node-exporter-proof --network "$net" curlimages/curl:latest -fsS --data-binary @- \
    "http://$vm_ip:8428/api/v1/import/prometheus" < "$work/kube_pod_info.prom"
  for _ in $(seq 1 40); do
    n="$(qcurl -G "http://$vm_ip:8428/api/v1/series" --data-urlencode 'match[]=node_cpu_seconds_total' \
      | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["data"]))' 2>/dev/null || echo 0)"
    [ "$n" != 0 ] && break; sleep 1
  done
  docker run -d --label node-exporter-proof --name "$al" --network "$net" -v "$work/rules.yml:/rules.yml:ro" \
    "victoriametrics/vmalert:$vm_tag" -rule=/rules.yml -datasource.url="http://$vm_ip:8428" \
    -remoteWrite.url="http://$vm_ip:8428" -evaluationInterval=5s >/dev/null
  # The store hides its newest 30s from queries, rate() needs samples, and
  # vmalert needs a few evaluations.
  sleep 75

  echo
  echo "=== case $case: how the exporter's series are stored ==="
  qcurl -G "http://$vm_ip:8428/api/v1/series" --data-urlencode 'match[]=node_uname_info' --data-urlencode 'match[]=up' \
    | python3 -c '
import json, sys
for s in sorted(json.load(sys.stdin)["data"], key=lambda s: s["__name__"]):
    print("  %-16s job=%s instance=%s node=%s k8s_cluster_name=%s deployment_environment_name=%s namespace=%s pod=%s k8s_namespace_name=%s" % (
        s["__name__"], s.get("job"), s.get("instance"), s.get("node", "<absent>"), s.get("k8s_cluster_name", "<absent>"),
        s.get("deployment_environment_name", "<absent>"), s.get("namespace"), s.get("pod"), s.get("k8s_namespace_name", "<absent>")))'
  local stored total
  stored="$(qcurl -G "http://$vm_ip:8428/api/v1/series" --data-urlencode 'match[]=node_cpu_seconds_total' \
    | python3 -c 'import json,sys; d=json.load(sys.stdin)["data"]; print("|".join(sorted({"%s,%s,%s,%s" % (s.get("job"), s.get("instance"), s.get("k8s_cluster_name"), s.get("k8s_namespace_name","<absent>")) for s in d})))')"
  total="$(qcurl -G "http://$vm_ip:8428/api/v1/series" --data-urlencode 'match[]={instance!=""}' \
    | python3 -c 'import json,sys; print(len([s for s in json.load(sys.stdin)["data"] if s.get("job") in ("node-exporter","x-prometheus-node-exporter")]))')"
  echo "  series stored for this node (every name, including up and scrape_*): $total"
  if [ "$case" = chart ]; then
    [ "$stored" = "node-exporter,node-a,example-cluster,<absent>" ] \
      && echo "PASS: node_cpu_seconds_total stored as job=node-exporter instance=node-a k8s_cluster_name=example-cluster, no k8s_namespace_name" \
      || { echo "FAIL: node_cpu_seconds_total stored as [$stored]" >&2; fail=1; }
  else
    [[ "$stored" == x-prometheus-node-exporter,* ]] \
      && echo "PASS: control stores the operator's Service-named job ($stored)" \
      || { echo "FAIL: control stored as [$stored]" >&2; fail=1; }
  fi

  echo "=== case $case: what the two recording groups recorded ==="
  # `<name> <group>`; a `!` marks a rule upstream writes with no cluster in
  # its `by (...)`, so it cannot carry k8s_cluster_name (see the header).
  local recorded=0 blind=0 ruleset entry name group rows
  ruleset=(
    'node_namespace_pod:kube_pod_info: node.rules'
    'node:node_num_cpu:sum node.rules'
    ':node_memory_MemAvailable_bytes:sum node.rules'
    'node:node_cpu_utilization:ratio_rate5m node.rules'
    'cluster:node_cpu:ratio_rate5m node.rules'
    'instance:node_cpu:ratio kube-prometheus-node-recording.rules'
    '!instance:node_cpu:rate:sum kube-prometheus-node-recording.rules'
    '!instance:node_network_receive_bytes:rate:sum kube-prometheus-node-recording.rules'
    '!instance:node_network_transmit_bytes:rate:sum kube-prometheus-node-recording.rules'
    '!cluster:node_cpu:sum_rate5m kube-prometheus-node-recording.rules'
    '!cluster:node_cpu:ratio kube-prometheus-node-recording.rules')
  local labelled_bad=0 total_rules=${#ruleset[@]} nr_silent=0
  for entry in "${ruleset[@]}"; do
    name="${entry%% *}"; group="${entry##* }"; name="${name#!}"
    rows="$(qcurl -G "http://$vm_ip:8428/api/v1/query" --data-urlencode "query={__name__=\"$name\"}" \
      | python3 -c '
import json, sys
name = sys.argv[1]
r = json.load(sys.stdin)["data"]["result"]
for x in r:
    m = x["metric"]
    print("  %-44s k8s_cluster_name=%-16s node=%-7s instance=%-7s value=%.4g" % (m["__name__"], m.get("k8s_cluster_name", "<absent>"), m.get("node", "-"), m.get("instance", "-"), float(x["value"][1])))
if not r:
    print("  %-44s (no series)" % name)' "$name")"
    echo "$rows   [$group]"
    # The four node.rules outputs that READ node-exporter (the fifth is
    # kube-state-metrics' own row, which the job label never touches).
    if grep -q 'no series' <<<"$rows" && [ "$group" = node.rules ]; then nr_silent=$((nr_silent + 1)); fi
    if ! grep -q 'no series' <<<"$rows"; then
      recorded=$((recorded + 1))
      if grep -q 'k8s_cluster_name=<absent>' <<<"$rows"; then
        blind=$((blind + 1))
        # node.rules must never lose the cluster; a rule that is on the
        # blind list is the upstream-expression limitation.
        [[ "$entry" == '!'* ]] || { echo "FAIL: $name lost the cluster label" >&2; labelled_bad=1; }
      fi
    fi
  done
  if [ "$case" = chart ]; then
    [ "$recorded" = "$total_rules" ] && echo "PASS: all $total_rules rules of node.rules and kube-prometheus-node-recording.rules record data" \
      || { echo "FAIL: only $recorded of $total_rules rules recorded" >&2; fail=1; }
    [ "$labelled_bad" = 0 ] && echo "PASS: every node.rules output, and instance:node_cpu:ratio, carries k8s_cluster_name" || fail=1
    [ "$blind" = 5 ] && echo "NOTE: $blind outputs of kube-prometheus-node-recording.rules carry NO cluster label (upstream's expressions aggregate by instance, or by nothing, so on a store several clusters write to they merge across clusters): keep that group off on such a store" \
      || { echo "FAIL: expected exactly the 5 cluster-blind outputs, saw $blind (upstream's expressions moved: re-read them)" >&2; fail=1; }
  else
    [ "$nr_silent" = 4 ] && echo "PASS: control: without job=node-exporter the four node.rules outputs that read node-exporter record nothing (kube-prometheus-node-recording.rules selects no job, so it still records)" \
      || { echo "FAIL: control: $nr_silent of the 4 node-exporter rules of node.rules were silent; the proof cannot fail" >&2; fail=1; }
  fi
  docker rm -f "$vm" "$ag" "$al" >/dev/null
}

run_case chart
run_case control

echo
if [ "$fail" = 0 ]; then
  echo "$me: PASS — the rendered scrape stores node-exporter's series as job=node-exporter with the cluster label (and no namespace key), the upstream node recording rules return data on them, and without the job step the node.rules outputs that read it record nothing."
else
  echo "$me: FAIL" >&2
  exit 1
fi

#!/usr/bin/env bash
# hack/cadvisor-churn-drop-proof.sh — REAL proof, against the pinned
# vmagent and VictoriaMetrics single binaries, that
# `metrics.scrape.cadvisorDrop` (0.9.1, charts/observability-emitters)
# actually does what tests/cadvisor_churn_drop_test.go proves against a
# library replay of the relabel chain: dropped names never reach
# storage, the kept exception does, and the `id` label is cleared ONLY
# on a series that already carries a non-empty `container` label —
# never unconditionally, which is what a review caught this chart's own
# first draft getting wrong (see docs/safety.md, "Metric churn: what
# cadvisor never has read"): cadvisor's node-level cgroups (the root,
# the pod-manager slice, a systemd unit) carry no pod or container at
# all, so `id` is their ONLY distinguishing label, and an unconditional
# `labeldrop id` merges every one of them, on one node, into ONE
# identical label set — silently kept as an arbitrary survivor by
# vmagent/vmsingle deduplication, not merely "one fewer series".
#
# tests/cadvisor_churn_drop_test.go is the regression gate (fast, no
# Docker, runs in `just test`) — it replays the rendered
# metric_relabel_configs through a real relabel.Config chain from
# github.com/prometheus/prometheus/model/relabel. What it cannot show is
# vmagent's OWN scrape → relabel → remote-write pipeline end to end: the
# metric_relabel_configs this script extracts is read from the same
# render, but here it is vmagent itself, the pinned image, that scrapes a
# fixture target and writes the result — a real store then answers the
# query, not this repository's model of what vmagent would do.
#
# Three containers on one docker network:
#   fixture  - a tiny python http.server answering GET /metrics with a
#              fixed Prometheus-exposition fixture: the three dropped
#              metric names, one arbitrary `_bucket` series that the
#              blanket bucket-drop must catch, the one kept exception
#              (go_sched_latencies_seconds_bucket), one ordinary
#              container-level kept counter, and — the fixture this
#              script's own review finding was missing — FOUR MORE
#              `container_cpu_usage_seconds_total` series sharing the
#              same metric name but no `container` label: the cgroup
#              root (`id="/"`), the pod-manager slice
#              (`id="/kubepods.slice"`), a systemd unit
#              (`id="/system.slice/containerd.service"`), and a
#              pod-level rollup (`pod` set, `container` unset). All
#              five arrive from the SAME scrape target, exactly as a
#              real node presents them, so a labeldrop that merges any
#              two of them shows up as fewer than 5 series in vmsingle,
#              not as a difference this script would otherwise miss.
#   vmagent  - the exact image charts/observability-emitters/values.yaml
#              pins (metrics.image), scraping the fixture over a
#              file_sd_configs target (a static file-sd target, not
#              Kubernetes service discovery — there is no cluster here)
#              with the REAL cadvisor job's metric_relabel_configs,
#              extracted from `helm template` rather than retyped by
#              hand, and remote-writing to vmsingle.
#   vmsingle - the VictoriaMetrics version charts/observability-stack's
#              own vendored victoria-metrics-k8s-stack pins, the same
#              way hack/platform-alerts-newest-job-proof.sh reads it.
#
# Needs Docker, curl, python3 (with PyYAML) and helm. Deliberately NOT
# part of `check` or CI, the same reason
# hack/platform-alerts-newest-job-proof.sh's own Docker requirement is
# not: a one-off, run-by-hand proof for this feature, not a regression
# gate.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d -t cadvisor-churn-drop-proof.XXXXXX)"
net=cadvisor-churn-drop-proof-net
fixture=cadvisor-churn-drop-proof-fixture
vmagent=cadvisor-churn-drop-proof-vmagent
vmsingle=cadvisor-churn-drop-proof-vmsingle

cleanup() {
  docker rm -f "$fixture" "$vmagent" "$vmsingle" >/dev/null 2>&1 || true
  docker network rm "$net" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

for tool in docker curl python3 helm; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "hack/cadvisor-churn-drop-proof.sh needs $tool, which is not on PATH" >&2
    exit 1
  }
done
python3 -c 'import yaml' 2>/dev/null || {
  echo "hack/cadvisor-churn-drop-proof.sh needs python3's PyYAML (import yaml)" >&2
  exit 1
}

vmagent_tag="$(awk '/^metrics:/{m=1} m && /^ *tag: /{print $2; exit}' "$root/charts/observability-emitters/values.yaml")"
[ -n "$vmagent_tag" ] || {
  echo "hack/cadvisor-churn-drop-proof.sh: could not read metrics.image.tag from charts/observability-emitters/values.yaml" >&2
  exit 1
}
vm_tgz=("$root"/charts/observability-stack/charts/victoria-metrics-k8s-stack-*.tgz)
[ -e "${vm_tgz[0]}" ] || {
  echo "hack/cadvisor-churn-drop-proof.sh: no vendored victoria-metrics-k8s-stack under charts/observability-stack/charts/ — run 'just vendor observability-stack'" >&2
  exit 1
}
vmsingle_tag="$(tar -xzOf "${vm_tgz[0]}" victoria-metrics-k8s-stack/Chart.yaml | awk '/^appVersion:/ { print $2 }')"
echo "hack/cadvisor-churn-drop-proof.sh: vmagent $vmagent_tag (charts/observability-emitters' own pin), victoria-metrics $vmsingle_tag (charts/observability-stack's vendored pin)"

echo "hack/cadvisor-churn-drop-proof.sh: rendering charts/observability-emitters (minimal case, defaults — cadvisorDrop.enabled is true with no override)"
rendered="$(helm template x "$root/charts/observability-emitters" --values "$root/tests/cases/observability-emitters/minimal/values.yaml")"

cat > "$work/extract.py" <<'PYEOF'
import sys, yaml, json

rendered = sys.stdin.read()
agent = None
for doc in yaml.safe_load_all(rendered):
    if doc and doc.get("kind") == "VMAgent":
        agent = doc
        break
if agent is None:
    sys.exit("no VMAgent in the render")

jobs = yaml.safe_load(agent["spec"]["inlineScrapeConfig"])
cadvisor = next((j for j in jobs if j.get("job_name") == "cadvisor"), None)
if cadvisor is None:
    sys.exit("no cadvisor job in inlineScrapeConfig")

print(json.dumps({
    "honor_labels": cadvisor["honor_labels"],
    "metric_relabel_configs": cadvisor["metric_relabel_configs"],
}))
PYEOF
extracted="$(python3 "$work/extract.py" <<<"$rendered")"
echo
echo "=== cadvisor job's own metric_relabel_configs, read back off the real render ==="
python3 -c 'import json,sys,yaml; print(yaml.safe_dump(json.loads(sys.argv[1])["metric_relabel_configs"], sort_keys=False))' "$extracted"
echo

# The fixture: every series carries cadvisor's own identity labels
# (namespace, pod, container, uid, id, image) so the `id` labeldrop has
# something to prove against a SURVIVING series, not only an absent one.
cat > "$work/metrics.txt" <<'EOF'
# HELP container_cpu_usage_seconds_total dummy fixture value, ordinary kept series
# TYPE container_cpu_usage_seconds_total counter
container_cpu_usage_seconds_total{namespace="team-a",pod="web-0",container="app",uid="abc-123",id="/kubepods/burstable/podabc123/def456",image="example.com/app:v1"} 12.5
container_cpu_usage_seconds_total{id="/"} 1
container_cpu_usage_seconds_total{id="/kubepods.slice"} 1
container_cpu_usage_seconds_total{id="/system.slice/containerd.service"} 1
container_cpu_usage_seconds_total{namespace="team-a",pod="web-0",uid="abc-123",id="/kubepods/burstable/podabc123"} 1

# HELP container_tasks_state dummy fixture value, must be dropped by the exact-name list
# TYPE container_tasks_state gauge
container_tasks_state{namespace="team-a",pod="web-0",container="app",uid="abc-123",id="/kubepods/burstable/podabc123/def456",image="example.com/app:v1",state="sleeping"} 3

# HELP container_memory_failures_total dummy fixture value, must be dropped by the exact-name list
# TYPE container_memory_failures_total counter
container_memory_failures_total{namespace="team-a",pod="web-0",container="app",uid="abc-123",id="/kubepods/burstable/podabc123/def456",image="example.com/app:v1",failure_type="pgfault",scope="container"} 7

# HELP container_blkio_device_usage_total dummy fixture value, must be dropped by the exact-name list
# TYPE container_blkio_device_usage_total counter
container_blkio_device_usage_total{namespace="team-a",pod="web-0",container="app",uid="abc-123",id="/kubepods/burstable/podabc123/def456",image="example.com/app:v1",device="/dev/sda",operation="Read"} 42

# HELP container_fs_io_time_seconds_bucket dummy fixture value, an ARBITRARY _bucket series the blanket drop must catch
# TYPE container_fs_io_time_seconds_bucket histogram
container_fs_io_time_seconds_bucket{namespace="team-a",pod="web-0",container="app",uid="abc-123",id="/kubepods/burstable/podabc123/def456",image="example.com/app:v1",le="1"} 1

# HELP go_sched_latencies_seconds_bucket dummy fixture value, the ONE _bucket exception that must survive
# TYPE go_sched_latencies_seconds_bucket histogram
go_sched_latencies_seconds_bucket{namespace="team-a",pod="web-0",container="app",uid="abc-123",id="/kubepods/burstable/podabc123/def456",image="example.com/app:v1",le="1"} 1
EOF

cat > "$work/fixture_server.py" <<'EOF'
import http.server

class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path != "/metrics":
            self.send_response(404)
            self.end_headers()
            return
        with open("/fixture/metrics.txt", "rb") as f:
            body = f.read()
        self.send_response(200)
        self.send_header("Content-Type", "text/plain; version=0.0.4")
        self.send_header("Content-Length", str(len(body)))
        self.end_headers()
        self.wfile.write(body)

    def log_message(self, fmt, *args):
        pass

http.server.HTTPServer(("0.0.0.0", 8080), Handler).serve_forever()
EOF

echo "hack/cadvisor-churn-drop-proof.sh: docker network + three containers (fixture, vmsingle, vmagent)"
docker network create "$net" >/dev/null

docker run -d --name "$fixture" --network "$net" \
  -v "$work/metrics.txt:/fixture/metrics.txt:ro" \
  -v "$work/fixture_server.py:/server.py:ro" \
  python:3-alpine python3 /server.py >/dev/null

docker run -d --name "$vmsingle" --network "$net" \
  "victoriametrics/victoria-metrics:$vmsingle_tag" -retentionPeriod=100y >/dev/null

# Container IPs, not container NAMES: this sandbox's docker embedded DNS
# (127.0.0.11) answered SERVFAIL for a plain container-name lookup on
# every run of this script, regardless of `--dns-search`, on a network
# nothing else was attached to — reproduced directly with `nslookup`
# and `wget` inside the vmagent container. The container's own IP on
# this network, read back from `docker inspect` rather than assumed,
# resolves every time; container-name DNS is a docker convenience this
# proof does not depend on.
fixture_ip="$(docker inspect "$fixture" --format "{{(index .NetworkSettings.Networks \"$net\").IPAddress}}")"
vmsingle_ip="$(docker inspect "$vmsingle" --format "{{(index .NetworkSettings.Networks \"$net\").IPAddress}}")"
[ -n "$fixture_ip" ] && [ -n "$vmsingle_ip" ] || {
  echo "hack/cadvisor-churn-drop-proof.sh: could not read the fixture/vmsingle container IPs off the $net network" >&2
  exit 1
}

# A static file-sd target — vmagent's `file_sd_configs`, the same
# mechanism Prometheus itself uses for a target list that is not
# Kubernetes service discovery. `metric_relabel_configs` and
# `honor_labels` below are the extracted JSON above, dumped as YAML, not
# retyped: this is the chart's own render running inside vmagent, not a
# hand-copied stand-in for it.
cat > "$work/targets.yml" <<EOF
- targets: ["$fixture_ip:8080"]
EOF

python3 - "$work/extracted.yml" <<PYEOF
import json, yaml
extracted = json.loads('''$extracted''')
job = {
    "job_name": "cadvisor-fixture",
    "scrape_interval": "2s",
    "scheme": "http",
    "metrics_path": "/metrics",
    "honor_labels": extracted["honor_labels"],
    "file_sd_configs": [{"files": ["/config/targets.yml"]}],
    "metric_relabel_configs": extracted["metric_relabel_configs"],
}
with open("$work/scrape.yml", "w") as f:
    yaml.safe_dump({"scrape_configs": [job]}, f, sort_keys=False)
PYEOF

docker run -d --name "$vmagent" --network "$net" \
  -v "$work/scrape.yml:/config/scrape.yml:ro" \
  -v "$work/targets.yml:/config/targets.yml:ro" \
  "victoriametrics/vmagent:$vmagent_tag" \
  -promscrape.config=/config/scrape.yml \
  -remoteWrite.url="http://$vmsingle_ip:8428/api/v1/write" >/dev/null

# Every query below runs INSIDE the docker network, through a throwaway
# curlimages/curl container, rather than against a host-published port:
# this sandbox's own loopback-to-published-port path was observed to
# hang outright (a plain `curl 127.0.0.1:<published-port>` from the host
# shell timed out with zero bytes, on the very same vmsingle a
# container on its own network reaches immediately) — a property of
# this environment's networking, not of vmsingle or vmagent. `docker
# exec`/`docker run` both go through the docker API rather than raw
# host TCP, which is why they are unaffected.
qcurl() {
  docker run --rm --network "$net" curlimages/curl:latest -fsS "$@"
}

healthy=0
for _ in $(seq 1 30); do
  qcurl "http://$vmsingle_ip:8428/health" >/dev/null 2>&1 && { healthy=1; break; }
  sleep 1
done
[ "$healthy" = 1 ] || {
  echo "hack/cadvisor-churn-drop-proof.sh: vmsingle never became healthy" >&2
  docker logs "$vmsingle" >&2 || true
  exit 1
}
echo "hack/cadvisor-churn-drop-proof.sh: vmsingle healthy at $vmsingle_ip:8428"

names() {
  qcurl "http://$vmsingle_ip:8428/api/v1/label/__name__/values" | python3 -c 'import json,sys; print("\n".join(json.load(sys.stdin)["data"]))'
}

echo "hack/cadvisor-churn-drop-proof.sh: waiting for vmagent to scrape the fixture and vmsingle to index all 5 container_cpu_usage_seconds_total series"
cpu_series_count() {
  qcurl "http://$vmsingle_ip:8428/api/v1/series?match[]=container_cpu_usage_seconds_total" \
    | python3 -c 'import json,sys; print(len(json.load(sys.stdin)["data"]))' 2>/dev/null || echo 0
}
found=0
for _ in $(seq 1 30); do
  current="$(names || true)"
  count="$(cpu_series_count)"
  # ALL FIVE, not merely the metric name: this is exactly the check
  # that would have passed on 1-of-5 (an arbitrary survivor of the
  # collision) had the fix been wrong the same way the labeldrop it
  # replaces was.
  if echo "$current" | grep -q '^go_sched_latencies_seconds_bucket$' && [ "$count" = "5" ]; then
    found=1
    break
  fi
  sleep 1
done
[ "$found" = 1 ] || {
  echo "hack/cadvisor-churn-drop-proof.sh: never reached 5 distinct container_cpu_usage_seconds_total series (last count: ${count:-0}) and/or the kept bucket series after 30s" >&2
  echo "--- vmagent logs ---" >&2
  docker logs "$vmagent" >&2 || true
  echo "--- fixture logs ---" >&2
  docker logs "$fixture" >&2 || true
  exit 1
}

echo
echo "=== __name__ values vmsingle actually holds ==="
all_names="$(names)"
echo "$all_names"
echo

fail=0

assert_absent() {
  if echo "$all_names" | grep -qx "$1"; then
    echo "FAIL: $1 should have been dropped and is NOT — measured as never queried on a real install (see docs/safety.md, \"Metric churn\")" >&2
    fail=1
  else
    echo "PASS: $1 absent"
  fi
}
assert_present() {
  if echo "$all_names" | grep -qx "$1"; then
    echo "PASS: $1 present"
  else
    echo "FAIL: $1 should have been kept and is NOT present" >&2
    fail=1
  fi
}

assert_absent "container_tasks_state"
assert_absent "container_memory_failures_total"
assert_absent "container_blkio_device_usage_total"
assert_absent "container_fs_io_time_seconds_bucket"
assert_present "container_cpu_usage_seconds_total"
assert_present "go_sched_latencies_seconds_bucket"

echo
echo "=== every series vmsingle holds for container_cpu_usage_seconds_total ==="
echo "hack/cadvisor-churn-drop-proof.sh: the fixture above sent 5 label sets for this one metric name — one ordinary container-level series, and four node/pod-level cgroups (the root, the pod-manager slice, a systemd unit, a pod-level rollup) whose id is their ONLY distinguishing label. This is the review finding's own reproduction: an unconditional id labeldrop collapses those four onto ONE identical label set (same job/instance, same empty namespace/pod/container/uid), and vmagent/vmsingle deduplication then keeps an arbitrary one — 5 inputs, fewer than 5 series out. The fix must show exactly 5."
all_series_json="$(qcurl "http://$vmsingle_ip:8428/api/v1/series?match[]=container_cpu_usage_seconds_total")"
echo "$all_series_json" | python3 -m json.tool

cat > "$work/check_series.py" <<'PYEOF'
import json, sys

data = json.loads(sys.stdin.read())["data"]
fail = False

count = len(data)
if count == 5:
    print(f"PASS: {count} distinct series survived for container_cpu_usage_seconds_total — the 5 fixture inputs did not collapse into fewer")
else:
    print(f"FAIL: expected 5 distinct series for container_cpu_usage_seconds_total (5 fixture inputs), vmsingle holds {count} — a labeldrop not properly scoped to container-level series merges node/pod-level cgroups together", file=sys.stderr)
    fail = True

for s in data:
    has_container = bool(s.get("container"))
    has_id = "id" in s
    label = {k: v for k, v in s.items() if k not in ("__name__", "job", "instance")}
    if has_container:
        if has_id:
            print(f"FAIL: container-level series {label} still carries id — should have been cleared (container is set)", file=sys.stderr)
            fail = True
        else:
            print(f"PASS: container-level series {label} has id cleared")
    else:
        if has_id:
            print(f"PASS: non-container series {label} kept its id — nothing else would identify it")
        else:
            print(f"FAIL: non-container series {label} lost its id, and nothing else identifies it — this IS the collision the fix exists to prevent", file=sys.stderr)
            fail = True

sys.exit(1 if fail else 0)
PYEOF
if ! echo "$all_series_json" | python3 "$work/check_series.py"; then
  fail=1
fi

echo
if [ "$fail" = 0 ]; then
  echo "hack/cadvisor-churn-drop-proof.sh: PASS — a real vmagent scrape of a fixture, remote-written to a real VictoriaMetrics single, shows exactly the drop docs/safety.md's \"Metric churn\" section and CHANGELOG.md's 0.9.1 entry claim."
else
  echo "hack/cadvisor-churn-drop-proof.sh: FAIL" >&2
  exit 1
fi

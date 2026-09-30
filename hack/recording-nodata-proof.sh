#!/usr/bin/env bash
# hack/recording-nodata-proof.sh — REAL proof that upstream's
# `RecordingRulesNoData` no longer flags `count:up0`, a recording that is
# empty by design while every target is up, and still flags any other
# recording that produces no data.
#
# Three steps, none of them a stand-in:
#   1. Render charts/observability-stack and read the sync job's own
#      ConfigMap and image.
#   2. Run the vendored sync job's REAL image against that config and a
#      throwaway fake API server that records what the job applies. The job
#      fetches upstream's rules over the network. Run once with the
#      chart's override and once without it: the run WITHOUT must apply
#      upstream's expression exactly as this script expects (so a change
#      upstream fails here instead of drifting under the copy in
#      values.yaml), and the run WITH must apply the expression the chart
#      sets.
#   3. Seed a real victoria-metrics (the vendored appVersion) with the
#      series vmalert exports for three recordings — `count:up0` and
#      `other:empty` with 0 samples, `other:full` with 5 — and evaluate
#      both expressions: upstream's flags all the empty ones, the applied
#      one flags `other:empty` only.
#
# Needs Docker, curl, helm, python3 with PyYAML, and network access to
# raw.githubusercontent.com and ghcr.io. Deliberately NOT part of `check` or
# CI: run by hand.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d -t recording-nodata-proof.XXXXXX)"
vm=recording-nodata-proof-vm
me="hack/recording-nodata-proof.sh"
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

upstream='sum(vmalert_recording_rules_last_evaluation_samples) without(id) < 1'

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
grep -q 'RecordingRulesNoData' "$work/config.yaml" \
  || { echo "FAIL: rendered sync-job config carries no RecordingRulesNoData override" >&2; exit 1; }
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
cat > "$work/kubeconfig" <<'EOF2'
apiVersion: v1
kind: Config
clusters: [{name: c, cluster: {server: "http://127.0.0.1:18082"}}]
users: [{name: u, user: {token: t}}]
contexts: [{name: c, context: {cluster: c, user: u}}]
current-context: c
EOF2
# "without": the same config with the override block removed.
python3 - "$work" <<'PYEOF'
import sys, yaml
work = sys.argv[1]
c = yaml.safe_load(open(work + "/config.yaml"))
assert "RecordingRulesNoData" in c["rules"]["rules"]
del c["rules"]["rules"]["RecordingRulesNoData"]
if not c["rules"]["rules"]:
    del c["rules"]["rules"]
yaml.safe_dump(c, open(work + "/config-without.yaml", "w"))
PYEOF
declare -A expr
for variant in with without; do
  src="$work/config.yaml"; [ "$variant" = without ] && src="$work/config-without.yaml"
  : > "$work/writes-$variant.jsonl"
  python3 "$work/fakeapi.py" 18082 "$work/writes-$variant.jsonl" & api_pid=$!
  sleep 1
  mkdir -p "$work/etc-$variant"; cp "$src" "$work/etc-$variant/config.yaml"
  docker run --rm --network host -v "$work/etc-$variant:/etc/config:ro" -v "$work/kubeconfig:/kc:ro" \
    -e KUBECONFIG=/kc "$image" 2>&1 | tail -1
  kill "$api_pid"; api_pid=""
  expr[$variant]="$(python3 - "$work/writes-$variant.jsonl" <<'PYEOF'
import json, sys
for line in open(sys.argv[1]):
    d = json.loads(json.loads(line)["body"])
    for g in d.get("spec", {}).get("groups", []):
        for r in g["rules"]:
            if r.get("alert") == "RecordingRulesNoData":
                sys.stdout.write(" ".join(r["expr"].split()))
                sys.exit(0)
sys.exit("alert not applied")
PYEOF
  )"
  echo "--- $variant the chart's override"
  echo "${expr[$variant]}"
done
[[ "${expr[without]}" == "$upstream" ]] \
  || { echo "FAIL: upstream's RecordingRulesNoData is no longer '$upstream'; update the override in values.yaml" >&2; exit 1; }
[[ "${expr[with]}" == *'recording!~"count:up0"'* && "${expr[with]}" == *'< 1' ]] \
  || { echo "FAIL: the override was not applied" >&2; exit 1; }

echo
echo "##### 3. a real store: which empty recordings each expression flags"
docker run -d --name "$vm" -p 127.0.0.1::8428 "victoriametrics/victoria-metrics:$vm_version" -retentionPeriod=100y >/dev/null
port=""
for _ in $(seq 1 30); do
  port="$(docker port "$vm" 8428/tcp 2>/dev/null | head -1 | cut -d: -f2 || true)"
  [ -n "$port" ] && curl -fsS "http://127.0.0.1:$port/health" >/dev/null 2>&1 && break
  port=""; sleep 1
done
[ -n "$port" ] || { echo "$me: victoria-metrics never became healthy" >&2; exit 1; }
now="$(( $(date +%s) - 60 ))"
g='group="kube-prometheus-general.rules",file="/etc/vmalert/rules/x.yaml"'
{
  echo "vmalert_recording_rules_last_evaluation_samples{$g,recording=\"count:up0\",id=\"1\"} 0 $((now * 1000))"
  echo "vmalert_recording_rules_last_evaluation_samples{$g,recording=\"other:empty\",id=\"2\"} 0 $((now * 1000))"
  echo "vmalert_recording_rules_last_evaluation_samples{$g,recording=\"other:full\",id=\"3\"} 5 $((now * 1000))"
} > "$work/seed.prom"
curl -fsS -X POST "http://127.0.0.1:$port/api/v1/import/prometheus" --data-binary "@$work/seed.prom" >/dev/null
n=0
for _ in $(seq 1 30); do
  n="$(curl -fsS "http://127.0.0.1:$port/api/v1/query" --data-urlencode 'query=count(vmalert_recording_rules_last_evaluation_samples)' --data-urlencode "time=$now" \
    | python3 -c 'import json,sys; r=json.load(sys.stdin)["data"]["result"]; print(r[0]["value"][1] if r else 0)')"
  [ "$n" = 3 ] && break; sleep 1
done
[ "$n" = 3 ] || { echo "$me: seeded series never became queryable ($n/3)" >&2; exit 1; }

flagged() { # expr -> sorted recording names the expression returns
  curl -sS "http://127.0.0.1:$port/api/v1/query" --data-urlencode "query=$1" --data-urlencode "time=$now" \
  | python3 -c '
import json, sys
d = json.load(sys.stdin)
if d.get("status") != "success":
    print("ERR", str(d.get("error"))[:200]); sys.exit()
print(" ".join(sorted(x["metric"]["recording"] for x in d["data"]["result"])))'
}
up="$(flagged "${expr[without]}")"; ch="$(flagged "${expr[with]}")"
echo "upstream's expression flags: $up"
echo "the chart's expression flags: $ch"
fail=0
[ "$up" = "count:up0 other:empty" ] || { echo "FAIL: upstream's expression was expected to flag count:up0 and other:empty" >&2; fail=1; }
[ "$ch" = "other:empty" ] || { echo "FAIL: the chart's expression must flag other:empty and not count:up0" >&2; fail=1; }
echo
[ "$fail" = 0 ] && echo "$me: OK — count:up0 (empty when healthy) is no longer flagged; a recording that produces no data still is"
exit $fail

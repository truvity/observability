#!/usr/bin/env bash
# hack/serverless-stream-aggregation-proof.sh — REAL proof, against the
# pinned VictoriaMetrics single binary, that the
# `serverless-stream-aggregation` preset of charts/observability-stack does
# what docs/safety.md ("Metric churn: serverless sources") says:
#
#   - OTLP counters and histogram buckets/_sum/_count sent by TWO instances,
#     one of which resets, come out as ONE series with no `instance` and no
#     `faas_instance`, and the aggregate rose by exactly the sum of both
#     instances' increases (the reset counted as the instance's own restart
#     from zero, not as a drop of the sum);
#   - a gauge reported by both instances comes out as the larger of the two;
#   - a series from another source (`telemetry_source` other than the
#     preset's) is stored untouched, instance labels and all;
#   - the Kargo series shape: duplicate samples of one label set at one
#     timestamp (what `kargo_promotion_phase` without a `promotion` label
#     produces, one per Promotion of a Stage) read back as the LARGEST value
#     under the store's deduplication.
#
# The rules are read off the real render (helm template of the stack with
# the preset), not retyped; only the interval is shortened (to 5s, from 1m)
# so the proof does not wait minutes.
#
# One container: victoriametrics/victoria-metrics at the version the
# vendored victoria-metrics-k8s-stack pins. Needs docker, curl, python3
# (PyYAML) and helm. Not part of `check` or CI: a run-by-hand proof.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d -t serverless-streamaggr-proof.XXXXXX)"
vm=serverless-streamaggr-proof-vm
cleanup() {
  docker rm -f "$vm" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

for tool in docker curl python3 helm; do
  command -v "$tool" >/dev/null 2>&1 || { echo "needs $tool on PATH" >&2; exit 1; }
done
python3 -c 'import yaml' 2>/dev/null || { echo "needs python3 PyYAML" >&2; exit 1; }

vm_tgz=("$root"/charts/observability-stack/charts/victoria-metrics-k8s-stack-*.tgz)
vm_tag="$(tar -xzOf "${vm_tgz[0]}" victoria-metrics-k8s-stack/Chart.yaml | awk '/^appVersion:/ { print $2 }')"
echo "serverless-stream-aggregation-proof: victoria-metrics $vm_tag (the vendored pin)"

helm template x "$root/charts/observability-stack" \
  -f "$root/charts/observability-stack/presets/serverless-stream-aggregation.yaml" \
  -f "$root/tests/cases/observability-stack/minimal/values.yaml" > "$work/render.yaml"

python3 - "$work/render.yaml" "$work/streamaggr.yaml" <<'PYEOF'
import sys, yaml
single = next(d for d in yaml.safe_load_all(open(sys.argv[1])) if d and d.get("kind") == "VMSingle")
cfg = single["spec"]["streamAggrConfig"]
assert cfg["keepInput"] is False, cfg
rules = cfg["rules"]
assert len(rules) == 2, rules
for r in rules:
    r["interval"] = "5s"
yaml.safe_dump(rules, open(sys.argv[2], "w"))
PYEOF
echo "--- rules (interval shortened to 5s) ---"; cat "$work/streamaggr.yaml"

docker run -d --name "$vm" -p 127.0.0.1:18428:8428 \
  -v "$work/streamaggr.yaml:/streamaggr.yaml:ro" \
  "victoriametrics/victoria-metrics:$vm_tag" \
  -retentionPeriod=100y -dedup.minScrapeInterval=30s -search.latencyOffset=0s \
  -streamAggr.config=/streamaggr.yaml -streamAggr.keepInput=false >/dev/null
for _ in $(seq 1 30); do curl -fs http://127.0.0.1:18428/health >/dev/null && break; sleep 1; done
curl -fs http://127.0.0.1:18428/health >/dev/null || { docker logs "$vm"; exit 1; }

url=http://127.0.0.1:18428

# push <instance> <source> <counter> <histo-count> <histo-sum> <gauge>:
# one OTLP/protobuf request (VictoriaMetrics refuses OTLP/JSON), cumulative,
# with the labels as data point attributes. The protobuf is encoded by hand
# (varints and length-delimited fields) so the proof needs no OTLP library.
push() {
  python3 - "$@" <<'PYEOF' | curl -fsS -X POST -H 'Content-Type: application/x-protobuf' --data-binary @- "$url/opentelemetry/v1/metrics" >/dev/null
import sys, time, struct
inst, src, counter, hcount, hsum, gauge = sys.argv[1:7]
now = int(time.time() * 1e9)

def varint(n):
    out = bytearray()
    while True:
        b = n & 0x7F
        n >>= 7
        out.append(b | (0x80 if n else 0))
        if not n:
            return bytes(out)
def ld(field, payload):  # length-delimited
    if isinstance(payload, str):
        payload = payload.encode()
    return varint(field << 3 | 2) + varint(len(payload)) + payload
def vint(field, n):
    return varint(field << 3 | 0) + varint(n)
def f64(field, v):       # fixed64 (timestamps, counts)
    return varint(field << 3 | 1) + struct.pack("<Q", int(v))
def dbl(field, v):       # double
    return varint(field << 3 | 1) + struct.pack("<d", float(v))

def attr(k, v):
    return ld(1, k) + ld(2, ld(1, v))
labels = [("telemetry_source", src), ("namespace", "external"), ("instance", inst), ("faas_instance", "fi-" + inst), ("function", "billing")]
def attrs(field):
    return b"".join(ld(field, attr(k, v)) for k, v in labels)

def number_point(v):
    return attrs(7) + f64(3, now) + dbl(4, v)
counter_m = ld(1, "invocations_total") + ld(7, ld(1, number_point(counter)) + vint(2, 2) + vint(3, 1))
gauge_m = ld(1, "memory_used_bytes") + ld(5, ld(1, number_point(gauge)))
hist_point = (attrs(9) + f64(3, now) + f64(4, hcount) + dbl(5, hsum)
              + ld(6, struct.pack("<QQ", int(hcount), 0)) + ld(7, struct.pack("<d", 1.0)))
hist_m = ld(1, "duration_seconds") + ld(9, ld(1, hist_point) + vint(2, 2))
req = ld(1, ld(2, ld(2, counter_m) + ld(2, hist_m) + ld(2, gauge_m)))
sys.stdout.buffer.write(req)
PYEOF
}

q() { curl -fsS --data-urlencode "query=$1" "$url/api/v1/query" | python3 -c '
import sys, json
r = json.load(sys.stdin)["data"]["result"]
print(json.dumps(sorted([x["metric"], x["value"][1]] for x in r)))'; }

flush() { sleep 12; }   # two intervals + slack

# 1. baseline: both instances report once.
push a external 10 10 5 7;  push b external 100 100 50 9; push a other 1 1 1 1
flush
v0_c="$(q 'sum(invocations_total{telemetry_source="external"})' | python3 -c 'import sys,json; print(json.load(sys.stdin)[0][1])')"
v0_h="$(q 'sum(duration_seconds_count{telemetry_source="external"})' | python3 -c 'import sys,json; print(json.load(sys.stdin)[0][1])')"
v0_b="$(q 'sum(duration_seconds_bucket{telemetry_source="external",le="1"})' | python3 -c 'import sys,json; print(json.load(sys.stdin)[0][1])')"
v0_s="$(q 'sum(duration_seconds_sum{telemetry_source="external"})' | python3 -c 'import sys,json; print(json.load(sys.stdin)[0][1])')"

# 2. a: +5, b: +30. 3. a resets to 3 (its own restart: +3), b: +20.
push a external 15 15 8 7;  push b external 130 130 80 9; flush
push a external 3 3 2 7;    push b external 150 150 100 9; flush

# The VM sample timestamps are the flush times; wait for the last flush.
sleep 8
expect_total=58          # 5 + 30 + 3 + 20
expect_sum=$((8-5 + 2 + 80-50 + 100-80))   # histogram _sum: a +3, reset to 2 -> +2, b +30, +20

fail=0
check() { # name actual expected
  if python3 -c 'import sys; sys.exit(0 if abs(float(sys.argv[1])-float(sys.argv[2]))<1e-6 else 1)' "$2" "$3"; then
    echo "ok   $1: $2"
  else
    echo "FAIL $1: got $2, want $3"; fail=1
  fi
}
get() { q "$1" | python3 -c 'import sys,json; print(json.load(sys.stdin)[0][1])'; }

v1_c="$(get 'sum(invocations_total{telemetry_source="external"})')"
v1_h="$(get 'sum(duration_seconds_count{telemetry_source="external"})')"
v1_b="$(get 'sum(duration_seconds_bucket{telemetry_source="external",le="1"})')"
v1_s="$(get 'sum(duration_seconds_sum{telemetry_source="external"})')"
check "counter rose by the sum of both instances' increases" "$(python3 -c "print($v1_c-$v0_c)")" "$expect_total"
check "histogram _count rose likewise" "$(python3 -c "print($v1_h-$v0_h)")" "$expect_total"
check "histogram le=1 bucket rose likewise" "$(python3 -c "print($v1_b-$v0_b)")" "$expect_total"
check "histogram _sum rose likewise" "$(python3 -c "print($v1_s-$v0_s)")" "$expect_sum"

echo "--- external series in the store ---"
curl -fsS "$url/api/v1/series" --data-urlencode 'match[]={telemetry_source="external"}' | python3 -c '
import sys, json
for m in sorted(json.dumps(x, sort_keys=True) for x in json.load(sys.stdin)["data"]): print(m)'

leaked="$(curl -fsS "$url/api/v1/series" --data-urlencode 'match[]={telemetry_source="external",instance!=""}' | python3 -c 'import sys,json; print(len(json.load(sys.stdin)["data"]))')"
leaked2="$(curl -fsS "$url/api/v1/series" --data-urlencode 'match[]={telemetry_source="external",faas_instance!=""}' | python3 -c 'import sys,json; print(len(json.load(sys.stdin)["data"]))')"
check "external series carrying an instance label" "$leaked" 0
check "external series carrying a faas_instance label" "$leaked2" 0

check "gauge: the larger of the two instances" "$(get 'max(memory_used_bytes{telemetry_source="external"})')" 9
check "gauge: one series, not two" "$(get 'count(memory_used_bytes{telemetry_source="external"})')" 1
check "other source: stored untouched, instance label kept" "$(get 'count(invocations_total{telemetry_source="other",instance="a"})')" 1

# The Kargo shape: two samples of one label set at one timestamp.
printf 'kargo_promotion_phase{namespace="p",stage="s",phase="Errored"} 0\nkargo_promotion_phase{namespace="p",stage="s",phase="Errored"} 1\nkargo_promotion_phase{namespace="p",stage="s",phase="Errored"} 0\n' \
  | curl -fsS -X POST --data-binary @- "$url/api/v1/import/prometheus"
sleep 6   # a written sample is searchable after a few seconds
check "duplicate samples of one Kargo label set read back as the largest" "$(get 'kargo_promotion_phase{stage="s",phase="Errored"}')" 1

[ "$fail" = 0 ] && echo "serverless-stream-aggregation-proof: all assertions hold"
exit "$fail"

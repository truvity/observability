#!/usr/bin/env bash
# hack/self-alerts-source-absent-proof.sh — REAL proof, against a real
# victoria-metrics binary, that charts/observability-stack's opt-in
# `selfAlerts.sourceAbsent` guard (SelfAlertSourceAbsent) is per cluster.
#
# The failure: every self-alert reads `rate()` of a store counter, and an
# empty vector is "healthy", so when the scrape stops they all go quiet. On a
# store that holds several clusters a bare `absent()` stays false while any
# cluster still reports, so the guard is per cluster. One source metric is
# seeded, with clusters `a` and `b` (generic names), per case:
#
#   both              current on a AND b                  -> no alert
#   b-gone            current on a; b's last sample 2h old -> one alert, cluster b
#   stale-unlabelled  a, b current plus an old series with NO cluster label
#                                                          -> no alert
#   never             the series exists nowhere           -> one alert, no cluster
#
# Only the seeded source's alerts are judged: the guard watches every metric
# name configured, and the others are (deliberately) never seeded.
#
# Needs Docker, curl, helm and python3 with PyYAML. Deliberately NOT part of
# `check` or CI: run by hand, like hack/platform-alerts-absent-proof.sh.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d -t self-alerts-source-absent-proof.XXXXXX)"
container=self-alerts-source-absent-proof-vm
me="hack/self-alerts-source-absent-proof.sh"
trap 'docker rm -f "$container" >/dev/null 2>&1 || true; rm -rf "$work"' EXIT

for tool in docker curl python3 helm; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$me needs $tool, which is not on PATH" >&2; exit 1; }
done
python3 -c 'import yaml' 2>/dev/null || { echo "$me needs python3's PyYAML" >&2; exit 1; }

vm_tgz=("$root"/charts/observability-stack/charts/victoria-metrics-k8s-stack-*.tgz)
[ -e "${vm_tgz[0]}" ] || { echo "$me: no vendored victoria-metrics-k8s-stack — run 'just vendor observability-stack'" >&2; exit 1; }
vm_version="$(tar -xzOf "${vm_tgz[0]}" victoria-metrics-k8s-stack/Chart.yaml | awk '/^appVersion:/ { print $2 }')"
echo "$me: using victoria-metrics:$vm_version, the version charts/observability-stack pins"

cat > "$work/extract.py" <<'PYEOF'
import sys, yaml
for doc in yaml.safe_load_all(sys.stdin.read()):
    if doc and doc.get("kind") == "VMRule":
        for group in doc["spec"]["groups"]:
            for rule in group["rules"]:
                if rule.get("alert") == "SelfAlertSourceAbsent":
                    sys.stdout.write(" ".join(rule["expr"].split()))
                    sys.exit(0)
sys.exit("SelfAlertSourceAbsent not found in the render")
PYEOF

case_dir="$root/tests/cases/observability-stack/selfalerts-source-absent"
expr="$(helm template observability-stack "$root/charts/observability-stack" --namespace "$(cat "$case_dir/namespace" 2>/dev/null || echo default)" \
  -f "$case_dir/values.yaml" | python3 "$work/extract.py")"
cl=k8s_cluster_name
src="$(python3 -c 'import re,sys; print(re.search(r"\"source\", \"([^\"]*rows_ignored_total)\"", sys.argv[1]).group(1))' "$expr")"
echo "$me: cluster label $cl, source $src"

docker run -d --name "$container" -p 127.0.0.1::8428 \
  "victoriametrics/victoria-metrics:$vm_version" -retentionPeriod=100y >/dev/null
port=""
for _ in $(seq 1 30); do
  port="$(docker port "$container" 8428/tcp 2>/dev/null | head -1 | cut -d: -f2 || true)"
  [ -n "$port" ] && curl -fsS "http://127.0.0.1:$port/health" >/dev/null 2>&1 && break
  port=""; sleep 1
done
[ -n "$port" ] || { echo "$me: victoria-metrics never became healthy" >&2; exit 1; }

# Each case gets its own "now", three days apart (the lookback is at most a
# day), and nothing is deleted between cases.
slot=0
now=0
wipe() { slot=$((slot + 1)); now=$(( $(date +%s) - 86400 * 400 + slot * 3 * 86400 )); }
ms() { echo $((($now - $1) * 1000)); }
old=7200 # b's last sample: inside the lookback, outside the 5m staleness
load() {
  curl -fsS -X POST "http://127.0.0.1:$port/api/v1/import/prometheus" --data-binary "@$1" >/dev/null
  curl -fsS "http://127.0.0.1:$port/internal/force_flush" >/dev/null
  sleep 1
}
query() { # -> sorted "<cluster>" of the alerts whose source is $src, or "<none>"
  curl -fsS "http://127.0.0.1:$port/api/v1/query" --data-urlencode "query=$expr" --data-urlencode "time=$now" \
    | python3 -c '
import json, sys
cl, src = sys.argv[1], sys.argv[2]
r = [x["metric"] for x in json.load(sys.stdin)["data"]["result"] if x["metric"].get("source") == src]
print(" ".join(sorted(m.get(cl, "-") for m in r)) or "<none>")' "$cl" "$src"
}
fail=0
check() { if [ "$2" = "$3" ]; then echo "ok    $1 -> $3"; else echo "FAIL  $1 -> $3 (wanted $2)" >&2; fail=1; fi; }
series() { echo "$src{${1}job=\"x\"} 1 $(ms "$2")"; }

wipe; { series "$cl=\"a\"," 0; series "$cl=\"b\"," 0; } > "$work/s.prom"; load "$work/s.prom"
check "both-present" "<none>" "$(query)"
wipe; { series "$cl=\"a\"," 0; series "$cl=\"b\"," 0; series "" $old; } > "$work/s.prom"; load "$work/s.prom"
check "unlabelled-stale-beside-healthy" "<none>" "$(query)"
wipe; { series "$cl=\"a\"," 0; series "$cl=\"b\"," $old; } > "$work/s.prom"; load "$work/s.prom"
check "b-gone" "b" "$(query)"
wipe
check "never" "-" "$(query)"

echo
[ "$fail" = 0 ] && echo "$me: OK — the guard fires per cluster, stays quiet when all report, and fires store-wide when the series never existed"
exit $fail

#!/usr/bin/env bash
# hack/platform-alerts-absent-proof.sh — REAL proof, against a real
# victoria-metrics binary, that charts/platform-alerts' `*Absent` guards are
# per cluster, and that a bare `absent()` was not.
#
# The bug: one metrics store holds several clusters' series, and a bare
# `absent(up{...})` is true only when the series is missing from EVERY
# cluster. A controller vanishing from one cluster, while the others still
# report it, left every guard silent.
#
# For each rendered guard the same three cases are seeded, with clusters `a`
# and `b` (generic names, nothing else about the estate):
#
#   both     the series is current on a AND b       -> no alert
#   b-gone   current on a; b's last sample is 2h old -> one alert, cluster b
#   never    the series exists nowhere              -> one alert, no cluster
#                                                      label (whole store)
#
# and, for the old expression (`clusterLabel=` AND the pre-fix `absent()`
# form is what `clusterLabel=` still renders), b-gone is shown to be silent.
# ACK with `expectedNamespaces` also seeds a controller that never existed on
# b while b runs another one: the alert names b and the missing namespace.
#
# Needs Docker, curl, helm and python3 with PyYAML. Deliberately NOT part of
# `check` or CI, like the other platform-alerts proofs: run by hand
# (`just platform-alerts-absent-proof`).
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d -t platform-alerts-absent-proof.XXXXXX)"
container=platform-alerts-absent-proof-vm
me="hack/platform-alerts-absent-proof.sh"
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
alert = sys.argv[1]
for doc in yaml.safe_load_all(sys.stdin.read()):
    if doc and doc.get("kind") == "VMRule":
        for group in doc["spec"]["groups"]:
            for rule in group["rules"]:
                if rule.get("alert") == alert:
                    sys.stdout.write(" ".join(rule["expr"].split()))
                    sys.exit(0)
sys.exit(alert + " not found in the render")
PYEOF

base=(--values "$root/tests/cases/platform-alerts/minimal/values.yaml")
render() { helm template x "$root/charts/platform-alerts" "${base[@]}" "$@"; }
expr_of() { python3 "$work/extract.py" "$1"; }

docker run -d --name "$container" -p 127.0.0.1::8428 \
  "victoriametrics/victoria-metrics:$vm_version" -retentionPeriod=100y >/dev/null
port=""
for _ in $(seq 1 30); do
  port="$(docker port "$container" 8428/tcp 2>/dev/null | head -1 | cut -d: -f2 || true)"
  [ -n "$port" ] && curl -fsS "http://127.0.0.1:$port/health" >/dev/null 2>&1 && break
  port=""; sleep 1
done
[ -n "$port" ] || { echo "$me: victoria-metrics never became healthy" >&2; exit 1; }

# Every case gets its own "now", three days apart, so it never sees a
# neighbour's samples (the lookback is one day) and nothing is ever deleted
# from the store between cases: a delete would hide a re-ingested identical
# series until the store's caches catch up.
now="$(( $(date +%s) - 86400 * 400 ))"
slot=0
next_slot() { slot=$((slot + 1)); now=$(( $(date +%s) - 86400 * 400 + slot * 3 * 86400 )); }
ms() { echo $((($now - $1) * 1000)); }
old=7200 # b's last sample, 2h before "now": inside the 1d lookback, outside the 5m staleness

wipe() { next_slot; }
load() { # file
  curl -fsS -X POST "http://127.0.0.1:$port/api/v1/import/prometheus" --data-binary "@$1" >/dev/null
  curl -fsS "http://127.0.0.1:$port/internal/force_flush" >/dev/null
  sleep 1
}
query() { # expr -> sorted "<cluster>/<namespace>" lines, or "<none>"
  curl -fsS "http://127.0.0.1:$port/api/v1/query" --data-urlencode "query=$1" --data-urlencode "time=$now" \
    | python3 -c '
import json, sys
d = json.load(sys.stdin)
r = d["data"]["result"]
print(" ".join(sorted("%s/%s" % (x["metric"].get("k8s_cluster_name", "-"), x["metric"].get("namespace", "-")) for x in r)) or "<none>")'
}

fail=0
check() { # label want got
  if [ "$2" = "$3" ]; then echo "ok    $1 -> $3"; else echo "FAIL  $1 -> $3 (wanted $2)" >&2; fail=1; fi
}

# guard ALERT "render flags" "series template with @C@ and @T@"
#   Series lines are given once; @C@ is the cluster label fragment, @T@ the
#   timestamp in ms.
guard() { # alert flags... -- series-lines
  local alert="$1"; shift
  local flags=()
  while [ "$1" != "--" ]; do flags+=("$1"); shift; done
  shift
  local lines="$1" new_expr bare_expr
  new_expr="$(render "${flags[@]}" | expr_of "$alert")"
  bare_expr="$(render "${flags[@]}" --set clusterLabel= | expr_of "$alert")"
  echo "=== $alert ==="
  echo "$new_expr"
  seed() { # a-age b-age
    wipe
    local out="$work/seed.prom"
    : > "$out"
    [ "$1" != none ] && sed -e "s/@C@/k8s_cluster_name=\"a\",/g" -e "s/@T@/$(ms "$1")/g" <<<"$lines" >> "$out"
    [ "$2" != none ] && sed -e "s/@C@/k8s_cluster_name=\"b\",/g" -e "s/@T@/$(ms "$2")/g" <<<"$lines" >> "$out"
    [ -s "$out" ] && load "$out" || true
  }
  seed 0 0;        check "$alert both-present" "<none>" "$(query "$new_expr")"
  seed 0 $old;     check "$alert b-gone" "b/${ONLY_NS:--}" "$(query "$new_expr")"
                   check "$alert b-gone (bare absent, the old rule)" "<none>" "$(query "$bare_expr")"
  seed none none;  check "$alert never" "${NEVER:--/-}" "$(query "$new_expr")"
}

NEVER="-/external-secrets" guard ESOMetricsAbsent --set groups.eso.enabled=true -- \
  'up{@C@namespace="external-secrets",job="eso"} 1 @T@'
NEVER="-/external-secrets" guard ESOWebhookAbsent --set groups.eso.enabled=true -- \
  'up{@C@namespace="external-secrets",job="external-secrets-webhook"} 1 @T@'
guard ArgoCDMetricsAbsent --set groups.argocd.enabled=true -- \
  'argocd_app_info{@C@job="argocd-application-controller-metrics",name="x"} 1 @T@'
guard NATSMetricsAbsent --set groups.nats.enabled=true -- \
  'up{@C@job="nats/nats"} 1 @T@'
guard NodeClaimMetricsAbsent --set groups.nodeClaims.enabled=true -- \
  'nodeclaim_status_condition{@C@type="Launched",nodeclaim="n"} 1 @T@'
guard KargoControllerAbsent --set groups.kargo.enabled=true --set groups.kargo.controllerAbsent.enabled=true -- \
  'up{@C@job="kargo-controller-metrics"} 1 @T@'
# two series, two absent clauses: both fire when neither ever existed
NEVER="-/- -/-" guard KargoStateMetricsAbsent --set groups.kargo.enabled=true -- \
  'kargo_stage_condition{@C@type="Ready",stage="s"} 1 @T@
kargo_promotion_phase{@C@promotion="p",phase="Errored"} 0 @T@'
guard HTTPProbeDownAbsent --set groups.probes.enabled=true --set groups.probes.probe=example -- \
  'probe_success{@C@probe="example"} 1 @T@'
ONLY_NS=ack-iam guard ACKControllerAbsent --set groups.ack.enabled=true -- \
  'up{@C@namespace="ack-iam"} 1 @T@'

# ACK with expectedNamespaces: b runs ack-iam but never ran ack-s3.
echo "=== ACKControllerAbsent, expectedNamespaces ==="
ack_flags=(--set groups.ack.enabled=true --set 'groups.ack.expectedNamespaces={ack-iam,ack-s3}')
ack_expr="$(render "${ack_flags[@]}" | expr_of ACKControllerAbsent)"
echo "$ack_expr"
wipe
{
  echo "up{k8s_cluster_name=\"a\",namespace=\"ack-iam\"} 1 $(ms 0)"
  echo "up{k8s_cluster_name=\"a\",namespace=\"ack-s3\"} 1 $(ms 0)"
  echo "up{k8s_cluster_name=\"b\",namespace=\"ack-iam\"} 1 $(ms 0)"
} > "$work/ack.prom"
load "$work/ack.prom"
check "ACK expected, b never ran ack-s3" "b/ack-s3" "$(query "$ack_expr")"
wipe
check "ACK expected, nothing anywhere" "-/ack-iam -/ack-s3" "$(query "$ack_expr")"

echo
if [ "$fail" = 0 ]; then
  echo "$me: OK — every absent guard fires per cluster (series gone on one of two), stays quiet when all report, and still fires when the series never existed"
fi
exit $fail

#!/usr/bin/env bash
# hack/multi-group-reader-proof.sh — REAL proof, against the pinned vmauth,
# VictoriaMetrics and VictoriaLogs binaries, that a `tenancy.principals[]`
# entry with several `groups` and one grant per cluster lets a token read
# EVERY cluster in the store, and that the shape it replaces did not.
#
# The bug this guards against: a role granted the same reach on several
# clusters, rendered as ONE principal PER CLUSTER, each selected by that
# cluster's group. vmauth takes the FIRST user a token matches and never a
# union, so a token holding all the groups is filtered to whichever
# cluster's user came first, and the person sees one cluster of several
# although the store holds them all.
#
# What runs (all real software; nothing is mocked but the identity issuer,
# which is a python file server publishing a discovery document and a JWKS
# for a key generated here):
#   vmsingle + victoria-logs  each holding series and log lines for
#                             cluster-a, cluster-b, cluster-c AND cluster-z;
#                             cluster-z is in no principal's grants.
#   vmauth                    configured FROM THE RENDERED CHART
#                             (tests/cases/observability-stack/tenancy-multi-group,
#                             through `helm template`), the VMUser translated
#                             into vmauth's own file format field for field.
#   control                   the same chart, the old shape: one single-group
#                             principal per cluster.
#
# Cases, each asserting the clusters a token READS on metrics AND on logs:
#   all three groups + aud ok   -> a,b,c
#   ONLY cluster-b's group      -> a,b,c   (correct under the grant-matrix
#                                            assumption: every holder of one
#                                            spelling holds them all; the
#                                            caller must guarantee that)
#   a group in no principal     -> 401
#   right groups, wrong aud     -> 401
#   control, all three groups   -> a only  (the bug)
#
# Needs Docker, python3 (PyYAML, PyJWT, cryptography), helm and curl. Not part
# of `check` or CI, like the other hack/*-proof.sh: a run-by-hand proof.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d -t mgr-proof.XXXXXX)"
net=mgr-proof-net
cleanup() {
  docker ps -aq --filter "label=mgr-proof" | xargs -r docker rm -f >/dev/null 2>&1 || true
  docker network rm "$net" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

for tool in docker curl python3 helm; do
  command -v "$tool" >/dev/null 2>&1 || { echo "hack/multi-group-reader-proof.sh needs $tool on PATH" >&2; exit 1; }
done
python3 -c 'import yaml, jwt, cryptography' 2>/dev/null || { echo "needs python3 with PyYAML, PyJWT and cryptography" >&2; exit 1; }

vm_tgz=("$root"/charts/observability-stack/charts/victoria-metrics-k8s-stack-*.tgz)
vm_tag="$(tar -xzOf "${vm_tgz[0]}" victoria-metrics-k8s-stack/Chart.yaml | awk '/^appVersion:/ { print $2 }')"
vl_tgz=("$root"/charts/observability-stack/charts/victoria-logs-single-*.tgz)
vl_tag="$(tar -xzOf "${vl_tgz[0]}" victoria-logs-single/Chart.yaml | awk '/^appVersion:/ { print $2 }')"
vmauth_tag="$(awk '/^vmauth:/{m=1} m && /^ *tag: /{print $2; exit}' "$root/charts/observability-stack/values.yaml")"
echo "vmauth $vmauth_tag, victoria-metrics $vm_tag, victoria-logs $vl_tag (the chart's pins)"

case_values="$root/tests/cases/observability-stack/tenancy-multi-group/values.yaml"
helm template x "$root/charts/observability-stack" -f "$case_values" > "$work/chart.yaml"

# The control: the same case with the principal split into one per cluster.
python3 - "$case_values" "$work/control-values.yaml" <<'PY'
import sys, yaml
v = yaml.safe_load(open(sys.argv[1]))
old = v["tenancy"]["principals"][0]
v["tenancy"]["principals"] = [
    {"group": g, "grants": [gr]} for g, gr in zip(old["groups"], old["grants"])
]
yaml.safe_dump(v, open(sys.argv[2], "w"))
PY
helm template x "$root/charts/observability-stack" -f "$work/control-values.yaml" > "$work/control.yaml"

# Translate the rendered VMUsers into vmauth's own file, as the operator
# does. Only the backend addresses and the issuer are swapped for the
# containers; matchClaims and the default claim are copied untouched.
python3 - "$work" <<'PY'
import sys, yaml
work = sys.argv[1]
def convert(src, dst):
    users = []
    for d in yaml.safe_load_all(open(f"{work}/{src}.yaml")):
        if not d or d.get("kind") != "VMUser" or d["metadata"]["labels"].get("observability.role") != "reader":
            continue
        s = d["spec"]
        rows = []
        for ref in s["targetRefs"]:
            url = ref["static"]["url"]
            url = "http://mgr-vm:8428" if ":8428" in url else "http://mgr-vl:9428"
            args = "&".join(f"{a['name']}={v}" for a in ref["query_args"] for v in a["values"])
            rows.append({"src_paths": ref["paths"], "url_prefix": f"{url}?{args}"})
        users.append({
            "name": s["name"],
            "jwt": {
                "oidc": {"issuer": "http://mgr-oidc:8080"},
                "match_claims": s["jwt"]["matchClaims"],
                "default_vm_access_claim": {
                    "metrics_extra_filters": s["jwt"]["defaultVMAccessClaim"]["metricsExtraFilters"],
                    "logs_extra_stream_filters": s["jwt"]["defaultVMAccessClaim"]["logsExtraStreamFilters"],
                },
            },
            "url_map": rows,
        })
    yaml.safe_dump({"users": users}, open(f"{work}/{dst}.yml", "w"), sort_keys=False)
    print(f"--- {dst}.yml: {len(users)} reader user(s)")
    for u in users:
        print(f"    {u['name']}: groups={u['jwt']['match_claims']['groups']}")
        print(f"      metrics_extra_filters={u['jwt']['default_vm_access_claim']['metrics_extra_filters']}")
        print(f"      logs_extra_stream_filters={u['jwt']['default_vm_access_claim']['logs_extra_stream_filters']}")
convert("chart", "vmauth-chart")
convert("control", "vmauth-control")
PY

# The issuer: a key, its JWKS, the discovery document, and the tokens.
python3 - "$work" <<'PY'
import sys, json, time, base64, jwt
from cryptography.hazmat.primitives.asymmetric import rsa
from cryptography.hazmat.primitives import serialization
work = sys.argv[1]
key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
n = key.public_key().public_numbers()
b64 = lambda i: base64.urlsafe_b64encode(i.to_bytes((i.bit_length() + 7) // 8, "big")).rstrip(b"=").decode()
json.dump({"keys": [{"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256", "n": b64(n.n), "e": b64(n.e)}]}, open(work + "/jwks.json", "w"))
json.dump({"issuer": "http://mgr-oidc:8080", "jwks_uri": "http://mgr-oidc:8080/jwks.json"}, open(work + "/openid-configuration", "w"))
pem = key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption())
def mint(name, groups, aud="example-observability-client"):
    tok = jwt.encode({"iss": "http://mgr-oidc:8080", "aud": aud, "sub": "someone", "groups": groups,
                      "iat": int(time.time()), "exp": int(time.time()) + 3600}, pem, algorithm="RS256", headers={"kid": "k1"})
    open(f"{work}/token-{name}", "w").write(tok)
mint("all", ["cluster-a:k8s:viewer", "cluster-b:k8s:viewer", "cluster-c:k8s:viewer"])
mint("only-b", ["cluster-b:k8s:viewer"])
mint("stranger", ["cluster-z:k8s:viewer"])
mint("wrong-aud", ["cluster-a:k8s:viewer", "cluster-b:k8s:viewer", "cluster-c:k8s:viewer"], aud="some-other-client")
PY
mkdir -p "$work/www/.well-known"
cp "$work/openid-configuration" "$work/www/.well-known/openid-configuration"
cp "$work/jwks.json" "$work/www/jwks.json"

docker network create "$net" >/dev/null
run() { local name="$1"; shift; docker run -d --label mgr-proof --name "$name" --network "$net" "$@" >/dev/null; }
run mgr-oidc -v "$work/www:/www:ro" python:3-alpine python3 -m http.server 8080 -d /www
# latencyOffset 0: an instant query otherwise ignores samples younger than 30s
run mgr-vm "victoriametrics/victoria-metrics:$vm_tag" -retentionPeriod=100y -search.latencyOffset=0s
run mgr-vl "victoriametrics/victoria-logs:$vl_tag"
qcurl() { docker run --rm --label mgr-proof --network "$net" -v "$work:/w:ro" curlimages/curl:latest -sS "$@"; }
for h in mgr-vm:8428/health mgr-vl:9428/health mgr-oidc:8080/jwks.json; do
  for _ in $(seq 1 40); do qcurl -f "http://$h" >/dev/null 2>&1 && break; sleep 1; done
done

# The stores hold four clusters; the principal's grants name three.
{
  for c in cluster-a cluster-b cluster-c cluster-z; do
    echo "probe_up{k8s_cluster_name=\"$c\",k8s_namespace_name=\"ns\"} 1"
  done
} > "$work/metrics.txt"
{
  for c in cluster-a cluster-b cluster-c cluster-z; do
    printf '{"_msg":"hello from %s","k8s.cluster.name":"%s","kubernetes.pod_namespace":"ns"}\n' "$c" "$c"
  done
} > "$work/logs.jsonl"
chmod -R a+rX "$work"   # mktemp -d is 0700; the containers run as other users
qcurl -f --data-binary @/w/metrics.txt "http://mgr-vm:8428/api/v1/import/prometheus" >/dev/null
# The content type matters: curl's form-urlencoded default makes VictoriaLogs
# ingest nothing while still answering 200.
qcurl -f -H "Content-Type: application/stream+json" --data-binary @/w/logs.jsonl "http://mgr-vl:9428/insert/jsonline?_stream_fields=k8s.cluster.name,kubernetes.pod_namespace&_msg_field=_msg" >/dev/null
qcurl -f "http://mgr-vm:8428/internal/force_flush" >/dev/null 2>&1 || true
sleep 3

fail=0
start_vmauth() {
  docker rm -f mgr-vmauth >/dev/null 2>&1 || true
  run mgr-vmauth -v "$work/$1.yml:/vmauth.yml:ro" "victoriametrics/vmauth:$vmauth_tag" \
    -auth.config=/vmauth.yml -httpListenAddr=:8427
  for _ in $(seq 1 30); do
    qcurl -o /dev/null "http://mgr-vmauth:8427/" 2>/dev/null && break; sleep 1
  done
  sleep 2
}
# read <token> -> "<metrics clusters>|<logs clusters>|<metrics status>" (or a bare status)
read_as() {
  local tok="$1" code m l
  code="$(qcurl -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $(cat "$work/token-$tok")" \
    -G "http://mgr-vmauth:8427/prometheus/api/v1/query" --data-urlencode 'query=probe_up')"
  if [ "$code" != 200 ]; then echo "HTTP $code"; return; fi
  m="$(qcurl -H "Authorization: Bearer $(cat "$work/token-$tok")" -G "http://mgr-vmauth:8427/prometheus/api/v1/query" --data-urlencode 'query=probe_up' \
    | python3 -c 'import json,sys; print(",".join(sorted(r["metric"]["k8s_cluster_name"] for r in json.load(sys.stdin)["data"]["result"])))')"
  l="$(qcurl -H "Authorization: Bearer $(cat "$work/token-$tok")" -G "http://mgr-vmauth:8427/select/logsql/query" --data-urlencode 'query=*' --data-urlencode 'limit=100' \
    | python3 -c '
import json,sys
print(",".join(sorted({json.loads(x)["k8s.cluster.name"] for x in sys.stdin if x.strip()})))')"
  echo "metrics=[$m] logs=[$l]"
}
expect() {
  local label="$1" tok="$2" want="$3" got
  got="$(read_as "$tok")"
  if [ "$got" = "$want" ]; then echo "PASS  $label -> $got"; else echo "FAIL  $label -> $got (wanted $want)" >&2; fail=1; fi
}

echo; echo "=== chart: one principal, three groups, three grants ==="
start_vmauth vmauth-chart
all="metrics=[cluster-a,cluster-b,cluster-c] logs=[cluster-a,cluster-b,cluster-c]"
expect "token with all three groups, aud=grafana-client   " all "$all"
expect "token with ONLY cluster-b's group (correct under the grant-matrix assumption)" only-b "$all"
expect "token whose group no principal names               " stranger "HTTP 401"
expect "token with the right groups but another audience   " wrong-aud "HTTP 401"

echo; echo "=== control: the old shape, one single-group principal per cluster ==="
start_vmauth vmauth-control
expect "token with all three groups (first match only)     " all "metrics=[cluster-a] logs=[cluster-a]"

echo
if [ "$fail" = 0 ]; then
  echo "hack/multi-group-reader-proof.sh: PASS — one multi-group principal reads every cluster on metrics and logs, an unmatched group or audience gets 401, and the per-cluster shape it replaces read one cluster only."
else
  echo "hack/multi-group-reader-proof.sh: FAIL" >&2; exit 1
fi

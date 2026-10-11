#!/usr/bin/env bash
# hack/vm-access-reader-proof.sh — REAL proof, against the pinned vmauth,
# VictoriaMetrics and VictoriaLogs binaries, of `tenancy.vmAccessReader`:
#
#   * a token with a COMPLETE `vm_access` claim (non-empty metrics_extra_filters
#     and logs_extra_stream_filters) is read by the vm-access user and reads
#     exactly what its claim says, whatever groups it also carries;
#   * a token without the claim keeps the group-matched principals (the
#     migration: both readers coexist; the user with more match_claims wins);
#   * a token with no usable claim and no known group gets 401: deny by default;
#   * a half-filled claim (only one list) never reaches the vm-access user.
#
# What runs (all real software; nothing is mocked but the identity issuer,
# a python file server publishing a discovery document and a JWKS for a key
# generated here): vmsingle + victoria-logs holding series and log lines for
# cluster-a, cluster-b, cluster-c and cluster-z, and vmauth configured FROM
# THE RENDERED CHART (tests/cases/observability-stack/tenancy-vm-access, through
# `helm template`), the VMUsers translated into vmauth's own file format
# field for field (principals first, as the chart renders them).
#
# Needs Docker, python3 (PyYAML, PyJWT, cryptography), helm and curl. Not part
# of `check` or CI, like the other hack/*-proof.sh: a run-by-hand proof.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d -t vma-proof.XXXXXX)"
net=vma-proof-net
cleanup() {
  docker ps -aq --filter "label=vma-proof" | xargs -r docker rm -f >/dev/null 2>&1 || true
  docker network rm "$net" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT
for tool in docker curl python3 helm; do
  command -v "$tool" >/dev/null 2>&1 || { echo "hack/vm-access-reader-proof.sh needs $tool on PATH" >&2; exit 1; }
done
python3 -c 'import yaml, jwt, cryptography' 2>/dev/null || { echo "needs python3 with PyYAML, PyJWT and cryptography" >&2; exit 1; }

vm_tgz=("$root"/charts/observability-stack/charts/victoria-metrics-k8s-stack-*.tgz)
vm_tag="$(tar -xzOf "${vm_tgz[0]}" victoria-metrics-k8s-stack/Chart.yaml | awk '/^appVersion:/ { print $2 }')"
vl_tgz=("$root"/charts/observability-stack/charts/victoria-logs-single-*.tgz)
vl_tag="$(tar -xzOf "${vl_tgz[0]}" victoria-logs-single/Chart.yaml | awk '/^appVersion:/ { print $2 }')"
vmauth_tag="$(awk '/^vmauth:/{m=1} m && /^ *tag: /{print $2; exit}' "$root/charts/observability-stack/values.yaml")"
echo "vmauth $vmauth_tag, victoria-metrics $vm_tag, victoria-logs $vl_tag (the chart's pins)"

case_values="$root/tests/cases/observability-stack/tenancy-vm-access/values.yaml"
helm template x "$root/charts/observability-stack" -f "$case_values" > "$work/chart.yaml"

python3 - "$work" <<'PY'
import sys, yaml
work = sys.argv[1]
users = []
for d in yaml.safe_load_all(open(f"{work}/chart.yaml")):
    if not d or d.get("kind") != "VMUser" or d["metadata"]["labels"].get("observability.role") != "reader":
        continue
    s = d["spec"]
    rows = []
    for ref in s["targetRefs"]:
        url = ref["static"]["url"]
        url = "http://vma-vm:8428" if ":8428" in url else "http://vma-vl:9428"
        args = "&".join(f"{a['name']}={v}" for a in ref["query_args"] for v in a["values"])
        rows.append({"src_paths": ref["paths"], "url_prefix": f"{url}?{args}"})
    jwt_cfg = {"oidc": {"issuer": "http://vma-oidc:8080"}, "match_claims": dict(s["jwt"]["matchClaims"])}
    # the container issuer replaces the example one in the iss pin as well
    if "iss" in jwt_cfg["match_claims"]:
        jwt_cfg["match_claims"]["iss"] = "^(http://vma-oidc:8080)$"
    dv = s["jwt"].get("defaultVMAccessClaim")
    if dv:
        jwt_cfg["default_vm_access_claim"] = {
            "metrics_extra_filters": dv["metricsExtraFilters"],
            "logs_extra_stream_filters": dv["logsExtraStreamFilters"],
        }
    users.append({"name": s["name"], "jwt": jwt_cfg, "url_map": rows})
yaml.safe_dump({"users": users}, open(f"{work}/vmauth.yml", "w"), sort_keys=False)
print(f"--- vmauth.yml: {len(users)} reader user(s), in render order")
for u in users:
    print(f"    {u['name']}: {len(u['jwt']['match_claims'])} match claims, default claim: {'default_vm_access_claim' in u['jwt']}")
PY

python3 - "$work" <<'PY'
import sys, json, time, base64, jwt
from cryptography.hazmat.primitives.asymmetric import rsa
from cryptography.hazmat.primitives import serialization
work = sys.argv[1]
key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
n = key.public_key().public_numbers()
b64 = lambda i: base64.urlsafe_b64encode(i.to_bytes((i.bit_length() + 7) // 8, "big")).rstrip(b"=").decode()
json.dump({"keys": [{"kty": "RSA", "kid": "k1", "use": "sig", "alg": "RS256", "n": b64(n.n), "e": b64(n.e)}]}, open(work + "/jwks.json", "w"))
json.dump({"issuer": "http://vma-oidc:8080", "jwks_uri": "http://vma-oidc:8080/jwks.json"}, open(work + "/openid-configuration", "w"))
pem = key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption())
AUD = "example-observability-client"
ALL = ["cluster-a:k8s:viewer", "cluster-b:k8s:viewer", "cluster-c:k8s:viewer"]
def m(*cs): return ["{k8s_cluster_name=\"%s\"}" % c for c in cs]
def l(*cs): return ["_stream:{" + " or ".join('"k8s.cluster.name"="%s"' % c for c in cs) + "}"]
def mint(name, groups=None, vm=None, aud=AUD):
    body = {"iss": "http://vma-oidc:8080", "aud": aud, "sub": "someone",
            "iat": int(time.time()), "exp": int(time.time()) + 3600}
    if groups is not None: body["groups"] = groups
    if vm is not None: body["vm_access"] = vm
    open(f"{work}/token-{name}", "w").write(jwt.encode(body, pem, algorithm="RS256", headers={"kid": "k1"}))
mint("groups-only", groups=ALL)
mint("claim-a", groups=["nobody"], vm={"metrics_extra_filters": m("cluster-a"), "logs_extra_stream_filters": l("cluster-a")})
mint("claim-a-no-groups", vm={"metrics_extra_filters": m("cluster-a"), "logs_extra_stream_filters": l("cluster-a")})
mint("claim-a-and-all-groups", groups=ALL, vm={"metrics_extra_filters": m("cluster-a"), "logs_extra_stream_filters": l("cluster-a")})
mint("claim-ab", groups=["nobody"], vm={"metrics_extra_filters": m("cluster-a", "cluster-b"), "logs_extra_stream_filters": l("cluster-a", "cluster-b")})
mint("nothing", groups=["nobody"])
mint("metrics-only-claim", groups=["nobody"], vm={"metrics_extra_filters": m("cluster-a")})
mint("logs-only-claim", groups=["nobody"], vm={"logs_extra_stream_filters": l("cluster-a")})
mint("empty-lists", groups=["nobody"], vm={"metrics_extra_filters": [], "logs_extra_stream_filters": []})
mint("claim-a-wrong-aud", groups=["nobody"], vm={"metrics_extra_filters": m("cluster-a"), "logs_extra_stream_filters": l("cluster-a")}, aud="some-other-client")
PY
mkdir -p "$work/www/.well-known"
cp "$work/openid-configuration" "$work/www/.well-known/openid-configuration"
cp "$work/jwks.json" "$work/www/jwks.json"

docker network create "$net" >/dev/null
run() { local name="$1"; shift; docker run -d --label vma-proof --name "$name" --network "$net" "$@" >/dev/null; }
run vma-oidc -v "$work/www:/www:ro" python:3-alpine python3 -m http.server 8080 -d /www
# latencyOffset 0: an instant query otherwise ignores samples younger than 30s
run vma-vm "victoriametrics/victoria-metrics:$vm_tag" -retentionPeriod=100y -search.latencyOffset=0s
run vma-vl "victoriametrics/victoria-logs:$vl_tag"
qcurl() { docker run --rm --label vma-proof --network "$net" -v "$work:/w:ro" curlimages/curl:latest -sS "$@"; }
for h in vma-vm:8428/health vma-vl:9428/health vma-oidc:8080/jwks.json; do
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
qcurl -f --data-binary @/w/metrics.txt "http://vma-vm:8428/api/v1/import/prometheus" >/dev/null
# The content type matters: curl's form-urlencoded default makes VictoriaLogs
# ingest nothing while still answering 200.
qcurl -f -H "Content-Type: application/stream+json" --data-binary @/w/logs.jsonl "http://vma-vl:9428/insert/jsonline?_stream_fields=k8s.cluster.name,kubernetes.pod_namespace&_msg_field=_msg" >/dev/null
qcurl -f "http://vma-vm:8428/internal/force_flush" >/dev/null 2>&1 || true
sleep 3

fail=0
start_vmauth() {
  docker rm -f vma-vmauth >/dev/null 2>&1 || true
  run vma-vmauth -v "$work/$1.yml:/vmauth.yml:ro" "victoriametrics/vmauth:$vmauth_tag" \
    -auth.config=/vmauth.yml -httpListenAddr=:8427
  for _ in $(seq 1 30); do
    qcurl -o /dev/null "http://vma-vmauth:8427/" 2>/dev/null && break; sleep 1
  done
  sleep 2
}
# read <token> -> "<metrics clusters>|<logs clusters>|<metrics status>" (or a bare status)
read_as() {
  local tok="$1" code m l
  code="$(qcurl -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $(cat "$work/token-$tok")" \
    -G "http://vma-vmauth:8427/prometheus/api/v1/query" --data-urlencode 'query=probe_up')"
  if [ "$code" != 200 ]; then echo "HTTP $code"; return; fi
  m="$(qcurl -H "Authorization: Bearer $(cat "$work/token-$tok")" -G "http://vma-vmauth:8427/prometheus/api/v1/query" --data-urlencode 'query=probe_up' \
    | python3 -c 'import json,sys; print(",".join(sorted(r["metric"]["k8s_cluster_name"] for r in json.load(sys.stdin)["data"]["result"])))')"
  l="$(qcurl -H "Authorization: Bearer $(cat "$work/token-$tok")" -G "http://vma-vmauth:8427/select/logsql/query" --data-urlencode 'query=*' --data-urlencode 'limit=100' \
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

echo; echo "=== chart: group principal + vm_access reader, vmauth picks the user with more match_claims ==="
start_vmauth vmauth
abc="metrics=[cluster-a,cluster-b,cluster-c] logs=[cluster-a,cluster-b,cluster-c]"
expect "groups only (no claim): the group principal, union of its grants   " groups-only "$abc"
expect "complete claim (cluster-a), no group the principal knows           " claim-a "metrics=[cluster-a] logs=[cluster-a]"
expect "complete claim (cluster-a), no groups claim at all                 " claim-a-no-groups "metrics=[cluster-a] logs=[cluster-a]"
expect "complete claim (cluster-a) AND all three groups: the CLAIM wins    " claim-a-and-all-groups "metrics=[cluster-a] logs=[cluster-a]"
expect "complete claim (cluster-a, cluster-b)                              " claim-ab "metrics=[cluster-a,cluster-b] logs=[cluster-a,cluster-b]"
expect "no claim, no known group: deny by default                          " nothing "HTTP 401"
expect "only a metrics list (half-filled): never the vm-access user, 401   " metrics-only-claim "HTTP 401"
expect "only a logs list (half-filled): never the vm-access user, 401      " logs-only-claim "HTTP 401"
expect "both lists empty: 401                                              " empty-lists "HTTP 401"
expect "complete claim, another audience: 401                              " claim-a-wrong-aud "HTTP 401"

echo
if [ "$fail" = 0 ]; then
  echo "hack/vm-access-reader-proof.sh: PASS — a complete vm_access claim is applied by the vm-access user and wins over groups, a token without it keeps the group readers, and an absent or half-filled claim with no known group gets 401."
else
  echo "hack/vm-access-reader-proof.sh: FAIL" >&2; exit 1
fi

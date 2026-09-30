#!/usr/bin/env bash
# hack/vmalert-api-proof.sh — REAL proof, against the pinned vmauth,
# VictoriaMetrics and vmalert binaries, that a `tenancy.principals[]` entry
# with `vmalertAPI: true` reads the metrics vmalert's alerts and rules at
# `/prometheus/vmalert/api/v1/{alerts,rules}` (vmauth drops the first path
# part, so vmalert receives `/vmalert/api/v1/...`), and that a principal
# without the key, or the same principal on any other vmalert path, is
# refused.
#
# What runs (all real software; nothing is mocked but the identity issuer,
# a python file server publishing a discovery document and a JWKS for a key
# generated here):
#   vmsingle   holding one series
#   vmalert    evaluating an always-firing rule against it
#   vmauth     configured FROM THE RENDERED CHART
#              (tests/cases/observability-stack/tenancy-vmalert-api, through
#              `helm template`), each VMUser translated into vmauth's own file
#              format field for field, `drop_src_path_prefix_parts` included.
#
# Cases:
#   principal with vmalertAPI, alerts         -> 200 carrying the firing alert
#   principal with vmalertAPI, rules          -> 200 carrying the rule group
#   principal with vmalertAPI, a query        -> 200 (its metrics route is intact)
#   principal with vmalertAPI, /-/reload      -> refused (not a route)
#   principal without vmalertAPI, alerts      -> refused
#   principal without vmalertAPI, rules       -> refused
#
# Needs Docker, python3 (PyYAML, PyJWT, cryptography), helm and curl. Not part
# of `check` or CI, like the other hack/*-proof.sh: a run-by-hand proof.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d -t vaa-proof.XXXXXX)"
net=vaa-proof-net
cleanup() {
  docker ps -aq --filter "label=vaa-proof" | xargs -r docker rm -f >/dev/null 2>&1 || true
  docker network rm "$net" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

for tool in docker curl python3 helm; do
  command -v "$tool" >/dev/null 2>&1 || { echo "hack/vmalert-api-proof.sh needs $tool on PATH" >&2; exit 1; }
done
python3 -c 'import yaml, jwt, cryptography' 2>/dev/null || { echo "needs python3 with PyYAML, PyJWT and cryptography" >&2; exit 1; }

vm_tgz=("$root"/charts/observability-stack/charts/victoria-metrics-k8s-stack-*.tgz)
vm_tag="$(tar -xzOf "${vm_tgz[0]}" victoria-metrics-k8s-stack/Chart.yaml | awk '/^appVersion:/ { print $2 }')"
vmauth_tag="$(awk '/^vmauth:/{m=1} m && /^ *tag: /{print $2; exit}' "$root/charts/observability-stack/values.yaml")"
echo "vmauth $vmauth_tag, victoria-metrics and vmalert $vm_tag (the chart's pins)"

helm template x "$root/charts/observability-stack" \
  -f "$root/tests/cases/observability-stack/tenancy-vmalert-api/values.yaml" > "$work/chart.yaml"

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
        url = "http://vaa-vm:8428" if ":8428" in url else "http://vaa-vmalert:8080"
        row = {"src_paths": ref["paths"], "url_prefix": url}
        if ref.get("query_args"):
            args = "&".join(f"{a['name']}={v}" for a in ref["query_args"] for v in a["values"])
            row["url_prefix"] = f"{url}?{args}"
        if "drop_src_path_prefix_parts" in ref:
            row["drop_src_path_prefix_parts"] = ref["drop_src_path_prefix_parts"]
        rows.append(row)
    users.append({
        "name": s["name"],
        "jwt": {
            "oidc": {"issuer": "http://vaa-oidc:8080"},
            "match_claims": s["jwt"]["matchClaims"],
            "default_vm_access_claim": {
                "metrics_extra_filters": s["jwt"]["defaultVMAccessClaim"]["metricsExtraFilters"],
                "logs_extra_stream_filters": s["jwt"]["defaultVMAccessClaim"]["logsExtraStreamFilters"],
            },
        },
        "url_map": rows,
    })
yaml.safe_dump({"users": users}, open(f"{work}/vmauth.yml", "w"), sort_keys=False)
for u in users:
    print(f"    {u['name']}: " + "; ".join(f"{r['src_paths']} -> {r['url_prefix']} drop={r.get('drop_src_path_prefix_parts', 0)}" for r in u["url_map"]))
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
json.dump({"issuer": "http://vaa-oidc:8080", "jwks_uri": "http://vaa-oidc:8080/jwks.json"}, open(work + "/openid-configuration", "w"))
pem = key.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption())
def mint(name, groups, aud):
    tok = jwt.encode({"iss": "http://vaa-oidc:8080", "aud": aud, "sub": "someone", "groups": groups,
                      "iat": int(time.time()), "exp": int(time.time()) + 3600}, pem, algorithm="RS256", headers={"kid": "k1"})
    open(f"{work}/token-{name}", "w").write(tok)
mint("mcp", ["example:observability-mcp:reader"], "example-mcp-reader")
mint("viewer", ["example:k8s:viewer"], "example-observability-client")
PY
mkdir -p "$work/www/.well-known"
cp "$work/openid-configuration" "$work/www/.well-known/openid-configuration"
cp "$work/jwks.json" "$work/www/jwks.json"
cat > "$work/rules.yml" <<'EOF'
groups:
  - name: proof
    interval: 2s
    rules:
      - alert: ProofAlwaysFiring
        expr: vector(1)
        labels: {severity: warning}
EOF

docker network create "$net" >/dev/null
run() { local name="$1"; shift; docker run -d --label vaa-proof --name "$name" --network "$net" "$@" >/dev/null; }
chmod -R a+rX "$work"   # mktemp -d is 0700; the containers run as other users
run vaa-oidc -v "$work/www:/www:ro" python:3-alpine python3 -m http.server 8080 -d /www
run vaa-vm "victoriametrics/victoria-metrics:$vm_tag" -retentionPeriod=100y -search.latencyOffset=0s
qcurl() { docker run --rm --label vaa-proof --network "$net" -v "$work:/w:ro" curlimages/curl:latest -sS "$@"; }
for h in vaa-vm:8428/health vaa-oidc:8080/jwks.json; do
  for _ in $(seq 1 40); do qcurl -f "http://$h" >/dev/null 2>&1 && break; sleep 1; done
done
echo 'probe_up{k8s_cluster_name="example-cluster",k8s_namespace_name="example-app"} 1' > "$work/metrics.txt"
chmod a+r "$work/metrics.txt"
qcurl -f --data-binary @/w/metrics.txt "http://vaa-vm:8428/api/v1/import/prometheus" >/dev/null
run vaa-vmalert -v "$work/rules.yml:/rules.yml:ro" "victoriametrics/vmalert:$vm_tag" \
  -rule=/rules.yml -datasource.url=http://vaa-vm:8428 -notifier.blackhole -httpListenAddr=:8080
for _ in $(seq 1 40); do qcurl -f "http://vaa-vmalert:8080/health" >/dev/null 2>&1 && break; sleep 1; done
run vaa-vmauth -v "$work/vmauth.yml:/vmauth.yml:ro" "victoriametrics/vmauth:$vmauth_tag" \
  -auth.config=/vmauth.yml -httpListenAddr=:8427
for _ in $(seq 1 30); do qcurl -o /dev/null "http://vaa-vmauth:8427/" 2>/dev/null && break; sleep 1; done
sleep 6   # let vmalert evaluate the rule and the alert go active

fail=0
# get <token> <path> [curl args] -> "<body>|<status>"
get() {
  local tok="$1" path="$2"; shift 2
  qcurl -w '|%{http_code}' -H "Authorization: Bearer $(cat "$work/token-$tok")" -G "http://vaa-vmauth:8427$path" "$@"
}
expect() { # label token path body-substring|REFUSED [curl args]
  local label="$1" tok="$2" path="$3" want="$4" out code body
  shift 4
  out="$(get "$tok" "$path" "$@")"; code="${out##*|}"; body="${out%|*}"
  if [ "$want" = REFUSED ]; then
    case "$code" in 400|401|403|404) echo "PASS  $label -> $code" ;; *) echo "FAIL  $label -> $code (wanted a refusal)" >&2; fail=1 ;; esac
  elif [ "$code" = 200 ] && grep -q -- "$want" <<<"$body"; then echo "PASS  $label -> 200 carrying $want"
  else echo "FAIL  $label -> $code: ${body:0:200}" >&2; fail=1; fi
}
expect "vmalertAPI principal, alerts            " mcp    /prometheus/vmalert/api/v1/alerts ProofAlwaysFiring
expect "vmalertAPI principal, rules             " mcp    /prometheus/vmalert/api/v1/rules  proof
expect "vmalertAPI principal, a query           " mcp    /prometheus/api/v1/query probe_up --data-urlencode query=probe_up
expect "vmalertAPI principal, /-/reload         " mcp    /prometheus/vmalert/-/reload REFUSED
expect "principal without vmalertAPI, alerts    " viewer /prometheus/vmalert/api/v1/alerts REFUSED
expect "principal without vmalertAPI, rules     " viewer /prometheus/vmalert/api/v1/rules REFUSED

echo
if [ "$fail" = 0 ]; then
  echo "hack/vmalert-api-proof.sh: PASS — vmalertAPI reads vmalert's alerts and rules through the dropped prefix, and nothing else of vmalert's; a principal without it is refused."
else
  echo "hack/vmalert-api-proof.sh: FAIL" >&2; exit 1
fi

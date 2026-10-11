#!/usr/bin/env bash
# hack/tenancy-authz-proof.sh — REAL proof, on kind, of `tenancy.authz`: the
# Envoy external authorisation service that turns a person's groups into the
# `vm_access` claim vmauth applies.
#
# What runs, all real software but the identity provider:
#
#   * kind, with Envoy Gateway (the release the SecurityPolicy CRD is
#     vendored from) serving a Gateway and an HTTPRoute in front of vmauth;
#   * VictoriaMetrics and VictoriaLogs holding series and log lines for
#     several namespaces of two clusters;
#   * vmauth, configured FROM THE RENDERED CHART: the VMUsers translated
#     into vmauth's own file format field for field, the vm-access reader
#     included — so what vmauth trusts is what the chart says it trusts;
#   * External Secrets Operator, generating tenancy-authz's signing key from
#     the chart's own SSHKey generator and ExternalSecret;
#   * tenancy-authz, built here, deployed from the chart's own rendered
#     objects (ServiceAccount, ConfigMap, Service, Deployment) with two
#     replicas that mount that one Secret, and the chart's own
#     SecurityPolicy attaching it to the route;
#   * a fake identity provider: a python file server publishing a discovery
#     document and a JWKS for an ES256 key generated here. It is the only
#     thing mocked.
#
# It proves:
#
#   1. a person with two namespace groups reads ONLY those two namespaces'
#      series, and the logs of both (the OR works) — and none of the others;
#   2. a person whose groups select no principal gets 403;
#   3. a forged token (signed by another key) gets 401;
#   4. a token of another audience passes through UNCHANGED to vmauth's own
#      readers, which enforce as they always did;
#   5. the person's own token sent straight to vmauth never matches the
#      vm-access reader (it gets a group principal's single reach, or 401),
#      and a `vm_access` claim a client puts into a token of its own is
#      ignored;
#   6. a client-supplied `extra_filters` / `extra_stream_filters` argument
#      cannot widen the result, and neither can the array spelling
#      `extra_filters[]` that VictoriaMetrics ORs in beside `extra_filters`
#      (every metrics route pins both, so vmauth drops the client's copy);
#   7. both replicas sign with the one key the operator generated, and
#      deleting the Secret rotates it with no restart and no failed request:
#      the new key is published beside the old one at once;
#   8. with the service down a person gets no data (the policy fails closed),
#      and a workload calling vmauth directly is unaffected.
#
# Needs network (images and the Envoy Gateway and External Secrets charts),
# Docker, kind, kubectl, helm, go, python3 (PyYAML, PyJWT,
# cryptography) and curl. Not part of `check` or CI, like the other
# hack/*-proof.sh: a run-by-hand proof. It is heavy (a cluster, six images):
# run it through `heavy`, and KEEP=1 leaves the cluster up to look at.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d -t tenancy-authz-proof.XXXXXX)"
cluster=tenancy-authz-proof
ns=obs
eg_version=v1.9.2
cleanup() {
  if [ -z "${KEEP:-}" ]; then
    kind delete cluster --name "$cluster" >/dev/null 2>&1 || true
  else
    echo "KEEP set: cluster $cluster left up (kubectl --context kind-$cluster)"
  fi
  rm -rf "$work"
}
trap cleanup EXIT
for tool in docker kind kubectl helm go python3 curl; do
  command -v "$tool" >/dev/null 2>&1 || { echo "hack/tenancy-authz-proof.sh needs $tool on PATH" >&2; exit 1; }
done
python3 -c 'import yaml, jwt, cryptography' 2>/dev/null || { echo "needs python3 with PyYAML, PyJWT and cryptography" >&2; exit 1; }

k() { kubectl --context "kind-$cluster" "$@"; }
kn() { k -n "$ns" "$@"; }

vm_tgz=("$root"/charts/observability-stack/charts/victoria-metrics-k8s-stack-*.tgz)
vm_tag="$(tar -xzOf "${vm_tgz[0]}" victoria-metrics-k8s-stack/Chart.yaml | awk '/^appVersion:/ { print $2 }')"
vl_tgz=("$root"/charts/observability-stack/charts/victoria-logs-single-*.tgz)
vl_tag="$(tar -xzOf "${vl_tgz[0]}" victoria-logs-single/Chart.yaml | awk '/^appVersion:/ { print $2 }')"
vmauth_tag="$(awk '/^vmauth:/{m=1} m && /^ *tag: /{print $2; exit}' "$root/charts/observability-stack/values.yaml")"
echo "vmauth $vmauth_tag, victoria-metrics $vm_tag, victoria-logs $vl_tag (the chart's pins), Envoy Gateway $eg_version"

# --- the service image --------------------------------------------------
mkdir -p "$work/image"
( cd "$root" && CGO_ENABLED=0 GOOS=linux GOARCH="$(go env GOARCH)" GOWORK=off \
    go build -trimpath -ldflags "-s -w -X main.version=proof" -o "$work/image/tenancy-authz" ./cmd/tenancy-authz )
cat > "$work/image/Dockerfile" <<'EOF'
FROM gcr.io/distroless/static:nonroot
COPY tenancy-authz /tenancy-authz
ENTRYPOINT ["/tenancy-authz"]
EOF
docker build -q -t tenancy-authz:proof "$work/image" >/dev/null

# --- the chart, rendered ------------------------------------------------
# Principals: two namespace groups on one cluster, an operations group on
# another cluster, and a workload on its own audience (metrics only).
cat > "$work/values.yaml" <<EOF
tenancy:
  issuerUrl: http://oidc.$ns.svc:8080
  audience: grafana
  claimName: groups
  vmAccessReader:
    enabled: true
  authz:
    enabled: true
    replicas: 2
    image: {repository: tenancy-authz, tag: proof, pullPolicy: Never}
    securityPolicy:
      targetRefs:
        - {kind: HTTPRoute, name: observability-read}
    networkPolicy:
      gatewayFrom:
        - podSelector: {}
  principals:
    - group: team-a
      grants:
        - {cluster: cluster-a, namespaces: [ns-a]}
    - group: team-b
      grants:
        - {cluster: cluster-a, namespaces: [ns-b]}
    - group: ops
      grants:
        - {cluster: cluster-b, allNamespaces: true}
    - group: cluster-a:ns-a:reader
      audience: reader
      routes: [metrics]
      grants:
        - {cluster: cluster-a, namespaces: [ns-a]}
networkPolicy:
  enabled: false
vmalert: {enabled: false}
alertmanager: {enabled: false}
victoria-metrics-k8s-stack: {enabled: false}
victoria-logs-single: {enabled: false}
victoria-traces-single: {enabled: false}
EOF
helm template x "$root/charts/observability-stack" --namespace "$ns" -f "$work/values.yaml" > "$work/chart.yaml"

python3 - "$work" <<'PY'
import sys, yaml
work = sys.argv[1]
docs = [d for d in yaml.safe_load_all(open(f"{work}/chart.yaml")) if d]
authz_kinds = {"ServiceAccount", "ConfigMap", "Service", "Deployment", "SecurityPolicy", "PodDisruptionBudget", "SSHKey", "ExternalSecret"}
keep = [d for d in docs if "tenancy-authz" in d["metadata"]["name"] and d["kind"] in authz_kinds]
yaml.safe_dump_all(keep, open(f"{work}/authz.yaml", "w"), sort_keys=False)
print(f"--- authz.yaml: {', '.join(sorted(d['kind'] for d in keep))}")

users = []
for d in docs:
    if d.get("kind") != "VMUser" or d["metadata"]["labels"].get("observability.role") != "reader":
        continue
    s = d["spec"]
    rows = []
    for ref in s["targetRefs"]:
        url = ref["static"]["url"]
        url = f"http://vm.obs.svc:8428" if ":8428" in url else "http://vl.obs.svc:9428"
        args = "&".join(f"{a['name']}={v}" for a in ref["query_args"] for v in a["values"])
        rows.append({"src_paths": ref["paths"], "url_prefix": f"{url}?{args}"})
    j = {"oidc": {"issuer": s["jwt"]["oidc"]["issuer"]}, "match_claims": dict(s["jwt"]["matchClaims"])}
    dv = s["jwt"].get("defaultVMAccessClaim")
    if dv:
        j["default_vm_access_claim"] = {"metrics_extra_filters": dv["metricsExtraFilters"],
                                        "logs_extra_stream_filters": dv["logsExtraStreamFilters"]}
    users.append({"name": s["name"], "jwt": j, "url_map": rows})
yaml.safe_dump({"users": users}, open(f"{work}/vmauth.yml", "w"), sort_keys=False)
print(f"--- vmauth.yml: {len(users)} reader user(s)")
for u in users:
    print(f"    {u['name']}: oidc {u['jwt']['oidc']['issuer']}, {len(u['jwt']['match_claims'])} match claims, default claim: {'default_vm_access_claim' in u['jwt']}")
PY

# --- the fake identity provider: keys and tokens ------------------------
python3 - "$work" "$ns" <<'PY'
import sys, json, time, base64, jwt
from cryptography.hazmat.primitives.asymmetric import ec
from cryptography.hazmat.primitives import serialization
work, ns = sys.argv[1], sys.argv[2]
def keypair():
    k = ec.generate_private_key(ec.SECP256R1())
    n = k.public_key().public_numbers()
    b64 = lambda i: base64.urlsafe_b64encode(i.to_bytes(32, "big")).rstrip(b"=").decode()
    pem = k.private_bytes(serialization.Encoding.PEM, serialization.PrivateFormat.PKCS8, serialization.NoEncryption())
    return pem, {"kty": "EC", "crv": "P-256", "kid": "idp-1", "use": "sig", "alg": "ES256", "x": b64(n.x), "y": b64(n.y)}
pem, jwk = keypair()
other_pem, _ = keypair()
issuer = f"http://oidc.{ns}.svc:8080"
json.dump({"issuer": issuer, "jwks_uri": issuer + "/jwks.json"}, open(work + "/openid-configuration", "w"))
json.dump({"keys": [jwk]}, open(work + "/jwks.json", "w"))
def mint(name, groups=None, aud="grafana", key=pem, extra=None, sub="alice"):
    body = {"iss": issuer, "aud": aud, "sub": sub, "iat": int(time.time()), "exp": int(time.time()) + 3600}
    if groups is not None: body["groups"] = groups
    if extra: body.update(extra)
    open(f"{work}/token-{name}", "w").write(jwt.encode(body, key, algorithm="ES256", headers={"kid": "idp-1"}))
mint("ab", ["team-a", "team-b", "unrelated"])
mint("a", ["team-a"])
mint("ops-a", ["ops", "team-a"])
mint("nobody", ["nobody"])
mint("forged", ["team-a", "team-b"], key=other_pem)
mint("workload", ["cluster-a:ns-a:reader"], aud="reader", sub="svc")
mint("a-claims-b", ["team-a"], extra={"vm_access": {"metrics_extra_filters": ['{k8s_cluster_name="cluster-b"}'], "logs_extra_stream_filters": ['_stream:{"k8s.cluster.name"="cluster-b"}']}})
mint("nobody-claims-b", ["nobody"], extra={"vm_access": {"metrics_extra_filters": ['{k8s_cluster_name="cluster-b"}'], "logs_extra_stream_filters": ['_stream:{"k8s.cluster.name"="cluster-b"}']}})
PY

# --- the cluster --------------------------------------------------------
kind create cluster --name "$cluster" --wait 120s >/dev/null
for image in "victoriametrics/victoria-metrics:$vm_tag" "victoriametrics/victoria-logs:$vl_tag" "victoriametrics/vmauth:$vmauth_tag" python:3-alpine curlimages/curl:latest; do
  docker image inspect "$image" >/dev/null 2>&1 || docker pull -q "$image" >/dev/null
  kind load docker-image --name "$cluster" "$image" >/dev/null
done
kind load docker-image --name "$cluster" tenancy-authz:proof >/dev/null

helm install eg oci://docker.io/envoyproxy/gateway-helm --version "$eg_version" \
  --kube-context "kind-$cluster" -n envoy-gateway-system --create-namespace --wait --timeout 300s >/dev/null
helm install external-secrets oci://ghcr.io/external-secrets/charts/external-secrets --version 2.12.0 \
  --kube-context "kind-$cluster" -n external-secrets --create-namespace --set installCRDs=true --wait --timeout 300s >/dev/null
k create namespace "$ns" >/dev/null

kn create configmap oidc --from-file=jwks.json="$work/jwks.json" --from-file=openid-configuration="$work/openid-configuration" >/dev/null
kn create configmap vmauth-config --from-file=vmauth.yml="$work/vmauth.yml" >/dev/null

cat > "$work/base.yaml" <<EOF
apiVersion: apps/v1
kind: Deployment
metadata: {name: oidc}
spec:
  replicas: 1
  selector: {matchLabels: {app: oidc}}
  template:
    metadata: {labels: {app: oidc}}
    spec:
      containers:
        - name: oidc
          image: python:3-alpine
          imagePullPolicy: Never
          command: [python3, -m, http.server, "8080", -d, /www]
          volumeMounts: [{name: www, mountPath: /www}]
      volumes:
        - name: www
          configMap:
            name: oidc
            items:
              - {key: jwks.json, path: jwks.json}
              - {key: openid-configuration, path: .well-known/openid-configuration}
---
apiVersion: v1
kind: Service
metadata: {name: oidc}
spec: {selector: {app: oidc}, ports: [{port: 8080}]}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: vm}
spec:
  replicas: 1
  selector: {matchLabels: {app: vm}}
  template:
    metadata: {labels: {app: vm}}
    spec:
      containers:
        - name: vm
          image: victoriametrics/victoria-metrics:$vm_tag
          imagePullPolicy: Never
          args: [-retentionPeriod=100y, -search.latencyOffset=0s]
---
apiVersion: v1
kind: Service
metadata: {name: vm}
spec: {selector: {app: vm}, ports: [{port: 8428}]}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: vl}
spec:
  replicas: 1
  selector: {matchLabels: {app: vl}}
  template:
    metadata: {labels: {app: vl}}
    spec:
      containers:
        - name: vl
          image: victoriametrics/victoria-logs:$vl_tag
          imagePullPolicy: Never
---
apiVersion: v1
kind: Service
metadata: {name: vl}
spec: {selector: {app: vl}, ports: [{port: 9428}]}
---
apiVersion: apps/v1
kind: Deployment
metadata: {name: vmauth}
spec:
  replicas: 1
  selector: {matchLabels: {app: vmauth}}
  template:
    metadata: {labels: {app: vmauth}}
    spec:
      containers:
        - name: vmauth
          image: victoriametrics/vmauth:$vmauth_tag
          imagePullPolicy: Never
          args: [-auth.config=/vmauth.yml, -httpListenAddr=:8427]
          volumeMounts: [{name: cfg, mountPath: /vmauth.yml, subPath: vmauth.yml}]
      volumes:
        - name: cfg
          configMap: {name: vmauth-config}
---
apiVersion: v1
kind: Service
metadata: {name: vmauth}
spec: {selector: {app: vmauth}, ports: [{port: 8427}]}
---
apiVersion: v1
kind: Pod
metadata: {name: tools}
spec:
  containers:
    - name: tools
      image: curlimages/curl:latest
      imagePullPolicy: Never
      command: [sleep, "3600"]
---
apiVersion: gateway.networking.k8s.io/v1
kind: GatewayClass
metadata: {name: eg}
spec: {controllerName: gateway.envoyproxy.io/gatewayclass-controller}
---
apiVersion: gateway.networking.k8s.io/v1
kind: Gateway
metadata: {name: proof}
spec:
  gatewayClassName: eg
  listeners:
    - {name: http, port: 80, protocol: HTTP}
---
apiVersion: gateway.networking.k8s.io/v1
kind: HTTPRoute
metadata: {name: observability-read}
spec:
  parentRefs: [{name: proof}]
  rules:
    - backendRefs: [{name: vmauth, port: 8427}]
EOF
# The Gateway API CRDs come with Envoy Gateway; the GatewayClass is cluster scoped.
kn apply -f "$work/base.yaml" >/dev/null
kn wait --for=condition=Available deployment/oidc deployment/vm deployment/vl deployment/vmauth --timeout=180s >/dev/null
kn wait --for=condition=Ready pod/tools --timeout=120s >/dev/null

# --- the service, from the chart's own objects, and its policy ----------
kn apply -f "$work/authz.yaml" >/dev/null
kn rollout status deployment/x-observability-stack-tenancy-authz --timeout=180s >/dev/null
# kind has no load balancer, so the Gateway never gets an address (Programmed stays False); Accepted is what can be waited for.
kn wait --for=condition=Accepted gateway/proof --timeout=180s >/dev/null
eg_svc=""
for _ in $(seq 1 60); do
  eg_svc="$(k -n envoy-gateway-system get svc -l "gateway.envoyproxy.io/owning-gateway-name=proof,gateway.envoyproxy.io/owning-gateway-namespace=$ns" -o jsonpath='{.items[0].metadata.name}' 2>/dev/null || true)"
  [ -n "$eg_svc" ] && break; sleep 2
done
for _ in $(seq 1 30); do k -n envoy-gateway-system get deployment -l "gateway.envoyproxy.io/owning-gateway-name=proof" -o name 2>/dev/null | grep -q . && break; sleep 2; done
k -n envoy-gateway-system wait --for=condition=Available deployment -l "gateway.envoyproxy.io/owning-gateway-name=proof" --timeout=180s >/dev/null
gw="http://$eg_svc.envoy-gateway-system.svc:80"
direct="http://vmauth.$ns.svc:8427"
echo "gateway $gw, vmauth $direct"

# --- the data: three namespaces on cluster-a, one on cluster-b ----------
series=(cluster-a/ns-a cluster-a/ns-b cluster-a/ns-c cluster-b/ns-x)
for s in "${series[@]}"; do
  echo "probe_up{k8s_cluster_name=\"${s%/*}\",k8s_namespace_name=\"${s#*/}\"} 1"
done | kn exec -i tools -- curl -sSf --data-binary @- "http://vm.$ns.svc:8428/api/v1/import/prometheus" >/dev/null
for s in "${series[@]}"; do
  printf '{"_msg":"hello from %s","k8s.cluster.name":"%s","kubernetes.pod_namespace":"%s"}\n' "$s" "${s%/*}" "${s#*/}"
done | kn exec -i tools -- curl -sSf -H "Content-Type: application/stream+json" --data-binary @- \
  "http://vl.$ns.svc:9428/insert/jsonline?_stream_fields=k8s.cluster.name,kubernetes.pod_namespace&_msg_field=_msg" >/dev/null
kn exec tools -- curl -sS "http://vm.$ns.svc:8428/internal/force_flush" >/dev/null 2>&1 || true
sleep 3

# --- reading -------------------------------------------------------------
fail=0
tool_curl() { kn exec tools -- curl -sS "$@"; }
token() { cat "$work/token-$1"; }
# status <base> <token> [extra curl args] -> HTTP status of a metrics query
status() {
  local base="$1" tok="$2"; shift 2
  tool_curl -o /dev/null -w '%{http_code}' -H "Authorization: Bearer $(token "$tok")" -G "$base/prometheus/api/v1/query" --data-urlencode 'query=probe_up' "$@"
}
# metrics <base> <token> [extra curl args] -> "cluster/ns,..." sorted
metrics() {
  local base="$1" tok="$2"; shift 2
  tool_curl -H "Authorization: Bearer $(token "$tok")" -G "$base/prometheus/api/v1/query" --data-urlencode 'query=probe_up' "$@" \
    | python3 -c 'import json,sys; print(",".join(sorted(r["metric"]["k8s_cluster_name"] + "/" + r["metric"]["k8s_namespace_name"] for r in json.load(sys.stdin)["data"]["result"])))'
}
logs() {
  local base="$1" tok="$2"; shift 2
  tool_curl -H "Authorization: Bearer $(token "$tok")" -G "$base/select/logsql/query" --data-urlencode 'query=*' --data-urlencode 'limit=100' "$@" \
    | python3 -c '
import json,sys
print(",".join(sorted({json.loads(x)["k8s.cluster.name"] + "/" + json.loads(x)["kubernetes.pod_namespace"] for x in sys.stdin if x.strip()})))'
}
expect() {
  local label="$1" got="$2" want="$3"
  if [ "$got" = "$want" ]; then echo "PASS  $label -> $got"; else echo "FAIL  $label -> $got (wanted $want)" >&2; fail=1; fi
}
# The first request through a new policy can race Envoy's configuration.
for _ in $(seq 1 60); do
  [ "$(status "$gw" ab)" = 200 ] && break; sleep 2
done

echo; echo "=== 1. two namespace groups: exactly those namespaces, metrics and logs ==="
expect "person [team-a, team-b, unrelated] metrics via the gateway  " "$(metrics "$gw" ab)" "cluster-a/ns-a,cluster-a/ns-b"
expect "person [team-a, team-b, unrelated] logs via the gateway     " "$(logs "$gw" ab)" "cluster-a/ns-a,cluster-a/ns-b"
expect "person [ops, team-a]: two clusters, metrics                 " "$(metrics "$gw" ops-a)" "cluster-a/ns-a,cluster-b/ns-x"
expect "person [ops, team-a]: two clusters, logs (one OR-ed filter) " "$(logs "$gw" ops-a)" "cluster-a/ns-a,cluster-b/ns-x"
expect "person [team-a]: one namespace                              " "$(metrics "$gw" a)" "cluster-a/ns-a"

echo; echo "=== 2. no group selects a principal ==="
expect "person [nobody]                                             " "$(status "$gw" nobody)" "403"

echo; echo "=== 3. forged token ==="
expect "signed by another key                                       " "$(status "$gw" forged)" "401"

echo; echo "=== 4. another audience passes through to vmauth's own readers ==="
expect "workload token via the gateway: its own principal's reach   " "$(metrics "$gw" workload)" "cluster-a/ns-a"
expect "workload token direct to vmauth: the same                   " "$(metrics "$direct" workload)" "cluster-a/ns-a"

echo; echo "=== 5. the person's own token straight to vmauth ==="
expect "[team-a, team-b] direct: one group principal, not the union " "$(metrics "$direct" ab)" "cluster-a/ns-a"
expect "[nobody] + client vm_access claim direct: 401               " "$(status "$direct" nobody-claims-b)" "401"
expect "[nobody] + client vm_access claim via gateway: 403          " "$(status "$gw" nobody-claims-b)" "403"
expect "[team-a] + client vm_access (cluster-b) via gateway: ignored" "$(metrics "$gw" a-claims-b)" "cluster-a/ns-a"

echo; echo "=== 6. client-supplied filters cannot widen ==="
expect "extra_filters naming another namespace (metrics)            " "$(metrics "$gw" a --data-urlencode 'extra_filters={k8s_namespace_name="ns-b"}')" "cluster-a/ns-a"
# vmauth drops a client argument that clashes with the route's own BY NAME, and
# VictoriaMetrics also reads the array spelling `extra_filters[]` and ORs it with
# the grant. A route pinning only `extra_filters` therefore let a client widen
# EVERY reader's result with `extra_filters[]`, the group principals' straight
# through vmauth included. Every metrics route now pins both spellings.
expect "extra_filters[] matching everything (metrics), gateway      " "$(metrics "$gw" a --data-urlencode 'extra_filters[]={k8s_cluster_name=~".+"}')" "cluster-a/ns-a"
expect "extra_filters[] matching everything (metrics), vmauth direct" "$(metrics "$direct" a --data-urlencode 'extra_filters[]={k8s_cluster_name=~".+"}')" "cluster-a/ns-a"
expect "extra_filters[] naming another namespace (metrics), gateway " "$(metrics "$gw" a --data-urlencode 'extra_filters[]={k8s_namespace_name="ns-b"}')" "cluster-a/ns-a"
expect "extra_filters[] matching everything, two-group person       " "$(metrics "$gw" ab --data-urlencode 'extra_filters[]={k8s_cluster_name=~".+"}')" "cluster-a/ns-a,cluster-a/ns-b"
expect "extra_stream_filters naming another namespace (logs)        " "$(logs "$gw" a --data-urlencode 'extra_stream_filters={"kubernetes.pod_namespace"="ns-b"}')" "cluster-a/ns-a"
expect "extra_stream_filters matching everything (logs)             " "$(logs "$gw" a --data-urlencode 'extra_stream_filters={"k8s.cluster.name"=~".+"}')" "cluster-a/ns-a"

echo; echo "=== 7. the service down: a person gets nothing; direct workload traffic is unaffected ==="
kn scale deployment/x-observability-stack-tenancy-authz --replicas=0 >/dev/null
kn wait --for=delete pod -l app.kubernetes.io/name=tenancy-authz --timeout=120s >/dev/null 2>&1 || true
sleep 3
code="$(status "$gw" ab)"
if [ "$code" != 200 ]; then echo "PASS  person via the gateway, service down -> HTTP $code (not 200: fails closed)"; else echo "FAIL  person via the gateway, service down -> HTTP 200" >&2; fail=1; fi
echo "INFO  workload via the gateway, service down -> HTTP $(status "$gw" workload) (the policy covers the whole route)"
expect "workload direct to vmauth, service down                     " "$(metrics "$direct" workload)" "cluster-a/ns-a"
kn scale deployment/x-observability-stack-tenancy-authz --replicas=2 >/dev/null
kn rollout status deployment/x-observability-stack-tenancy-authz --timeout=180s >/dev/null
for _ in $(seq 1 60); do
  [ "$(status "$gw" ab)" = 200 ] && break; sleep 2
done
expect "person via the gateway, service back                        " "$(metrics "$gw" ab)" "cluster-a/ns-a,cluster-a/ns-b"

echo; echo "=== 8. one key from the operator; deleting the Secret rotates it with no restart ==="
secret=x-observability-stack-tenancy-authz-signing
for _ in $(seq 1 60); do kn get secret "$secret" >/dev/null 2>&1 && break; sleep 2; done
pod_ips() { kn get pod -l app.kubernetes.io/name=tenancy-authz -o jsonpath='{.items[*].status.podIP}'; }
# The key set ONE pod serves: the Service would answer from either replica.
jwks_kids_of() { tool_curl "http://$1:8080/jwks" | python3 -c 'import json,sys; print(",".join(sorted(k["kid"] for k in json.load(sys.stdin)["keys"])))'; }
all_pods_kids() { for ip in $(pod_ips); do jwks_kids_of "$ip"; done | sort -u; }
expect "every replica publishes the same single key set             " "$(all_pods_kids | wc -l | tr -d ' ')" "1"
old_kid="$(all_pods_kids)"
echo "      kid: $old_kid"
kn delete secret "$secret" >/dev/null
# ExternalSecret recreates it, the kubelet refreshes each pod's mount, and
# each pod's keyring publishes the new key beside the old one.
n=0
for _ in $(seq 1 100); do
  n=0; for ip in $(pod_ips); do [ "$(jwks_kids_of "$ip" | tr ',' '\n' | grep -c .)" = 2 ] && n=$((n + 1)); done
  [ "$n" = 2 ] && break; sleep 3
done
expect "both replicas publish old and new key                       " "$n" "2"
expect "the old key is still published by both                      " "$(for ip in $(pod_ips); do jwks_kids_of "$ip" | tr ',' '\n' | grep -c "^$old_kid\$"; done | awk '{s+=$1} END {print s}')" "2"
expect "people still read (the old key signs until the switch)      " "$(metrics "$gw" ab)" "cluster-a/ns-a,cluster-a/ns-b"

echo
if [ "$fail" = 0 ]; then
  echo "hack/tenancy-authz-proof.sh: PASS — a person is granted the union of their groups through the gateway, nothing else matches, a client-supplied extra_filters, extra_filters[] or extra_stream_filters cannot widen it, workloads are untouched, and the service failing closes the route."
else
  echo "hack/tenancy-authz-proof.sh: FAIL" >&2; exit 1
fi

#!/usr/bin/env bash
# Install the release into a throwaway cluster and require the OPERATOR to
# accept it.
#
# hack/apply.sh proves the API SERVER accepts every object. That is not the
# same question, and 0.3.1 is the proof: the API server accepted a VMAuth
# perfectly, pruned a field it had never heard of, and the OPERATOR then
# refused what was left -- "at least one of url_map, url_prefix or
# targetRefs must be defined" -- so no Deployment was ever created and
# there was no read path at all. Every check upstream of the cluster was
# green, including an apply.
#
# So this one installs the chart for real, waits for the operator, and
# requires every custom resource to reach a working status AND to have
# produced the workload it exists to produce. A resource the operator
# refuses reports `failed` and creates nothing, which is exactly the shape
# of that incident.
#
# The admission webhook is disabled here on purpose: its certificate comes
# from cert-manager, which this repository does not ship, and the class
# this test exists for is the RECONCILE refusal rather than an admission
# one.
#
#   hack/reconcile.sh        create a throwaway cluster, check, delete it
#   KEEP=1 hack/reconcile.sh keep the cluster afterwards
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cluster="${KIND_CLUSTER:-observability-reconcile}"
keep="${KEEP:-0}"
namespace=observability

crds_golden="$root/tests/golden/observability-crds/minimal.yaml"
values="$root/tests/cases/observability-stack/minimal/values.yaml"

for tool in kind kubectl helm; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "hack/reconcile.sh needs $tool, which is not on PATH. It is in devbox.json." >&2
    exit 1
  }
done

docker info >/dev/null 2>&1 || {
  echo "hack/reconcile.sh needs a running Docker daemon: kind runs the cluster in containers." >&2
  exit 1
}

# Never the caller's kubeconfig.
kubeconfig="$(mktemp -t observability-reconcile-kubeconfig.XXXXXX)"
export KUBECONFIG="$kubeconfig"

# Where the cluster's own answers are written down before they are read.
docs_dir="$(mktemp -d -t observability-reconcile.XXXXXX)"

created=0
cleanup() {
  if [ "$created" = 1 ] && [ "$keep" != 1 ]; then
    kind delete cluster --name "$cluster" >/dev/null 2>&1 || true
  fi
  rm -f "$kubeconfig"
  rm -rf "$docs_dir"
}
trap cleanup EXIT

if kind get clusters 2>/dev/null | grep -qx "$cluster"; then
  kind export kubeconfig --name "$cluster" >/dev/null
else
  echo "creating kind cluster $cluster"
  kind create cluster --name "$cluster" --wait 240s >/dev/null
  created=1
fi

kubectl apply --server-side -f "$crds_golden" >/dev/null
kubectl wait --for=condition=Established --timeout=180s crd --all >/dev/null
kubectl create namespace "$namespace" --dry-run=client -o yaml | kubectl apply -f - >/dev/null

# The install reads these; their VALUES are never in this repository, so
# the test supplies its own. What is being checked is reconciliation, not
# authentication.
kubectl create secret generic observability-store-credentials \
  --namespace "$namespace" --from-literal=username=test --from-literal=password=test \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl create secret generic observability-write-token \
  --namespace "$namespace" --from-literal=token=test \
  --dry-run=client -o yaml | kubectl apply -f - >/dev/null

echo "installing the release"
# --no-hooks: the metrics subchart's post-install hook is a Job that pulls
# its own image and syncs dashboards and default rules. Helm WAITS for a
# hook whatever else you ask it, so it turns a two-minute check into a
# five-minute timeout, and it reconciles nothing this test is about. The
# rules it would have created are supplied below, deliberately, so their
# selection can be asserted rather than hoped for.
helm install observability-stack "$root/charts/observability-stack" \
  --namespace "$namespace" --values "$values" --no-hooks \
  --set victoria-metrics-k8s-stack.victoria-metrics-operator.admissionWebhooks.enabled=false \
  >/dev/null

echo "waiting for the operator"
kubectl wait --namespace "$namespace" --for=condition=Available --timeout=300s \
  deployment -l app.kubernetes.io/name=victoria-metrics-operator >/dev/null

# A rule in ANOTHER namespace, to settle what `ruleNamespaceSelector: {}`
# actually reaches. The chart sets it empty meaning "every namespace", on
# Kubernetes' own convention for an empty selector -- the shipped
# CustomResourceDefinition documents none of these fields, so it is
# asserted here rather than assumed.
# Two rules, in ANOTHER namespace, one of each language. Together they
# answer both questions the chart's selectors make claims about:
# whether `ruleNamespaceSelector: {}` really reaches other namespaces, and
# whether a PromQL rule can still reach the logs alerter -- which is what
# crash-looped it in 0.3.3, and which no amount of rendering can show.
# The unlabelled one stands for every rule another chart ships.
kubectl create namespace observability-elsewhere --dry-run=client -o yaml | kubectl apply -f - >/dev/null
kubectl apply -f - >/dev/null <<'RULE'
apiVersion: operator.victoriametrics.com/v1beta1
kind: VMRule
metadata:
  name: reconcile-logs-rule
  namespace: observability-elsewhere
  labels:
    observability.rule-type: vlogs
spec:
  groups:
    - name: reconcile.logs-rule
      rules:
        - alert: CrossNamespaceLogsRuleWasSelected
          expr: '* | stats count() as hits'
---
apiVersion: operator.victoriametrics.com/v1beta1
kind: VMRule
metadata:
  name: reconcile-metrics-rule
  namespace: observability-elsewhere
spec:
  groups:
    - name: reconcile.metrics-rule
      rules:
        - alert: CrossNamespaceMetricsRuleWasSelected
          expr: 'up == 0'
RULE

echo "waiting for reconciliation"
crs="$docs_dir/crs.json"
kinds=vmauth,vmalert,vmsingle,vmalertmanager,vmagent

deadline=$(( $(date +%s) + 300 ))
while :; do
  kubectl get "$kinds" --namespace "$namespace" -o json > "$crs" 2>/dev/null || true
  pending="$(python3 "$root/hack/crstatus.py" "$crs" pending)"
  [ -z "$pending" ] && break
  case "$pending" in *=failed*) break;; esac
  [ "$(date +%s)" -gt "$deadline" ] && break
  sleep 5
done

kubectl get "$kinds" --namespace "$namespace" -o json > "$crs"
python3 "$root/hack/crstatus.py" "$crs" report

# A status is a claim; the workload is the thing. 0.3.1 was a resource the
# operator had a status for and which produced nothing.
#
# Retried, because a resource reports `expanding` from the moment the
# operator picks it up, which can be a moment before the object it creates
# is readable. What is NOT waited for is the workload becoming ready: that
# needs image pulls, and an image pull says nothing about whether the spec
# was right.
check_workloads() {
  local kind name workload
  missing=0
  while IFS=: read -r kind name; do
    [ -n "$kind" ] || continue
    workload="$(echo "$kind" | tr '[:upper:]' '[:lower:]')-$name"
    if ! kubectl get deployment "$workload" --namespace "$namespace" >/dev/null 2>&1 \
      && ! kubectl get statefulset "$workload" --namespace "$namespace" >/dev/null 2>&1; then
      last_missing="$kind/$name (expected Deployment or StatefulSet $workload)"
      missing=$((missing + 1))
    fi
  done < <(python3 "$root/hack/crstatus.py" "$crs" names)
}

deadline=$(( $(date +%s) + 120 ))
while :; do
  check_workloads
  [ "$missing" = 0 ] && break
  [ "$(date +%s)" -gt "$deadline" ] && break
  sleep 5
done

if [ "$missing" != 0 ]; then
  echo "NO WORKLOAD: $missing resource(s) the operator has a status for produced nothing — last: $last_missing" >&2
  echo "That is the 0.3.1 shape: the resource exists, the operator has seen it, and nothing was created." >&2
  exit 1
fi
echo "every custom resource produced its workload"

# And the two selector questions, answered rather than assumed. The
# operator writes each alerter's selected rules into its own configmap, so
# that configmap IS the answer to "which rules would this alerter load".
read_groups() {
  kubectl get configmap -n "$namespace" -o json > "$docs_dir/cms.json" 2>/dev/null || return 0
  python3 "$root/hack/rulegroups.py" "$docs_dir/cms.json"
}

# Wait for BOTH alerters to have written their rule files, not for the
# first one: the two are reconciled independently and the metrics alerter
# gets there first, so breaking on the first `reconcile.` group that
# appears reports the other alerter as having selected nothing when it has
# simply not been reached yet. The deadline is what keeps this a test --
# it waits for the expected state, then asserts it, and a state that never
# arrives still fails.
echo "waiting for the operator to write the rule files"
deadline=$(( $(date +%s) + 240 ))
while :; do
  groups="$(read_groups)"
  seen_logs=0
  seen_metrics=0
  case "$groups" in *"logs:reconcile.logs-rule"*) seen_logs=1;; esac
  case "$groups" in *"metrics:reconcile.metrics-rule"*) seen_metrics=1;; esac
  [ "$seen_logs" = 1 ] && [ "$seen_metrics" = 1 ] && break
  [ "$(date +%s)" -gt "$deadline" ] && break
  sleep 5
done

failures=0

expect_group() {
  case "$groups" in
    *"$1:$2"*) ;;
    *) echo "MISSING: the $1 alerter did not select $2 — $3" >&2; failures=$((failures + 1)) ;;
  esac
}

refuse_group() {
  case "$groups" in
    *"$1:$2"*) echo "WRONGLY SELECTED: the $1 alerter selected $2 — $3" >&2; failures=$((failures + 1)) ;;
  esac
}

expect_group logs reconcile.logs-rule \
  "so ruleNamespaceSelector {} does NOT reach another namespace, while the chart claims it does"
expect_group metrics reconcile.metrics-rule \
  "so an unlabelled rule reaches NOBODY, and every rule another chart ships would be silently unevaluated"
refuse_group logs reconcile.metrics-rule \
  "this is 0.3.3: vmalert exits on the first rule it cannot parse, so the logs alerter crash-loops and takes every log rule with it"
refuse_group metrics reconcile.logs-rule \
  "the same failure the other way round: LogsQL does not parse as PromQL"

if [ "$failures" != 0 ]; then
  echo "selected groups were: ${groups:-<none>}" >&2
  exit 1
fi

echo "rule selection holds on a live operator, across namespaces, in both directions"

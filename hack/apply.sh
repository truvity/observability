#!/usr/bin/env bash
# Apply every golden render to a real API server and require it to be
# accepted.
#
# Three consecutive releases carried a defect that rendered, linted and
# diffed clean and was only ever visible in a cluster: a field the API
# server PRUNED (0.3.1), and a value it REFUSED (0.3.2). Both now have a
# static check that reads the CustomResourceDefinitions this repository
# ships — but each of those checks had to be written after the failure, and
# each only knows the one rule it was taught.
#
# An API server knows all of them. It enforces types, required fields, CEL
# validation rules, name shapes and immutability, without anybody here
# having to anticipate which one the next defect will break. So this runs
# the goldens past one.
#
# WHAT THIS DOES NOT COVER, stated plainly because the gap is the
# interesting part: an object the API server accepts can still be refused
# by the OPERATOR that reconciles it. That is exactly what 0.3.1 was — a
# pruned field left an unauthorized section the operator rejected, long
# after a clean apply. Catching that class needs the operator running, and
# this does not install it.
#
#   hack/apply.sh        create a throwaway cluster, check, delete it
#   KEEP=1 hack/apply.sh keep the cluster afterwards, to poke at it
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
cluster="${KIND_CLUSTER:-observability-apply}"
keep="${KEEP:-0}"

# The definitions come from the golden of the chart that installs them, so
# the objects are checked against what this repository would really put in
# a cluster rather than whatever an upstream tag holds today. `minimal` is
# the case that renders all of them.
crds_golden="$root/tests/golden/observability-crds/minimal.yaml"

# Documents whose definitions this repository does not ship. They are
# skipped BY GROUP and counted out loud: a silent skip is how a check ends
# up proving less than its name claims.
foreign_group="cert-manager.io"

for tool in kind kubectl helm; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "hack/apply.sh needs $tool, which is not on PATH. It is in devbox.json — run this inside 'devbox shell' or under direnv." >&2
    exit 1
  }
done

docker info >/dev/null 2>&1 || {
  echo "hack/apply.sh needs a running Docker daemon: kind runs the API server in a container." >&2
  exit 1
}

# Never the caller's kubeconfig. A test that edits ~/.kube/config can
# repoint a context somebody else is using.
kubeconfig="$(mktemp -t observability-apply-kubeconfig.XXXXXX)"
export KUBECONFIG="$kubeconfig"

created=0

cleanup() {
  if [ "$created" = 1 ] && [ "$keep" != 1 ]; then
    kind delete cluster --name "$cluster" >/dev/null 2>&1 || true
  fi
  rm -f "$kubeconfig"
}
trap cleanup EXIT

if kind get clusters 2>/dev/null | grep -qx "$cluster"; then
  kind export kubeconfig --name "$cluster" >/dev/null
else
  echo "creating kind cluster $cluster"
  # 240s, not the 120s that reads like plenty: on a machine already
  # running another kind cluster the control plane bootstrap regularly
  # takes past two minutes, and the failure looks like a broken script
  # rather than a slow one.
  kind create cluster --name "$cluster" --wait 240s >/dev/null
  created=1
fi

echo "installing the CustomResourceDefinitions this repository ships"
kubectl apply --server-side -f "$crds_golden" >/dev/null
kubectl wait --for=condition=Established --timeout=180s crd --all >/dev/null

docs_dir="$(mktemp -d -t observability-apply.XXXXXX)"
trap 'cleanup; rm -rf "$docs_dir"' EXIT

checked=0
skipped=0
failed=0

for golden in "$root"/tests/golden/*/*.yaml; do
  chart="$(basename "$(dirname "$golden")")"
  case_name="$(basename "$golden" .yaml)"

  # The definitions themselves are already installed above.
  [ "$chart" = observability-crds ] && continue

  namespace="$(cat "$root/tests/cases/$chart/$case_name/namespace" 2>/dev/null || echo default)"
  if [ "$namespace" != default ]; then
    kubectl create namespace "$namespace" --dry-run=client -o yaml | kubectl apply -f - >/dev/null
  fi

  # Split on helm's own document separator, so each object is reported by
  # name rather than the whole file failing as one.
  rm -rf "${docs_dir:?}/split" && mkdir -p "$docs_dir/split"
  awk -v dir="$docs_dir/split" '
    /^---$/ { n++; next }
    { print > sprintf("%s/%04d.yaml", dir, n) }
  ' n=0 "$golden"

  for doc in "$docs_dir"/split/*.yaml; do
    [ -s "$doc" ] || continue
    grep -q '^kind:' "$doc" || continue

    if grep -q "^apiVersion: $foreign_group/" "$doc"; then
      skipped=$((skipped + 1))
      continue
    fi

    if ! output="$(kubectl apply --server-side --force-conflicts --dry-run=server \
                     --namespace "$namespace" -f "$doc" 2>&1)"; then
      echo "REFUSED: $chart/$case_name — $(grep -m1 '^kind:' "$doc" | cut -d' ' -f2-) $(grep -m1 '^  name:' "$doc" | cut -d' ' -f4-)" >&2
      echo "$output" | sed 's/^/    /' >&2
      failed=$((failed + 1))
      continue
    fi

    checked=$((checked + 1))
  done
done

# A check that matched nothing passes forever while proving nothing.
if [ "$checked" = 0 ]; then
  echo "no object was applied at all — the goldens moved, or the split stopped working" >&2
  exit 1
fi

if [ "$failed" != 0 ]; then
  echo "$failed object(s) the API server would refuse" >&2
  exit 1
fi

echo "apply check: $checked objects accepted by a real API server, $skipped skipped as $foreign_group (not shipped here)"

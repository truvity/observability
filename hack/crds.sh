#!/usr/bin/env bash
# Regenerate charts/observability-crds from its five pinned upstreams.
#
# All five sets are fetched, never hand-written, and all are pinned:
#
#   VictoriaMetrics operator   the upstream victoria-metrics-operator-crds
#                              chart, vendored under the chart's own charts/
#                              directory so a render needs no network.
#   Prometheus Operator        ocictl's crdctl, from the upstream repository
#                              at hack/crds/prometheus-operator/crdctl.yaml,
#                              reduced to the four scrape kinds this stack's
#                              components actually author.
#   grafana-operator           ocictl's crdctl, from the upstream repository
#                              at hack/crds/grafana-operator/crdctl.yaml, all
#                              thirteen kinds, at the version
#                              charts/observability-cluster vendors.
#   Envoy Gateway              ocictl's crdctl, from the upstream repository
#                              at hack/crds/envoy-gateway/crdctl.yaml, the
#                              SecurityPolicy only: a copy for the schema
#                              check, off by default.
#   External Secrets           ocictl's crdctl, from the upstream repository
#                              at hack/crds/external-secrets/crdctl.yaml, the
#                              ExternalSecret and the SSHKey generator only,
#                              for the same reason.
#
# docs/doctrine.md says why those two mechanisms rather than one.
#
#   hack/crds.sh    fetch and regenerate, then run `just golden` and read
#                   the inventory at the foot of the golden before
#                   committing. That block is the review.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
chart="$root/charts/observability-crds"
gen="$chart/templates"

# The four kinds. Every component in a consuming estate authors its scrape
# objects in these; the rest of the Prometheus Operator's CRDs describe
# workloads this stack does not run, and installing them would invite
# objects nothing here reconciles.
keep="podmonitors.monitoring.coreos.com probes.monitoring.coreos.com scrapeconfigs.monitoring.coreos.com servicemonitors.monitoring.coreos.com"

# --- The VictoriaMetrics set -------------------------------------------
#
# `build` uses the committed Chart.lock and fails when Chart.yaml has moved
# past it, which is exactly when the lock and the vendored archive need
# resolving again.
if ! helm dependency build "$chart" >/dev/null 2>&1; then
  helm dependency update "$chart"
fi

vm_tgz=("$chart"/charts/victoria-metrics-operator-crds-*.tgz)
vm_tgz="${vm_tgz[0]}"
vm_crds="$(mktemp)"
tar -xzOf "$vm_tgz" victoria-metrics-operator-crds/charts/crds/crd.yaml > "$vm_crds"

# crdctl is pinned to a release, like every other input: `@latest` would make
# a regeneration depend on the day it runs. Bump it by hand and read the diff.
crdctl="github.com/truvity/ocictl/cmd/crdctl@v0.8.1"

# What is dropped is exactly a `description` key with a scalar value.
# A property NAMED description is a mapping — `description:` with nothing
# after it — and is left alone, which matters because dropping one would
# delete a field from the schema rather than a sentence from it.
strip_descriptions() {
  awk '
    /^[[:space:]]*description: ./ { ind = match($0, /[^ ]/) - 1; strip = 1; next }
    strip {
      if ($0 ~ /^[[:space:]]*$/) { next }
      cur = match($0, /[^ ]/) - 1
      if (cur > ind) { next }
      strip = 0
    }
    { print }
  ' "$1"
}

# --- The Prometheus Operator set ---------------------------------------
go run "$crdctl" build \
  --config "$root/hack/crds/prometheus-operator/crdctl.yaml"

po_all="$root/hack/crds/prometheus-operator/templates/crds.yaml"
po_crds="$(mktemp)"

# crdctl fetches a whole upstream directory; it has no way to ask for four
# files out of ten. So the selection happens here, by CRD name, and a name
# that upstream has renamed drops out of the inventory rather than being
# silently replaced by something else.
awk -v keep="$keep" '
  BEGIN { split(keep, k, " "); for (i in k) wanted[k[i]] = 1 }
  /^---$/ { if (want) printf "---\n%s", doc; doc = ""; want = 0; next }
  { doc = doc $0 "\n" }
  /^  name: / { if ($2 in wanted) want = 1 }
  END { if (want) printf "---\n%s", doc }
' "$po_all" > "$po_crds"

# Descriptions are dropped, the way upstream drops them from its own
# published CRD bundles. Three reasons, in order of weight. They are the
# whole of the size difference — 934 KB against 350 KB for the same four
# definitions — and a golden is only a review if someone reads it. They
# carry prose naming in-pod mount paths under a secrets directory, which
# is documentation and not a particular — but the leak canary cannot tell
# the difference, and a canary that cries wolf is one people learn to skip.
# And the VictoriaMetrics half is already description-free, because
# upstream ships its CRDs chart that way; one chart should not install two
# kinds of CRD.
#
po_full="$po_crds"
po_crds="$(mktemp)"
strip_descriptions "$po_full" > "$po_crds"
rm -f "$po_full"

found="$(grep -c '^kind: CustomResourceDefinition$' "$po_crds")"
wanted="$(printf '%s\n' $keep | wc -l)"
if [ "$found" != "$wanted" ]; then
  echo "hack/crds.sh: kept $found of $wanted Prometheus Operator CRDs — a name has changed upstream" >&2
  exit 1
fi

{
  echo '{{/*'
  echo 'Generated by hack/crds.sh from hack/crds/prometheus-operator/crdctl.yaml.'
  echo 'Do not edit: a hand edit is lost at the next bump and, worse, is invisible'
  echo 'in the diff that bump produces.'
  echo ''
  echo 'A Helm comment rather than a YAML one, so that the file contributes no'
  echo 'empty document of its own to the render.'
  echo '*/}}'
  echo '{{- if .Values.sets.prometheusOperator }}'
  cat "$po_crds"
  echo '{{- end }}'
} > "$gen/prometheus-operator.yaml"

# --- The grafana-operator set --------------------------------------------
#
# Every kind, descriptions dropped for the same reasons as above. The
# Grafana kind is 600 KB with them and still 345 KB without: past the 256 KiB
# that a client-side `kubectl apply` can store in its last-applied
# annotation. So this set can only be applied server-side, whatever is done
# to its size, and docs/adoption.md says so.
go run "$crdctl" build \
  --config "$root/hack/crds/grafana-operator/crdctl.yaml"

go_full="$root/hack/crds/grafana-operator/templates/crds.yaml"
go_crds="$(mktemp)"
strip_descriptions "$go_full" > "$go_crds"

# The operator kinds are one fixed number; a different count means upstream
# added or dropped a kind and the inventory below must be read, not trusted.
found="$(grep -c '^kind: CustomResourceDefinition$' "$go_crds")"
if [ "$found" != 13 ]; then
  echo "hack/crds.sh: found $found grafana-operator CRDs, expected 13 — a kind has been added or removed upstream; update this count and the docs after reading the inventory" >&2
  exit 1
fi

{
  echo '{{/*'
  echo 'Generated by hack/crds.sh from hack/crds/grafana-operator/crdctl.yaml.'
  echo 'Do not edit: a hand edit is lost at the next bump and, worse, is invisible'
  echo 'in the diff that bump produces.'
  echo ''
  echo 'A Helm comment rather than a YAML one, so that the file contributes no'
  echo 'empty document of its own to the render.'
  echo '*/}}'
  echo '{{- if .Values.sets.grafanaOperator }}'
  cat "$go_crds"
  echo '{{- end }}'
} > "$gen/grafana-operator.yaml"

# --- The Envoy Gateway SecurityPolicy ------------------------------------
#
# One kind out of the set, selected by CRD name for the same reason the
# Prometheus Operator's four are: crdctl fetches a whole upstream directory
# and has no way to ask for one file. A name upstream has renamed drops out
# and the count check below fails, rather than something else being
# installed in its place.
go run "$crdctl" build \
  --config "$root/hack/crds/envoy-gateway/crdctl.yaml"

eg_all="$root/hack/crds/envoy-gateway/templates/crds.yaml"
eg_full="$(mktemp)"
awk -v keep="securitypolicies.gateway.envoyproxy.io" '
  BEGIN { wanted[keep] = 1 }
  /^---$/ { if (want) printf "---\n%s", doc; doc = ""; want = 0; next }
  { doc = doc $0 "\n" }
  /^  name: / { if ($2 in wanted) want = 1 }
  END { if (want) printf "---\n%s", doc }
' "$eg_all" > "$eg_full"
eg_crds="$(mktemp)"
strip_descriptions "$eg_full" > "$eg_crds"
rm -f "$eg_full"

found="$(grep -c '^kind: CustomResourceDefinition$' "$eg_crds")"
if [ "$found" != 1 ]; then
  echo "hack/crds.sh: kept $found of 1 Envoy Gateway CRDs — securitypolicies.gateway.envoyproxy.io has been renamed or removed upstream" >&2
  exit 1
fi

{
  echo '{{/*'
  echo 'Generated by hack/crds.sh from hack/crds/envoy-gateway/crdctl.yaml.'
  echo 'Do not edit: a hand edit is lost at the next bump and, worse, is invisible'
  echo 'in the diff that bump produces.'
  echo ''
  echo 'A Helm comment rather than a YAML one, so that the file contributes no'
  echo 'empty document of its own to the render.'
  echo '*/}}'
  echo '{{- if .Values.sets.envoyGateway }}'
  cat "$eg_crds"
  echo '{{- end }}'
} > "$gen/envoy-gateway.yaml"

# --- The External Secrets ExternalSecret and SSHKey ----------------------
#
# Two kinds out of the set, selected by CRD name for the same reason.
go run "$crdctl" build \
  --config "$root/hack/crds/external-secrets/crdctl.yaml"

es_all="$root/hack/crds/external-secrets/templates/crds.yaml"
es_full="$(mktemp)"
awk -v keep="externalsecrets.external-secrets.io sshkeys.generators.external-secrets.io" '
  BEGIN { split(keep, k, " "); for (i in k) wanted[k[i]] = 1 }
  /^---$/ { if (want) printf "---\n%s", doc; doc = ""; want = 0; next }
  { doc = doc $0 "\n" }
  /^  name: / { if ($2 in wanted) want = 1 }
  END { if (want) printf "---\n%s", doc }
' "$es_all" > "$es_full"
es_crds="$(mktemp)"
strip_descriptions "$es_full" > "$es_crds"
rm -f "$es_full"

found="$(grep -c '^kind: CustomResourceDefinition$' "$es_crds")"
if [ "$found" != 2 ]; then
  echo "hack/crds.sh: kept $found of 2 External Secrets CRDs — externalsecrets or sshkeys has been renamed or removed upstream" >&2
  exit 1
fi

{
  echo '{{/*'
  echo 'Generated by hack/crds.sh from hack/crds/external-secrets/crdctl.yaml.'
  echo 'Do not edit: a hand edit is lost at the next bump and, worse, is invisible'
  echo 'in the diff that bump produces.'
  echo ''
  echo 'A Helm comment rather than a YAML one, so that the file contributes no'
  echo 'empty document of its own to the render.'
  echo '*/}}'
  echo '{{- if .Values.sets.externalSecrets }}'
  cat "$es_crds"
  echo '{{- end }}'
} > "$gen/external-secrets.yaml"

# --- The inventory -----------------------------------------------------
#
# The golden render of this chart is megabytes of upstream schema, and the
# question a bump has to answer is none of it: did a kind disappear, get
# renamed, or change the version it stores? So the chart renders the answer
# as a comment block, generated from the same files it installs. It carries
# no object — it exists so that `tests/golden/observability-crds/*.yaml`
# ends on a list a person can read.
inventory() {
  awk '
    function flush(   i, vers) {
      if (kind != "") {
        vers = ""
        for (i = 1; i <= nv; i++) {
          vers = vers (vers == "" ? "" : ", ") vname[i]
          if (vstorage[i]) { vers = vers " (storage)" }
          if (!vserved[i]) { vers = vers " (not served)" }
        }
        printf "#   %-24s %-34s %s\n", kind, group, vers
      }
      kind = ""; group = ""; nv = 0; inver = 0
    }
    /^---$/ { flush(); next }
    /^  group: / { group = $2; next }
    /^    kind: / { if (!inver) { kind = $2 }; next }
    /^  versions:/ { inver = 1; next }
    inver && /^  - name: / { nv++; vname[nv] = $3; vserved[nv] = 1; vstorage[nv] = 0; next }
    inver && /^    name: / { nv++; vname[nv] = $2; vserved[nv] = 1; vstorage[nv] = 0; next }
    inver && /^      name: v[0-9]/ { nv++; vname[nv] = $2; vserved[nv] = 1; vstorage[nv] = 0; next }
    inver && /^    served: false/ { vserved[nv] = 0; next }
    inver && /^    storage: true/ { vstorage[nv] = 1; next }
    inver && /^      served: false/ { vserved[nv] = 0; next }
    inver && /^      storage: true/ { vstorage[nv] = 1; next }
    END { flush() }
  ' "$1"
}

vm_chart_version="$(basename "$vm_tgz" .tgz | sed 's/^victoria-metrics-operator-crds-//')"
vm_app_version="$(tar -xzOf "$vm_tgz" victoria-metrics-operator-crds/Chart.yaml | awk '/^appVersion:/ { print $2 }')"
es_version="$(awk '/^pinned_version:/ { gsub(/"/, "", $2); print $2 }' "$root/hack/crds/external-secrets/crdctl.yaml")"
eg_version="$(awk '/^pinned_version:/ { gsub(/"/, "", $2); print $2 }' "$root/hack/crds/envoy-gateway/crdctl.yaml")"
go_version="$(awk '/^pinned_version:/ { gsub(/"/, "", $2); print $2 }' "$root/hack/crds/grafana-operator/crdctl.yaml")"
po_version="$(awk '/^pinned_version:/ { gsub(/"/, "", $2); print $2 }' "$root/hack/crds/prometheus-operator/crdctl.yaml")"

{
  echo '# Generated by hack/crds.sh. Do not edit.'
  echo '#'
  echo '# Every CustomResourceDefinition this chart installs, with the versions each'
  echo '# one serves and the one it stores. This document carries no object: it is'
  echo '# here so that a bump of either upstream shows up as a few readable lines at'
  echo '# the foot of the golden render, instead of as a diff nobody can review.'
  echo '#'
  echo '{{- if .Values.sets.victoriaMetrics }}'
  echo "# VictoriaMetrics operator $vm_app_version, chart victoria-metrics-operator-crds-$vm_chart_version"
  echo '#'
  inventory "$vm_crds"
  echo '{{- end }}'
  echo '{{- if .Values.sets.prometheusOperator }}'
  echo '#'
  echo "# Prometheus Operator v$po_version, the scrape kinds only"
  echo '#'
  inventory "$po_crds"
  echo '{{- end }}'
  echo '{{- if .Values.sets.grafanaOperator }}'
  echo '#'
  echo "# grafana-operator v$go_version"
  echo '#'
  inventory "$go_crds"
  echo '{{- end }}'
  echo '{{- if .Values.sets.envoyGateway }}'
  echo '#'
  echo "# Envoy Gateway v$eg_version, the SecurityPolicy only"
  echo '#'
  inventory "$eg_crds"
  echo '{{- end }}'
  echo '{{- if .Values.sets.externalSecrets }}'
  echo '#'
  echo "# External Secrets Operator v$es_version, the ExternalSecret and the SSHKey generator only"
  echo '#'
  inventory "$es_crds"
  echo '{{- end }}'
} > "$gen/crd-inventory.yaml"

rm -f "$vm_crds" "$po_crds" "$go_crds" "$eg_crds" "$es_crds"
rm -rf "$root/hack/crds/prometheus-operator/templates" "$root/hack/crds/grafana-operator/templates" "$root/hack/crds/envoy-gateway/templates" "$root/hack/crds/external-secrets/templates"

echo "regenerated $gen — run 'just golden' and read the inventory"

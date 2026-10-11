#!/usr/bin/env bash
# Re-vendor one chart's pinned dependencies into its charts/ directory.
#
#   hack/vendor.sh <chart>        (the `just vendor` recipe calls this)
#
# For every chart but one this is `helm dependency update` and nothing more.
# The exception is observability-cluster, whose grafana-operator archive has
# its nested CRDs subchart (charts/crds/crds/*.yaml) removed afterwards, on
# purpose:
#
#   Helm installs everything under a chart's crds/ directory, subcharts'
#   included, and upstream's grafana-operator chart ships its CRDs there by
#   default. Those CRDs belong to charts/observability-crds `sets.grafanaOperator`
#   (docs/architecture/observability-cluster.md, "CRDs"), applied server-side
#   and upgraded with the operator. A second owner would take turns
#   overwriting the first, and Helm never upgrades what it installed from
#   crds/ in any case. The chart has no switch that turns the subchart off
#   without rendering the CRDs as templates instead, so the subchart is not
#   shipped. tests/observability_cluster_test.go fails if it comes back.
#
# Two small extension points are patched into the operator's Deployment
# template afterwards (hack/patches/grafana-operator-extension-points.patch):
# `extraContainers` (rendered through tpl) and `kubeAuth.volume` (a volume
# source that replaces the projected token volume). Upstream offers neither,
# and the token sidecar of observability-cluster (externalGrafana.auth) needs
# both. With neither set the Deployment renders as upstream's. `patch` fails
# loudly if a version bump moves the lines it touches.
#
# The repack is deterministic (sorted names, epoch mtime, no owners, gzip
# without a timestamp), so running this twice yields the same bytes.
set -euo pipefail

chart="${1:?usage: hack/vendor.sh <chart>}"
root="$(cd "$(dirname "$0")/.." && pwd)"
dir="$root/charts/$chart"

helm dependency update "$dir"

if [ "$chart" = observability-cluster ]; then
  for tgz in "$dir"/charts/grafana-operator-*.tgz; do
    work="$(mktemp -d)"
    tar -xzf "$tgz" -C "$work"
    rm -rf "$work/grafana-operator/charts/crds"
    patch -p1 -d "$work/grafana-operator" < "$root/hack/patches/grafana-operator-extension-points.patch"
    # An emptied charts/ directory goes too: nothing else lives in it.
    rmdir "$work/grafana-operator/charts" 2>/dev/null || true
    (cd "$work" && tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner \
        -cf - grafana-operator | gzip -n -9 > "$tgz.new")
    mv "$tgz.new" "$tgz"
    rm -rf "$work"
  done
fi

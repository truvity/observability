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
    # An emptied charts/ directory goes too: nothing else lives in it.
    rmdir "$work/grafana-operator/charts" 2>/dev/null || true
    (cd "$work" && tar --sort=name --mtime=@0 --owner=0 --group=0 --numeric-owner \
        -cf - grafana-operator | gzip -n -9 > "$tgz.new")
    mv "$tgz.new" "$tgz"
    rm -rf "$work"
  done
fi

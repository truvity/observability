#!/usr/bin/env bash
# Regenerate charts/observability-dashboards/dashboards from
# hack/dashboards/sources.yaml.
#
# Every dashboard is fetched from a URL upstream's own store chart names
# in its `defaultDashboards.sources` list (see
# charts/observability-stack/charts/victoria-metrics-k8s-stack-*.tgz),
# pinned to a release rather than the moving branch that list points at,
# rewritten to docs/dashboards.md's contract by hack/dashboards.py, and
# committed — a render needs no network, the way `hack/crds.sh` vendors
# CRDs instead of resolving them at render time.
#
#   hack/dashboards.sh     fetch and rewrite, then run `just golden` and
#                           `just dashboard-lint` and read both diffs
#                           before committing. That is the review.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"

python3 "$root/hack/dashboards.py"

echo "regenerated charts/observability-dashboards/dashboards — run 'just golden' and 'just dashboard-lint'"

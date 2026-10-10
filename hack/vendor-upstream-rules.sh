#!/usr/bin/env bash
# hack/vendor-upstream-rules.sh — refresh charts/platform-alerts/upstream/,
# the vendored copy of the upstream rule sets that victoria-metrics-k8s-stack's
# sync job applies at run time (restructure step 4).
#
#   hack/vendor-upstream-rules.sh           re-vendor from the PINNED commits
#                                           (deterministic: same pins, same bytes)
#   hack/vendor-upstream-rules.sh update    move every pin to the current head of
#                                           its tracked branch, then re-vendor
#   hack/vendor-upstream-rules.sh check     re-vendor into a temp dir and fail if
#                                           the committed files differ
#
# How: render charts/observability-stack (the `minimal` test case) and take
# the sync job's own ConfigMap, change it in four ways (only the enabled
# sources; every group on; the two install-specific values replaced with
# placeholders; the chart's RecordingRulesNoData override removed so the file
# is plain upstream), point every source URL at its PINNED commit, and run the
# vendored sync job's REAL image (the one the stack renders) against a
# throwaway fake API server that records what the job applies. The recorded
# VMRule groups are what the stack would have put in the cluster.
#
# Needs Docker, helm, python3 with PyYAML, git, curl and network access to
# raw.githubusercontent.com and ghcr.io. Deliberately NOT part of CI (the
# network and the image are not hermetic); CI instead checks that the
# committed files still match the checksums in upstream/PIN.yaml
# (tests/upstream_rule_pack_test.go).
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
me="hack/vendor-upstream-rules.sh"
mode="${1:-vendor}"
case "$mode" in vendor | update | check) ;; *) echo "usage: $me [vendor|update|check]" >&2; exit 2 ;; esac

for tool in docker helm python3 git curl; do
  command -v "$tool" >/dev/null 2>&1 || { echo "$me needs $tool on PATH" >&2; exit 1; }
done
python3 -c 'import yaml' 2>/dev/null || { echo "$me needs python3's PyYAML" >&2; exit 1; }

exec python3 -I "$root/hack/vendor_upstream_rules.py" "$root" "$mode"

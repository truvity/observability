#!/usr/bin/env bash
# hack/mcp-paths-proof.sh — REAL proof, with the three stock VictoriaMetrics
# MCP servers built from their pinned tags and the aggregator built from this
# tree, that every tool charts/observability-mcp exposes for a store reaches
# an HTTP path the store's vmauth serves, and that the tools the chart drops
# are the ones whose paths it does not.
#
# What runs:
#   mcp-victoriametrics, mcp-victorialogs, mcp-victoriatraces
#                built from the tags in charts/observability-mcp/values.yaml
#                (go build, vendored), configured with the ENVIRONMENT THE
#                CHART RENDERS for them (`helm template` of the `single-store`
#                case), on the loopback ports the chart gives them.
#   mcp-aggregator
#                this tree, configured with the ConfigMap the chart renders.
#   a stand-in for the proxy's outbound listener and the store's vmauth,
#                on 127.0.0.1:8429: a python server that answers 200 on a
#                path a route of tests/golden/observability-stack/
#                tenancy-mcp-reader.yaml (the reader principal, all three
#                routes and vmalertAPI) serves, and 401 on any other path,
#                and records every request.
#
# Then every tool the aggregator exposes is called through its MCP endpoint,
# with arguments built from its own input schema, and the script checks the
# call did not come back as an error and that the path the stand-in saw is the
# one tests/mcp_routes_test.go says that tool asks for. It also calls the
# tools the chart drops, against the stock servers directly, and shows each
# arrive at a path the routes refuse.
#
# Needs go, git, helm and python3 (PyYAML) and network access to github.com.
# Not part of `check` or CI, like the other hack/*-proof.sh: a run-by-hand
# proof. MCP_SRC_DIR=<dir> reuses clones and builds from an earlier run.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
for tool in go git helm python3; do
  command -v "$tool" >/dev/null 2>&1 || { echo "hack/mcp-paths-proof.sh needs $tool on PATH" >&2; exit 1; }
done
python3 -c 'import yaml' 2>/dev/null || { echo "needs python3 with PyYAML" >&2; exit 1; }

work="${MCP_SRC_DIR:-$(mktemp -d -t mcp-paths-proof.XXXXXX)}"
mkdir -p "$work"
if [ -z "${MCP_SRC_DIR:-}" ]; then trap 'rm -rf "$work"' EXIT; fi

exec python3 "$root/hack/mcp_paths_proof.py" "$root" "$work"

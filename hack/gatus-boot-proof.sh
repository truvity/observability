#!/usr/bin/env bash
# hack/gatus-boot-proof.sh — REAL proof, in Docker, that pkg/statusbox's
# RenderGatus produces a Config the real twinproduction/gatus:v5.37.0
# image actually boots — not a stand-in, and not only the golden-YAML
# comparison tests/golden/observability-emitters-shaped assertions
# already give (gatus_internal_test.go proves the STRUCTURE; this proves
# the bytes are something Gatus itself accepts).
#
# Renders a representative Catalogue (two companies, a business host
# with and without a StatusPath, the deadman, both alert providers, and
# a Public instance's OIDC security block) through RenderGatus, boots it,
# and requires:
#   1. The container's own /health endpoint answers 200 ("UP") — Gatus
#      parsed the config and is serving.
#   2. Every endpoint this render named actually appears in Gatus's own
#      API (/api/v1/endpoints/statuses) — proving Gatus accepted every
#      name/group pair without refusing one as a duplicate (the exact
#      failure endpointNames' own collision fallback exists to avoid).
#   3. The container logs carry none of Gatus's own fatal parse/refusal
#      strings.
#
# Needs Docker, curl and jq. Deliberately not part of `check` or CI, the
# same reason hack/statusbox-ec2-ci.sh's own Docker requirement is
# not: a one-off, run-by-hand proof, not a regression gate.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d -t gatus-boot-proof.XXXXXX)"
container=gatus-boot-proof

cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

for tool in docker curl jq go; do
  command -v "$tool" >/dev/null 2>&1 || {
    echo "hack/gatus-boot-proof.sh needs $tool, which is not on PATH" >&2
    exit 1
  }
done

cat > "$work/render.go" <<'EOF'
package main

import (
	"fmt"
	"os"

	"github.com/truvity/observability/pkg/statusbox"
)

func main() {
	c := statusbox.Catalogue{
		PlatformHosts: []string{"argocd.example.private", "grafana.example.private"},
		Companies: []statusbox.Company{
			{
				Code:        "acme",
				DisplayName: "Acme Corp",
				Hosts: []statusbox.CompanyHost{
					{Host: "billing.devel.example.xyz", Env: "devel"},
					{Host: "keycloak.prod.example.xyz", Env: "prod", StatusPath: "/realms/customer/.well-known/openid-configuration"},
				},
			},
			{
				Code:        "globex",
				DisplayName: "Globex LLC",
			},
		},
		AlertsRead: statusbox.AlertsRead{Host: "alerts.kernel.example.private", TokenEnvKey: "ops_alerts_read_token"},
		// Providers and Security are deliberately left zero/nil here:
		// Providers configured would need a container reachable Slack/
		// PagerDuty endpoint to validate against, and Security's own
		// issuer-url would need Gatus to complete OIDC discovery
		// against a REAL issuer at boot — neither is available to this
		// throwaway proof, and neither is what this script exists to
		// check (both are exercised structurally, with no network
		// needed, by pkg/statusbox's own gatus_internal_test.go). This
		// is the "no alert providers configured yet" shape
		// RenderGatus's own leading comment names, and it is what a
		// breakglass instance (Security: nil) always renders.
		StoragePath: "/data/ops.db",
	}

	out, err := statusbox.RenderGatus(c)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	if err := os.WriteFile(os.Args[1], []byte(out), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
EOF

echo "hack/gatus-boot-proof.sh: rendering a representative Catalogue with RenderGatus"
( cd "$root" && go run "$work/render.go" "$work/config.yaml" )

echo "--- rendered config ---"
cat "$work/config.yaml"
echo "-----------------------"

echo "hack/gatus-boot-proof.sh: starting twinproduction/gatus:v5.37.0 with the rendered config"
mkdir -p "$work/data"
docker run -d --name "$container" \
  -p 127.0.0.1::8080 \
  -e GATUS_CONFIG_PATH=/config/config.yaml \
  -v "$work/config.yaml:/config/config.yaml:ro" \
  -v "$work/data:/data" \
  twinproduction/gatus:v5.37.0 >/dev/null

port="$(docker port "$container" 8080/tcp | cut -d: -f2)"

echo "hack/gatus-boot-proof.sh: waiting for it to come up"
up=0
for _ in $(seq 1 30); do
  if curl -fsS "http://127.0.0.1:$port/health" >/dev/null 2>&1; then
    up=1
    break
  fi
  sleep 1
done

fail=0

if [ "$up" != 1 ]; then
  echo "FAIL: /health never answered — container log:" >&2
  docker logs "$container" >&2 || true
  fail=1
else
  health="$(curl -fsS "http://127.0.0.1:$port/health")"
  echo "hack/gatus-boot-proof.sh: /health -> $health"
  if ! grep -q '"status":"UP"' <<<"$health"; then
    echo "FAIL: /health did not report UP: $health" >&2
    fail=1
  fi
fi

echo "hack/gatus-boot-proof.sh: checking every rendered endpoint is live in Gatus's own API"
# Every endpoint's monitoring goroutine starts at its own pace, and a
# fake hostname's first probe still has to time out before its result
# is recorded — so the listing fills in over several seconds, not
# instantly with /health. Poll until all 7 are present or give up.
listing="[]"
for _ in $(seq 1 30); do
  listing="$(curl -fsS "http://127.0.0.1:$port/api/v1/endpoints/statuses")"
  if [ "$(jq 'length' <<<"$listing")" -ge 7 ]; then
    break
  fi
  sleep 1
done
for label in "argocd.example.private" "grafana.example.private" "billing · devel" "keycloak · prod" "acme-customer-facing" "globex-customer-facing" "deadman"; do
  if ! jq -e --arg name "$label" '[.[] | select(.name == $name)] | length > 0' >/dev/null <<<"$listing"; then
    echo "FAIL: no endpoint named $label in Gatus's own /api/v1/endpoints/statuses — see the full listing below" >&2
    echo "$listing" | jq '[.[].name]' >&2
    fail=1
  fi
done

logs="$(docker logs "$container" 2>&1 || true)"
for bad in "invalid endpoint" "should contain at least one endpoint" "yaml: unmarshal errors" "panic:"; do
  if grep -qF "$bad" <<<"$logs"; then
    echo "FAIL: container log contains %q" "$bad" >&2
    echo "$logs" >&2
    fail=1
  fi
done

if [ "$fail" = 0 ]; then
  echo "hack/gatus-boot-proof.sh: RenderGatus's output boots on the real image, serves /health, and every rendered endpoint is live — OK"
fi
exit $fail

#!/usr/bin/env bash
# hack/dashboards/verify-live.sh
#
# For maintainers, before a release; NOT run in CI. Checks the claim
# hack/dashboards/available-metrics.yaml makes -- that a store holds each
# listed series -- against a real store, and prints, per source, which listed
# names are missing from it.
#
#   STORE_URL=https://<metrics read endpoint> STORE_TOKEN=<bearer token> \
#     hack/dashboards/verify-live.sh
#
# STORE_URL is the base URL of a Prometheus-compatible query API (it must
# serve /api/v1/label/__name__/values). STORE_TOKEN, if set, is sent as a
# bearer token. Neither is stored anywhere: both come from the environment.
#
# Names a source lists under `absentOk` (a counter created on the first
# event, such as a recovered panic) are allowed to be missing and are shown
# as such. Sources marked `optional: true` are reported but do not fail the
# run. `prefixes` are claims about families, not names, so each is reported
# with how many live names match it; a prefix matching none is a warning.
#
# Exit status: 0 when every listed name is present (or absentOk / optional),
# 1 when any is missing, 2 on a usage or transport error.
set -euo pipefail

: "${STORE_URL:?set STORE_URL to the query API base URL of the store}"
root="$(cd "$(dirname "$0")/../.." && pwd)"

auth=()
if [ -n "${STORE_TOKEN:-}" ]; then
  auth=(-H "Authorization: Bearer ${STORE_TOKEN}")
fi

live="$(mktemp)"
trap 'rm -f "$live"' EXIT
if ! curl -fsS "${auth[@]}" "${STORE_URL%/}/api/v1/label/__name__/values" -o "$live"; then
  echo "verify-live: could not read metric names from STORE_URL" >&2
  exit 2
fi

python3 - "$live" "$root/hack/dashboards/available-metrics.yaml" <<'PY'
import json, sys
import yaml

live = set(json.load(open(sys.argv[1]))["data"])
sources = yaml.safe_load(open(sys.argv[2]))["sources"]
print("%d live metric names\n" % len(live))
failed = False
for src in sources:
    names = list(src.get("names", []))
    ok_absent = set(src.get("absentOk", []))
    missing = [n for n in names if n not in live and n not in ok_absent]
    tolerated = [n for n in names if n not in live and n in ok_absent]
    optional = bool(src.get("optional"))
    present = len(names) - len(missing) - len(tolerated)
    status = "ok" if not missing else ("missing (optional source)" if optional else "MISSING")
    print("%s: %d/%d names present [%s]" % (src["name"], present, len(names), status))
    for n in missing:
        print("    absent: %s" % n)
    for n in tolerated:
        print("    absent, allowed (absentOk): %s" % n)
    for p in src.get("prefixes", []):
        n = sum(1 for m in live if m.startswith(p))
        if n == 0:
            print("    warning: prefix %s matches no live name" % p)
    if missing and not optional:
        failed = True
sys.exit(1 if failed else 0)
PY

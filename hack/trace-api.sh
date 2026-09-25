#!/usr/bin/env bash
# Probe a VictoriaTraces store for the Jaeger and Tempo endpoints a
# Grafana datasource calls, and print which of them it implements.
#
# The reason this exists: the store answers BOTH dialects on
# `/select/jaeger/*` and `/select/tempo/*`, and implements neither
# completely. An endpoint it does not implement answers 400 with
# `unsupported path requested`, which a datasource reports as a failed
# query rather than as a missing feature — so the symptom of picking the
# wrong dialect is "traces do not work", with nothing naming the cause.
#
# docs/safety.md carries the measurement this script produced. Re-run it
# against a new store version rather than trusting that table: it is a
# snapshot of somebody else's software.
#
# Usage:
#   kubectl port-forward -n <ns> svc/<vt-single-service> 10428:10428 &
#   STORE=http://127.0.0.1:10428 SERVICE=<a service in the store> \
#     [USER=... PASS=...] hack/trace-api.sh
set -uo pipefail

store=${STORE:-http://127.0.0.1:10428}
service=${SERVICE:-}
auth=()
[ -n "${USER:-}" ] && auth=(-u "${USER}:${PASS:-}")

if [ -z "$service" ]; then
  service=$(curl -s -m 10 "${auth[@]}" "$store/select/jaeger/api/services" |
    sed -n 's/.*"data":\["\([^"]*\)".*/\1/p')
  [ -z "$service" ] && {
    echo "no service found in $store and SERVICE is unset; the store may be empty" >&2
    exit 1
  }
  echo "# using service: $service"
fi

now=$(date +%s)000
day=86400000

probe() { # dialect path
  code=$(curl -s -m 12 -o /dev/null -w '%{http_code}' "${auth[@]}" "$store$2")
  case "$code" in
    200) verdict="ok" ;;
    400) verdict="UNSUPPORTED" ;;
    *)   verdict="http $code" ;;
  esac
  printf '  %-9s %-46s %s\n' "$1" "${2#/select/*/}" "$verdict"
}

echo "jaeger — the dialect this chart's default datasource uses"
probe jaeger "/select/jaeger/api/services"
probe jaeger "/select/jaeger/api/services/$service/operations"
probe jaeger "/select/jaeger/api/traces?service=$service&limit=1"
probe jaeger "/select/jaeger/api/dependencies?endTs=$now&lookback=$day"
# The flat operations form. Jaeger's own UI moved to it; Grafana's
# datasource has not, which is the only reason this is not a problem.
probe jaeger "/select/jaeger/api/operations?service=$service"

echo "tempo — admitted by the read route, implemented in part"
probe tempo "/select/tempo/api/echo"
probe tempo "/select/tempo/api/search?limit=1"
probe tempo "/select/tempo/api/v2/search/tags"
probe tempo "/select/tempo/api/search/tags"
probe tempo "/select/tempo/api/status/buildinfo"

#!/usr/bin/env bash
# Negative fixtures, checked for the right reason, not just a non-zero
# exit.
#
# `helm template invalid charts/<chart> -f <fixture>` failing is not
# enough: charts/observability-stack had an early, unconditional refusal
# (`alertmanager.enabled` true with no `notifications`) that fired first
# on any fixture that did not configure one, whatever that fixture was
# actually testing. 12 of 38 fixtures passed this way — each one exiting
# non-zero, none of them exercising the check its name promised. A
# refusal under it could have been deleted outright and every fixture
# named for it would still have "passed".
#
# So every fixture under tests/invalid/<chart>/ starts with a leading
# comment line:
#
#   # expect: <distinctive substring of the refusal's own message>
#
# kept in the fixture itself so the two cannot drift apart in separate
# files. This recipe requires a non-zero exit AND that substring in the
# combined output — stderr for a `fail` in a template, or the JSON
# schema's own "- at '<path>': ..." line for a fixture the schema
# rejects before any template runs. A fixture with no `# expect:` line
# fails too, on purpose: the check that would have caught the twelve
# above must also catch the next fixture added without one.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
fail=0
total=0

for values in "$root"/tests/invalid/*/*.yaml; do
  chart="$(basename "$(dirname "$values")")"
  fixture="$(basename "$values" .yaml)"
  total=$((total + 1))

  expect="$(sed -n '1{/^# expect: /{s/^# expect: //p}}' "$values")"
  if [ -z "$expect" ]; then
    echo "NO 'expect' DECLARATION: tests/invalid/$chart/$fixture.yaml — add a leading '# expect: <distinctive substring of the refusal it tests>' line" >&2
    fail=1
    continue
  fi

  set +e
  out="$(helm template invalid "$root/charts/$chart" -f "$values" 2>&1)"
  rc=$?
  set -e

  if [ "$rc" -eq 0 ]; then
    echo "RENDERED BUT SHOULD HAVE FAILED: tests/invalid/$chart/$fixture.yaml" >&2
    fail=1
    continue
  fi

  if ! grep -qF -- "$expect" <<<"$out"; then
    echo "WRONG REFUSAL: tests/invalid/$chart/$fixture.yaml" >&2
    echo "  expected output to contain: $expect" >&2
    echo "  but it failed with:" >&2
    echo "$out" | sed 's/^/    /' >&2
    fail=1
    continue
  fi
done

[ "$fail" = 0 ] && echo "$total negative fixtures: each failed for the refusal it declares"
exit $fail

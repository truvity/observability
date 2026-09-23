# Development commands. Everything CI runs is a recipe here — the shared
# check workflow (truvity/ci-workflows) runs each one as its own job, so a
# laptop and CI run the same thing.

charts := "platform-alerts"

# The parent workspace would otherwise interfere with this standalone
# module.
export GOWORK := "off"

# Lint every chart, and prove every refusal still refuses.
#
# The schema is part of the lint: an unknown key must fail the render, not
# be silently ignored — in an alerting chart a silently ignored key is a
# rule that never fires. Everything the schema cannot express (a group
# enabled with nothing to watch, a store the deadman cannot see) is a
# render-time `fail`, and every one of those has a fixture under
# tests/invalid/<chart>/ that must fail.
lint:
    #!/usr/bin/env bash
    set -euo pipefail
    golangci-lint run ./...
    for chart in {{ charts }}; do
      helm lint "charts/$chart" --values tests/cases/"$chart"/minimal/values.yaml
      # An unknown top-level key must fail the render. Not `! helm
      # template ...`: bash's `set -e` ignores a command negated with `!`,
      # so such a probe could never fail the recipe.
      if helm template x "charts/$chart" \
           --values tests/cases/"$chart"/minimal/values.yaml \
           --set bogusKey=1 >/dev/null 2>&1; then
        echo "$chart: an unknown key rendered" >&2
        exit 1
      fi
      # Every negative fixture must fail; one that renders is a hole in the
      # validation, and a hole in an alerting chart is silent by nature.
      for values in tests/invalid/"$chart"/*.yaml; do
        if helm template invalid "charts/$chart" -f "$values" >/dev/null 2>&1; then
          echo "RENDERED BUT SHOULD HAVE FAILED: $values" >&2
          exit 1
        fi
      done
      echo "$chart: schema and $(ls tests/invalid/"$chart"/*.yaml | wc -l | tr -d ' ') negative fixtures OK"
    done

# Golden renders, then the Go library's own tests.
#
# One recipe, because they answer the same question from two sides: the
# goldens prove the charts render what we think, and the library tests
# prove a grant means what it says before it ever reaches a chart.
test:
    hack/golden.sh
    go test ./... -coverprofile=coverage.out

# Regenerate the golden renders — review the diff before committing.
golden:
    hack/golden.sh update

# The reason this repository can be public. Runs in CI as its own job.
leak-canary:
    hack/leak-canary.sh

# Package every chart locally (the release workflow stamps the version
# from the tag).
package:
    #!/usr/bin/env bash
    set -euo pipefail
    for chart in {{ charts }}; do helm package "charts/$chart" --destination dist/; done

# Go vulnerability check. Deliberately NOT in `check` and not a required
# context: a standard-library advisory with no released fix would
# otherwise wedge every pull request on a finding nobody can act on.
vuln:
    govulncheck ./...

# Format Go files.
fmt:
    golangci-lint fmt ./...

# Everything CI runs on a pull request.
check: lint test leak-canary

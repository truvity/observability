# Development commands. Everything CI runs is a recipe here — the shared
# check workflow (truvity/ci-workflows) runs each one as its own job, so a
# laptop and CI run the same thing.

charts := "observability-crds observability-emitters observability-stack platform-alerts alert-ingress"

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
      # A chart renders with WHATEVER archive is in its charts/ directory:
      # move the version in Chart.yaml and leave the vendored archive
      # behind, and helm installs the old CRDs while every check passes and
      # the golden does not move. `helm dependency list` is where that
      # shows, and it exits 0 either way, so the STATUS column is read.
      if helm dependency list "charts/$chart" \
           | tail -n +2 | grep -v '^[[:space:]]*$' | grep -qv 'ok[[:space:]]*$'; then
        helm dependency list "charts/$chart" >&2
        echo "$chart: a declared dependency is missing or is the wrong version — run 'just crds' for observability-crds, 'just vendor $chart' otherwise" >&2
        exit 1
      fi
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
      echo "$chart: schema OK"
    done
    # Every negative fixture must fail, AND for the refusal it is named
    # for — an exit code alone cannot tell a fixture guarding its own
    # refusal from one an earlier, unrelated refusal preempted. See
    # hack/lint-fixtures.sh and docs/safety.md.
    hack/lint-fixtures.sh

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

# Re-fetch observability-crds from its two pinned upstreams. Needs network
# and a GITHUB_TOKEN; deliberately NOT part of `check`, for the same reason
# `golden` is not: it writes what the checks then read.
crds:
    hack/crds.sh

# Re-vendor one chart's pinned dependencies into its charts/ directory,
# after moving a version in its Chart.yaml. The archives are committed on
# purpose: a render that needs the network is a render that differs
# depending on when it runs, and `just lint` reads the STATUS column to
# prove the archive and the pin still agree.
vendor chart:
    helm dependency update charts/{{ chart }}

# The reason this repository can be public. Runs in CI as its own job.
leak-canary:
    hack/leak-canary.sh

# Apply every golden to a real API server and require it to be accepted.
#
# Needs Docker: it runs a throwaway kind cluster. Deliberately NOT part of
# `check`, which must stay a thing a laptop can run in seconds without a
# container runtime -- but it IS a CI job, because the defects it catches
# are the ones that reach a cluster otherwise. `KEEP=1 just apply` leaves
# the cluster up.
apply:
    hack/apply.sh

# Install the release and require the OPERATOR to accept it.
#
# `apply` proves the API server accepts every object; this proves the
# thing that reconciles them does. They are not the same question -- 0.3.1
# was an object the API server took happily and the operator then refused,
# leaving no Deployment and no read path.
#
# Needs Docker, and more inotify instances than a laptop already running
# another kind cluster tends to have: kube-proxy dies with "too many open
# files" and everything downstream looks like a chart bug. kind's own docs
# say to raise fs.inotify.max_user_instances.
reconcile:
    hack/reconcile.sh

# Run setup.sh — the release asset a box's cloud-init fetches, verifies
# by checksum and executes, see pkg/statusbox — against a fixture, and
# require every instance it starts to answer /health.
#
# Deliberately NOT part of `check`: it needs root (apt-get, systemctl,
# and it writes /opt/statusbox and /data for real) the same way `apply`
# needs Docker, so a laptop run is destructive in a way `check` must
# never be. It is a CI job for the reason the header of hack/statusbox-ci.sh
# gives at length: the first run of setup.sh must not be on the box, on
# a bad day.
statusbox:
    hack/statusbox-ci.sh

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

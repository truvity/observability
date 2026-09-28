# Development commands. Everything CI runs is a recipe here — the shared
# check workflow (truvity/ci-workflows) runs each one as its own job, so a
# laptop and CI run the same thing.

charts := "observability-crds observability-emitters observability-stack platform-alerts alert-ingress observability-dashboards"

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

# Re-fetch charts/observability-dashboards' generic dashboard set from its
# pinned upstreams (hack/dashboards/sources.yaml). Needs network;
# deliberately NOT part of `check`, for the same reason `crds` is not: it
# writes what the checks then read. Run `just golden` and `just
# dashboard-lint` after and read both diffs before committing.
dashboards:
    hack/dashboards.sh

# The six-rule contract docs/dashboards.md defines, against one or more
# dashboard JSON files. With no arguments, lints this chart's own shipped
# set — the check CI runs. An estate runs the identical command on its
# own dashboards, inside or outside this repository:
#
#   just dashboard-lint path/to/mine.json
#
# The rule that matters most: a panel pinned to one datasource UID is how
# a fleet dashboard silently becomes a one-install dashboard.
dashboard-lint *files:
    #!/usr/bin/env bash
    set -euo pipefail
    files=({{ files }})
    if [ ${#files[@]} -eq 0 ]; then
      files=(charts/observability-dashboards/dashboards/*.json)
    fi
    go run ./cmd/dashboardlint "${files[@]}"

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

# Run setup.sh's install_container_runtime function ALONE, inside a
# plain debian:12 container — the actual OS the box boots, which
# `statusbox` above cannot exercise: its own job runs the whole,
# unmodified script on an Ubuntu runner, so an apt package that exists
# only on Ubuntu (docker-compose-v2, until 0.7.4) passed there while
# failing on every real box. See hack/statusbox-debian-ci.sh.
#
# Needs Docker, the same as `apply`/`reconcile`/`statusbox`; deliberately
# NOT part of `check` for the same reason.
statusbox-debian:
    hack/statusbox-debian-ci.sh

# REAL proof, in Docker, that statusbox.Args.TrustedCAs actually makes
# Gatus trust a private root: a throwaway CA and server certificate, a
# tiny HTTPS server presenting it, and the real twinproduction/gatus
# image probing it with and without the CA mounted — plus a public HTTPS
# probe in the SAME with-CA container, proving SSL_CERT_DIR adds to the
# image's own trust bundle rather than replacing it. See
# hack/statusbox-ca-proof.sh's own header for why this exists alongside
# `statusbox` above (which proves setup.sh's OWN logic, never whether
# Gatus's TLS stack actually behaves differently because of it).
#
# Needs Docker, openssl, curl and jq; deliberately NOT part of `check`
# or CI, the same reason `statusbox`/`statusbox-debian` are not: a
# one-off, run-by-hand proof for this feature, not a regression gate.
statusbox-ca-proof:
    hack/statusbox-ca-proof.sh

# REAL proof, against a real victoria-metrics binary, that
# `charts/platform-alerts`' BackupJobFailed rule now fires on only the
# NEWEST Job of each CronJob — the fix for a CronJob's
# failedJobsHistoryLimit keeping a failed Job around long after a later
# run succeeded, so the old expression fired on it forever.
# tests/backupjobfailed_test.go pins the rendered MetricsQL string
# against edits; it cannot evaluate the JOIN that string performs — no
# vendored MetricsQL engine in this repository can, see
# tests/kargo_alerts_test.go's own header — so this is the other half:
# synthetic kube-state-metrics series for four CronJobs imported into a
# real victoria-metrics, queried with both the OLD and the NEW
# expression. See hack/platform-alerts-newest-job-proof.sh's own header.
#
# Needs Docker, curl and python3 (with PyYAML); deliberately NOT part of
# `check` or CI, the same reason `statusbox-ca-proof` above is not: a
# one-off, run-by-hand proof for this fix, not a regression gate.
platform-alerts-newest-job-proof:
    hack/platform-alerts-newest-job-proof.sh

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
check: lint test leak-canary dashboard-lint

# Development commands. Everything CI runs is a recipe here — the shared
# check workflow (truvity/ci-workflows) runs each one as its own job, so a
# laptop and CI run the same thing.

charts := "observability-crds observability-emitters observability-stack platform-alerts alert-ingress observability-dashboards observability-grafana observability-mcp observability-rum observability-projects observability-portal observability-alerting"

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
    GOTOOLCHAIN=local golangci-lint run ./...
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
    # A chart's own image.repository default naming an image the release
    # workflow does not build (.goreleaser.yaml's kos:) renders, installs
    # and pulls nothing — see hack/check-image-refs.py's own header.
    python3 hack/check-image-refs.py

# Golden renders, then the Go library's own tests.
#
# One recipe, because they answer the same question from two sides: the
# goldens prove the charts render what we think, and the library tests
# prove a grant means what it says before it ever reaches a chart.
test:
    hack/golden.sh
    go test ./... -coverprofile=coverage.out

# The portal's single-page app (apps/portal): install exactly what the
# lockfile says, type-check, run its tests (the published schema, the
# merge of the built-in catalog with /config/portal.json, the rendered
# page), and build the static site the image copies. The release job runs
# the same install and build as goreleaser hooks.
portal:
    yarn --cwd apps/portal install --immutable
    yarn --cwd apps/portal typecheck
    yarn --cwd apps/portal test
    yarn --cwd apps/portal build

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
      files=(charts/observability-dashboards/dashboards/*.json charts/observability-rum/dashboards/*.json)
    fi
    go run ./cmd/dashboardlint "${files[@]}"

# Parse every operational dashboard's queries on a REAL VictoriaMetrics (the
# version charts/observability-stack pins), and hold them to the metrics a
# store actually has (hack/dashboards/available-metrics.yaml). The same
# tests run under `just test`; here a missing Docker is a failure, not a
# skip. Needs Docker and network for the image.
dashboard-queries:
    DASHBOARD_VM=require go test ./tests -run 'AvailableMetrics|OperationalDashboard|TheAllowList' -v

# Parse every VMRule expression on the REAL VictoriaMetrics / VictoriaLogs
# binaries, then hold the parsed rules to the semantic checks of
# pkg/rulecheck/lint.go (aggregations keep the cluster label, every absent()
# carries the per-cluster guard, no per-pod heartbeat, every self-alert
# source is watched by SelfAlertSourceAbsent). Parsing proves a rule is
# valid; the checks prove it is not one of the shapes releases 0.43.3,
# 0.44.1, 0.45.1 and 0.46.0 had to fix. Intentional exceptions live in
# DefaultAllowlist, each with its reason. `-require-source-absent` makes a
# render without SelfAlertSourceAbsent a failure too (an estate that enables
# it everywhere). The parse half: at the versions charts/observability-stack pins (read from its
# vendored archives, so never written down twice). With no arguments, checks
# the golden renders -- every rule the charts ship: platform-alerts, the
# stack's own self-alerts and Watchdog, alert-ingress's. `just test`
# already proves each golden equals what `helm template` renders, so these
# ARE the rendered rules. An expression neither parser accepts is a VMRule
# the operator's admission webhook refuses, and a sync that never finishes;
# every other check passes on it, because text is valid YAML whatever the
# expression inside says. An estate runs the same command on its own
# manifests:
#
#   just rulecheck path/to/rendered.yaml some/dir
#
# Downloads the two release binaries from github.com (sha256-verified
# against the release's checksum file) on first use; RULECHECK_BIN_DIR
# names a directory already holding victoria-metrics-prod and
# victoria-logs-prod for offline use. Vendored default rules the upstream
# stack chart's sync job fetches at install time are not in any render and
# so are not checked here.
rulecheck *paths:
    #!/usr/bin/env bash
    set -euo pipefail
    paths=({{ paths }})
    if [ ${#paths[@]} -eq 0 ]; then
      paths=(tests/golden)
    fi
    go run ./cmd/rulecheck "${paths[@]}"

# Per-cluster rule coverage: which groups and rules each cluster does NOT
# alert on, against every group and rule any of them renders. Here a
# "cluster" is one golden render (tests/golden/<chart>/<case>.yaml), so the
# report shows what each configuration leaves switched off; an estate runs
# `rulecheck coverage` on its own per-cluster renders:
#
#   go run ./cmd/rulecheck coverage prod=rendered/prod.yaml dev=rendered/dev/
#
# Writes <out>/coverage.json and <out>/coverage.md (CI uploads both as the
# `rulecheck-coverage` artifact and prints the markdown in the job
# summary). Needs no parser binaries and no network.
rulecheck-coverage out="rulecheck-coverage":
    #!/usr/bin/env bash
    set -euo pipefail
    mkdir -p {{ out }}
    go run ./cmd/rulecheck coverage -format json -o {{ out }}/coverage.json tests/golden
    go run ./cmd/rulecheck coverage -format markdown -o {{ out }}/coverage.md tests/golden
    if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
      head -c 900000 {{ out }}/coverage.md >> "$GITHUB_STEP_SUMMARY"
    fi

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

# A change to an EXISTING golden is a default-render change and must be
# declared with a `**Behaviour change` line in CHANGELOG.md's newest entry
# (adding a new golden is fine). BASE is the ref to compare against: CI
# passes the pull request's base, locally it is origin/master.
default-change-guard base="origin/master":
    hack/default-change-guard.sh {{ base }}

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

# Run the EC2 backend's setup script (pkg/statusbox/ec2/setup.sh, rendered
# into the instance's user-data) inside an amazonlinux:2023 container: the
# pinned Litestream and cloudflared downloads, the units and configs, a real
# litestream replicate/restore round trip, and the boot phase's ordering (a
# warm-pool boot restores and starts nothing) with systemctl, aws and the
# metadata service shimmed. See hack/statusbox-ec2-ci.sh.
#
# Needs Docker; deliberately NOT part of `check`.
statusbox-ec2:
    hack/statusbox-ec2-ci.sh

# REAL proof, in Docker, that pkg/statusbox's RenderGatus (0.9.0, item 5
# of "consumer simplification") produces a Config the real
# twinproduction/gatus:v5.37.0 image actually boots: a representative
# multi-company Catalogue, every one of its seven endpoints confirmed
# live in Gatus's own /api/v1/endpoints/statuses, and /health answering
# UP. gatus_internal_test.go already proves the STRUCTURE with no
# network and no Docker; this proves the bytes are something Gatus
# itself accepts.
#
# Needs Docker, curl, jq and go; deliberately NOT part of `check` or CI,
# the same reason `statusbox-ec2` above is not.
gatus-boot-proof:
    hack/gatus-boot-proof.sh

# REAL proof, in Docker, of the deadman group (Catalogue.Deadman) on the
# real Gatus image against a mock read API and a mock chat API: three
# checks, a page after two failures with the bot token in the header,
# resolved on recovery. About ten minutes. Needs Docker, curl, python3 and
# go; deliberately NOT part of `check` or CI.
gatus-deadman-proof:
    hack/gatus-deadman-proof.sh

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
# `check` or CI, the same reason `statusbox-ec2` above is not: a
# one-off, run-by-hand proof for this fix, not a regression gate.
platform-alerts-newest-job-proof:
    hack/platform-alerts-newest-job-proof.sh

# REAL proof, against a real victoria-metrics binary, that
# `charts/platform-alerts`' CronJobNotSucceeding and BackupJobFailed skip
# a SUSPENDED CronJob (0.11.0, `groups.backups.ignoreSuspended`), still
# fire on an active one, and still fire when kube_cronjob_spec_suspend is
# absent — and that the 0.10.0 expressions do not skip it. The companion
# of `platform-alerts-newest-job-proof` above, for the same reason: no
# engine in this repository can evaluate the join. See
# hack/platform-alerts-suspended-proof.sh's own header.
#
# Needs Docker, curl, helm and python3 (with PyYAML); deliberately NOT
# part of `check` or CI, the same reason as the newest-job proof.
platform-alerts-suspended-proof:
    hack/platform-alerts-suspended-proof.sh

# REAL proof, against a real victoria-metrics binary, that
# `charts/platform-alerts`' joins are cluster-aware: two clusters sharing a
# namespace, Job and PVC name make the 0.11.2 expressions fail with a
# duplicate-series 422 (or mask each other), and the current ones fire per
# cluster, and still fire on a single-cluster store with no cluster label.
# See hack/platform-alerts-cluster-proof.sh's own header.
#
# Needs Docker, curl, helm and python3 (with PyYAML); deliberately NOT
# part of `check` or CI, like the proofs above.
platform-alerts-cluster-proof:
    hack/platform-alerts-cluster-proof.sh

# REAL proof, against a real victoria-metrics binary, that every `*Absent`
# guard of platform-alerts is per cluster: series present on clusters a and b
# stays quiet, series gone on b only fires for b, series that never existed
# fires without a cluster label. See hack/platform-alerts-absent-proof.sh's
# own header. Needs Docker, curl, python3 (PyYAML) and helm; not part of
# `check` or CI, like the proofs above.
platform-alerts-absent-proof:
    hack/platform-alerts-absent-proof.sh

# REAL proof that the vendored k8s-stack's default recording rules carry
# `k8s_cluster_name`: the real sync job image rewrites the fetched rules,
# and on a real victoria-metrics two clusters sharing a namespace and pod
# name get one series each, where upstream's `cluster` form collapses them.
# See hack/k8s-stack-cluster-label-proof.sh's own header.
#
# Needs Docker, curl, helm, python3 (with PyYAML) and network access;
# deliberately NOT part of `check` or CI.
k8s-stack-cluster-label-proof:
    hack/k8s-stack-cluster-label-proof.sh

# REAL proof, against the real node-exporter, vmagent, VictoriaMetrics and
# vmalert binaries, that `nodeExporter.enabled` (0.15.0,
# charts/observability-emitters) stores node-exporter's series as
# `job="node-exporter"` with the cluster label from the chart's own rendered
# relabel steps, and that the k8s-stack's `node.rules` and
# `kube-prometheus-node-recording.rules` (fetched and rewritten by the real
# sync job) record data on them -- and record nothing without the job step.
# See hack/node-exporter-proof.sh's own header.
#
# Needs Docker, curl, helm, python3 (with PyYAML) and network access;
# deliberately NOT part of `check` or CI.
node-exporter-proof:
    hack/node-exporter-proof.sh

# REAL proof, in Docker, of `backup.auth.mode: credentialProcess` end to
# end: MinIO stands in for an S3-compatible store that is not AWS, the
# RENDERED vmbackup command/env/AWS-config this chart produces for that
# mode runs against it for real, a backup lands in MinIO, and vmrestore
# then rebuilds a second, empty VictoriaMetrics from it — queried
# afterward for the series the first one was seeded with. See
# hack/backup-restore-proof.sh's own header, and docs/reference.md's
# "Restore" section, which this script is the proof for.
#
# Needs Docker, curl and python3 (PyYAML); deliberately NOT part of
# `check`, the same reason `statusbox-ec2` above is not: a
# one-off, run-by-hand proof for this feature, not a regression gate.
backup-restore-proof:
    hack/backup-restore-proof.sh

# REAL proof, in Docker, of the logs/traces backup CronJobs (0.11.1 fix)
# end to end: a real victoria-logs and victoria-traces, store auth ON,
# and the pinned, UNMODIFIED `backup.image` (rclone/rclone:1.73.0) —
# busybox wget has no `--user`/`--password` at all, and rclone reads an
# `s3://` destination as a missing remote, so both jobs failed as shipped
# the moment store auth (mandatory) was on. The RENDERED command/env this
# chart now produces runs unmodified against real store binaries and a
# real S3-compatible endpoint (adobe/s3mock — see hack/backup-logs-
# traces-proof.sh's own header for why not minio/minio), and both
# snapshots land there. See hack/backup-logs-traces-proof.sh's own
# header.
#
# Needs Docker and python3 (PyYAML); deliberately NOT part of `check`,
# the same reason `backup-restore-proof` above is not: a one-off,
# run-by-hand proof for this fix, not a regression gate — the golden
# renders (tests/golden/observability-stack/backup-*.yaml) are that.
backup-logs-traces-proof:
    hack/backup-logs-traces-proof.sh

# REAL proof, against the pinned vmagent and VictoriaMetrics single
# binaries, that `metrics.scrape.cadvisorDrop` (0.9.1,
# charts/observability-emitters) drops what CHANGELOG.md's `0.9.1` entry
# says it drops and nothing else: a real vmagent scrapes a fixture
# `/metrics` over a static file-sd target with the chart's own rendered
# metric_relabel_configs, remote-writes to a real VictoriaMetrics single,
# and the store is then queried for which names made it in. See
# hack/cadvisor-churn-drop-proof.sh's own header, and
# tests/cadvisor_churn_drop_test.go for the fast, no-Docker regression
# gate this complements rather than replaces.
#
# Needs Docker, curl, python3 (with PyYAML) and helm; deliberately NOT
# part of `check`, the same reason `statusbox-ec2` above is not: a
# one-off, run-by-hand proof for this feature, not a regression gate.
cadvisor-churn-drop-proof:
    hack/cadvisor-churn-drop-proof.sh

# REAL proof, against the pinned vmagent and VictoriaMetrics single, that the
# kubelet and cadvisor jobs store the `metrics_path` label the kube-prometheus
# dashboards select on, and that the shipped kubelet dashboard's `cluster`
# variable then returns a value (and is empty without the label). See
# hack/metrics-path-proof.sh. Needs Docker, curl, python3 (PyYAML) and helm;
# not part of `check`, like the other proofs.
metrics-path-proof:
    hack/metrics-path-proof.sh

# REAL proof, against the pinned vmauth, VictoriaMetrics and VictoriaLogs,
# that a `tenancy.principals[]` entry with several `groups` reads every
# cluster its grants name, on metrics and logs, and that one principal per
# cluster read only the first. Needs Docker, curl, python3 (PyYAML, PyJWT,
# cryptography) and helm; not part of `check`, like the other proofs. See
# hack/multi-group-reader-proof.sh's own header.
multi-group-reader-proof:
    hack/multi-group-reader-proof.sh

# REAL proof, with the three stock VictoriaMetrics MCP servers built from
# their pinned tags and cmd/mcp-aggregator built from this tree, that every
# tool observability-mcp exposes for a store reaches a path the store's
# vmauth serves, and that the dropped tools do not. Needs go, git, helm,
# python3 (PyYAML) and network access; not part of `check`, like the other
# proofs. See hack/mcp-paths-proof.sh's own header.
mcp-paths-proof:
    hack/mcp-paths-proof.sh

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
check: lint test portal leak-canary dashboard-lint rulecheck

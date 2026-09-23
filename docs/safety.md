# Safety

What can break, what these charts refuse in order to prevent it, and the
failure that earned each rule. Thresholds are stated against a measured
healthy range, because a threshold without one is a guess that will either
never fire or always fire.

## The refusals: `observability-crds`

Fixtures under `tests/invalid/observability-crds/`.

| Refusal | The failure it prevents |
|---|---|
| An unknown key | A misspelled set name leaves a set installed that the consumer believed they had turned off, and two owners then take turns overwriting the same CustomResourceDefinition. |
| Every set disabled | The release installs no CustomResourceDefinition at all and still reports Synced and Healthy. The failure surfaces much later, in the controller that wanted the kind, as an error nobody connects back to this release. The upstream Envoy Gateway CRDs chart defaults both of its sets to false and is exactly this trap. |
| A set that is not a boolean | `victoriaMetrics: "false"` is a non-empty string, which Helm's `if` reads as true: the set the consumer meant to disable installs anyway. |

There is no refusal for a kind that upstream has removed, because a chart
cannot see what a cluster already has. That question is answered by the
inventory at the foot of the render and by `kubectl diff` before the sync;
docs/adoption.md says how.

## The failure this chart's ordering prevents

**A missing CRD does not fail an install. It removes a template.**

The common shape upstream is a monitor template gated on the API surface:

```
{{- if .Capabilities.APIVersions.Has "monitoring.coreos.com/v1" }}
```

When the kind is absent the condition is false, the template renders
nothing, and the release installs successfully. Nothing warns. The operator
who set `serviceMonitor.enabled: true` has a green deployment, no scrape
object, and no metrics — and looks for the fault in the scraper, which is
the one place it is not. Argo CD, external-secrets, Kargo and the Keycloak
operator all ship charts that do this.

That is the whole reason `observability-crds` is applied at a wave ahead of
everything else, and the reason enabling a monitor value before the kinds
exist is a sequencing mistake rather than a matter of taste. Two things
make it survivable:

- **Prefer an upstream mode that fails loudly.** Where a chart offers one,
  use it: external-secrets has `serviceMonitor.renderMode`, whose
  `failIfMissing` turns exactly this silent skip into a refused render.
  `skipIfMissing` is the dangerous default; `alwaysRender` produces an
  object the API server then rejects, which is at least visible.
- **Check for the object, not for the install.** `kubectl get
  servicemonitor -A` after enabling one answers the question a successful
  Helm release does not.

A controller that cached the answer at startup keeps it: see
docs/adoption.md on what to restart after the CRDs first land.

## The refusals: `platform-alerts`

Each of these fails the render, and each has a fixture under
`tests/invalid/platform-alerts/` that must keep failing.

| Refusal | The failure it prevents |
|---|---|
| An unknown key | In an alerting chart a silently ignored key is a rule that never fires. Schema is strict everywhere. |
| Every group disabled | The release installs, reports success, and alerts on nothing. |
| A store-watching group enabled with an empty `stores` | The deadman would watch nothing while appearing installed. |
| A store with no `rowsMetric` | The deadman cannot see that store, but the install looks complete. |
| `minCapacityRatio` above 1 | A mounted filesystem is always somewhat smaller than its claim, so this fires on every healthy volume and is then silenced everywhere. |
| `headroomFactor` of 1 or less | The alert arrives at the moment writes already fail, which is the event it exists to precede. |
| A severity outside `critical`/`warning`/`info` | An alert whose severity has no branch in the routing tree fires into nowhere. |
| One free-space metric without the other | The read-only rule would compare a gauge against nothing. |
| A duration that is not one | vmalert rejects the rule at load, long after the chart reported a successful install. |

## The rules, and the incident behind each

### The write-path deadman

A metrics store went read-only and kept serving reads for twenty minutes
with nothing noticing.

The obvious rule — alert on the store's own read-only flag — **cannot fire
in its own headline case**, because that sample is written into the store
that is refusing writes. Reads keep working when writes do not, so
dashboards stay up and queries answer. Watching the rows counter stop is
the only rule that can see it.

The expression is `(sum(rate(<rowsMetric>[window])) or vector(0)) == 0`.
The `or vector(0)` is load-bearing: it makes the rule fire for both shapes
of the failure, the counter that stops advancing and the counter that
disappears because the store is gone. Without it, a store that vanishes
produces an empty result, and an empty result is not an alert.

### A CronJob that stopped being scheduled

A backup silently stopped for two days. Nothing failed, because nothing
ran: a CronJob that is suspended, deleted, or whose controller is not
creating Jobs produces no failed Job at all. "No failures" is not
"working". Only the age of the last **success** can see it, which is why
`CronJobNotSucceeding` is the rule that matters and `BackupJobFailed` is
the companion.

Measured healthy range for a daily job: the age resets below 24h on every
run. The default threshold of 26h is that plus enough slack for a slow run
or a retry.

### A volume that was never mounted

A pod reported `1/1 Running` while writing to the node's root filesystem,
because its real volume was never mounted. Everything looked healthy and
the data was not where anyone thought.

The tell is capacity: a mounted filesystem is always *somewhat* smaller
than the claim that satisfied it — measured healthy range 0.95 to 0.99 of
the claim, the difference being filesystem overhead — but it is never a
fraction of it. The default threshold of 0.5 sits roughly two times below
that healthy range, so overhead can never reach it and a wrong mount
always does.

### A store approaching read-only

The threshold comes from the store's **own exported limit**, never from a
constant written into the chart. A constant is how a rule either never
fires (the limit is lower than you assumed) or always fires (it is
higher). A store that exports no such limit gets no rule, which is the
honest outcome rather than a rule built on a guess.

## The refusals: `observability-stack`

Seventeen, each with a fixture under `tests/invalid/observability-stack/`
that is otherwise valid, so it fails for its one reason and no other.

| Refusal | The failure it prevents |
|---|---|
| An unknown key | A setting that does not apply: a retention that never changed, a filter that never narrowed, a secret name nothing reads. The install succeeds either way. |
| A retention without a unit | Every store in this family reads a bare number as MONTHS. `90` is seven and a half years on a volume sized for three months, and the first anyone hears of it is the volume filling up a quarter later. |
| Both disk guards on one store | `-retention.maxDiskUsagePercent` and `-retention.maxDiskSpaceUsageBytes` are mutually exclusive: the binary calls Fatal and never starts. A values file that looks more careful than the correct one produces a store that does not come up. |
| A `-retention.max*` flag on the metrics store | Single-node VictoriaMetrics has neither flag — they exist only on the log and trace stores — and refuses to start on an unknown one. Its guard is `-storage.minFreeDiskSpaceBytes`. |
| A fractional CPU | The binaries size their thread pool from the cgroup quota and round DOWN, so `1500m` buys one thread and pays for 1.5. Nothing reports it but a log line at startup. |
| Requests that differ from limits | A Burstable pod is evicted before a Guaranteed one — at the moment of node pressure, which is when a store matters most. |
| An `enterprise` image tag | An Enterprise image without a licence key RUNS, refusing only the Enterprise features, so the estate is in breach of the vendor's terms with everything apparently healthy. |
| A `-license` or `-licenseFile` flag | The same boundary from the other side. This chart wraps the community edition; an install that needs a licence flag is an install this chart is the wrong shape for. |
| A vmauth tag below v1.152.0 | `default_vm_access_claim` arrived in v1.147.0, and v1.147.0–v1.151.x matched `match_claims` values UNANCHORED (GHSA-f99m-22fh-qw96) — `admin` also matched `not-admin-really`, in the exact mechanism that decides which user a token is. |
| An `*AuthKey` flag on a store | An authKey does not add to `-httpAuth.*`, it REPLACES it for those endpoints: basic auth is never checked, and the key travels in the query string and therefore into every access log. |
| A tenant or environment name outside the plain-name shape | The name is interpolated into a filter expression. `dms\|prod` does not look odd in the rendered filter — it grants a second tenant. Refused, never escaped. |
| A mirror that disagrees with `interval` | Deduplication keeps one sample per window: wider than the scrape interval it discards good samples, narrower it deduplicates nothing. Neither announces itself. |
| A store whose credentials come from another Secret | The proxy authenticates to the stores with `storeCredentials`; a store reading a different Secret answers every query with 401, and the proxy is the only thing that ever sees it. |
| `ha: true` with fewer than two zones | No store here replicates across a zone. An install labelled highly available with one zone is the single-zone install with a label that stops anyone looking at it again. |
| vmalert with no notifier at all | Every rule evaluates and the result goes nowhere, which is indistinguishable from an estate with no problems. |
| A Grafana datasource without `oauthPassThru` | Every query reaches the proxy as GRAFANA's identity rather than the signed-in person's, so the proxy scopes nothing and a viewer sees every tenant. It looks exactly like a working dashboard. |
| A Grafana datasource without a `version` | With more than one replica Grafana only updates a provisioned datasource whose version is at least the stored one, so an edit without a bump lands on a fresh install and nowhere else. |
| Grafana with alerting enabled | A second alerting engine, with its own rules, silences and notification policies: a second place to look at three in the morning, and the one nobody remembers. |
| Grafana without an admin Secret | The Grafana chart then generates a random admin password on every render: `helm upgrade` rotates it silently, and the release's manifest differs from itself when nothing changed. |
| Grafana with `use_refresh_token` off, `role_attribute_strict` off, `locking_attempt_timeout_sec` outside 60–300, or a dashboard `updateIntervalSeconds` of 10 or less | Four defaults that leave a Grafana which looks fine: a session that outlives its token and 401s on every query, an unmapped person given the default role, a second replica crash-looping through a database migration, and dashboards that never update because a ConfigMap projection is a symlink swap that fires no watch event. |
| A backup with no destination or no credentials | It runs, finds nothing to do and reports success. |

### Why there is no deny rule in the proxy's configuration

`/internal/*` must not be reachable through the proxy: those endpoints have
their own query-string auth keys which OVERRIDE `-httpAuth.*` rather than
adding to it, so a route to them is a route around the stores' own
authentication.

vmauth grew a `deny_paths` option in v1.152.0, but **the operator's VMUser
CRD does not expose it**, so a chart that renders VMUsers cannot write one.
The deny is therefore structural: every route is an explicit list of named
endpoints, never a prefix — `/prometheus/.*` also matches
`/prometheus/api/v1/write` and `delete_series`, and the operator's own
default for a `targetRef` without `paths` is `/.*`. The chart checks every
route it renders against `vmauth.deniedPaths` and refuses a match, and
`pkg/tenancy` carries the same lists with a test that no read route admits
a write.

The other half of that defence is at the stores: no `*AuthKey` flag is set
on any of them, so `/internal/*` stays behind each store's own
`-httpAuth.*` — and the chart refuses one being added.

### Why the backups look the way they do

Three stores, three mechanisms, and the only thing they share is the rule
that a job which finds an empty source must fail loudly. `rclone sync` from
an empty source deletes the destination and exits zero: a backup that finds
nothing reports success and destroys the copy you had. That rule used to be
a convention this repository could only write down; in this chart it is
code, and `CronJobNotSucceeding` in `platform-alerts` is what notices when
it fires.

The trace store's backup is the vendor's documented sync-detach-sync-attach
procedure rather than a snapshot. The binary does carry the same snapshot
endpoints as the log store, but they are undocumented for it, and a backup
built on an endpoint the vendor has not documented is a backup that can
stop working in a patch release.

## The Enterprise boundary

The VictoriaMetrics family ships a community edition (Apache 2.0) and an
Enterprise edition whose binaries require a licence key. A chart that
quietly pulled an Enterprise image, or rendered an Enterprise-only flag,
would put its consumer in breach of the vendor's terms while everything
still ran — so the boundary is a refusal, not a note.

Nothing in this repository uses: downsampling, multiple retentions or
retention filters, vmstorage auto-discovery, `vmbackupmanager`,
`vmgateway`, per-tenant or query statistics, automatic TLS issuing, mTLS
between components or as a routing key, IP filters in vmauth, vmalert
multitenancy, rules read from object storage, Kafka or Pub/Sub
integrations, or FIPS builds. The stack chart refuses an image tag
containing `enterprise` and any `-license` flag, each with a fixture under
`tests/invalid/observability-stack/`.

What the design does rely on — vmauth's JWT verification, OIDC discovery,
claim matching and the `vm_access` claim, `vmbackup`, the partition
snapshot API, cardinality limits, deduplication, `-httpAuth` — is all
community. The vmauth version floor is **v1.147.0**, where
`default_vm_access_claim` arrived.

## A convention a chart could not enforce, until it could

**A backup job must refuse an empty source.** `rclone sync` against an
empty source deletes the destination and exits zero — a backup that finds
nothing to back up reports success and destroys the copy you had. The job
must fail loudly instead. No rule here can see that, because from the
outside it is a successful run; it belongs in the job, and it is written
here so the next person to write a backup job reads it.

## What is deliberately not here

No rule fires on a store's read-only flag, on a "backup succeeded" gauge
alone, or on anything else whose absence is indistinguishable from health.
Every rule in this chart can see its own failure case.

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
| A vmauth tag below v1.152.0 | `default_vm_access_claim` arrived in v1.147.0, and every release from v1.138.0 through v1.151.x matched `match_claims` values UNANCHORED (GHSA-f99m-22fh-qw96) — `admin` also matched `not-admin-really`, in the exact mechanism that decides which user a token is. |
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

## The refusals: `observability-emitters`

Twenty-six, each with a fixture under
`tests/invalid/observability-emitters/` that is otherwise valid, so it
fails for its one reason and no other.

They divide into four kinds, and the first kind is the reason the chart
exists.

### Tenancy, which is a security property and not a convenience

| Refusal | The failure it prevents |
|---|---|
| `metrics.spec.overrideHonorLabels: false` | With honor labels not overridden, a label a **target exports itself** wins over the label the agent stamps. Any workload that exposes a `tenant` metric label then chooses its own tenant: it can write into another team's data, or hide its own from the people responsible for it. The render, the sync and the dashboards all look correct. |
| A default scrape class without `attachMetadata.namespace` | A namespace's labels are not part of Kubernetes service discovery unless they are asked for. Without it `__meta_kubernetes_namespace_label_*` is simply absent, every tenancy rule matches nothing, and the whole cluster collapses onto `fallbackTenant` — a single-tenant install rendered to look like a multi-tenant one. |
| `tenancy.env` empty | Telemetry labelled `env=""` matches no grant the proxy injects. It is stored, it is paid for, and it is invisible to everyone who might have acted on it. |
| `tenancy.fallbackTenant` empty | The same, for every namespace nobody has labelled yet — which on any real estate is the namespaces added most recently. |
| `tenancy.namespaceLabels.project` empty | Nothing reads a tenant off a namespace. Everything falls through to the fallback. |
| `project` and `layer` set to the same key | They are consulted in order, so the second never applies and one of the two rules somebody wrote does nothing. |
| A blank or non-plain `tenantLabel` / `envLabel` | The key the emitters stamp is the key `pkg/tenancy` filters on. A mismatch is not an error — it is an empty result, read as "this tenant produces nothing". A name outside the plain shape is interpolated into a filter expression, where `\|` or `.*` widens the grant. |
| `tenantLabel` equal to `envLabel` | One relabel rule overwrites the other, so every series carries one dimension and the grants select on a dimension that is not there. |
| A `fallbackTenant` outside the plain-name shape | `infra\|prod` does not look odd in a rendered filter — it grants a second tenant. Refused, never escaped. |

### Replication, which is the writer's job

| Refusal | The failure it prevents |
|---|---|
| `remoteWrite.shardByURL` | It SPLITS the series between the destinations instead of replicating to all of them, so a zone-redundant pair holds half the data each. Every query still answers and every dashboard still draws, with half of every result missing. |
| An empty destination list on an enabled emitter | The agent collects everything, buffers it, and drops the oldest when the buffer fills — with a Ready pod and a green sync for as long as it takes anyone to notice. |
| A destination name used twice | The names become exporter ids and queue directories. Two destinations sharing one share a queue, and only one is ever written to: an install that looks zone-redundant holds one copy. |
| One URL listed twice | Not redundancy — one store receiving every sample twice, and half the buffer it looked like there was. |
| `selectAllByDefault: false` | With no selectors set that selects NO scrape objects: not a narrower set, none. A component whose `PodMonitor` is ignored looks exactly like a component with nothing wrong. |

### Buffers, which have to be on something

| Refusal | The failure it prevents |
|---|---|
| `statefulMode: false` | The operator renders a Deployment and the persistent queue lands on `/tmp`, which is an emptyDir. Every rollout, eviction and node replacement discards whatever had not been delivered; on a node with an ephemeral-storage budget, the queue counts against it as well. |
| `statefulStorage.emptyDir` | The same loss with an extra step. Checked with `hasKey` and not truthiness, because `emptyDir: {}` is the ordinary way to write one and an empty map is false in a template — a truthiness test would pass exactly the value it exists to refuse. |
| The log agent's queue on an `emptyDir` | Two things live there: the buffer, and the CHECKPOINT recording how far into each container's log file the agent has read. An agent that forgets re-reads every file from the beginning on every rollout and ships every line again. A duplicate is not a gap, so nothing alerts and nothing looks broken — the first sign is the bill. |
| A log destination with no `maxDiskUsagePerURL` | That buffer is on a hostPath, so an uncapped one does not fill a volume: it fills the NODE's disk, and a node under disk pressure evicts every pod on it. A collection agent that can take down the workloads it was watching is worth one required value. |
| `otlp.queue.size` empty | The OTLP sending queues and the remote-write write-ahead log both live on that volume. Without it every replica holds undelivered data in memory and loses it on the next rollout — which is the most likely moment for a store to be briefly unreachable. |

### Stores that are wrecked slowly

| Refusal | The failure it prevents |
|---|---|
| `otlp.streamFields` empty | No `VL-Stream-Fields` header is sent, and with none VictoriaLogs treats EVERY resource attribute as a stream field. An OpenTelemetry SDK's resource carries the pod's UID and its start time, so every restart of every workload mints a stream that is never written to again. The store does not fail; it degrades, over weeks, in a way that reads as growth. |
| A stream field outside the chart's list | A field that changes per request — an address, a user id, a trace id — creates a stream per value. It is the vendor's own named way to wreck this store, and it does not recover on its own. The list is the chart's and not a value, because an allow-list a caller can extend is a comment. |
| The tenant or env key missing from either stream-field list | A stream filter, which is what the proxy injects, only selects on stream fields. Every tenant-scoped log query returns nothing at all. |
| A log write path other than `/insert/native` | The only path that accepts that protocol. A wrong one answers 404, and vlagent treats 404 as a permanent rejection and DROPS the block rather than retrying it. The loss is silent, unrecoverable, and proportional to how long it takes somebody to look. |

### And the rest

| Refusal | The failure it prevents |
|---|---|
| An unknown key | A setting that does not apply: a label never stamped, a destination never written to, a buffer that was never on a volume. The install succeeds either way. |
| Every emitter disabled | A release that collects nothing and reports Synced — one more green application saying the cluster is fine. |
| `writeCredentials.secretName` empty | The stores answer 401 to every write, each agent buffers until full, then drops the oldest, with every pod Ready throughout. |
| A log destination with no credential | The same, for the one emitter whose credential is upstream's shape rather than this chart's. |
| `podMonitor.vm: true` on the log agent | It renders a `VMPodScrape` instead of a `PodMonitor` — see below. |
| A mirror that disagrees | `interval` against the agent's scrape interval, and `tenancy.env` against the log agent's `extraFields`. Helm evaluates a subchart's values before any template runs, so some values have to be written twice; two numbers that are supposed to be equal stop being equal the first time somebody changes one. |
| An `enterprise` image tag, or a licence key | An Enterprise image without a key RUNS, refusing only the Enterprise features, so the estate is in breach with everything apparently healthy. |

## Scrape objects are always the Prometheus Operator kinds

`PodMonitor`, `ServiceMonitor`, `ScrapeConfig`, `Probe` — never
`VMPodScrape` or `VMServiceScrape`, in this repository or in any chart
that authors a scrape object for something this stack collects.

This is not a style rule and it is not about the vendor. It is the only
thing that keeps the agent underneath replaceable: the VictoriaMetrics
operator converts the Prometheus kinds today, and the OpenTelemetry Target
Allocator reads the same objects, so swapping the collection layer is a
values change rather than a rewrite of every chart on the estate. One
object in the other spelling is the first of the ones that follow it, and
by the time there are thirty the swap is a project.

Two consequences in this chart: `victoria-logs-collector.podMonitor.vm` is
refused true, and the metrics agent runs with
`disableSelfServiceScrape: true` so the operator does not quietly create a
`VMServiceScrape` for the agent itself — the chart writes a `PodMonitor`
instead.

## The tenant on the log path is not called `tenant`

This is the sharpest edge in the chart, and it is upstream's rather than
ours.

vlagent has **no way to rename a field**. A namespace label reaches
VictoriaLogs as `kubernetes.namespace_labels.<key>`, and there is no flag,
no header and no ingest pipeline that turns it into `tenant`. So on the
log path the tenancy stream field carries that long name, and the proxy
has to filter on it. A filter on `tenant` against those streams matches
nothing, returns an empty result, and reads as "this namespace writes no
logs".

The chart derives the name and then requires it to appear in the log
agent's own `streamFields`, refusing to render until it does. That is
deliberate: the chart could compute the value silently, but the value has
to travel out of the chart and into the proxy's configuration, and a value
nobody writes is a value nobody carries.

Two things this does **not** fix, stated because discovering them during
an incident is worse:

- **There is no fallback tenant on the log path.** A namespace with no
  project label produces log streams with no tenancy field at all. They
  are stored and are invisible to every tenant-scoped query. The metrics
  and OTLP paths have `tenancy.fallbackTenant`; vlagent can express no
  such thing. Label every namespace.
- **There is no layer fallback either**, for the same reason. Only the
  project label reaches the log store's streams.

`pkg/tenancy` renders one pair of names for both signals, so an estate
using the log path needs a second filter shape on the read side today.
That is a gap in the library, not a decision.

## Why the gateway is a StatefulSet, and why its processors are ordered

Each gateway replica owns a persistent queue, and a ReadWriteOnce volume
cannot be shared by two pods — so a Deployment with more than one replica
is a Deployment where at most one replica has a queue. A queue on an
emptyDir is not a queue: it is a buffer discarded exactly when it is
holding something.

The processor order is a second, quieter thing. `k8sattributes` resolves
the sending pod and copies the namespace's labels onto the resource under
names of this chart's choosing; `transform/tenancy` then writes `tenant`
and `env` from them with `set`, which overwrites whatever an SDK put
there, and deletes the intermediates.

The obvious version — letting `k8sattributes` write `tenant` directly —
is what that avoids. That processor leaves an attribute that is already
present alone, so an SDK that set its own `tenant` would keep it, and the
stored label would be the application's claim about itself. The two-step
renders identically for a well-behaved application and differently for the
one that matters.

One more that is easy to get backwards: the **Prometheus remote-write
exporter has no `sending_queue`** and cannot use the `file_storage`
extension at all. Its durability is its own write-ahead log, configured
separately, on the same volume. A gateway configured as if every exporter
queued the same way has one signal with no durability and nothing saying
so.

## Kubernetes Events come from the gateway, not the log agent

vlagent collects container stdout and stderr and nothing else. Events are
an API object, and they come from the gateway's `k8s_events` receiver —
which is why the gateway is the emitter with a ClusterRole.

Getting this backwards produces an install where Events are simply absent,
with every pod healthy. And because every replica watching the same Events
would ingest all of them, the chart renders a leader-election lease
whenever the receiver is on: only the holder reads.

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
community.

### The vmauth version floor is a security floor

**vmauth v1.152.0, or a patched v1.148 LTS.** The feature floor would be
v1.147.0, where `default_vm_access_claim` arrived. That is not the floor,
because of GHSA-f99m-22fh-qw96, fixed upstream in v1.152.0:

> lib/jwt: fix unanchored `match_claims` regex allowing JWT
> authorization bypass

Every release from v1.138.0, where claim matching was introduced, through
v1.151.x matched `match_claims` values **unanchored**. An entry
configured for `admin` also matched a token claiming `notadmin`.

That matters more here than it would in most places, because
`match_claims` is how this design decides which principal a token is, and
therefore which tenants it may read. A group name that is a substring of
a more privileged one would have selected the more privileged entry. It
is the same failure `Validate` refuses on the tenant-name side — a name
reaching a regular expression without anchors — sitting in the proxy
rather than in this library.

Upstream backported the fix only to the v1.148 LTS line, and the
operator's own default image tag is older than both, so the chart sets
the tag explicitly rather than inheriting it, and refuses anything
lower.

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

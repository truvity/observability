# Safety

What can break, what these charts refuse in order to prevent it, and the
failure that earned each rule. Thresholds are stated against a measured
healthy range, because a threshold without one is a guess that will either
never fire or always fire.

## The negative-fixture suite counted refusals it was not guarding

`just lint` renders every fixture under `tests/invalid/<chart>/` alone
and required only that `helm template` exit non-zero. It never checked
*which* refusal fired — and a fixture that fails is not evidence that it
failed for the reason its name claims.

Measured on `charts/observability-stack`, after an early, unconditional
refusal was added (`alertmanager.enabled` true with no `notifications`
configured — it fires before every other check in `_validate.tpl`), 12
of the 38 fixtures then under `tests/invalid/observability-stack/` hit
that refusal first and never reached the one they were named for:
`backup-without-destination`, `datasource-without-oauth-passthru`,
`grafana-alerting-enabled`, `grafana-replicas-on-sqlite`,
`grafana-without-admin-secret`, `groups-claim-named-aud`,
`label-keys-collide`, `log-fields-collide`,
`merge-query-args-returns-the-filter`, `principals-without-audience`,
`store-without-a-datasource` and `traces-without-unfiltered-optin`. Each
one exited non-zero. None of them exercised the check its own name
promised. Any one of those twelve refusals could have been deleted
outright and the fixture written to guard it would still have "passed"
— a hole in the exact mechanism this repository exists to be, sitting in
that mechanism itself. (Six of the twelve, once past that one, hit a
*second* preemption before their own: the trace store is enabled by
default and every fixture in this chart sets `tenancy.principals`, so
`tenancy.allowUnfilteredTraceReads` fires ahead of the Grafana and
backup checks those six fixtures were actually testing.)

So every fixture under `tests/invalid/<chart>/` now starts with a
leading declaration, kept in the fixture itself rather than a companion
file so the two cannot drift apart in separate diffs:

```
# expect: <distinctive substring of the refusal's own message>
```

`hack/lint-fixtures.sh`, run as part of `just lint`, requires BOTH a
non-zero exit AND that substring in the fixture's combined output — the
`fail` message from a template, or the JSON Schema's own `- at
'<path>': ...` line for a fixture the schema rejects before any
template runs. A fixture with no `# expect:` line fails the recipe too,
by name: the check that caught the twelve above has to also catch the
next fixture added without anyone asking what it proves.

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

Fifty-one, each with a fixture under
`tests/invalid/observability-stack/` that is otherwise valid, so it fails
for its one reason and no other.

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
| `principals` with no `tenancy.audience` | vmauth validates a token's expiry and its issuer and stops: it has no audience option and never inspects `aud`. Without the pin, every reader is selected by its group alone, so any unexpired token the issuer minted is admitted whatever client it was minted for — a token the same person holds for another application reads their namespaces here, with nothing to see: the token verifies, the filters apply. |
| An audience that is not an identifier | A client id carrying a dot or a `|` is escaped, not refused — the issuer assigns it and we do not. What is refused is a value no issuer mints: one with whitespace or a newline in it, which is a value that arrived from the wrong place rather than a client id. |
| `tenancy.claimName` set to `aud` | The groups claim and the audience pin are entries in one `matchClaims` map, so one overwrites the other and the proxy checks either which principal a token is or which client minted it, never both. The rendered manifest looks like one that does both. |
| An `*AuthKey` flag on a store | An authKey does not add to `-httpAuth.*`, it REPLACES it for those endpoints: basic auth is never checked, and the key travels in the query string and therefore into every access log. |
| A cluster or namespace name outside the plain-name shape | The name is interpolated into a filter expression. `example-app\|other-app` does not look odd in the rendered filter — it grants a second namespace. Refused, never escaped. |
| A log key carrying stream-filter syntax | The key is interpolated into the filter exactly as a name is, so a quote or a brace ends the filter early and a second alternative opens beside it — a grant wider than the one somebody wrote. Refused, never escaped. |
| A metrics key that is not a Prometheus label name | `k8s.namespace.name` is the convention's spelling and exactly what a label cannot be: the remote-write exporter writes it with underscores, so a filter naming the dotted form selects a label no series has. Refused by the schema. |
| The two keys of one signal set to the same name | One name for both dimensions is one dimension: the filter selects on one of them and ignores the other, and every grant is wider or narrower than written. |
| A mirror that disagrees with `interval` | Deduplication keeps one sample per window: wider than the scrape interval it discards good samples, narrower it deduplicates nothing. Neither announces itself. |
| A store whose credentials come from another Secret | The proxy authenticates to the stores with `storeCredentials`; a store reading a different Secret answers every query with 401, and the proxy is the only thing that ever sees it. |
| `ha: true` with fewer than two zones | No store here replicates across a zone. An install labelled highly available with one zone is the single-zone install with a label that stops anyone looking at it again. |
| vmalert with no notifier at all | Every rule evaluates and the result goes nowhere, which is indistinguishable from an estate with no problems. |
| `alertmanager.enabled` with no `notifications` configured | The `blackhole` shape this chart exists to retire: every rule evaluates and Alertmanager routes the result to a receiver with no configs, and nothing about the install looks unhealthy. |
| A receiver kind configured with neither `notifications.externalUrl` nor `vmalert.externalUrl` set | Every Grafana link this chart puts in a Slack message is built from one of them; with neither set, every one of them points at nothing a person can open. |
| `notifications.externalUrl` and `vmalert.externalUrl` both set and disagreeing | Two inputs for one fact: the alert's own source link and the Slack message's Grafana link would point at two different places, and nothing notices until somebody clicks the one that is wrong. |
| `notifications.severities.<tier>` missing while a receiver kind is configured | Every route this chart renders falls back to a no-op receiver when nothing more specific matches; a tier with no default reaches nobody and looks routed. |
| `notifications.severities.<tier>.receiver` naming a kind that is not configured | The same failure one level down: a route to `slack` with no `webhookSecret`, or to a name absent from `notifications.webhook`, looks like a route and reaches nobody. |
| `notifications.also[].receiver` naming a webhook that is not configured | The status-page bridge silently does not bridge: the matcher is real, the delivery is not. |
| `notifications.routes[].match` on a key other than `k8s_cluster_name` or `k8s_namespace_name` | The collectors stamp exactly those two dimensions on every alert; a route on anything else — `tenant`, `env`, a team name — matches nothing a rule actually carries. |
| A Slack or webhook receiver with an empty secret name | A receiver that cannot send: the manifest, the route and the schema all agree it exists, and it never delivers anything. |
| `notifications.mode` outside `route` or `evaluate-only` | Refused by the schema, the same as any other enum this chart writes: a typo in the mode name is not a value to guess a fallback for. |
| `notifications.mode: evaluate-only` with `alertmanager.enabled` true (or left at its default) | Evaluate-only means vmalert sends its result to nobody, on purpose; an Alertmanager rendered beside it has nothing to route, so the chart does not render it in this mode and refuses the value that would ask it to. |
| `notifications.mode: evaluate-only` with `alertmanager.notifierUrl` set | The mode renders vmalert's `-notifier.blackhole`, and vmalert itself refuses to start with that flag alongside any notifier URL — `-notifier.url`, `-notifier.config` and `-notifier.blackhole` are mutually exclusive. Refused here, before the two ever reach the same pod. |
| `notifications.mode: evaluate-only` with a receiver, a severity, a route or an `also` bridge configured | Nobody is notified in this mode: a receiver configured beside it looks wired up in the values file and review, and is never reached, because vmalert never sends what it would carry. |
| `alertmanager.watchdog.repeatInterval` not strictly less than `alertmanager.watchdog.timeout` | The heartbeat is due at or after the moment the far end gives up on it, so a single delayed delivery reads as the estate being down when it is not. |
| A Grafana datasource without `oauthPassThru` | Every query reaches the proxy as GRAFANA's identity rather than the signed-in person's, so the proxy scopes nothing and a viewer sees every namespace on every cluster. It looks exactly like a working dashboard. |
| A Grafana datasource without a `version` | With more than one replica Grafana only updates a provisioned datasource whose version is at least the stored one, so an edit without a bump lands on a fresh install and nowhere else. |
| Grafana with alerting enabled | A second alerting engine, with its own rules, silences and notification policies: a second place to look at three in the morning, and the one nobody remembers. |
| Grafana without an admin Secret | The Grafana chart then generates a random admin password on every render: `helm upgrade` rotates it silently, and the release's manifest differs from itself when nothing changed. |
| Grafana with `use_refresh_token` off, `role_attribute_strict` off, `locking_attempt_timeout_sec` outside 60–300, or a dashboard `updateIntervalSeconds` of 10 or less | Four defaults that leave a Grafana which looks fine: a session that outlives its token and 401s on every query, an unmapped person given the default role, a second replica crash-looping through a database migration, and dashboards that never update because a ConfigMap projection is a symlink swap that fires no watch event. |
| `vmauth.extraArgs.mergeQueryArgs` naming `extra_filters` or `extra_stream_filters` | vmauth drops a client query argument that clashes with one the route already set, and that drop is the only thing stopping a reader sending its own filter beside the enforced one. `mergeQueryArgs` exempts an argument from it. vmselect ORs each `extra_filters` as an alternative, so a caller adding an empty one reads every cluster and namespace — with the claim, the route and the rendered filter all still exactly right. |
| A trace store enabled alongside `principals`, without `tenancy.allowUnfilteredTraceReads` | The proxy enforces a grant by substituting it into the route it forwards on, and VictoriaTraces' select APIs accept no query argument to substitute one into. The trace route would sit between two scoped routes, look exactly like them, and scope nothing. |
| A backup with no destination or no credentials | It runs, finds nothing to do and reports success. |
| `selfAlerts.cardinality.hourlyCurrentSeriesMetric` or `hourlyMaxSeriesMetric` (or the daily pair) set without its other half | MetricStoreCardinalityNearLimit / MetricStoreDailyCardinalityNearLimit would compare a gauge against nothing instead of never rendering, the same failure platform-alerts refuses for its own `stores[].freeSpaceMetric`. |
| `selfAlerts.cardinality.ratio` at or above 1 | The alert arrives at the moment the limit has already bitten, not before it. |
| `selfAlerts.diskGuard.<store>` with one free-space metric name and not the other | `{Metric,Log,Trace}StoreDiskNearGuard` for that store would compare a gauge against nothing. |
| `selfAlerts.gateway.queueSizeMetric` or `queueCapacityMetric` set without the other | GatewayQueueFilling would compare a gauge against nothing. |
| `selfAlerts.enabled` true with no metric name set anywhere and no `backup.<store>.enabled` | The rendered `VMRule` would have an empty rule list — coverage that looks like coverage and evaluates nothing. |
| `victoria-logs-single.server.serviceMonitor.basicAuth` or `victoria-traces-single...`'s naming a Secret other than `storeCredentials.secretName` | The store answers 401 to every scrape forever, and a target that always 401s is indistinguishable, from the outside, from one that was never there. |
| `networkPolicy.scrapeFrom[]` naming an `ipBlock` with neither `podSelector` nor `namespaceSelector` | A pod IP is reassigned on every reschedule, eviction and rollout. The rule installs and scrapes fine today, and stops silently the first time the scraper pod moves — the same failure this value exists to fix, reintroduced by the value meant to fix it. |
| `metricsSelfScrape.enabled` with `victoria-metrics-k8s-stack.vmsingle.spec.disableSelfServiceScrape` not `true` | The operator reconciles its own `VMServiceScrape` for the VMSingle alongside this chart's `ServiceMonitor` — a kind this file rules out on its own, and one with no `basicAuth` either way, so every scrape it drives 401s against a store running `-httpAuth.*`. |
| A `ServiceMonitor` this chart renders (`metricsSelfScrape`, or the log/trace stores' own `serviceMonitor`) with the operator's ServiceMonitor converter off — `disable_prometheus_converter: true`, or `VM_ENABLEDPROMETHEUSCONVERTER_SERVICESCRAPE: "false"` in the operator's `env` | Nothing ever converts the object to the native `VMServiceScrape` vmagent watches, so nothing ever scrapes it — a render that looks like coverage and is not. Measured on a live install; see "The doctrine's own promise was broken from this chart's first commit", above. |

### The self-alerts: nineteen rules, and what a live install did to eleven of the original twelve

Nineteen rules, rendered as one more `VMRule` alongside this chart's
other own objects — `templates/selfalerts.yaml` — and evaluated by the
metrics vmalert like any other rule this repository ships. See
docs/notifications.md, "Store self-alerts", for the full list.

The set grew from the twelve this release first shipped after a second
pass over the same design: hourly and daily cardinality are two rules,
not one distinguished by a `window` label; log and trace stream churn
are two rules, not one that only watched the log store; every rule that
is genuinely about one specific store — `MetricStoreIgnoringRows`,
`{Metric,Log,Trace}StoreDiskNearGuard`,
`{Metric,Log,Trace}StoreSnapshotOlderThanWindow` — is named for that
store rather than distinguished only by a label, because three stores
exist and a rule named just "Store..." does not say which one paged
you. `GatewayEnqueueFailing` joined `GatewayExportFailing` as a rule of
its own: the gateway's own queue refusing what a sender hands it is a
different loss from a destination refusing a batch it already accepted,
and a single rule cannot tell an operator which one happened.

**MetricStoreIgnoringRows** is the rule the header incident of this
release needed. A series pushed past the metrics store's per-day label
limit is discarded WHILE THE WRITE STILL ANSWERS 200: a collector
reported 888k rows written with zero errors while the store held none of
them, and `vm_rows_ignored_total` sat at 9,748,387 with nothing else
showing it. An alert on the store's own health looked fine, the
collector's own logs looked fine, and the only place the loss was
visible was a counter nobody was watching.

**GatewayQueueFilling**, **GatewayExportFailing** and
**GatewayEnqueueFailing** are the rule group a network-policy change
needed, the same week: it cut `charts/observability-emitters`'
OpenTelemetry gateway off from its own proxy, and "sending queue is
full" ran for hours with every pod Running and nothing reporting it. The
gateway is a component of a SIBLING chart — `otelcol_exporter_queue_size`
/ `_capacity` and `otelcol_exporter_send_failed_*` /
`_enqueue_failed_*` are named in `docs/reference.md`'s own
`otlp.podMonitor` entry, not rendered by this one — but its metrics land
in this chart's own metrics store the same way any other component's
do, once something scrapes it there; vmalert here would then read them
directly, never through the proxy, the same as every rule in this file.
A self-alert that only ever watched its OWN chart's objects would have
missed this one too.

**Measured against a live install, ten of the eleven counters this design
names are simply ABSENT from this chart's own metrics store — not wrong
values, no series under those names at all.** `count by (job) (up)`
against that store's own scrape targets shows twelve jobs: cadvisor,
kubelet, the operator, both vmalerts, Alertmanager, vmauth and vmsingle.
Nothing scrapes the log store, the trace store, vmagent, the OpenTelemetry
gateway or vlagent: `vl_`, `vt_`, `vmagent_`, `otelcol_` and `vlagent_`
each have ZERO metric names in that store. `vm_` has 63, so vmsingle
itself is scraped — but `vm_rows_ignored_total` and
`vm_free_disk_space_bytes` are not among them, which is its own question
(below). The one exception:
`vmauth_concurrent_requests_limit_reached_total` IS present.

So the design page's rule list could not be evaluated on a real install
as originally shipped in this PR, and shipping it with the metric names
written in would have produced exactly the shape this repository exists
to refuse: rules that render cleanly, look like coverage, and never fire.
Every metric name in `selfAlerts` is now a value with NO default — the
same shape `charts/platform-alerts` already uses for its own `stores`
list, for the same reason — and `selfAlerts.enabled` defaults to `false`,
because with nothing confirmed yet, "on" would mean an empty rule list.
`templates/_validate.tpl` refuses `enabled: true` with nothing that would
actually render, for the same reason it refuses `platform-alerts`' every
group disabled: a chart that renders nothing while claiming to render
something is worse than one that renders nothing and says so.

**Two possible reasons behind the absences, and this session could not
tell them apart.** `vm_rows_ignored_total` is plausibly a counter
VictoriaMetrics only registers once a row has actually been ignored — a
LAZY, reason-labelled counter, common in the metrics libraries this
family uses — in which case its absence on a HEALTHY install is the
correct, expected shape, not evidence of a wrong name. `vm_free_disk_space_bytes`
has no such excuse: it is a plain gauge with no obvious reason to be
registered lazily, so its absence despite `storage.minFreeDiskSpaceBytes`
being set is a genuinely open question — possibly a wrong name, possibly a
flag this vmsingle build reads differently, possibly something this
session does not have the visibility to explain. Recorded here rather
than resolved, because guessing which one it is would be exactly the
mistake this file exists to avoid.

**This release closes part of the coverage gap directly, for the two
components this chart itself renders.** `victoria-logs-single.server.serviceMonitor`
and `victoria-traces-single.server.serviceMonitor` are both upstream
values this chart had not been setting; turning them on gives the log and
trace stores a scrape object for the first time. Both upstream charts
already support the toggle — no new template was needed, only a value
this chart had left at its default. `ServiceMonitor`, never
`VMServiceScrape` — "Scrape objects are always the Prometheus Operator
kinds", above — because that is the one shape this repository's
collection layer is documented to read regardless of which agent or
Target Allocator ends up doing the scraping. It does NOT close the gap
for `charts/observability-emitters`' vmagent, vlagent or OpenTelemetry
gateway; that chart's own scrape coverage is its own chart's decision,
not this one's.

**A follow-up release closed the third.** The metrics store's own
`vmsingle` had no equivalent value to turn on: the vendored
`victoria-metrics-k8s-stack` offers no
`victoria-logs-single.server.serviceMonitor`-shaped toggle for it, only
the operator's own self-scrape, ON by default and impossible to add
`basicAuth` to without also making it the one kind this repository rules
out. `networkPolicy.scrapeFrom`'s default (below) makes this visible
rather than academic: once the metrics agent can reach this store's port
at all, the operator's un-authed self-scrape is a live 401, forever,
indistinguishable from no scrape object existing — the exact failure
this table's "A store whose credentials come from another Secret" row
already names, just for the one store that had never had a
`ServiceMonitor` to disagree with `storeCredentials` to begin with.
`victoria-metrics-k8s-stack.vmsingle.spec.disableSelfServiceScrape: true`
turns the operator's version off — the same value
`charts/observability-emitters` already sets for its own vmagent, and
for the same reason — and `templates/selfscrape.yaml` renders a
`ServiceMonitor` in its place, reading `storeCredentials` the same way
`templates/vmauth.yaml` and `templates/vmalert.yaml` already do, rather
than as a values.yaml literal that could drift from it.

**A doctrine refinement, while on the subject of what can and cannot be
observed.** docs/doctrine.md, "Rules are proven, not asserted", says: "A
rule whose failure case it cannot itself observe does not belong here.
That is why there is no alert on a store's read-only flag: the sample
carrying it is written into the store that has stopped accepting
writes." True as written for the METRICS store's own read-only flag —
that is genuinely self-referential, the exact failure the sentence
describes. It is NOT true in general: `vl_storage_is_read_only` and
`vt_storage_is_read_only` exist on the log and trace stores, and a
sample carrying either would be scraped into a DIFFERENT process — this
chart's own metrics store, a separate vmsingle — so it is observable IN
PRINCIPLE the same way `vl_rows_dropped_total` is. Today it is not
observed, for the same reason `vl_rows_dropped_total` was not: nothing
scraped either store into anywhere at all until this release's
`serviceMonitor` change. The doctrine sentence is over-general as
written; the metrics store's own flag is the one case it correctly rules
out, not a statement about every store's flag.

**No local proof that any of the nineteen expressions parse, even now.**
This repository has no vendored MetricsQL or LogsQL evaluator and no
bundled default-rules file inside `charts/*.tgz` to check a name against,
and `hack/rulegroups.py` reads a LIVE cluster's rendered configmaps rather
than evaluating anything offline — none of which this change may reach
(no cluster was touched to produce this PR; the live measurement above
was run and reported separately). `just golden` proves the nineteen
render into the shape intended when a metric name is supplied; it does
not prove vmalert would load or evaluate the result. Confirming any of
it — the expression parses, the name exists, the rule actually fires on
a synthetic failure — is validation this PR could not perform and the
next person to turn a rule on should.
### The quiet estate, and the restart that forgets it was already worried

An install of this chart with `alertmanager.enabled` and nothing else
used to render a route to a receiver named `blackhole` — literally that,
because there was nothing else honest to call it. vmalert evaluated
every rule in the cluster, Alertmanager accepted every one of them, and
the result went to a receiver with no configuration at all. Nothing
crashed. Nothing logged an error. The install passed every health check
this chart or Kubernetes could run, and it was the most expensive shape
a monitoring system can have, because it is indistinguishable from an
estate with no problems — right up until the incident that a rule
existed to catch and nobody heard about.

`alertmanager.config` made that shape impossible to refuse: it was a
free-form object in Alertmanager's own syntax, and an empty one, or one
that pointed everything at a receiver with no webhook, rendered exactly
as cleanly as a working one. `notifications` (docs/notifications.md)
replaces it with a structure the chart can reason about — receiver
kinds, severities, a route list — and refuses `alertmanager.enabled`
until at least one receiver kind is configured, both severities this
chart's rules use have a default, and every name a route or a severity
points at resolves to something that can actually send. The table above
is that refusal, broken into the ways a smaller piece of the same shape
can still slip through: a project route to a receiver kind that was
never wired up, a bridge webhook that is a typo, a route matched on a
label no collector stamps.

**The second failure lives one layer down, in the vmalerts themselves,
and it is not a routing problem at all.** vmalert holds the state of
every `for:` timer — how long a condition has been true — in memory,
and it writes that state to the metrics store only so it can read it
back on the next restart. Without `-remoteWrite.url` **and**
`-remoteRead.url` set, that write happens and the read does not: a
rolling upgrade, a node drain, an OOM kill, anything that restarts the
pod, resets every pending timer to zero. A rule with `for: 30m`, sat at
25 minutes when the pod restarted, is a rule that has to sit through
another 30 minutes before it can fire — in a cluster that rolls its
pods more often than that, it can never fire at all, and the dashboard
looks exactly as green as it did before the restart. Both vmalerts this
chart renders carry both flags unconditionally; neither is a value,
because there is exactly one correct answer and the wrong one is silent
in precisely the way `blackhole` was.

### A filter that is computed and never applied

**This is the failure this whole design nearly shipped, so it is written
out at length.**

The proxy's job is to turn "this person may read namespace `example-app`
on `example-cluster`" into something the store applies. It does that in two halves,
and only one of them is obvious.

The obvious half is the claim. Each principal's `VMUser` carries a
`defaultVMAccessClaim` with the selectors that principal is entitled to
— a MetricsQL series selector per grant, one LogsQL stream filter for
the whole principal. It is right there in the rendered manifest, in
`helm get manifest`, in the golden renders, and in review.

The half that actually enforces anything is the route. vmauth applies a
`vm_access` claim **only** by substituting a placeholder into the route
it is about to forward on:

```go
// app/vmauth/jwt.go
func replaceJWTPlaceholders(bu *backendURL, hc HeadersConf, vma *jwt.VMAccessClaim) (*url.URL, HeadersConf) {
    if !bu.hasPlaceHolders && !hc.hasAnyPlaceHolders {
        return bu.url, hc
    }
```

A route with no `{{.MetricsExtraFilters}}` in its query arguments — and
no placeholder in a request header — returns on that first line. The
claim was verified, selected, computed and correct, and it is discarded.
The request goes to the store with no filter on it.

So for a while every principal who passed JWT verification read every
namespace's metrics and logs on every cluster, and the manifest said
otherwise on the same object.

Every route this chart renders now carries its filter argument, and
`pkg/tenancy` cannot construct a read route without one. Three
mechanical details that are easy to get wrong, all from the same
function:

- **The placeholder must be the whole value.** Substitution is a map
  lookup on the complete value of a query argument, not a string
  replacement, so `extra_filters=x{{.MetricsExtraFilters}}` is forwarded
  to the store as written. Only the URL *path* is substring-replaced, and
  only for the tenant and account placeholders.
- **An empty filter list is not a deny, it is a hand-off.** The
  placeholder expands to the claim's *values*, so an empty list expands
  to nothing and the argument disappears from the request. The only
  thing stopping a caller from sending its own `extra_filters` is that
  such an argument *clashes* with one the route already set — and with
  the route's argument gone there is no clash, so the caller's filter is
  used. `RenderClaim` refuses to produce an empty list.
- **The clash is the other half of the enforcement.** vmauth forwards a
  client's query argument only when it does not clash with one the route
  already set; that is what stops a reader sending its own
  `extra_filters` alongside the enforced one. `-mergeQueryArgs` exempts
  an argument from that rule, and vmselect ORs each `extra_filters` as
  an alternative — so a caller adding an empty one reads everything,
  with the claim, the route and the rendered filter all still exactly
  right. The chart refuses the flag naming either filter argument.
- **Not every endpoint under a route reads the argument.** The route is
  a whole `url_map` row; the filter reaches every request in it, but a
  handler that never looks at `extra_filters` is unfiltered anyway. That
  is why the metrics route names `/api/v1/status/tsdb` rather than
  `/api/v1/status/[^/]+`: `/status/active_queries` and
  `/status/top_queries` return other principals' query text,
  `/status/metric_names_stats` returns metric names across every
  namespace, and `/api/v1/metadata` returns the metadata of every series
  in the store. None of the four takes a filter. `/status/buildinfo` does
  not either and is kept, because it carries the store's version and
  nothing from any namespace.

#### What the proxy cannot defend against, and who has to

**A token that carries its own `vm_access` claim wins.**

```go
// app/vmauth/main.go
vmac := tkn.VMAccess()
if !tkn.HasVMAccessClaim() {
    vmac = ui.JWT.DefaultVMAccessClaim
}
```

`defaultVMAccessClaim` applies only when the token has **no** `vm_access`
claim at all. A token carrying one — including an empty
`"vm_access": {}`, which counts as present — replaces the proxy-side
mapping entirely, and an empty claim expands the placeholder to nothing,
which removes the argument, which also removes the clash that stops the
caller supplying their own.

That is the design: the two shapes in `pkg/tenancy` exist because an
estate may hold the mapping in the proxy **or** in the issuer, and
`RenderClaim` renders exactly the body an issuer would mint. But it
means the proxy-side shape is only authoritative while the issuer mints
no `vm_access` claim, and nothing in vmauth can insist on that.

So it is a property of the ISSUER, and it belongs in whatever review
covers issuer configuration: in the proxy-side shape, `vm_access` must
not be a claim any client can influence — not through a scope, not
through a mapper on a user attribute, not through a token exchange that
copies unknown claims through. The same control that stops a caller
choosing their own `groups` has to cover `vm_access`, and it is a
different claim in a different place.

#### What the test that would have caught it looks like

Nothing about the broken version looked broken. The render succeeded.
The install succeeded. The proxy was healthy. Every principal could sign
in and query, and every query returned data. **And a query for the one
namespace the reviewer had data for returned exactly the rows it would
have returned if the filter had been applied** — because the filter that was
not applied would not have removed any of them.

A test that renders one principal and asserts the claim is correct
passes on both versions. A test that queries as one principal and checks
the answer passes on both versions. The two are distinguishable only by
asking one of two questions:

- **structurally**, walk every route the artifact renders and fail on any
  that does not carry the placeholder as the whole value of its filter
  argument. This is `TestEveryRenderedReadRouteCarriesItsFilter`, in both
  `pkg/tenancy` and `tests/agreement_test.go`, and it is a property of
  the output rather than of the input, because a route is only enforced
  where it is emitted.
- **behaviourally**, write a second namespace's data, query as a
  principal entitled to the first, and assert the second namespace's rows
  are **absent**. An authorization test with one namespace in it does
  not test authorization; it tests that the query works.

The general shape, worth carrying out of this repository: **a proxy that
computes an authorization decision and then does not apply it is
indistinguishable from one that applies it, in every test that does not
exercise a second principal.** The decision being visible, correct and
well tested is not evidence that anything consumes it. Assert on the
artifact that enforces, not on the artifact that decides.

### The trace store speaks two dialects, and neither one completely

The read route admits `/select/jaeger/*` and `/select/tempo/*`, because
the store answers both. It implements neither in full, and an endpoint it
does not implement answers **400** with `unsupported path requested` —
which a Grafana datasource reports as a failed query rather than as a
missing feature. So the symptom of choosing the wrong dialect is "traces
do not work", with nothing naming the cause.

Measured against VictoriaTraces 0.x with `hack/trace-api.sh`, which is in
this repository so the table below can be re-measured rather than
believed:

| Dialect | Endpoint | |
|---|---|---|
| jaeger | `api/services` | ok |
| jaeger | `api/services/{service}/operations` | ok |
| jaeger | `api/traces?service=…` | ok |
| jaeger | `api/dependencies?endTs=…&lookback=…` | ok |
| jaeger | `api/operations?service=…` | **unsupported** |
| tempo | `api/echo` | ok |
| tempo | `api/search` | ok |
| tempo | `api/v2/search/tags` | ok |
| tempo | `api/search/tags` | **unsupported** |
| tempo | `api/status/buildinfo` | **unsupported** |

**This is why the chart's default trace datasource is `type: jaeger`.**
Grafana's Jaeger datasource calls the four endpoints the store
implements and none of the one it does not: it asks for operations by the
NESTED path, `api/services/{service}/operations`, while Jaeger's own UI
moved to the flat `api/operations?service=`. That divergence is the only
reason the gap is not a problem, and it is somebody else's decision to
keep — if a Grafana release ever switches to the flat form, the Operation
dropdown empties and nothing else changes.

**A Tempo datasource against this store is the shape to avoid.** Search
works, but `api/status/buildinfo` is how Grafana's Tempo datasource
decides which features the backend has, and a 400 there degrades it for a
reason no message connects to the cause.

Two consequences worth stating, because neither is obvious from a working
install:

- **A 400 from this store is usually not about your query.** Read the
  body: `unsupported path requested` names an endpoint, and
  `incorrect trace query params` names a missing argument. The second is
  ordinary — Grafana's Explore posts a Jaeger search with no service
  selected and the store refuses it, which is correct, and which looks
  identical to an outage until the body is read.
- **The dependency graph answers empty rather than refusing, and by
  default nothing is computing it.** `api/dependencies` returns
  `{"data":[],"total":0}` with a `200`, so a service map with nothing on
  it looks like a store with no dependencies to report, not a disabled
  feature — and on this chart it usually is the latter: the graph is
  built by a background task, `-servicegraph.enableTask`, that upstream
  ships off and this chart writes out explicitly as `"false"` in
  `victoria-traces-single.server.extraArgs` for exactly that reason —
  see the comment there. Turning it on does not change the failure mode
  above, it changes which of the two causes is true: the endpoint still
  answers `200` with an empty body until the task has had at least one
  `taskInterval` to run, and it is upstream-experimental, supported only
  on a single-node or vtstorage deployment. Whether the relations it
  computes are written back into the store, and so count against
  retention and disk the way a trace does, is inferred from the endpoint
  existing at all — nobody has measured it here.

## Four metrics endpoints, one of them a decision

The metrics read route is a list of named endpoints rather than a prefix,
and four endpoints the prefix would have caught are worth naming
individually, because they fail in different ways.

`/api/v1/status/active_queries` and `/api/v1/status/top_queries` return
other principals' **query text**. `/api/v1/status/metric_names_stats`
returns metric names with per-tenant counts. None of the three takes a
filter, and no value in this chart admits any of them: what they disclose
is per-principal, so there is no estate for which routing them is
correct.

`/api/v1/metadata` is the fourth, and it is different in degree. It
returns every metric **name** in the store with its type and help string
— a bounded list, the same for everybody, with no label values and no
per-tenant counts in it. It takes no filter either: measured against the
store, an `extra_filters` naming a namespace that matches nothing returns
the same body as no filter at all.

So whether to route it depends on the install:

- where every grant is `allNamespaces`, it discloses nothing a principal
  could not already query, and routing it costs nothing;
- where grants are per-namespace, it is an inventory of the components
  and products another tenant runs.

That is an estate's judgement rather than this chart's, so it is
`tenancy.allowUnfilteredMetricMetadata`, and it is **off**.

**The cost of off is visible, which is the point.** Grafana's
Prometheus-family datasources ask for this endpoint to put descriptions
on metric names, and write a 401 into their own logs when the proxy does
not route it:

    path=/api/datasources/uid/<ds>/resources/api/v1/metadata status=401

Nothing is broken: the query builder still lists metric names, because
those come from `/label/__name__/values`, which **is** filtered — only
the descriptions are missing. But an operator reading that 401 should
arrive at a value they can set, not at a mystery, which is why the
endpoint is a switch rather than a rule. A wildcard would have admitted
all four at once and said nothing about any of them.

## The one signal this proxy cannot scope

**Traces.** vmauth enforces by substituting a filter into the route, and
VictoriaTraces' Jaeger and Tempo select APIs accept no query argument to
substitute one into: `tracecommon.GetCommonParams` takes a tenant id from
the `AccountID`/`ProjectID` headers, `hidden_fields_filters` — which
hides *fields* from a result, not rows — and `allow_partial_response`,
and nothing else. The Jaeger query parameters (`service`, `operation`,
`tags`, ...) are the caller's own and are overwritten rather than
appended to, so a proxy cannot narrow them either.

The tenant headers are a real mechanism, but a different tenancy model:
they select one of the store's own tenant ids, and this design scopes by
*label* precisely so a fleet-wide question stays answerable across
clusters and namespaces. Nothing writes per-namespace account ids on the
way in, so there would be nothing for them to select.

So trace reads through this proxy cannot be scoped to a principal, and
the chart says so rather than rendering a route that looks like the two
beside it. With a trace store and `principals` both set it refuses to
render until `tenancy.allowUnfilteredTraceReads` is `true`, and the
library refuses the same way unless `AllowUnfilteredTraceReads` is set.
The name is the point: what it admits is that **every principal who can
reach the proxy reads every namespace's spans on every cluster.** It admits the trace route
and nothing else — no value of it relaxes the metrics or logs route.

An estate that cannot accept that turns the trace store off. An estate
that can has written down that it did, in a values file somebody
reviews. What neither of them gets is the third option, which is the
defect above one level down: a route that carries a grant nobody
applies.

### What vmauth checks on a token, and what it does not

It checks two things: that the token has not **expired**, and — with OIDC
discovery configured — that its **issuer** is the one configured. That is
the whole list. There is no audience option anywhere in vmauth's
configuration, and `aud` is a claim it never reads on its own.

That is easy to read past, because an issuer is exactly the thing one
expects a proxy to check. But an issuer is not a client. An estate's
issuer mints tokens for every application that signs people in through
it, and all of those tokens carry the same `iss`, are signed by the same
keys, and carry the same person's groups. So a proxy that checks the
issuer and the groups admits **any unexpired token that issuer minted,
for any of its clients**: a token a person holds for some entirely
different application is a token that reads their namespaces here.

Nothing about that looks wrong from the inside. The signature verifies,
the claim matches a principal, the filters are computed and applied, and
the query returns exactly the rows that principal is entitled to. It is
not a leak of another person's data — it is the wrong *credential*
reading the right person's data, which is the failure an audience exists
to stop and the one nothing in the request will ever report.

So the audience is the caller's to pin, and both artifacts make it
required: `tenancy.audience` on the chart, `Config.Audience` in
`pkg/tenancy`, rendered into every reader's `match_claims` beside the
group, under `aud`. The claim name is fixed rather than an input —
OpenID Connect specifies it, and a second spelling of a spec-defined
claim is how a configuration comes to read as though something were
pinned when nothing is.

What this does not do is make a token's `vm_access` claim trustworthy, or
limit what a holder of a token for this audience may ask for. It answers
one question — was this token minted for this proxy — which is the
question that was not being asked at all.

### Every `match_claims` value means only itself

A `match_claims` value is compiled as a **regular expression** — vmauth
does that with both entries in the map, the group and the audience
alike. Neither value is this repository's to choose: a group is whatever
the identity provider calls that population and whatever a derivation
produced from it, an audience is whatever an issuer assigned. So both
are **escaped** where they are rendered and **anchored** around:

```yaml
matchClaims:
  groups: "^(example:k8s:viewer)$"
  aud: "^(123\\.apps\\.example-issuer)$"
```

Escaped, so each value means itself: a client id with a dot in it pins
that client and not every id of the same length. Anchored, so each means
only itself: a group written `.*` matches the literal `.*` and no other
token, which is what a derivation that produced a pattern instead of a
name should do.

**Escaped rather than refused, which is the opposite of what this
repository does with a cluster or a namespace name, and the distinction
is the point.** Those names are the estate's own: it chooses them, it
can change them, and holding them to a narrow shape costs it nothing —
so a name carrying `|` or `.*` is refused, because a name that needs
escaping is a name nobody should have chosen. A client id and a group
name are handed to the estate by an identity provider it does not
control. Issuers mint client ids with dots in them, and identity
providers name populations with spaces and dots. Refusing a shape we do
not control is not the doctrine applied consistently; it is an outage
for that operator with no alternative they could take, in a component
published for anyone to install. **The rule is: refuse what we name,
escape what we are handed.**

What is still refused on the audience is a value no issuer mints at all
— one carrying whitespace or a newline, which is how a value that
arrived from the wrong place looks: a file read with its trailing
newline, a heredoc, two ids in one string. That refusal is about the
plumbing, not about the character set.

Two more things follow from where these values land:

- **The anchors are rendered although vmauth anchors too.** It wraps a
  `match_claims` value in `^(?:…)$` from v1.152.0 — the release that
  fixed the unanchored-claim advisory that sets the version floor below
  — so the anchors here are redundant in front of a proxy at that floor.
  They are rendered because a narrowing control that works only when the
  binary in front of it is patched is a control with a version number in
  it, and because anchoring twice costs nothing: `^(?:^(x)$)$` is still
  exactly `x`. Both the library and the chart are tested under both
  compilations.
- **A list `aud` needs no special case.** An issuer mints the claim as a
  string or as an array, and vmauth tests a `match_claims` entry against
  an array claim element by element, matching if any one of them does.

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

The same reasoning removed two more paths from the metrics route: an
endpoint that takes no filter is not narrowed by one, so
`/api/v1/metadata` and everything under `/api/v1/status/` except `tsdb`
and `buildinfo` are no longer routes. See "A filter that is computed and
never applied" above.

### Why there is no unauthorized user, and why that is written as nothing

vmauth has an `unauthorized_user` section: a route served to callers that
did not authenticate. This stack has none, and the way the chart says so is
by rendering **no `unauthorizedUserAccessSpec` at all**. The absence is the
setting. It is worth stating why, because an empty spot in a manifest reads
like an oversight and invites someone to fill it in.

**A token that verifies but carries no `vm_access` claim falls through to
the unauthorized user.** That is vmauth's own order from v1.147.0: find the
user whose `match_claims` the token matches, and if that token has neither
a `vm_access` claim of its own nor a `default_vm_access_claim` on the user
it matched, hand the request to the unauthorized user instead of refusing
it. So an unauthorized section is not only a door for anonymous callers —
it is where a **half-configured principal** ends up, with a token that
verified.

**Every shape the operator accepts is a shape that serves.** There is no
setting that means "an unauthorized user which refuses". The operator
validates the section and rejects one that routes nowhere — *at least one
of `url_map`, `url_prefix` or `targetRefs` must be defined* — so the
minimum it will accept is a route. On a cluster running the pinned operator
and vmauth v1.152.0, a proxy with the smallest acceptable section answered
**200** to all four of: a token matching a principal, a verified token with
no claim, a garbage token, and no `Authorization` header at all. With no
section, the same four answered 200, **401, 401, 401**. The section is not
a policy knob; it is an on switch.

Both halves of the defence therefore have to hold at once, and neither is
visible in the other's absence:

| The control | What it stops | What it looks like when it is missing |
|---|---|---|
| No `unauthorizedUserAccessSpec` on the VMAuth | An unauthenticated or half-authenticated caller being served | Reads succeed for everybody, and nothing logs a refusal |
| `defaultVMAccessClaim` on every reader VMUser | A verified token reaching the fall-through in the first place | Every token for that principal is refused, with nothing saying why |

`tests/pruning_test.go` holds `TestNoUnauthorizedUser`, which fails if any
rendered VMAuth carries either `unauthorizedUserAccessSpec` or the
deprecated `unauthorizedAccessConfig`.

### A field the API server prunes is invisible everywhere but the cluster

The defect that earned the check above was two lines that looked like the
control and were not: `unauthorizedUserAccessSpec: {disabled: true}`, a
plausible spelling of "off" that no release of the operator has ever had.

**A key a CustomResourceDefinition does not have is not an error.** The
manifest renders. It is valid YAML. `helm lint` passes, the values schema
has nothing to say because the key is in the template's output rather than
in anyone's values, and the golden is byte-for-byte what a golden of a
working chart would be. The API server then silently drops the key and
stores what is left — here, `unauthorizedUserAccessSpec: {}`, a section
that routes nowhere, which the operator refuses, so the proxy got **no
Deployment and the estate had no read path at all**. A control that was
never applied and a component that never started, from a diff that showed
nothing wrong.

Three things make this class worse than a typo:

- **It is invisible in the direction people look.** Every artifact between
  the template and the cluster agrees the field is there. Only the stored
  object disagrees, and nobody diffs against that.
- **Strict validation is on the path nobody delivers through.** `kubectl
  apply` defaults to strict field validation and *does* refuse it outright.
  Helm and Argo CD do not, so the check that would have caught it is the
  one a person runs by hand and never the one that ships.
- **The loud failure is the lucky one.** This field was load-bearing enough
  that the operator refused the object. A pruned field that merely *relaxes*
  something — a misspelled `defaultVMAccessClaim`, a filter under a key that
  does not exist — installs cleanly and reports healthy, having quietly
  removed a restriction.

So the rule is: **a rendered field is not a configured field until something
has checked it against the schema that will receive it.** This repository
can check that without a cluster, because it ships both halves —
`charts/observability-crds` vendors the definitions and the other charts
render objects against them. `TestRenderedObjectsSurviveTheCRDs` walks every
custom resource in every golden against the definition for its kind and
fails on any field that would be pruned. `tests/pruned/` holds manifests
that are destroyed by the API server and pass every other check, and
`TestPrunedFixturesAreCaught` requires each one to be reported — a checker
whose only evidence is that it has never failed is not evidence.

One subtlety the check had to be taught, verified against a live API server
rather than reasoned about: `x-kubernetes-preserve-unknown-fields` holds
only at the node that sets it. `VMAuth.spec` sets it, so an unknown key
directly under `spec` survives — but `spec.unauthorizedUserAccessSpec`
carries `properties` of its own and prunes inside itself. A checker that
stopped at the first preserving node would have passed this defect.

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

Thirty, each with a fixture under
`tests/invalid/observability-emitters/` that is otherwise valid, so it
fails for its one reason and no other.

They divide into four kinds, and the first kind is the reason the chart
exists.

### The scoping key, which is a security property and not a convenience

| Refusal | The failure it prevents |
|---|---|
| `metrics.spec.overrideHonorLabels: false` | With honor labels not overridden, a label a **target exports itself** wins over the label the agent stamps. Any workload that exposes its own `k8s_namespace_name` or `k8s_cluster_name` label then chooses where its series are filed: it can write into another team's data, or hide its own from the people responsible for it. The render, the sync and the dashboards all look correct. |
| A default scrape class that writes neither key | `mergeOverwrite` replaces a list wholesale, so a caller who adds one scrape class of their own replaces the stamping one — and a replacement that is still the default class passes every other check while stamping nothing at all. The rules are checked for the two keys, not just their container. |
| `tenancy.cluster` empty | The cluster is half of the scoping key. Telemetry stamped with a cluster called nothing matches no grant the proxy injects: stored, paid for, and invisible to everyone who might have acted on it. |
| `tenancy.environment` empty | The tier is never a key, so a blank one selects nothing wrongly. It is refused because a `deployment.environment.name` of `""` on every series is a dimension that exists and says nothing, and nobody notices until the first dashboard that groups by it. |
| Either outside the plain-name shape | `example-cluster\|other-cluster` does not look odd in a rendered filter — it grants a second cluster. Refused, never escaped. The tier is held to the same shape because it is stamped into the same places. |

There is no fallback value and no namespace-label key any more, and no
refusal for an unlabelled namespace: the namespace is the key, a
namespace always has a name, and the emitters read it from service
discovery or from the pod object rather than from a label somebody had
to remember. What used to be a tenant — a project, a team — is a
derivation from a name to a namespace list held with whoever writes the
grants.

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
| The log agent's `extraFields` not a JSON object, or disagreeing with `tenancy` | It is how the container-log agent stamps the cluster and the tier on every line, and Helm cannot compute a subchart's values, so it is written twice and checked. A wrong cluster here files every container log on the cluster under another one, where no grant for this one reaches it. |
| `otlp.streamFields` empty | No `VL-Stream-Fields` header is sent, and with none VictoriaLogs treats EVERY resource attribute as a stream field. An OpenTelemetry SDK's resource carries the pod's UID and its start time, so every restart of every workload mints a stream that is never written to again. The store does not fail; it degrades, over weeks, in a way that reads as growth. |
| A stream field outside the chart's list | A field that changes per request — an address, a user id, a trace id — creates a stream per value. It is the vendor's own named way to wreck this store, and it does not recover on its own. The list is the chart's and not a value, because an allow-list a caller can extend is a comment. |
| `k8s.cluster.name` or `kubernetes.pod_namespace` missing from either writer's stream fields | A stream filter, which is what the proxy injects, only selects on stream fields. A key that is an ordinary field is a key every scoped log query misses — for that writer's half of the store, while the other writer's half still answers, which is the more confusing shape. |
| The Helm release label as a stream field | It is constant per pod, but a stream field is a cardinality decision, and the release is navigation. The allow-list is the chart's and does not include it. |
| A log write path other than `/insert/native` | The only path that accepts that protocol. A wrong one answers 404, and vlagent treats 404 as a permanent rejection and DROPS the block rather than retrying it. The loss is silent, unrecoverable, and proportional to how long it takes somebody to look. |

### And the rest

| Refusal | The failure it prevents |
|---|---|
| An unknown key | A setting that does not apply: a label never stamped, a destination never written to, a buffer that was never on a volume. The install succeeds either way. |
| Every emitter disabled | A release that collects nothing and reports Synced — one more green application saying the cluster is fine. |
| `writeCredentials.secretName` empty | The stores answer 401 to every write, each agent buffers until full, then drops the oldest, with every pod Ready throughout. |
| A log destination with no credential | The same, for the one emitter whose credential is upstream's shape rather than this chart's. |
| `podMonitor.vm: true` on the log agent | It renders a `VMPodScrape` instead of a `PodMonitor` — see below. |
| A mirror that disagrees | `interval` against the agent's scrape interval, and `tenancy.cluster` / `tenancy.environment` against the log agent's `extraFields`. Helm evaluates a subchart's values before any template runs, so some values have to be written twice; two numbers that are supposed to be equal stop being equal the first time somebody changes one. |
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

### The doctrine's own promise was broken from this chart's first commit

"The VictoriaMetrics operator converts the Prometheus kinds today", above,
was not true for THIS chart, from `values.yaml`'s very first version:
`victoria-metrics-k8s-stack.victoria-metrics-operator.operator.
disable_prometheus_converter` was `true`, which turns every one of the
operator's six per-kind converters off at once —
`VM_ENABLEDPROMETHEUSCONVERTER_{PODMONITOR,SERVICESCRAPE,PROBE,
SCRAPECONFIG,PROMETHEUSRULE,ALERTMANAGERCONFIG}` all `false` — and the
operator has no per-owner or per-namespace filter for it: the switch is
cluster-wide or nothing.

Measured on a live install: every `ServiceMonitor` and `PodMonitor` this
repository's charts render was exactly as inert as one authored in the
wrong kind outright. `count by (job) (up)` in the metrics store showed
only the operator's own native `VMServiceScrape` objects (the operator
itself, both vmalerts, vmauth) plus kubelet/cadvisor — never the log
store's `ServiceMonitor`, the trace store's, or the metrics store's own
`*-metrics-selfscrape` this release's predecessor added, and never
`charts/observability-emitters`' `PodMonitor` objects for vmagent, vlagent
or the OpenTelemetry gateway. `vm_` stayed at 63 metric names — the same
63 as before `metricsSelfScrape` existed — and `vl_`/`vt_`/`vmagent_`/
`otelcol_`/`vlagent_` stayed at zero. Two releases (0.5.0, 0.5.1) added a
`ServiceMonitor` believing this section's own promise; neither closed
anything, because the promise was never kept on this install to begin
with.

The comment the value carried was a real concern, not a mistake: turning
on a converter is watching every `ServiceMonitor`/`PodMonitor` cluster-wide
and reconciling a native object for each, and on an estate that also runs
its own Prometheus Operator, or a second VictoriaMetrics operator
instance, converting an object a different team's chart owns is exactly
how two controllers end up fighting over one scrape. The mistake was the
instrument: `disable_prometheus_converter` has no scope narrower than
"every kind, cluster-wide", so protecting against a collision on kinds
this stack never touches also disabled the two kinds every chart in this
repository actually authors.

The fix leaves `disable_prometheus_converter: false` — the vendored
chart's own default — and restates the four converters this stack has no
stake in back to `false` explicitly, as `env` entries on the operator
(`VM_ENABLEDPROMETHEUSCONVERTER_PROBE`, `_SCRAPECONFIG`,
`_PROMETHEUSRULE`, `_ALERTMANAGERCONFIG`): nothing this repository renders
is a `Probe`, `ScrapeConfig`, `PrometheusRule` or `AlertmanagerConfig`, so
the original protection stands for exactly the kinds it was ever needed
for. `VM_ENABLEDPROMETHEUSCONVERTER_SERVICESCRAPE` and `_PODMONITOR` are
left unset, which is the operator's own default of `true` — the two kinds
this repository's own `ServiceMonitor`/`PodMonitor` objects need converted
for vmagent to ever see them.

`pkg/scrapeconversion`'s Go test reads the checked-in golden renders and
asserts these six values directly, rather than only their agreement with
their own regeneration: `just golden` will happily rewrite a golden file
to match a `values.yaml` that turns a converter back off, and a diff that
matches itself is not evidence anything scrapes anything — the exact
shape "renders cleanly, does nothing" this repository exists to refuse.

## Two writers per signal, and which one yields

One name per dimension per signal is the invariant, and on two signals
two different writers share one store. Each pair collides once, and each
collision is closed the same way: the writer that can be told what to
call a thing matches the one that cannot.

### Logs: the container-log agent cannot rename a field

vlagent has **no way to rename a field**. Its levers are
`-kubernetesCollector.extraFields`, `.ignoreFields`, `.streamFields` and
the `include*` toggles; none renames. So the namespace reaches the log
store under the agent's own spelling, `kubernetes.pod_namespace`, and
no other — and that is the one place in the whole vocabulary where the
name is not OpenTelemetry's.

The OTLP gateway's log pipeline can call an attribute anything. So it
yields: `transform/tenancy` copies `k8s.namespace.name` — which it keeps
too — into `kubernetes.pod_namespace` on the log pipeline alone, and
`VL-Stream-Fields` names it. One store, one spelling for the key, and
the ugly name is confined to the one signal that forced it.

Cluster and tier need no such step. Both are constants for the cluster,
which is exactly what a static extra field can carry, so the agent adds
`k8s.cluster.name` and `deployment.environment.name` under the exact
conventional names through `extraFields` and the two writers agree for
free. Helm cannot compute a subchart's values, so that string is a
mirror of `tenancy` and the chart refuses it when it disagrees or is not
a JSON object.

What this closes, compared with the previous design: the field derived
from an estate's namespace label (`kubernetes.namespace_labels.<key>`)
is gone entirely, the read side no longer needs to be told a field name
with no safe default, and the gap where a namespace with no label
produced log streams nobody could select cannot occur — a namespace
always has a name.

### Metrics: a resource attribute is not a label

Scraped series carry the namespace already, as `namespace`, from
service discovery; the metrics agent relabels `k8s_namespace_name` from
`__meta_kubernetes_namespace` beside it and stamps the cluster and the
tier statically. `namespace` stays, for every pre-built dashboard and
rule that reads it.

OTLP-derived metrics reach the same store through the gateway's
Prometheus remote-write exporter, and that exporter **does not promote a
resource attribute to a label unless configured to**: the resource goes
onto a `target_info` series and the metric itself arrives carrying only
its datapoint attributes. So `k8s.namespace.name`, set correctly on the
resource by `k8sattributes`, would have reached the store on
`target_info` and nowhere else, and every scoped query would have missed
every OTLP-derived series. The exporter is therefore told to promote
exactly three attributes — `k8s.cluster.name`, `k8s.namespace.name`,
`deployment.environment.name` — through `resource_constant_labels`,
which spells them with underscores on the way out; that is how they
match the agent's labels and the proxy's filters. Only those three: the
rest of the resource carries the pod UID, and a label that changes per
restart is a series that changes per restart. Verified against the
collector binary the chart pins rather than read from its
documentation, because the option is one of two the documentation
lists and the other is deprecated.

### The application does not get a vote

On metrics, `overrideHonorLabels` makes the agent's stamp replace a
target's own, and the `exported_(…)` copy the agent would otherwise keep
of a conflicting label is dropped for the three names, so a target that
exports `k8s_namespace_name` neither wins nor leaves a second label
somebody will eventually query by.

On the gateway the order of the processors is the property.
`k8sattributes` writes an attribute **only when it is absent or empty**
— so a resource that arrived already carrying `k8s.namespace.name` would
keep the application's claim, and the namespace is the key. So
`transform/disown` runs first and deletes both namespace spellings;
`k8sattributes` then writes the namespace from the pod object it
resolved the sender to; and `transform/tenancy` sets the cluster and the
tier with `set`, which overwrites whatever an SDK put there. The pod is
resolved from the **connection** first, because the pod-UID and pod-IP
resource attributes the other two sources read are the sender's own
claim about itself; they remain as fallbacks for a sender whose address
resolves to no pod, such as a host-network pod.

### What the read side does about it

`pkg/tenancy` and `charts/observability-stack` take the keys as inputs
**with defaults** — `clusterLabel` / `namespaceLabel` and
`logsClusterField` / `logsNamespaceField` on the chart, the same names
on the `Config` — and the defaults are what `charts/observability-emitters`
stamps. `tests/agreement_test.go` reads the names back out of a rendered
library filter and finds each one in the rendered manifest of every
writer that stamps that signal. An estate whose collectors were not
built here sets them; nobody else touches them.

Two consequences of the shape, both of which the renderers handle and
neither of which is obvious from the metrics path.

**The log field name is quoted, and held to its own shape.** A LogsQL
word is `[a-zA-Z0-9_]` and nothing else, so a real field name — which
carries dots — is not a word and has to be quoted to be read as one
name. It is therefore held to a *field* shape,
`^[a-zA-Z0-9_][a-zA-Z0-9_./-]*$`, rather than to the plain-name shape a
namespace is held to. The namespace rule is not loosened to let a field
name through: a namespace name goes inside the alternation, where a `.`
is a metacharacter. A field name carrying stream-filter syntax — a
quote, a brace, a comma, an equals sign, a `|`, a colon, a space — is
refused rather than escaped, for the same reason a namespace name is.
A metrics key is held to the Prometheus label shape instead, because
the defaults carry underscores and a label carries no dot.

**A principal gets one stream filter, not one per grant.** VictoriaLogs
AND-s every `extra_stream_filters` argument it receives into the query as
its own global constraint, so a second entry does not widen a principal's
reach: it narrows it to the intersection, and two grants naming two
clusters intersect in nothing at all. That is an empty screen for
exactly the people with the most access. So the grants are rendered as
`or` alternatives inside a single filter, where a comma still binds
tighter than `or` and no grant can borrow another grant's namespaces.
The metrics path takes the opposite convention — vmselect OR-s its
`extra_filters` — which is why the two claim fields are neither the same
list nor the same length, and why a test asserts they are not.

### Why this is a safety property and not a footnote

Every other refusal in this document prevents something that eventually
announces itself: a pod that will not start, a store that answers 401, a
bill. This one prevents a **successful query that returns nothing**.

There is no error, no failed object, no alert and no log line. The person
who ran it sees an empty result and draws the obvious conclusion — that
their service is not logging, or that the collector is down — and starts
looking in the place the fault is not. During an incident that is worse
than a refusal by a wide margin, because the empty result is *evidence*,
and it is evidence for something untrue. A refusal at render time costs
somebody five minutes with this page open. An empty result costs whatever
the incident costs.

That is the whole argument for asking rather than defaulting.

## Why the gateway is a StatefulSet, and why its processors are ordered

Each gateway replica owns a persistent queue, and a ReadWriteOnce volume
cannot be shared by two pods — so a Deployment with more than one replica
is a Deployment where at most one replica has a queue. A queue on an
emptyDir is not a queue: it is a buffer discarded exactly when it is
holding something.

The processor order is a second, quieter thing, and it is written up
above under "The application does not get a vote": `transform/disown`
strips the namespace a sender claimed, `k8sattributes` resolves the pod
and writes the real one, `transform/tenancy` sets the cluster and the
tier. The three-step renders identically for a well-behaved application
and differently for the one that matters.

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
rather than in this library. It is also why every `match_claims` value
this repository renders is escaped and anchored rather than trusted to
the proxy's own anchoring: see "Every `match_claims` value means only
itself" above.

Upstream backported the fix only to the v1.148 LTS line, and the
operator's own default image tag is older than both, so the chart sets
the tag explicitly rather than inheriting it, and refuses anything
lower.

## Grafana's replica count and its database are one decision

Grafana's default database is SQLite, a file on the pod. Running two
replicas on it has two shapes and neither is usable:

- two filesystems, so two databases — a dashboard saved on one replica is
  missing from the other, and which one a person gets is the load
  balancer's business;
- one ReadWriteOnce volume shared between them, so two processes writing
  one SQLite file:

      500  database is locked (SQLITE_BUSY)

The second is the one that gets shipped, because it looks right. The
failure is per REQUEST, not at startup: the pods are Running and Ready,
sign-in works, the dashboard list renders, and roughly one interaction in
three fails. Measured on a live install before it was moved to Postgres.

So the chart refuses `replicas > 1` unless `[database] type` names
something shared. It is one refusal rather than a note because the two
values live in different parts of the file and are set by different
people at different times — the replica count when somebody wants
availability, the database when somebody is thinking about databases.

The password goes in `envValueFrom.GF_DATABASE_PASSWORD`, not in
`grafana.ini`: that section renders into a ConfigMap, and a ConfigMap is
readable by anything that can read ConfigMaps.

`locking_attempt_timeout_sec` is a separate refusal and a separate
failure — Grafana takes a lock through its schema migration at startup,
and the default of 0 means "do not wait", so the second replica of a
rolling update crash-loops through the migration. Setting it does not
make SQLite shareable.

## A store nobody can query

Enabling a store provisions it, gives it a volume, writes to it, and
retains it for as long as its retention says. Whether anyone can READ it
is a different value in a different part of the file: the datasource
list.

A store with no datasource is not broken. It ingests, it answers, its
volume fills at exactly the expected rate, every alert on it evaluates —
and nobody ever looks at it. There is no error, no unhealthy object and
no metric that goes the wrong way. The only symptom is absence, and
absence is what an idle store looks like too.

This chart's own defaults had it: the trace store was enabled and the
datasource list named metrics and logs. So the chart now refuses an
enabled store that no datasource type reads. The escape is explicit —
turn the store off, or add the datasource.

Three stores, three URL **shapes**, and they do not resemble each other:

| store | datasource type | url |
| --- | --- | --- |
| metrics | `prometheus` | `…:8427/prometheus` — the plugin appends `/api/v1/...` |
| logs | `victoriametrics-logs-datasource` | `…:8427` — the ROOT; the plugin appends `/select/logsql/query` |
| traces | `jaeger` | `…:8427/select/jaeger` |

Naming the logs plugin's query path in its URL is the mistake that
reaches the store as `/select/logsql/select/logsql/query` and comes back
`unsupported path requested`. The datasource saves, the health check
passes, and only a query fails.

## A store whose own policy hides it from the scraper

The NetworkPolicy in front of each store admits the proxy, vmalert and
the store's own pods, and denies everything else — which is the point of
a policy that selects a pod at all. For as long as this chart existed,
that list did not include whatever scrapes the store's own `/metrics`,
because nothing did: the metrics agent `charts/observability-emitters`
renders carries `app.kubernetes.io/instance: observability-emitters`,
`app.kubernetes.io/name: vmagent`, and none of the three admitted peers
matches it.

Measured on a live install: `up=0` for every store's own scrape job, no
`scrape_samples_scraped` series for it at all, and the store's own
`/metrics` endpoint answering with 194 `vm_`-prefixed names while the
store held 63 of them — arriving from vmauth, vmalert, Alertmanager and
the operator, which are all scraped. Not one from the store itself.

The install looked healthy by every other measure. The store answered
200 on every query, ingested, retained, rotated its backups — the
NetworkPolicy did exactly what it was written to do, which is the
failure: a policy that is present and correct for the peers it names,
and simply never named the one peer that would have caught this. Every
rule in `charts/platform-alerts` that names a store's own counter, and
every one of this chart's own `selfAlerts`, depends on a
sample that never arrived; each evaluates against no data, which is not
the same as evaluating to healthy, and none of them can tell the
difference. A `TargetDown`-shaped rule watching the scrape job itself
would have caught it days sooner — except this chart ships no
notification path by default either, so on an install that also never
configured `notifications`, that alert fires into the receiver
`alertmanager.enabled` refuses to leave unconfigured: nobody. Two silent
failures stacked exactly on top of each other look, from a dashboard, the
same as no failure at all.

`networkPolicy.scrapeFrom` (docs/reference.md) closes it: the metrics
agent's selector is now one of the ingress peers by default, so a bare
`helm install` self-monitors, and the same file's own refusal (above)
stops the value from being reopened by an override that names a pod's
address instead of its identity.

The proxy's own policy shipped the identical gap one port over. The
vm-operator injects a config-reloader sidecar into the VMAuth pod to
watch the Secret it generates and signal a reload, and renders a second
endpoint for it — `reloader-http`, port 8435 — on the VMServiceScrape it
manages alongside this chart's objects. The proxy's NetworkPolicy
selected that pod and admitted only 8427, so 8435 answered at the socket
and lost every sample at admission: `up=0` for the job,
`TargetDown`/`ServiceDown` firing forever on a pod that was otherwise
perfectly healthy. Fixed the same way: the proxy policy's ingress now
admits `networkPolicy.scrapeFrom` on 8435 too, and
`TestProxyPolicyAdmitsReloaderScrape` (tests/networkpolicy_test.go)
proves it against every golden the proxy's NetworkPolicy renders into.

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

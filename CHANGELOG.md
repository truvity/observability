# Changelog

Prose bullets, written for the consumer: what changes in the render, what
must be done first, and whether a default moved. Newest first.

A version missing from this file changed nothing for a consumer — it is a
patch cut for dependency bumps alone, and its GitHub Release lists them.

## 0.5.0

The nineteen store self-alerts, INF-986 — and, once a live install was
checked against them, mostly a value list waiting for names.

- **New value: `charts/observability-stack`** — `selfAlerts`, one more
  `VMRule` alongside this chart's other own objects, watching the stack's
  own components rather than a consumer's telemetry:
  `MetricStoreIgnoringRows`, `MetricStoreCardinalityNearLimit`,
  `MetricStoreDailyCardinalityNearLimit`, `LogStoreDroppingRows`,
  `TraceStoreDroppingRows`, `LogStoreStreamsChurning`,
  `TraceStoreStreamsChurning`, `WriterBufferGrowing`,
  `WriterDroppingPackets`, `GatewayQueueFilling`, `GatewayExportFailing`,
  `GatewayEnqueueFailing`, `ProxyAtConcurrencyLimit`,
  `MetricStoreDiskNearGuard`, `LogStoreDiskNearGuard`,
  `TraceStoreDiskNearGuard`, `MetricStoreSnapshotOlderThanWindow`,
  `LogStoreSnapshotOlderThanWindow`, `TraceStoreSnapshotOlderThanWindow`
  — see docs/notifications.md, "Store self-alerts", for what each one
  catches. Every rule that watches one specific store is named for that
  store — three stores exist, and a rule named just "Store..." does not
  say which one paged you; a rule stays generic
  (`WriterBufferGrowing`, `WriterDroppingPackets`,
  `ProxyAtConcurrencyLimit`) only where it genuinely is not about any one
  store.

  Two rules close incidents from this week: `MetricStoreIgnoringRows`
  watches for the counter that sat at 9,748,387 while a collector reported
  888k rows written with zero errors and the store held none of them;
  `GatewayQueueFilling` / `GatewayExportFailing` / `GatewayEnqueueFailing`
  watch the OpenTelemetry gateway's queue and its send and enqueue
  failures — two separate rules, because a destination refusing a batch
  already accepted and the gateway's own queue refusing at the door are
  different losses — after a network policy change cut the gateway off
  from its own proxy and "sending queue is full" ran for hours with every
  pod Running and nothing reporting it.

  **Every metric name in `selfAlerts` is a value with NO default**, the
  same shape `charts/platform-alerts` already uses for its own `stores`
  list. Measured against a live install: ten of the eleven counters this
  design originally named were simply absent from this chart's own
  metrics store — the log store, the trace store, and the sibling chart's
  OpenTelemetry gateway had NOTHING scraping them into it at all. Shipping
  the names as written would have produced exactly what this repository
  refuses: rules that render cleanly, look like coverage, and never fire.
  `selfAlerts.enabled` therefore **defaults to `false`**; each rule renders
  independently once its metric name is confirmed against your own
  component's `/metrics` and set, and the render refuses `enabled: true`
  with nothing that would actually render at all. See docs/safety.md,
  "The self-alerts: nineteen rules, and what a live install did to eleven
  of the original twelve", for the measurement, the two exceptions
  (`kube_cronjob_status_last_successful_time` for the `*SnapshotOlderThanWindow`
  rules, a standard field this session did not doubt;
  `vmauth_concurrent_requests_limit_reached_total`, confirmed present but
  still not written in), and a doctrine refinement on which store
  read-only flags are observable in principle.

- **New value: `victoria-logs-single.server.serviceMonitor.enabled` /
  `victoria-traces-single.server.serviceMonitor.enabled`**, both now
  `true`. The two components this chart itself renders that the
  measurement above found with zero scrape coverage; this closes that for
  them, using each upstream chart's own existing toggle rather than a new
  template. `basicAuth` MIRRORs `storeCredentials`, refused if it
  disagrees. Does **not** close the gap for `charts/observability-emitters`'
  vmagent, vlagent or OpenTelemetry gateway — that chart's own scrape
  coverage is its own decision.

- **New chart `charts/observability-dashboards`** — the generic
  dashboards, shipped as their own artifact into Grafana's namespace
  rather than left to the sidecar's own cluster, per docs/dashboards.md.
  One ConfigMap per dashboard, labelled `grafana_dashboard: "1"` for the
  sidecar and annotated `k8s-sidecar-target-directory` for its folder.

  The generic set is fetched from the same upstream URLs the store
  chart's own `defaultDashboards.sources` names — VictoriaMetrics
  (single-node, vmagent, vmalert, operator), VictoriaLogs (single-node,
  vlagent), VictoriaTraces (single-node), Alertmanager, node-exporter-full
  and kubelet — pinned by release and committed under
  `charts/observability-dashboards/dashboards/`, never resolved at render
  time. `just dashboards` re-fetches; `hack/dashboards.sh` and
  `hack/dashboards/sources.yaml` say where from and why pinned there.

  Every one of the ten is rewritten to the same contract: a `datasource`
  variable every panel uses instead of a literal UID, a `cluster`
  variable chained off it and populated by a label-values query, `$cluster`
  in the title, and — where a dashboard is namespace-scoped, which only
  Alertmanager's is — a `namespace` variable chained off `cluster`.

  New values: `datasources.{metrics,logs,traces}` (the provisioned
  Grafana datasource UIDs a dashboard is pointed at; only `metrics` is
  used by the shipped set), `folders.{infrastructure,stores}` (the two
  folders the shipped set files into), `dashboards.<name>.enabled` (one
  key per shipped dashboard), and `extraDashboards` (the estate's own,
  held to the same contract, filed into their own named folder).

- **New six-rule dashboard lint**, `pkg/dashboardlint` and the
  `dashboardlint` binary it ships as `cmd/dashboardlint`, run with `just
  dashboard-lint` — on this chart's own set by default, or against any
  dashboard JSON an estate names. The rule that matters most: a panel
  pinned to one datasource UID is how a fleet dashboard silently becomes
  a one-install dashboard, and the lint fails on any panel whose
  datasource is a literal. The other five: a `cluster` variable used in
  every query, a `namespace` variable chained off it where the dashboard
  is namespace-scoped, `$cluster` in the title, the environment tier
  never a selector, and upstream's own `namespace` label accepted beside
  `k8s_namespace_name`. `just check` now runs it in CI on the shipped
  set; every rule has its own fixture under
  `tests/dashboardlint/invalid/`.

## 0.4.1

One background task, off, and the empty panel it explains.

- **New value: `charts/observability-stack`** —
  `victoria-traces-single.server.extraArgs['servicegraph.enableTask']`,
  default `"false"`. It turns on the Jaeger dependency graph (Grafana's
  service map, over `api/dependencies`), which the trace store computes
  with a background task that upstream ships disabled — this chart now
  writes that default out explicitly instead of leaving it to upstream's
  own.

  Off looks like a bug and always has: the endpoint answers `200` with
  `{"data":[],"total":0}` rather than an error, so a consumer sees an
  empty panel with nothing naming the cause, even on an install where
  the trace reads and context propagation both work. That is the whole
  reason the flag is now spelled out where somebody tuning the store can
  find it, alongside its companions — `taskInterval`, `taskLookbehind`,
  `taskLimit`, `taskTimeout`, `databaseTaskLimit` — rather than left to
  a background task nobody knew existed.

  It is upstream-experimental and only supported on a single-node or
  vtstorage deployment, which is this chart's shape. Whether the graph
  it computes is written back into the store, and so costs retention
  headroom and write throughput, is inferred from the endpoint's
  existence, not measured.

  **Nothing to do on upgrade.** The default is upstream's own behaviour,
  now written down.

- **`docs/safety.md`** — *The trace store speaks two dialects, and
  neither one completely* corrected: an empty service map was said to be
  "an empty graph, not a broken route", which is true but was
  incomplete. By default nothing computes the graph at all, which is why
  it is empty.

- **`docs/reference.md`** gains the value.

## 0.4.0

The one router, and the retirement of the shape it replaces.

- **New value: `charts/observability-stack`** — `notifications`, the
  routing tree docs/notifications.md describes: receiver kinds (Slack,
  named webhooks), severity defaults, a project route list matched on
  cluster and namespace, and a bridge to a webhook beside the normal
  route. The chart renders group_by, the group/repeat intervals, an
  inhibit rule (a `critical` silences the matching `warning`), and the
  Slack message template — cluster and namespace in the title, the
  alert's summary, a runbook link, a Grafana link built from the alert's
  labels, and a silence link. Every receiver's secret is mounted and
  read with `*_url_file`, the same mechanism the deadman's webhook
  already used; none is ever interpolated into the rendered config.

  **`alertmanager.config` is gone.** It was a free-form object in
  Alertmanager's own syntax, and the chart could not tell an empty one
  from a working one — which is exactly how every install used to end
  up routing to a receiver named `blackhole`: every rule evaluated,
  Alertmanager accepted every alert, and the result reached nobody,
  with nothing anywhere reporting it. `alertmanager.enabled` now refuses
  to render until `notifications` configures at least one receiver kind
  and a default for both `critical` and `warning` — see docs/safety.md
  for the rest of what it refuses and why each one is silent otherwise.

  An existing `alertmanager.config` block does not carry over: replace
  it with `notifications` before upgrading, or the render will refuse.

  `notifications.externalUrl` defaults to `vmalert.externalUrl` — they
  are the same fact, the base URL a link leaving the cluster should
  point at, and the chart refuses if both are set and disagree. Set
  `vmalert.externalUrl` and the Slack template's Grafana link works too,
  with nothing else to set; `notifications.externalUrl` stays a
  separate value because `vmalert.externalUrl` still has to work on its
  own for an install with `alertmanager.enabled: false`, which renders
  no `notifications` block at all.

- **Two defaults that made an alerting path lie about itself, fixed
  everywhere they apply, not values:**

  - Every vmalert this chart renders now carries `-remoteWrite.url`
    **and** `-remoteRead.url` against the metrics store — it already
    wrote that state, but never read it back. vmalert keeps every
    `for:` timer's state there, and without the read half a restart
    resets every pending timer to zero: a rule with `for: 30m` that was
    25 minutes into firing has to start over, and in a cluster that
    rolls its pods more often than that it can never fire at all.
  - The logs alerter's `-rule.evalDelay` drops from vmalert's own 30s
    default to 5s. That default exists to match VictoriaMetrics'
    `-search.latencyOffset`, which withholds a metrics query's newest
    samples because they may still be incomplete; VictoriaLogs makes no
    such promise and needs no such offset, so inheriting it held every
    log-based alert back by half a minute for a latency the log store
    does not have.

- **`docs/reference.md`** and **`docs/safety.md`** gain the value list
  and the refusal table for `notifications`. docs/notifications.md is
  the design this release implements; its "Store self-alerts" section
  ships separately.

- **New chart `charts/alert-ingress`, and a new image, `cmd/alert-ingress`**
  — turns a cloud provider's own notification topic (a threat-detection
  finding, a root sign-in, a signing operation on a key that should
  never sign, a budget crossing its line) into an alert on the same
  Alertmanager `notifications` now routes everything else through,
  rather than a second router with its own silences to keep. Every
  message is signature-verified against a certificate fetched only from
  the provider's own signing domain, pinned by pattern in the binary; a
  subscription is confirmed only for an allow-listed topic; a message no
  mapping rule matches is never dropped — it becomes `CloudEventUnmapped`
  rather than vanishing. The chart renders its own deadman `VMRule`,
  because a notification service retries and then gives up quietly and
  nothing else would say so.

  **`image.repository` has no default and is a required value.** This
  repository has never built or published an image for a Go binary —
  every release to date has been chart-only, via goreleaser's
  `builds-skip` — and this one does not change that: no image-build
  workflow was invented for it. Point it at wherever your estate builds
  and pushes `cmd/alert-ingress` from this tag.

  **`networkPolicy.allowCloudHTTPS`** (default `true`) is a decision for
  the installing estate, not a footnote. Verifying a signature and
  confirming a subscription both need HTTPS egress to the cloud
  provider, and vanilla Kubernetes NetworkPolicy has no way to pin
  egress to a hostname — only to a peer selector or a CIDR block. The
  rendered policy says so rather than pretending otherwise: Alertmanager
  (peer-scoped, required), cluster DNS, and HTTPS to anywhere. Turn it
  off only if your cluster's CNI enforces FQDN-scoped egress and the
  real signing domain is pinned there instead.

- **New package: `pkg/statusbox`, and its first provider,
  `pkg/statusbox/lightsail`** — the watcher outside: a small virtual
  machine, provisioned by a Pulumi call, running several Gatus instances
  behind a tunnel and a private network with no inbound port open. It is
  what receives the deadman (the alert that fires when this chart's own
  Alertmanager has stopped) and what carries a public status page,
  because both have to live somewhere the estate's own failure cannot
  reach. docs/statusbox.md is the design; docs/target-state.md has the
  smallest worked example.

  `setup.sh` — the script the box actually runs — is a release asset of
  this repository, not something a consumer writes or copies. A box's
  Pulumi call pins only a `Version`; at deploy time it fetches that
  release's `checksums.txt` and bakes `setup.sh`'s sha256 into the box's
  own boot script, which refuses to run a `setup.sh` whose checksum does
  not match. Nobody hand-copies a hash, and nothing about the script is
  a moving target.

  **The box is immutable.** The provider applies the rendered boot
  script once, at creation, so changing an instance's Gatus
  configuration — or bumping `Version` — **replaces the box**: about two
  minutes of status-page blip while its data disk reattaches to the new
  instance, with the SQLite history on it intact. There is no
  in-place config update to ask for.

  Two operational facts worth knowing before they are a surprise rather
  than a line in this entry: the rendered boot script is capped at 16 KB
  (Lightsail's own user-data limit) and `pkg/statusbox` refuses to
  render past it rather than produce a box that silently fails to boot;
  and that boot script — tailnet key, tunnel token, alert-push URLs and
  all — is readable in plain text from the instance metadata service by
  any process running on the box. Accepted rather than worked around,
  because the box is single-purpose, the tailnet key is spent at first
  boot, and an alert URL is rotated the day the box is ever asked to be
  anything else.

## 0.3.10

Documentation and a probe, no render change.

- **`hack/trace-api.sh`** — probes a live trace store for the Jaeger and
  Tempo endpoints a Grafana datasource calls, and prints which of them it
  implements. The store answers **both** dialects on `/select/jaeger/*`
  and `/select/tempo/*` and implements neither completely; an endpoint it
  does not implement returns 400 `unsupported path requested`, which a
  datasource reports as a failed query rather than as a missing feature.

- **`docs/safety.md`** gains *The trace store speaks two dialects, and
  neither one completely*, with the measured table — including **why the
  chart's default trace datasource is `type: jaeger`**: Grafana's Jaeger
  datasource asks for operations by the nested path
  `api/services/{service}/operations`, which the store has, while
  Jaeger's own UI moved to the flat `api/operations?service=`, which it
  does not. A Tempo datasource is the shape to avoid, because
  `api/status/buildinfo` is how it decides what the backend supports.

  It also records the two 400s that look like outages and are not: an
  Explore search with no service selected is refused correctly by the
  store, and `api/dependencies` answers `{"data":[]}` rather than
  refusing, so an empty service map is an empty graph and not a broken
  route.

- **`docs/reference.md`** and the datasource comment in `values.yaml` say
  the same thing where somebody changing the type will read it.

## 0.3.9

One metrics endpoint becomes an estate's decision instead of an
unexplained 401.

- **New value: `charts/observability-stack`** —
  `tenancy.allowUnfilteredMetricMetadata`, default `false`, admits
  `/api/v1/metadata` on the metrics read route.

  The endpoint returns every metric **name** in the store with its type
  and help string, and no filter reaches it: measured against the store,
  an `extra_filters` naming a namespace that matches nothing returns the
  same body as no filter at all. Where every grant is `allNamespaces` it
  discloses nothing a principal could not already query; where grants are
  per-namespace it is an inventory of what another tenant runs. That is a
  judgement about an install, so it is a value.

  Leaving it off has always been the behaviour — the omission was
  deliberate and reasoned in `pkg/tenancy`. What was missing is that the
  cost of off is **visible and looked like a fault**: Grafana's
  Prometheus-family datasources ask for this endpoint to put descriptions
  on metric names, and write

      path=…/resources/api/v1/metadata status=401

  into their own logs. Nothing is broken — metric names come from
  `/label/__name__/values`, which IS filtered, so only descriptions are
  missing — but an operator reading that 401 should find a value rather
  than a mystery.

  **Nothing to do on upgrade.** The default is the old behaviour.

- **Unchanged, and now tested as such** — `/api/v1/status/active_queries`,
  `/api/v1/status/top_queries` and `/api/v1/status/metric_names_stats` are
  admitted by no value. Two return other principals' query *text* and one
  returns names with per-tenant counts, so unlike the metadata endpoint
  there is no install for which routing them is correct. A wildcard on
  this route once admitted all four together; `tests/metadata_test.go` and
  `TestMetricMetadataOptIn` now fail if any of them reappears, and both
  were shown failing on the shapes they exist to catch.

- **`pkg/tenancy`** gains `Config.AllowUnfilteredMetricMetadata` and the
  exported `MetricMetadataPath`, so the library and the chart still render
  the same routes.

- **Docs** — `docs/safety.md` gains *Four metrics endpoints, one of them a
  decision*; `docs/reference.md` gains the value.

## 0.3.8

Grafana here could be configured into two shapes that look right and are
not: more replicas than its database can serve, and a store nobody can
query.

- **New refusal: `charts/observability-stack`** — `grafana.replicas`
  above one is refused unless `grafana.ini`'s `[database] type` names
  something shared.

  Grafana's default is SQLite, a file on the pod. Two replicas on it are
  either two separate databases — a dashboard saved on one is missing
  from the other — or one ReadWriteOnce volume with two processes writing
  it, which answers `500 database is locked` on whichever request loses
  while the rest of the UI keeps working. The pods are Running and Ready
  throughout.

  The replica count and the database are one decision, and they were two
  values in different parts of the file set by different people at
  different times.

  **On upgrade**: an install running more than one replica on SQLite now
  fails to render. It was already losing data. Point `[database]` at
  Postgres or MySQL — the password belongs in
  `envValueFrom.GF_DATABASE_PASSWORD`, since `grafana.ini` renders into a
  ConfigMap — or drop to one replica.

- **New refusal: `charts/observability-stack`** — an enabled store that
  no Grafana datasource type reads is refused, when Grafana is enabled
  here.

  A store with no datasource ingests, retains and answers exactly as if
  it were being read. Nothing is unhealthy and no metric moves the wrong
  way; the only symptom is that nobody ever looks at it.

- **Fix: `charts/observability-stack`** — the default datasource list
  gains **VictoriaTraces**. It had the fault above: the trace store is
  enabled by default and only metrics and logs were readable.

  It is `type: jaeger` — the store serves the Jaeger select API and
  Grafana ships that datasource in core, so there is no plugin to
  install. Note that the three stores take three unlike URL shapes; see
  the table in `docs/safety.md`.

- **Docs** — `docs/safety.md` gains *Grafana's replica count and its
  database are one decision* and *A store nobody can query*;
  `docs/adoption.md` gains the database prerequisite; `docs/reference.md`
  gains the three values.

## 0.3.7

Every span a writer sent went to the **log** store and was rejected. The
trace store had never held anything.

- **Fix: `charts/observability-stack`** — the log store's write routes are
  now enumerated instead of `/insert/.*`.

  vmauth matches a VMUser's `src_paths` in the order its `targetRefs`
  render and stops at the first hit, and a writer's routes render metrics,
  logs, traces. `/insert/.*` matched `/insert/opentelemetry/v1/traces`
  before the trace store's own route was reached, so spans were posted to
  the log store, which answered:

  *Permanent error: rpc error: code = InvalidArgument desc = error
  exporting items, request to …/insert/opentelemetry/v1/traces responded
  with HTTP Status Code 400*

  The collector treats that as permanent, drops the batch and moves on.
  Nothing was unhealthy, every query answered, and the trace store stayed
  empty — which is indistinguishable from an estate that emits no spans.

  The catch-all also routed `/insert/multitenant/*`, the endpoint a writer
  uses to NAME the tenant it writes to. Deciding that is what this proxy is
  for, so the route around it is gone with it.

  **Nothing to do on upgrade**, unless a writer here ingests logs through
  an endpoint outside the store's own list — the routes are now
  `/insert/{datadog/api/v2/logs,elasticsearch/_bulk,journald/upload,jsonline,loki/api/v1/push,native,opentelemetry/v1/logs,splunk}`.
  Add an endpoint deliberately rather than widening one back to a pattern.

- **New refusal: `charts/observability-stack`** — the render now refuses
  any route declared before another store's that also matches it, naming
  both. `tests/routing_test.go` holds the same property against the routes
  as they are ORDERED in a rendered VMUser, which is what vmauth actually
  reads.

  How it was found: by sending one span and then asking the *store*
  whether it had arrived. The sender's 200 said nothing — it only means
  the collector accepted the batch for its queue.
## 0.3.6

The gateway's release never converged. `charts/observability-emitters`
rendered its `volumeClaimTemplates` entry without `apiVersion` or `kind`,
the API server defaults both in, and continuous delivery then compared
what it rendered against what the cluster holds and reported the release
**OutOfSync for ever** — with nothing to converge on, because each sync
writes the same manifest and the server adds them back.

- **Fix: `charts/observability-emitters`** — the queue's
  `volumeClaimTemplate` spells out `apiVersion: v1` and
  `kind: PersistentVolumeClaim`.

  Declaring the defaults is the fix rather than teaching a differ to
  ignore those fields: an ignore rule would hide a real change in the same
  field later, and this costs nothing. Both upstream stores already did
  it, which is how the shape was recognised.

  **Nothing to do on upgrade.** The rendered object is the same one the
  cluster already holds; the release simply stops reporting a difference.

- **Check: `tests/volumes_test.go`** — every `volumeClaimTemplate` in
  every golden must declare both. Stated about the shape rather than this
  chart, because every claimed volume has the same trap.

## 0.3.5

**Every kubelet and cadvisor series was being discarded by the store**, and
every counter on the writing side said success. Measured on a live
cluster, because nothing else can see this.

- **Fix: `charts/observability-emitters`** — the node scrapes no longer
  `labelmap` every node label onto every series.

  That snippet is conventional and it is unbounded by construction: the
  labels belong to the cloud provider, not to this chart. On EKS a node
  carries around forty (`eks_amazonaws_com_instance_*`, karpenter,
  topology), so each kubelet and cadvisor series arrived with **46 to 52
  labels** — past VictoriaMetrics' `-maxLabelsPerTimeseries=40`.

  What the store does then is the part worth knowing: it **ignores the
  series and answers 200**. The agent reported 888k rows written, zero
  errors, zero dropped. The store held none of them. The only record
  anywhere was a warning in the store's own log.

  So node identity is now one label, `node`, from the node's name — the
  conventional name, and the one dashboards and recording rules join on.

- **New: `metrics.scrape.nodeLabels`** — node labels to copy, **by name**,
  empty by default. Breadth is asked for where somebody can count it,
  since every entry lands on every node series.

  **On upgrade:** if your dashboards join on a node label other than
  `node`, name it here. If they never worked, this is why.

- **Check: `tests/cardinality_test.go`** — no scrape config this chart
  renders may copy labels it has not named.

## 0.3.4

The OTLP gateway could not start, and could not be placed. Both were found
by installing the chart on a real cluster for the first time, which is
where this pair of defects had to be found: neither is visible to a render,
a lint, a golden, an API server or the operator.

- **Fix: `charts/observability-emitters`** — the gateway pod now sets an
  `fsGroup`, so the queue volume it declares is one it can write to.
  Before, it exited at startup on **any** cluster with a default
  StorageClass:

  *failed to build extensions: failed to create extension "file_storage":
  mkdir /var/lib/otelcol/queue: permission denied*

  A dynamically provisioned volume arrives owned by root and the collector
  image does not run as root; `fsGroup` is the only thing that bridges the
  two. It is a default rather than a value to discover, because a
  StatefulSet that declares a volume it cannot write to is not a
  configuration choice. Replace `otlp.podSecurityContext` wholesale if
  your policy differs.

  Nothing upstream of the cluster could see it: the template renders, the
  chart lints, the golden is ordinary, the API server accepts the object
  and the operator has no opinion. The only thing that disagreed was the
  container, after the volume was attached.

- **Check: `tests/volumes_test.go`** — every StatefulSet in every golden
  that claims storage must say who may write to it. Stated about the shape
  rather than this chart: a pod that asks for storage intends to write to
  it. The upstream stores already passed; ours was the only one that did
  not.

  **Nothing to do on upgrade.**

- **`charts/observability-emitters`** — new `otlp.nodeSelector` and
  `otlp.tolerations`, both empty by default, so nothing changes for an
  install that does not set them.

  This is the third of three rather than a new idea: the other two
  emitters already had a way to say where they run, and the gap was only
  visible on a cluster that is fully tainted, where the schema correctly
  refused the values an operator would reach for first.

  Worth stating because it is not symmetric: the gateway is **not** given
  the log agent's blanket `operator: Exists`. A log agent is a node agent
  and has to run everywhere or the logs it skipped are missing — and
  missing logs look exactly like quiet ones. The gateway is one replica
  per queue volume and should be placed deliberately, on a pool that is
  not reclaimed underneath it.

  **Nothing to do on upgrade.**

## 0.3.3

Both alerters loaded every rule in the cluster. `charts/observability-stack`
runs two vmalerts that speak different query languages, and gave each of
them `selectAllByDefault: true` with no selector.

- **Fix: `charts/observability-stack`** — each alerter now selects its
  rules by label. Before, the logs alerter (which runs with
  `-rule.defaultRuleType=vlogs`) was handed the metrics subchart's PromQL
  and **crash-looped**, because vmalert parses every rule at startup and
  exits on the first one it cannot parse:

  *cannot parse configuration file: errors(23): invalid expression for rule
  "TargetDown": bad LogsQL expr … probably, the whole string must be put
  into quotes*

  So every log rule stopped being evaluated too, and the reverse pairing —
  LogsQL reaching the metrics alerter — is the same failure the other way
  round.

  **The label is now load-bearing.** A rule written in LogsQL must carry
  `observability.rule-type: vlogs` to be evaluated; in `platform-alerts`
  that is the `ruleLabels` value. A PromQL rule needs no label, because the
  metrics alerter selects everything *not* marked as LogsQL — which is what
  keeps the rules other charts ship working untouched.

  **Nothing to do on upgrade unless you already ship LogsQL rules.** If you
  do, label them: until you do they are selected by nobody, and a rule
  nobody selects is a file on the cluster rather than an alert.

  Why it was invisible: an install with no rules yet is perfectly healthy.
  It fires the moment the first rules exist — for the metrics subchart,
  when its own sync job runs.

- **Check: `tests/selection_test.go`** — the property is about the pair, so
  the test is too. No rule shape may be selected by two alerters at once,
  and none may be selected by none of them: a selector pair that overlaps
  nowhere is otherwise satisfied perfectly by two selectors that match
  nothing, which is an install where no rule is ever evaluated and every
  pod is green.

## 0.3.2

The metrics alerter was refused by the API server. `charts/observability-stack`
wrote `extraArgs` on its VMAlert unconditionally while everything under it
was conditional, so the alerter that had no flags to set rendered the key
with an empty body — and an empty body is null, where the definition types
the field as an object.

- **Fix: `charts/observability-stack`** — the VMAlert renders `extraArgs`
  only when it has something to put there. Before, the metrics alerter
  rendered a bare `extraArgs:` and the API server refused the **whole
  object**:

  *VMAlert.operator.victoriametrics.com "…-metrics" is invalid:
  [spec.extraArgs: Invalid value: "null": spec.extraArgs in body must be of
  type object]*

  The logs alerter hid it, because its one flag (`rule.defaultRuleType`) is
  unconditional and so its body was never empty.

  What this costs is not the field. The resource never exists, so **the
  metrics alerter does not run and no metrics rule is evaluated** — while
  every other object in the release applies and reports healthy. In
  continuous delivery the whole sync is marked failed on that one resource,
  and on a self-healing install the same revision is not retried, so the
  next unrelated change to that cluster waits behind it.

  **Nothing to do on upgrade.** No value changes. An install that carries
  `vmalert.externalUrl` renders the same flag as before, now serialized by
  `toYaml` and therefore unquoted — the same string either way.

- **Check: `tests/typing_test.go`** — every custom resource these charts
  render is now walked against the definition this repository installs for
  its kind, and a value whose type the definition contradicts fails the
  test. This is the sibling of the pruning check: that one catches a field
  the API server silently *drops*, this one a value it *refuses*. The
  reasoning that left types out was that a refusal is loud — it is, but it
  is loud at apply, which is not a place anyone is watching. Fixtures under
  `tests/rejected/` prove it can fail.

## 0.3.1

The proxy had no Deployment. `charts/observability-stack` rendered
`unauthorizedUserAccessSpec: {disabled: true}` on its VMAuth, and that
field does not exist — no release of the VictoriaMetrics operator has ever
had it, so the CustomResourceDefinition this repository ships has no room
for it.

- **Fix: `charts/observability-stack`** — the VMAuth no longer renders
  `unauthorizedUserAccessSpec`. The API server pruned the unknown key and
  stored an unauthorized section that routes nowhere, which the operator
  then refused — *cannot build unauthorized_user config section: at least
  one of `url_map`, `url_prefix` or `targetRefs` must be defined* — so the
  VMAuth reported `failed`, no Deployment was created, and **there was no
  read path at all**. Nothing upstream of the cluster showed it: the
  template rendered, the render was valid, the chart linted and the golden
  was byte-identical to one that works.

  **The intent is unchanged, and absence is how it is expressed.** There
  must be no unauthorized user, because from vmauth v1.147.0 a token that
  verifies but carries no `vm_access` claim falls THROUGH to it rather than
  being rejected. Omitting the field is what produces that: the operator
  writes the `unauthorized_user` section of vmauth's configuration only
  when the field is set, nothing defaults it, and with it absent the key is
  never written. There is no "off" setting to write instead — the operator
  rejects a section with no route, so every shape it accepts is a shape
  that serves. Confirmed on a cluster running the pinned operator: with no
  section, a verified token carrying no claim, a garbage token and a
  request with no `Authorization` header are all answered **401**; with the
  smallest section the schema accepts, all three are answered **200**.

  **Nothing to do on upgrade.** No value changes, and an object already
  holding the pruned `unauthorizedUserAccessSpec: {}` has the field removed
  by the upgrade itself, after which the operator reconciles it
  `operational`.

- **New: rendered objects are checked against the CustomResourceDefinitions
  this repository ships.** `TestRenderedObjectsSurviveTheCRDs` walks every
  custom resource in every golden against the schema for its kind and fails
  on any field the API server would prune, and `TestNoUnauthorizedUser`
  fails if a rendered VMAuth carries an unauthorized section by either
  spelling. `tests/pruned/` holds manifests that pass every other check and
  are destroyed on apply, so the checker has to prove it can fail. The
  general shape — a pruned field is indistinguishable, in a rendered
  manifest and in every golden, from a field that works — is in
  docs/safety.md.

## 0.3.0

The audience pin, and every `match_claims` value meaning only itself.
vmauth checks a token's expiry and its issuer; who the token was minted
FOR is the caller's to state, and now has to be.

- **Breaking: `pkg/tenancy` and `charts/observability-stack`** —
  `Config.Audience` and `tenancy.audience` are new and **required**
  wherever a proxy configuration is rendered. The migration is one line:
  set it to the client id this proxy's own tokens are minted under. It is
  rendered into every reader's `match_claims` beside the group, under
  `aud`.

  **This narrows who the proxy admits.** vmauth validates a token's
  expiry and, with OIDC discovery configured, its issuer — and nothing
  else. It has no audience option and never inspects `aud` on its own. So
  until now a reader was selected by its group alone, and every unexpired
  token the issuer minted was admitted whatever client it was minted for:
  an estate whose issuer serves several applications was admitting a
  token a person holds for a different one, which then read that person's
  namespaces. Nothing reported it, because the token verified, the claim
  matched and the filters applied.
  - **The claim name is fixed, not an input.** `aud` is OpenID Connect's
    own name for it, and a second spelling of a spec-defined claim is how
    a configuration comes to read as though something were pinned when
    nothing is. `tenancy.claimName` may therefore no longer be `aud`:
    both are entries in one `matchClaims` map, and one would overwrite
    the other.
  - **Every `match_claims` value is now escaped and anchored** — the
    audience and the **group** alike, on both the library and the chart
    side. vmauth compiles each value as a regular expression, and neither
    value belongs to this repository: an issuer assigns a client id, an
    identity provider names a population. Escaped, so a client id with a
    dot in it pins that client rather than every id of the same length;
    anchored, so a group written `.*` matches the literal `.*` and no
    other token. **The rendered values change shape**: `groups:
    "example:k8s:viewer"` becomes `groups: "^(example:k8s:viewer)$"`, and
    `helm diff` shows it on every reader. It matches the same tokens it
    was meant to match and fewer of the ones it was not, so no grant
    widens and no principal that was reachable stops being reachable.
  - **Escaped rather than refused, unlike a cluster or namespace name.**
    Those names are the estate's own and are still refused outside the
    plain-name shape. A client id and a group name are handed to the
    estate by an identity provider it does not control — issuers mint
    ids with dots in them — so refusing a shape we do not control would
    be an outage with no alternative available to the operator. The rule
    is: refuse what we name, escape what we are handed. What is still
    refused on the audience is a value no issuer mints: one carrying
    whitespace or a newline, which is how a value that arrived from the
    wrong place looks.
  - **The anchors are rendered although vmauth anchors too**
    (`^(?:…)$`, since v1.152.0, which is already this design's floor):
    a narrowing control that works only when the binary in front of it is
    patched is a control with a version number in it. Both sides are
    tested under both compilations.
  - **A list `aud` needs no special case.** vmauth tests a
    `match_claims` entry against an array claim element by element, so
    the pin works whether the issuer mints the claim as a string or as a
    list.
  - Three negative fixtures under `tests/invalid/observability-stack/`
    (no audience beside principals, an audience that is not an identifier,
    the groups claim named `aud`), the Go refusals beside them, and the
    rendered values walked in the OUTPUT on both sides — the chart's
    VMUsers and the library's users — because a pin both sides dropped
    would leave every comparison between them satisfied.

  Adopting: register a client for this proxy if there is not one already,
  and set `tenancy.audience` / `Config.Audience` to its id. A render
  refuses until you do. Readers whose tokens are minted for that client
  are unaffected; readers arriving with a token for some other client of
  the same issuer stop being admitted, which is the point.

## 0.2.0

The vocabulary rework. `tenant` × `env`, derived from namespace labels,
is retired; the scoping key is the **cluster and the namespace**, under
OpenTelemetry's names, and the environment tier rides along as a
descriptive dimension that is never a key.

- **Breaking: `pkg/tenancy`, `charts/observability-stack`,
  `charts/observability-emitters`** — one coordinated change, and the
  migration is two lines: a grant's `env` becomes `cluster` and its
  `tenants` become `namespaces` (`allTenants` → `allNamespaces`); the
  emitters' `tenancy` block is replaced by `tenancy.cluster` and
  `tenancy.environment`. What every writer stamps and every filter
  selects on, per signal:

  | dimension | metrics label | log field (both writers) | span attribute |
  |---|---|---|---|
  | cluster — key | `k8s_cluster_name` | `k8s.cluster.name` | `k8s.cluster.name` |
  | namespace — key | `k8s_namespace_name` (`namespace` stays too) | `kubernetes.pod_namespace` | `k8s.namespace.name` |
  | tier — descriptive | `deployment_environment_name` | `deployment.environment.name` | `deployment.environment.name` |

  A project is not a label on the telemetry any more: it is a
  derivation from a name to the namespaces it owns, held with whoever
  writes the grants. That is what removes `tenancy.namespaceLabels`,
  `tenancy.fallbackTenant`, `tenancy.tenantLabel` and `tenancy.envLabel`
  from the emitters chart — a namespace always has a name, so the
  unlabelled-namespace case, the fallback tenant, and the no-fallback gap
  on the log path all stop existing. The tier is never a key because two
  clusters can share one.
  - **`Grant{Env, Tenants, AllTenants}` is `Grant{Cluster, Namespaces,
    AllNamespaces}`**; the empty-list refusal, the hostile-name refusals
    and every message stay, rewritten to teach the new model.
  - **The keys are inputs with defaults on both paths.** `ClusterLabel`
    / `NamespaceLabel` (`tenancy.clusterLabel` / `tenancy.namespaceLabel`)
    default to `k8s_cluster_name` / `k8s_namespace_name`;
    `LogsClusterField` / `LogsNamespaceField` (`tenancy.logsClusterField`
    / `tenancy.logsNamespaceField`) default to `k8s.cluster.name` /
    `kubernetes.pod_namespace`. The log fields had no default in 0.1.0
    because the field was derived from an estate's own namespace label,
    which every estate spelled differently; the key is the namespace's
    name now, which every estate spells the same way, and the default is
    the field the container-log agent natively writes. A metrics key is
    held to the Prometheus label shape rather than the plain-name shape,
    because the defaults carry underscores.
  - **The two log writers name the namespace the same way**, which
    0.1.0's docs/safety.md listed as not fixed. The container-log agent
    cannot rename a field, so its native `kubernetes.pod_namespace` is
    the key and the gateway writes that spelling on its log pipeline
    beside `k8s.namespace.name`; cluster and tier reach the agent
    through `-kubernetesCollector.extraFields` under the exact
    conventional names, so the two agree for free. `extraFields` is a
    mirror of `tenancy` and is refused when it disagrees.
  - **OTLP-derived metrics carry the keys as labels.** The Prometheus
    remote-write exporter puts a resource attribute on `target_info`
    and nowhere else unless told to promote it, so a series would have
    reached the store with no cluster and no namespace and every scoped
    query would have missed it. The gateway's metrics exporters now
    promote exactly the three through `resource_constant_labels`
    (dots to underscores on the way out), verified against the collector
    binary the chart pins. The gateway's components are also declared
    under the pinned version's current type names —
    `prometheus_remote_write`, `otlp_http`, `delta_to_cumulative` — since
    the old aliases log a deprecation warning at 0.161.0; and the queue
    extension now creates its directory, because a fresh volume is empty
    and the extension refuses to start on a directory that does not
    exist, which would have crash-looped every first boot on a new
    PersistentVolume. Both found by running the rendered file.
  - **The gateway strips a namespace an SDK claims before resolving the
    pod**, in a `transform/disown` step, because `k8sattributes` writes
    an attribute only when it is absent — a resource that arrived
    carrying `k8s.namespace.name` would have kept the application's
    claim, and the namespace is now the key. The pod is resolved from
    the connection first; the pod-UID and pod-IP attributes an SDK
    supplies are fallbacks.
  - **The metrics agent** relabels `k8s_namespace_name` from service
    discovery on every scrape object, copies the container's own
    `namespace` into it on the two node-level jobs after the scrape,
    stamps the cluster and the tier statically, and passes the Helm
    release through as `app_kubernetes_io_instance`. `attachMetadata.namespace`
    on the scrape class, and its refusal, are gone: the namespace name
    needs no metadata. The `overrideHonorLabels` guard and its
    `exported_(…)` labeldrop, and the "default scrape class stamps
    nothing" refusal, are re-targeted to the three new labels.
  - **The Helm release passes through everywhere and is never a stream
    field**: pod labels are on for the container-log agent (upstream's
    default, reversed from 0.1.0), the gateway extracts
    `app.kubernetes.io/instance` as `k8s.pod.labels.app.kubernetes.io/instance`,
    and the stream-field allow-list refuses it.
  - **`tests/agreement_test.go` walks the rendered output of every
    writer for every dimension** — the metrics agent's two scrape
    shapes, the container-log agent's flags, and the gateway's three
    pipelines — against the names read back out of a rendered library
    filter. Negative fixtures: a blank `tenancy.cluster`, a blank
    `tenancy.environment`, a hostile value of either, a static-fields
    mirror that disagrees or is not JSON, either log key missing from
    either writer's stream fields, the release label as a stream field,
    and the two keys colliding on the stack chart.

  Adopting: rename the grant fields, replace the emitters' `tenancy`
  block, write the log agent's `extraFields` as the chart tells you to,
  and drop `logsTenantField` / `logsEnvField` unless your log agent is
  not this chart's. Existing data stamped `tenant`/`env` is not
  rewritten; it stops matching grants once the proxy is upgraded, which
  is the intended shape rather than a migration to run.

## 0.1.0

The first release. Everything below is new to a consumer, so the two
authorization fixes among these entries describe defects that **never
reached a published version** — they were found and fixed between the
repository being created and this tag. Nobody ran them.

They are written up anyway, at length, because the mechanisms are the
ones an operator has to understand to run this safely, and because each
one is a shape that could come back.


- **`pkg/tenancy` and `charts/observability-stack`** — the proxy now
  APPLIES the filters it renders. **This is an authorization fix: before
  it, every principal who passed JWT verification read every tenant's
  metrics and logs.** A `vm_access` claim does nothing on its own —
  vmauth applies it only by substituting a placeholder into the route it
  forwards on, and the routes carried none, so each principal's claim was
  verified, computed, written into the manifest and then discarded. Every
  read route now carries its filter argument
  (`extra_filters={{.MetricsExtraFilters}}` for metrics,
  `extra_stream_filters={{.LogsExtraStreamFilters}}` for logs), a route
  cannot be constructed without one, `Validate` refuses one that lost it,
  and tests on both sides walk every rendered route and fail on any that
  does not carry it. Four things follow:
  - **Traces cannot be scoped at all, and now say so.** VictoriaTraces'
    Jaeger and Tempo select APIs accept no query argument a proxy could
    put a filter in. With a trace store and `principals` both set, the
    chart refuses to render until `tenancy.allowUnfilteredTraceReads` is
    `true` and the library until `AllowUnfilteredTraceReads` is set —
    which records that every principal who can reach the proxy reads
    every tenant's spans. It admits the trace route and nothing else.
    **The trace store is enabled by default, so an existing values file
    with principals in it will refuse to render until this is answered.**
  - **The logs filter is now prefixed `_stream:`.** VictoriaLogs reads an
    `extra_stream_filters` value beginning with `{"` as its JSON object
    form, and every filter rendered here begins with `{"` because a log
    field name has to be quoted — so the unprefixed value would have
    failed to parse on every log query once it started being sent.
  - **Two metrics routes are gone.** `/api/v1/metadata` and everything
    under `/api/v1/status/` except `tsdb` and `buildinfo` take no filter,
    so no filter narrows them: they returned metric names, and other
    principals' query text, across every tenant.
  - **`vmauth.extraArgs.mergeQueryArgs` naming a filter argument is
    refused.** The clash between a client's query argument and the
    route's is the other half of the enforcement, and that flag exempts
    an argument from it; vmselect ORs `extra_filters` alternatives, so a
    caller adding an empty one would read every tenant.
  - **A claim can no longer be rendered empty.** An empty filter list
    does not deny anything — it removes the query argument, and with it
    the clash that stops a caller supplying its own.

  Needs no action beyond answering the traces question, and `helm diff`
  before the upgrade shows the new `query_args` on every reader's routes.
  See docs/safety.md, "A filter that is computed and never applied".

- **`pkg/tenancy` and `charts/observability-stack`** — the logs filter
  names the field the log store actually has. Until now both rendered the
  same string for both signals, so a reader querying logs through the
  proxy was filtered on `tenant` — a field the log path does not have and
  cannot have, because vlagent can rename no field and a namespace label
  arrives as `kubernetes.namespace_labels.<key>`. The query did not fail;
  it returned nothing, which reads as "my service logged nothing".
  **Breaking, and deliberately so:** `tenancy.logsTenantField` and
  `tenancy.logsEnvField` on the chart, `LogsTenantField` and
  `LogsEnvField` on `tenancy.Config`, are now required whenever there is a
  principal, and there is no default — every default anyone would write is
  right on one estate and silently wrong on the next. With
  `charts/observability-emitters` they are
  `kubernetes.namespace_labels.<tenancy.namespaceLabels.project>` and
  `tenancy.envLabel`, which that chart already refuses to render without.
  Two further changes follow from LogsQL rather than from taste: the field
  name is quoted and held to a field shape (`^[a-zA-Z0-9_][a-zA-Z0-9_./-]*$`)
  rather than to the plain-name shape a tenant is held to, since a real
  field name carries dots and a slash; and a principal now gets **one**
  stream filter with its grants as `or` alternatives instead of one entry
  per grant, because VictoriaLogs AND-s every `extra_stream_filters`
  argument it is given — two entries naming two environments intersected
  in nothing, so the principal with the most access got the emptiest
  screen. The metrics and traces paths are unchanged. See docs/safety.md.

- **`pkg/tenancy`** — the default `vm_access` claim is rendered inside
  the token block rather than beside the route map. vmauth's user object
  has no such field and its parser is strict, so the misplaced version
  did not merely lose the default: vmauth refused the whole
  configuration file and exited, and the proxy never started. Caught by
  running a rendered configuration against the binary; a test now asserts
  the nesting against the marshalled output rather than against our own
  structs.

- **`charts/observability-emitters`** — per-cluster collection: vmagent as
  a `VMAgent` the operator reconciles, vlagent from the vendor's own
  DaemonSet chart, and an OpenTelemetry gateway this chart renders itself.
  Each is optional, each replicates to every destination it is given with
  its own on-disk buffer, and each stamps `tenant` and `env` from the
  **namespace's** labels — an application that sets them itself has them
  overwritten. Install `charts/observability-crds` first: the chart
  renders `PodMonitor` objects, and on a cluster without those CRDs every
  other chart's monitor template renders nothing at all, silently, with a
  successful sync. Twenty-seven refusals, each with a fixture, and the three
  worth knowing before you write the values file: `overrideHonorLabels`
  cannot be turned off, because a target that exports its own `tenant`
  label would otherwise choose its own tenant; `remoteWrite.shardByURL` is
  refused outright, because it splits the series between a redundant pair
  instead of replicating to both and every query still answers with half
  of every result missing; and a buffer on an emptyDir is refused for all
  three emitters, including the log agent's, where the same volume holds
  the checkpoint that stops it re-reading every container log from the
  beginning on each rollout. Five values have no default and are asked for
  rather than guessed — `tenancy.env`, `tenancy.fallbackTenant`,
  `tenancy.namespaceLabels.project`, `writeCredentials.secretName` and a
  destination list per emitter — because each of them renders, runs and
  reports healthy when it is wrong. **One thing to carry out of the
  chart:** on the log path the tenancy stream field is
  `kubernetes.namespace_labels.<your project label key>` and **not**
  `tenant`, because vlagent cannot rename a field; a proxy filtering on
  `tenant` against those streams returns an empty result rather than an
  error. See docs/safety.md.
- **`charts/observability-stack`** — one install of the store: the
  VictoriaMetrics family from the vendor's own pinned charts, with the
  proxy, the two vmalerts, Alertmanager, the network policies and the
  backups this chart renders itself. Reads go through vmauth, which
  verifies the caller's token against an OIDC issuer and injects the
  filters that token is entitled to; `pkg/tenancy` renders the same
  principals into the claim an issuer mints, and a test compares the two
  so they cannot drift. Single-replica: `ha` is accepted and refuses fewer
  than two zones, and the zone-redundant behaviour lands in a later
  release. Install `charts/observability-crds` first and have cert-manager
  present — the operator's own `crds.enabled` is off here, and its webhook
  certificate comes from cert-manager rather than from a self-signed CA
  the chart would regenerate on every upgrade. Seventeen refusals, each
  with a fixture: a retention without a unit (a bare number is months), the
  two disk guards that are mutually exclusive at the binary, a fractional
  CPU (the store rounds it down and buys one thread), an `enterprise` image
  tag, a licence flag, a vmauth below v1.152.0, a Grafana datasource
  without `oauthPassThru`, and the rest in docs/safety.md. Three values are
  written twice because Helm cannot compute a subchart's values; the chart
  refuses to render when a pair disagrees.
- **`charts/observability-crds`** — the CustomResourceDefinitions this stack
  needs, as a release of their own: the VictoriaMetrics operator's, and the
  `PodMonitor`, `ServiceMonitor`, `ScrapeConfig` and `Probe` kinds every
  component authors its scrape objects in. Install it at a wave ahead of
  the stack with `prune: false` and `ServerSideApply=true`, and turn the
  operator chart's own `crds.enabled` off — Helm never upgrades a CRD it
  installed from a chart's `crds/` directory, so a set with two owners is
  a schema that drifts behind the controller reading it. Both upstreams are
  pinned; every render ends with the kinds it carries and the version each
  one stores, which is what a bump is reviewed against. Install it before
  any chart that offers a monitor template and before switching such a
  value on: a chart whose monitor is gated on
  `.Capabilities.APIVersions.Has "monitoring.coreos.com/v1"` renders
  nothing when the kind is absent and still reports a successful install.
- **`charts/platform-alerts`** — the rules that fire when something has
  stopped working while everything still looks green: a CronJob no longer
  being scheduled, a store whose write path has died, a volume that was
  never mounted, a store approaching its own read-only limit. There is no
  default store list: the chart refuses to render until the counters are
  named, because a guessed metric name renders cleanly and then never
  fires.
- **`pkg/tenancy`** — one input, two shapes: a vmauth configuration and
  the `vm_access` claim an issuer mints. A tenant or environment name
  outside the plain-name shape is refused rather than escaped, because a
  name carrying `|` or `.*` would widen the grant it appears in.

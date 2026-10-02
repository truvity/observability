# High availability: a pair of stores

None of the three stores replicates across a zone in a way that survives
one. High availability here is therefore **two independent installs of the
stores, with the writers holding the redundancy**: every collector sends to
both, buffers per destination, and a read is answered by whichever store is
up. This page is how `charts/observability-stack` builds that pair, what
each half renders, what a zone loss costs, and how to replace a replica.

The chart supports both shapes. The single install is the default and
renders exactly what it always did; the pair is opt-in.

## The shape

Two releases of the same chart, in one namespace:

| | Primary | Replica |
|---|---|---|
| `mode` | `full` | `replica` |
| `ha` | `{enabled: true, peer: {...}}` | `{enabled: true}` |
| Stores (metrics VMSingle, logs, traces) | yes | yes |
| Their NetworkPolicies and scrape objects | yes | yes |
| Operator | yes, and it reconciles the replica's VMSingle too | no |
| vmauth, vmalert, Alertmanager, karma, Grafana | yes | no |
| Backup | yes | **refused** |
| Store self-alerts (`storeMemory`) | optional | optional |

A pair is two releases rather than two copies of the stores in one release
because the metrics store is part of the vendored `victoria-metrics-k8s-stack`
chart, which bundles the operator and cannot be aliased twice in a release.
Two releases are also what "independent" means in practice: a bad change to
one is synced, rolled back and judged on its own.

`mode: replica` renders only the stores. Confirmed against the vendored
chart: with its operator and sync Job off, `victoria-metrics-k8s-stack`
renders exactly one object, the `VMSingle`; the operator of the primary
release reconciles it (the operator watches the namespace). The mode is a
contract in the sense `operator-only` is: it refuses, naming the key, every
component it does not run that is still switched on.

### What the primary adds under `ha.enabled`

- **Reads over both.** Every reader route (`metrics`, `logs`, `traces`, and
  the vmalert API routes) carries `static.urls: [this release, the peer]` with
  `load_balancing_policy: first_available` and the existing
  `retry_status_codes` (`[500, 502, 503]`). The primary is first, so reads
  stay on it until it fails. `least_loaded` is refused. The peer is
  addressed by `ha.peer.{metrics,logs,traces}` and authenticated with the
  same `storeCredentials`: the two releases share that Secret.
- **Writes are not fanned out.** vmauth cannot send one write to two
  backends (a list of backends is load balancing, never replication), so the
  writers send to both stores themselves. Writer routes keep one backend: the
  primary. This chart does not touch the collectors; see "Not in this chart".
- **A proxy that is not a single pod.** `vmauth.replicaCount` must be at
  least 2 (refused otherwise), spread over zones softly, with a
  PodDisruptionBudget (`maxUnavailable: 1`).
- **A vmalert per store replica.** Four alerters instead of two: the metrics
  and logs vmalerts, each with a `-peer` twin that reads the peer's store.
  Identical rule selector, `evaluationInterval`, `externalLabels` and
  notifiers; every Alertmanager replica is notified by every alerter.
  Alertmanager deduplicates by labels, so one alert from two evaluators pages
  once. **No `replica` external label is added**, because a label that
  differed between the twins would defeat exactly that. Rules over data only
  one replica holds fire from that replica alone; either firing is enough.
- **`remoteRead`/`remoteWrite` state per replica.** The twin keeps its
  recording-rule output and `for:` state (`ALERTS_FOR_STATE`) in the store it
  evaluates against, not in one shared store. Three reasons. A store that is
  down must not take its alerting with it, and an alerter reading through the
  proxy would silently evaluate against the other replica. Whichever replica
  answers a read then has the recording rules and alert state of the alerter
  that reads it. And no alerter ever writes into the other half of the pair,
  which would put one sample into one store from two sources. The cost: a
  replica that was seeded late has recording-rule history only from when its
  twin started.
- **`-dedup.minScrapeInterval`** is already on the metrics store (`interval`,
  mirrored and validated) and a replica release carries the same default, so
  both replicas deduplicate the same way, and a writer's replayed queue
  collapses on the way in.
- **A PodDisruptionBudget per store pair** (`maxUnavailable: 1`), rendered in
  the primary only, selecting both releases' store pods by the shared pair
  label. It guards voluntary disruption (drains, upgrades); a zone loss
  takes a replica regardless.

## Zone spread, and what a zone loss costs

A zonal volume (an EBS volume, for instance) pins its replica to the zone it
was created in. So the spread is **required**, not advisory:
`topologySpreadConstraints` over `topology.kubernetes.io/zone` with
`DoNotSchedule`, selecting the shared pair label, on the stores of **both**
releases. The chart cannot write it (a subchart's values are evaluated
before any template), so it **refuses to render** without it, either role,
and says what to add. `ha` with fewer than two `zones` is refused as it always
was.

**When a zone is lost, the replica in that zone is down and stays down until
the zone returns.** Its volume cannot attach elsewhere. That is the design:
the pair is the redundancy, reads fail over to the other replica, and writers
queue for the lost one. While one replica is down the pair is a single
install; a second failure is an outage. Nothing in this chart tries to
re-create the volume in another zone, because doing that silently would hide
which replica is stale.

The values a pair needs on its stores (`<name>` is `ha.name`, default
`observability`; `<letter>` is `a` on the primary and `b` on the replica; the
same block, with `metrics`, `logs`, `traces`):

```yaml
victoria-metrics-k8s-stack:
  vmsingle:
    spec:
      podMetadata:
        labels:
          observability.pair: <name>-metrics
      topologySpreadConstraints:
        - maxSkew: 1
          topologyKey: topology.kubernetes.io/zone
          whenUnsatisfiable: DoNotSchedule
          labelSelector:
            matchLabels:
              observability.pair: <name>-metrics
victoria-logs-single:
  server:
    podLabels:
      observability.pair: <name>-logs
    topologySpreadConstraints:
      - maxSkew: 1
        topologyKey: topology.kubernetes.io/zone
        whenUnsatisfiable: DoNotSchedule
        labelSelector:
          matchLabels:
            observability.pair: <name>-logs
    serviceMonitor:
      relabelings:
        - {action: replace, targetLabel: observability_replica, replacement: <letter>}
# victoria-traces-single.server: the same, with `-traces`.
```

The replica release also turns the operator and the sync Job off:

```yaml
victoria-metrics-k8s-stack:
  victoria-metrics-operator: {enabled: false}
  syncJob: {enabled: false}
```

`tests/cases/observability-stack/ha` and `.../replica` are complete, working
examples of both halves.

## Resources

The stores' requests and limits are explicit values with right-sized
defaults already (`victoria-metrics-k8s-stack.vmsingle.spec.resources`,
`victoria-logs-single.server.resources`,
`victoria-traces-single.server.resources`); a replica release takes the same
defaults and this change moves none of them. Keep the two halves equal: size
one replica for the pair's peak, not for half of it, because after a failover
one store carries every read. Right-sizing per cluster belongs in the values
of the estate that owns the cluster.

`selfAlerts.storeMemory.enabled` (either mode) alerts when a store
container's working set is above 80% of its memory limit for 15 minutes. Set
it in both releases: each alerts for its own pods.

## Divergence

`selfAlerts.divergence.enabled` (primary only) compares the rate of rows each
store ingested over `window` (default 1h) between replica `a` and replica `b`,
per store, and fires when the ratio leaves `1 +/- threshold`. It needs the
`observability_replica` label on both stores' own scrape, which the chart
sets for the metrics store and refuses to render without for the other two.

What divergence means. The two stores take the same writes, so their rates
match up to the writers' queues draining. A ratio that stays away from 1 means
one store is not getting what the writers send it: a writer that lost one of
its destinations, a full or dropped queue, a store refusing rows (see
`MetricStoreIgnoringRows`), or a replica still catching up after a restart.
It is a tolerance and not an equality, and the default (`0.25`) is a
conservative guess: **measure a week of a healthy pair first**, then set the
threshold just above the worst healthy value. A store that is down has no
series and fires nothing here; that is `up == 0`'s job.

## Runbook: replace a replica

A replica's volume is lost (zone gone for good, disk corrupted) or the
replica is being rebuilt. The other replica keeps serving; do the replica
that is **not** the one reads are currently answered from last.

1. **Drain.** Confirm the other replica is healthy and answering through the
   proxy. Scale the broken replica's store to 0 (or leave it down). The pair
   is a single install from here: do not start other disruptive work. The
   pair's PodDisruptionBudget will now refuse a voluntary eviction of the
   survivor.
2. **Writers keep queueing.** Collectors buffer per destination (a
   persistent queue on disk), so the lost destination fills its queue while
   the rest is unaffected. Watch the buffer-growth alerts: the buffers are
   bounded, and a replica that stays down longer than its writers' queues
   hold loses the difference. That gap is not recoverable from the writers.
3. **New volume.** Delete the PVC (and the stuck pod) of the lost replica so
   the next start provisions an empty volume in a zone that satisfies the
   pair's spread. The release keeps its name; do not change `ha.replica`.
   If the metrics history matters, restore the VMSingle volume from the
   primary's backup first (`docs/reference.md`, "Restore"): a backup is
   only ever taken from the primary, so a restored replica is as old as the
   last snapshot. Logs and traces are refilled from the writers' queues
   only; there is no snapshot flow to a replica.
4. **Start.** Scale the store back up. The writers' queues drain into the new
   store: it starts empty and fills from the oldest queued data forward.
5. **Buffers refill, then check.** Wait for the buffer-growth alerts to clear
   and the divergence ratio to return near 1.

**A recovered replica lies until its writers' queues drain.** Until then it
holds a hole where the outage was. Reads are protected: `first_available`
keeps them on the primary while the primary is up, and the recovered replica
is read only if the primary fails. If the primary is the replica that was
replaced, the pair is exactly as exposed as it was before step 1 until the
queues have drained, so do not take down the other replica in that window.

## The writers

The stores half of the pair is `charts/observability-stack`; the other half
is every writer sending to **both** stores. `charts/observability-emitters`
already held one buffer per destination (the metrics agent, the OpenTelemetry
gateway and the log agent each queue on disk per URL); what it learned for a
pair is how to say "two destinations" once, with a credential per
destination.

### One list, three agents

```yaml
remote:
  name: zone-a
  url: https://write-a.example.private
  tokenSecret: {name: write-token-a, key: token}
  caSecret: {name: write-ca, key: ca.crt}      # optional, shared by default
  replicas:
    - name: zone-b
      url: https://write-b.example.private
      tokenSecret: {name: write-token-b, key: token}
```

| Agent | What it renders per destination |
|---|---|
| Metrics agent (vmagent) | one `remoteWrite` entry (`<url>/api/v1/write`), its own `bearerTokenSecret`, its own CA; one persistent-queue slice (see below) |
| OpenTelemetry gateway | per signal (metrics, logs, traces) one exporter with its own `file_storage` `sending_queue` (or write-ahead log, for metrics), its own `Authorization` header from its own environment variable, its own CA mount |
| Log agent (vlagent) | **not rendered by `remote`**: a subchart's values are computed before any template runs, so the list is written by hand, one entry per destination, with its own `bearerTokenFile` and `maxDiskUsagePerURL` |

The log agent is therefore **checked**, not trusted. With `remote.replicas`
set and `logs` among the signals, the chart refuses a
`victoria-logs-collector.remoteWrite` whose URLs are not exactly
`remote.url` plus the replicas, and refuses two entries reading their bearer
from one file. `tests/cases/observability-emitters/remote-ha-pair` is a
complete example.

A cluster that runs **both halves itself** needs no separate form: the same
list with in-cluster write endpoints (the primary's proxy, and the replica's
write endpoint) works, or write the low-level destination lists, where each
entry takes an optional `tokenSecret` of its own
(`tests/cases/observability-emitters/local-ha-pair`). Both forms render the
same thing.

Refused, each with a fixture: a repeated URL (a trailing slash does not hide
it), a repeated name, a replica without a credential of its own or sharing
one with another entry, and `-remoteWrite.shardByURL` (which would give each
half of the pair half the series).

**Why a credential per destination.** The store authenticates one user per
bearer token, and a token is unique per user on the proxy. Two destinations
on one token are one identity: the halves cannot be told apart in the store's
logs, nor one revoked without the other. A pair therefore has two tokens, and
usually two hostnames (the replica release has no proxy of its own; the
writer routes of the primary keep one backend each, see above).

**The log agent's URL path.** The agent's URL must stay without a path (the
chart refuses anything but the protocol's own `/insert/native`). The path is
the endpoint, not a prefix: the agent appends the endpoint itself, and a
wrong path answers 404, which the agent treats as permanent and drops the
block. So a replica cannot be reached by a path prefix on a shared host; give
each half its own host, which also keeps one failure domain per destination.

### Buffers: on disk, per destination, sized by values

Nothing queues in memory only. Each destination fills its own buffer while
its store is down, so one slow or dead half never takes space from the other
in the same agent (the cap, not the disk, is the bound):

| Agent | Where | Sized by | Per destination |
|---|---|---|---|
| vmagent | PVC (`persistent-queue-data`) | `metrics.queue.size` | the operator divides it by the number of URLs: `size / N`, at least 500Mi each. 2 destinations on the default `10Gi` is 5Gi each |
| Gateway | PVC per replica (`queue`) | `otlp.queue.size` for the whole volume, `otlp.queue.maxBatches` for each exporter's queue | the volume holds 3N buffers (N destinations x 3 signals). Raise `size` with N: an HA pair wants roughly double the single-destination size. `maxBatches` keeps one stalled exporter from using the others' room; the write-ahead log of the metrics exporter has no cap of its own and is bounded by the volume |
| vlagent | hostPath (`tmpDataPath`) | `maxDiskUsagePerURL` | per URL, required. The node's disk is bounded by `maxDiskUsagePerURL` x URLs, not by the kubelet: keep it deliberate (3GB each for a pair, for instance) |

Defaults are unchanged by pair support (`10Gi`, no `maxBatches`, the cap
you set). A buffer holds hours, not days, and does not backfill a store that
was down longer: see "Runbook: replace a replica" above.

### Alerts that make the buffers live

Every rule below is **per destination**, and every one is off until its
metric name is set (`selfAlerts`, in the primary release; the metric names
are values, not defaults, because a name nobody confirmed against the store
is a rule that never fires):

| Value | Set to (confirm against the store) | Fires for |
|---|---|---|
| `selfAlerts.writer.bufferMetric` | `vmagent_remotewrite_pending_data_bytes` | a buffer growing, per `url` (the series carries the destination) |
| `selfAlerts.writer.bufferMetricsExtra` | `[vlagent_remotewrite_pending_data_bytes]` | the log agent's, in the same rule |
| `selfAlerts.writer.droppedPacketsMetric` | `vmagent_remotewrite_packets_dropped_total` | data discarded, summed `by (url)` |
| `selfAlerts.writer.droppedPacketsMetricsExtra` | the log agent's counter, if it exports one | same |
| `selfAlerts.gateway.queueSizeMetric` / `queueCapacityMetric` | `otelcol_exporter_queue_size` / `otelcol_exporter_queue_capacity` | an exporter queue above `queueRatio`, per `exporter` (one per destination and signal) |
| `selfAlerts.gateway.exportFailedMetricPrefix` / `enqueueFailedMetricPrefix` | `otelcol_exporter` prefixes | send and enqueue failures, per `exporter` |

The writers' own series must reach a store the primary's vmalert reads
(scrape the emitters' `PodMonitor`s into it); otherwise the names return
nothing. A buffer that is deliberately down (a replica being rebuilt) will
fire `WriterBufferGrowing` for its URL while it grows; that is the signal the
runbook above tells you to watch, and once the buffer is full the drop rule
takes over.

## Backup

Backup runs **only in the primary**. It pins to the primary's VMSingle pod
and writes one bucket prefix. A replica refuses `backup.enabled: true`: two
releases backing up to the same prefix delete everything under it that is not
in their own source, so they take turns erasing each other's snapshots. A
restore seeds one replica from the primary's snapshot; restore the other
from the bucket only if both volumes are lost.

## Not in this chart

- **The estate's collectors.** `charts/observability-emitters` writes to
  both halves (see "The writers"); setting it up in each cluster, and
  routing cross-cluster writers to the replica (a hostname and a credential
  of its own), is the estate's configuration. Until the writers send to both
  stores, a replica is empty.
- **Seeding the replica** from the primary's history. A new replica starts
  empty and fills from the moment its writers send to it; seed the metrics
  store from the backup before the pair is trusted if the history matters.
- **Sizing the pair's nodes** in the estate's node pools.

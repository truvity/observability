# kube-state-metrics

Design for `charts/observability-emitters`' `kubeStateMetrics`, the
fourth and only-off-by-default emitter. Not a new signal — it wraps
upstream's own chart — but the placement, the collector list and one
relabel rule are this repository's own decisions, and this page is where
they are written down.

## The problem it closes

Nothing in this repository has ever collected the cluster's own object
state. `charts/platform-alerts`' `CronJobNotSucceeding` and
`BackupJobFailed` rules read `kube_cronjob_status_last_successful_time`
and `kube_job_status_failed`; `charts/observability-stack`'s own
`selfAlerts.snapshotAge` rules read the same CronJob metric to watch
this stack's own backups. All three have always assumed an estate
collects kube-state-metrics some other way — a line in
docs/adoption.md's "what must already exist" table, not a chart. A fleet
that wants to notice a crash-looping pod
(`kube_pod_container_status_restarts_total`) or a node under pressure
(`kube_node_status_condition`) has had nowhere in this repository to get
either metric from.

## Where it lives, and why not the other two places

**Not `charts/observability-stack`'s own vendored copy.** The vendored
`victoria-metrics-k8s-stack` carries a `kube-state-metrics` subchart
already, and this repository leaves it permanently disabled — see that
chart's `values.yaml`: "kube-state-metrics and node-exporter are the
cluster's, not this release's. Two installs of kube-state-metrics export
the same series twice and every rate() over them is wrong." That
sentence is still the reason it is not turned on there. It is also the
wrong CHART architecturally: `observability-stack` is the store, and a
central cluster may host the stores for many clusters while each of
those clusters still runs only its own emitters — a cluster that runs
no store of its own would never get one collecting its object state if
this lived there.

**Not a new, fifth chart.** kube-state-metrics is scraped by the same
metrics agent every other scrape object on the cluster is, through the
same default scrape class, and it needs the same "one per cluster"
shape the metrics agent's own kubelet and cAdvisor scrapes already have
— including the cluster-wide read RBAC that shape implies. A separate
chart would buy nothing this one does not already give it, and one more
install unit that has to agree with this one on cluster and tier is one
more place for the two to drift.

**`charts/observability-emitters`**, pinned to the same upstream
version (`7.5.3`) `charts/observability-stack`'s own vendored copy
carries, so the estate never runs two different renders of one upstream
chart under one release.

## Off by default, on purpose

Every other emitter in this chart defaults on: something only this
chart can do. kube-state-metrics is not — a cluster may already run a
shared one for its whole fleet, and turning this on unconditionally
would be a second install exporting the same series a second time,
silently doubling every `rate()` and `sum()` over them. So it asks
rather than assumes, the same reasoning that leaves
`observability-stack`'s copy off. Turning it on grants a ClusterRole
that reads every object of every kind in `kube-state-metrics.collectors`
— cluster-wide, every namespace, not only this release's own — which is
also not a thing to default on quietly.

## The collector allow-list: a deliberate trim

Upstream's own default (`kube-state-metrics/values.yaml`) is 28 resource
kinds. This chart ships eleven:

| Kind | Why it is in |
|---|---|
| `cronjobs`, `jobs` | `kube_cronjob_status_last_successful_time` and `kube_job_status_failed` — `platform-alerts`' `CronJobNotSucceeding` / `BackupJobFailed`, and this stack's own `selfAlerts.snapshotAge`. |
| `persistentvolumeclaims` | `kube_persistentvolumeclaim_resource_requests_storage_bytes` — `platform-alerts`' `VolumeSmallerThanClaimed`. |
| `pods` | `kube_pod_container_status_restarts_total` — a fleet's crashloop rule, the documented reason a fleet installs this chart at all. |
| `nodes` | `kube_node_status_condition` — a fleet's node-pressure rule. |
| `deployments`, `replicasets`, `statefulsets`, `daemonsets` | The standard "is a rollout stuck" surface: desired vs. ready/updated replicas, one series family per controller kind. |
| `namespaces` | `kube_namespace_status_phase` — a namespace stuck `Terminating`. One series per namespace; cheap. |
| `horizontalpodautoscalers` | Autoscaling headroom, the same shape as the rollout kinds above. |

Left **out**, relative to upstream's default — each one a real object
read denied, not a metric dropped after collection: `secrets`,
`configmaps`, `endpointslices`, `leases`, `networkpolicies`,
`storageclasses`, `validatingwebhookconfigurations`,
`mutatingwebhookconfigurations`, `volumeattachments`,
`resourcequotas`, `poddisruptionbudgets`, `replicationcontrollers`,
`limitranges`, `ingresses`, `certificatesigningrequests`, `services`.
Nothing in this repository, or in the fleet rules named above, reads
their series today. Add one deliberately, with the name of the rule or
dashboard that will read it, rather than restoring the upstream default
wholesale — an allow-list a caller can freely widen back to "everything"
is a comment, not a budget.

Upstream's own guidance (its README) puts the `pods` collector at
roughly ten to twenty series per pod, and the others at single digits
per object — an upstream estimate, not a measurement of this list
against a real cluster's object count. This repository's own doctrine
is to ask the store rather than trust a sender's number (see
docs/doctrine.md, "A 200 is not storage"); the same applies here.

`metricLabelsAllowlist` and `metricAnnotationsAllowList` stay at
upstream's own default, empty — no per-object label or annotation
becomes a series label unless asked for by name. A `[*]` entry for any
resource is refused: kube-state-metrics keys those series' labels off
the workload's own values, so a wildcard multiplies series by every
distinct combination a workload happens to use, which is the identical
cardinality trap `metrics.scrape.nodeLabels` documents elsewhere in this
chart for the kubelet and cAdvisor scrapes — measured there as a store
silently IGNORING every series past its own per-series label limit.

## The namespace stamp

The most important mechanism in this file, and the reason it needed
more than a values toggle: see docs/safety.md, "The namespace stamp
kube-state-metrics needs and no other scrape object does", for the full
argument. In short —

Every scrape object on the cluster is stamped by the metrics agent's
default scrape class with `k8s_namespace_name` taken from the namespace
of the pod being **scraped**. That much is the same story as the
kubelet's and cAdvisor's own node-level series. What is NOT the same,
and what an earlier version of this fix got wrong by assuming it was: by
the time metric relabeling runs, `namespace` itself is no longer the
object's namespace either.

The VictoriaMetrics operator's own ServiceMonitor conversion stamps
`namespace`, `pod`, `container` and `service` as TARGET labels —
unconditionally, for every `endpoints`-role scrape — from the
kube-state-metrics POD's own identity, never from the object a series
describes. This chart forces `honor_labels: false` on every scrape
(`overrideHonorLabels: true`), and under that setting a collision
between a target label and a same-named label the SERIES itself exposes
is resolved in the target's favour: the target's value wins under the
bare name, and the series' own value survives only as `exported_<name>`.
kube-state-metrics' own series carry exactly those names —
`kube_pod_container_status_restarts_total{namespace,pod,container,...}`
— so they collide, and `namespace` ends up holding the kube-state-metrics
pod's namespace, with the object's real one sitting under
`exported_namespace`.

The fix, shipped as this chart's own default `metricRelabelings` on the
`ServiceMonitor`, restores each of the four from its `exported_` twin
where the object had one, and drops the bare name outright where it did
not (a bare name with nothing to restore it is only ever the target's
own identity):

```yaml
metricRelabelings:
  - action: labeldrop
    regex: k8s_namespace_name
  - action: labeldrop
    regex: (namespace|pod|container|service)
  - action: replace
    sourceLabels: [exported_namespace]
    regex: (.+)
    targetLabel: namespace
  - action: replace
    sourceLabels: [exported_pod]
    regex: (.+)
    targetLabel: pod
  - action: replace
    sourceLabels: [exported_container]
    regex: (.+)
    targetLabel: container
  - action: replace
    sourceLabels: [exported_service]
    regex: (.+)
    targetLabel: service
  - action: labeldrop
    regex: exported_(namespace|pod|container|service)
  - action: replace
    sourceLabels: [namespace]
    regex: (.+)
    targetLabel: k8s_namespace_name
```

Every `replace` step above spells out `action: replace` even though it
is also the default. It has to: the monitoring.coreos.com
`ServiceMonitor` CRD's structural schema defaults an omitted `action`
to `replace` on ADMISSION, not merely for whoever reads the YAML, so
the object actually stored in the cluster (and reported back by
`kubectl get -o yaml`) always carries it. A rendered manifest that
leaves `action` out matches that stored object in every way that runs,
but not byte-for-byte — and ArgoCD's diff is byte-for-byte, so it stays
`OutOfSync` forever on a field nothing ever changed. Same class of
problem as this chart's earlier `record: ""` `VMRule` issue: write out
what the server would otherwise fill in for you.

Order is load-bearing at every step: the bare-name drop before the
restore (so the restore writes into a label already cleared of the
target's stamp, not layered on top of it), the restore before the
`exported_` cleanup, and the final `k8s_namespace_name` derivation last
of all, reading the now-corrected `namespace`. A series with no
`namespace` label at all — `kube_node_*`, and any other cluster-scoped
kind in `collectors` — has nothing to restore and comes out of this
with no `namespace` and no `k8s_namespace_name` either, the same shape
the kubelet's and cAdvisor's own node-level series already carry:
visible only to a grant with `allNamespaces: true` on this cluster (see
`pkg/tenancy.metricsFilter`). Upstream's own `namespace` label is
restored rather than left dropped — `docs/dashboards.md`'s rule that it
stays is why the chain puts it back under its own name rather than only
deriving `k8s_namespace_name` from it and moving on.

See docs/safety.md, "The namespace stamp kube-state-metrics needs and no
other scrape object does", for the full argument, including the exact
VictoriaMetrics operator source this reasons from.

## Refusals

Loads more than a values toggle: `_validate.tpl` checks the merged
`kube-state-metrics.prometheus.monitor.http.metricRelabelings` for every
step of the chain above — each of the four bare-name drops, each of the
four restores, the `exported_` cleanup, the final derive, and its own
leading drop — AND for the relative ORDER between the steps that depend
on one another, failing a chain that has every step present but two of
them swapped exactly as it fails one with a step missing outright. Each
distinct check has a fixture under `tests/invalid/observability-emitters/`
prefixed `kube-state-metrics-`; see docs/safety.md's own table for the
non-namespace-stamp refusals in one line each: `kubeStateMetrics.enabled`
with the metrics agent off; no `ServiceMonitor` rendered for it; a
`namespaces` filter; a non-cluster Role; RBAC not created with no
existing role named; more than one unsharded replica; a `[*]` label or
annotation allow-list entry.

## Proof, before release

- `just lint` renders every negative fixture alone and requires it to
  fail for the refusal it names, not merely to fail (`hack/lint-fixtures.sh`).
- `just test` renders the `everything` case with `kubeStateMetrics`
  enabled and a non-default `resources` and `metricLabelsAllowlist`,
  proving the passthrough is plumbed rather than ignored.
- `tests/kubestatemetrics_relabel_test.go` reads the RENDERED
  `metricRelabelings` back out of that same golden and replays them
  through `github.com/prometheus/prometheus/model/relabel` — a real
  relabel engine, not this repository's own reasoning about one — against
  hand-built label sets shaped exactly as vmagent would present them
  after the operator's target stamp and the `honor_labels: false`
  collision rename described above, for three representative
  kube-state-metrics series. This is what caught the first version of
  this fix reading the wrong label outright: reasoning about the YAML
  said it was correct, and a real relabel engine said otherwise.
- Measured on a real install, the same way every claim in this
  repository about a metric name is expected to be: read the store's
  own `kube_*` series count before and after, and confirm a namespace
  grant sees only the objects of the namespaces it was granted —
  neither this release's own namespace's worth of everything, nor
  nothing at all.

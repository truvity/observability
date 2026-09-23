# Adoption

How a platform takes these charts into use, and what to do at each upgrade
that changes what runs.

## The CRDs come first, and they are a release of their own

`charts/observability-crds` installs the CustomResourceDefinitions the rest
of this stack needs: the VictoriaMetrics operator's twenty-five, and the
four Prometheus Operator scrape kinds — `PodMonitor`, `ServiceMonitor`,
`ScrapeConfig` and `Probe` — that every component authors its scrape objects
in. They are a separate release for two reasons that have both cost estates
an outage.

Treat the Prometheus Operator half as a **prerequisite, not a tidy-up**. On
a cluster where nothing has installed `monitoring.coreos.com` — no
`ServiceMonitor`, no `PodMonitor`, no `PrometheusRule` — this chart is the
only thing that puts those kinds there, and until it has, every chart that
offers a monitor template quietly produces none. That failure is in
docs/safety.md, and it is why the order below is a rule rather than a
preference.

**Helm never upgrades a CRD installed from a chart's `crds/` directory.**
The first install lays them down and every upgrade after that leaves them
exactly as they were, with no diff, no warning and no error. The schema a
cluster validates against then drifts behind the controller reconciling it,
and the symptom arrives much later as a field silently dropped from an
object somebody just wrote.

**A CRD that arrives as a side effect has no owner.** Where the scrape kinds
exist only because whichever chart happened to install them did so, the
answer to "which release owns `ScrapeConfig`?" is "the last one that
applied", and removing that chart takes the kind — and every object of it —
with it.

### Install order

1. **This chart, before anything that uses the kinds.** As its own Argo CD
   `Application` at a sync wave ahead of the stack:

   ```yaml
   apiVersion: argoproj.io/v1alpha1
   kind: Application
   metadata:
     name: observability-crds
     annotations:
       argocd.argoproj.io/sync-wave: "10"
   spec:
     sources:
       - repoURL: oci://ghcr.io/truvity/charts/observability-crds
         targetRevision: <version>
         path: .
     destination:
       name: <cluster>
       namespace: observability
     syncPolicy:
       automated:
         prune: false
         selfHeal: true
       syncOptions:
         - ServerSideApply=true
   ```

   `prune: false` is not tidiness. Deleting a CustomResourceDefinition
   deletes every object of that kind on the cluster, including the ones
   other releases created, and nothing asks first. `ServerSideApply=true`
   is not tidiness either: client-side apply writes the whole object into
   the `kubectl.kubernetes.io/last-applied-configuration` annotation, which
   Kubernetes caps at 262144 bytes, and a single one of these CRDs is
   several times that.

2. **Then tell the operator not to manage CRDs.** The VictoriaMetrics
   operator chart installs its own by default; left on, two releases own
   the same objects and take turns overwriting each other, the winner being
   whichever reconciled last. In its values:

   ```yaml
   crds:
     enabled: false
   ```

   An estate that runs the Prometheus Operator itself makes the mirror
   choice here instead, leaving that operator to own its kinds and turning
   the set off in this chart:

   ```yaml
   sets:
     prometheusOperator: false
   ```

   Both sets off is refused: a CRD release that installs nothing reports
   Synced and Healthy, and the failure surfaces later in the controller
   that wanted the kind.

3. **Then the stack**, and only then anything else that offers a monitor
   template, at a later wave. Switching `serviceMonitor.enabled: true` on
   in some other chart before the kinds exist installs cleanly and scrapes
   nothing; docs/safety.md says why nobody notices.

### After the first install

**Restart anything that read the API surface at startup.** A controller
that decided once, at boot, whether `monitoring.coreos.com/v1` existed goes
on believing the answer it got — the Keycloak operator is one, and it will
not emit its `ServiceMonitor` until it has been restarted. Rolling those
deployments is part of installing this chart the first time, not a separate
piece of housekeeping.

**Then turn the monitor values on**, in that order, and confirm an object
actually exists (`kubectl get servicemonitor -A`) rather than trusting a
green deployment.

### Diff the CRDs on every bump

Both upstreams are pinned, and a bump of either is a deliberate change with
a render to read before it is applied. Every release of this chart carries
an inventory — every kind, the versions it serves and the one it stores —
as the last document of its own render, so the question that matters can be
answered without reading a megabyte of schema:

```console
helm template observability-crds \
  oci://ghcr.io/truvity/charts/observability-crds --version <version> \
  | tail -40
```

Compare that block against the release you are running. A kind that has
disappeared, been renamed, or moved its storage version is a migration,
not a bump: existing objects are stored under the old version, and the
conversion has to happen while both are still served. Then, before the
sync, `kubectl diff` the render against the cluster — an upstream that has
narrowed a field is visible there and nowhere else.

## What must already exist

`platform-alerts` renders `VMRule` objects and nothing else. It assumes:

| Thing | Why |
|---|---|
| The VictoriaMetrics operator | It owns the `VMRule` CRD and hands the rules to vmalert. Without it the chart installs objects nothing reads. |
| vmalert, with a rule selector that matches | A `VMRule` nobody selects is a file on the cluster, not an alert. Put the selector's labels in `ruleLabels`. |
| Alertmanager, with a route for each `severity` | An alert whose severity has no branch fires into nowhere. |
| kube-state-metrics | `CronJobNotSucceeding`, `BackupJobFailed` and `VolumeSmallerThanClaimed` read its series. |
| kubelet volume stats | `VolumeSmallerThanClaimed` compares them against the claim. |

The expressions are MetricsQL. They use duration literals in arithmetic
(`> 26h`), which MetricsQL supports and PromQL does not, so they are for
vmalert rather than for Prometheus.

## Installing platform-alerts

1. Decide the store list. The chart has no default for it and refuses to
   render without one, because a guessed metric name is a rule that never
   fires. Read the counter names off your own stores' `/metrics`.
2. Install with `groups.writePath.enabled: false` if you want the cheap
   rules first; enable it once the store list is right.
3. Point `commonLabels` at whatever your Alertmanager routes on.
4. Prove each rule before trusting it: see "Prove the rule" below.

```console
helm install platform-alerts oci://ghcr.io/truvity/charts/platform-alerts \
  --version <version> --namespace observability --values values.yaml
```

## The zero-diff gate

**A consumer adopts a release only when the render it produces is
byte-identical to what runs, or differs exactly by the change the release
announces.** Moving hand-written `VMRule` files to this chart is one pull
request whose render diff is empty: render the chart, diff it against the
live objects, and reconcile the difference before installing rather than
after. Tightening a threshold is a separate release, adopted separately.

## Adopting rules that already exist

If the cluster already carries a `VMRule` with any of these alert names,
decide which one wins before installing. Two objects defining
`WritePathDead` both fire, Alertmanager groups them, and the one you
thought you had deleted keeps paging from its own thresholds. Delete the
hand-written object in the same change that installs the chart.

## Prove the rule before trusting it

Every rule here is silent by nature: it fires when something has stopped,
and a rule that is subtly wrong looks exactly like a healthy estate. Before
relying on one, evaluate its expression both ways against the live store:

- on healthy data it must return **no series**;
- with the condition inverted (or against a deliberately broken object) it
  must return series, with a non-zero `seriesFetched`.

A rule that returns nothing in both directions is not passing. It is
querying metrics that do not exist — usually a metric name that is right
for a different version of the exporter.

## Upgrades that change what runs

Each entry says what to do; none is optional reading before a bump.

*Nothing yet — the first release has not been cut.*

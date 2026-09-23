# Adoption

How a platform takes these charts into use, and what to do at each upgrade
that changes what runs.

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

## Install order

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

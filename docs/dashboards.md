# Dashboards

Design for `charts/observability-dashboards` and the lint every dashboard
passes. Not yet released; this page is the contract.

## The problem it closes

A Grafana with no dashboards is a Grafana where every question starts in
Explore. The upstream store charts ship dashboards, and Grafana's sidecar
collects any ConfigMap with the right label — but the sidecar reads the
cluster Grafana runs on, and an estate whose Grafana sits on one cluster
while an install sits on another has dashboards rendered where nobody
can see them. Dashboards have to be shipped to Grafana deliberately, as
their own artifact, with their own contract.

## The shape

A chart that renders one ConfigMap per dashboard, labelled for the
sidecar, in Grafana's namespace. Two sources:

- **the generic set**, carried in the chart: the stores (metrics single,
  metrics agent, alerter, operator, log single, log agent, trace single,
  Alertmanager), the OpenTelemetry gateway, node, kubelet, a
  namespace/pod view. The upstream dashboards, fetched at build time
  from the same URL list the upstream store chart uses, pinned by
  release, and rewritten to the contract below before they are
  committed;
- **the estate's own**, passed as values (`extraDashboards`) or as a
  sibling chart — fleet components, products — and held to the same
  contract by the same lint.

```yaml
datasources:
  # UIDs, because a dashboard names its datasource by UID and the estate
  # chose them when it provisioned Grafana.
  metrics: victoriametrics
  logs:    victorialogs
  traces:  victoriatraces
folders:
  infrastructure: Infrastructure       # keyed by cluster
  stores:         Observability        # the stack's own health
dashboards:
  node-exporter-full: {enabled: true}
  kubelet:            {enabled: true}
  # …one key per shipped dashboard; `enabled: false` drops it from the render
```

## The contract every dashboard passes

Held by a lint that runs on the chart's own dashboards in CI and is
shipped as a `just` recipe an estate runs on its own:

1. **A `datasource` variable, and every panel uses it.** A panel pinned
   to one datasource UID is how a fleet dashboard silently becomes a
   one-install dashboard. The lint fails on any panel whose datasource
   is a literal.
2. **A `cluster` variable, chained off `datasource`**, populated by a
   label-values query so the list is whatever the chosen install
   actually holds — never hardcoded — and used in every query.
3. **A `namespace` variable, chained off `cluster`**, where the
   dashboard is namespace-scoped. Both variables scope themselves to
   the viewer: the proxy narrows label-values queries as it narrows
   data, so a dropdown lists only what the viewer may read.
4. **`$cluster` in the title.** A screenshot must say which cluster it
   is; nobody reads staging as production because the tab looked the
   same.
5. **The environment tier is a display label, never a selector.** Two
   clusters can share a tier.
6. **Upstream's `namespace` label stays.** The metrics agent keeps the
   native label beside `k8s_namespace_name` precisely so the upstream
   Kubernetes dashboards work with a cluster variable added and little
   else; the lint accepts either spelling.

## Layout, as a recommendation the chart defaults to

Infrastructure dashboards keyed by cluster, in one folder: the same
components run on every cluster and the question is always "which one".
Product dashboards keyed by project and spanning clusters, one folder
per project: the question there is "how does my service compare across
environments". Two mental models in one Grafana is the cost, stated;
it matches how each audience asks.

One organisation, every folder visible, to start. A viewer opening
another project's dashboard sees empty dropdowns and empty panels, which
reads as "not mine" clearly enough; per-project folder permissions are
additive and can follow.

## Refusals

| Shape | Why |
|---|---|
| a datasource UID missing for a store that is enabled | dashboards for a store nobody can point at |
| a dashboard failing the lint | shipped into the render is shipped into every consumer |
| `extraDashboards` entry with no folder | lands at the root beside the shipped set |

## Proof, before release

- the lint passes on every shipped dashboard, and fails on fixtures with
  a literal datasource, a missing cluster variable, a title without
  `$cluster`;
- golden render for the default set and for a set with two extras;
- in a consumer: the store-health dashboard shows an install's write
  path, and switching `datasource` switches the cluster list.

# observability

A self-hosted observability stack for Kubernetes estates, as reusable
mechanism: the VictoriaMetrics family as the store, every query scoped
at the door to the clusters and namespaces the caller's own token allows,
the collectors that stamp those under OpenTelemetry's names, and the
alerting rules that catch a backup, a store or a volume failing while
everything still looks green.

| Artifact | What | Status |
|---|---|---|
| `charts/observability-crds` | The CustomResourceDefinitions the rest of the stack needs, owned as their own release rather than as a side effect of whichever chart installed them first: the VictoriaMetrics operator's, and the four Prometheus Operator scrape kinds every component authors its scrape objects in. Applied before the controllers, never pruned. | unreleased |
| `charts/platform-alerts` | The rules that fire when something has stopped working silently: a CronJob that is no longer scheduled, a store whose write path has died, a volume that was never mounted, a store approaching its own read-only limit. Every rule carries the incident that earned it and a negative fixture that must fail. | unreleased |
| `charts/observability-stack` | One install of the store: VictoriaMetrics, VictoriaLogs and VictoriaTraces behind an authorising proxy that scopes every query to the clusters and namespaces the caller may read; two vmalerts and Alertmanager with a deadman that leaves the cluster; network policies and backups; optionally Grafana, forwarding the signed-in user's identity. Single-replica today — `ha` is accepted and the zone-redundant behaviour follows. | unreleased |
| `charts/observability-emitters` | Per-cluster collection: a metrics agent, a log agent and an OpenTelemetry gateway, each optional, each stamping the cluster, the namespace and the environment tier under OpenTelemetry's names — from what the collector can see, never from what the application said — and each replicating to every destination with its own on-disk buffer. | unreleased |
| `pkg/tenancy` (Go) | From a list of principals, render the proxy's user entries or the token claim an issuer mints — one input, both shapes, so the two can never disagree. Refuses a name that could widen a grant rather than escaping it. | unreleased |

Charts publish to `oci://ghcr.io/truvity/charts/<chart>` on every tag; from
the release that adds it, the same tag is the Go module
`github.com/truvity/observability`'s version.

## Who it is for

A platform team running the VictoriaMetrics family on Kubernetes with the
VictoriaMetrics operator, an OIDC issuer of their own, and more than one
team looking at the same telemetry. It assumes kube-state-metrics and
kubelet metrics exist, and that vmalert selects rules and Alertmanager
routes them.

The **stores themselves** are not here: these charts wrap the upstream
ones rather than replacing them. What is here is everything an install is
judged on when it fails — whether a query can reach another team's data,
whether the write path stopping is noticed, and whether a backup that
silently stopped two days ago is still reported healthy.

## The model

Two nouns. The **scoping key** is the cluster and the namespace: every
series, log stream and span carries `k8s.cluster.name` and
`k8s.namespace.name` (spelled `k8s_cluster_name` and `k8s_namespace_name`
where a label cannot hold a dot, and `kubernetes.pod_namespace` on logs,
where the container-log agent's own spelling is the one both log writers
use), stamped by the collectors from the pod they resolved the data to
and never from what the application said. A grant names a cluster and
the namespaces on it; a project or a team is a derivation from a name to
a namespace list that lives with whoever writes grants, and is not a
label anywhere. An **install** is one set of stores serving many of
them; isolation happens at query time, where a proxy reads the caller's
token and injects the filters that token is entitled to.

The environment tier, `deployment.environment.name`, rides on every
signal and is never a key: two clusters can share a tier, so a filter on
it would hand a principal both.

That first sentence is the whole security property, and
`charts/observability-emitters` is what holds it up: the metrics agent
discards a `k8s_namespace_name` label a target exported itself, the
gateway strips the one an SDK set before resolving the pod, and the
values that would turn either of those off are refusals rather than
defaults. Query-time isolation over telemetry an application labelled
itself is not isolation.

Everything else follows from those two. Redundancy is the writer's job
because no store here replicates across a zone: two independent instances,
collectors sending to both with per-destination buffers, the proxy in
front of reads. High availability is a values flag, not a different
architecture.

## Install

The stack itself installs after the CRDs and before the rules — see
[docs/adoption.md](docs/adoption.md), which lists what must already exist
(the CRDs chart, cert-manager, an OIDC issuer, and the Secret holding the
stores' own credentials):

```console
helm install observability oci://ghcr.io/truvity/charts/observability-stack \
  --version <version> --namespace observability --values values.yaml
```

The rules are their own release:

```console
helm install platform-alerts oci://ghcr.io/truvity/charts/platform-alerts \
  --version <version> --namespace observability --values values.yaml
```

```yaml
# values.yaml — a worked example with neutral values.
#
# There is no default store list and the chart refuses to render without
# one: a guessed metric name renders cleanly and then never fires, which
# is the failure this chart exists to prevent. Read these names off your
# own stores' /metrics.
stores:
  - name: metrics
    rowsMetric: vm_rows_inserted_total
    freeSpaceMetric: vm_free_disk_space_bytes
    freeSpaceLimitMetric: vm_free_disk_space_limit_bytes
  - name: logs
    rowsMetric: vl_rows_ingested_total

# vmalert selects rule objects by these.
ruleLabels:
  vmalert: platform

# Whatever the Alertmanager routing tree reads.
commonLabels:
  k8s_cluster_name: example-cluster
  deployment_environment_name: development

runbookBaseUrl: https://runbooks.example.com
```

### Scoping a query to its caller

```go
cfg := tenancy.Config{
    ClaimName:      "groups",
    Audience:       "example-observability-client",
    MetricsBackend: "http://metrics.example:8428",
    LogsBackend:    "http://logs.example:9428",

    // The keys default to what charts/observability-emitters stamps:
    // k8s_cluster_name / k8s_namespace_name on metrics, k8s.cluster.name /
    // kubernetes.pod_namespace on logs. Set them only for collectors that
    // were not built here.

    Principals: []tenancy.Principal{
        {Group: "example:k8s:viewer", Grants: []tenancy.Grant{
            {Cluster: "example-cluster", AllNamespaces: true},
        }},
        {Group: "example:example-app:deployer", Grants: []tenancy.Grant{
            {Cluster: "example-cluster", Namespaces: []string{"example-app"}},
        }},
    },
}

vmauth, err := cfg.RenderVMAuth("https://issuer.example")   // the proxy's config
claim, err := cfg.RenderClaim(cfg.Principals[1])            // what an issuer mints
```

Both come from the same input on purpose. A difference between them is a
difference between what a token says a person may read and what the proxy
lets them read, and that is not a difference anyone notices until it
matters.

`Audience` is the client id this proxy's own tokens are minted under, and
it is required. vmauth validates a token's expiry and, under OIDC
discovery, its issuer, and nothing else — it has no audience option and
never inspects `aud` — so without the pin any unexpired token that issuer
minted is admitted whatever client it was minted for, including one the
same person holds for a different application. It is rendered into every
user's `match_claims` beside the group, under `aud`.

Each route `RenderVMAuth` emits carries the filter argument that applies
the claim — `.../?extra_filters={{.MetricsExtraFilters}}` for metrics,
`extra_stream_filters={{.LogsExtraStreamFilters}}` for logs — because
that substitution is the **only** thing vmauth does with a `vm_access`
claim. A route without it forwards every query unfiltered while the claim
beside it still states the grant, which is why a read route cannot be
constructed without one. `TracesBackend` needs
`AllowUnfilteredTraceReads` with it: the trace store's select APIs accept
no argument to put a filter in, so that route cannot be scoped and has to
be admitted by name. [docs/safety.md](docs/safety.md) has the mechanism
and the test that catches it.

## Documentation

- [docs/adoption.md](docs/adoption.md) — prerequisites, install order, the
  zero-diff gate, proving a rule before trusting it, and every breaking
  upgrade with its steps.
- [docs/safety.md](docs/safety.md) — every refusal and every default, with
  the failure that earned it.
- [docs/reference.md](docs/reference.md) — every value: default, type,
  what it does, when it is required.
- [docs/doctrine.md](docs/doctrine.md) — what this repository owns, what
  the consuming estate owns, and why the shape is what it is.

## The rule that makes this repository public

**Mechanism only.** Nothing here names an account, a cluster, a hostname,
a bucket, an issuer or a secret path. Every such thing is an input with a
neutral default, supplied by the consuming estate from its own private
repository, and `hack/leak-canary.sh` enforces it in CI as its own job —
because public history cannot be unpublished.

Secrets are the caller's: a chart takes the *name* of a Secret and never
stores one, generates one into a manifest, or knows a secret manager.

## Licences, and the Enterprise boundary

This repository is MIT. It wraps the **community edition** of the
VictoriaMetrics family, which is Apache 2.0 and free to use for any number
of tenants or companies. It never pulls an Enterprise image, never renders
a licence flag, and never uses an Enterprise-only feature — the charts
refuse rather than warn. [docs/doctrine.md](docs/doctrine.md) lists the
boundary; [docs/safety.md](docs/safety.md) lists what is deliberately
absent because of it.

Grafana and its VictoriaMetrics data source plugin are AGPL-3.0; they are
referenced by name and never vendored here.

## Status

Used in production by its maintainers.

## Development

```console
devbox shell
just check      # lint, golden renders, leak canary
just golden     # regenerate the golden renders — review the diff
just crds       # re-fetch observability-crds from its pinned upstreams
```

Every chart carries a strict `values.schema.json`, golden renders under
`tests/golden/`, and one negative fixture per refusal under
`tests/invalid/`. A refusal without a fixture is a refusal that will
quietly stop working.

## Releasing

One tag stamps every artifact: pushing `vX.Y.Z` releases every chart at
`X.Y.Z` and, once it exists, the Go module at `vX.Y.Z`. Chart versions are
committed as `0.0.0` and stamped by the release workflow.

Auto-release is present but **disarmed**, and when armed it only ever cuts
patches. Minors, majors and every first release are manual tags, pushed
after the CHANGELOG heading for that version has merged.

## Licence

MIT, see [LICENSE](LICENSE).

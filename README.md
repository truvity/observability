# observability

A self-hosted observability stack for Kubernetes estates, as reusable
mechanism: the VictoriaMetrics family as the store, tenancy enforced at
the door from the caller's own token, the collectors that stamp it, and
the alerting rules that catch a backup, a store or a volume failing while
everything still looks green.

| Artifact | What | Status |
|---|---|---|
| `charts/platform-alerts` | The rules that fire when something has stopped working silently: a CronJob that is no longer scheduled, a store whose write path has died, a volume that was never mounted, a store approaching its own read-only limit. Every rule carries the incident that earned it and a negative fixture that must fail. | unreleased |
| `charts/observability-stack` | One install of the store: VictoriaMetrics, VictoriaLogs and VictoriaTraces, single or as a zone-redundant pair, behind an authorising proxy that scopes every query to the caller's tenants; vmalert and Alertmanager; optionally Grafana, forwarding the signed-in user's identity. | planned |
| `charts/observability-emitters` | Per-cluster collection: a metrics agent, a log agent and an OpenTelemetry collector, each stamping tenancy from namespace labels and replicating to every destination with its own on-disk buffer. | planned |
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

Two nouns. A **tenant** is a label, not an instance: telemetry carries
`tenant` and `env`, the collectors stamp both from the namespace's own
labels, and an application cannot choose its own. An **install** is one
set of stores serving many tenants; isolation happens at query time, where
a proxy reads the caller's token and injects the filters that token is
entitled to.

Everything else follows from those two. Redundancy is the writer's job
because no store here replicates across a zone: two independent instances,
collectors sending to both with per-destination buffers, the proxy in
front of reads. High availability is a values flag, not a different
architecture.

## Install

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
  tenant: infra
  env: example

runbookBaseUrl: https://runbooks.example.com
```

### Scoping a query to its caller

```go
cfg := tenancy.Config{
    ClaimName:      "groups",
    MetricsBackend: "http://metrics.example:8428",
    LogsBackend:    "http://logs.example:9428",
    Principals: []tenancy.Principal{
        {Group: "example:k8s:viewer", Grants: []tenancy.Grant{
            {Env: "devel", AllTenants: true},
        }},
        {Group: "example:dms:deployer", Grants: []tenancy.Grant{
            {Env: "devel", Tenants: []string{"dms"}},
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

## Status

Used in production by its maintainers.

## Development

```console
devbox shell
just check      # lint, golden renders, leak canary
just golden     # regenerate the golden renders — review the diff
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

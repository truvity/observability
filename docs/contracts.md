# Contracts

Contract version: 1.0

This is the contract between the observability platform and everything that
runs on it. It is written so that it stays true while the platform is split
by plane (per-cluster frontend, store, alerting, platform alerts, Grafana):
nothing here names a plane, a store or a cluster.

The Go constants live in `pkg/contracts`, which is the single source;
`pkg/tenancy`, `pkg/rulecheck` and `pkg/dashboardlint` re-export from it. A
test pins this page's version to `contracts.Version` and requires every name
in the package to appear here.

Each item is marked **stable** (rendered or enforced today; a change is
breaking) or **planned** (reserved and documented, not yet rendered or
enforced; do not rely on it until a later contract version marks it
stable). The restructure step that delivers a planned item is named.

Versioning: the minor number moves when a name is added or a planned item
becomes stable; the major number moves when a stable name is removed or
changes meaning.

## Platform side

What the platform stamps and what a reader or a rule may select on.

### Labels and fields (stable)

| Signal | Cluster | Namespace | Environment tier |
|---|---|---|---|
| Metrics (labels) | `k8s_cluster_name` | `k8s_namespace_name` | `deployment_environment_name` |
| Logs (stream fields) | `k8s.cluster.name` | `kubernetes.pod_namespace` | `deployment.environment.name` |
| Traces (resource attributes) | `k8s.cluster.name` | `k8s.namespace.name` | `deployment.environment.name` |

The scoping key is the cluster and the namespace. The environment tier is
descriptive and never a key: two clusters can share a tier. `owner` is an
optional metrics label derived from the namespace (`tenancy.owners`); see
`docs/tenancy-owner.md`.

### Keys under `observability.truvity.io/`

| Key | On | Meaning | Status |
|---|---|---|---|
| `observability.truvity.io/evaluator` | `VMRule` label | Name of the vmalert that evaluates the rule (`vmalert.remoteEvaluators`). | stable |
| `observability.truvity.io/rule-type` | `VMRule` label | `alert` or `recording`. | planned (restructure step 2) |

### Identity kinds (stable)

`pkg/tenancy` turns "who may read which telemetry" into a vmauth
configuration or a token claim. A caller is one of:

| Kind | Meaning | Type in `pkg/tenancy` |
|---|---|---|
| `human` | A person, selected by a group claim on an OIDC token. | `Principal` |
| `machine` | A workload reading one namespace of one cluster; its group is `<cluster>:<namespace>:<role>`. | `MachineReader` |
| `store-connector` | An install's outbound connector, reading every namespace of every cluster it serves. | `StoreReader` |

The audience is pinned under the `aud` claim.

### Ownership (stable names; enforcement planned)

A rule or a dashboard has one owner, the party that writes it and answers
for it:

| Value | Who |
|---|---|
| `platform` | Shipped by the platform charts: the stack's self-alerts, `platform-alerts`, `observability-dashboards`. |
| `component` | Shipped by a component chart (`truvity/<component>`) for that component's own behaviour. |

The ownership labels are introduced by restructure step 2, together with
`rule-type`; until then ownership is by chart, not by label.

### Logical datasources

Dashboards read three logical datasources: `metrics`, `logs`, `traces`.
Which store answers each is the platform's business and changes across the
restructure; a dashboard never says. From restructure R2.2 each cluster's
datasource forces `k8s_cluster_name` on every query (planned), so a
dashboard must not filter on it itself.

## Component side

What a component chart (`truvity/<component>`) may rely on in any cluster,
and what it must ship. This is the same on every cluster and does not
change when the platform is split.

1. **Emit OTLP to the in-cluster endpoint.** (stable) A component sends
   metrics, logs and traces by OTLP to the collector in its own cluster. The
   collector stamps the cluster, namespace and environment tier
   (`docs/emitting.md`); a component does not stamp them and does not name a
   store.
2. **Ship `VMRule`s with the ownership labels.** (rules: stable; ownership
   labels `observability.truvity.io/{evaluator,rule-type}`: planned, step 2)
   A component's alerts and recordings are `VMRule` objects in its own
   chart, not entries in the platform's charts.
3. **Ship `GrafanaDashboard`s that reference the logical datasources
   `metrics`, `logs` and `traces`.** (the dashboard rules in
   `docs/dashboards.md`: stable; the logical names and the forced cluster
   filter: planned, R2.2) No store names, no datasource UIDs and no cluster
   filters in a dashboard: the per-cluster datasource forces
   `k8s_cluster_name`.
4. **Lint both with the published `rulecheck` and `dashboardlint`.**
   (the tools: stable; running them as a published gate in a component's CI:
   planned, R2.3) They are `cmd/rulecheck` and `cmd/dashboardlint`, and the
   libraries `pkg/rulecheck` and `pkg/dashboardlint`.

## Stack key freeze

Until the stack is split by plane, `charts/observability-stack` takes no new
keys except fixes. `tests/stack_keys_freeze_test.go` compares every key
path in its `values.yaml` and `values.schema.json` with the allow-list
`tests/stack-keys.yaml` and fails on a key that is in neither `keys` nor
`fixes`.

### Adding a fix-key

1. Make sure it is a fix: the stack is wrong without the key. A new
   feature belongs in the chart of the plane that will own it.
2. Add the key to `fixes:` in `tests/stack-keys.yaml`, in the form the
   failure prints (`values:<dotted.path>` or `schema:<dotted.path>`; the
   schema spells an array item `[]` and a map value `*`), with a `reason`:

   ```yaml
   fixes:
     - key: "values:vmalert.example"
       reason: "the default renders an invalid rule without it"
   ```

3. Say so in the PR description. A fix with an empty reason, a fix for a
   key that is already in `keys`, and a fix for a key that no longer exists
   all fail the test.

Removing a key from the chart means deleting its line from `keys` (the test
fails on a stale line, so the key cannot come back unnoticed).

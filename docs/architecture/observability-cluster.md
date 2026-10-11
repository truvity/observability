# observability-cluster

Design for the per-cluster observability frontend (restructure R2.1). The
first slice is released as `charts/observability-cluster`; this page says what
the whole chart becomes, why it is cut the way it is, and which choices were
made ("Decisions", at the foot).

## What it is for

A component chart (`truvity/<component>`) is allowed to know four things about
the platform, the same on every cluster ([contracts.md](../contracts.md)): an
OTLP service in its own cluster, the `VMRule` kind, the `GrafanaDashboard` kind
with the logical datasource names `metrics`, `logs` and `traces`, and nothing
else. It names no store, no Grafana address and no datasource uid.

Where Grafana and the stores live is the platform's per-cluster business. Today
that is one Grafana beside one store; tomorrow a store may move, or a second
Grafana may appear. If a component chart knew, every move would touch every
component chart. `observability-cluster` is the platform's side of that
contract: installed on **every** cluster, it is the one release that turns
the in-cluster contracts into whatever the estate runs behind them.

It holds, in the end state:

| Part | What | Today |
|---|---|---|
| collectors | metrics agent, logs collector, OTLP gateway, kube-state-metrics, node-exporter, blackbox | `observability-emitters` |
| operators | the VictoriaMetrics operator (reconciles `VMRule`, `VMAgent` and the scrape kinds), the grafana-operator (reconciles `GrafanaDashboard`) | the VM operator ships inside `observability-stack` (`mode: operator-only` on a cluster with no store); no grafana-operator anywhere |
| Grafana link | an external `Grafana` resource naming the shared Grafana with this cluster's credential, and the cluster's folder | nothing; dashboards are ConfigMaps read by a sidecar in the Grafana's own cluster |

`observability-grafana` stays the only Grafana. Its dashboard sidecar is
removed once every dashboard is a `GrafanaDashboard`; the generic platform
dashboards (`observability-dashboards`) become `GrafanaDashboard` resources
too.

## Composition

How does one chart hold the emitters, the operator and the new parts? Three
shapes were weighed.

**A. An umbrella chart with the existing charts as dependencies.** The
obvious reading, and the one that was checked first, because it would keep
every template where it is. It cannot be adopted in place:

- Every workload the emitters render carries `app.kubernetes.io/instance:
  {{ .Release.Name }}` in its **selector** (gateway, blackbox,
  kube-state-metrics, node-exporter), and its names derive from the release
  name. A selector is immutable. Under an umbrella the release is
  `observability-cluster`, so each Deployment and DaemonSet would have to be
  deleted and recreated, which for the gateway is a gap in all telemetry and
  for the agents the loss of what they had buffered.
- One release cannot be named both `observability-emitters` (what the emitters
  run as) and `observability-stack` (what the operator runs as). `fullnameOverride`
  pins names but not the instance label.
- A subchart's values nest one level down, so every consumer's `valueFiles`
  and presets (top-level keys today) would stop applying.

**B. A sibling release that owns only what is new.** `observability-cluster`
renders the grafana-operator, the external `Grafana` and the folder. The
emitters and the operator keep their Applications. It is additive: nothing
existing is touched, so nothing can be pruned or recreated. The cost is that
"one chart per cluster" is, for now, one chart plus two.

**C. Move ownership into the chart, the way R1 did.** `observability-alerting`
copies the stack's templates, takes `stackReleaseName` so every name and the
instance label are derived exactly as the stack derives them, and a
`source` switch on the stack stops it rendering what the new chart renders.
A proof test holds `stack(default) == stack(chart) + alerting`, object for
object. The same shape would apply to the emitters (`emittersReleaseName`) and
the operator.

**Recommendation: B now, C per component, after the pilot.** The first slice is
B because it is safe and useful on its own. C is the end state the issue asks
for, but it duplicates the emitters' templates and helpers (about 3,300 lines)
and gains nothing until a cluster needs the single chart; the pilot (R2.4)
shows whether it does. A is not recommended on any reading. Decision Q1.

## The grafana-operator

**Upstream's chart, not templates of ours.** `grafana-operator` 5.25.0 from
`oci://ghcr.io/grafana/helm-charts`, pinned exactly and vendored under
`charts/` (as `observability-emitters` vendors its dependencies), enabled by
`grafanaOperator.enabled` (default off). Its Deployment, ServiceAccount, RBAC
and metrics Service are upstream's; this chart passes `grafana-operator:`
values through unchanged. The pin moves by a deliberate change with the render
read, like every other pin here. Its namespace is the release's, so
`observability` on a cluster that installs the rest of the platform there.

**CRDs.** Thirteen CRDs, one of them (`Grafana`) 600 KB as upstream ships it
and still 345 KB without descriptions. That one cannot be client-side applied
(the `last-applied-configuration` annotation is capped at 262,144 bytes), and
Helm never upgrades a CRD it installed from `crds/`. Both are the reasons
[adoption.md](../adoption.md) gives for `observability-crds` being a release of
its own, so the CRDs are a third set there, `sets.grafanaOperator` (default
off), applied server-side with pruning off. The set is generated by ocictl's
`crdctl` (`hack/crds/grafana-operator/crdctl.yaml`) from the same upstream
release this chart vendors, and a test holds the two pins together and the
set's kinds equal to the operator's own. Decision Q2.

This chart ships the operator **and none of its CRDs**. Upstream's chart puts
them in a `crds/` subchart that Helm (and Argo CD's render) installs, and has
no switch that turns that off without rendering the CRDs as templates of the
release instead, so `hack/vendor.sh` removes the nested `charts/crds` from the
vendored archive, deterministically, and a test fails if it comes back.
`grafana-operator.crds.immutable: false` (the templated form) is refused at
render. What cannot be detected at render time is whether the CRDs are on the
cluster: Argo CD and `helm template` give a chart no reliable view of the
cluster's API surface. The order is therefore a rule: `observability-crds` with
`sets.grafanaOperator: true` syncs first (a wave ahead, as the other sets do),
and a controller that starts before its CRDs does not notice them later.

**RBAC.** Upstream's ClusterRole reads and writes `secrets`, `configmaps`,
`services`, `deployments`, `ingresses`, `httproutes` and `persistentvolumeclaims`
cluster-wide, because it also manages Grafana instances it deploys itself. An
external-only operator needs far less: all of `grafana.integreatly.org`, read
on `secrets` and `configmaps`, events, and the leader-election lease. The
manager's cache still lists and watches the kinds it owns when it manages
in-cluster Grafanas, so cutting those blind would crash it at start-up; the
trimmed role needs a proof on a kind cluster before it ships. The plan is to
ship upstream's RBAC first (this slice), then render our own ClusterRole with
`rbac.create: false`, read-only on everything except the platform CRDs, with a
kind proof. Decision Q3.

**Which kinds a cluster may create.** The operator reconciles every
`grafana.integreatly.org` kind; the platform wants a cluster to create three:

| Kind | Allowed | By whom |
|---|---|---|
| `GrafanaDashboard` | yes | component charts, `observability-dashboards` |
| `GrafanaFolder` | yes, only the cluster's own | this chart |
| `Grafana` | yes, only the external one | this chart |
| `GrafanaDatasource` | not by a component | the platform renders one datasource set per store; per-cluster datasources were withdrawn ([cluster-datasources.md](cluster-datasources.md)) |
| alerting kinds (`GrafanaAlertRuleGroup`, `GrafanaContactPoint`, `GrafanaMuteTiming`, `GrafanaNotificationPolicy*`, `GrafanaNotificationTemplate`) | no | alerts are `VMRule`s evaluated by the alerters, never Grafana's |
| `GrafanaServiceAccount`, `GrafanaManifest`, `GrafanaLibraryPanel` | no | not part of the contract |

RBAC cannot express this (it grants a whole API group or a kind, to the
operator, not to the creator). The enforcement is a `ValidatingAdmissionPolicy`
rendered by this chart, which denies the "no" kinds and, for a
`GrafanaDashboard`, requires the instance selector and the cluster's folder
and refuses a literal datasource uid. That is a later slice. Decision Q4.

## The external `Grafana`

```yaml
apiVersion: grafana.integreatly.org/v1beta1
kind: Grafana
metadata:
  name: grafana
  labels: {observability.truvity.io/grafana: shared}
spec:
  external:
    url: <the shared Grafana's root URL, as this cluster reaches it>
    apiKey: {name: <Secret>, key: token}
    tls: {certSecretRef: {name: <CA Secret>}}   # only when a private CA signed it
```

- **`url` is data.** A cluster with no store reaches the shared Grafana over
  the network; the cluster that holds the stores and the Grafana reaches it by
  its in-cluster Service. The resource is the same either way, which is the
  point: moving Grafana changes one value per cluster, no component chart.
- **The credential is a reference.** The chart never creates, reads or renders
  a token. `credentials.secretName` and `key` name a Secret in the release's
  namespace. What fills it, a folder-scoped Grafana service account per cluster,
  is R2.2 (the operator's own token handling has no scoping, so the scoping has
  to be the service account's). TLS verification cannot be turned off through
  this chart; a private CA is a `caSecret` reference.
- **One label for selection.** The `Grafana` and the folder carry
  `observability.truvity.io/grafana: shared`. A component's `GrafanaDashboard`
  selects the instance with `instanceSelector.matchLabels` on that label and puts
  itself in the cluster's folder with `folderUID: <cluster>` (a uid does not depend on the
  dashboard's namespace), and sets `allowCrossNamespaceImport:
  true`, because the `Grafana` lives in the platform namespace and the
  dashboard in the component's. A component that also renders its own
  `GrafanaFolder` (a subfolder, `parentFolderUID: <cluster>`) sets the same flag
  on it: the operator skips a folder in another namespace than the `Grafana`
  without it, and the dashboard then fails with 403. This label, the folder uid rule and the flag are
  a component-facing contract, and move [contracts.md](../contracts.md) to 1.2 when
  the chart is stable. Decision Q6.
- **The cluster's folder.** `GrafanaFolder` titled and uid'd after the cluster
  (the uid is immutable; it equals `tenancy.cluster` unless overridden).
  Whether this chart creates it depends on what the service account may do:
  Decision Q5.

## A cluster with the stores, and one without

| | Has the stores and the Grafana | Has neither |
|---|---|---|
| `observability-stack` | `mode: full` | `mode: operator-only` (the VM operator) or, later, the operator chart |
| `observability-emitters` | writes to the local proxy | `remote` to the stores' cluster |
| `observability-cluster` | `externalGrafana.url` = the in-cluster Grafana Service | `externalGrafana.url` = the Grafana's cluster address |
| shared Grafana | `observability-grafana` | none |

The chart is the same; only values differ. `observability-cluster` never reads
`observability-stack`'s keys, so the stack freeze
([contracts.md](../contracts.md), "Stack key freeze") is untouched.

## Dashboards

`observability-dashboards` renders one sidecar ConfigMap per dashboard; the
sidecar reads the Grafana's own cluster only, which is the problem
[dashboards.md](../dashboards.md) opens with. The move:

1. `observability-dashboards` gains an output switch (`configmap`, the default
   and today's render byte for byte, or `grafanadashboard`). A dashboard larger
   than a ConfigMap's 1 MiB limit is not a problem here either way; a
   `GrafanaDashboard` can hold its JSON gzipped (`gzipJson`).
2. The generic set is installed once, from the cluster that hosts the Grafana,
   into a platform folder. Installing it on every cluster would put N copies
   of every upstream dashboard in one Grafana.
3. A component's own dashboards go in that cluster's folder, installed with the
   component.
4. When every dashboard is a CR, `observability-grafana` gains a switch that
   removes the sidecar; its default keeps it, so no render moves until it is
   flipped.

## Migration per cluster

Each step is its own change, checked before the next, and none deletes
anything. Argo CD tracks an object by an annotation naming its Application, and
prunes only objects that name it: that is what makes an in-place adoption safe
and what makes a handover with pruning on dangerous
([adoption.md](../adoption.md), 0.71.0).

1. **Bump the pin** and change nothing else. No golden moves.
2. **CRDs.** `observability-crds` with `sets.grafanaOperator: true`, a wave
   ahead, server-side applied, pruning off. Restart nothing: the operator is
   not yet installed.
3. **Install `observability-cluster`** as a new Application with
   `grafanaOperator.enabled`, the external Grafana and the folder. Additive;
   nothing it renders existed before. Prove: the `Grafana` resource reports a
   successful sync status, and the folder appears.
4. **Credentials** (R2.2): the Secret exists before step 3 syncs, or the
   `Grafana` resource sits in an error state that costs nothing.
5. **Pilot** (R2.4): one component ships its dashboards as `GrafanaDashboard`.
   Prove it appears in the cluster's folder with the logical datasources.
6. **Dashboards chart** (the section above), then the sidecar off.
7. **Composition (shape C), per component, last.** For the emitters:
   `observability-cluster` renders the emitter objects with `emittersReleaseName`
   set to the emitters' release name, under the same kind, name and namespace;
   the emitters' Application stops rendering them (`source: cluster`). The
   handover follows 0.71.0 to the letter: **prune off on the old Application
   first**, the new one syncs and re-tags the objects, then the old one is
   removed. A proof test holds `emitters(default) == emitters(cluster-source) +
   observability-cluster` for every emitters case. The operator follows the
   same path or stays in the stack (Decision Q7).

## Traps the design already knows

- A new Application per concern is not ordered against another: do not depend on
  sync waves between Applications for the handover; prune-off is the only guard.
- `Release.Name` in a selector is why shape A fails; any ownership move must
  derive the instance label from the old release name.
- A controller that reads the API surface at start-up (the operator, if it
  starts before its CRDs) does not notice CRDs added later; CRDs first.
- A `GrafanaDashboard` that names a literal datasource uid works on one
  cluster and silently breaks on the next. The dashboard lint refuses it
  ([dashboards.md](../dashboards.md)); the admission policy refuses it at the
  cluster.
- The operator's own metrics (`serviceMonitor`, `dashboard` keys upstream) are off
  by default; turning the ServiceMonitor on needs the scrape kinds first.
- Alerting on the link itself (a `Grafana` resource that has been unreachable
  for an hour) is a `platform-alerts` rule, added with the pilot; until then
  the failure is invisible.

## What stays for R2.2 to R2.5

| Step | What |
|---|---|
| R2.2 | one datasource set per store, isolation from the viewer's own token (per-cluster datasources withdrawn, [cluster-datasources.md](cluster-datasources.md)); the folder-scoped Grafana service account per cluster and how its token reaches the Secret |
| R2.3 | `rulecheck` and `dashboardlint` as a published gate in a component's CI |
| R2.4 | the pilot component |
| slice 2 (done) | `observability-crds` `sets.grafanaOperator` |
| later slices | own RBAC and the admission policy; dashboards output switch; sidecar off; composition |

## Decisions

Taken 2026-10-10. The options considered are kept in one line each; the
reasoning is in the sections above.

**Q1. Composition: B.** This chart owns only what is new. The existing
components (emitters, the VictoriaMetrics operator, and so on) move into it
later, one at a time, by the ownership switch of 0.71.0 (shape C), never as an
umbrella (A cannot be adopted in place: immutable selectors, release names,
nested values).

**Q2. grafana-operator CRDs: a third set in `observability-crds`.**
`sets.grafanaOperator`, default off, before any cluster enables the operator.
Built with `crdctl` from the pinned upstream release, applied server-side with
pruning off. Rejected: upstream's `crds/` (never upgraded; the 345 KB CRD
cannot be client-side applied) and its mutable-CRD templates (this release
would own CRDs, so removing it deletes every dashboard on the cluster).

**Q3. Operator RBAC: upstream's first.** Upstream's ClusterRole for the first
install; our own read-mostly ClusterRole after a proof on a kind cluster, in a
later slice. Watch scope stays cluster-wide.

**Q4. Allowed kinds: a `ValidatingAdmissionPolicy`** in this chart, with lint as
the earlier signal. A later slice. Needs Kubernetes 1.30 or later, and must
exempt this chart's own resources.

**Q5. The cluster folder: the platform pre-creates it.** Each cluster's Grafana
service account gets Edit on its own folder only (R2.2), so the token cannot
touch another cluster's dashboards. Those clusters set `folder.enabled: false`;
the folder resource stays for a cluster whose token may adopt it.

**Operator authentication (R2.2, E3 A): an exchanged token, not a static Secret.**
With `externalGrafana.auth.sluisExchange.enabled`, the operator's `Grafana`
resource sets `spec.client.useKubeAuth: true`. The operator then reads
`/var/run/secrets/grafana.com/serviceaccount/token` again whenever it has
expired (it treats a token as expired 30 s early, and recovers without a
restart once the file is rewritten). A sidecar in the operator pod keeps that
file fresh: every `refreshEvery` it posts the operator's own projected
ServiceAccount token to the issuer's `/token` as an RFC 8693 exchange for the
client `grafana-workload` and writes the access token with an atomic rename
into an in-memory volume shared with the operator. Upstream's chart already
mounts a projected token volume at that very path, so the volume is replaced,
not added to, through a patched `kubeAuth.volume` of the vendored chart. Grafana
accepts the access token (`[auth.jwt]`), which maps the operator's subject to
the role None; the folder permission (Q5) is what lets it write its own
dashboards. Rejected: a static service account token per cluster (a long-lived
credential to mint, store, rotate and leak; see
[cluster-datasources.md](cluster-datasources.md)).

**Q6. The component-facing selector: the label** `observability.truvity.io/grafana: shared`,
with `folderUID: <cluster>` and `allowCrossNamespaceImport: true` (on a component's own `GrafanaFolder` too). A mutating
default is reconsidered only if the pilot shows the three fields written badly.

**Q7. The VictoriaMetrics operator** stays `observability-stack` with
`mode: operator-only` until the stack split reaches it; it is then composed by
shape C.

**Q8. Generic dashboards: once.** Installed once, from the Grafana's own cluster, into
a platform folder, not per cluster.

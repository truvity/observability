# Per-cluster datasources

Design for restructure R2.2: each cluster gets its own datasources in the one
shared Grafana, with its cluster forced on every query by the proxy, and its
own Grafana service account that can write only its own folder. It follows
[observability-cluster.md](observability-cluster.md) (R2.1, decisions Q5 and
Q6) and is the platform side of the logical datasources in
[contracts.md](../contracts.md).

Status: the first slice is shipped, off by default (see "First slice"). The
rest is design, with the open decisions at the foot.

## What the platform promises

- A dashboard has no cluster filter and cannot show another cluster's data.
  The same JSON works on every cluster.
- A compromised cluster can rewrite only its own folder's dashboards.
- Fleet-wide dashboards stay central, on unfiltered datasources that only
  platform folders use.

Two different guarantees hide in the first line, and the design keeps them
apart. **What data a datasource can return** is enforced by the proxy and is
strong: the token a datasource holds cannot read another cluster. **Which
datasource a dashboard names** is enforced by an admission policy and by
Grafana permissions, and is only as strong as they are (see "Which datasource
a dashboard reads"). A person who may query `<other>-metrics` can always do so
in Explore; the guarantee is about what a cluster's dashboards can present,
not about what a signed-in person may see.

## Correction to the brief: who creates the datasources

The task text says the cluster's own operator creates its datasources. It
cannot, and should not. Grafana (OSS) has fixed roles only: creating a
datasource needs the org Admin role (`datasources:create`), and a service
account with Admin could rewrite every other cluster's datasource, which is the
compromise this design exists to prevent. Custom, uid-scoped roles are
Enterprise.

So the datasources are rendered **once, by the platform's own
`observability-cluster` release** (the one beside the stores, whose operator
token is the platform's), for the whole fleet: `datasources.clusters` lists the
clusters, and the release renders `<cluster>-metrics` and `<cluster>-logs`
(and, later, `<cluster>-traces`) for each. A cluster's release renders the
external Grafana and nothing datasource-shaped. Dashboards and folder writes
stay per cluster; datasource writes stay central.

## Proxy users (vmauth)

One bearer-token `VMUser` per cluster, rendered by `observability-stack`
(`tenancy.clusterDatasources`, opt-in, empty by default). It sits beside
principals (people, OIDC), writers, readers and alert readers, and is the same
mechanism as a reader's: the grant is written onto the route as a literal query
argument, because a static token has no claim to substitute from; vmauth drops
a client argument that clashes with one the route sets, so a query cannot widen
it (`vmauth.extraArgs.mergeQueryArgs` naming it is refused for every install).

| Signal | Route | Forced by | Value for cluster `c` |
|---|---|---|---|
| metrics | the dashboard read set: query, query_range, series, labels, label values, tsdb status, buildinfo (what a principal gets, minus nothing) | `extra_filters` | `{k8s_cluster_name="c"}` |
| logs | `/select/logsql/.*` | `extra_stream_filters` | `_stream:{"k8s.cluster.name"="c"}` |
| traces | none in this slice | see D3 | see D3 |

The values are the ones a principal's `allNamespaces` grant on that cluster
renders (`observability-stack.filter` and `.logsFilter`, which mirror
`pkg/tenancy`'s filters), so one definition of "this cluster" serves people and
datasources. `pkg/tenancy` stays unchanged in this slice: the user is a chart
construct. Whether it becomes a library identity kind (`datasource`) is D6.

Traces are the gap. VictoriaTraces' Jaeger and Tempo select APIs take no filter
argument, which is why the stack already refuses a trace route unless
`tenancy.allowUnfilteredTraceReads` is set. A per-cluster trace datasource that
enforces the cluster is therefore not possible with a route alone: D3.

## Credential flow

Three credentials, three directions.

| Credential | Minted by | Stored | Read by |
|---|---|---|---|
| Datasource token (per cluster, read, scoped by the proxy user above) | the platform's secret plane (OpenBAO), a random value written once | OpenBAO; synced to the platform's cluster as two Secrets: the proxy user's `tokenRef` and the datasource's `valuesFrom` | the proxy (compares) and the platform's operator (puts into the datasource) |
| Grafana service-account token for the cluster's operator | the platform's Grafana provisioning (D2): one service account per cluster, a token with a lifetime | OpenBAO, path per cluster | the cluster's operator, through a Secret synced to the cluster (the `Grafana` resource's `apiKey`) |
| The platform operator's Grafana token (admin) | the platform | the platform's cluster only | the platform's operator |

The datasource token never leaves the platform's cluster: the datasources are
created there, so a compromised cluster holds neither another cluster's token
nor its own datasource token. Its only Grafana credential is the folder-scoped
service account.

**Minting and rotation of the service-account token (D2).** The Grafana API
creates a service account and its token with an expiry (`secondsToLive`).
Recommended: a Pulumi-managed resource per cluster in the platform's own
stack (the Grafana provider's service account, folder permission and token),
which writes the token to OpenBAO; rotation is a scheduled replace before
expiry, with the cluster's External Secrets refresh interval shorter than the
overlap (two tokens live at once for a day). The operator reads the Secret on
each reconcile, so no restart is needed.

**Folder and permissions (Q5).** The platform pre-creates each cluster's
folder (uid = cluster) and sets its permissions explicitly:

- the cluster's service account: Edit, on that folder only;
- the service account's org role: Viewer (or None where Grafana offers it), so
  it can create no folder and has no datasource right;
- remove the default "Editor role: Edit" entry that Grafana puts on every new
  folder, and on the General folder, or a service account with the Editor role
  could write any folder. Humans who edit cluster dashboards get an explicit
  grant (a team per cluster).

With a Viewer-role service account and one Edit grant, a token stolen from
cluster `c` can create, update and delete dashboards in folder `c`, read
what Viewers read, and nothing else.

## Which datasource a dashboard reads

Datasource uids and names are unique per Grafana organisation, not per folder.
A dashboard in folder `c` that names uid `d-metrics` reads `d`'s data if the
viewer may query it; Grafana OSS cannot say "this folder's dashboards may use
only these datasources" (datasource permissions are Enterprise). So one JSON
cannot say a logical `metrics` and have Grafana resolve it per folder. The
resolution has to happen **before** the dashboard reaches Grafana, in the one
component that knows the cluster: the cluster's operator and the
`GrafanaDashboard` it reads. Options in D1; the design picks input mapping:

1. The dashboard JSON uses Grafana's own `__inputs` convention: its panels
   name `${DS_METRICS}`, `${DS_LOGS}`, `${DS_TRACES}`.
2. The `GrafanaDashboard` carries `spec.datasources`, which the operator
   applies to the JSON when it imports it:
   `{inputName: DS_METRICS, datasourceName: <cluster>-metrics}` and the two
   others. The datasource's uid equals its name, so name and uid resolve alike.
3. The component's chart renders that mapping and the `folderUID` from one
   value, the cluster name (`global.cluster.name` or the shared chart helper),
   the same value Q6 already requires for `folderUID`.
4. A `ValidatingAdmissionPolicy` in the cluster (Q4, a later slice) rejects a
   `GrafanaDashboard` whose `datasources` map to anything but this cluster's
   three names, whose folder is not this cluster's, and any dashboard JSON with
   a literal datasource uid. The cluster name the policy compares with comes
   from a parameter the platform installs. This is what makes "cannot show
   another cluster's data" hold against a component that gets it wrong, and the
   proxy token is what makes it hold against one that is malicious: the only
   datasources named `c-*` are `c`'s, and the only way to get another's is to
   name it, which the policy and the lint refuse.

What this does not do: a person with write access to the cluster's folder who
edits the JSON in the Grafana UI can type another uid. The folder's editors are
that cluster's team, who may query other clusters anyway; the per-cluster
service account cannot do so through the API either, because the operator's
reconcile overwrites it with the mapped JSON and the admission policy is the
cluster's only door.

## Network path

The cluster's operator talks to the Grafana API (`externalGrafana.url`), over
the internal gateway or the peering that already joins the clusters; nothing
public. The platform's cluster alone reaches the proxy. Grafana reaches the
proxy from the platform's cluster, so no store is reachable from a workload
cluster over a path that is not the proxy's. A workload cluster's operator
needs egress to Grafana's API only, and a network policy on the operator pod
says so. The shared Grafana accepts API traffic from the clusters' addresses
only.

## Fleet-wide datasources

The unfiltered datasources (`all-metrics`, `all-logs`, `all-traces`, backed by
a proxy user with `allNamespaces` on every cluster, or the principal tokens
passed through) live in the platform's cluster, with the generic dashboards
in the platform folder (Q8). Their use is restricted by the platform folder's
permissions plus the admission policy above (no cluster's dashboard may name
them). Since datasource permissions are Enterprise, "usable only by platform
folders" is a convention enforced at the dashboards' door, not by Grafana: D5.

## First slice

Off by default; no existing render moves (every golden is unchanged).

- `observability-stack`: `tenancy.clusterDatasources[]` (`cluster`,
  `tokenSecret`) renders the per-cluster VMUser above. Validation: needs the
  proxy; a cluster is a plain name, listed once; refused in `operator-only` and
  `replica`. (A client argument merged instead of dropped is already refused
  for any install: `vmauth.extraArgs.mergeQueryArgs`.) The three keys are in the
  stack freeze as fix-keys with reasons (planned restructure key).
- `observability-cluster`: `datasources.{enabled,proxyUrl,metricsType,clusters[]}`
  renders `GrafanaDatasource` `<cluster>-metrics` and `<cluster>-logs`, uid
  equal to name, the token substituted from a Secret reference.

Not in the slice: traces (D3), the admission policy (Q4), the service-account
provisioning (D2), the dashboards' input mapping in the contract (D1), the
`pkg/tenancy` identity kind (D6).

## Rollout order

1. Mint the datasource tokens in the secret plane; sync them into the
   platform's cluster.
2. Release with `tenancy.clusterDatasources` set (proxy users). Prove: a
   request with the token and a client `extra_filters` for another cluster
   returns only the token's cluster; `series` and label values too.
3. Release `observability-cluster` with `datasources` on, on the platform's
   cluster. Prove: `<cluster>-metrics` and `-logs` appear and a test query
   returns only that cluster.
4. Provision the folders, the service accounts and their permissions (D2), then
   the tokens into the secret plane and the clusters.
5. Per cluster: install `observability-cluster` with the external Grafana (the
   Secret exists first), `folder.enabled: false`.
6. Pilot (R2.4): one component with input-mapped dashboards.
7. Admission policy; the contract moves to 1.2 (logical names, label, folder
   rule, input mapping stable).
8. The old per-store datasources (`<store>-metrics`, `<store>-prom`, ...) are
   retired last, once no dashboard names them.

## Decisions for the owner

**D1. How one dashboard JSON resolves to `<cluster>-metrics` per cluster.**

| Option | Pro | Contra |
|---|---|---|
| A. `__inputs` plus `spec.datasources` mapping, checked by admission policy (recommended) | Standard Grafana convention; no JSON edits per cluster; the operator does the substitution; the policy makes a wrong mapping a rejected object | The component renders the mapping, so it needs the cluster name (already needed for `folderUID`); a policy needs Kubernetes 1.30 |
| B. Admission mutation injects the mapping | Component chart knows nothing | Mutating policy is beta only in 1.34 and later; a kind of magic that hides the mapping from the author |
| C. A datasource template variable filtered by regex on a name prefix | Pure JSON | Needs the cluster name in the JSON, or lists every cluster's datasources: a viewer could switch to another's; defeats the guarantee |
| D. One Grafana organisation per cluster, datasources named `metrics|logs|traces` in each | True logical names; service accounts are org-scoped, so a cluster cannot touch another org at all; strongest isolation | Overturns Q5/Q6 (one folder per cluster); people need membership per org and cannot search across them; fleet dashboards are copied or live in a platform org; the operator would manage one org per cluster |

Recommendation: A. D is the strongest and the right answer if the estate grows
tenants beyond clusters, but it reverses decisions already taken.

**D2. Who mints the cluster's Grafana service account and token.**

| Option | Pro | Contra |
|---|---|---|
| A. Pulumi in the platform's stack, token to the secret plane, scheduled replace (recommended) | One place for folder, permissions, service account and token; reviewable; rotation is a re-run | Pulumi holds a Grafana admin credential; rotation needs a schedule |
| B. The platform operator's `GrafanaServiceAccount` kind, a push to the secret plane | In-cluster, declarative | The kind is outside the contract list (Q4 denies it to clusters, fine here); folder permissions by user id are not expressible, so a second mechanism is needed anyway |
| C. A small controller or CronJob | Rotation is exact | New code to own |

**D3. Traces.** VictoriaTraces takes no filter argument.

| Option | Pro | Contra |
|---|---|---|
| A. Wait for an upstream filter argument; until then traces are fleet-wide only, in platform folders (recommended) | Honest; no fake guarantee | Component dashboards have no trace panel per cluster meanwhile |
| B. A per-cluster tenant (the store's account id header, set by the proxy user, written by the cluster's writer) | Real isolation, nothing to filter | Writers and the store layout change; existing spans stay in the default tenant |
| C. Unfiltered `<cluster>-traces` behind the `allowUnfilteredTraceReads` opt-in | Dashboards work | The datasource name lies: it shows every cluster |

**D4. Where a cluster's service-account token lives in the cluster.** A Secret
in the operator's namespace, synced by External Secrets (recommended), or
mounted through the CSI driver. The Secret route needs no new component; both
leave a token in the cluster, which is the accepted exposure (folder-scoped,
expiring).

**D5. "Fleet-wide datasources usable only by platform folders".** Enforcement
by admission policy and lint (recommended; Grafana OSS cannot restrict a
datasource by folder) or the Enterprise datasource permissions (a licence
decision).

**D6. A `datasource` identity kind in `pkg/tenancy`.** Now a chart construct
(recommended until a second consumer exists); or a library kind next to
`machine` and `store-connector`, so a sluis-style estate can derive the users
from its roster the way it derives readers.

**D7. Datasource users sharing the reader's grant vocabulary.** The slice is
all namespaces of one cluster. Namespace-scoped datasources (per component
team) are possible with the same mechanism; recommended only on demand.

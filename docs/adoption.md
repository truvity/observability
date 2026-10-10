# Adoption

How a platform takes these charts into use, and what to do at each upgrade
that changes what runs.

## The complete set, in order

What a consumer installs when this repository is complete
([target-state.md](target-state.md)), and the order, because every step
is a floor for the next.

| # | Piece | Floor it needs | Proof before the next step |
|---|---|---|---|
| 1 | `observability-crds` | — | every kind present on the cluster |
| 2 | `observability-stack` (stores, proxy, alerters, Alertmanager) | 1, cert-manager, an issuer, the Secrets | a query through the proxy with a real token returns only that token's grants |
| 3 | `observability-emitters`, on every cluster | 1, 2 | **ask the store**: a line, a series and a span from a real namespace, stamped with that namespace |
| 4 | `notifications:` on the stack | 2, a Slack bot token (or Telegram, or a webhook) Secret | a synthetic critical reaches the right channel |
| 5 | `pkg/statusbox` → the box | a private network; an edge tunnel once a page is public | the private page answers only over the private network; the deadman group reads the install's alerts through `tenancy.alertReaders` |
| 6 | the deadman | 4, 5 | scaling the metrics vmalert to zero fires the box's deadman on its own chat channel within two probe intervals, and posts RESOLVED when it returns |
| 7 | `platform-alerts` with the store list | 4 | `WritePathDead` fires when a store's writes are stopped |
| 8 | the store self-alerts (inside 2) | 4 | each fires on its inverted condition, silent on a week of healthy data |
| 9 | `alert-ingress` | 4, a public route, topics | a real finding reaches the channel; suspending the heartbeat fires the deadman rule |
| 10 | `observability-dashboards`, and `observability-grafana` where one Grafana spans installs | Grafana | the store-health dashboard shows step 3's write path |
| 11 | the second half of a pair: `mode: replica`, `ha.enabled` on the primary, `remote.replicas` on every emitter | 2, 3, two zones | reads answer with the primary's store scaled to 0; the divergence ratio sits near 1 ([high-availability.md](high-availability.md)) |
| 12 | `observability-rum`, for the browser apps | 3, a route on each app's host | an exception thrown in a page appears in "Frontend Issues" with a fingerprint, symbolicated |
| 13 | `observability-mcp`, for agents | 2, an issuer with token exchange | a tool call through the connector returns only the principal's grants |
| 14 | the estate's own rule packs and dashboards | 4, 10 | the same contract: incident, healthy range, fixture; the same lint |

Receivers before packs, because a rule that fires into a receiver named
`blackhole` proves nothing. The box before the deadman, because the
deadman is what proves the router; and the router before the rules,
because the rules are what the router is for. The pair after the single
install works, because a pair is two of something that already does.

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
| kube-state-metrics | `CronJobNotSucceeding`, `BackupJobFailed` and `VolumeSmallerThanClaimed` read its series. `charts/observability-emitters`' `kubeStateMetrics` (off by default) can BE that source — see docs/kube-state-metrics.md — or an estate's own shared install may already provide it; either way, not both on one cluster. |
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

## Installing observability-stack

### What must already exist

| Thing | Why |
|---|---|
| `charts/observability-crds`, installed and synced | This chart renders `VMAuth`, `VMUser`, `VMAlert`, `VMAlertmanager` and `VMSingle` objects, and its operator dependency has `crds.enabled: false`. Without the CRDs the operator's own chart installs nothing and every object here is rejected — or worse, a chart that gates a monitor template on the kind renders nothing and reports success. It is a wave ahead, and it is a rule rather than a preference. |
| cert-manager | The VictoriaMetrics operator's admission webhook needs a certificate. The alternative is upstream's default, where the chart generates a self-signed CA **at render time**: a new certificate on every `helm upgrade`, and a release whose manifest differs from itself when nothing changed. |
| An OIDC issuer | vmauth verifies every read token against its discovery document. `tenancy.issuerUrl` is required as soon as a principal exists, because a proxy that trusts an unverified token is worse than no proxy. |
| A client of that issuer, for this proxy | `tenancy.audience` is its client id, and is required alongside the issuer. vmauth validates a token's expiry and its issuer and nothing else — it has no audience option — so without the pin any unexpired token that issuer minted is admitted whatever client it was minted for, including one the same person holds for a different application. |
| A Secret with the stores' credentials | Named by `storeCredentials.secretName`, default `observability-store-credentials`, with `username` and `password` keys. Every store runs with `-httpAuth.*` so nothing in the cluster can reach one around the proxy; the chart takes the name and never creates the Secret. |
| A `StorageClass` that binds | The stores are stateful and their volumes are ReadWriteOnce. The backup jobs mount the same volumes, which is why each carries a pod affinity onto its store's node. |
| A deadman watcher outside the cluster | Optional, and the only alert that can see this stack's own alerting path fail. `alertmanager.watchdog.secretName` names the Secret holding its receiver URL. |
| A database for Grafana, above one replica | Only when `grafana.enabled` is true with `replicas` above one. Grafana's default is SQLite on the pod: two replicas either hold two separate databases or collide on one file and answer `500 database is locked` per request. The chart refuses the combination. Point `grafana.ini`'s `[database]` at Postgres or MySQL and put the password in `envValueFrom.GF_DATABASE_PASSWORD` — `grafana.ini` renders into a ConfigMap. |

### Install order

1. `observability-crds`, at a wave ahead. See above.
2. The Secrets: the store credentials, the Watchdog receiver, each writer's
   token, the backup credentials, Grafana's admin and OAuth client. All of
   them are the estate's; the chart renders none of them.
3. This chart.

   ```console
   helm install observability oci://ghcr.io/truvity/charts/observability-stack \
     --version <version> --namespace observability --values values.yaml
   ```

4. `platform-alerts`, once the stores are answering: its rules name the
   counters your stores export.

The smallest values file that is worth installing is
`tests/cases/observability-stack/minimal/values.yaml`; the shape this
release supports, written out, is `…/single/values.yaml`. An estate run
by one operator — no per-team read scoping, a few millicores per
component, its own alert labels and bucket prefixes — starts from
`…/small-estate/values.yaml` instead; docs/reference.md, "The
single-operator estate", lists every switch it turns.

### Store credentials: who needs them

The stores' `-httpAuth.*` is mandatory for every estate, one-operator
ones included: there is no switch that turns it off. Adopting the chart
over stores that ran without it means giving the credential to every
client that reaches a store directly:

| Client | Who wires it |
|---|---|
| Both vmalerts, the proxy's VMUser targets, the backup CronJobs, the stores' own ServiceMonitors | This chart, from `storeCredentials`. |
| A writer that bypasses the proxy — a vmagent remote-write, an OpenTelemetry exporter, a log shipper | The estate: basic auth from the same Secret. |
| Grafana with `vmauth` off | The estate, through `grafana.envValueFrom`; checked by the chart (docs/reference.md, "Grafana without the proxy"). |
| A hand-made job calling a store's API — a snapshot, an export | The estate. |
| A prober on `/health` or `/ping` | Nobody. The stores answer those before their auth check; the prober needs NetworkPolicy admission only (`networkPolicy.clientsFrom`). `/metrics` needs the credential. |

List them before the cut-over: a client that was missed does not fail
at install, it gets 401 from then on.

### Some values are written twice, and the chart refuses the disagreement

Helm evaluates a subchart's values before any template runs, so a parent
chart cannot compute them. These values therefore appear both in this
chart's surface and in an upstream chart's own key, marked `MIRROR:` in
values.yaml:

| This chart | The upstream key |
|---|---|
| `interval` | `victoria-metrics-k8s-stack.vmsingle.spec.extraArgs['dedup.minScrapeInterval']` |
| `storeCredentials.secretName` | the `VM_httpAuth_*` entries in each store's `env` / `extraEnvs` |
| `backup.seLinuxLevel` | each enabled store's own `securityContext.seLinuxOptions.level` — `victoria-metrics-k8s-stack.vmsingle.spec...`, `victoria-logs-single.server.podSecurityContext...`, `victoria-traces-single.server.podSecurityContext...` |

Change one and the render fails, naming the other. That is the point: a
deduplication window wider than the scrape interval silently discards good
samples, a store reading a different Secret than the proxy presents
answers every query with 401, and a backup job whose SELinux categories
do not match its store's own gets `permission denied` reading a
snapshot it already proved it could create — see docs/reference.md,
`backup.seLinuxLevel`, and docs/safety.md for that last one — none of
which announces itself.

### The zero-diff gate, and the one difference to expect

The rule is unchanged: a consumer adopts a release only when the render is
byte-identical to what runs, or differs exactly by the change the release
announces. Render the chart, diff it against the live objects, reconcile
before installing rather than after.

One difference is built in. The golden renders in this repository are
tracked files in a public repository, so they are rendered with
`global.cluster.dnsDomain: cluster.example.` rather than the real default:
the leak canary bans in-cluster DNS names, in tests as much as in docs. An
estate on the default suffix sees that one substitution and nothing else.

### Turning a store off

Each upstream chart has its own `enabled` key, and this chart follows it:
the proxy stops rendering the routes for a store that is off, the network
policy for it disappears, and the mirror checks for it stop applying. An
estate that keeps its log store elsewhere sets
`victoria-logs-single.enabled: false` and `stores.logs.url`.

Turning one off is also the way to satisfy the datasource refusal: with
Grafana enabled here, every enabled store must have a datasource that
reads it. A store nobody can query ingests and retains exactly as if it
were being read, and nothing reports the difference.

## Installing observability-emitters

Last of the four, and on every cluster rather than on the ones holding a
store. The order matters twice over:

1. `observability-crds`, or the `PodMonitor` objects this chart renders
   are rejected and — worse — every other chart on the cluster that gates
   its monitor template on the CRD's presence renders **nothing, silently,
   with a successful sync**. A `serviceMonitor.enabled: true` flipped
   before the CRDs land produces a green deploy, no scrape object, and a
   fault that looks like the scraper's.
2. `observability-stack`, because this chart's `VMAgent` is reconciled by
   the operator that chart installs, and because its destinations are that
   install's addresses.

### What must already exist

| Thing | Why |
|---|---|
| `observability-crds` | `PodMonitor` is one of its kinds, and so is `VMAgent`. |
| The VictoriaMetrics operator | It reconciles the `VMAgent`. `observability-stack` installs it. |
| A Secret with the cluster's write token | Its **name** is `writeCredentials.secretName`; the chart neither creates nor reads it. |
| Nothing about the namespaces | The namespace is the key and every pod has one; there is no label to carry and no fallback to choose. A project's reach is the namespace list in its grant, on the read side. |
| Reachable store addresses | Every destination URL, from this cluster. A NetworkPolicy in the way is a buffer that fills. |

### The values that have no default

Three, and none of them can be guessed. Each renders, runs and reports
healthy when wrong, which is why the chart refuses rather than defaults:

```yaml
tenancy:
  cluster: example-cluster           # this cluster — half of the scoping key
  environment: development           # the tier: never a key, always stamped

writeCredentials:
  secretName: example-write-token

victoria-logs-collector:
  collector:
    # A mirror of the two above, because Helm cannot compute a subchart's
    # values; the chart refuses it when it disagrees.
    extraFields: '{"k8s.cluster.name":"example-cluster","deployment.environment.name":"development"}'
```

Plus a destination list per emitter: one `remote` block for the usual
shape, with `replicas` for a store pair. `tests/cases/observability-emitters/`
holds a worked values file per shape — the smallest useful one, one that
sets everything, the HA pair reached across a gateway (`remote-ha-pair`)
and in-cluster (`local-ha-pair`), the external receiver, the probes,
kube-state-metrics, node-exporter — each with the render it produces
under `tests/golden/`.

### Two things to carry out of this chart

**The names, which you do not set.** Every writer stamps the cluster and
the namespace under OpenTelemetry's names as each store can carry them —
`k8s_cluster_name` / `k8s_namespace_name` on metrics, `k8s.cluster.name`
/ `kubernetes.pod_namespace` on logs, `k8s.cluster.name` /
`k8s.namespace.name` on spans — and the read side (`observability-stack`,
`pkg/tenancy`) defaults to the same. Nothing to carry across. The one
name that is not the convention's, the log-path namespace, is the
container-log agent's own because it cannot rename a field; docs/safety.md
has why the gateway yields to it.

**Enabling a component's monitor is a second step.** This chart collects
every `PodMonitor` and `ServiceMonitor` on the cluster, so wiring a
component is a values flip in the chart that owns it — and, after the CRDs
are present, nothing more. A component with no scrape object is not an
omission this chart can see.

### The zero-diff gate, here

An estate replacing hand-written collection objects adopts this chart in
one pull request whose render diff is empty, then tightens in later ones.
The two diffs to read first are the `VMAgent`'s `scrapeClasses` block and
the gateway's `transform/disown` and `transform/tenancy` statements: they
are where the keys come from, and a difference there is a difference in
what every query returns.

### Turning an emitter off

`metrics.enabled`, `logs.enabled` and `otlp.enabled` are independent, and
all three off is refused. Turning one off removes its objects, its
refusals and its destinations — an estate that already runs a log shipper
sets `logs.enabled: false` and keeps the other two.

## Upgrades that change what runs

Each entry says what to do; none is optional reading before a bump. What
changed and why is in CHANGELOG.md, and is not repeated here: an entry
below is the work, in the order it has to happen. Every entry since
0.11.0 that moved a default is marked `**Behaviour change` in
CHANGELOG.md with its opt-out; the ones that need a step beyond a bump
are below.

### The alerting plane as a chart of its own (0.71.0)

**Nothing moves on the bump.** `observability-stack`'s new `alerting.source`
defaults to `stack` and the new chart `observability-alerting` is not
installed by anything: every golden render is unchanged. The upgrade is an
opt-in that moves **who renders the alerting plane**: the vmalerts, the
VMAlertmanager, karma, the Watchdog rule, the store self-alerts and the
Alertmanager ServiceMonitor ([reference.md](reference.md),
`charts/observability-alerting`) from the stack's Application to an
Application of their own, under the **same kind, name and namespace**, so
the objects are adopted in place and never deleted. The alerting plane is the
one part of an install where a gap costs a page that does not arrive, so the
order below is chosen so that no step can delete anything.

**Why it can be adopted in place.** Argo CD tracks an object by an annotation
(`argocd.argoproj.io/tracking-id`) naming the Application that applied it, and
prunes only objects that name it. The new Application server-side-applies the
same objects: same name, same namespace, same uid, the same spec, only that
annotation moves. The VictoriaMetrics operator sees no change to the
`VMAlert`/`VMAlertmanager` spec, so it rolls nothing: vmalert keeps its loaded
rules and its `for:` timers, and Alertmanager keeps its silences and its
mesh (a pair holds its state in memory, replicated between the replicas; a
restart of the whole set would lose it, and nothing here restarts it).
`tests/alerting_chart_test.go` is the proof that "the same spec" is true: for
every stack test case, the stack with the switch off plus the alerting chart
equals the stack with it on, object for object, byte for byte.

**The hazard, and the mechanism.** The stack's Application has `prune: true`.
The moment the stack stops rendering the plane (`alerting.source: chart`), its
next sync prunes every object of it that still names the stack Application.
The new Application only takes them over when it syncs, and nothing orders the
two: do not lean on sync waves between Applications for this, a root that
health-gates waves still does not make one Application's prune wait for
another's apply. If the
stack syncs first with prune on, the plane is deleted, the operator tears down
the vmalert and Alertmanager pods, and until the new Application recreates
them nothing is evaluated and nothing notifies. The same mechanism that
hands VMRules from one Application to another without deleting them removes
the hazard: **turn the stack's prune off for the handover.** With prune off, whichever Application syncs
first, nothing is deleted: the stack that stops rendering leaves the objects
where they are, and the new Application then re-tags them.

**Order.** Four steps, each a separate change, each checked before the next.

1. **Bump the pin to 0.71.0 on the cluster, and change nothing else.** The
   bump is a no-op render; confirm with the zero-diff gate. This is a floor,
   not a step to skip: a cluster still on 0.70.0 refuses the `alerting` key.
2. **Hand over, with prune off, in ONE change.** In the stack Application:
   `alerting.source: chart` and, temporarily, `syncPolicy.automated.prune:
   false` (commented as temporary). Add the `observability-alerting`
   Application, same destination namespace, release `observability-alerting`,
   `stackReleaseName: <the stack's release name>`, and the plane's sections of
   the stack's values **copied unchanged** (`vmalert`, `alertmanager`,
   `karma`, `notifications`, `selfAlerts`, plus the mirrored keys listed in
   [reference.md](reference.md): `mode`, `ha`, `interval`, `storeCredentials`,
   `stores`, `tenancy.clusterLabel`, `upstreamRules`, `backup` switches, and
   the subchart keys the stack sets for `defaultRules`). Do not change a
   value, the chart version of either Application or the Alertmanager's
   `replicaCount` in this change: the whole argument is that nothing about the
   objects differs. Render both Applications' manifests with the real values
   and diff them against the live objects first (`kubectl diff` of the new
   chart's render, with `--server-side`): the diff must be empty apart from the
   tracking annotation.
   Both orders are safe:
   - *new Application first*: it applies identical objects and takes the
     tracking id; the stack then has nothing left to prune.
   - *stack first*: it stops rendering the plane; with prune off it deletes
     nothing, shows OutOfSync with "extra resources" until the new
     Application has applied them, and a `SharedResourceWarning` for the
     minutes between is expected and harmless.
   Both Applications run `ServerSideApply=true` like the stack's already does.
3. **Verify the tracking ids moved, read-only.** On each cluster:

   ```
   kubectl --context <c> -n observability get vmalert,vmalertmanager,vmrule,servicemonitor,deploy,svc,cm,sa \
     -o custom-columns=KIND:.kind,NAME:.metadata.name,APP:.metadata.annotations.argocd\\.argoproj\\.io/tracking-id
   ```

   Every object in the table of `charts/observability-alerting` names the new
   Application (the stack's other objects, the proxy, the stores, the
   NetworkPolicies, still name the stack). Also: the new Application is
   Synced and Healthy; the stack Application shows no resource requiring
   prune; vmalert's `/api/v1/rules` still lists every group with no
   `health: err`; the count of firing alerts and the Watchdog did not change;
   the Alertmanager pods' age did not change (nothing restarted) and a silence
   created before step 2 is still listed.
4. **Restore prune.** A separate change removes the temporary `prune: false`
   from the stack Application. Do not merge it before step 3 holds on every
   cluster that has the plane. With prune back on, anything still naming the
   stack and no longer rendered by it would be deleted; step 3 is what proves
   there is nothing.

**No-gap argument.** At no step is a plane object absent: step 2 deletes
nothing under either sync order (prune off), the objects' specs are
byte-identical so the operator restarts nothing, and the tracking-id rewrite
is metadata. There is no evaluation gap (the vmalert pods run through), no
notification gap (the Alertmanager pods run through, same mesh, same
silences), and the Watchdog rule keeps its object so the status box's deadman
reads no interruption. The two things that *can* still go wrong are listed
under Rolling back.

**Do not.**
- do not install the new chart with the stack still on `alerting.source:
  stack`: two Applications would own the same objects and fight over the
  tracking id on every sync;
- do not turn the stack's prune back on in the same change as the handover;
- do not combine the handover with a chart upgrade or a change to the plane's
  values. Change them before (stack still renders) or after (the new chart
  renders), where an ordinary sync applies them;
- do not change `nameOverride`/`fullnameOverride`/`stackReleaseName` on one
  side only: every name derives from them, and a name that differs is a
  different object, which is a delete and a create.

**What stays in the stack.** The NetworkPolicies (including the vmalert and
karma ones, which select pods by label, and the labels are unchanged), the
proxy and its VMUsers (the `alertReaders` and `vmalertAPI` routes point at the
vmalert and Alertmanager Services by their unchanged names), the stores and
the backups. A later release may move the two NetworkPolicies with the
plane.

**Rolling back** is the same order reversed: set the stack's prune off, return
`alerting.source: stack`, remove the new Application (it carries no finalizer,
so deleting it does not cascade to the objects; check that before relying on
it), then restore prune. Nothing is deleted at any point for the same reason.
Two failure modes remain, and neither is a regression of this procedure: a
values difference between the stack's copy and the new Application's (the
objects *would* change, and the operator would roll the pods; the render diff
in step 2 is the check), and the new Application pointing at a different
`stackReleaseName` (different object names: new objects are created beside the
old ones and the old ones are left until prune).

### 0.69.2 → 0.70.0

**Nothing moves on the bump.** `observability-stack`'s new
`upstreamRules.source` defaults to `sync-job` and `platform-alerts`'
`groups.upstream.enabled` to `false`: no golden changed. The upgrade is an
opt-in, and a two-step one, because it changes **what applies the upstream
rules** (`KubePodCrashLooping`, `TargetDown`, `Watchdog`, the `k8s.rules.*`
recordings, ...): from `victoria-metrics-k8s-stack`'s sync Job, which fetches
them from upstream's branch heads, to a copy vendored at pinned commits in
`charts/platform-alerts` with a per-rule `exclude`
([reference.md](reference.md), `groups.upstream`). The stack and
`platform-alerts` are separate Applications that sync independently, so a
single change cannot be made atomic. The order below picks the failure
mode that cannot lose an alert: a short **duplicate**, never a **gap**.

1. **Translate what the sync job was told.** Every
   `victoria-metrics-k8s-stack.defaultRules.rules.<Alert>: {enabled: false}`
   becomes `groups.upstream."<group>".exclude: [<Alert>]` (the group is the one
   that rule is in: `kubernetes-apps`, `kubernetes-system-kubelet`,
   `kubernetes-resources`, ... `upstream/PIN.yaml` lists each group's rules);
   a `defaultRules.groups.<group>.enabled` becomes
   `groups.upstream."<group>".enabled`; a per-rule `spec.expr` becomes
   `groups.upstream."<group>".override.<Alert>.expr`; the presets
   `rules-no-apiserver` and `rules-on-demand-nodes` become the same keys. An
   install whose stack runs no Alertmanager (`alertmanager.enabled: false` /
   a sync-job `alertmanager` source off) sets
   `groups.upstream."alertmanager.rules".enabled: false`. Set
   `groups.upstream.alertmanagerNamespace` when the Alertmanager is not in
   the platform-alerts namespace. A misspelt rule name fails the render.
2. **Turn the pack on first.** Upgrade `platform-alerts` to 0.70.0 with
   `groups.upstream.enabled: true` and the keys from step 1, with the stack
   still on `sync-job`. For as long as both run, every upstream group is
   evaluated twice: one rule file from the sync job, one from the pack, same
   group name, same rule names, same expressions, same labels. That is
   harmless by construction: an alert is the same series with the same label
   set (`alertgroup` included) from both, so Alertmanager sees one
   fingerprint and notifies once, and vmsingle's `dedup.minScrapeInterval`
   (MIRROR of `interval`, 30s) collapses the two writes of a recording. Check
   before step 3: `kubectl get vmrule` lists the `*-upstream-*` objects,
   vmalert's `/api/v1/rules` shows each group twice and no group with
   `health: err`, and `count by (alertname) (ALERTS{alertstate="firing"})`
   did not change. The rule counts in `upstream/PIN.yaml` are the expected
   number per group.
   Argo CD: the pack renders recording rules as well as alerts. The VMRule
   CRD defaults `alert: ""` on a rule item exactly as it defaults `record: ""`,
   so an Application that already ignores `.spec.groups[]?.rules[]?.record` for
   alert-only charts needs `.spec.groups[]?.rules[]?.alert` ignored too, or the
   diff never resolves.
3. **Then hand the stack over.** Upgrade `observability-stack` to 0.70.0 and
   list `presets/upstream-rules-platform-alerts.yaml` before your own values
   (it sets `upstreamRules.source: platform-alerts`,
   `victoria-metrics-k8s-stack.defaultRules.enabled: false` and `syncJob.enabled:
   false`). The sync Job's ServiceAccount goes with the Job, and Kubernetes
   garbage-collects the VMRules it owned: the pack's copies were already
   evaluating, so the alerts that were firing keep firing from the pack's
   instance (no resolve, no re-notification) and the recordings never stop.
   The stack renders no `Watchdog` of its own from now on; the pack's
   `general.rules` carries upstream's (**keep that group on**, and do not
   exclude `Watchdog`). The stack refuses to render with `source:
   platform-alerts` and the rule sync still on.

**Do not reverse steps 2 and 3, and do not land both in one change.** With the
stack handed over first, its Job's VMRules are collected before the pack's
exist: for the minutes `platform-alerts` takes to sync and the operator and
vmalert to reload, no upstream rule is evaluated, `Watchdog` included (the
status box's deadman reads it), firing alerts resolve and re-notify after
their `for`, and `for` timers restart. If one Kargo freight bumps both
charts, bump both with the new keys *unset* (the bump itself is a no-op) and
make steps 2 and 3 two promotions, the second after step 2's checks.

**What differs from the sync job.** The pack is pinned (the Job followed
upstream's `main`/`master` at every deploy); a refresh is a reviewed change
(`hack/vendor-upstream-rules.sh update`, then re-run the checks above).
Rules carry upstream's labels only: not `commonLabels`, not `runbookBaseUrl`.
Sources the stack never rendered (kube-state-metrics, node-exporter, etcd,
vmagent, vmcluster, VictoriaLogs/Traces) are not in the pack.

**Rolling back** is the same order reversed: remove the preset (the sync Job
returns and applies upstream's current heads; wait for `sync complete`), then
turn `groups.upstream.enabled` off.

### 0.69.0 → 0.69.1

**`observability-stack`, rule ownership: vmalert selects on
`observability.truvity.io/evaluator`.** Upgrade the stack before the
component charts (`platform-alerts`, `observability-projects`,
`observability-rum`, `alert-ingress`) when `vmalert.remoteEvaluators` is
set: an older stack refuses a rule labelled `evaluator: metrics`, so that
rule is not evaluated until the stack is upgraded. Then check, in this
order:

1. A `vmalert.remoteEvaluators[].name` that no alerter runs (a typo) used
   to be evaluated by nobody and is now evaluated by the local metrics
   alerter. Look for such rules before the bump if that would page.
2. `metrics` and `logs` are refused as a remote evaluator name; rename
   any that use them.
3. `vmalert.acceptLegacyRuleLabels` defaults to `true`, so the old
   LogsQL marker `observability.rule-type: vlogs` is still accepted.
   Move your own LogsQL rules to `observability.truvity.io/evaluator:
   logs` and set the key to `false` while v0.70 and v0.71 are current: the
   old spelling and the key are removed in v0.72.

**`observability-stack`: `notifications.routeLabels`.** The default is
unchanged (`k8s_cluster_name` and `k8s_namespace_name`, plus `ownerLabel`
when set), so an install that does not set it renders as before. Listing
`source` in it, to route alerts born outside a cluster on `source`
instead of a made-up cluster name, changes those alerts' labels and so
their fingerprints: every open alert of that kind re-fires once, as a new
notification, when the mapping and the routes move. Do it in a quiet
window, and move the routes and the mapping in the same rollout.

Slack and Telegram titles name `source` when an alert has no cluster, and
`source` joins the default `group_by` when it is listed in `routeLabels`.

### 0.67.1 → 0.68.0

**`deploy/pulumi/status` / `pkg/statusbox`: the Lightsail backend is gone.**
Move any stack still on Lightsail to the EC2 backend first (an EC2 box
beside it, then retire the Lightsail stack), then bump. Callers drop
`Backend` (EC2 is the only backend) and the Lightsail-only inputs
(`AvailabilityZone`, `Hostname`, `TailscaleTag`, `Generation`, the tailnet
key and the secret-value inputs such as `TunnelToken`, `OIDCClientSecret`,
`TelegramToken`, `TelegramChatID`); the EC2 backend reads its secrets from
SSM parameters named in `EC2Inputs`. With SSH on, the box also writes an
sshd drop-in preferring `mlkem768x25519-sha256`, then
`sntrup761x25519-sha512@openssh.com`; the launch template changes, so the
instance rolls once.

### 0.66.1 → 0.67.0

**`deploy/pulumi/status` / `pkg/statusbox/ec2`: `SSHArgs.HostCert` is a
pointer.** A caller that set SSH passes `&preset` (or nil for SSH through
opkssh alone, with a fixed host key from `SSHArgs.HostKeyParameter`, an
SSM SecureString holding an OpenSSH-format private key). A stack without
SSH builds unchanged. Turning SSH on replaces the box's security group
once (its description changes) and rolls the instance; set the host-key
parameter first, or the box keeps its own generated key until the next
boot. `EC2Inputs.PrivatePort` (default 8081, unchanged) set to 80 moves
the private page's security-group rule from 8081 to 80 and rolls the
instance; update bookmarks and runbooks that name `:8081`.

### 0.39.0 → 0.40.0

**`observability-rum`: `sourcemaps.sync` is gone, and the schema refuses
the key.** An install that synced maps from an object store moves to
`sourcemaps.smctl` (maps as OCI artifacts, pushed by `smctl push` at
release time, `repositoryTemplate` required) or to a mounted
`sourcemaps.directory` — one of the two, never both (docs/frontend.md,
"Source maps"). The operator Deployment of `observability-stack` restarts
once for the new `VM_PROMETHEUSCONVERTERADDARGOCDIGNOREANNOTATIONS`
environment variable; nothing to do, but expect the rollout.

### 0.36.1 → 0.37.0

**`observability-stack`'s `WriterBufferGrowing` / `WriterDroppingPackets`
are per destination** and `WriterDroppingPackets` is summed `by (url)`. A
cluster writing to a pair sets `selfAlerts.writer.bufferMetricsExtra` and
`droppedPacketsMetricsExtra` to the log agent's metric names so both
agents are covered (docs/high-availability.md, "Alerts that make the
buffers live"). A route or silence that matched the old global alert now
needs the `url` label.

### 0.35.0 → 0.35.1

**Every `*Absent` guard in `platform-alerts` fires per cluster.** A store
holding several clusters will raise alerts it never raised for a
controller, exporter or probe missing on ONE cluster; each carries that
cluster's own `clusterLabel`. A cluster, controller or probe removed on
purpose keeps alerting until `absentLookback` (default `1d`) has passed —
silence it for a day rather than widening the lookback. `clusterLabel: ""`
renders the old expressions.

### 0.28.0 → 0.29.0

**`metrics.scrape.probes` is answered by a blackbox exporter, and
`up{job="http-probe"}` no longer exists.** Set `blackboxExporter.enabled:
true` in `observability-emitters` before the bump (probes without it are
refused), move any own alert from `up{job="http-probe"}` to
`probe_success{probe="<name>"}` — `platform-alerts` `groups.probes` does
that for you — and, from 0.33.1, expect the probe series to carry the
tenancy labels, so a scoped reader sees them.

### 0.10.0 → 0.11.0

Every new switch defaults to the behaviour 0.10.0 rendered; what there
is to adopt is listed in docs/reference.md, "The single-operator
estate". Two defaults DO change, both fixes, and each has an opt-out
that renders the 0.10.0 output byte for byte:

- **The inhibit rule gains a label guard** (`observability-stack`,
  `notifications.inhibit.requireLabels: true`): a critical that does not
  carry the `equal` labels no longer mutes every warning that does not
  carry them either. Expect warnings the old rule muted by accident —
  on a default install, `BackupJobFailed` beside a firing
  `CronJobNotSucceeding` — to start arriving. Opt out:
  `requireLabels: false`.
- **The backup rules skip suspended CronJobs** (`platform-alerts`,
  `groups.backups.ignoreSuspended: true`): a deliberately suspended
  backup stops paging. An accidental suspension is no longer caught by
  `CronJobNotSucceeding`. Opt out: `ignoreSuspended: false`.

One new refusal can reach an existing install: an enabled Grafana with
`grafana.envValueFrom.GF_SECURITY_SECRET_KEY.secretKeyRef.name` left
empty, which the API server already refused on apply.

### 0.9.0 → 0.9.1

Metric-churn reduction: nothing here is required before the render still
passes, and there is nothing to adopt — the new default IS the change.

**`charts/observability-emitters`'s cadvisor scrape gets a new DEFAULT
DROP, not a new requirement**: `metrics.scrape.cadvisorDrop.enabled:
true`, dropping `container_tasks_state`, `container_memory_failures_total`,
`container_blkio_device_usage_total` and every `_bucket` histogram
series cadvisor emits except `go_sched_latencies_seconds_bucket`, plus
clearing cadvisor's own `id` (cgroup-path) label on every series that
already carries a non-empty `container` label — never unconditionally,
since cadvisor's node-level cgroups (`id: "/"`, `/kubepods.slice`, a
systemd unit) have no `pod`/`container` and would otherwise collapse
into one series — a real measurement against a live install (see
docs/safety.md, "Metric churn:
what cadvisor never has read"). **This changes what the cadvisor scrape
stores for every existing install that does not already override it** —
these series simply stop accumulating; nothing reads them today (see
CHANGELOG.md's `0.9.1` entry for the full list and the argument). Set
`metrics.scrape.cadvisorDrop.enabled: false` to keep the old shape, or
narrow the change with `metricNames`/`keepBucketMetrics` (replaced
wholesale) or `extraMetricNames`/`extraKeepBucketMetrics` (added to the
default) — see docs/reference.md's own row on each.

### 0.8.x → 0.9.0

"Consumer simplification": nothing here is required before the render
still passes — every one of these is a boilerplate reduction a consumer
can adopt at its own pace, on top of a chart that keeps rendering
exactly as it did on 0.8.x for every value left untouched, with two
exceptions called out below.

**`charts/observability-emitters`'s `victoria-logs-collector` gets new
DEFAULTS, not new requirements**: `priorityClassName: system-node-
critical`, `tolerations: [{operator: Exists}]`, and `resources` of
`{requests: {cpu: 15m, memory: 192Mi}, limits: {cpu: 100m, memory:
192Mi}}` — a real measurement across a live fleet's node pools, replacing
whatever this chart's own render left implicit before. **This changes
the rendered manifest for every existing install that did not already
set these three itself** — `helm diff` will show it on the next bump.
Set any of the three explicitly to keep the old shape.

**Collapse a hand-assembled single write destination into `remote`.** If
your values file writes the SAME url/token/CA into `metrics.
destinations`, `otlp.destinations.metrics/logs/traces` and
`writeCredentials` separately (the "central install, remote writers"
shape, docs/target-state.md), replace all four with one `remote:` block
— see docs/reference.md's own section on it. Leave it alone if any
signal writes to more than one destination, or writes somewhere
DIFFERENT from the others: `remote` is for the one-destination-
everywhere shape only, and the low-level form still works unchanged for
everything else. `victoria-logs-collector.remoteWrite` is NOT part of
this — see `remote`'s own doc for why (a real Helm subchart's values,
which this chart cannot compute).

**Drop the ~40-line `kube-state-metrics.customResourceState.config` +
`rbac.extraRules` block for Kargo, if you hand-wrote it**, in favour of
`kubeStateMetrics.customResources.kargo.enabled: true`. Same metrics
(`kargo_stage_condition`, `kargo_promotion_phase`), same RBAC (get/list/
watch on `stages`/`promotions` only). **Whenever `kubeStateMetrics.
enabled` is true — this preset used or not — this chart now pins
`kube-state-metrics.customResourceState.enabled: true` / `.create:
false`** so it can render that ConfigMap itself; a consumer-authored
`customResourceState.config` is refused rather than silently unused. If
you were relying on `customResourceState` for something OTHER than
Kargo, that combination now refuses — open an issue.

**`charts/observability-stack`'s `mode: operator-only` can drop four
explicit offs.** `vmauth.enabled`, `vmalert.enabled`, `alertmanager.
enabled` and `metricsSelfScrape.enabled` now default to `null`, which
`mode` resolves (`operator-only` → off, `full` → on) — leave all four
unset instead of writing `enabled: false` under each. The three stores'
own `enabled`, `grafana.enabled` and `victoria-metrics-k8s-stack.
syncJob.enabled` still need an explicit `false`: real Helm subchart
values this chart cannot compute from `mode` either. The render still
refuses an explicit `true` on any of the fifteen beside `operator-only`
— nothing about the CONTRACT changed, only how much of it you have to
spell out yourself. tests/cases/observability-stack/operator-only-
implicit is the short-hand shape's own golden fixture, proven
byte-identical to the long-hand one.

**If you built your own Gatus config for a status page, look at
`pkg/statusbox.RenderGatus`** before maintaining that renderer further:
it is the same combined-page shape ("internal → status, pulled",
docs/statusbox.md) as a typed, estate-neutral function — your own
catalogue derivation stays yours, only the YAML-building moves here.
Adopting it is optional and has no interaction with anything a Helm
chart renders.

### 0.2.0 → 0.3.0

**Set `tenancy.audience` before you upgrade, or the render refuses.** It
is the client id this proxy's own tokens are minted under —
`tenancy.audience` on `charts/observability-stack`, `Config.Audience` in
`pkg/tenancy` — and it is required as soon as a principal exists. A values
file that rendered under 0.2.0 fails until it is there, by design: there
is no value that could be defaulted which pins anything. If this proxy has
no client of its own yet, register one first; the id is the issuer's to
assign, never a name to invent.

**Rename the groups claim if it is `aud`.** `tenancy.claimName` /
`ClaimName` may no longer be that spelling, because the audience is pinned
under it and both are entries in one `matchClaims` map. Refused rather
than merged, since whichever entry survived would decide either which
principal a token is or which client it was minted for, never both.

**Expect `helm diff` to show every reader changed, and let it through.**
Each `matchClaims` value is escaped and anchored now, so
`groups: "example:k8s:viewer"` renders as
`groups: "^(example:k8s:viewer)$"`, with the new `aud` entry beside it, on
principals whose grants you did not touch. Nothing widens: each value
matches the tokens it was always meant to match and fewer of the ones it
was not. See docs/safety.md, "Every `match_claims` value means only
itself".

**One thing does stop working, and that is the point.** A reader arriving
with a token minted for another client of the same issuer was admitted
before this release and is not after it. Nothing reported it then and
nothing announces it now, so if the estate has more than one application
on that issuer, confirm the tokens your readers actually present carry the
client id you set — before the upgrade rather than from a support request
after it.

### 0.1.0 → 0.2.0

The vocabulary rework. `tenant` × `env` is retired for cluster ×
namespace, so both charts, the library and the data already in the stores
are all in scope. Four pieces of work and one thing that will look broken.

**Rewrite every grant.** `env` becomes `cluster` and `tenants` becomes
`namespaces`, with `allTenants` becoming `allNamespaces` — on
`tenancy.principals` in `charts/observability-stack` and on
`Grant{Env, Tenants, AllTenants}`, now
`Grant{Cluster, Namespaces, AllNamespaces}`, in `pkg/tenancy`. The
namespaces a project reaches are a derivation you hold, not a label on the
telemetry: wherever that mapping lives, it now produces namespace names.

**Replace the emitters' `tenancy` block** with `tenancy.cluster` and
`tenancy.environment`, both required and neither guessable, and mirror
both into `victoria-logs-collector.collector.extraFields` as JSON under
the conventional names. The chart refuses a mirror that disagrees, which
is the only reason it is safe to write a value twice.

**Delete the keys that are gone rather than leaving them behind.**
`tenancy.namespaceLabels`, `tenancy.fallbackTenant`, `tenancy.tenantLabel`
and `tenancy.envLabel` on the emitters chart; `tenancy.logsTenantField`
and `tenancy.logsEnvField` on the stack chart. Both schemas are strict
where this repository defines the structure, so a leftover key fails the
render and names itself — the good case, and the reason this is a minute's
work rather than a silently ignored stanza. The stack's replacements,
`tenancy.logsClusterField` and `tenancy.logsNamespaceField`, have defaults
now, and an estate collecting with `charts/observability-emitters` sets
neither.

**Move both sides in one change.** Between the two upgrades, whichever
goes first, scoped reads return nothing for the data being written: the
old proxy filters on a label the new collectors no longer stamp, and the
new proxy filters on one the old collectors never stamped. The window is
unavoidable and it should be minutes.

**Then expect empty panels for everything written before the upgrade, and
do not go hunting.** Series, streams and spans already in the stores carry
`tenant` and `env`; nothing rewrites them, and no grant selects them once
the proxy is upgraded. A scoped query answers from the data written since
the collectors moved, and the old data is unreachable until it ages out of
retention. Anything else of yours that selects on `tenant` or `env` — a
dashboard, a recording rule, an Alertmanager route — now selects nothing,
and is yours to move onto `k8s_cluster_name` and `k8s_namespace_name`,
with `deployment_environment_name` for the tier, which is descriptive and
never a key.

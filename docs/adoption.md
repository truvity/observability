# Adoption

How a platform takes these charts into use, and what to do at each upgrade
that changes what runs.

## The complete set, in order

What a consumer installs when this repository is complete
([target-state.md](target-state.md)), and the order, because every step
is a floor for the next. Steps marked *planned* have a design page and
no release yet.

| # | Piece | Floor it needs | Proof before the next step |
|---|---|---|---|
| 1 | `observability-crds` | — | every kind present on the cluster |
| 2 | `observability-stack` (stores, proxy, alerters, Alertmanager) | 1, cert-manager, an issuer, the Secrets | a query through the proxy with a real token returns only that token's grants |
| 3 | `observability-emitters`, on every cluster | 1, 2 | **ask the store**: a line, a series and a span from a real namespace, stamped with that namespace |
| 4 | `notifications:` on the stack *(planned)* | 2, a webhook Secret | a synthetic critical reaches the right channel |
| 5 | `pkg/statusbox` → the box *(planned)* | a private network, an edge tunnel | the public pages render; the ops page answers only privately |
| 6 | the deadman *(planned)* | 4, 5 | scaling Alertmanager to zero fires the box, on two providers, within five minutes |
| 7 | `platform-alerts` with the store list | 4 | `WritePathDead` fires when a store's writes are stopped |
| 8 | the store self-alerts *(planned, inside 2)* | 4 | each fires on its inverted condition, silent on a week of healthy data |
| 9 | `alert-ingress` *(planned)* | 4, a public route, topics | a real finding reaches the channel; suspending the heartbeat fires the deadman rule |
| 10 | `observability-dashboards` *(planned)* | Grafana | the store-health dashboard shows step 3's write path |
| 11 | the estate's own rule packs and dashboards | 4, 10 | the same contract: incident, healthy range, fixture; the same lint |

Receivers before packs, because a rule that fires into a receiver named
`blackhole` proves nothing. The box before the deadman, because the
deadman is what proves the router; and the router before the rules,
because the rules are what the router is for.

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
release supports, written out, is `…/single/values.yaml`.

### Some values are written twice, and the chart refuses the disagreement

Helm evaluates a subchart's values before any template runs, so a parent
chart cannot compute them. Three values therefore appear both in this
chart's surface and in an upstream chart's own key, marked `MIRROR:` in
values.yaml:

| This chart | The upstream key |
|---|---|
| `interval` | `victoria-metrics-k8s-stack.vmsingle.spec.extraArgs['dedup.minScrapeInterval']` |
| `storeCredentials.secretName` | the `VM_httpAuth_*` entries in each store's `env` / `extraEnvs` |

Change one and the render fails, naming the other. That is the point: a
deduplication window wider than the scrape interval silently discards good
samples, and a store reading a different Secret than the proxy presents
answers every query with 401 — neither of which announces itself.

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

Plus a destination list per emitter. `tests/cases/observability-emitters/`
holds three worked values files — the smallest useful one, one that sets
everything, and the zone-redundant pair — each with the render it
produces beside it.

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
below is the work, in the order it has to happen.

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

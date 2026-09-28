# Target state

What this repository is when it is complete, and what a consuming estate
supplies to run it. This is the document to disagree with first; the
design pages it links to are each one piece of it, and
[adoption.md](adoption.md) is how a consumer installs the pieces in order.

## One sentence

A consuming estate installs the charts in this repository, supplies its
own names and secrets from its own private repository, and has — with no
other tooling — stores for metrics, logs and traces scoped to the caller
at the door; collectors on every cluster; one router for every alert and
one address every alert reaches a human through; rules that notice a
failure nobody else would report; a watcher outside the estate that
notices when the alerting pipeline itself is dead; public status pages
per company; and dashboards to read it all with.

## The boundary, stated once

| This repository (mechanism) | The consuming estate (data) |
|---|---|
| how a query is scoped to its caller | who the principals are and what they may read |
| how telemetry is stamped, buffered and replicated | the cluster names, the tier, the destinations |
| how an alert is routed, grouped, templated and refused | which cluster and namespace go to which channel; the webhook |
| how an event born outside the cluster becomes an alert | which topics are allowed and how each maps |
| how a box outside the clusters probes, pages and renders a status page | which hostnames, which companies, which domains |
| which dashboards exist and what variables they take | which of them are installed, and the estate's own |
| every refusal | every value that would have been refused |

Nothing in the left column names an account, a cluster, a hostname, a
channel, a company or a secret; `hack/leak-canary.sh` holds that in CI.
Nothing in the right column requires the estate to write a template, a
processor, a rule or a script — it writes values, and a small amount of
Pulumi that calls a package here.

## The pieces

| Piece | Kind | State |
|---|---|---|
| `charts/observability-crds` | the CustomResourceDefinitions, owned separately | released |
| `charts/observability-stack` | the stores, the proxy, the alerters, Alertmanager, network policy, backups, optionally Grafana | released |
| `charts/observability-emitters` | per-cluster collection: metrics agent, log agent, OpenTelemetry gateway | released |
| `charts/platform-alerts` | the rules that catch a silent failure | released |
| `pkg/tenancy` | one input, two shapes: the proxy's users or the issuer's claim | released |
| **`notifications:` in the stack chart** | the one router: receivers, routing shape, template, refusals | [designed](notifications.md) |
| **`charts/alert-ingress` + `cmd/alert-ingress`** | events born outside the cluster, into the same router | [designed](alert-ingress.md) |
| **`pkg/statusbox` + `setup.sh`** | the watcher outside: deadman, external probes, status pages | [designed](statusbox.md) |
| **`charts/observability-dashboards`** | the generic dashboards, and the lint every dashboard passes | [designed](dashboards.md) |
| **store self-alerts** in the stack chart | rules on the stack's own counters | [designed](notifications.md#store-self-alerts) |

## How the pieces connect

```
                     every cluster                                 the install
  ┌──────────────────────────────────────────┐        ┌──────────────────────────────────────┐
  │ applications ──OTLP──► gateway ──┐       │        │  vmauth ◄── Grafana (user's token)   │
  │ scrape targets ──► metrics agent ─┼──────┼─write──►  ├─ metrics store ◄── vmalert (metrics)│──┐
  │ container stdout ─► log agent ────┘      │        │  ├─ log store     ◄── vmalert (logs)   │  │
  └──────────────────────────────────────────┘        │  └─ trace store                        │  │
                                                      │        vmalert ──► Alertmanager(optional)┼──► Slack (by cluster × namespace × severity)
  ┌──────────────────────────────────────────┐        │                                        │  │
  │ the cloud: findings, sign-ins,           │        │                                        │  │ vmauth, one bearer
  │ budgets, key use ──SNS──► alert-ingress ─┼────────┼────────────────────────────────────────┘  │ token, one route:
  └──────────────────────────────────────────┘        └───────────────────────────────────────────┘ /api/v1/alerts
                                                                                                      ▲  (over a private network)
                                                      ┌───────────────────────────────────────────┐  │
                                                      │ the status box (outside every cluster)     │  │
                                                      │  gatus-<company> ×N (later)  public pages  │  │
                                                      │  gatus-ops         PULLS the alerting state┼──┘
                                                      └──────────────▲──────────────────────────────┘
                                                                     │ health check
                                                              the edge provider
```

Three parties watch each other, and none of them is inside the thing it
watches: the status box reads the install's own alerting state directly
(one bearer token, one route — `tenancy.alertReaders` — nothing pushed
into the box, no Alertmanager required on the install's side at all);
the status box probes the estate from outside and alerts when that read
goes quiet or the alert it is watching for disappears; the edge
provider's health check watches the status box. A dead alerting path, a
dead box or a dead estate is each noticed by one of the other two.
[doctrine.md](doctrine.md#the-watcher-lives-outside) has why the third
party is not optional, and [statusbox.md](statusbox.md#internal--status-pulled)
has why this is a read and not a push.

## Central install, remote writers

The diagram above draws one cluster running the install and its own
collectors. An estate with more than one cluster does not have to run a
second install for each: one cluster's `observability-stack` can be the
STORE for several others, each of which runs only
`charts/observability-emitters` and writes across the network to the
one that holds the data.

```
  ┌───────────────────────┐   write, bearer token,   ┌────────────────────────┐
  │ remote cluster         │   over the network       │ central cluster         │
  │  observability-emitters│──────────────────────────►  observability-stack   │
  │  (mode: operator-only) │   pinned to `cluster`    │  (mode: full)           │
  └───────────────────────┘                           └────────────────────────┘
```

Two values carry the whole shape, both on `charts/observability-stack`
alone — the remote cluster's own `charts/observability-emitters` needs no
chart change to take part, only a values-level destination:

- **The writer's identity is minted where it runs, never handed to the
  install.** Each remote cluster mints its OWN write token locally — the
  same credential machinery every writer has always used, nothing new —
  and the install is handed the token's value (a Secret name it reads,
  never a value it generates) through whatever secret-distribution
  mechanism the estate already uses to get a value from one cluster's
  namespace into another's. This repository does not prescribe that
  mechanism; it is data-plane, not mechanism, per the boundary above.
- **The install pins each remote writer to the cluster it is FOR.**
  `tenancy.writers[].cluster`, set, forces every series, log record and
  span that writer sends to belong to that cluster, overriding whatever
  the writer's own collector config claims — a bearer token alone proves
  nothing about which cluster it actually ran on. See docs/reference.md
  and values.yaml's own comment on `tenancy.writers` for the mechanism
  per signal, and `tenancy.ownCluster` for the companion refusal that
  keeps a writer from being pinned to the install's own identity instead
  of a remote one.

A cluster that holds no store of its own still needs somewhere for
`charts/observability-emitters`' `VMAgent` custom resource to be
reconciled — that needs the VictoriaMetrics operator's CONTROLLER, not
just its CRDs (`charts/observability-crds` is CRDs-only, applied first,
same as everywhere else). `charts/observability-stack`'s `mode:
operator-only` is that: one chart, so a cluster running only the operator
gets the exact same operator version as the cluster running the full
stack, never a second pin to track. See docs/reference.md's `mode` row
for the full contract.

## What a consumer writes, in full

For an estate with one install, one company and one edge provider — the
smallest shape that exercises everything. Every value below is invented.

**The stack** (`observability-stack`): the issuer and audience, the
principals and their grants (or the same list through `pkg/tenancy`),
the retention per store, the backup destination, the Secret names — and
now the `notifications:` block: a webhook Secret name, the external URL,
and the route list.

```yaml
notifications:
  externalUrl: https://grafana.example
  slack:
    webhookSecret: {name: example-slack-webhook, key: url}
  routes:
    - match: {k8s_cluster_name: example-cluster}
      critical: "#alerts-critical"
      warning: "#alerts"
    - match: {k8s_namespace_name: example-app}
      critical: "#example-app"
      warning: "#example-app"
```

**The emitters**, on every cluster: the cluster name, the tier, the
destinations, the write-token Secret name.

**The rules**: `platform-alerts` with the store list; the fleet-specific
pack, if any, is the estate's own chart.

**`alert-ingress`**: the topic allow-list and the mapping rules.

```yaml
topics:
  - "<the security-alerts topic ARN>"
mappings:
  - name: guardduty
    match: {"detail-type": "GuardDuty Finding"}
    alert:
      alertname: CloudSecurityFinding
      severity: '{{ if ge .detail.severity 7.0 }}critical{{ else }}warning{{ end }}'
      labels: {source: guardduty}
      annotations: {summary: "{{ .detail.title }}"}
```

**The status box**: one Pulumi call with the release version, two
secrets, and the instance list — each instance a Gatus configuration the
estate renders from wherever it keeps its hostnames. Today that list is
ONE instance: a single private page carrying every company's own
component alongside cluster infrastructure, reachable only over the
tailnet — no tunnel token, no public ingress, because nothing here is
public yet (statusbox.md, "The shape").

```go
statusbox.NewLightsail(ctx, "status", &statusbox.LightsailArgs{
    AvailabilityZone: "us-east-1a",
    Args: statusbox.Args{
        Version:  "v1.0.0",
        Hostname: "statusbox",
        Secrets: statusbox.Secrets{
            TailscaleAuthKey: tailnetKey,
            // opsYAML references ${ALERT_URL_ALERTS_READ}: the bearer
            // token tenancy.alertReaders minted, not a push URL — see
            // statusbox.md, "internal → status, pulled".
            AlertURLs: map[string]pulumi.StringInput{"alerts_read": alertsReadToken},
        },
        Instances: []statusbox.Instance{
            {Name: "ops", Port: 8084, Public: false, Config: opsYAML},
        },
    },
})
```

Later, once a company's own `status.<company domain>` hostname is
delegated, that company's public page is a SECOND instance added to the
same call — `TunnelToken` joins `Secrets`, a `{Name: "example-co", Port:
8081, Public: true, Config: exampleCoYAML}` entry joins `Instances`, and
its hostname joins `Hostnames: map[string]string{"example-co":
"status.example.com"}`. Nothing about the private instance above
changes when that happens.

**Dashboards**: `observability-dashboards` with the datasource UIDs;
the estate's own dashboards beside it, passing the same lint.

That is the whole of it. No estate writes a Helm template, an
Alertmanager routing tree, a collector pipeline, a rule expression for
the stack's own health, a cloud-init file or a shell script.

## What is deliberately not in the target

- **On-call and incident tooling.** Schedules, escalation, phone paging,
  postmortems. Alertmanager can hand an alert to any of those; this
  repository does not pick one, because that choice is about a team's
  rotation and not about telemetry. [doctrine.md](doctrine.md#notifications-are-not-on-call).
- **Email as a receiver.** Offered by Alertmanager, not surfaced here:
  a notification to a mailbox is a notification nobody is looking at,
  and a notification to a group is one the group's mail policy may
  silently reject. An estate that wants it can add the receiver kind;
  the chart does not make it easy.
- **A second alert router.** ChatOps bots, cloud-native chat
  integrations, Grafana's own alerting — each is a second place alerts
  can go and a second place silences have to be kept.
- **Multi-vantage probing from this repository's own hosts.** One box
  is one vantage; the second vantage is the edge provider's health
  checks, which exist anyway.
- **Anything that names the estate.** By construction.

## How to read the rest

- [doctrine.md](doctrine.md) — why the shape is what it is, including
  the sections added for the alert route.
- [notifications.md](notifications.md), [alert-ingress.md](alert-ingress.md),
  [statusbox.md](statusbox.md), [dashboards.md](dashboards.md) — one
  design page per planned piece: the values it takes, what it renders,
  what it refuses, how it is proven.
- [emitting.md](emitting.md) — the guide for whoever wires a service's
  SDK: the one address, which attributes are theirs, how to ask the
  store.
- [adoption.md](adoption.md) — install order for the complete set.
- [safety.md](safety.md), [reference.md](reference.md) — every refusal,
  every value, for what is released.

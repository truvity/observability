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
notices when the router itself is dead; public status pages per company;
and dashboards to read it all with.

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
  │ scrape targets ──► metrics agent ─┼──────┼─write──►  ├─ metrics store ◄── vmalert (metrics)│
  │ container stdout ─► log agent ────┘      │        │  ├─ log store     ◄── vmalert (logs)   │
  └──────────────────────────────────────────┘        │  └─ trace store                        │
                                                      │        vmalert ──► Alertmanager ───────┼──► Slack (by cluster × namespace × severity)
  ┌──────────────────────────────────────────┐        │                      ▲       │        │
  │ the cloud: findings, sign-ins,           │        │                      │       │Watchdog│
  │ budgets, key use ──SNS──► alert-ingress ─┼────────┼──────────────────────┘       │        │
  └──────────────────────────────────────────┘        └──────────────────────────────┼────────┘
                                                                                     ▼  (over a private network)
                                                      ┌──────────────────────────────────────┐
                                                      │ the status box (outside every cluster)│
                                                      │  gatus-<company> ×N   public pages    │
                                                      │  gatus-ops            deadman, probes │
                                                      └──────────────▲───────────────────────┘
                                                                     │ health check
                                                              the edge provider
```

Three parties watch each other, and none of them is inside the thing it
watches: the install's Alertmanager heartbeats into the status box; the
status box probes the estate from outside and alerts on the heartbeat's
silence; the edge provider's health check watches the status box. A dead
router, a dead box or a dead estate is each noticed by one of the other
two. [doctrine.md](doctrine.md#the-watcher-lives-outside) has why the
third party is not optional.

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

**The status box**: one Pulumi call with the release version, three
secrets, and the instance list — each instance a Gatus configuration the
estate renders from wherever it keeps its hostnames.

```go
statusbox.NewLightsail(ctx, "status", &statusbox.LightsailArgs{
    Version:   "v1.0.0",
    Tailscale: tailnetKey, Cloudflared: tunnelToken,
    Instances: []statusbox.Instance{
        {Name: "example-co", Port: 8081, Public: true, Config: exampleCoYAML},
        {Name: "ops",        Port: 8084, Public: false, Config: opsYAML},
    },
})
```

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

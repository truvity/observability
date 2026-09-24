# Notifications: the one router

Design for the `notifications:` block of `charts/observability-stack`.
Not yet released; this page is the contract the implementation is held
to.

## The problem it closes

The chart ships with Alertmanager routing to a receiver named
`blackhole`. That is honest — the chart cannot know a channel — but it
means a consumer who installs the stack, the collectors and the rules
has a system that evaluates every rule and tells nobody. Nothing about
it is unhealthy. It is the most expensive shape a monitoring system can
have, because it looks exactly like a quiet estate.

`alertmanager.config` today is a free-form object the consumer fills
with Alertmanager's own syntax. That has three costs: the chart cannot
refuse the blackhole shape, the consumer writes a routing tree by hand
that every other consumer writes again, and the message template —
which is where the cluster name, the runbook link and the Grafana link
live — is copied rather than shipped.

## The shape

```yaml
notifications:
  # vmalert's -external.url. Without it every link in a notification is
  # the alerter pod's hostname, which resolves nowhere a person is.
  externalUrl: https://grafana.example

  # Receiver kinds. Each is a mechanism (how the secret is mounted, what
  # the message looks like); none carries a value.
  slack:
    webhookSecret: {name: example-slack-webhook, key: url}
  webhook:
    - name: status-page
      urlSecret: {name: example-status-webhook, key: url}

  # Severity tiers, and where each goes when no route says otherwise.
  # `info` goes nowhere: an alert that pages nobody is a dashboard.
  severities:
    critical: {receiver: slack, channel: "#alerts-critical"}
    warning:  {receiver: slack, channel: "#alerts"}

  # Routes, in order. The first match wins per severity; a route that
  # names only `critical` sends its warnings to the default. The estate
  # renders this list from wherever it derives projects from namespaces.
  routes:
    - match: {k8s_cluster_name: example-cluster}
      critical: "#alerts-example-cluster"
      warning:  "#alerts-example-cluster"
    - match: {k8s_namespace_name: example-app}
      critical: "#example-app"
      warning:  "#example-app"

  # Which alerts also reach a webhook receiver — the status-page
  # bridge. Matchers, not channels.
  also:
    - receiver: status-page
      match: {severity: critical, customer_facing: "true"}
```

The chart renders from this an Alertmanager configuration with:

- `group_by: [alertname, k8s_cluster_name, k8s_namespace_name]`, so one
  failing namespace on one cluster is one notification;
- `group_wait: 30s`, `group_interval: 5m`, `repeat_interval: 4h` as the
  defaults, each a value;
- an inhibit rule so a `critical` on a cluster + namespace silences the
  `warning` on the same pair, and a rule that `Watchdog` inhibits
  nothing and is routed only by `deadman` (below);
- the Slack template: the cluster and the namespace in the title, the
  alert's `summary`, a link to the runbook from `runbookBaseUrl` +
  `runbook` annotation, a link to Grafana built from `externalUrl` and
  the alert's labels, and the silence link;
- the `Watchdog` route to the deadman receiver, when one is configured
  (`deadman.urlSecret`), with `repeat_interval` equal to the heartbeat
  interval the far end expects.

The receiver secret is **mounted**, never templated: the Slack webhook
arrives as a file the way the watchdog URL already does, and the
rendered configuration names the file. A manifest that contains a
webhook is a webhook in git.

## Refusals

| Shape | Why it is refused |
|---|---|
| `alertmanager.enabled` with no `notifications` | the blackhole: rules evaluated into nothing |
| a route or a severity naming a receiver that is not configured | a route to nowhere looks like a route |
| a receiver with no secret | a receiver that cannot send |
| a route matching on a label the collectors do not stamp (`tenant`, `env`, …) | matches nothing, pages nobody; the vocabulary is cluster × namespace |
| `externalUrl` unset | every link dead |
| `repeat_interval` on the deadman route longer than the far end's heartbeat | the far end alerts on healthy silence |

Each has a fixture under `tests/invalid/observability-stack/`.

## What stays the consumer's

The channel names, the webhook, the route list, the external URL. And
the *policy* of what is `critical`: the chart ships severities on its own
rules and documents the convention — `critical` means a person should
look now, `warning` means a person should look today, `info` is for a
dashboard — but a consumer's own rules carry whatever severity the
consumer gives them.

## Also in the same release: the vmalert settings that make routing true

Not values; changed defaults, because each has a wrong upstream default
that fails in a way that looks like something else:

- every vmalert carries `-remoteWrite.url` and `-remoteRead.url` to the
  metrics store, so `for:` timers survive a restart. Without them, a
  rolling upgrade resets every pending alert and a slow-burning one
  never fires.
- one evaluation interval, equal to the store's deduplication interval,
  equal to the scrape interval — 30s — so a rule never reads a window
  the store has already deduplicated differently.
- the logs alerter's `evalDelay` reduced, since the log store has no
  search latency offset.

## Store self-alerts

Shipped inside this chart because they name the stack's own counters,
scraped directly and never through the proxy. Each is a rule in a
`VMRule` the chart renders, following the `platform-alerts` contract:
the failure it catches, the healthy range it was measured against, the
threshold's headroom, a negative fixture.

| Rule | Catches |
|---|---|
| `StoreIgnoringRows` | `vm_rows_ignored_total` rising — a series past the label limit is discarded and the write answers 200; a sender can report millions written with zero errors while the store holds none |
| `StoreCardinalityNearLimit` | hourly or daily series at 90% of the limit |
| `LogStoreDroppingRows`, `TraceStoreDroppingRows` | `vl_rows_dropped_total`, `vt_rows_dropped_total` rate above zero |
| `LogStreamsChurning` | streams created faster than a partition explains — the shape a non-constant stream field produces |
| `WriterBufferGrowing`, `WriterDroppingPackets` | a collector's remote-write buffer growing, or packets dropped — the write path is blocked and the loss is proportional to how long it takes to look |
| `GatewayQueueFilling`, `GatewayExportFailing` | the OpenTelemetry gateway's queue above 80%, or send/enqueue failures — "sending queue is full" running for hours |
| `ProxyAtConcurrencyLimit` | vmauth refusing requests |
| `StoreDiskNearGuard` | free disk approaching the store's own minimum |
| `SnapshotOlderThanWindow` | the newest snapshot older than the backup schedule allows |

None of these can be written by a consumer, because each names a
counter the chart controls; and none is optional, because each is a
failure that reports itself nowhere else.

## Proof, before release

- golden renders for a Slack-only install and a Slack + webhook +
  deadman install;
- one fixture per refusal;
- a rendered configuration passes `amtool check-config`;
- the template renders against a fixture alert with every link
  resolving to a URL with no pod hostname in it.

After release, in a consumer: a synthetic critical reaches the right
channel within five minutes; a warning in a project's namespace reaches
that project's channel; scaling Alertmanager to zero fires the far end.

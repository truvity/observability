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
  failing namespace on one cluster is one notification (a value since
  0.11.0: `groupBy`);
- `group_wait: 30s`, `group_interval: 5m`, `repeat_interval: 4h` as the
  defaults, each a value;
- an inhibit rule so a `critical` on a cluster + namespace silences the
  `warning` on the same pair (`inhibit`, a value since 0.11.0 — see
  "Inhibition" below), and a rule that `Watchdog` inhibits
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
| `notifications.mode` outside `route` or `evaluate-only` | an unknown mode, refused by the schema rather than falling back to a guess |
| `notifications.mode: evaluate-only` with `alertmanager.enabled` true, or a receiver, severity, route, `also` bridge, `catchAll` or `drop` configured, or `alertmanager.notifierUrl` set | a channel or an Alertmanager configured beside a mode that mutes vmalert, which looks wired up and is never reached |
| `inhibit.equal` empty (schema) | any critical would mute every warning |
| a `drop` entry with no key (schema) | an empty matcher matches, and drops, every alert |
| `catchAll` naming a receiver that is not configured | every alert no tier claimed routed to nowhere |

Each has a fixture under `tests/invalid/observability-stack/`.

## Telegram

Added in 0.10.0: Telegram as a third receiver kind, beside `slack` and
`webhook`. Any combination is accepted, each severity tier picks one,
and a `telegram` block on its own is enough to satisfy the "no
receiver" refusal.

```yaml
notifications:
  externalUrl: https://grafana.example
  telegram:
    # An EXISTING Secret: its name and the key holding the bot token.
    # Never a value in these values, never in the rendered config.
    botTokenSecret: {name: example-telegram-bot, key: token}
    # The default destination. An integer, as Alertmanager takes it;
    # negative for a group or channel.
    chatId: -1000000000001
    # Optional: a forum topic in that chat. Unset or 0 is none.
    messageThreadId: 0
    # Optional: HTML (default), MarkdownV2 or Markdown.
    parseMode: HTML
    # Optional: default true, the same as the Slack and webhook receivers.
    sendResolved: true
  severities:
    critical: {receiver: telegram}
    # A tier may land somewhere else: its own chatId and/or thread.
    warning: {receiver: telegram, messageThreadId: 42}
```

What the chart renders from it:

- a Secret volume `notifications-telegram` on the VMAlertmanager pod,
  mounted at `/etc/alertmanager/notifications-telegram`, and every
  Telegram receiver reading the token with `bot_token_file` from there
  — the same mounted-file rule as `api_url_file` and `url_file`, so the
  token is never in the release's manifest;
- one Alertmanager receiver per distinct (chat, thread) any tier
  resolves to, named `telegram-<chat>[-<thread>]` with a negative chat
  id's sign spelled `n` (`telegram-n1000000000001-42`), so two tiers in
  one chat are one receiver;
- `telegram_configs` with `chat_id`, `message_thread_id` (only when
  set), `parse_mode` and `send_resolved`;
- the shipped message: the Slack template's facts — status, alert name,
  cluster/namespace, each alert's `summary`, the runbook link when
  `runbookBaseUrl` is set, the Grafana link, the silence link — written
  in Telegram's HTML.

Why HTML is the default and the only mode with a shipped message:
Alertmanager executes a Telegram message through Go's `html/template`
when `parse_mode` is `HTML`, so every value interpolated from an alert
(a summary with a `<` or an `&` in it) is escaped by Alertmanager
itself. Markdown and MarkdownV2 have no such escaping: one `_` or `*`
in an alert name and Telegram refuses the whole message, so the
notification is lost rather than garbled. The schema therefore requires
`telegram.message` — your own Alertmanager template, written for that
mode — whenever `parseMode` is not `HTML`.

**The chat id from a Secret (0.11.0).** A chat id is not a credential —
without the bot token it sends nothing — but some estates keep it out
of git anyway. `chatIdSecret: {name, key}` replaces `chatId`: the Secret
is mounted at `/etc/alertmanager/notifications-telegram-chat` and read
with Alertmanager's `chat_id_file`, which exists since Alertmanager
v0.31.0 (the pinned operator deploys v0.34.0). The chart never sees the
value, so the receiver is named `telegram-chatfile` (plus the thread),
not after the chat. It may be the same Secret as the bot token.

```yaml
notifications:
  telegram:
    botTokenSecret: {name: example-telegram-bot, key: token}
    chatIdSecret: {name: example-telegram-bot, key: chat_id}
```

What differs from Slack, on purpose:

- a Telegram tier has no `channel`, and a route cannot override one: a
  route's per-tier value is a Slack channel name, so on a Telegram tier
  it would be read by nothing, and it is refused. Send a project to a
  different chat by giving it a Slack tier, or, for a whole tier, with
  that tier's own `chatId`/`messageThreadId`;
- `also` stays webhook-only: it is the status-page bridge.

Compatibility: before 0.10.0 `telegram` was not a keyword, so an
install could have a `notifications.webhook` entry NAMED `telegram` (a
webhook bridge to Telegram) with tiers pointing at it. That install
renders exactly as before: `receiver: telegram` means the Telegram kind
only once `notifications.telegram` is configured, and configuring it
beside a webhook of that name is refused as ambiguous.

The shipped HTML message was checked end to end against the
Alertmanager this chart's operator runs (`prom/alertmanager:v0.34.0`,
bot API pointed at a local stand-in): the token is read from the
mounted file, `message_thread_id` and `parse_mode` arrive as rendered,
and a summary of `disk <90% & rising_fast` arrives as
`disk &lt;90% &amp; rising_fast`.

| Shape | Why it is refused |
|---|---|
| `telegram` without `botTokenSecret`, or with an empty `name`/`key` (schema) | a receiver that cannot authenticate cannot send |
| `telegram` with neither or both of `chatId` and `chatIdSecret` (schema) | a bot with nowhere to post, or two defaults for one chat |
| `parseMode` other than `HTML` without `message` (schema) | the shipped message is HTML; under Markdown it fails on the first `_` |
| a tier with `receiver: telegram` and no `notifications.telegram` (and no webhook of that name) | a route to a receiver that is not configured |
| `chatId`/`messageThreadId` on a tier that is not Telegram | read by nothing |
| `channel` on a Telegram tier, or a route overriding a Telegram tier | a Slack channel where no Slack receiver reads it |
| `notifications.telegram` beside a webhook named `telegram` | `receiver: telegram` would be ambiguous |

## Inhibition

One rule: a `critical` silences a `warning` whose values are equal for
every label in `notifications.inhibit.equal` (default
`[k8s_cluster_name, k8s_namespace_name]`).

**The hazard.** Alertmanager compares a label that is missing on both
alerts as EQUAL. An alert that carries none of the `equal` labels
therefore matches every other alert that carries none of them, and one
such `critical` mutes every such `warning` in the install — with
nothing anywhere saying so. It is easy to hit: an estate whose alerts
carry `namespace` rather than `k8s_namespace_name` hits it on every
alert, and a rule that aggregates `by (namespace, …)` drops
`k8s_namespace_name` from its own alerts even on an estate that stamps
it.

**The guard (0.11.0, on by default).** `inhibit.requireLabels: true`
renders a source matcher `<label> =~ ".+"` for every `equal` label: a
critical that does not carry them inhibits nothing, and `equal` then
requires the warning to carry the same non-empty values. It errs loud —
a warning the unguarded rule muted by accident now arrives — which is
why it is the default rather than documentation alone: this chart's own
`platform-alerts` hit the hazard on a default install.
`CronJobNotSucceeding` (critical) and `BackupJobFailed` (warning)
aggregate `by (namespace, …)`, which drops `k8s_namespace_name`, so one
CronJob not succeeding muted every failed backup Job's warning on its
cluster. `requireLabels: false` renders the 0.10.0 rule byte for byte.

Beyond the guard, name labels your alerts actually carry
(`inhibit.equal`, and `groupBy` for the same reason), or turn the rule
off (`inhibit.enabled: false`). `groupBy` has the milder form of the same
trap: a label no alert carries groups every alert together into one
notification.

```yaml
notifications:
  groupBy: [alertname, namespace]
  inhibit:
    equal: [namespace]
```

## Beyond `critical` and `warning`

This chart's rules carry only `critical` and `warning`, and by default
only those two are routed: an alert that pages nobody is a dashboard.
An estate that wants more says so:

- `notifications.catchAll` — a severity target (`slack`, `telegram` or a
  webhook, the same shape as a `severities` entry) for every alert no
  tier claimed: `info`, no `severity`, or a tier `severities` leaves out.
  It is the fallback of EVERY node in the primary tree, so an `info`
  alert inside a project route lands there too. Set, the two tiers are
  no longer required. With no deadman receiver configured, `Watchdog`
  — which fires for as long as the install is healthy — is routed to
  nobody first, or it would arrive every `repeatInterval`.
- `notifications.drop` — exact matchers routed to nobody, ahead of every
  other route: for an alert the estate has decided it will never act
  on, without editing the rule set that ships it. Other always-firing
  vendored rules (`InfoInhibitor`) belong here.

```yaml
notifications:
  catchAll: {receiver: telegram}
  drop:
    - {alertname: InfoInhibitor}
```

## Evaluate, notify nobody yet

`notifications.mode: evaluate-only` is the other accepted shape, for a
consumer who does not yet have a Slack webhook or a status-page
credential and wants every rule evaluated anyway rather than turning the
whole alerting path off. It exists because the two components this
value touches used to leave exactly one way to avoid the refusals above
without a receiver: `alertmanager.enabled: false` **and**
`vmalert.enabled: false` — which stops every rule from being evaluated
at all, the least useful shape a monitoring stack can be in, silently
reached by disabling two unrelated-looking toggles together.

Use it when: a receiver is coming but is not wired up yet, or an install
genuinely has nowhere to page (a personal cluster, a demo) but the rules
should still run so their state is visible somewhere.

What it renders:

- both vmalerts (`metrics` and `logs`, if `vmalert.logs.enabled`) keep
  running, with no change to `evaluationInterval`, `datasource`,
  `remoteWrite` or `remoteRead` — every rule is evaluated on schedule,
  exactly as in `route` mode;
- neither renders a `notifiers:` block; both instead carry
  `-notifier.blackhole` in `extraArgs`, the flag vmalert has shipped
  since v1.93.0 for evaluating alerting rules "without sending any
  notifications to external receivers" (VictoriaMetrics
  `CHANGELOG_2023.md`; the flag's own help text, unchanged through the
  v1.152.0 this chart's operator defaults to, adds that `-notifier.url`,
  `-notifier.config` and `-notifier.blackhole` are mutually exclusive —
  `app/vmalert/notifier/init.go`). The VictoriaMetrics operator refuses
  the same combination at the CustomResource level
  (`api/operator/v1beta1/vmalert_types.go`, `validateNotifierConfigs`):
  `spec.notifier`, `spec.notifiers` and `spec.notifier.notifierConfigRef`
  must all be absent when `notifier.blackhole` is one of `extraArgs`;
  this chart simply never renders them in this mode, so that CRD-level
  refusal is never reached;
- Alertmanager (the `VMAlertmanager` object) is **not rendered** in this
  mode, refused if `alertmanager.enabled` is left at its default `true`
  or set explicitly: with nothing to route — vmalert sends its result
  nowhere on purpose — an Alertmanager beside it would be a component
  with no job, which is its own way to look more configured than it is.

What stays visible: every alert vmalert evaluates shows up in that
vmalert's own `/vmalert/` UI and `/api/v1/alerts` API, state included,
the same as in `route` mode. And because both vmalerts here already
carry `-remoteWrite.url` unconditionally (see below), the `ALERTS` and
`ALERTS_FOR_STATE` time series vmalert writes for every active alert
(`app/vmalert/rule/alerting.go`, `toTimeSeries`) land in the metrics
store regardless of the notifier — that write does not go through the
notifier at all, blackholed or not — so `ALERTS{alertname="..."}` is a
query away in this mode exactly as it would be in `route` mode.

Why this is not the blackhole `alertmanager.enabled` with nothing
configured used to render: that shape was never a decision — the
default rendered it whether anyone meant to or not, and looked exactly
like a working install because Alertmanager itself was healthy and
routing, just to a receiver with no configuration. `evaluate-only` is a
value nobody reaches by omission: it is refused the moment it disagrees
with `alertmanager.enabled`'s own default, so setting it is the only way
to get it, and the render it produces has no Alertmanager to look
healthy in the first place — the absence is the whole visible fact,
not a receiver quietly doing nothing behind a passing health check.

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
| `MetricStoreIgnoringRows` | `vm_rows_ignored_total` rising — a series past the label limit is discarded and the write answers 200; a sender can report millions written with zero errors while the store holds none |
| `MetricStoreCardinalityNearLimit`, `MetricStoreDailyCardinalityNearLimit` | hourly, or daily, series at 90% of the limit — two rules, different windows |
| `LogStoreDroppingRows`, `TraceStoreDroppingRows` | `vl_rows_dropped_total`, `vt_rows_dropped_total` rate above zero |
| `LogStoreStreamsChurning`, `TraceStoreStreamsChurning` | streams created faster than a partition explains — the shape a non-constant stream field produces, one rule per store since each keys streams differently |
| `WriterBufferGrowing`, `WriterDroppingPackets` | a collector's remote-write buffer growing, or packets dropped — the write path is blocked and the loss is proportional to how long it takes to look |
| `GatewayQueueFilling`, `GatewayExportFailing`, `GatewayEnqueueFailing` | the OpenTelemetry gateway's queue above 80%, a destination refusing a batch already accepted, or the gateway's own queue refusing at the door — three different moments of "sending queue is full" running for hours |
| `ProxyAtConcurrencyLimit` | vmauth refusing requests |
| `MetricStoreDiskNearGuard`, `LogStoreDiskNearGuard`, `TraceStoreDiskNearGuard` | free disk approaching the store's own minimum, one rule per store |
| `MetricStoreSnapshotOlderThanWindow`, `LogStoreSnapshotOlderThanWindow`, `TraceStoreSnapshotOlderThanWindow` | the newest snapshot older than the backup schedule allows, one rule per store |

None of these can be written by a consumer, because each names a
counter the chart controls; and none is optional, because each is a
failure that reports itself nowhere else. Every rule that watches one
specific store is named for that store — three stores exist, and a rule
named just "Store..." does not say which one paged you.

## Proof, before release

- golden renders for a Slack-only install and a Slack + webhook +
  deadman install, a Telegram-only install and a Telegram + Slack +
  webhook install;
- one fixture per refusal;
- a rendered configuration passes `amtool check-config`;
- the template renders against a fixture alert with every link
  resolving to a URL with no pod hostname in it.

After release, in a consumer: a synthetic critical reaches the right
channel within five minutes; a warning in a project's namespace reaches
that project's channel; scaling Alertmanager to zero fires the far end.

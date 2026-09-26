# Notification subscriptions: many workspaces, many channels, one router

> **Proposal — not implemented.** This page describes a possible
> evolution of `notifications:` (see [notifications.md](notifications.md)).
> Nothing here renders today; it is written to be argued with. Where a
> claim about Slack or Alertmanager matters to the design, the source is
> cited and anything unconfirmed is marked as such.

## The problem

`notifications.slack` is one workspace, one webhook, one credential.
`notifications.severities` sends every alert of a tier to one channel
unless a `routes[]` entry overrides it, and an override is still one
channel. That shape has served a single small estate well, but three
requests now sit on top of it, and none of them fits:

1. **More than one Slack workspace.** An estate that runs alerting for
   more than one organisation — its own, and a partner's — needs each
   cluster's alerts to land in a *different* workspace, not just a
   different channel in the same one.
2. **More than one channel per workspace**, addressed independently of
   severity — today's `routes[]` already does cluster/namespace ×
   severity, so this part mostly exists; the gap is doing it across
   several workspaces at once with one shared vocabulary.
3. **Both a catch-all and fine-grained routes, at the same time.** The
   maintainer's own framing: a project wants its own channel for its
   own noise, and someone still wants one channel that sees everything,
   so an unfiled alert is never simply unseen. Today's routing tree
   picks ONE receiver per severity per alert — the most specific
   `routes[]` entry that matches, or the tier default if none does — so
   "also send this to the firehose" is not expressible without the
   `also` webhook bridge, which is Slack-shaped only by accident (it
   renders to any webhook receiver, unfiltered by severity).

Threaded through all three is a fourth question the maintainer asked
directly: what is a reasonable dial between "all alerts" and the most
granular route there is? The candidates, ordered from coarsest to
finest, are:

```
all alerts on a workspace
  → all alerts on one cluster
    → all alerts on one cluster + one namespace
```

and every rung of that ladder is crossed with a severity floor —
"critical only" up to "everything, down to warning" — because a
catch-all channel that pages on every warning is not a catch-all,
it is a second pager.

## The proposed shape

Two new lists replace the one-workspace assumption. Nothing about
`severities` or `routes` changes in this sketch other than being
superseded — see [Migration](#migration-from-todays-shape) below for
whether that is the right call.

```yaml
notifications:
  workspaces:
    - name: acme
      # Bot-token credential: ONE credential per workspace, many
      # channels. See "The Slack facts" below for why this is the shape
      # that actually delivers "several channels per workspace" — a
      # webhook credential cannot.
      botTokenSecret: {name: acme-slack-bot-token, key: token}
    - name: partner
      botTokenSecret: {name: partner-slack-bot-token, key: token}

  subscriptions:
    # The catch-all: every alert, both workspaces, at critical only.
    # (Two subscriptions, one per workspace — "the firehose" is not a
    # single channel across two workspaces, because nothing SENDS a
    # message across a workspace boundary; see below.)
    - workspace: acme
      channel: "#alerts-all"
      match: {}
      minSeverity: critical

    # Fine-grained: everything from prod-eu, warning and up, in acme.
    - workspace: acme
      channel: "#alerts-prod-eu"
      match: {cluster: prod-eu}
      minSeverity: warning

    # Finer still: one project's namespace, in the partner's workspace.
    - workspace: partner
      channel: "#billing-alerts"
      match: {cluster: prod-eu, namespace: billing}
      minSeverity: warning
```

**Every subscription is evaluated independently, and every match is
`continue: true`.** A `Watchdog`-shaped alert aside, nothing here picks
"the one winning route" the way today's `routes[]` does. Take a
`critical` alert on `prod-eu`/`billing`: it satisfies all three
subscriptions' `match` (the `{}`, the `{cluster: prod-eu}`, and the
`{cluster: prod-eu, namespace: billing}` all hold) and all three
`minSeverity` floors (`critical` clears every floor from `critical` up
to `all`), so it reaches `#alerts-all`, `#alerts-prod-eu`, AND
`#billing-alerts` — three notifications, one alert. Drop it to
`warning` and it stops clearing the `#alerts-all` subscription's
`minSeverity: critical` floor, so only the other two fire. The
catch-all and the fine-grained channel are not alternatives, they are
both true whenever both floors are cleared, which is exactly the "we
might need both" the maintainer named. Ordering among
`subscriptions[]` therefore carries no meaning for delivery (no
first-match-wins); it only matters for two refusals below (duplicate
detection, and a deterministic error order).

### The granularity ladder, spelled out

`match` takes zero, one, or two keys:

| `match` | Reads as |
|---|---|
| `{}` | every cluster, every namespace |
| `{cluster: X}` | every namespace on cluster X |
| `{cluster: X, namespace: Y}` | one project on one cluster |

A `namespace` with no `cluster` is refused (see Refusals) — a namespace
name is only unique within a cluster, and `production` on one cluster is
not `production` on another; the doctrine's own vocabulary is "cluster ×
namespace", never namespace alone (docs/doctrine.md, "Tenancy is cluster
× namespace"). This mirrors `tenancy.principals[].grants` exactly on
purpose: both are "which cluster, which namespace" statements, read by
two different mechanisms (vmauth's JWT claim vs. Alertmanager's route
tree) from the same two label names.

`minSeverity` is the other axis, independent of `match`:

| `minSeverity` | Delivers |
|---|---|
| `critical` | critical only |
| `warning` | warning and critical |
| `all` | every tier this router knows |

### What `all` means today, concretely

The chart's own rules use three severity values: `critical`, `warning`,
and `info` (`charts/platform-alerts/templates/vmrule.yaml`, and see the
`BackupJobFailed` rule in `tests/golden/platform-alerts/everything.yaml`
for a real `severity: info` example). But the router this chart renders
today has **no `info` route at all**, on purpose — docs/notifications.md
and the `vmalertmanager.yaml` template both say so directly: "there is
deliberately no `info` route", because "an alert that pages nobody is a
dashboard" (docs/notifications.md, "What stays the consumer's").

So `minSeverity: all` in this proposal means *every tier the router
routes* — today, `critical` and `warning` — not literally every
severity string a rule might carry. It is spelled `all` rather than
`warning` for two reasons: it reads as an explicit choice rather than an
accident of today's two-tier world, and it is forward-compatible if a
third routed tier is ever added (an `all` subscription would pick it up
without being rewritten; a `warning` one would not). `info` stays
unrouted unless a future decision changes that — this proposal does not
make that call; see Open Question 4.

### How this renders

Each subscription becomes one child route under the same wrapping
`match: {}` / `continue: true` node the chart already renders (see
`vmalertmanager.yaml`), narrowed by whatever `match` gives it and by a
severity matcher built from `minSeverity`. One rendering change this
proposal needs: today's tree writes child routes with the legacy
`match:` map, which is exact-only. A `minSeverity` floor above
`critical` needs a regex ("this tier or higher"), which `match:` cannot
express — Alertmanager's regex equivalent is a same-shaped `match_re:`
map, or the newer unified `matchers:` list that can mix exact and regex
operators in one place. The example below uses `matchers:`, since a
route that needs both an exact cluster/namespace matcher and a regex
severity matcher is exactly the case `matchers:` was added to make less
awkward.

```yaml
route:
  receiver: notifications-none
  group_by: [alertname, k8s_cluster_name, k8s_namespace_name]
  routes:
    - match: {}
      receiver: notifications-none
      continue: true
      routes:
        # subscription: acme / #alerts-all / {} / critical
        - matchers:
            - severity = "critical"
          receiver: slack-acme-alerts-all
          continue: true

        # subscription: acme / #alerts-prod-eu / {cluster: prod-eu} / warning
        - matchers:
            - k8s_cluster_name = "prod-eu"
            - severity =~ "critical|warning"
          receiver: slack-acme-alerts-prod-eu
          continue: true

        # subscription: partner / #billing-alerts / {cluster: prod-eu, namespace: billing} / warning
        - matchers:
            - k8s_cluster_name = "prod-eu"
            - k8s_namespace_name = "billing"
            - severity =~ "critical|warning"
          receiver: slack-partner-billing-alerts
          continue: true
receivers:
  - name: notifications-none
  - name: slack-acme-alerts-all
    slack_configs:
      - channel: "#alerts-all"
        api_url: https://slack.com/api/chat.postMessage
        app_token_file: /etc/alertmanager/notifications-acme/token
        send_resolved: true
        # ... same title/text template as today, per workspace's externalUrl
  - name: slack-acme-alerts-prod-eu
    slack_configs:
      - channel: "#alerts-prod-eu"
        api_url: https://slack.com/api/chat.postMessage
        app_token_file: /etc/alertmanager/notifications-acme/token
  - name: slack-partner-billing-alerts
    slack_configs:
      - channel: "#billing-alerts"
        api_url: https://slack.com/api/chat.postMessage
        app_token_file: /etc/alertmanager/notifications-partner/token
```

`minSeverity: warning` compiles to the regex matcher
`severity =~ "critical|warning"` — "this tier and every one above it" is
an alternation over the known tiers at or above the floor, which is
plain Alertmanager matcher syntax
(<https://prometheus.io/docs/alerting/latest/configuration/>, matcher
operators `=`, `!=`, `=~`, `!~`). `minSeverity: critical` needs no
regex, an exact `severity = "critical"` matcher already says it.
`continue: true` on every subscription's own child route is what lets
one alert reach several receivers, the same fan-out mechanism the chart
already uses once, for the primary-tree/`also` split
(<https://prometheus.io/docs/alerting/latest/configuration/>, `continue`;
confirmed against the existing `vmalertmanager.yaml` template in this
repo, which sets it for exactly this reason today).

Grouping (`group_by`) is unchanged and global: it groups by
`alertname`, `k8s_cluster_name`, `k8s_namespace_name` regardless of
which subscriptions an alert reaches, so the catch-all and the
fine-grained channel each get their own grouped notification — one per
channel per group, not one shared draft.

### Ordering and overlap

There is no first-match-wins here, unlike today's `routes[]`. Every
subscription that matches, fires. The two things that DO need ordering
or dedup:

- **Two subscriptions naming the same workspace + channel + match +
  minSeverity** are a refusal (below) — an exact duplicate is very
  likely a copy-paste that was meant to change one field and did not.
- **Two subscriptions naming the same workspace + channel with
  DIFFERENT `match`/`minSeverity`** are allowed and both render: e.g.
  one subscription puts `#alerts-prod-eu` on `{cluster: prod-eu}` at
  `warning`, and a second puts the same channel on `{cluster:
  prod-eu, namespace: billing}` at `critical` for extra emphasis. The
  channel gets two notifications for a matching alert — from
  Alertmanager's side these are two different receivers only if the
  rendered receiver name differs; **the render must give each
  subscription its OWN receiver name** (not dedupe by channel), so two
  subscriptions to the same channel with different Slack message text
  (should a future release add per-subscription text) do not collide.
  This is a note for the implementation, not something this proposal
  resolves further.

## The Slack and Alertmanager facts

These are the load-bearing facts the shape above depends on. Each is
cited; the one thing left unconfirmed is called out plainly.

1. **A modern Slack-app incoming webhook is bound to ONE channel, fixed
   at the moment a user authorises it, and the payload's `channel`
   field is silently ignored for it.** Slack's own docs: "You cannot
   override the default channel (chosen by the user who installed your
   app), username, or icon when you're using incoming webhooks to post
   messages"
   (<https://docs.slack.dev/messaging/sending-messages-using-incoming-webhooks/>).
   This is why "several channels in one workspace" cannot mean "one
   webhook Secret, several channel names" — it would need one webhook
   authorised per channel, each its own Secret, which is workable but
   does not read as "one workspace" at all in the values file; it reads
   as N unrelated webhooks that happen to share a Slack team. (The
   *legacy* custom-integration webhook, which Slack has not allowed
   creating fresh for years, did honour a `channel` override — not a
   shape to design toward.)

2. **Alertmanager's `slack_config` supports a second, distinct
   credential kind for exactly this: a Slack bot token used against
   `chat.postMessage`, where `channel` is a real per-message parameter.**
   The official configuration reference
   (<https://prometheus.io/docs/alerting/latest/configuration/>) states
   Slack notifications can be sent via incoming webhooks OR bot tokens:
   with a bot token, `api_url` is set to the fixed
   `https://slack.com/api/chat.postMessage` endpoint, the token is
   supplied via `app_token`/`app_token_file`, and `channel` then
   addresses any channel the bot has been invited to or has `chat:write`
   scope for. That is the mechanism this proposal's
   `workspaces[].botTokenSecret` is written against: **one Secret per
   workspace, many channels**, which is what "several channels in each
   Slack workspace" needs to mean for the values file to have one entry
   per workspace rather than one per channel.

3. **This chart's default Alertmanager already carries this feature —
   no image-tag bump needed.** Bot-token / `app_token` support landed
   in Alertmanager **v0.30.0** (changelog: "`[FEATURE] Slack app
   support. #4211`"; verified from upstream's own release notes,
   `gh api repos/prometheus/alertmanager/releases/tags/v0.30.0`).
   **v0.31.0** made no functional change to it, only a documentation
   fix ("`[ENHANCEMENT] docs(slack): Document missing app configs.
   #4871`", same source, tag `v0.31.0`) — which is consistent with
   `app_token`/`app_token_file` only reliably appearing in the rendered
   `configuration.md` from v0.31.0 onward. The VictoriaMetrics operator
   this chart depends on (`victoria-metrics-k8s-stack` 0.93.0, vendoring
   `victoria-metrics-operator` appVersion `v0.74.1`) compiles in a
   **default** `VMAlertmanager` image of exactly `prom/alertmanager:v0.31.0`
   — confirmed directly from that pinned tag's source,
   `internal/config/config.go` in
   <https://github.com/VictoriaMetrics/operator/blob/v0.74.1/internal/config/config.go>,
   lines 472–475:
   ```go
   // Default container image for Alertmanager.
   Image string `default:"prom/alertmanager" env:"ALERTMANAGERDEFAULTBASEIMAGE"`
   // Default Alertmanager version.
   Version string `default:"v0.31.0" env:"ALERTMANAGERVERSION"`
   ```
   **v0.31.0 is past the v0.30.0 floor**, so bot-token mode works
   against the image this chart already deploys, with nothing new to
   pin, override, or refuse on that account. Today, nothing in
   `notifications:` or anywhere else in this chart's schema lets a
   consumer set `alertmanager.image.tag` at all — the `alertmanager:`
   values block has no `image` key
   (`charts/observability-stack/values.schema.json`) — and that absence
   is what makes the floor moot rather than a gap: there is no lever in
   this chart a consumer could use to pin an OLDER, unsupported
   Alertmanager even if they wanted to. See Open Question 6 for whether
   an `alertmanager.image.tag` value belongs in this proposal anyway,
   for a different reason.

4. **Matcher and fan-out mechanics used above are current, general
   Alertmanager behaviour, not new**: matcher operators `=`, `!=`, `=~`,
   `!~` and the `continue` field are both documented at
   <https://prometheus.io/docs/alerting/latest/configuration/>, and
   `continue: true` fan-out is already exercised by this chart's own
   `vmalertmanager.yaml` template (the wrapping node before the
   `routes[]`/`also` split).

**Not confirmed:** whether VictoriaMetrics' own `VMAlertmanager` CRD (as
opposed to upstream Alertmanager's binary and config format, which the
CR just runs unmodified) has any opinion on `app_token_file` — the CR's
`configRawYaml` is passed to the Alertmanager binary as-is in every
release this chart has ever rendered, so there is no operator-specific
translation layer to check, but this was not tested end to end against
a live pod as part of writing this proposal. That is one thing golden +
`apply` proof would need to cover before release (see
docs/notifications.md, "Proof, before release").

## Refusals this chart would add

In the existing style — each names what silently breaks, not just that
the shape is wrong:

| Shape | Why it is refused |
|---|---|
| `alertmanager.enabled: true`, `notifications.mode` unset or `notify`, and neither `workspaces`/`subscriptions` NOR the legacy `slack`/`severities` configure a receiver | the blackhole again — see docs/notifications.md |
| a subscription naming a `workspace` that is not in `workspaces[]` | a route to a workspace that does not exist looks like a route and reaches nobody |
| a `workspaces[]` entry with no `botTokenSecret` (and, if webhook-per-channel sugar is kept, no webhook secret either — see Migration) | a workspace that cannot send |
| a subscription's `match.namespace` set with no `match.cluster` | a namespace name is only unique within a cluster; an unscoped namespace matcher is a route to a name that means something different on every cluster it happens to appear on |
| a subscription's `match` naming a key other than `cluster`/`namespace` | the collectors stamp exactly two dimensions on an alert, `k8s_cluster_name` and `k8s_namespace_name` (docs/doctrine.md); anything else matches nothing any rule carries |
| two subscriptions with identical `workspace` + `channel` + `match` + `minSeverity` | an exact duplicate is one notification twice, or — more likely — a copy meant to change a field that was left unchanged |
| `workspaces[]` with two entries of the same `name` | the second is unreachable; every `subscriptions[].workspace` naming it resolves to whichever the renderer happened to pick |
| `notifications.mode: evaluate-only` together with any `workspaces`/`subscriptions`/`slack`/`webhook` entry | says two things at once: "notify nobody yet" and "notify these people" — see below |

There is deliberately no refusal here for an Alertmanager image too old
for bot tokens: see Slack fact 3 above — the default this chart already
deploys is past the feature's floor, and the chart has no value that
lets a consumer pin an older one instead. If that ever changes (see
Open Question 6), a version-floor refusal in the existing
`vmauth.image.tag` style would need to be added at the same time.

Each would get a fixture under `tests/invalid/observability-stack/`,
following the existing `notifications-*.yaml` naming.

## Migration from today's shape

**Recommendation: keep `slack`/`severities`/`routes` as sugar, expressed
in terms of the new shape, not a second code path.** Concretely:

- `notifications.slack.webhookSecret` becomes exactly one implicit
  workspace named (say) `default`, using a webhook credential kind
  `workspaces[]` would also need to support (see below) — not the
  bot-token kind, since a plain webhook Secret is exactly what it is
  today and migrating it to a bot token is a Slack-app re-authorisation
  step this proposal must not force on every existing install silently.
- Every `severities.<tier>` entry becomes one `subscriptions[]` entry
  with `match: {}` and `minSeverity: <tier>` (a `severities.critical`
  and `severities.warning` pair, unrelated in today's shape, become two
  independent subscriptions rather than one two-key struct — which is a
  simplification, since nothing in the new shape needs the pairing).
- Every `routes[].match` + per-tier channel becomes one subscription
  per tier the route entry names, with that `match` carried over
  unchanged (`k8s_cluster_name`/`k8s_namespace_name` map directly to
  `cluster`/`namespace`).
- `also` has no equivalent need once fan-out is the default behaviour
  of every subscription — an `also` entry IS a subscription with
  `minSeverity` derived from its own `match.severity`, if it names one,
  or `all` if it does not. Whether to actually delete `also` or keep it
  as the one remaining way to reach a plain webhook (status pages are
  not Slack, and `workspaces[]` is Slack-shaped) is Open Question 3.

The alternative — a hard replacement that requires every consumer to
rewrite their values at the next chart bump — is worse for a chart this
few releases old only if a real consumer already depends on the old
shape in a way sugar cannot cover; this proposal assumes not, but the
maintainer is the one who knows if that assumption holds.

Either way, `notifications.webhook[]` (plain webhook receivers, for the
status-page bridge) is untouched: it is not Slack-shaped and nothing
above changes it.

## The "optional notifications" mode

**Decision already made** (per the request that opened this issue):
Slack must be optional. A cluster with `alertmanager.enabled: true` and
no Slack credential yet must still install cleanly, evaluating every
rule, without falling back to the silent `blackhole` shape this whole
chart exists to retire, and without forcing the consumer to disable
alert evaluation entirely just because no token exists yet — which is
what happened, and is exactly the failure docs/notifications.md was
written to close from the other direction.

Proposed value: **`notifications.mode`**, one of:

- `notify` (the default, and the only mode today): at least one
  receiver kind must be configured, exactly as `_validate.tpl` already
  refuses.
- `evaluate-only`: `alertmanager.enabled: true`, every rule evaluates,
  Alertmanager runs, and the router renders **the deadman route (if
  `watchdog.secretName` is set) and nothing else** — every alert this
  install's own rules produce lands on `notifications-none`, and the
  render refuses if any `workspaces`/`subscriptions`/`slack`/`webhook`
  key is also present, so the value cannot silently coexist with a
  half-finished Slack config someone forgot to remove.

Why this is not the `blackhole` it looks like at first read: the
`blackhole` failure was never "a route to nowhere exists" — a
deliberate "notify nobody" receiver is fine, `notifications-none`
already is one. The failure was that it was **unstated**: nothing
distinguished "an estate that decided not to page anyone yet" from "an
estate that forgot to configure Slack", so the same render came from
either, and only one of those is a decision worth trusting. `mode:
evaluate-only` is a value with a name that says which one this install
is, the same fix the `enterprise-image-tag` and `licence-flag` refusals
use for a different silent failure: state the thing explicitly rather
than let a shape that resembles a mistake also be how the deliberate
choice looks.

The watchdog stays live in this mode on purpose: "evaluate, notify
nobody" is a real, named intent, and a consumer that chose it still
wants the outside world to know if the whole pipeline died, which is a
different question from whether any individual rule pages a human.

## Open questions for the maintainer

1. **Bot token vs. webhook-per-channel, as the DEFAULT credential kind
   for a new workspace.** This proposal recommends bot token, because
   it is the only shape that gives "several channels per workspace" a
   single credential — and, per the corrected fact 3 above, this needs
   no new moving part: the Alertmanager this chart already deploys by
   default (`v0.31.0`) supports bot tokens today, with no image-tag
   bump, no new value, and no version floor to add. The alternative is:
   `workspaces[]` entries hold a LIST of `{channel, webhookSecret}`
   pairs instead of one bot token, at the cost of one Secret per
   channel rather than one per workspace, for no upside now that the
   bot-token path costs nothing extra. **Recommendation: bot token,
   with more confidence than the first draft of this proposal had** —
   the whole point of the request was "several channels in each
   workspace" as one coherent unit, and N webhook Secrets per workspace
   re-creates today's one-credential-per-destination shape with extra
   values-file ceremony, not less.

2. **Does a subscription need its own message template (title/text),
   or does every subscription in a workspace share the one Slack
   template `vmalertmanager.yaml` already renders?** This proposal
   assumes the latter — one template, parameterised by cluster/
   namespace/runbook exactly as today — because per-subscription
   templates multiply the render's surface area for a feature nobody
   asked for yet. **Recommendation: one shared template for now;**
   revisit only if a real consumer asks for a different one per
   channel.

3. **Does `also` survive, or does every non-Slack webhook route through
   `subscriptions[]` too** (with `workspace` widened to also name a
   `notifications.webhook[]` entry)? Folding `also` in is more
   consistent — one list, one mental model — but a webhook receiver has
   no `channel` and no bot-token workspace concept, so `workspace` would
   have to mean two different things depending on which kind it names.
   **Recommendation: keep `also` separate** — it is a bridge to a
   different receiver kind, not a Slack subscription, and pretending
   otherwise buys uniformity the schema would have to fake.

4. **Should `minSeverity: all` ever include `info`?** Nothing today
   routes `info` anywhere, by design (docs/notifications.md). This
   proposal preserves that and defines `all` as "every tier the router
   knows," which today is `critical`+`warning`. Making `info` routable
   is a bigger decision than this proposal — it changes what `info` for
   a rule author means chart-wide — and does not have to be answered to
   ship subscriptions. **Recommendation: leave `info` unrouted; treat
   this as a separate future proposal if it comes up.**

5. **Cross-workspace catch-all: is "one subscription per workspace" an
   acceptable way to express "the firehose sees everything," or does
   the maintainer want a single top-level `catchAll:` that fans out to
   every workspace automatically?** The example above needs two
   `subscriptions[]` entries (one per workspace) to cover both
   workspaces' firehoses; a `catchAll` shorthand would need one. This
   proposal did not add one, on the theory that a values file rarely
   has more than two or three workspaces and the duplication is
   visible rather than hidden — but it is a small addition if the
   maintainer would rather have it. **Recommendation: skip it unless a
   real values file shows the duplication getting painful (four or more
   workspaces).**

6. **Does this proposal need `alertmanager.image.tag` at all?** The
   first draft of this doc added the value to enforce a version floor
   for bot-token support; that floor turned out to already be cleared
   by the chart's default (fact 3), so nothing here requires it.
   Separately, though: this chart has *no* way today for a consumer to
   pin or bump the Alertmanager image at all (no `alertmanager.image`
   key anywhere in the schema), which is a gap independent of Slack —
   a CVE in the Alertmanager binary, or a later feature such as
   v0.32.0's "allow receiver to edit existing messages", has no lever.
   **Recommendation: leave `alertmanager.image.tag` out of this
   proposal.** It solves a real but separate problem (image
   pinning/patching for a component this chart currently gives no
   control over at all), and bundling it into the notifications
   proposal would make an unrelated capability look like a prerequisite
   for subscriptions, when it is not one.

{{- define "observability-alerting.validate" -}}
{{- include "observability-alerting.validate.mode" . -}}
{{- include "observability-alerting.validate.selfAlerts" . -}}
{{- include "observability-alerting.validate.slackWorkspaces" . -}}
{{- include "observability-alerting.validate.evaluateOnly" . -}}
{{- include "observability-alerting.validate.watchdogSource" . -}}
{{- include "observability-alerting.validate.notifier" . -}}
{{- include "observability-alerting.validate.notifications" . -}}
{{- include "observability-alerting.validate.karma" . -}}
{{- include "observability-alerting.validate.remoteEvaluators" . -}}
{{- include "observability-alerting.validate.upstreamRules" . -}}
{{- end -}}

{{/*
Self-alerts: metric-name pairs that must arrive together, and a rule
enabled with a metric name but no usable threshold — shapes the schema
cannot express on its own, mirroring the exact refusal
`charts/platform-alerts` already gives its own `stores[].freeSpaceMetric`
pair.
*/}}
{{- define "observability-alerting.validate.selfAlerts" -}}
{{- $sa := .Values.selfAlerts -}}
{{- if and $sa.cardinality.hourlyCurrentSeriesMetric (not $sa.cardinality.hourlyMaxSeriesMetric) -}}
{{- fail "observability-alerting: `selfAlerts.cardinality.hourlyCurrentSeriesMetric` is set but `hourlyMaxSeriesMetric` is not. MetricStoreCardinalityNearLimit would compare a gauge against nothing; set both, or neither." -}}
{{- end -}}
{{- if and $sa.cardinality.hourlyMaxSeriesMetric (not $sa.cardinality.hourlyCurrentSeriesMetric) -}}
{{- fail "observability-alerting: `selfAlerts.cardinality.hourlyMaxSeriesMetric` is set but `hourlyCurrentSeriesMetric` is not. MetricStoreCardinalityNearLimit would compare a gauge against nothing; set both, or neither." -}}
{{- end -}}
{{- if and $sa.cardinality.dailyCurrentSeriesMetric (not $sa.cardinality.dailyMaxSeriesMetric) -}}
{{- fail "observability-alerting: `selfAlerts.cardinality.dailyCurrentSeriesMetric` is set but `dailyMaxSeriesMetric` is not. MetricStoreDailyCardinalityNearLimit would compare a gauge against nothing; set both, or neither." -}}
{{- end -}}
{{- if and $sa.cardinality.dailyMaxSeriesMetric (not $sa.cardinality.dailyCurrentSeriesMetric) -}}
{{- fail "observability-alerting: `selfAlerts.cardinality.dailyMaxSeriesMetric` is set but `dailyCurrentSeriesMetric` is not. MetricStoreDailyCardinalityNearLimit would compare a gauge against nothing; set both, or neither." -}}
{{- end -}}
{{- $diskGuardAlertPrefix := dict "metrics" "MetricStore" "logs" "LogStore" "traces" "TraceStore" -}}
{{- range $store := list "metrics" "logs" "traces" -}}
{{- $g := index $sa.diskGuard $store -}}
{{- if and $g.freeSpaceMetric (not $g.freeSpaceLimitMetric) -}}
{{- fail (printf "observability-alerting: `selfAlerts.diskGuard.%s.freeSpaceMetric` is set but `freeSpaceLimitMetric` is not. %sDiskNearGuard would compare a gauge against nothing; set both, or neither." $store (index $diskGuardAlertPrefix $store)) -}}
{{- end -}}
{{- if and $g.freeSpaceLimitMetric (not $g.freeSpaceMetric) -}}
{{- fail (printf "observability-alerting: `selfAlerts.diskGuard.%s.freeSpaceLimitMetric` is set but `freeSpaceMetric` is not. %sDiskNearGuard would compare a gauge against nothing; set both, or neither." $store (index $diskGuardAlertPrefix $store)) -}}
{{- end -}}
{{- end -}}
{{- if and $sa.gateway.queueSizeMetric (not $sa.gateway.queueCapacityMetric) -}}
{{- fail "observability-alerting: `selfAlerts.gateway.queueSizeMetric` is set but `queueCapacityMetric` is not. GatewayQueueFilling would compare a gauge against nothing; set both, or neither." -}}
{{- end -}}
{{- if and $sa.gateway.queueCapacityMetric (not $sa.gateway.queueSizeMetric) -}}
{{- fail "observability-alerting: `selfAlerts.gateway.queueCapacityMetric` is set but `queueSizeMetric` is not. GatewayQueueFilling would compare a gauge against nothing; set both, or neither." -}}
{{- end -}}
{{- if and $sa.sourceAbsent.enabled (not $sa.enabled) -}}
{{- fail "observability-alerting: `selfAlerts.sourceAbsent.enabled` is true but `selfAlerts.enabled` is not. SelfAlertSourceAbsent watches the metric names the self-alerts are configured with; with the self-alerts off there is nothing for it to watch." -}}
{{- end -}}
{{- /*
Every metric name here is optional, by design, since none could be
confirmed for certain against this chart's pins. But `selfAlerts.enabled`
with NOTHING configured renders a VMRule with an empty rule list — an
object that looks like coverage and is not, the exact shape this file
exists to refuse elsewhere. So at least one thing has to actually render:
one metric name, or one enabled backup whose *SnapshotOlderThanWindow can
watch it.
*/ -}}
{{- if $sa.enabled -}}
{{- $anyRule := or
    $sa.ingest.metric
    (and $sa.cardinality.hourlyCurrentSeriesMetric $sa.cardinality.hourlyMaxSeriesMetric)
    (and $sa.cardinality.dailyCurrentSeriesMetric $sa.cardinality.dailyMaxSeriesMetric)
    $sa.logStore.metric
    $sa.traceStore.metric
    (and $sa.gateway.queueSizeMetric $sa.gateway.queueCapacityMetric)
    $sa.gateway.exportFailedMetricPrefix
    $sa.gateway.enqueueFailedMetricPrefix
    (and $sa.diskGuard.metrics.freeSpaceMetric $sa.diskGuard.metrics.freeSpaceLimitMetric)
    (and $sa.diskGuard.logs.freeSpaceMetric $sa.diskGuard.logs.freeSpaceLimitMetric)
    (and $sa.diskGuard.traces.freeSpaceMetric $sa.diskGuard.traces.freeSpaceLimitMetric)
    (and .Values.backup.enabled .Values.backup.metrics.enabled)
    (and .Values.backup.enabled .Values.backup.logs.enabled)
    (and .Values.backup.enabled .Values.backup.traces.enabled)
    $sa.logStreamChurn.streamsCreatedMetric
    $sa.traceStreamChurn.streamsCreatedMetric
    $sa.writer.bufferMetric
    $sa.writer.droppedPacketsMetric
    $sa.proxyConcurrency.limitedRequestsMetric
    (and ((.Values.notifications | default dict).slack | default dict).workspaces $sa.slackDelivery.enabled)
-}}
{{- if not $anyRule -}}
{{- fail "observability-alerting: `selfAlerts.enabled` is true but no rule would actually render — no metric name is set anywhere under `selfAlerts`, and no `backup.<store>.enabled` is true either. A VMRule with an empty rule list looks like coverage and is not. Confirm at least one metric name against your own component's /metrics and set it here, or leave `selfAlerts.enabled: false` until you have." -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
A Watchdog from somewhere, always.

Two sources, and the chart picks whichever one is not already covering
it (see `observability-alerting.vendoredWatchdogPresent` in _helpers.tpl):
`victoria-metrics-k8s-stack`'s own vendored default rule set — ON here
by default, and the reason it stays on, because it is also where
several rules with no `charts/platform-alerts` equivalent come from —
or, when that is turned off, this chart's own `templates/watchdog.yaml`.
Turning BOTH off at once is the one combination that leaves the status
box's deadman (docs/statusbox.md, "internal → status, pulled") with
nothing to read: not a rule this chart carries, and not one the
vendored set carries either.
*/}}
{{- define "observability-alerting.validate.watchdogSource" -}}
{{- $observabilityStackEffective := include "observability-alerting.effectiveEnabled" . | fromYaml -}}
{{- $vmks := index .Values "victoria-metrics-k8s-stack" -}}
{{- if and $vmks.enabled $observabilityStackEffective.vmalert -}}
{{- if and (not (include "observability-alerting.vendoredWatchdogPresent" .)) (not .Values.vmalert.watchdog.enabled) -}}
{{- fail "observability-alerting: victoria-metrics-k8s-stack's vendored default rule set is off (`defaultRules.enabled: false`, or its `general.rules` group specifically disabled) AND `vmalert.watchdog.enabled` is false. Between them, this install renders no Watchdog alert at all — not the vendored one, not this chart's own — and the status box's deadman (docs/statusbox.md) depends on one existing to read. Turn one of the two back on." -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Tenancy.

The names in a grant are interpolated into a filter expression, so this
is the same security boundary `pkg/tenancy` enforces in Go and for the
same reason: a namespace named `a|b` or `.*` would not look odd in a
rendered filter, it would widen the grant it appears in. Such a name is
refused rather than escaped.

The paths are checked too. vmauth has no deny primitive that the operator
exposes, so a route that must not exist is a route that must not be
written — and the operator's default for a targetRef without `paths` is
`/.*`, which includes `/internal/*`, where the store's own authKey flags
would override its `-httpAuth.*`.
*/}}
{{- define "observability-alerting.validate.notifier" -}}
{{- $observabilityStackEffective := include "observability-alerting.effectiveEnabled" . | fromYaml -}}
{{- if $observabilityStackEffective.vmalert -}}
{{- $mode := ((.Values.notifications | default dict).mode) | default "route" -}}
{{- if and (eq $mode "route") (not $observabilityStackEffective.alertmanager) (not .Values.alertmanager.notifierUrl) -}}
{{- fail "observability-alerting: vmalert is enabled, Alertmanager is not, and `alertmanager.notifierUrl` is empty. vmalert would evaluate every rule and send the result nowhere — which looks exactly like an estate with no problems, for as long as nobody checks. Enable Alertmanager, name the one the estate already runs, or set `notifications.mode: evaluate-only` for the explicit \"evaluate every rule, notify nobody yet\" shape." -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Slack workspaces: one Slack app (one bot token) per workspace.

Before this, `notifications.slack.webhookSecret` was ONE incoming webhook
shared by every channel, and `channel` was chosen per route. A webhook
ignores the `channel` a message asks for and posts to the one channel it
was created for, so a second channel looked routed and was not. The
token of a Slack app honours `channel`, so the webhook is gone and this
block replaces it. Everything below refuses a shape that looks wired up
and delivers nowhere.

Runs before `evaluateOnly` so a leftover `webhookSecret` is refused with
its migration even when the rest of the file is otherwise (validly or
not) evaluate-only.
*/ -}}
{{- define "observability-alerting.validate.slackWorkspaces" -}}
{{- $n := .Values.notifications | default dict -}}
{{- $slack := $n.slack | default dict -}}
{{- if hasKey $slack "webhookSecret" -}}
{{- fail "observability-alerting: `notifications.slack.webhookSecret` was removed. One incoming webhook ignores the `channel` a message asks for and always posts to the one channel it was created for, so routing to several channels through it could not work. Replace it with one entry per Slack workspace under `notifications.slack.workspaces`: `[{name: <short-name>, appTokenSecret: {name: <Secret>, key: <key>}}]`, holding that workspace's Slack app bot token (xoxb-...). Then each severity, route and `catchAll` destination may name a `workspace` (optional when exactly one is declared). See docs/notifications.md, \"Slack\"." -}}
{{- end -}}
{{- $workspaces := $slack.workspaces | default list -}}
{{- $names := dict -}}
{{- range $i, $w := $workspaces -}}
{{- if not $w.name -}}
{{- fail (printf "observability-alerting: notifications.slack.workspaces[%d] has an empty `name`. The name is how a severity, route or catch-all picks this workspace, and it names the mounted Secret volume." $i) -}}
{{- end -}}
{{- if hasKey $names $w.name -}}
{{- fail (printf "observability-alerting: notifications.slack.workspaces has two entries named %q. A `workspace` that could mean either is a destination nobody can read, and the two would mount one volume name twice." (toString $w.name)) -}}
{{- end -}}
{{- $_ := set $names $w.name true -}}
{{- if not (($w.appTokenSecret).name) -}}
{{- fail (printf "observability-alerting: notifications.slack.workspaces[%d] (%s) has an empty `appTokenSecret.name`. A workspace whose bot token Secret is unnamed cannot send: the route, the receiver and the schema would all agree it exists, and it would deliver nothing." $i (toString $w.name)) -}}
{{- end -}}
{{- if not (($w.appTokenSecret).key) -}}
{{- fail (printf "observability-alerting: notifications.slack.workspaces[%d] (%s) has an empty `appTokenSecret.key`. The key is the file under the mounted Secret that Alertmanager reads the bot token from; without it there is no file to read." $i (toString $w.name)) -}}
{{- end -}}
{{- end -}}
{{- with $slack.failureReceiver -}}
{{- if not $workspaces -}}
{{- fail "observability-alerting: `notifications.slack.failureReceiver` is set but `notifications.slack.workspaces` is empty. It names where the \"Slack is not delivering\" alert goes, and with no Slack workspace there is no such alert to route." -}}
{{- end -}}
{{- if eq . "slack" -}}
{{- fail "observability-alerting: `notifications.slack.failureReceiver` is \"slack\". The alert says Slack is failing to deliver; routed to Slack it would fail to deliver itself. Name a webhook from `notifications.webhook`, or `telegram`." -}}
{{- end -}}
{{- $isTelegram := and (eq . "telegram") ((($n.telegram | default dict).botTokenSecret).name) -}}
{{- $isWebhook := false -}}
{{- range $w := ($n.webhook | default list) -}}{{- if eq $w.name $slack.failureReceiver -}}{{- $isWebhook = true -}}{{- end -}}{{- end -}}
{{- if not (or $isTelegram $isWebhook) -}}
{{- fail (printf "observability-alerting: notifications.slack.failureReceiver is %q, which is neither the name of an entry in notifications.webhook nor \"telegram\" (with notifications.telegram configured). An alert routed to a receiver that is not configured looks routed and reaches nobody." (toString .)) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
`notifications.mode: evaluate-only` — the explicit, named opt-out for a
consumer with no Slack or webhook credential YET, docs/notifications.md,
"Evaluate, notify nobody yet". `route` (the default) changes nothing
here; every check below applies only once the mode is evaluate-only, and
each one refuses a combination that would otherwise render a receiver,
or an Alertmanager, that the mode makes pointless: vmalert never notifies
anybody while it is set, so anything configured to be notified is
configured to be unreachable.

This runs BEFORE `validate.notifications`, so a fixture testing one of
these refusals is never preempted by a later check that also happens to
trip over an unconfigured severity or receiver.
*/ -}}
{{- define "observability-alerting.validate.evaluateOnly" -}}
{{- $observabilityStackEffective := include "observability-alerting.effectiveEnabled" . | fromYaml -}}
{{- $n := .Values.notifications | default dict -}}
{{- if eq ($n.mode | default "route") "evaluate-only" -}}
{{- if $observabilityStackEffective.alertmanager -}}
{{- fail "observability-alerting: `notifications.mode` is `evaluate-only` and `alertmanager.enabled` is true (or left at its default). Evaluate-only means vmalert evaluates every rule and sends the result to nobody — on purpose, visible in vmalert's own UI and API, not silently — so there is nothing for Alertmanager to route and this chart does not render it in this mode. Set `alertmanager.enabled: false`, or drop `notifications.mode` back to `route` and configure a receiver." -}}
{{- end -}}
{{- if .Values.alertmanager.notifierUrl -}}
{{- fail "observability-alerting: `notifications.mode` is `evaluate-only` and `alertmanager.notifierUrl` is set. Evaluate-only renders vmalert's `-notifier.blackhole`, which vmalert itself refuses to combine with any notifier URL: `-notifier.url`, `-notifier.config` and `-notifier.blackhole` are mutually exclusive. Unset `alertmanager.notifierUrl`, or drop `notifications.mode` back to `route` and point vmalert at the Alertmanager it names." -}}
{{- end -}}
{{- $slack := $n.slack | default dict -}}
{{- $receiverConfigured := or $slack.workspaces (($n.telegram | default dict).botTokenSecret).name (gt (len ($n.webhook | default list)) 0) (gt (len ($n.severities | default dict)) 0) (gt (len ($n.routes | default list)) 0) (gt (len ($n.also | default list)) 0) $n.catchAll (gt (len ($n.drop | default list)) 0) -}}
{{- if $receiverConfigured -}}
{{- fail "observability-alerting: `notifications.mode` is `evaluate-only` and `notifications` also configures a receiver, a severity, a route or an `also` bridge (or a `catchAll` or `drop`). Evaluate-only means nobody is notified yet: a receiver configured beside it looks wired up and is never reached, because vmalert never sends the notification it would carry. Remove the receiver configuration, or drop `notifications.mode` back to `route`." -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Notifications: the one router, refused into existence rather than left a
free-form passthrough. See docs/notifications.md and docs/safety.md.
*/}}
{{- define "observability-alerting.validate.notifications" -}}
{{- $observabilityStackEffective := include "observability-alerting.effectiveEnabled" . | fromYaml -}}
{{- $n := .Values.notifications | default dict -}}
{{- $mode := ($n.mode) | default "route" -}}
{{- $slack := $n.slack | default dict -}}
{{- $telegram := $n.telegram | default dict -}}
{{- $telegramConfigured := ($telegram.botTokenSecret).name -}}
{{- $webhooks := $n.webhook | default list -}}
{{- $severities := $n.severities | default dict -}}
{{- $routes := $n.routes | default list -}}
{{- $also := $n.also | default list -}}
{{- $webhookNames := dict -}}
{{- $slackWorkspaceNames := dict -}}
{{- range $w := ($slack.workspaces | default list) -}}{{- $_ := set $slackWorkspaceNames $w.name true -}}{{- end -}}
{{- range $w := $webhooks -}}{{- $_ := set $webhookNames $w.name true -}}{{- end -}}
{{- $configured := or $slack.workspaces $telegramConfigured (gt (len $webhooks) 0) -}}
{{- if and $observabilityStackEffective.alertmanager (not $configured) (ne $mode "evaluate-only") -}}
{{- fail "observability-alerting: `alertmanager.enabled` is true and `notifications` configures no receiver kind — no `notifications.slack.workspaces`, no `notifications.telegram.botTokenSecret` and no `notifications.webhook` entries. Alertmanager then routes to the `blackhole` shape this chart exists to retire: vmalert evaluates every rule and the result reaches nobody, and nothing about the install looks unhealthy. Configure at least one receiver kind under `notifications`, set `alertmanager.enabled: false` and point `alertmanager.notifierUrl` at one the estate already runs, or set `notifications.mode: evaluate-only` for the explicit \"evaluate every rule, notify nobody yet\" shape if there is no channel yet." -}}
{{- end -}}
{{- /*
`notifications.externalUrl` and `vmalert.externalUrl` are one fact — the
base URL a link leaving the cluster should point at — kept as two
values only because `vmalert.externalUrl` has to keep working on its
own for an install with `alertmanager.enabled: false`, which has no
`notifications` block at all. Two inputs for one fact is refused rather
than left to disagree quietly: see docs/doctrine.md, "One input, two
shapes".
*/ -}}
{{- if and $n.externalUrl .Values.vmalert.externalUrl (ne $n.externalUrl .Values.vmalert.externalUrl) -}}
{{- fail (printf "observability-alerting: notifications.externalUrl is %q and vmalert.externalUrl is %q. They are the same fact — the base URL a link leaving the cluster should point at — so a difference between them is a difference nobody notices until an alert fires and one link works while the other does not. Set them to the same value, or leave notifications.externalUrl unset and let it default to vmalert.externalUrl." (toString $n.externalUrl) (toString .Values.vmalert.externalUrl)) -}}
{{- end -}}
{{- $effectiveExternalUrl := $n.externalUrl | default .Values.vmalert.externalUrl -}}
{{- if and $configured (not $effectiveExternalUrl) -}}
{{- fail "observability-alerting: `notifications` configures a receiver but neither `notifications.externalUrl` nor `vmalert.externalUrl` is set. One of them is the base of the Grafana link this chart puts in every Slack message; without it, every link a message carries points at nothing a person can open." -}}
{{- end -}}
{{- /*
A receiver kind with no default route for a severity is the blackhole
again, one layer down: the wrapping route's own receiver is a no-op, so
a severity `severities` does not cover reaches it silently.
*/ -}}
{{- /*
With `catchAll` set, a tier `severities` leaves out lands there instead,
so it is no longer the blackhole this refuses.
*/ -}}
{{- if and $configured (not $n.catchAll) -}}
{{- range $tier := list "critical" "warning" -}}
{{- if not (hasKey $severities $tier) -}}
{{- fail (printf "observability-alerting: `notifications` configures a receiver but `notifications.severities.%s` is not set. Every route this chart renders falls back to a no-op receiver when none of `severities`, `routes` or `also` match, so a %s alert with no default reaches nobody and looks routed." $tier $tier) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- /*
`telegram` is a keyword only once `notifications.telegram` is configured.
Before 0.10.0 it was an ordinary webhook name, and an install that
bridges to Telegram through a webhook it NAMED `telegram` must render
exactly as it did; the two together are ambiguous and refused.
*/ -}}
{{- if and $telegramConfigured (hasKey $webhookNames "telegram") -}}
{{- fail "observability-alerting: `notifications.telegram` is configured and `notifications.webhook` also has an entry named \"telegram\". A severity with `receiver: telegram` could then mean either one, and a route whose meaning depends on which the chart picked is a route nobody can read. Rename the webhook entry." -}}
{{- end -}}
{{- /*
A severity naming a receiver that does not exist. `slack` and `telegram`
are the literal keywords; anything else must be a name from
`notifications.webhook`.
*/ -}}
{{- /*
Every severity tier, and `catchAll`, which has the same shape and the
same ways to name a receiver that is not there.
*/ -}}
{{- $targets := list -}}
{{- range $tier, $cfg := $severities -}}
{{- $targets = append $targets (dict "where" (printf "notifications.severities.%s" $tier) "cfg" $cfg) -}}
{{- end -}}
{{- with $n.catchAll -}}
{{- $targets = append $targets (dict "where" "notifications.catchAll" "cfg" .) -}}
{{- end -}}
{{- range $t := $targets -}}
{{- $cfg := $t.cfg -}}
{{- $isTelegram := and (eq $cfg.receiver "telegram") $telegramConfigured -}}
{{- if and $cfg.receiver (ne $cfg.receiver "slack") (not $isTelegram) (not (hasKey $webhookNames $cfg.receiver)) -}}
{{- fail (printf "observability-alerting: %s.receiver is %q, which is neither \"slack\", \"telegram\" (with notifications.telegram configured) nor the name of an entry in notifications.webhook. A route to a receiver that is not configured looks like a route and reaches nobody." $t.where (toString $cfg.receiver)) -}}
{{- end -}}
{{- if and (not $isTelegram) (or (hasKey $cfg "chatId") (hasKey $cfg "messageThreadId")) -}}
{{- fail (printf "observability-alerting: %s sets `chatId` or `messageThreadId` but its receiver is %q, not a configured `telegram`. Those keys pick a Telegram chat; on any other receiver they would be read by nothing." $t.where (toString $cfg.receiver)) -}}
{{- end -}}
{{- if and $isTelegram $cfg.channel -}}
{{- fail (printf "observability-alerting: %s.receiver is \"telegram\" and it also sets `channel`. `channel` is a Slack channel; a Telegram tier lands in `notifications.telegram.chatId`, or in this tier's own `chatId`/`messageThreadId`. Remove `channel`." $t.where) -}}
{{- end -}}
{{- if and $cfg.workspace (ne $cfg.receiver "slack") -}}
{{- fail (printf "observability-alerting: %s sets `workspace` but its receiver is %q, not `slack`. A workspace picks the Slack app whose bot token posts; on any other receiver it would be read by nothing." $t.where (toString $cfg.receiver)) -}}
{{- end -}}
{{- if and $cfg.mention (ne $cfg.receiver "slack") -}}
{{- fail (printf "observability-alerting: %s sets `mention` but its receiver is %q, not `slack`. A mention is Slack's `<!here>`/`<!channel>` in the message text; on any other receiver it would be read by nothing." $t.where (toString $cfg.receiver)) -}}
{{- end -}}
{{- if eq $cfg.receiver "slack" -}}
{{- include "observability-alerting.validate.slackDestination" (list $t.where $cfg.channel $cfg.workspace $slackWorkspaceNames) -}}
{{- end -}}
{{- end -}}
{{- /*
A route matching outside the vocabulary the collectors actually stamp.
Anything else matches nothing a rule carries and pages nobody while
looking exactly like a route that works.
*/ -}}
{{- range $i, $r := $routes -}}
{{- range $tier := list "critical" "warning" -}}
{{- $tcfg := index $severities $tier | default dict -}}
{{- if and (index $r $tier) (eq ($tcfg.receiver | default "") "telegram") $telegramConfigured -}}
{{- fail (printf "observability-alerting: notifications.routes[%d].%s overrides a channel, but notifications.severities.%s.receiver is \"telegram\". A route's per-tier value is a Slack channel name; a Telegram tier has no channel to override, so it would be read by nothing. Remove it, or route that tier to Slack." $i $tier $tier) -}}
{{- end -}}
{{- end -}}
{{- range $tier := list "critical" "warning" -}}
{{- $tcfg := index $severities $tier | default dict -}}
{{- $override := index $r $tier -}}
{{- if and $override (eq ($tcfg.receiver | default "") "slack") -}}
{{- $where := printf "notifications.routes[%d].%s" $i $tier -}}
{{- if kindIs "map" $override -}}
{{- include "observability-alerting.validate.slackDestination" (list $where ($override.channel | default $tcfg.channel) ($override.workspace | default $tcfg.workspace) $slackWorkspaceNames) -}}
{{- else -}}
{{- include "observability-alerting.validate.slackDestination" (list $where $override $tcfg.workspace $slackWorkspaceNames) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- range $k, $_ := ($r.match | default dict) -}}
{{- $routeLabels := $n.routeLabels | default (list "k8s_cluster_name" "k8s_namespace_name") -}}
{{- if not (has $k (concat $routeLabels (without (list (toString ($n.ownerLabel | default ""))) ""))) -}}
{{- fail (printf "observability-alerting: notifications.routes[%d].match has key %q. A route may match only on a label in `notifications.routeLabels` (default: k8s_cluster_name and k8s_namespace_name, the two dimensions the collectors stamp on every alert; now: %s), plus `notifications.ownerLabel` when set. A route on anything else (tenant, env, team, …) matches nothing any rule actually carries. An alert born outside a cluster (alert-ingress) is routed on the label its mapping sets, usually `source`: add it to `notifications.routeLabels`. A route on the owning company needs `notifications.ownerLabel` set to the label the emitters stamp (`tenancy.owners` in observability-emitters)." $i (toString $k) (join ", " $routeLabels)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- /*
`also` names a webhook that is not configured. This bridge exists to
reach a webhook beside the normal route; a name that resolves to nothing
is a route that reaches nobody, same as the severities check above.
*/ -}}
{{- range $i, $a := $also -}}
{{- $where := printf "notifications.also[%d]" $i -}}
{{- if eq (toString $a.receiver) "slack" -}}
{{- if hasKey $webhookNames "slack" -}}
{{- fail (printf "observability-alerting: %s.receiver is \"slack\" and notifications.webhook also has an entry named \"slack\". The entry could mean either, and a route whose meaning depends on which the chart picked is a route nobody can read. Rename the webhook entry." $where) -}}
{{- end -}}
{{- include "observability-alerting.validate.slackDestination" (list $where $a.channel $a.workspace $slackWorkspaceNames) -}}
{{- else -}}
{{- if and $telegramConfigured (eq (toString $a.receiver) "telegram") -}}
{{- fail (printf "observability-alerting: %s.receiver is \"telegram\". `also` delivers to a webhook or to Slack, not to Telegram; a Telegram destination is a severity tier's, a catchAll's, or the Slack failure receiver." $where) -}}
{{- end -}}
{{- if not (hasKey $webhookNames $a.receiver) -}}
{{- fail (printf "observability-alerting: notifications.also[%d].receiver is %q, which is not the name of any notifications.webhook entry (and is not \"slack\")." $i (toString $a.receiver)) -}}
{{- end -}}
{{- range $k := list "channel" "workspace" "mention" -}}
{{- if index $a $k -}}
{{- fail (printf "observability-alerting: %s sets `%s` but its receiver is %q, not `slack`. It picks a Slack destination; on a webhook it would be read by nothing." $where $k (toString $a.receiver)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- /*
`notifications.alertmanagerUrl` is embedded in an Alertmanager template,
so it must be an absolute http(s) URL with no trailing slash and none of
the characters that would end the template string or open an action.
*/ -}}
{{- with $n.alertmanagerUrl -}}
{{- if or (not (regexMatch "^https?://[^\\s/\"'`{}\\\\<>]" (toString .))) (regexMatch "[\\s\"'`{}\\\\<>]" (toString .)) (hasSuffix "/" (toString .)) -}}
{{- fail (printf "observability-alerting: notifications.alertmanagerUrl is %q. It must be an absolute http:// or https:// URL with a host, without a trailing slash and without whitespace, quotes, braces or backslashes: the chart appends `/#/silences/new?...` to it and embeds it in an Alertmanager message template." (toString .)) -}}
{{- end -}}
{{- end -}}
{{- /*
The deadman's own repeat interval against the far end's timeout, when the
estate has stated one: the heartbeat has to land comfortably inside it, or
a single delayed delivery reads as the estate being down.
*/ -}}
{{- $watchdog := .Values.alertmanager.watchdog -}}
{{- /*
`alertmanager.cluster.reconnectTimeout` becomes `--cluster.reconnect-timeout`,
a Go duration; anything else makes Alertmanager exit at start.
*/ -}}
{{- $reconnect := toString (($.Values.alertmanager.cluster | default dict).reconnectTimeout | default "") -}}
{{- if not (regexMatch "^([0-9]+(\\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$" $reconnect) -}}
{{- fail (printf "observability-alerting: alertmanager.cluster.reconnectTimeout is %q. It must be a Go duration such as 5m, 90s or 1h30m: it is passed as Alertmanager's --cluster.reconnect-timeout, which refuses anything else and would crash-loop every replica." $reconnect) -}}
{{- end -}}
{{- if and $watchdog.tokenKey (not $watchdog.secretName) -}}
{{- fail "observability-alerting: alertmanager.watchdog.tokenKey is set but alertmanager.watchdog.secretName is empty. The token qualifies the deadman receiver, which only exists with a Secret to read its URL from; set secretName, or remove tokenKey." -}}
{{- end -}}
{{- if and $watchdog.secretName $watchdog.timeout -}}
{{- $repeatS := include "observability-alerting.durationSeconds" $watchdog.repeatInterval | int64 -}}
{{- $timeoutS := include "observability-alerting.durationSeconds" $watchdog.timeout | int64 -}}
{{- if ge $repeatS $timeoutS -}}
{{- fail (printf "observability-alerting: alertmanager.watchdog.repeatInterval is %q and alertmanager.watchdog.timeout is %q. The heartbeat must land comfortably INSIDE the far end's own timeout, or a single delayed delivery reads as the estate being down when it is not. repeatInterval must be strictly less than timeout." (toString $watchdog.repeatInterval) (toString $watchdog.timeout)) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
One Slack destination — a severity tier, the catch-all, or a route's
per-tier override — as `(where channel workspace workspaceNames)`.
*/ -}}
{{- define "observability-alerting.validate.slackDestination" -}}
{{- $where := index . 0 -}}
{{- $channel := index . 1 -}}
{{- $workspace := index . 2 -}}
{{- $names := index . 3 -}}
{{- if not $names -}}
{{- fail (printf "observability-alerting: %s sends to Slack but notifications.slack.workspaces is empty. A route to a receiver kind that is not configured looks like a route and reaches nobody." $where) -}}
{{- end -}}
{{- if not $channel -}}
{{- fail (printf "observability-alerting: %s sends to Slack with an empty `channel`. A token posts to the channel a message names; with none named Slack refuses every message, and the route looks wired up and delivers nothing." $where) -}}
{{- end -}}
{{- if and (not $workspace) (gt (len $names) 1) -}}
{{- fail (printf "observability-alerting: %s sends to Slack without a `workspace`, and notifications.slack.workspaces declares %d. With exactly one workspace the name may be left out; with two or more it is required, because the same channel name is a different place in each." $where (len $names)) -}}
{{- end -}}
{{- if and $workspace (not (hasKey $names $workspace)) -}}
{{- fail (printf "observability-alerting: %s names workspace %q, which no entry in notifications.slack.workspaces declares. A destination in a workspace with no bot token looks routed and reaches nobody." $where (toString $workspace)) -}}
{{- end -}}
{{- end -}}

{{/*
karma, the alert console, and the notification links that point at it.

The point of karma here is the AUTHOR of a silence: Alertmanager takes
`createdBy` as free text, and karma with header authentication rewrites it
to the signed-in user and applies silence ACLs. Every refusal below is a
way for that to quietly not be true.

- no `authentication.header.name` and no `authentication.none`: a console
  that silences pages would run anonymously by accident;
- a header name with no `valueRe`, or a groups header with no
  `groupValueRe`: karma refuses to start (its own rule), and the Pod would
  crash-loop instead of this render failing;
- ACL rules and groups karma cannot honour: a rule naming a group that is
  not declared protects nothing, and groups with no header authentication
  never match a user;
- `notifications.console: karma` with no `consoleUrl`, or with karma off:
  a message whose silence link points at nothing.
*/}}
{{- define "observability-alerting.validate.karma" -}}
{{- $n := .Values.notifications | default dict -}}
{{- $console := $n.console | default "alertmanager" -}}
{{- if not (has $console (list "alertmanager" "karma")) -}}
{{- fail (printf "observability-alerting: notifications.console is %q, which is neither \"alertmanager\" nor \"karma\"." (toString $console)) -}}
{{- end -}}
{{- /*
`consoleUrl` is embedded in an Alertmanager template exactly like
`alertmanagerUrl` is, so it is held to the same shape.
*/ -}}
{{- with $n.consoleUrl -}}
{{- if or (not (regexMatch "^https?://[^\\s/\"'`{}\\\\<>]" (toString .))) (regexMatch "[\\s\"'`{}\\\\<>]" (toString .)) (hasSuffix "/" (toString .)) -}}
{{- fail (printf "observability-alerting: notifications.consoleUrl is %q. It must be an absolute http:// or https:// URL with a host, without a trailing slash and without whitespace, quotes, braces or backslashes: the chart appends `/?m=...` to it and embeds it in an Alertmanager message template." (toString .)) -}}
{{- end -}}
{{- end -}}
{{- if eq $console "karma" -}}
{{- if not $n.consoleUrl -}}
{{- fail "observability-alerting: notifications.console is \"karma\" but notifications.consoleUrl is empty. The Silence and View links are built on karma's external base URL (for example https://karma.example.com), and the chart cannot guess it." -}}
{{- end -}}
{{- if not .Values.karma.enabled -}}
{{- fail "observability-alerting: notifications.console is \"karma\" but karma.enabled is false. The message would link to a console this release does not run. Set `karma.enabled: true`, or leave `notifications.console` at \"alertmanager\"." -}}
{{- end -}}
{{- end -}}
{{- if .Values.karma.enabled -}}
{{- $k := .Values.karma -}}
{{- $eff := include "observability-alerting.effectiveEnabled" . | fromYaml -}}
{{- $h := $k.authentication.header -}}
{{- if and $k.authentication.none $h.name -}}
{{- fail "observability-alerting: karma.authentication.none is true and karma.authentication.header.name is set. They contradict: one says karma runs anonymously, the other that it trusts a header. Keep one." -}}
{{- end -}}
{{- if and (not $k.authentication.none) (not $h.name) -}}
{{- fail "observability-alerting: karma.enabled is true with no karma.authentication.header.name. A console that silences pages must not run anonymously by accident: without authentication karma creates every silence under whatever name the browser sends, which is the free-text `createdBy` problem it is here to solve. Set `karma.authentication.header.name` to the header your SSO gateway sets (for example X-Auth-Request-Email), or acknowledge an anonymous console with `karma.authentication.none: true`." -}}
{{- end -}}
{{- if $h.name -}}
{{- if not (regexMatch "^[A-Za-z0-9-]+$" (toString $h.name)) -}}
{{- fail (printf "observability-alerting: karma.authentication.header.name is %q. It must be an HTTP header name: letters, digits and dashes." (toString $h.name)) -}}
{{- end -}}
{{- if not $h.valueRe -}}
{{- fail "observability-alerting: karma.authentication.header.name is set but karma.authentication.header.valueRe is empty. karma requires `value_re` whenever a header name is set, and refuses to start without it. The default is ^(.+)$." -}}
{{- end -}}
{{- end -}}
{{- if and $h.groupName (not $h.groupValueRe) -}}
{{- fail "observability-alerting: karma.authentication.header.groupName is set but karma.authentication.header.groupValueRe is empty. karma requires `group_value_re` whenever a groups header name is set, and refuses to start without it." -}}
{{- end -}}
{{- if and (or $h.groupValueRe $h.groupValueSeparator) (not $h.groupName) -}}
{{- fail "observability-alerting: karma.authentication.header.groupValueRe or groupValueSeparator is set without groupName. karma would read no groups header, and the value would do nothing." -}}
{{- end -}}
{{- if and $k.authorization.groups (not $h.name) -}}
{{- fail "observability-alerting: karma.authorization.groups is set without karma.authentication.header.name. Groups map the user names the authentication layer passes, and with no header authentication there are none: no ACL scoped to a group would ever match." -}}
{{- end -}}
{{- $groupNames := dict -}}
{{- range $g := $k.authorization.groups -}}
{{- if or (not $g.name) (not $g.members) -}}
{{- fail "observability-alerting: every karma.authorization.groups entry needs a `name` and a non-empty `members` list." -}}
{{- end -}}
{{- if hasKey $groupNames $g.name -}}
{{- fail (printf "observability-alerting: karma.authorization.groups declares %q twice." (toString $g.name)) -}}
{{- end -}}
{{- $_ := set $groupNames $g.name true -}}
{{- end -}}
{{- range $i, $r := $k.acl.silences -}}
{{- if not (has (toString $r.action) (list "allow" "block" "requireMatcher")) -}}
{{- fail (printf "observability-alerting: karma.acl.silences[%d].action is %q. karma knows allow, block and requireMatcher." $i (toString $r.action)) -}}
{{- end -}}
{{- range $g := (($r.scope).groups | default list) -}}
{{- if not (hasKey $groupNames $g) -}}
{{- fail (printf "observability-alerting: karma.acl.silences[%d].scope.groups names %q, which karma.authorization.groups does not declare. A rule scoped to an unknown group applies to nobody." $i (toString $g)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $names := dict -}}
{{- if $eff.alertmanager -}}
{{- $_ := set $names "alertmanager" true -}}
{{- /* An Alertmanager pair names its replicas alertmanager-0, -1, ... */ -}}
{{- if gt (int $.Values.alertmanager.replicaCount) 1 -}}
{{- range $r := until (int $.Values.alertmanager.replicaCount) -}}
{{- $_ := set $names (printf "alertmanager-%d" $r) true -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- range $i, $s := ($k.alertmanagers | default list) -}}
{{- if or (not $s.name) (not $s.uri) -}}
{{- fail (printf "observability-alerting: karma.alertmanagers[%d] needs a `name` and a `uri`." $i) -}}
{{- end -}}
{{- if hasKey $names $s.name -}}
{{- fail (printf "observability-alerting: karma.alertmanagers[%d].name is %q, which is already taken. This release's own Alertmanager is named \"alertmanager\" and every name must be unique." $i (toString $s.name)) -}}
{{- end -}}
{{- $_ := set $names $s.name true -}}
{{- end -}}
{{- if not $names -}}
{{- fail "observability-alerting: karma.enabled is true but karma has no Alertmanager to read: alertmanager.enabled is false and karma.alertmanagers is empty. Name the Alertmanager the estate runs in karma.alertmanagers." -}}
{{- end -}}
{{- if and $k.history.enabled (not $k.history.uri) -}}
{{- fail "observability-alerting: karma.history.enabled is true but karma.history.uri is empty. karma needs the Prometheus-compatible endpoint that holds the ALERTS series." -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
`vmalert.remoteEvaluators`: one extra VMAlert per entry, evaluating the
VMRules labelled `observability.truvity.io/evaluator: <name>` against
another store and notifying through this install's Alertmanager. Every
check refuses a shape that renders and then evaluates nothing, or notifies
nobody, or evaluates the same rule twice. docs/notifications.md,
"Evaluating another store's rules".
*/}}
{{- define "observability-alerting.validate.remoteEvaluators" -}}
{{- $evs := .Values.vmalert.remoteEvaluators | default list -}}
{{- if $evs -}}
{{- $eff := include "observability-alerting.effectiveEnabled" . | fromYaml -}}
{{- $fullname := include "observability-alerting.fullname" . -}}
{{- $mode := ((.Values.notifications | default dict).mode) | default "route" -}}
{{- if not $eff.vmalert -}}
{{- fail "observability-alerting: `vmalert.remoteEvaluators` is set but this install renders no vmalert (`mode` is not \"full\", or `vmalert.enabled` is false). A remote evaluator notifies through this install's Alertmanager; `mode: replica` and `mode: operator-only` have none. Set it on the full install, or empty the list." -}}
{{- end -}}
{{- if eq $mode "evaluate-only" -}}
{{- fail "observability-alerting: `vmalert.remoteEvaluators` is set and `notifications.mode` is `evaluate-only`. Evaluate-only sends nothing to anybody, which is the very thing a remote evaluator exists to avoid: its alerts would be evaluated and notify nobody. Drop `notifications.mode` back to `route`, or empty the list." -}}
{{- end -}}
{{- $names := dict -}}
{{- range $i, $e := $evs -}}
{{- if not $e.name -}}
{{- fail (printf "observability-alerting: vmalert.remoteEvaluators[%d] has an empty `name`. The name is the value of the `observability.truvity.io/evaluator` label its rules carry and names its VMAlert." $i) -}}
{{- end -}}
{{- if not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$" (toString $e.name)) -}}
{{- fail (printf "observability-alerting: vmalert.remoteEvaluators[%d] name %q is not a DNS label (lower-case alphanumerics and hyphens, starting and ending with an alphanumeric)." $i (toString $e.name)) -}}
{{- end -}}
{{- if has (toString $e.name) (list "metrics" "logs") -}}
{{- fail (printf "observability-alerting: vmalert.remoteEvaluators[%d] is named %q, which is the value of `observability.truvity.io/evaluator` that the local alerters own (`metrics` is the local metrics alerter, `logs` the logs alerter). A rule carrying it would be evaluated locally, not by this evaluator. Pick another name." $i (toString $e.name)) -}}
{{- end -}}
{{- if gt (len (printf "%s-remote-%s" $fullname $e.name)) 52 -}}
{{- fail (printf "observability-alerting: vmalert.remoteEvaluators[%d] name %q makes the VMAlert name %q longer than 52 characters, which the pod and Service names derived from it cannot carry. Shorten it." $i (toString $e.name) (printf "%s-remote-%s" $fullname $e.name)) -}}
{{- end -}}
{{- if hasKey $names $e.name -}}
{{- fail (printf "observability-alerting: vmalert.remoteEvaluators has two entries named %q. Two evaluators for one name would both evaluate the same rules and name one VMAlert twice." (toString $e.name)) -}}
{{- end -}}
{{- $_ := set $names $e.name true -}}
{{- $ds := $e.datasource | default dict -}}
{{- if not $ds.url -}}
{{- fail (printf "observability-alerting: vmalert.remoteEvaluators[%d] (%s) has no `datasource.url`. It is the other store's read endpoint; without it there is nothing to evaluate against." $i (toString $e.name)) -}}
{{- end -}}
{{- if not (regexMatch "^https?://[^\\s]+$" (toString $ds.url)) -}}
{{- fail (printf "observability-alerting: vmalert.remoteEvaluators[%d] (%s) `datasource.url` %q is not an http(s) URL." $i (toString $e.name) (toString $ds.url)) -}}
{{- end -}}
{{- $auth := $ds.auth | default dict -}}
{{- if eq (not (not $auth.bearer)) (not (not $auth.basic)) -}}
{{- fail (printf "observability-alerting: vmalert.remoteEvaluators[%d] (%s) needs `datasource.auth` with exactly one of `bearer` ({secretName, key}) or `basic` ({secretName, usernameKey, passwordKey}), each naming an EXISTING Secret. An unauthenticated read of another store is refused, and a credential is never a value here." $i (toString $e.name)) -}}
{{- end -}}
{{- with $auth.bearer -}}
{{- if not (and .secretName .key) -}}
{{- fail (printf "observability-alerting: vmalert.remoteEvaluators[%d] (%s) `datasource.auth.bearer` must set both `secretName` and `key`: the Secret and the key holding the token." $i (toString $e.name)) -}}
{{- end -}}
{{- end -}}
{{- with $auth.basic -}}
{{- if not (and .secretName .usernameKey .passwordKey) -}}
{{- fail (printf "observability-alerting: vmalert.remoteEvaluators[%d] (%s) `datasource.auth.basic` must set `secretName`, `usernameKey` and `passwordKey`." $i (toString $e.name)) -}}
{{- end -}}
{{- end -}}
{{- with $ds.caBundle -}}
{{- if eq (not (not .configMap)) (not (not .secret)) -}}
{{- fail (printf "observability-alerting: vmalert.remoteEvaluators[%d] (%s) `datasource.caBundle` must name exactly one of `configMap` or `secret` ({name, key})." $i (toString $e.name)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
`upstreamRules.source: platform-alerts` hands the upstream rule sets to the
vendored pack in charts/platform-alerts. The sync Job applies them too unless
its rule sources are off, and the same alerts and recordings would be
evaluated twice — from two sources that drift apart (the Job tracks upstream's
branches, the pack is pinned). Helm cannot switch a subchart's value from
this chart's own, so this refuses the combination instead; the preset
presets/upstream-rules-platform-alerts.yaml sets both halves together.
*/}}
{{- define "observability-alerting.validate.upstreamRules" -}}
{{- $vmks := index .Values "victoria-metrics-k8s-stack" -}}
{{- if eq (.Values.upstreamRules).source "platform-alerts" -}}
{{- $dr := $vmks.defaultRules | default dict -}}
{{- if and $vmks.enabled (or $dr.enabled $dr.create) -}}
{{- fail "observability-alerting: `upstreamRules.source` is \"platform-alerts\" but `victoria-metrics-k8s-stack.defaultRules` is still on (`enabled`, or `create`, is true), so the sync job would keep applying the upstream rules that charts/platform-alerts now renders: every one would be evaluated twice. Turn it off (`victoria-metrics-k8s-stack.defaultRules.enabled: false` and `create: false`), or list the preset `presets/upstream-rules-platform-alerts.yaml`, which sets both halves. To keep the sync job as the source, set `upstreamRules.source: sync-job`." -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
This chart renders the alerting plane of a `mode: full` install. The stack
renders none of it in `operator-only` or `replica` mode, so a release of this
chart beside one would have nothing to hand over; refused rather than
rendering an empty release that reads as a decision.
*/}}
{{- define "observability-alerting.validate.mode" -}}
{{- if ne (.Values.mode | default "full") "full" -}}
{{- fail (printf "observability-alerting: `mode` is %q. The alerting plane (vmalerts, Alertmanager, karma, Watchdog rule, self-alerts) belongs to a `mode: full` install; the stack renders none of it in any other mode, so this chart has nothing to render. Do not install it beside this install." .Values.mode) -}}
{{- end -}}
{{- end -}}

{{/*
Names.

Only the objects THIS chart renders are named here. The stores are named
by their own charts, and this chart reads those names back (see the
address helpers below) rather than asking the caller to write them twice.
*/}}
{{- define "observability-stack.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "observability-stack.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "observability-stack.labels" -}}
app.kubernetes.io/name: {{ include "observability-stack.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: observability-stack
{{- end -}}

{{/*
The upstream charts' own fullnames, reproduced.

A parent chart cannot ask a subchart what it called something: Helm
renders each chart in isolation. These helpers reproduce the upstream
naming rules from the SAME values the upstream chart reads, so a caller
who sets `nameOverride` there does not have to repeat it here — and the
golden renders hold the resulting addresses, so a dependency bump that
changes a naming rule shows up as a diff rather than as a proxy pointing
at a Service that no longer exists.
*/}}
{{- define "observability-stack.vmks.fullname" -}}
{{- $v := index .Values "victoria-metrics-k8s-stack" | default dict -}}
{{- if $v.fullnameOverride -}}
{{- $v.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default "victoria-metrics-k8s-stack" $v.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "observability-stack.logs.fullname" -}}
{{- $v := index .Values "victoria-logs-single" | default dict -}}
{{- $server := $v.server | default dict -}}
{{- if $server.fullnameOverride -}}
{{- $server.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default "victoria-logs-single" $v.nameOverride -}}
{{- printf "%s-%s-server" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "observability-stack.traces.fullname" -}}
{{- $v := index .Values "victoria-traces-single" | default dict -}}
{{- $server := $v.server | default dict -}}
{{- if $server.fullnameOverride -}}
{{- $server.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default "vt-single" $v.nameOverride -}}
{{- printf "%s-%s-server" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/*
Addresses.

Written as `<service>.<namespace>.svc` rather than as a fully qualified
name: the cluster's DNS suffix is the estate's, not this chart's, and a
hardcoded one is wrong on every cluster that does not use the default.
*/}}
{{- define "observability-stack.metrics.url" -}}
{{- with .Values.stores.metrics.url -}}
{{- . -}}
{{- else -}}
{{- printf "http://vmsingle-%s.%s.svc:8428" (include "observability-stack.vmks.fullname" .) .Release.Namespace -}}
{{- end -}}
{{- end -}}

{{- define "observability-stack.logs.url" -}}
{{- with .Values.stores.logs.url -}}
{{- . -}}
{{- else -}}
{{- printf "http://%s.%s.svc:9428" (include "observability-stack.logs.fullname" .) .Release.Namespace -}}
{{- end -}}
{{- end -}}

{{- define "observability-stack.traces.url" -}}
{{- with .Values.stores.traces.url -}}
{{- . -}}
{{- else -}}
{{- printf "http://%s.%s.svc:10428" (include "observability-stack.traces.fullname" .) .Release.Namespace -}}
{{- end -}}
{{- end -}}

{{- define "observability-stack.tracesEnabled" -}}
{{- $v := index .Values "victoria-traces-single" | default dict -}}
{{- if or $v.enabled .Values.stores.traces.url -}}true{{- end -}}
{{- end -}}

{{- /*
Whether `victoria-metrics-k8s-stack`'s own vendored default rule set is
providing a `Watchdog` alert already — the reason
`templates/watchdog.yaml` renders this chart's OWN one only when this is
false. Two things have to hold: the metrics subchart's sync job has to
be populating rule sources AT ALL (`defaultRules.enabled` OR
`defaultRules.create`, defaulting `true` upstream — see
`defaultRules.create`'s own doc comment in values.yaml for why `create`
alone is not the switch it looks like), and its `general.rules` group —
upstream's own name, the one kube-prometheus's combined rule manifest
carries a `Watchdog` alert under — must not have been disabled on its
own via `defaultRules.groups`.

This cannot see the fetched rule CONTENT (the sync job fetches it over
the network, at apply time, not at render time), only whether this
install's own configuration would ask for it. See docs/safety.md for
what that leaves unverified.
*/}}
{{- define "observability-stack.vendoredWatchdogPresent" -}}
{{- $vmks := index .Values "victoria-metrics-k8s-stack" -}}
{{- /* `upstreamRules.source: platform-alerts`: upstream's Watchdog comes from the
       pack's `general.rules` group instead of the sync job; this chart's own
       would be a second one. */ -}}
{{- if eq (.Values.upstreamRules).source "platform-alerts" -}}true
{{- else -}}
{{- $dr := $vmks.defaultRules | default dict -}}
{{- $rulesOn := and $vmks.enabled (or $dr.enabled $dr.create) -}}
{{- $generalGroup := index ($dr.groups | default dict) "general.rules" | default dict -}}
{{- if and $rulesOn (ne $generalGroup.enabled false) -}}true{{- end -}}
{{- end -}}
{{- end -}}

 -}}

 -}}

 -}}

 -}}
 -}}
 -}}
 -}}
 -}}
 -}}

 -}}

 -}}

 -}}

 -}}

 -}}

 -}}

 -}}

 -}}

 -}}

 -}}

 -}}

 -}}

 -}}

 -}}

 -}}

 -}}

{{/*
How the proxy authenticates to a store.

The stores demand basic auth from everything, including the proxy in
front of them: a network policy is a rule about who may connect, and this
is a rule about who may read. Both, because each one is a different
mistake to make.
*/}}
{{- define "observability-stack.targetAuth" -}}
username:
  name: {{ .Values.storeCredentials.secretName | quote }}
  key: {{ .Values.storeCredentials.usernameKey | quote }}
password:
  name: {{ .Values.storeCredentials.secretName | quote }}
  key: {{ .Values.storeCredentials.passwordKey | quote }}
{{- end -}}

{{/*
Notifications: a Slack destination into an Alertmanager receiver name.

A destination is a (workspace, channel) pair: a bot token belongs to one
workspace, and the same channel name in two workspaces is two places. So
the name is derived from BOTH, joined by `--`: a slug never contains a
double hyphen (runs of non-alphanumerics collapse to one) and a workspace
name may not (values.schema.json), so `slack-a-b--c` and `slack-a--b-c`
can never be the same receiver by accident.

Every route only ever OVERRIDES a channel string, so the same destination
used from two places must render as the same receiver — hence a name
derived from the destination itself rather than invented per site.
Lower-cased and stripped to `[a-z0-9-]` because a receiver name reaching
this from `notifications.routes[].critical` is an estate's channel name,
not a value this chart controls the shape of.
*/}}
{{- define "observability-stack.notifications.slug" -}}
{{- $s := . | trimPrefix "#" | lower -}}
{{- regexReplaceAll "[^a-z0-9]+" $s "-" | trimAll "-" -}}
{{- end -}}

{{/*
A destination that pings (`mention: here|channel`) is a DIFFERENT receiver
from the same (workspace, channel) without one: the mention is part of
the receiver's message text, and one receiver cannot carry two texts. The
receiver is therefore keyed by (workspace, channel, mention), and the
mention is appended as a third `--` part ONLY when set. A destination
with no mention keeps the name it always had, so an install that does not
use `mention` renders byte-identical receivers (refusing a disagreement
instead would also have worked, but would have made a channel that wants
`@here` for critical and nothing for warning impossible to write). The
suffix is unambiguous for the same reason the workspace half is: a slug
never contains a double hyphen.
*/}}
{{- define "observability-stack.notifications.slackReceiver" -}}
{{- $name := printf "slack-%s--%s" .workspace (include "observability-stack.notifications.slug" .channel) -}}
{{- if .mention -}}{{- $name = printf "%s--%s" $name .mention -}}{{- end -}}
{{- $name -}}
{{- end -}}

{{/*
Notifications: one Slack destination, as YAML `{workspace, channel, mention}`
(`mention` empty for none).

`$cfg` is a severity tier (or the catch-all); `$override` is what a
route gave for that tier: "" for nothing, a channel string (the
tier's workspace is kept), or `{channel, workspace, mention}`. The workspace is
the override's, else the tier's, else — when exactly ONE workspace is
declared — that one. With two or more and none named it stays empty,
which `observability-stack.validate.notifications` refuses before
anything renders from it.
*/}}
{{- define "observability-stack.notifications.slackTarget" -}}
{{- $root := index . 0 -}}
{{- $cfg := index . 1 -}}
{{- $override := index . 2 -}}
{{- $slack := ($root.Values.notifications | default dict).slack | default dict -}}
{{- $workspaces := $slack.workspaces | default list -}}
{{- $channel := $cfg.channel -}}
{{- $workspace := $cfg.workspace | default "" -}}
{{- $mention := $cfg.mention | default "" -}}
{{- if kindIs "map" $override -}}
{{- $channel = $override.channel | default $cfg.channel -}}
{{- $mention = $override.mention | default $mention -}}
{{- $workspace = $override.workspace | default $workspace -}}
{{- else if $override -}}
{{- $channel = $override -}}
{{- end -}}
{{- if and (not $workspace) (eq (len $workspaces) 1) -}}
{{- $workspace = (index $workspaces 0).name -}}
{{- end -}}
workspace: {{ $workspace | quote }}
channel: {{ $channel | default "" | quote }}
mention: {{ $mention | quote }}
{{- end -}}

{{/*
Notifications: the key a workspace's bot token is read from, in the
Secret `workspaces[].appTokenSecret` names.
*/}}
{{- define "observability-stack.notifications.workspaceKey" -}}
{{- $want := index . 1 -}}
{{- range $w := index . 0 -}}
{{- if eq $w.name $want -}}{{- $w.appTokenSecret.key -}}{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Notifications: one severity tier's Telegram destination, as YAML
`{chat, thread}` — `notifications.telegram.chatId` /
`.messageThreadId`, each overridable per tier by the same key on
`notifications.severities.<tier>`. `thread` is empty for "no topic".

Both are integers in Alertmanager's own config. Helm reads a values
number as a float64, and a float64 printed as-is is `-1e+12`, so both
go through `int64` and `%d` here rather than being rendered raw.
*/}}
{{- define "observability-stack.notifications.telegramTarget" -}}
{{- $telegram := index . 0 -}}
{{- $cfg := index . 1 -}}
{{- $thread := ternary $cfg.messageThreadId $telegram.messageThreadId (hasKey $cfg "messageThreadId") -}}
{{- if and (not (hasKey $cfg "chatId")) ($telegram.chatIdSecret).name }}
{{- /*
The default chat read from `notifications.telegram.chatIdSecret`: the
chart never sees its value, so the target is the mounted file instead,
and `chat` stays empty.
*/}}
chat: ""
chatFile: {{ printf "/etc/alertmanager/notifications-telegram-chat/%s" ($telegram.chatIdSecret.key | default "chat_id") | quote }}
{{- else }}
{{- $chat := ternary $cfg.chatId $telegram.chatId (hasKey $cfg "chatId") -}}
chat: {{ printf "%d" ($chat | int64) | quote }}
{{- end }}
thread: {{ ternary (printf "%d" ($thread | int64)) "" (gt ($thread | default 0 | int64) 0) | quote }}
{{- end -}}

{{/*
A Telegram target into an Alertmanager receiver name, derived from the
chat and the thread so the same destination reached from two tiers is
one receiver. A group's chat id is negative; its sign is spelled `n`
rather than dropped, so chat `-5` and user `5` never share a name.
*/}}
{{- define "observability-stack.notifications.telegramReceiver" -}}
{{- $name := printf "telegram-%s" (.chat | replace "-" "n") -}}
{{- if .chatFile -}}
{{- /* A chat whose id the chart never sees: named for where it is read from. */ -}}
{{- $name = "telegram-chatfile" -}}
{{- end -}}
{{- if .thread -}}
{{- $name = printf "%s-%s" $name .thread -}}
{{- end -}}
{{- $name -}}
{{- end -}}

{{/*
A duration string, in seconds — for comparing two of them, which Helm has
no other way to do. Scoped to the unit suffixes `values.schema.json`
accepts (ms, s, m, h, d, w, y); a bare number never reaches here because
the schema already refuses one.
*/}}
{{- define "observability-stack.durationSeconds" -}}
{{- $d := toString . -}}
{{- $num := regexReplaceAll "^([0-9]+).*$" $d "${1}" | int64 -}}
{{- $unit := regexReplaceAll "^[0-9]+(.*)$" $d "${1}" -}}
{{- $perUnit := dict "ms" 0 "s" 1 "m" 60 "h" 3600 "d" 86400 "w" 604800 "y" 31536000 -}}
{{- mul $num (index $perUnit $unit) -}}
{{- end -}}

{{/*
Resolve one severity tier's target to an already-rendered receiver name.

`$root` is the top-level context (for the webhook list), `$severities`
is `notifications.severities`, `$tier` is "critical" or "warning", and
`$override` is what a project's own route entry gave for that tier (a
channel string, or `{channel, workspace}`), or "" when it gave none — in which case the tier's own
default channel is used, which is how a project naming only `critical`
gets its warnings routed to `severities.warning` without a second route
ever being written.
*/}}
{{- define "observability-stack.notifications.receiverFor" -}}
{{- $root := index . 0 -}}
{{- $severities := index . 1 -}}
{{- $tier := index . 2 -}}
{{- $override := index . 3 -}}
{{- $cfg := index $severities $tier -}}
{{- $telegram := ($root.Values.notifications | default dict).telegram | default dict -}}
{{- if eq $cfg.receiver "slack" -}}
{{- include "observability-stack.notifications.slackReceiver" (include "observability-stack.notifications.slackTarget" (list $root $cfg $override) | fromYaml) -}}
{{- else if and (eq $cfg.receiver "telegram") ($telegram.botTokenSecret).name -}}
{{- include "observability-stack.notifications.telegramReceiver" (include "observability-stack.notifications.telegramTarget" (list $telegram $cfg) | fromYaml) -}}
{{- else -}}
{{- $cfg.receiver -}}
{{- end -}}
{{- end -}}

{{/*
Operator-only's self-disable, for the four components whose own
`enabled` defaults to `null` rather than a literal `true`/`false`:
vmauth, vmalert, alertmanager, metricsSelfScrape.

A parent chart cannot compute a SUBCHART's values (see `mode`'s own
comment and docs/doctrine.md), but these four ARE this chart's own
values — `.Values.vmauth.enabled` and the rest are read here, by this
chart's own templates, not by a vendored one. That is what makes
self-disabling them possible at all where it is not for the three
stores, Grafana and the vendored sync Job.

`null` (the values.yaml default) is the one value that means "mode
decides": full resolves it to `true`, operator-only to `false`. Anything
else — an explicit `true` or `false` the caller actually wrote — is
taken exactly as written, INCLUDING a `true` beside `mode: operator-
only`, which `observability-stack.validate.mode` refuses outright. A
Helm value carries no memory of "the chart's own default" versus "the
caller wrote the same value" once the two coalesce, so `null` is the
only shape this chart can tell apart from a real answer — which is why
the default moved off `true` for exactly these four keys and nowhere
else.

Returns the four resolved booleans as a YAML mapping; parse it with
`fromYaml` at the call site: `{{- $eff := include
"observability-stack.effectiveEnabled" . | fromYaml -}}`, then read
`$eff.vmauth`, `$eff.vmalert`, `$eff.alertmanager`,
`$eff.metricsSelfScrape` — real booleans, not strings.
*/}}
{{- define "observability-stack.effectiveEnabled" -}}
{{- $full := eq (.Values.mode | default "full") "full" -}}
{{- /* `mode: replica` runs the stores, so it scrapes them; the other three are the primary release's. */ -}}
{{- $stores := or $full (eq (.Values.mode | default "full") "replica") -}}
vmauth: {{ if kindIs "invalid" .Values.vmauth.enabled }}{{ $full }}{{ else }}{{ .Values.vmauth.enabled }}{{ end }}
vmalert: {{ if kindIs "invalid" .Values.vmalert.enabled }}{{ $full }}{{ else }}{{ .Values.vmalert.enabled }}{{ end }}
alertmanager: {{ if kindIs "invalid" .Values.alertmanager.enabled }}{{ $full }}{{ else }}{{ .Values.alertmanager.enabled }}{{ end }}
metricsSelfScrape: {{ if kindIs "invalid" .Values.metricsSelfScrape.enabled }}{{ $stores }}{{ else }}{{ .Values.metricsSelfScrape.enabled }}{{ end }}
{{- end -}}

{{/*
The pair (docs/high-availability.md).

`ha` is the switch. The PRIMARY is `mode: full` with `ha.enabled: true`: it owns the
proxy, the vmalerts and everything else, and knows the other release's
stores by address (`pair.peer`). A REPLICA is `mode: replica`: the three
stores and nothing else. `observability-stack.ha.primary` is "true" on the
first and empty on every other install, so a template can gate on it.
*/}}
{{- define "observability-stack.ha.primary" -}}
{{- if and (include "observability-stack.ha" . | fromYaml).enabled (eq (.Values.mode | default "full") "full") -}}true{{- end -}}
{{- end -}}

{{/*
`ha`, normalised: null or the object (values.yaml, `ha`). Returned as YAML
with every field present: `enabled` (the pair is on: only the object with
`enabled: true`), `name`, `replica`, `peer.{metrics,logs,traces}`. Parse
with `fromYaml`.
*/}}
{{- define "observability-stack.ha" -}}
{{- $ha := .Values.ha -}}
{{- $out := dict "enabled" false "name" "observability" "replica" "" "peer" (dict "metrics" "" "logs" "" "traces" "") -}}
{{- if kindIs "map" $ha -}}
{{- $_ := set $out "enabled" ($ha.enabled | default false) -}}
{{- $_ := set $out "name" ($ha.name | default "observability") -}}
{{- $_ := set $out "replica" ($ha.replica | default "") -}}
{{- $peer := $ha.peer | default dict -}}
{{- $_ := set $out "peer" (dict "metrics" ($peer.metrics | default "") "logs" ($peer.logs | default "") "traces" ($peer.traces | default "")) -}}
{{- end -}}
{{- toYaml $out -}}
{{- end -}}

{{/*
This release's replica label: `pair.replica`, else `a` on the primary and
`b` on a replica. The other half of the pair is the other letter.
*/}}
{{- define "observability-stack.pair.replica" -}}
{{- (include "observability-stack.ha" . | fromYaml).replica | default (ternary "b" "a" (eq (.Values.mode | default "full") "replica")) -}}
{{- end -}}

{{- define "observability-stack.pair.otherReplica" -}}
{{- ternary "b" "a" (eq (include "observability-stack.pair.replica" .) "a") -}}
{{- end -}}

 -}}

{{/*
`backup.destination` (an `s3://`, `gs://` or `fs://` URL — vmbackup's own
vocabulary, and what `-dst`/`-origin` render unmodified for the metrics
job) translated into an rclone DESTINATION for the logs/traces jobs,
which speak rclone's connection-string syntax, never a URL: handed
`s3://bucket/path` verbatim, rclone reads it as a remote named `s3`
that does not exist ("didn't find section in config file") and does
nothing — found live, 2026-09-29, against the pinned `rclone/rclone:
1.73.0`.

`env_auth=true` is the reason ONE destination form works under every
`backup.auth.mode`: it is rclone's own instruction to resolve
credentials from the SAME chain vmbackup's AWS SDK already does for
`ambient` (environment, then IRSA/`AWS_WEB_IDENTITY_TOKEN_FILE`, then
EKS Pod Identity, then IMDS — docs/reference.md, "`ambient`") — and under
`secret`, `backup.credentialsSecret` is `envFrom`'d into the same pod, so
the AWS SDK's OWN env-var precedence resolves it the identical way.
Neither mode needs a branch here. `credentialProcess` never reaches this
helper at all: `templates/backup.yaml` refuses it combined with
`backup.logs.enabled`/`backup.traces.enabled` (see backup.yaml's own
refusal), so only the metrics job — which calls vmbackup directly, not
rclone — ever renders under it.

Takes the raw `backup.destination` string; the caller appends
`/<prefix>` itself, exactly as it already did with the untranslated URL.
*/}}
{{- define "observability-stack.backup.rcloneDestination" -}}
{{- $url := . -}}
{{- if hasPrefix "s3://" $url -}}
{{- printf ":s3,env_auth=true:%s" (trimPrefix "s3://" $url) -}}
{{- else if hasPrefix "gs://" $url -}}
{{- printf ":gcs,env_auth=true:%s" (trimPrefix "gs://" $url) -}}
{{- else if hasPrefix "fs://" $url -}}
{{- trimPrefix "fs://" $url -}}
{{- else -}}
{{- fail (printf "observability-stack: `backup.destination` %q has no recognized scheme. rclone (the logs/traces backup jobs) and vmbackup (the metrics job) both need one of `s3://`, `gs://` or `fs://`." $url) -}}
{{- end -}}
{{- end -}}

{{/*
A component's `resources` without its nulls.

Until the default CPU limits were removed this chart's own components (vmauth, the vmalerts,
Alertmanager) defaulted a CPU limit, and Helm deletes a null that meets a
default, so `limits: {cpu: null}` was how a consumer removed it. The
default is gone, so Helm now keeps the null; dropping it here keeps that
existing values file meaning the same thing.
*/}}
{{- define "observability-stack.resources" -}}
{{- $out := dict -}}
{{- range $side, $m := . -}}
{{- $clean := dict -}}
{{- range $k, $v := $m -}}
{{- if not (kindIs "invalid" $v) -}}{{- $_ := set $clean $k $v -}}{{- end -}}
{{- end -}}
{{- $_ := set $out $side $clean -}}
{{- end -}}
{{- toYaml $out -}}
{{- end -}}

{{/*
The security context of a pod this chart renders through the
VictoriaMetrics operator (VMAlert, VMAuth, VMAlertmanager) or as a Job, set
to the Pod Security `restricted` profile. The operator inlines the pod and
the container fields of one `securityContext`, so this single object covers
the pod and every container the operator adds beside the main one (the
config reloader included).

65534 is the `nobody` user these images already treat as their own.
`fsGroupChangePolicy: OnRootMismatch` keeps a volume that is already owned
correctly from being walked again on every start.
*/}}
{{- define "observability-stack.restrictedSecurityContext" -}}
runAsNonRoot: true
runAsUser: 65534
runAsGroup: 65534
fsGroup: 65534
fsGroupChangePolicy: OnRootMismatch
seccompProfile:
  type: RuntimeDefault
allowPrivilegeEscalation: false
capabilities:
  drop:
    - ALL
{{- end -}}

{{/*
The `notifiers:` entries of a vmalert that notifies this install's
Alertmanager: one per replica of the pair, or the one URL. Shared by the
main alerters and the remote evaluators (templates/vmalert.yaml). Lines
start at column 0; indent at the call site.
*/}}
{{- define "observability-stack.vmalert.notifiers" -}}
{{- $fullname := include "observability-stack.fullname" . -}}
{{- $eff := include "observability-stack.effectiveEnabled" . | fromYaml -}}
{{- $notifier := .Values.alertmanager.notifierUrl | default (printf "http://vmalertmanager-%s.%s.svc:9093" $fullname .Release.Namespace) -}}
{{- if and $eff.alertmanager (not .Values.alertmanager.notifierUrl) (gt (int .Values.alertmanager.replicaCount) 1) -}}
{{- /*
An Alertmanager pair: vmalert sends every alert to EVERY instance,
through the per-pod DNS name of the operator's headless Service, and
the instances dedup through the mesh. One load-balanced URL would hand
an alert to one replica only, and a replica restart would lose it.
https://docs.victoriametrics.com/victoriametrics/vmalert/#high-availability
("The same alert will be sent to all configured notifiers").
*/ -}}
{{- range $i := until (int .Values.alertmanager.replicaCount) }}
- url: {{ printf "http://vmalertmanager-%s-%d.vmalertmanager-%s.%s.svc:9093" $fullname $i $fullname $.Release.Namespace | quote }}
{{- end -}}
{{- else }}
- url: {{ $notifier | quote }}
{{- end -}}
{{- end -}}

 -}}

 -}}

 -}}

 -}}

 -}}

 -}}

 -}}

 -}}

{{/*
The per-alert body of a notification, as Alertmanager template text
(docs/notifications.md, "Annotations"). Takes a dict: `externalUrl` (the
Grafana base; empty means no Grafana link) and `runbookBaseUrl`.

For each alert, in order:

  *summary*  · <Grafana>      bold; the alertname when there is no summary;
                              Grafana ONLY when the alert has
                              k8s_cluster_name (an alert born outside a
                              cluster, alert-ingress, has none, and a link
                              with an empty cluster opens nothing)
  description                 trimmed, cut at 1500 characters with "…"
  <link|link_name> · <Runbook>
                              `link` only when it is an http(s) URL, named
                              `link_name` or "Open"; the runbook when the
                              annotation is an http(s) URL, or is a path and
                              `runbookBaseUrl` is set

The first 5 alerts get all of that, the next 15 their summary only, and a
last line counts the rest: a group of hundreds stays one readable message,
well inside Slack's 40,000-character limit (at most about 12,000 here).

Text from an alert is written with Slack's escaping (`&`, `<`, `>` as
`&amp;`, `&lt;`, `&gt;`), so an annotation cannot open a link or a mention;
inside a link URL, `<`, `>` and `|` are percent-escaped and `&` is `&amp;`.
Alertmanager's own functions only (`match`, `reReplaceAll`, `trimSpace`)
and text/template's builtins: a 1500-character cut is `.{1000}.{500}`
because RE2 refuses a repeat count above 1000.

It is a named template so a receiver other than Slack can reuse the same
convention later; the Telegram message is HTML and keeps its own.
*/}}
{{- define "observability-stack.notifications.slackAlerts" -}}
{{- $esc := `| reReplaceAll "&" "&amp;" | reReplaceAll "<" "&lt;" | reReplaceAll ">" "&gt;"` -}}
{{- $urlEsc := `| reReplaceAll "&" "&amp;" | reReplaceAll "<" "%3C" | reReplaceAll ">" "%3E" | reReplaceAll "\\|" "%7C"` -}}
{{- $summary := printf `{{ or $a.Annotations.summary $a.Labels.alertname %s }}` $esc -}}
{{ `{{ range $i, $a := .Alerts }}{{ if lt $i 5 }}{{ if $i }}` }}
{{ `{{ end }}*` }}{{ $summary }}*
{{- if .externalUrl }}{{ `{{ if $a.Labels.k8s_cluster_name }} · <` }}{{ .externalUrl }}{{ `/?var-cluster={{ $a.Labels.k8s_cluster_name | urlquery }}{{ if $a.Labels.k8s_namespace_name }}&amp;var-namespace={{ $a.Labels.k8s_namespace_name | urlquery }}{{ end }}|Grafana>{{ end }}` }}{{ end }}
{{ `{{ with trimSpace $a.Annotations.description }}{{ $d := . }}{{ if match "(?s)^.{1000}.{501}" $d }}{{ $d = printf "%s…" (reReplaceAll "(?s)^(.{1000}.{500}).*$" "$1" $d) }}{{ end }}{{ $d ` }}{{ $esc }}{{ ` }}` }}
{{ `{{ end }}{{ $l := "" }}{{ if match "^https?://" $a.Annotations.link }}{{ $l = printf "<%s|%s>" ($a.Annotations.link ` }}{{ $urlEsc }}{{ `) (or $a.Annotations.link_name "Open" ` }}{{ $esc }}{{ `) }}{{ end }}{{ $r := "" }}{{ if match "^https?://" $a.Annotations.runbook }}{{ $r = $a.Annotations.runbook }}` }}
{{- if .runbookBaseUrl }}{{ `{{ else if $a.Annotations.runbook }}{{ $r = print ` }}{{ .runbookBaseUrl | quote }}{{ ` $a.Annotations.runbook }}` }}{{ end }}
{{- `{{ end }}{{ if $r }}{{ $r = printf "<%s|Runbook>" ($r ` }}{{ $urlEsc }}{{ `) }}{{ if $l }}{{ $l = print $l " · " $r }}{{ else }}{{ $l = $r }}{{ end }}{{ end }}{{ if $l }}{{ $l }}` }}
{{ `{{ end }}{{ else if lt $i 20 }}{{ if eq $i 5 }}` }}
{{ `{{ end }}• ` }}{{ $summary }}
{{ `{{ end }}{{ end }}{{ if gt (len .Alerts) 20 }}… {{ len .Alerts }} alerts in this notification; the first 20 are listed.` }}
{{ `{{ end }}` }}
{{- end -}}

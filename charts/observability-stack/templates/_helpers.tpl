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
{{- $dr := $vmks.defaultRules | default dict -}}
{{- $rulesOn := and $vmks.enabled (or $dr.enabled $dr.create) -}}
{{- $generalGroup := index ($dr.groups | default dict) "general.rules" | default dict -}}
{{- if and $rulesOn (ne $generalGroup.enabled false) -}}true{{- end -}}
{{- end -}}

{{- /*
The METRICS vmalert's own Service — not a store, so it carries no
`stores.*.url` override the way the three above do: nothing outside this
chart runs its own vmalert for this chart's tenancy to point at.

The VictoriaMetrics operator names a VMAlert's generated Service
"vmalert-<CR name>" (its `PrefixedName()`, `UseLegacyNaming` unset), on
its default port 8080 — templates/vmalert.yaml names the metrics CR
"<fullname>-metrics" and sets no `spec.port` override.
*/}}
{{- define "observability-stack.vmalert.metrics.url" -}}
{{- printf "http://vmalert-%s-metrics.%s.svc:8080" (include "observability-stack.fullname" .) .Release.Namespace -}}
{{- end -}}

{{/*
The read routes, per store.

Every path is named. The tempting shapes — `/.*`, or `/api/v1/.*` — are
not read routes: `/api/v1/write`, `/api/v1/import`,
`/api/v1/admin/tsdb/delete_series` and `/internal/force_merge` all live
under them, and the operator's own default for a targetRef without
`paths` is `/.*`. A reader's route that also accepts writes is not a
reader's route.

These lists are the ones `pkg/tenancy` renders, and the `tenancy` golden
case proves the two still match.
*/}}
{{- define "observability-stack.readPaths.metrics" -}}
- /prometheus/api/v1/query
- /prometheus/api/v1/query_range
- /prometheus/api/v1/series
- /prometheus/api/v1/labels
- /prometheus/api/v1/label/[^/]+/values
{{- /*
`/status/tsdb` by name, and the rest of `/status/` not at all.

TSDBStatusHandler takes its label filters from the same getCommonParams
every query handler uses, so the principal's selector reaches it. Its
neighbours do not take one: `/status/active_queries` and
`/status/top_queries` return other principals' query TEXT,
`/status/metric_names_stats` returns metric names across every namespace,
and `/api/v1/metadata` returns the metadata of every series in the
store. `/status/[^/]+` was all three of those plus this one.
*/}}
- /prometheus/api/v1/status/tsdb
{{- /*
Carries the store's version and nothing from any namespace. Grafana's
Prometheus datasource asks for it to decide which dialect it is talking
to.
*/}}
- /prometheus/api/v1/status/buildinfo
- /prometheus/vmui.*
{{- /*
`/api/v1/metadata` only when the estate has said so.

It returns every metric NAME in the store with its type and help, and no
filter reaches it: measured against the store, `extra_filters` naming a
namespace that matches nothing returns the same body as no filter at
all. So the route tells every principal which metrics exist, whatever
their grant says.

Its three siblings stay out under every setting, because what they leak
is per-principal rather than bounded: `/status/active_queries` and
`/status/top_queries` return other principals' query TEXT, and
`/status/metric_names_stats` returns names with per-tenant counts.

Leaving it off is visible rather than silent, which is the reason it is a
switch and not a rule: Grafana's Prometheus-family datasources ask for
this endpoint to put descriptions on metric names, and log a 401 when
the proxy does not route it. The query builder still works -- the names
come from `/label/__name__/values`, which IS filtered -- and only the
descriptions are missing.
*/ -}}
{{- if .Values.tenancy.allowUnfilteredMetricMetadata }}
- /prometheus/api/v1/metadata
{{- end }}
{{- end -}}

{{/*
The query argument that carries a principal's filter into each store,
and the vmauth placeholder that fills it.

This is the whole enforcement mechanism, and it is a separate helper
because the thing that goes wrong is leaving it out.

vmauth verifies the token, selects the VMUser by `matchClaims`, computes
the principal's `vm_access` claim from `defaultVMAccessClaim` — and then
applies it ONLY by substituting a placeholder into the route it chose.
A `targetRef` with no `query_args` forwards the request unfiltered, with
the claim computed, correct, visible in this manifest and discarded. It
renders identically to a working configuration and answers a
single-namespace question identically too.

The placeholder must be the WHOLE value of the argument: vmauth looks
the value up in a map rather than replacing a substring, so
`extra_filters=x{{"{{"}}.MetricsExtraFilters{{"}}"}}` would be forwarded
as written.

The arguments differ per store because the stores do. vmselect OR-s the
`extra_filters` it is given; VictoriaLogs AND-s every
`extra_stream_filters` into the query and into every subquery inside it,
which is what stops a subquery escaping the grant.
*/}}
{{- define "observability-stack.filterArg.metrics" -}}extra_filters{{- end -}}
{{- define "observability-stack.filterArg.logs" -}}extra_stream_filters{{- end -}}
{{- define "observability-stack.filterPlaceholder.metrics" -}}{{ "{{.MetricsExtraFilters}}" }}{{- end -}}
{{- define "observability-stack.filterPlaceholder.logs" -}}{{ "{{.LogsExtraStreamFilters}}" }}{{- end -}}

{{- define "observability-stack.readQueryArgs.metrics" -}}
- name: {{ include "observability-stack.filterArg.metrics" . }}
  values:
    - {{ include "observability-stack.filterPlaceholder.metrics" . | quote }}
{{- end -}}

{{- define "observability-stack.readQueryArgs.logs" -}}
- name: {{ include "observability-stack.filterArg.logs" . }}
  values:
    - {{ include "observability-stack.filterPlaceholder.logs" . | quote }}
{{- end -}}

{{- /*
The subset of `readPaths.metrics` a `tenancy.principals[].metricsQueryOnly`
reader gets: the two query endpoints and nothing else this store's own
full reader list also carries — no series, no labels, no label values,
no tsdb status, no vmui, and (gated the same way as the full list)
`/api/v1/metadata` never at all, because a query-only reader has less
reason for it than a full one does, not more.
*/}}
{{- define "observability-stack.readPaths.metrics.queryOnly" -}}
- /prometheus/api/v1/query
- /prometheus/api/v1/query_range
{{- end -}}

{{- define "observability-stack.readPaths.logs" -}}
- /select/logsql/.*
- /select/vmui.*
{{- end -}}

{{- define "observability-stack.readPaths.traces" -}}
- /select/jaeger/.*
- /select/tempo/.*
{{- end -}}

{{- /*
The one route `tenancy.alertReaders` is admitted to: vmalert's own
active-alerts listing. Exact path, not a prefix — vmalert's write-ish
surface (`/-/reload`, its own `/api/v1/rules`) is a different concern
already excluded by not being in this list at all, but an exact path is
the same belt-and-braces this chart uses everywhere else a route cannot
be scoped by a filter.
*/}}
{{- define "observability-stack.readPaths.alerts" -}}
- /api/v1/alerts
{{- end -}}

{{/*
The two routes a `tenancy.principals[].vmalertAPI` principal gets, written
the way a stock VictoriaMetrics MCP server in single-node mode asks for
them: `<entrypoint>/vmalert/api/v1/{alerts,rules}` with the entrypoint on
the store's `/prometheus` path. vmauth forwards the request path as
received, so each route drops its first path part
(`drop_src_path_prefix_parts: 1`) and vmalert sees `/vmalert/api/v1/...`,
which it serves. Exact paths, never a prefix: vmalert's `/-/reload` and its
per-rule and per-group endpoints are not reads this chart admits.
*/}}
{{- define "observability-stack.readPaths.vmalertAPI" -}}
- /prometheus/vmalert/api/v1/alerts
- /prometheus/vmalert/api/v1/rules
{{- end -}}

{{- define "observability-stack.writePaths.metrics" -}}
- /prometheus/api/v1/write
- /api/v1/write
- /opentelemetry/v1/metrics
{{- end -}}

{{/*
The log store's ingestion endpoints, ENUMERATED.

This was `/insert/.*` — one line, every endpoint, and two faults in it.

vmauth matches `src_paths` in the order the routes are declared and stops
at the first hit, and the writer's routes are declared metrics, logs,
traces. So `/insert/.*` matched `/insert/opentelemetry/v1/traces` before
the trace store's own route was ever reached, and EVERY span a writer
sent was posted to the log store, which answered 400. Nothing said so:
the collector recorded a permanent rejection and dropped the batch, the
trace store stayed empty, and an empty trace store is indistinguishable
from an estate that emits no spans. It is how this install shipped.

The second fault is `/insert/multitenant/*`, which the log store offers
so a writer can NAME the tenant it is writing to. This proxy exists to
decide that, so the catch-all was also a route around it.

The list is the store's own, and a new endpoint has to be added here
deliberately -- which is the point.
*/}}
{{- define "observability-stack.writePaths.logs" -}}
- /insert/datadog/api/v2/logs
- /insert/elasticsearch/_bulk
- /insert/journald/upload
- /insert/jsonline
- /insert/loki/api/v1/push
- /insert/native
- /insert/opentelemetry/v1/logs
- /insert/splunk
{{- end -}}

{{- define "observability-stack.writePaths.traces" -}}
- /insert/opentelemetry/v1/traces
{{- end -}}

{{/*
A writer's cluster pin, per signal — the write-side counterpart of
`readQueryArgs.*` above, and the reason a bearer token cannot simply
claim to be a cluster it is not once `tenancy.writers[].cluster` is set.

Each renders the query arg that FORCES the label or field, not merely
requests it: unlike `extra_filters`/`extra_stream_filters`, which vmauth
substitutes a Go-template placeholder into and the reader's OWN grant
fills in, these three are literal values this chart writes directly, no
placeholder involved, because the value is fixed at render time by
`writers[].cluster` rather than computed per request from a token's
claims.

Each takes a dict with `cluster` (the writer's pin, possibly empty) and
either `label` (metrics) or `field` (logs, traces) — the name to force —
and renders nothing at all when `cluster` is empty, which is how the
unscoped, local writer keeps rendering with no `query_args` on its
`targetRefs`, exactly as it did before this mechanism existed.

Confirmed against the pinned vmsingle and against a live VictoriaLogs and
VictoriaTraces (see values.yaml's own comment on `tenancy.writers` for
the detail): all three OVERRIDE a same-named value the writer's own
config already sent, they do not add a second, ignored one beside it.
*/}}
{{- define "observability-stack.writeQueryArgs.metrics" -}}
{{- if .cluster -}}
- name: extra_label
  values:
    - {{ printf "%s=%s" .label .cluster | quote }}
{{- end -}}
{{- end -}}

{{- define "observability-stack.writeQueryArgs.logs" -}}
{{- if .cluster -}}
- name: extra_fields
  values:
    - {{ printf "%s=%s" .field .cluster | quote }}
{{- end -}}
{{- end -}}

{{- /*
Traces alone need a prefix on the field name: VictoriaTraces stores every
OTLP resource attribute — which is where an OTel collector puts cluster
identity, the same as it does for logs — as a field named
`resource_attr:<name>`, not the bare attribute name. This is not
documented anywhere public with the prefix spelled out; it was read back
off a live ingest. Forcing the bare name instead would add a second,
inert field beside the real one: confirmed live that it enforces nothing.
*/ -}}
{{- define "observability-stack.writeQueryArgs.traces" -}}
{{- if .cluster -}}
- name: extra_fields
  values:
    - {{ printf "resource_attr:%s=%s" .field .cluster | quote }}
{{- end -}}
{{- end -}}

{{/*
One grant, as a MetricsQL series selector. vmselect OR-s the
`extra_filters` it is given, so one entry per grant is the principal's
whole reach.

This is `pkg/tenancy`'s metricsFilter, in Helm.
*/}}
{{- define "observability-stack.filter" -}}
{{- $cluster := printf "%s=%q" .labels.cluster .grant.cluster -}}
{{- if .grant.allNamespaces -}}
{{- printf "{%s}" $cluster -}}
{{- else -}}
{{- printf "{%s,%s=~\"^(%s)$\"}" $cluster .labels.namespace (join "|" (sortAlpha .grant.namespaces)) -}}
{{- end -}}
{{- end -}}

{{/*
A principal's WHOLE reach, as ONE LogsQL stream filter.

This is `pkg/tenancy`'s logsFilter, in Helm, and it differs from the
metrics one above in three ways that are all forced.

The FIELD NAMES are `tenancy.logsClusterField` and
`tenancy.logsNamespaceField` rather than the label keys: a label cannot
carry a dot, and the container-log agent can rename no field, so the log
store carries the namespace under the name that agent produces and a
filter naming the metrics label selects a field that does not exist — an
empty result, with no error anywhere.

The NAMES ARE QUOTED, because a LogsQL word is [a-zA-Z0-9_] and a real
field name has dots. Quoting is unconditional: a bare name
that collides with a keyword or a pipe name would parse as that keyword,
and the schema's `logFieldName` shape guarantees there is nothing inside
the quotes to escape.

And it is ONE filter with the grants as `or` alternatives, because
VictoriaLogs AND-s every `extra_stream_filters` argument into the query as
its own global constraint. Two entries would not widen a principal's
reach, they would narrow it to the intersection — and two grants naming
two clusters intersect in nothing at all. Comma binds tighter than
`or` inside `{...}`, so each alternative stays its own conjunction.
*/}}
{{- define "observability-stack.logsFilter" -}}
{{- $fields := .fields -}}
{{- $alternatives := list -}}
{{- range $grant := .grants -}}
{{- $cluster := printf "%s=%q" ($fields.cluster | quote) $grant.cluster -}}
{{- if $grant.allNamespaces -}}
{{- $alternatives = append $alternatives $cluster -}}
{{- else -}}
{{- $alternatives = append $alternatives (printf "%s,%s=~\"^(%s)$\"" $cluster ($fields.namespace | quote) (join "|" (sortAlpha $grant.namespaces))) -}}
{{- end -}}
{{- end -}}
{{- /*
`_stream:` is not decoration. VictoriaLogs reads an
`extra_stream_filters` value that begins with `{"` as its JSON object
form, and every filter rendered here begins with `{"` because the log
store's field names have to be quoted. Without the prefix the value
reaches a JSON parser and comes back as `cannot parse JSON: missing ':'
after object key` — a 400 on every log query the principal makes. With
it, the value is parsed as LogsQL, and `_stream:{...}` is exactly the
stream filter the bare `{...}` was meant to be.
*/}}
{{- printf "_stream:{%s}" (join " or " $alternatives) -}}
{{- end -}}

{{/*
The credentials every store demands, as environment variables.

Not as flags: a flag value is in the pod spec, so it is in every
`kubectl describe` and in `helm get manifest`. The stores read
`-envflag.enable`, which this chart sets on each of them.
*/}}
{{- define "observability-stack.storeCredentialEnv" -}}
- name: VM_httpAuth_username
  valueFrom:
    secretKeyRef:
      name: {{ .Values.storeCredentials.secretName | quote }}
      key: {{ .Values.storeCredentials.usernameKey | quote }}
- name: VM_httpAuth_password
  valueFrom:
    secretKeyRef:
      name: {{ .Values.storeCredentials.secretName | quote }}
      key: {{ .Values.storeCredentials.passwordKey | quote }}
{{- end -}}

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
channel: {{ $channel | quote }}
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
vmauth: {{ if kindIs "invalid" .Values.vmauth.enabled }}{{ $full }}{{ else }}{{ .Values.vmauth.enabled }}{{ end }}
vmalert: {{ if kindIs "invalid" .Values.vmalert.enabled }}{{ $full }}{{ else }}{{ .Values.vmalert.enabled }}{{ end }}
alertmanager: {{ if kindIs "invalid" .Values.alertmanager.enabled }}{{ $full }}{{ else }}{{ .Values.alertmanager.enabled }}{{ end }}
metricsSelfScrape: {{ if kindIs "invalid" .Values.metricsSelfScrape.enabled }}{{ $full }}{{ else }}{{ .Values.metricsSelfScrape.enabled }}{{ end }}
{{- end -}}

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

Until v0.20.0 this chart's own components (vmauth, the vmalerts,
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

{{/*
Names.

This chart renders objects that observability-stack renders today, under the
SAME kind, name and namespace, so that an Argo CD Application switch adopts
them in place. Everything below therefore reproduces the stack's naming from
the stack's own inputs rather than from this release:

  - `stackReleaseName` (default: this release's name) stands for the stack's
    Helm release name. The stack derives every name from it
    (`<release>-observability-stack`, or the release itself when it already
    contains `observability-stack`) and the upstream subcharts' names from it
    too (`vmks.fullname`, `logs.fullname`, `traces.fullname`).
  - the chart name used in names and in `app.kubernetes.io/name` is the
    stack's, `observability-stack`.

tests/proxy_chart_test.go renders both charts for every stack case and holds
the objects byte-identical, which is what keeps these copies honest.
*/}}
{{- define "observability-proxy.release" -}}
{{- .Values.stackReleaseName | default .Release.Name -}}
{{- end -}}

{{- define "observability-proxy.name" -}}
{{- default "observability-stack" .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "observability-proxy.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default "observability-stack" .Values.nameOverride -}}
{{- if contains $name (include "observability-proxy.release" .) -}}
{{- (include "observability-proxy.release" .) | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" (include "observability-proxy.release" .) $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "observability-proxy.labels" -}}
app.kubernetes.io/name: {{ include "observability-proxy.name" . }}
app.kubernetes.io/instance: {{ (include "observability-proxy.release" .) }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: observability-stack
{{- end -}}

{{/*
Which of the stack's components are on, reduced to the one this chart
renders: the proxy, plus the two switches its routes depend on (a route to
the vmalert API or to Alertmanager needs one to exist). Each unset follows
`mode` (on for `full`), as in the stack.
*/}}
{{- define "observability-proxy.effectiveEnabled" -}}
{{- $full := eq (.Values.mode | default "full") "full" -}}
vmauth: {{ if kindIs "invalid" .Values.vmauth.enabled }}{{ $full }}{{ else }}{{ .Values.vmauth.enabled }}{{ end }}
vmalert: {{ if kindIs "invalid" (.Values.vmalert).enabled }}{{ $full }}{{ else }}{{ .Values.vmalert.enabled }}{{ end }}
alertmanager: {{ if kindIs "invalid" (.Values.alertmanager).enabled }}{{ $full }}{{ else }}{{ .Values.alertmanager.enabled }}{{ end }}
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
{{- define "observability-proxy.vmks.fullname" -}}
{{- $v := index .Values "victoria-metrics-k8s-stack" | default dict -}}
{{- if $v.fullnameOverride -}}
{{- $v.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default "victoria-metrics-k8s-stack" $v.nameOverride -}}
{{- if contains $name (include "observability-proxy.release" .) -}}
{{- (include "observability-proxy.release" .) | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" (include "observability-proxy.release" .) $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "observability-proxy.logs.fullname" -}}
{{- $v := index .Values "victoria-logs-single" | default dict -}}
{{- $server := $v.server | default dict -}}
{{- if $server.fullnameOverride -}}
{{- $server.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default "victoria-logs-single" $v.nameOverride -}}
{{- printf "%s-%s-server" (include "observability-proxy.release" .) $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{- define "observability-proxy.traces.fullname" -}}
{{- $v := index .Values "victoria-traces-single" | default dict -}}
{{- $server := $v.server | default dict -}}
{{- if $server.fullnameOverride -}}
{{- $server.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default "vt-single" $v.nameOverride -}}
{{- printf "%s-%s-server" (include "observability-proxy.release" .) $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/*
Addresses.

Written as `<service>.<namespace>.svc` rather than as a fully qualified
name: the cluster's DNS suffix is the estate's, not this chart's, and a
hardcoded one is wrong on every cluster that does not use the default.
*/}}
{{- define "observability-proxy.metrics.url" -}}
{{- with .Values.stores.metrics.url -}}
{{- . -}}
{{- else -}}
{{- printf "http://vmsingle-%s.%s.svc:8428" (include "observability-proxy.vmks.fullname" .) .Release.Namespace -}}
{{- end -}}
{{- end -}}

{{- define "observability-proxy.logs.url" -}}
{{- with .Values.stores.logs.url -}}
{{- . -}}
{{- else -}}
{{- printf "http://%s.%s.svc:9428" (include "observability-proxy.logs.fullname" .) .Release.Namespace -}}
{{- end -}}
{{- end -}}

{{- define "observability-proxy.traces.url" -}}
{{- with .Values.stores.traces.url -}}
{{- . -}}
{{- else -}}
{{- printf "http://%s.%s.svc:10428" (include "observability-proxy.traces.fullname" .) .Release.Namespace -}}
{{- end -}}
{{- end -}}

{{- define "observability-proxy.tracesEnabled" -}}
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
{{- define "observability-proxy.vendoredWatchdogPresent" -}}
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

{{- /*
The METRICS vmalert's own Service — not a store, so it carries no
`stores.*.url` override the way the three above do: nothing outside this
chart runs its own vmalert for this chart's tenancy to point at.

The VictoriaMetrics operator names a VMAlert's generated Service
"vmalert-<CR name>" (its `PrefixedName()`, `UseLegacyNaming` unset), on
its default port 8080 — templates/vmalert.yaml names the metrics CR
"<fullname>-metrics" and sets no `spec.port` override.
*/}}
{{- define "observability-proxy.vmalert.metrics.url" -}}
{{- printf "http://vmalert-%s-metrics.%s.svc:8080" (include "observability-proxy.fullname" .) .Release.Namespace -}}
{{- end -}}

{{/*
The same, for the vmalert that evaluates the PEER's metrics store (`ha`
only, templates/vmalert.yaml): the operator names its Service from the CR,
"<fullname>-metrics-peer".
*/}}
{{- define "observability-proxy.vmalert.metrics.peerUrl" -}}
{{- printf "http://vmalert-%s-metrics-peer.%s.svc:8080" (include "observability-proxy.fullname" .) .Release.Namespace -}}
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
{{- define "observability-proxy.readPaths.metrics" -}}
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
{{- /*
Three endpoints Grafana's Prometheus datasource calls on its own, none of
which reads a series.

`/rules` and `/query_exemplars` answer from fixed placeholders in the
store (`{"groups":[]}` and `[]`), because the store runs without
`-vmalert.proxyURL`. If an install ever sets that flag, `/rules` starts
forwarding to vmalert and returns every rule group with its expression,
for every principal; that would need the same switch as
`/api/v1/metadata` below. `/format_query` is not implemented by the store
at all, and it answers 400 `unsupported path requested`.

They are routed anyway, because an unrouted path costs more than it
seems. vmauth answers a VERIFIED JWT user whose request matches no route
with the same 401 and `WWW-Authenticate: Basic` it sends a request with
no credentials. Grafana relays that header, and the browser opens a
sign-in prompt in Explore that no password can satisfy. Meanwhile the
`missing_route` counter stays at zero and nothing is logged.
*/}}
- /prometheus/api/v1/rules
- /prometheus/api/v1/query_exemplars
- /prometheus/api/v1/format_query
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

The metrics route pins TWO spellings of its argument. vmselect reads
`extra_filters` and ALSO `extra_filters[]` (searchutil.GetExtraTagFilters
appends both lists, then ORs them), and vmauth drops a client argument
only when the route set the SAME name. A route pinning only
`extra_filters` therefore let a reader send
`extra_filters[]={k8s_cluster_name=~".+"}` and widen its grant to every
series in the store. VictoriaLogs reads neither array spelling and AND-s
everything it does read, so the logs route pins one name. The list lives
in `filterArgs.*`; `pkg/tenancy` (MetricsFilterArgs) is its twin and
tests/agreement_test.go fails if they differ.
*/}}
{{- define "observability-proxy.filterArg.metrics" -}}extra_filters{{- end -}}
{{- define "observability-proxy.filterArg.logs" -}}extra_stream_filters{{- end -}}
{{- define "observability-proxy.filterArgs.metrics" -}}
- extra_filters
- extra_filters[]
{{- end -}}
{{- define "observability-proxy.filterArgs.logs" -}}
- extra_stream_filters
{{- end -}}
{{- define "observability-proxy.filterPlaceholder.metrics" -}}{{ "{{.MetricsExtraFilters}}" }}{{- end -}}
{{- define "observability-proxy.filterPlaceholder.logs" -}}{{ "{{.LogsExtraStreamFilters}}" }}{{- end -}}

{{- define "observability-proxy.readQueryArgs.metrics" -}}
{{- range $i, $arg := (fromYamlArray (include "observability-proxy.filterArgs.metrics" .)) -}}
{{ if $i }}
{{ end -}}
- name: {{ $arg }}
  values:
    - {{ include "observability-proxy.filterPlaceholder.metrics" $ | quote }}
{{- end -}}
{{- end -}}

{{- define "observability-proxy.readQueryArgs.logs" -}}
{{- range $i, $arg := (fromYamlArray (include "observability-proxy.filterArgs.logs" .)) -}}
{{ if $i }}
{{ end -}}
- name: {{ $arg }}
  values:
    - {{ include "observability-proxy.filterPlaceholder.logs" $ | quote }}
{{- end -}}
{{- end -}}

{{- /*
The subset of `readPaths.metrics` a `tenancy.principals[].metricsQueryOnly`
reader gets: the two query endpoints and nothing else this store's own
full reader list also carries — no series, no labels, no label values,
no tsdb status, no vmui, and (gated the same way as the full list)
`/api/v1/metadata` never at all, because a query-only reader has less
reason for it than a full one does, not more.
*/}}
{{- define "observability-proxy.readPaths.metrics.queryOnly" -}}
- /prometheus/api/v1/query
- /prometheus/api/v1/query_range
{{- end -}}

{{- define "observability-proxy.readPaths.logs" -}}
- /select/logsql/.*
- /select/vmui.*
{{- end -}}

{{- define "observability-proxy.readPaths.traces" -}}
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
{{- define "observability-proxy.readPaths.alerts" -}}
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
{{- define "observability-proxy.readPaths.vmalertAPI" -}}
- /prometheus/vmalert/api/v1/alerts
- /prometheus/vmalert/api/v1/rules
{{- end -}}

{{- define "observability-proxy.writePaths.metrics" -}}
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
{{- define "observability-proxy.writePaths.logs" -}}
- /insert/datadog/api/v2/logs
- /insert/elasticsearch/_bulk
- /insert/journald/upload
- /insert/jsonline
- /insert/loki/api/v1/push
- /insert/native
- /insert/opentelemetry/v1/logs
- /insert/splunk
{{- end -}}

{{- define "observability-proxy.writePaths.traces" -}}
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
{{- define "observability-proxy.writeQueryArgs.metrics" -}}
{{- if .cluster -}}
- name: extra_label
  values:
    - {{ printf "%s=%s" .label .cluster | quote }}
{{- end -}}
{{- end -}}

{{- define "observability-proxy.writeQueryArgs.logs" -}}
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
{{- define "observability-proxy.writeQueryArgs.traces" -}}
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
{{- define "observability-proxy.filter" -}}
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
{{- define "observability-proxy.logsFilter" -}}
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
How the proxy authenticates to a store.

The stores demand basic auth from everything, including the proxy in
front of them: a network policy is a rule about who may connect, and this
is a rule about who may read. Both, because each one is a different
mistake to make.
*/}}
{{- define "observability-proxy.targetAuth" -}}
username:
  name: {{ .Values.storeCredentials.secretName | quote }}
  key: {{ .Values.storeCredentials.usernameKey | quote }}
password:
  name: {{ .Values.storeCredentials.secretName | quote }}
  key: {{ .Values.storeCredentials.passwordKey | quote }}
{{- end -}}

{{/*
A duration string, in seconds — for comparing two of them, which Helm has
no other way to do. Scoped to the unit suffixes `values.schema.json`
accepts (ms, s, m, h, d, w, y); a bare number never reaches here because
the schema already refuses one.
*/}}
{{- define "observability-proxy.durationSeconds" -}}
{{- $d := toString . -}}
{{- $num := regexReplaceAll "^([0-9]+).*$" $d "${1}" | int64 -}}
{{- $unit := regexReplaceAll "^[0-9]+(.*)$" $d "${1}" -}}
{{- $perUnit := dict "ms" 0 "s" 1 "m" 60 "h" 3600 "d" 86400 "w" 604800 "y" 31536000 -}}
{{- mul $num (index $perUnit $unit) -}}
{{- end -}}

{{/*
The pair (docs/high-availability.md).

`ha` is the switch. The PRIMARY is `mode: full` with `ha: true`: it owns the
proxy, the vmalerts and everything else, and knows the other release's
stores by address (`pair.peer`). A REPLICA is `mode: replica`: the three
stores and nothing else. `observability-proxy.ha.primary` is "true" on the
first and empty on every other install, so a template can gate on it.
*/}}
{{- define "observability-proxy.ha.primary" -}}
{{- if and (include "observability-proxy.ha" . | fromYaml).enabled (eq (.Values.mode | default "full") "full") -}}true{{- end -}}
{{- end -}}

{{/*
`ha`, normalised. The key has two spellings (values.yaml, `ha`): the
boolean this chart shipped first, which still only asks for two zones, and
the object, which is the pair. Returned as YAML with every field present:
`enabled` (the pair is on: only the object with `enabled: true`),
`legacy` (the boolean was true), `name`, `replica`, `peer.{metrics,logs,
traces}`. Parse with `fromYaml`.
*/}}
{{- define "observability-proxy.ha" -}}
{{- $ha := .Values.ha -}}
{{- $out := dict "enabled" false "legacy" false "name" "observability" "replica" "" "peer" (dict "metrics" "" "logs" "" "traces" "") -}}
{{- if kindIs "map" $ha -}}
{{- $_ := set $out "enabled" ($ha.enabled | default false) -}}
{{- $_ := set $out "name" ($ha.name | default "observability") -}}
{{- $_ := set $out "replica" ($ha.replica | default "") -}}
{{- $peer := $ha.peer | default dict -}}
{{- $_ := set $out "peer" (dict "metrics" ($peer.metrics | default "") "logs" ($peer.logs | default "") "traces" ($peer.traces | default "")) -}}
{{- else -}}
{{- $_ := set $out "legacy" (eq (toString $ha) "true") -}}
{{- end -}}
{{- toYaml $out -}}
{{- end -}}

{{/*
A store's address list for a read route: this release's own first, the
peer's second, only under `ha`. Rendered as the lines of a `static:` block,
so the single install keeps its one `url:` line, byte for byte.
Arguments: (root, this release's url, the peer's url).
*/}}
{{- define "observability-proxy.staticTarget" -}}
{{- $root := index . 0 -}}
{{- $own := index . 1 -}}
{{- $peer := index . 2 -}}
{{- if and (include "observability-proxy.ha.primary" $root) $peer -}}
urls:
  - {{ $own | quote }}
  - {{ $peer | quote }}
{{- else -}}
url: {{ $own | quote }}
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
{{- define "observability-proxy.resources" -}}
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
{{- define "observability-proxy.restrictedSecurityContext" -}}
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
tenancy-authz: names, addresses and the one configuration both it and the
VMUsers are rendered from.
*/}}
{{- define "observability-proxy.authz.name" -}}
{{- printf "%s-tenancy-authz" (include "observability-proxy.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "observability-proxy.authz.signingSecretName" -}}
{{- $a := .Values.tenancy.authz -}}
{{- if eq $a.signingKey.source "existingSecret" -}}
{{- $a.signingKey.existingSecret -}}
{{- else -}}
{{- printf "%s-signing" (include "observability-proxy.authz.name" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}

{{/*
The service's own issuer: the address vmauth fetches discovery and keys
from, and the `iss` of every token it mints. `<service>.<namespace>.svc`,
the short form every address in this chart uses, with no path and no
trailing slash because vmauth compares it as a string.
*/}}
{{- define "observability-proxy.authz.issuerUrl" -}}
{{- printf "http://%s.%s.svc:8080" (include "observability-proxy.authz.name" .) .Release.Namespace -}}
{{- end -}}

{{/*
The audiences that mark a token as a person's: the explicit list, else
`tenancy.audience`. Rendered as `{audiences: [...]}` so a caller can
`fromYaml` it.
*/}}
{{- define "observability-proxy.authz.personAudiences" -}}
{{- $t := .Values.tenancy -}}
{{- $list := $t.authz.personAudiences -}}
{{- if not $list -}}
{{- $list = ternary (list $t.audience) (list) (ne (toString $t.audience) "") -}}
{{- end -}}
{{- toYaml (dict "audiences" $list) -}}
{{- end -}}

{{/*
The principals tenancy-authz unions, as `{principals: [...]}`: the SAME
`tenancy.principals` the VMUsers render from, in the shape pkg/tenancy.Config
reads — and rendered the way templates/vmusers.yaml renders a principal's
grants (one per cluster, the last of a repeated cluster winning, sorted by
cluster, namespaces sorted), so what the service grants and what a VMUser
grants cannot differ by an edit to one of them.

Only the principals of the PEOPLE's path: those whose audience (their own,
else `tenancy.audience`) is a person audience. A principal pinned to a
workload's audience is not the service's to apply — a token for it is passed
through to vmauth untouched — and putting it in would let a person who
happens to hold that group read what the workload reads.
*/}}
{{- define "observability-proxy.authz.principals" -}}
{{- $t := .Values.tenancy -}}
{{- $person := include "observability-proxy.authz.personAudiences" . | fromYaml -}}
{{- $out := list -}}
{{- range $p := (include "observability-proxy.tenancy.principals" $ | fromYaml).principals -}}
{{- $aud := $p.audience | default $t.audience -}}
{{- if has $aud $person.audiences -}}
{{- $byCluster := dict -}}
{{- range $grant := $p.grants -}}
{{- $_ := set $byCluster $grant.cluster $grant -}}
{{- end -}}
{{- $grants := list -}}
{{- range $cluster := (keys $byCluster | sortAlpha) -}}
{{- $g := index $byCluster $cluster -}}
{{- if $g.allNamespaces -}}
{{- $grants = append $grants (dict "cluster" $cluster "allNamespaces" true) -}}
{{- else -}}
{{- $grants = append $grants (dict "cluster" $cluster "namespaces" (sortAlpha $g.namespaces)) -}}
{{- end -}}
{{- end -}}
{{- $entry := dict "grants" $grants -}}
{{- if $p.groups -}}
{{- $_ := set $entry "name" $p.name -}}
{{- $_ := set $entry "groups" $p.groups -}}
{{- else -}}
{{- $_ := set $entry "group" $p.group -}}
{{- end -}}
{{- $out = append $out $entry -}}
{{- end -}}
{{- end -}}
{{- toYaml (dict "principals" $out) -}}
{{- end -}}

{{/*
The clusters the trace store serves, for tenancy-authz: `authz.traceClusters`
when set, else every cluster named by a grant of any person principal. A
person reads traces only with allNamespaces on EVERY one of them.
*/}}
{{- define "observability-proxy.authz.traceClusters" -}}
{{- $a := .Values.tenancy.authz -}}
{{- if $a.traceClusters -}}
{{- toYaml (dict "clusters" (sortAlpha (uniq $a.traceClusters))) -}}
{{- else -}}
{{- $set := dict -}}
{{- range $p := (include "observability-proxy.authz.principals" . | fromYaml).principals -}}
{{- range $g := $p.grants -}}
{{- $_ := set $set $g.cluster true -}}
{{- end -}}
{{- end -}}
{{- toYaml (dict "clusters" (keys $set | sortAlpha)) -}}
{{- end -}}
{{- end -}}

{{/*
The whole configuration file of tenancy-authz.
*/}}
{{- define "observability-proxy.authz.config" -}}
{{- $t := .Values.tenancy -}}
{{- $a := $t.authz -}}
{{- $cfg := dict
    "claimName" $t.claimName
    "clusterLabel" $t.clusterLabel
    "namespaceLabel" $t.namespaceLabel
    "logsClusterField" $t.logsClusterField
    "logsNamespaceField" $t.logsNamespaceField
    "principals" (include "observability-proxy.authz.principals" . | fromYaml).principals
    "authz" (dict
      "upstreamIssuer" ($a.upstreamIssuer | default $t.issuerUrl)
      "personAudiences" (include "observability-proxy.authz.personAudiences" . | fromYaml).audiences
      "issuerURL" (include "observability-proxy.authz.issuerUrl" .)
      "mintedAudience" $a.mintedAudience
      "tokenTTL" $a.tokenTTL
      "traceClusters" (include "observability-proxy.authz.traceClusters" . | fromYaml).clusters
      "signingKey" (dict "file" "/etc/tenancy-authz/signing/privateKey")) -}}
{{- toYaml $cfg -}}
{{- end -}}

{{/*
`tenancy.identities` (a generic list of "groups -> reach") and the older
`tenancy.principals`, as ONE list of principals: `{principals: [...]}`, in the
shape of `tenancy.principals` (so every consumer of that list renders an
identity exactly as it renders the principal written out by hand).

An identity `{name, groups, reach, audience, workload}` becomes the principal
`{name, groups, grants, audience, routes, metricsQueryOnly, vmalertAPI}` (the last
four only when set; docs/tenancy-authz.md has the table). Reach
`clusters: [{cluster, namespaces | allNamespaces}]` is the grants one for one; reach `wholeStore: true` is `allNamespaces` on each
cluster of `tenancy.storeClusters`. Its refusals run first, here, so that no
template ever works on an identity the validator has not read.
*/}}
{{- define "observability-proxy.tenancy.principals" -}}
{{- include "observability-proxy.validate.identities" . -}}
{{- $t := .Values.tenancy -}}
{{- $out := list -}}
{{- range $p := ($t.principals | default list) -}}
{{- $out = append $out $p -}}
{{- end -}}
{{- range $id := ($t.identities | default list) -}}
{{- $reach := $id.reach | default dict -}}
{{- $grants := list -}}
{{- if $reach.wholeStore -}}
{{- range $c := ($t.storeClusters | default list) -}}
{{- $grants = append $grants (dict "cluster" $c "allNamespaces" true) -}}
{{- end -}}
{{- else -}}
{{- range $c := ($reach.clusters | default list) -}}
{{- if $c.allNamespaces -}}
{{- $grants = append $grants (dict "cluster" $c.cluster "allNamespaces" true) -}}
{{- else -}}
{{- $grants = append $grants (dict "cluster" $c.cluster "namespaces" ($c.namespaces | default list)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $entry := dict "grants" $grants -}}
{{- /* No `name` is the single-`group` principal (validated: one group). */ -}}
{{- if $id.name -}}
{{- $_ := set $entry "name" $id.name -}}
{{- $_ := set $entry "groups" $id.groups -}}
{{- else -}}
{{- $_ := set $entry "group" (first $id.groups) -}}
{{- end -}}
{{- if hasKey $id "audience" -}}
{{- $_ := set $entry "audience" $id.audience -}}
{{- end -}}
{{- $w := $id.workload | default dict -}}
{{- if $w.routes -}}
{{- $_ := set $entry "routes" $w.routes -}}
{{- end -}}
{{- if $w.metricsQueryOnly -}}
{{- $_ := set $entry "metricsQueryOnly" true -}}
{{- end -}}
{{- if $w.vmalertAPI -}}
{{- $_ := set $entry "vmalertAPI" true -}}
{{- end -}}
{{- $out = append $out $entry -}}
{{- end -}}
{{- toYaml (dict "principals" $out) -}}
{{- end -}}

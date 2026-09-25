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

{{- define "observability-stack.readPaths.logs" -}}
- /select/logsql/.*
- /select/vmui.*
{{- end -}}

{{- define "observability-stack.readPaths.traces" -}}
- /select/jaeger/.*
- /select/tempo/.*
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

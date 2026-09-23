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
- /prometheus/api/v1/metadata
- /prometheus/api/v1/status/[^/]+
- /prometheus/vmui.*
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

{{- define "observability-stack.writePaths.logs" -}}
- /insert/.*
{{- end -}}

{{- define "observability-stack.writePaths.traces" -}}
- /insert/opentelemetry/v1/traces
{{- end -}}

{{/*
One grant, as a series selector. The same string for MetricsQL and for
LogsQL, which is only possible because this design restricts itself to
equality and alternation on stream labels — see pkg/tenancy.
*/}}
{{- define "observability-stack.filter" -}}
{{- $env := printf "%s=%q" .labels.env .grant.env -}}
{{- if .grant.allTenants -}}
{{- printf "{%s}" $env -}}
{{- else -}}
{{- printf "{%s,%s=~\"^(%s)$\"}" $env .labels.tenant (join "|" (sortAlpha .grant.tenants)) -}}
{{- end -}}
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

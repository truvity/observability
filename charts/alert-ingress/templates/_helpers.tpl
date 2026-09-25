{{/*
Names and labels for the objects this chart renders. One workload, so one
name and one selector — unlike observability-emitters, which names three.
*/}}
{{- define "alert-ingress.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "alert-ingress.fullname" -}}
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

{{- define "alert-ingress.labels" -}}
app.kubernetes.io/name: {{ include "alert-ingress.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{- define "alert-ingress.selectorLabels" -}}
app.kubernetes.io/name: {{ include "alert-ingress.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
Doubles a MetricsQL duration of the shape values.schema.json's `duration`
pattern requires: a bare number and a unit, nothing combined. The VMRule
needs `2 × heartbeat.interval` as a literal duration INSIDE a range
selector (`foo[30m]`), and a range selector cannot hold an expression —
`foo[2 * 15m]` is not valid MetricsQL, only `foo[30m]` is. So the
doubling happens here, once, in the one place that has both numbers, and
the VMRule spends a plain string.
*/}}
{{- define "alert-ingress.doubleDuration" -}}
{{- $num := regexFind "^[0-9]+" . | int -}}
{{- $unit := regexFind "[a-z]+$" . -}}
{{- printf "%d%s" (mul $num 2) $unit -}}
{{- end -}}

{{/*
The mounted configuration: exactly the four values the binary reads (see
cmd/alert-ingress/config.go), rendered once and shared by the ConfigMap
and the Deployment's own checksum annotation so the two can never drift.
*/}}
{{- define "alert-ingress.config" -}}
alertmanager:
  url: {{ .Values.alertmanager.url | quote }}
topics:
  {{- toYaml .Values.topics | nindent 2 }}
mappings:
  {{- toYaml (.Values.mappings | default list) | nindent 2 }}
heartbeat:
  match:
    {{- toYaml .Values.heartbeat.match | nindent 4 }}
  interval: {{ .Values.heartbeat.interval | quote }}
resolveAfter: {{ .Values.resolveAfter | quote }}
{{- end -}}

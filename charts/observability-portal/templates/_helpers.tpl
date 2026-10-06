{{- define "observability-portal.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "observability-portal.fullname" -}}
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

{{- define "observability-portal.selectorLabels" -}}
app.kubernetes.io/name: {{ include "observability-portal.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "observability-portal.labels" -}}
{{ include "observability-portal.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- with .Values.labels }}
{{ toYaml . }}
{{- end }}
{{- end -}}

{{- define "observability-portal.image" -}}
{{- printf "%s:%s" .Values.image.repository (default .Chart.AppVersion .Values.image.tag) -}}
{{- end -}}

{{/*
The document the page loads from /config/portal.json: exactly the keys that
were set, so a default install renders {"version": 1}. `extraEntries` is the
file's `entries`. Its shape is apps/portal/schema/portal.schema.json, and
tests/portal_config_test.go holds the render to it.
*/}}
{{- define "observability-portal.config" -}}
{{- $p := .Values.portal -}}
{{- $c := dict "version" 1 -}}
{{- if $p.title }}{{ $_ := set $c "title" $p.title }}{{ end -}}
{{- if $p.lede }}{{ $_ := set $c "lede" $p.lede }}{{ end -}}
{{- if $p.issuer }}{{ $_ := set $c "issuer" $p.issuer }}{{ end -}}
{{- if $p.replaceDefaults }}{{ $_ := set $c "replaceDefaults" true }}{{ end -}}
{{- if $p.tierOrder }}{{ $_ := set $c "tierOrder" $p.tierOrder }}{{ end -}}
{{- if $p.extraEntries }}{{ $_ := set $c "entries" $p.extraEntries }}{{ end -}}
{{- if $p.orientation }}{{ $_ := set $c "orientation" $p.orientation }}{{ end -}}
{{- if $p.commandLine }}{{ $_ := set $c "commandLine" $p.commandLine }}{{ end -}}
{{- if $p.guides }}{{ $_ := set $c "guides" $p.guides }}{{ end -}}
{{- $c | toPrettyJson -}}
{{- end -}}

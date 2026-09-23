{{/* The rendered VMRule object's name. */}}
{{- define "platform-alerts.fullname" -}}
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

{{/*
Every enabled group that needs `stores`, so one refusal can name them all.
*/}}
{{- define "platform-alerts.storeGroups" -}}
{{- $needing := list -}}
{{- if .Values.groups.writePath.enabled -}}{{- $needing = append $needing "writePath" -}}{{- end -}}
{{- if .Values.groups.storeLimits.enabled -}}{{- $needing = append $needing "storeLimits" -}}{{- end -}}
{{- join ", " $needing -}}
{{- end -}}

{{/*
A rule's labels: the estate's common labels with this rule's severity on
top, so `severity` can never be shadowed by a common label.
*/}}
{{- define "platform-alerts.labels" -}}
{{- $severity := .severity -}}
{{- $labels := merge (dict "severity" $severity) (deepCopy .root.Values.commonLabels) -}}
{{- toYaml $labels -}}
{{- end -}}

{{/*
A rule's runbook link, or nothing when no base URL is configured. An
annotation with an empty value reads as a broken link, so it is omitted
rather than rendered blank.
*/}}
{{- define "platform-alerts.runbook" -}}
{{- if .root.Values.runbookBaseUrl -}}
runbook_url: {{ printf "%s/%s" (trimSuffix "/" .root.Values.runbookBaseUrl) .alert | quote }}
{{- end -}}
{{- end -}}

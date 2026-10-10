{{- define "observability-cluster.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- /* The label every Grafana-addressed resource of this cluster carries, and the one a component's GrafanaDashboard selects on. */ -}}
{{- define "observability-cluster.instanceLabels" -}}
observability.truvity.io/grafana: shared
{{- end -}}

{{- define "observability-cluster.labels" -}}
app.kubernetes.io/name: {{ include "observability-cluster.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: observability
helm.sh/chart: {{ printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end -}}

{{- define "observability-cluster.folderUid" -}}
{{- default .Values.cluster .Values.folder.uid -}}
{{- end -}}

{{- define "observability-cluster.folderTitle" -}}
{{- default .Values.cluster .Values.folder.title -}}
{{- end -}}

{{- define "observability-cluster.validate" -}}
{{- if .Values.externalGrafana.enabled -}}
{{- if not .Values.externalGrafana.url -}}
{{- fail "observability-cluster: externalGrafana.enabled needs externalGrafana.url (the shared Grafana's root URL)" -}}
{{- end -}}
{{- if not .Values.externalGrafana.credentials.secretName -}}
{{- fail "observability-cluster: externalGrafana.enabled needs externalGrafana.credentials.secretName (the Secret holding this cluster's Grafana service account token)" -}}
{{- end -}}
{{- if and .Values.externalGrafana.caSecret.namespace (not .Values.externalGrafana.caSecret.name) -}}
{{- fail "observability-cluster: externalGrafana.caSecret.namespace is set without externalGrafana.caSecret.name" -}}
{{- end -}}
{{- if .Values.folder.enabled -}}
{{- if not (include "observability-cluster.folderUid" .) -}}
{{- fail "observability-cluster: the folder needs a uid: set cluster (or folder.uid)" -}}
{{- end -}}
{{- if not (regexMatch "^[A-Za-z0-9_-]{1,40}$" (include "observability-cluster.folderUid" .)) -}}
{{- fail (printf "observability-cluster: the folder uid %q must be 1 to 40 characters of letters, digits, '-' and '_'" (include "observability-cluster.folderUid" .)) -}}
{{- end -}}
{{- end -}}
{{- else -}}
{{- if .Values.folder.title -}}
{{- fail "observability-cluster: folder.title is set but externalGrafana.enabled is false: the folder lives in the shared Grafana" -}}
{{- end -}}
{{- if .Values.folder.uid -}}
{{- fail "observability-cluster: folder.uid is set but externalGrafana.enabled is false: the folder lives in the shared Grafana" -}}
{{- end -}}
{{- end -}}
{{- end -}}

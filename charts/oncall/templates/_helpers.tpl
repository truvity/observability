{{/*
Names and labels. Two workloads, so two names and two selectors, each with
the component label: a NetworkPolicy that names GoAlert must never also
select the relay.
*/}}
{{- define "oncall.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "oncall.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 50 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 50 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 50 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "oncall.goalertName" -}}
{{- printf "%s-goalert" (include "oncall.fullname" .) -}}
{{- end -}}

{{- define "oncall.relayName" -}}
{{- printf "%s-telegram-relay" (include "oncall.fullname" .) -}}
{{- end -}}

{{- define "oncall.labels" -}}
app.kubernetes.io/name: {{ include "oncall.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: oncall
{{- end -}}

{{/* Selector of every pod this release renders (the default-deny). */}}
{{- define "oncall.releaseSelector" -}}
app.kubernetes.io/name: {{ include "oncall.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{- define "oncall.goalertSelector" -}}
{{ include "oncall.releaseSelector" . }}
app.kubernetes.io/component: goalert
{{- end -}}

{{- define "oncall.relaySelector" -}}
{{ include "oncall.releaseSelector" . }}
app.kubernetes.io/component: telegram-relay
{{- end -}}

{{- define "oncall.goalertImage" -}}
{{- $i := .Values.goalert.image -}}
{{- if $i.digest -}}
{{- printf "%s@%s" $i.repository $i.digest -}}
{{- else -}}
{{- printf "%s:%s" $i.repository $i.tag -}}
{{- end -}}
{{- end -}}

{{/*
image.tag empty resolves to .Chart.AppVersion: the release workflow stamps
the chart and the image from the same tag.
*/}}
{{- define "oncall.relayImage" -}}
{{- printf "%s:%s" .Values.relay.image.repository (default .Chart.AppVersion .Values.relay.image.tag) -}}
{{- end -}}

{{/*
GoAlert's PostgreSQL URL, with no password in it: the password is
PGPASSWORD, which the PostgreSQL driver reads from the environment for any
URL that does not carry one.
*/}}
{{- define "oncall.dbURL" -}}
{{- $d := .Values.goalert.database -}}
{{- $q := list (printf "sslmode=%s" $d.sslMode) -}}
{{- if or $d.ca.configMap $d.ca.secret -}}
{{- $q = append $q (printf "sslrootcert=/etc/oncall/db-ca/%s" $d.ca.key) -}}
{{- end -}}
{{- printf "postgres://%s@%s:%d/%s?%s" ($d.user | urlquery) $d.host ($d.port | int) ($d.name | urlquery) (join "&" $q) -}}
{{- end -}}

{{/*
The environment GoAlert's main container and its init containers share:
the same database, the same key.
*/}}
{{- define "oncall.goalertEnv" -}}
- name: GOALERT_DB_URL
  value: {{ include "oncall.dbURL" . | quote }}
- name: PGPASSWORD
  valueFrom:
    secretKeyRef:
      name: {{ .Values.goalert.database.passwordSecret.name | quote }}
      key: {{ .Values.goalert.database.passwordSecret.key | quote }}
- name: GOALERT_DATA_ENCRYPTION_KEY
  valueFrom:
    secretKeyRef:
      name: {{ .Values.goalert.dataEncryptionKeySecret.name | quote }}
      key: {{ .Values.goalert.dataEncryptionKeySecret.key | quote }}
- name: GOALERT_JSON
  value: "true"
{{- end -}}

{{- define "oncall.restrictedContainer" -}}
allowPrivilegeEscalation: false
readOnlyRootFilesystem: true
runAsNonRoot: true
capabilities:
  drop: ["ALL"]
{{- end -}}

{{- define "oncall.restrictedPod" -}}
runAsNonRoot: true
runAsUser: 65532
runAsGroup: 65532
fsGroup: 65532
seccompProfile:
  type: RuntimeDefault
{{- end -}}

{{/* One NetworkPolicy egress rule for DNS. */}}
{{- define "oncall.dnsEgress" -}}
# DNS. With no `networkPolicy.dns`, port 53 to any destination.
{{- with .Values.networkPolicy.dns }}
- to:
    {{- toYaml . | nindent 4 }}
  ports:
    - protocol: UDP
      port: 53
    - protocol: TCP
      port: 53
{{- else }}
- ports:
    - protocol: UDP
      port: 53
    - protocol: TCP
      port: 53
{{- end }}
{{- end -}}

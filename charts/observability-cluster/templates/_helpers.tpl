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
{{- /* The operator's own chart can render its CRDs as templates (crds.immutable: false). They are observability-crds' to apply, server-side; a second owner would overwrite the first. */ -}}
{{- if and .Values.grafanaOperator.enabled (not (dig "crds" "immutable" true (index .Values "grafana-operator"))) -}}
{{- fail "observability-cluster: grafana-operator.crds.immutable is false, which makes the operator's chart render the Grafana CRDs as templates of this release. They belong to observability-crds (sets.grafanaOperator), applied server-side before this release; leave grafana-operator.crds.immutable at its default" -}}
{{- end -}}
{{- $op := index .Values "grafana-operator" -}}
{{- $sx := .Values.externalGrafana.auth.sluisExchange -}}
{{- if and .Values.grafanaOperator.enabled (not $sx.enabled) (dig "kubeAuth" "volume" dict $op) -}}
{{- fail "observability-cluster: grafana-operator.kubeAuth.volume is set (presets/operator-sluis-exchange.yaml) but externalGrafana.auth.sluisExchange.enabled is false: the token sidecar would find no ConfigMap and the operator no token" -}}
{{- end -}}
{{- if $sx.enabled -}}
{{- if not .Values.externalGrafana.enabled -}}
{{- fail "observability-cluster: externalGrafana.auth.sluisExchange.enabled needs externalGrafana.enabled (the exchange authenticates the Grafana resource's operator)" -}}
{{- end -}}
{{- if not $sx.issuerUrl -}}
{{- fail "observability-cluster: externalGrafana.auth.sluisExchange.enabled needs externalGrafana.auth.sluisExchange.issuerUrl (the issuer's base URL)" -}}
{{- end -}}
{{- if not $sx.clientId -}}
{{- fail "observability-cluster: externalGrafana.auth.sluisExchange.clientId must not be empty (the exchange client)" -}}
{{- end -}}
{{- if .Values.externalGrafana.credentials.secretName -}}
{{- fail "observability-cluster: externalGrafana.credentials.secretName is set together with externalGrafana.auth.sluisExchange.enabled: the operator authenticates one way; drop one" -}}
{{- end -}}
{{- if .Values.grafanaOperator.enabled -}}
{{- /* The subchart default (operator.grafana.com) is not an audience any issuer expects. */ -}}
{{- if eq (dig "kubeAuth" "audience" "operator.grafana.com" $op) "operator.grafana.com" -}}
{{- fail "observability-cluster: externalGrafana.auth.sluisExchange.enabled with grafanaOperator.enabled needs grafana-operator.kubeAuth.audience: the audience of the ServiceAccount token the issuer expects (upstream's default, operator.grafana.com, is refused)" -}}
{{- end -}}
{{- $named := false -}}
{{- range (dig "extraContainers" list $op) -}}{{- if eq (.name | default "") "token-exchange" -}}{{- $named = true -}}{{- end -}}{{- end -}}
{{- if or (not (dig "kubeAuth" "volume" dict $op)) (not $named) -}}
{{- fail "observability-cluster: externalGrafana.auth.sluisExchange.enabled with grafanaOperator.enabled needs the operator pod's half: list presets/operator-sluis-exchange.yaml before your values (it sets grafana-operator.kubeAuth.volume and the token-exchange container; docs/presets.md)" -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if .Values.externalGrafana.enabled -}}
{{- if not .Values.externalGrafana.url -}}
{{- fail "observability-cluster: externalGrafana.enabled needs externalGrafana.url (the shared Grafana's root URL)" -}}
{{- end -}}
{{- if and (not .Values.externalGrafana.credentials.secretName) (not $sx.enabled) -}}
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

{{- /* refreshEvery ("20m") in seconds. */ -}}
{{- define "observability-cluster.refreshSeconds" -}}
{{- $v := .Values.externalGrafana.auth.sluisExchange.refreshEvery -}}
{{- $n := int (regexFind "^[0-9]+" $v) -}}
{{- if hasSuffix "h" $v -}}{{- mul $n 3600 -}}{{- else if hasSuffix "m" $v -}}{{- mul $n 60 -}}{{- else -}}{{- $n -}}{{- end -}}
{{- end -}}

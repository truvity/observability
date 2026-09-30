{{/*
Every helper here reads `.Values.global.observabilityGrafana`, and that is
the point: the Grafana subchart evaluates its `tpl` strings with ITS OWN
root, where the parent's values do not exist but `global` does. So the same
helper renders the same answer whether this chart's templates call it or
the subchart's values do (values.yaml).
*/}}

{{/*
The provisioning file: one datasource set per store.

Per store, in this order: the VictoriaMetrics plugin type (Explore,
MetricsQL), the SAME URL again through Grafana's built-in `prometheus` type
(every community dashboard lists only prometheus-typed datasources, so
without this row each panel says "No data"), the logs datasource if wanted,
and the traces one if wanted. `isDefault` is on the prometheus-typed row of
the one default store, because a dashboard's datasource variable starts on
"default" and lists only prometheus-typed rows.

Every row forwards the signed-in person's own token (`oauthPassThru`), so
the store's proxy, not Grafana, is the boundary.
*/}}
{{- define "observability-grafana.datasourcesDoc" -}}
{{- $c := .Values.global.observabilityGrafana -}}
{{- $rows := list -}}
{{- range $s := $c.stores -}}
{{- $ca := default $c.caFile $s.caFile -}}
{{- $url := trimSuffix "/" $s.url -}}
{{- $json := dict "oauthPassThru" true -}}
{{- $secure := dict -}}
{{- if $ca -}}
{{- $_ := set $json "tlsAuthWithCACert" true -}}
{{- $_ := set $secure "tlsCACert" (printf "$__file{%s}" $ca) -}}
{{- end -}}
{{- $metrics := dict "name" (printf "Metrics (%s)" $s.name) "type" "victoriametrics-metrics-datasource" "uid" (printf "%s-metrics" $s.name) "url" (printf "%s/prometheus" $url) "access" "proxy" "jsonData" (deepCopy $json) -}}
{{- $prom := dict "name" (printf "Prometheus (%s)" $s.name) "type" "prometheus" "uid" (printf "%s-prom" $s.name) "url" (printf "%s/prometheus" $url) "access" "proxy" "jsonData" (merge (dict "httpMethod" "POST" "prometheusType" "Prometheus") (deepCopy $json)) -}}
{{- if $s.default -}}
{{- $_ := set $prom "isDefault" true -}}
{{- end -}}
{{- if $ca -}}
{{- $_ := set $metrics "secureJsonData" (deepCopy $secure) -}}
{{- $_ := set $prom "secureJsonData" (deepCopy $secure) -}}
{{- end -}}
{{- $rows = concat $rows (list $metrics $prom) -}}
{{- if or (not (hasKey $s "logs")) $s.logs -}}
{{- /* THE ROOT, not the query path: the logs plugin appends
       /select/logsql/query itself. */ -}}
{{- $logs := dict "name" (printf "Logs (%s)" $s.name) "type" "victoriametrics-logs-datasource" "uid" (printf "%s-logs" $s.name) "url" $url "access" "proxy" "jsonData" (deepCopy $json) -}}
{{- if $ca -}}
{{- $_ := set $logs "secureJsonData" (deepCopy $secure) -}}
{{- end -}}
{{- $rows = append $rows $logs -}}
{{- end -}}
{{- $t := $s.traces | default dict -}}
{{- if or (not (hasKey $t "enabled")) $t.enabled -}}
{{- $traces := dict "name" (printf "Traces (%s)" $s.name) "type" "jaeger" "uid" (printf "%s-traces" $s.name) "url" (printf "%s/select/jaeger" $url) "access" "proxy" "jsonData" (deepCopy $json) -}}
{{- if $ca -}}
{{- $_ := set $traces "secureJsonData" (deepCopy $secure) -}}
{{- end -}}
{{- if $t.bearerEnv -}}
{{- /* A fixed credential in place of the person's token: unscoped. */ -}}
{{- $_ := unset (get $traces "jsonData") "oauthPassThru" -}}
{{- $_ := set (get $traces "jsonData") "httpHeaderName1" "Authorization" -}}
{{- $sj := get $traces "secureJsonData" | default dict -}}
{{- $_ := set $sj "httpHeaderValue1" (printf "Bearer $__env{%s}" $t.bearerEnv) -}}
{{- $_ := set $traces "secureJsonData" $sj -}}
{{- end -}}
{{- $rows = append $rows $traces -}}
{{- end -}}
{{- end -}}
{{- $doc := dict "apiVersion" 1 "datasources" $rows -}}
{{- if $c.renamedDatasources -}}
{{- $del := list -}}
{{- range $n := $c.renamedDatasources -}}
{{- $del = append $del (dict "name" $n "orgId" 1) -}}
{{- end -}}
{{- $_ := set $doc "deleteDatasources" $del -}}
{{- end -}}
{{- toYaml $doc -}}
{{- end -}}

{{/*
The datasources ConfigMap's name carries a hash of its content. The
subchart hashes only its own ConfigMap into the pod template, so a change
to THIS one would otherwise roll nothing, and provisioning runs at start.
*/}}
{{- define "observability-grafana.datasourcesName" -}}
{{- printf "%s-datasources-%s" .Release.Name (include "observability-grafana.datasourcesDoc" . | sha256sum | trunc 8) -}}
{{- end -}}

{{/*
GF_SECURITY_SECRET_KEY is an `optional` reference, so an install that
knowingly runs on Grafana's built-in key names no real Secret.
*/}}
{{- define "observability-grafana.secretKeyName" -}}
{{- default "observability-grafana-no-secret-key" .Values.global.observabilityGrafana.secretKeyRef.name -}}
{{- end -}}

{{- define "observability-grafana.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

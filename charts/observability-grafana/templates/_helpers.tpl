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
{{- $t := $s.traces | default dict -}}
{{- $hasLogs := or (not (hasKey $s "logs")) $s.logs -}}
{{- $hasTraces := or (not (hasKey $t "enabled")) $t.enabled -}}
{{- /* Logs and traces are linked only inside ONE store, and only when that
       store provisions both: a link to a uid that does not exist is a
       dead button. `correlate` is on unless set false. */ -}}
{{- $link := and $hasLogs $hasTraces (or (not (hasKey $c "correlate")) $c.correlate) -}}
{{- if $hasLogs -}}
{{- /* THE ROOT, not the query path: the logs plugin appends
       /select/logsql/query itself. */ -}}
{{- $logs := dict "name" (printf "Logs (%s)" $s.name) "type" "victoriametrics-logs-datasource" "uid" (printf "%s-logs" $s.name) "url" $url "access" "proxy" "jsonData" (deepCopy $json) -}}
{{- if $ca -}}
{{- $_ := set $logs "secureJsonData" (deepCopy $secure) -}}
{{- end -}}
{{- if $link -}}
{{- /* OpenTelemetry logs reach VictoriaLogs with the trace id in the
       structured field `trace_id`; the derived field matches that field
       (matcherType label) and opens the same store's traces datasource on
       that id. */ -}}
{{- $_ := set (get $logs "jsonData") "derivedFields" (list (dict "name" "TraceID" "matcherType" "label" "matcherRegex" "trace_id" "url" "$${__value.raw}" "urlDisplayLabel" "Open trace" "datasourceUid" (printf "%s-traces" $s.name))) -}}
{{- end -}}
{{- $rows = append $rows $logs -}}
{{- end -}}
{{- if $hasTraces -}}
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
{{- if $link -}}
{{- /* (`$$` is Grafana provisioning's escape for a literal `$`.)
       Trace to logs: the same store's logs datasource, by trace id, in a
       window around the span. A custom LogsQL query, because the built-in
       filters write LogQL. */ -}}
{{- $_ := set (get $traces "jsonData") "tracesToLogsV2" (dict "datasourceUid" (printf "%s-logs" $s.name) "spanStartTimeShift" "-5m" "spanEndTimeShift" "5m" "filterByTraceID" false "filterBySpanID" false "customQuery" true "query" "trace_id:\"$${__trace.traceId}\"") -}}
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

{{/*
The `[auth.jwt]` section of `global.observabilityGrafana.workloadAuth`, or
nothing.

It is appended to a value INSIDE `[auth]` (see values.yaml), so it begins
with a newline and two spaces of indentation (the ConfigMap is a literal
block whose lines the subchart indents by two), opens `[auth.jwt]`, and ends
by re-opening `[auth]` so the keys that sort after it land where they belong.
Grafana's ini reader (gopkg.in/ini.v1) merges a repeated section.

What each key does, and why it is this value:

  header_name = Authorization   Grafana strips a leading "Bearer " from it
                                (pkg/services/authn/clients/jwt.go), so the
                                proxy's outbound side needs to inject nothing
                                but the standard header. A request whose
                                Authorization is not a JWT with a `sub`
                                (a service-account token, Basic) is not this
                                client's and falls through.
  jwk_set_url                   the issuer's keys; https only.
  expect_claims                 `iss` and `aud` must equal the values set.
                                The audience is the gate.
  username_attribute_path       the login is `workload:<sub>`: a workload has
                                no email, and Grafana needs a login or email;
                                the prefix keeps it from colliding with (and
                                being linked to) a person's existing login.
  role_attribute_path           a CONSTANT expression that yields Viewer
                                (`sub && 'Viewer'`; `sub` is checked to be
                                present by Grafana already). Not read from a
                                claim, so no token can ask for more. It does
                                not begin with a quote, which ini would strip.
  role_attribute_strict         a role that evaluates to nothing is refused.
  allow_assign_grafana_admin    false: the server-admin flag is never set.
  auto_sign_up                  the identity is created on first use.
  url_login, enable_login_token false: no `?auth_token=` and no session
                                cookie; every request carries its own token.
*/}}
{{- define "observability-grafana.workloadAuthIni" -}}
{{- $w := (.Values.global.observabilityGrafana).workloadAuth | default dict -}}
{{- if $w.enabled }}
  [auth.jwt]
  enabled = true
  header_name = Authorization
  jwk_set_url = {{ $w.jwksUrl }}
  cache_ttl = 60m
  expect_claims = {{ dict "iss" $w.issuer "aud" $w.audience | toJson }}
  username_attribute_path = join(':', ['workload', sub])
  role_attribute_path = sub && 'Viewer'
  role_attribute_strict = true
  allow_assign_grafana_admin = false
  skip_org_role_sync = false
  auto_sign_up = true
  url_login = false
  enable_login_token = false
  [auth]
{{- end -}}
{{- end -}}

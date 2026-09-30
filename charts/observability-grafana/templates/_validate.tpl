{{/*
Refusals. Each one names a Grafana that renders and looks fine while being
wrong: a dashboard set that never updates, a home page that shows another
store, a crash-looping pod on a rename. tests/invalid/observability-grafana
holds one fixture per refusal, and hack/lint-fixtures.sh checks each fails
for the reason it is named for.
*/}}
{{- define "observability-grafana.validate" -}}
{{- $c := .Values.global.observabilityGrafana -}}
{{- $g := .Values.grafana | default dict -}}

{{- /* Stores: at least one, one default, no duplicate uid. */ -}}
{{- if not $c.stores -}}
{{- fail "observability-grafana: `global.observabilityGrafana.stores` is empty. A Grafana with no datasource shows an empty console and reports nothing wrong; list at least one store." -}}
{{- end -}}
{{- $uids := dict -}}
{{- $names := list -}}
{{- $defaults := list -}}
{{- range $s := $c.stores -}}
{{- if not $s.name -}}
{{- fail "observability-grafana: a store has no `name`. The name is the prefix of every datasource uid." -}}
{{- end -}}
{{- if not $s.url -}}
{{- fail (printf "observability-grafana: store %q has no `url`. There is nothing for its datasources to read." $s.name) -}}
{{- end -}}
{{- if $s.default -}}
{{- $defaults = append $defaults $s.name -}}
{{- end -}}
{{- $t := $s.traces | default dict -}}
{{- range $suffix := list "prom" "metrics" (ternary "logs" "" (or (not (hasKey $s "logs")) $s.logs)) (ternary "traces" "" (or (not (hasKey $t "enabled")) $t.enabled)) -}}
{{- if $suffix -}}
{{- $uid := printf "%s-%s" $s.name $suffix -}}
{{- if hasKey $uids $uid -}}
{{- fail (printf "observability-grafana: datasource uid %q is produced twice (store %q appears more than once, or two stores collide on it). A repeated uid is refused by the database: the second pod inserts a row, finds the uid taken and crash-loops before it serves. Store names must be unique." $uid $s.name) -}}
{{- end -}}
{{- $_ := set $uids $uid $s.name -}}
{{- end -}}
{{- end -}}
{{- $names = append $names (printf "Metrics (%s)" $s.name) -}}
{{- $names = append $names (printf "Prometheus (%s)" $s.name) -}}
{{- $names = append $names (printf "Logs (%s)" $s.name) -}}
{{- $names = append $names (printf "Traces (%s)" $s.name) -}}
{{- end -}}
{{- if ne (len $defaults) 1 -}}
{{- fail (printf "observability-grafana: %d stores are marked `default: true` (%s); exactly one must be. The default is what a dashboard's datasource variable starts on, and it must be a prometheus-typed row: with none, a dashboard opens on no datasource, and with two Grafana keeps one at random." (len $defaults) (join ", " $defaults)) -}}
{{- end -}}

{{- /* A rename list may not name a datasource that is provisioned now. */ -}}
{{- range $n := $c.renamedDatasources -}}
{{- if has $n $names -}}
{{- fail (printf "observability-grafana: `renamedDatasources` lists %q, which is a datasource this chart provisions now. The list deletes by name BEFORE the inserts, so the datasource would be deleted and recreated on every start, dropping whatever the database attached to it. List only names that no longer exist." $n) -}}
{{- end -}}
{{- end -}}

{{- /* Sign-in. */ -}}
{{- if not $c.oauth.issuer -}}
{{- fail "observability-grafana: `global.observabilityGrafana.oauth.issuer` is empty. It is the OIDC issuer the sign-in URLs derive from, and there is no local login to fall back to (the login form is disabled): nobody could sign in." -}}
{{- end -}}
{{- if not $c.oauth.clientId -}}
{{- fail "observability-grafana: `global.observabilityGrafana.oauth.clientId` is empty. The issuer cannot tell which client is asking, and the login form is disabled: nobody could sign in." -}}
{{- end -}}
{{- if not $c.oauth.clientSecretRef.name -}}
{{- fail "observability-grafana: `global.observabilityGrafana.oauth.clientSecretRef.name` is empty. The client secret is read from a Secret; the pod would name a Secret called \"\", which the API server rejects." -}}
{{- end -}}
{{- if not $c.oauth.roleAttributePath -}}
{{- fail "observability-grafana: `global.observabilityGrafana.oauth.roleAttributePath` is empty. The chart sets `role_attribute_strict`, so a person whose claims map to no role is refused; with no mapping at all that is everybody." -}}
{{- end -}}
{{- if not $c.rootUrl -}}
{{- fail "observability-grafana: `global.observabilityGrafana.rootUrl` is empty. It is the URL the issuer redirects back to after sign-in; wrong or empty, sign-in never completes." -}}
{{- end -}}
{{- $ini := index $g "grafana.ini" | default dict -}}
{{- $oauth := index $ini "auth.generic_oauth" | default dict -}}
{{- if not $oauth.use_refresh_token -}}
{{- fail "observability-grafana: grafana.ini's [auth.generic_oauth] has `use_refresh_token` false. The session then outlives the access token: Grafana keeps the person signed in for up to 30 days while forwarding a token that expired an hour ago, the UI works, every query answers 401, and signing out and back in fixes it just long enough to make the report unreproducible." -}}
{{- end -}}
{{- if not $oauth.role_attribute_strict -}}
{{- fail "observability-grafana: grafana.ini's [auth.generic_oauth] has `role_attribute_strict` false, so a person whose claims map to no role is given the default one instead of being refused. An unmapped viewer is a support ticket; an unmapped editor is an incident." -}}
{{- end -}}

{{- /* Database and session key. */ -}}
{{- if or (not $c.database.host) (not $c.database.secretRef.name) -}}
{{- fail "observability-grafana: `global.observabilityGrafana.database.host` and `database.secretRef.name` are both required. State lives in an external PostgreSQL: with two replicas a SQLite file is either two databases or one file two processes write, and the console answers 500 `database is locked` on whichever request loses." -}}
{{- end -}}
{{- if and (not $c.secretKeyRef.name) (not $c.secretKeyRef.builtInKey) -}}
{{- fail "observability-grafana: `global.observabilityGrafana.secretKeyRef.name` is empty. That is the key Grafana signs sessions and encrypts stored secrets with; without a Secret every install shares Grafana's published built-in key. Name a Secret, or set `secretKeyRef.builtInKey: true` to run on the built-in key knowingly." -}}
{{- end -}}

{{- /* The sidecar. */ -}}
{{- $provider := ((($g.sidecar).dashboards).provider) | default dict -}}
{{- $interval := $provider.updateIntervalSeconds | default 0 | int -}}
{{- if le $interval 10 -}}
{{- fail (printf "observability-grafana: grafana.sidecar.dashboards.provider.updateIntervalSeconds is %d. At 10 or below Grafana watches the filesystem instead of polling it, and a Kubernetes ConfigMap projection is a symlink swap that fires no watch event, so a dashboard change never lands and nothing reports an error. Use a value above 10." $interval) -}}
{{- end -}}
{{- end -}}

{{/*
The connectors an HTTPRoute fronts, as JSON: name, Service, path (from the
resourceURL, scheme and host stripped) and host. Grafana FIRST when enabled,
so the bare protected-resource document goes to it, else to the first store.
*/}}
{{- define "observability-mcp.routeConnectors" -}}
{{- $out := list -}}
{{- if .Values.grafana.enabled -}}
{{- $u := urlParse .Values.grafana.resourceURL -}}
{{- $out = append $out (dict "name" "grafana" "service" (include "observability-mcp.fullname" (dict "root" . "name" "grafana")) "path" ($u.path | trimSuffix "/") "host" (regexReplaceAll ":[0-9]+$" $u.host "") "url" .Values.grafana.resourceURL) -}}
{{- end -}}
{{- range $s := .Values.stores -}}
{{- $u := urlParse $s.resourceURL -}}
{{- $out = append $out (dict "name" $s.name "service" (include "observability-mcp.fullname" (dict "root" $ "name" $s.name)) "path" ($u.path | trimSuffix "/") "host" (regexReplaceAll ":[0-9]+$" $u.host "") "url" $s.resourceURL) -}}
{{- end -}}
{{- toJson $out -}}
{{- end -}}

{{/*
Refusals for `httpRoute`, when it is enabled:
  - `parentRefs` empty: a route with no parent attaches to nothing and
    renders like a working one.
  - a resourceURL with no path: PathPrefix "/" would swallow the whole host.
  - two connectors at one path: the gateway could not tell them apart.
  - `hostnames` set and a resourceURL on another host: the route would not
    match the very URL clients are told to use.
*/}}
{{- define "observability-mcp.validate.httpRoute" -}}
{{- $r := .Values.httpRoute -}}
{{- if not $r.parentRefs -}}
{{- fail "observability-mcp: `httpRoute.enabled` is true but `httpRoute.parentRefs` is empty, so the route would attach to no Gateway and answer nothing. Name the Gateway or ListenerSet it attaches to." -}}
{{- end -}}
{{- $paths := dict -}}
{{- range $c := (include "observability-mcp.routeConnectors" . | fromJsonArray) -}}
{{- if not $c.path -}}
{{- fail (printf "observability-mcp: the resourceURL %q of connector %q has no path, and `httpRoute` would route PathPrefix `/` (the whole host) to it. Give each connector its own path, e.g. https://host/victoria/primary." $c.url $c.name) -}}
{{- end -}}
{{- if hasKey $paths $c.path -}}
{{- fail (printf "observability-mcp: two connectors have the resource path %q; one HTTPRoute cannot tell them apart." $c.path) -}}
{{- end -}}
{{- $_ := set $paths $c.path true -}}
{{- if and $r.hostnames (not (has $c.host $r.hostnames)) -}}
{{- fail (printf "observability-mcp: the resourceURL %q of connector %q is on host %q, which is not in `httpRoute.hostnames` (%s); the route would not match the URL clients are told to use." $c.url $c.name $c.host (join ", " $r.hostnames)) -}}
{{- end -}}
{{- end -}}
{{- end -}}

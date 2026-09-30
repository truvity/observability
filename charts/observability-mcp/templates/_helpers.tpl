{{/*
Names, labels and the two containers' configuration.

Every object is named for its server, `observability-mcp-<server>`, and not
for the release: the chart renders one set per enabled server, and two
releases of it that enable different servers in one namespace must not
collide.

Each helper below takes a context built by the caller:
  (dict "root" $ "key" "<server>" "s" <that server's values>)
*/}}
{{- define "observability-mcp.fullname" -}}
{{- printf "%s-%s" .root.Chart.Name .key | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "observability-mcp.selectorLabels" -}}
app.kubernetes.io/name: {{ .root.Chart.Name }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .key }}
{{- end -}}

{{- define "observability-mcp.labels" -}}
{{ include "observability-mcp.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .root.Release.Service }}
{{- end -}}

{{/*
An image reference: repository, tag, and the digest when one is pinned.
*/}}
{{- define "observability-mcp.image" -}}
{{- printf "%s:%s" .repository .tag -}}
{{- if .digest }}@{{ .digest }}{{ end -}}
{{- end -}}

{{/*
The port of a URL, from its scheme when it names none. Used to open
exactly the ports the NetworkPolicy's egress needs.
*/}}
{{- define "observability-mcp.urlPort" -}}
{{- $u := urlParse . -}}
{{- $p := regexFind ":[0-9]+$" $u.host | trimPrefix ":" -}}
{{- default (ternary "443" "80" (eq $u.scheme "https")) $p -}}
{{- end -}}

{{/*
The fixed loopback addresses inside the pod. The upstream listens on the
first, the proxy's outbound side on the second; neither is a container
port, and neither is reachable from outside the pod.
*/}}
{{- define "observability-mcp.upstreamListen" -}}127.0.0.1:8081{{- end -}}
{{- define "observability-mcp.outboundListen" -}}127.0.0.1:8429{{- end -}}
{{- define "observability-mcp.proxyPort" -}}8080{{- end -}}
{{- define "observability-mcp.tokenFile" -}}/var/run/observability-mcp/sa-token{{- end -}}

{{/*
THE resource-proxy's environment — the one place its variable names are
written, so a rename in that image is a one-line change here.

  LISTEN                    the pod's only port
  UPSTREAM                  the stock server's MCP endpoint, on loopback
  ISSUER_URL, RESOURCE_URL, SCOPE        the inbound token check
  OUTBOUND_LISTEN           loopback address the upstream calls the store on
  OUTBOUND_TARGET           the store's proxy
  OUTBOUND_SA_TOKEN_FILE, OUTBOUND_TOKEN_ENDPOINT,
  OUTBOUND_CLIENT_ID, OUTBOUND_AUDIENCE  how it gets the token it injects
*/}}
{{- define "observability-mcp.proxyEnv" -}}
- name: LISTEN
  value: {{ printf ":%s" (include "observability-mcp.proxyPort" .) | quote }}
- name: UPSTREAM
  value: {{ printf "http://%s/mcp" (include "observability-mcp.upstreamListen" .) | quote }}
- name: ISSUER_URL
  value: {{ .root.Values.issuerURL | quote }}
- name: RESOURCE_URL
  value: {{ .s.resourceURL | quote }}
- name: SCOPE
  value: {{ .s.scope | quote }}
- name: OUTBOUND_LISTEN
  value: {{ include "observability-mcp.outboundListen" . | quote }}
- name: OUTBOUND_TARGET
  value: {{ .s.outbound.target | quote }}
- name: OUTBOUND_SA_TOKEN_FILE
  value: {{ include "observability-mcp.tokenFile" . | quote }}
- name: OUTBOUND_TOKEN_ENDPOINT
  value: {{ .s.outbound.tokenEndpoint | quote }}
- name: OUTBOUND_CLIENT_ID
  value: {{ .s.outbound.clientId | quote }}
- name: OUTBOUND_AUDIENCE
  value: {{ .s.outbound.audience | quote }}
{{- end -}}

{{/*
The stock mcp-victoriametrics' environment.

No VM_INSTANCE_BEARER_TOKEN: the upstream holds no credential, and what it
sends as Authorization is replaced by the proxy's outbound side. The
entrypoint is the proxy's loopback listener plus the store's path prefix;
VM_INSTANCE_TYPE=single makes the upstream call `<entrypoint>/api/v1/...`
and `<entrypoint>/vmalert/api/v1/...` (cluster mode would call
`/select/<tenant>/prometheus/...`, a path the store's proxy does not serve).
MCP_DISABLED_TOOLS replaces the upstream's own default list, so values
carry that list too.
*/}}
{{- define "observability-mcp.upstreamEnv.metrics" -}}
- name: MCP_SERVER_MODE
  value: http
- name: MCP_LISTEN_ADDR
  value: {{ include "observability-mcp.upstreamListen" . | quote }}
- name: VM_INSTANCE_TYPE
  value: single
- name: VM_INSTANCE_ENTRYPOINT
  value: {{ printf "http://%s%s" (include "observability-mcp.outboundListen" .) .s.entrypointPath | quote }}
- name: MCP_DISABLED_TOOLS
  value: {{ join "," .s.disabledTools | quote }}
- name: MCP_DISABLE_RESOURCES
  value: "true"
- name: MCP_LOG_FORMAT
  value: json
{{- end -}}

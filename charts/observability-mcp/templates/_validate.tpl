{{/*
Every refusal in this chart.

All of them guard a value that is EMPTY by default and whose absence renders
cleanly and fails later, somewhere else:

  - a server that is not implemented yet is enabled  -> validate.implemented
  - `issuerURL` empty                                -> validate.issuer
  - `proxy.image.tag` empty                          -> validate.proxyImage
  - a server with no `resourceURL`                   -> validate.server
  - a server with its `outbound` settings incomplete -> validate.server
  - `networkPolicy` enabled with a peer list empty   -> validate.networkPolicy
  - a PodDisruptionBudget with fewer than two replicas -> validate.server

An install with NO server enabled renders nothing and is not refused.
*/}}
{{- define "observability-mcp.validate" -}}
{{- $enabled := list -}}
{{- range $key := (list "metrics" "logs" "traces" "dashboards") -}}
{{- if (index $.Values.servers $key).enabled -}}
{{- $enabled = append $enabled $key -}}
{{- end -}}
{{- end -}}
{{- if $enabled -}}
{{- include "observability-mcp.validate.implemented" (dict "enabled" $enabled) -}}
{{- include "observability-mcp.validate.issuer" . -}}
{{- include "observability-mcp.validate.proxyImage" . -}}
{{- range $key := $enabled -}}
{{- include "observability-mcp.validate.server" (dict "root" $ "key" $key "s" (index $.Values.servers $key)) -}}
{{- end -}}
{{- include "observability-mcp.validate.networkPolicy" . -}}
{{- end -}}
{{- end -}}

{{/*
A stub that renders a half-working server. `logs`, `traces` and `dashboards`
exist in values so a later phase is a values change, but the upstreams'
environment for them is not wired or proven here; enabling one would render
a pod that starts and answers nothing useful.
*/}}
{{- define "observability-mcp.validate.implemented" -}}
{{- range $key := .enabled -}}
{{- if ne $key "metrics" -}}
{{- fail (printf "observability-mcp: `servers.%s.enabled` is true, but only `servers.metrics` is implemented in this release. The other servers are values stubs so a later phase is a values change; enabling one now would render a pod whose upstream is not wired to the store. See docs/mcp.md." $key) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
No issuer, no token check: the proxy would refuse to start (or, worse, a
later edit would have it accept tokens from nowhere in particular).
*/}}
{{- define "observability-mcp.validate.issuer" -}}
{{- if not .Values.issuerURL -}}
{{- fail "observability-mcp: `issuerURL` is empty. The proxy validates every caller's access token against the issuer's signing keys and `iss`; without it there is nothing to validate against, and the pod would be an unauthenticated door to the store's read path. Set it to the access-token issuer's URL." -}}
{{- end -}}
{{- end -}}

{{/*
The proxy image is built and released by another repository, so there is no
tag this chart can be right about. An empty tag would render `image: repo:`,
which the API server accepts and the kubelet then cannot pull.
*/}}
{{- define "observability-mcp.validate.proxyImage" -}}
{{- if not .Values.proxy.image.tag -}}
{{- fail "observability-mcp: `proxy.image.tag` is empty. The resource-proxy sidecar is built by another repository's release, so this chart carries no default version; pin the release you have verified (and its digest, in `proxy.image.digest`, if you pin by digest)." -}}
{{- end -}}
{{- end -}}

{{- define "observability-mcp.validate.server" -}}
{{- $k := .key -}}
{{- $s := .s -}}
{{- if not $s.resourceURL -}}
{{- fail (printf "observability-mcp: `servers.%s.resourceURL` is empty. It is this server's RFC 8707 resource identifier: the proxy requires every token's `aud` to equal it, and publishes it in its protected-resource metadata. Empty, no client could ever obtain a token the server accepts." $k) -}}
{{- end -}}
{{- if not $s.image.tag -}}
{{- fail (printf "observability-mcp: `servers.%s.image.tag` is empty. The upstream server is pinned, never floating." $k) -}}
{{- end -}}
{{- range $f := (list "target" "tokenEndpoint" "clientId" "audience") -}}
{{- if not (index $s.outbound $f) -}}
{{- fail (printf "observability-mcp: `servers.%s.outbound.%s` is empty. The proxy's outbound side needs all four of target, tokenEndpoint, clientId and audience to get the token it presents to the store; with one missing the upstream would call the store with no credential and every tool would fail with an authorisation error." $k $f) -}}
{{- end -}}
{{- end -}}
{{- if and $.root.Values.podDisruptionBudget.enabled (lt (int $s.replicaCount) 2) -}}
{{- fail (printf "observability-mcp: `podDisruptionBudget.enabled` is true but `servers.%s.replicaCount` is %d. A budget of one available on a single replica blocks every node drain. Run two replicas, or leave the budget off." $k (int $s.replicaCount)) -}}
{{- end -}}
{{- end -}}

{{/*
A NetworkPolicy that admits, or reaches, nothing.

Enabled with `ingressFrom` empty, the ingress rule selects no peer, so the
gateway cannot reach the proxy; with an egress peer list empty, the pod
cannot reach the issuer or the store. Every one of those renders like a
correctly scoped policy and fails as a timeout.
*/}}
{{- define "observability-mcp.validate.networkPolicy" -}}
{{- if .Values.networkPolicy.enabled -}}
{{- if not .Values.networkPolicy.ingressFrom -}}
{{- fail "observability-mcp: `networkPolicy.enabled` is true but `networkPolicy.ingressFrom` is empty, so the rendered policy would admit NOBODY to the proxy's port, the gateway included. Name the gateway's peers, or set `networkPolicy.enabled: false` if the cluster's CNI does not enforce NetworkPolicy." -}}
{{- end -}}
{{- if not .Values.networkPolicy.egress.issuer -}}
{{- fail "observability-mcp: `networkPolicy.egress.issuer` is empty, so the rendered policy would let the pod reach the issuer from NOWHERE: it could not fetch signing keys or exchange its token. Name the peers the issuer is reached at." -}}
{{- end -}}
{{- if not .Values.networkPolicy.egress.vmauth -}}
{{- fail "observability-mcp: `networkPolicy.egress.vmauth` is empty, so the rendered policy would let the pod reach the store's proxy from NOWHERE, and every tool call would time out. Name the peers the store's proxy is reached at." -}}
{{- end -}}
{{- end -}}
{{- end -}}

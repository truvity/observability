{{/*
Every refusal in this chart.

All of them guard a value that is EMPTY (or wrong) by default and whose
absence renders cleanly and fails later, somewhere else:

  - `issuerURL` empty                                -> validate.issuer
  - `proxy.image.tag` empty                          -> validate.proxyImage
  - a store with a bad name, or two that collide     -> validate.stores
  - a connector with no `resourceURL`, or a duplicate -> validate.connector
  - a connector with its `outbound` settings incomplete -> validate.connector
  - a store with every signal off, or a bad cluster  -> validate.store
  - an allowlist that would be refused by the aggregator -> validate.tools
  - `networkPolicy` enabled with a peer list empty   -> validate.networkPolicy
  - a PodDisruptionBudget with fewer than two replicas -> validate.connector

An install with NO connector enabled renders nothing and is not refused.
*/}}
{{- define "observability-mcp.validate" -}}
{{- if or .Values.stores .Values.grafana.enabled -}}
{{- include "observability-mcp.validate.issuer" . -}}
{{- include "observability-mcp.validate.proxyImage" . -}}
{{- include "observability-mcp.validate.stores" . -}}
{{- include "observability-mcp.validate.tools" . -}}
{{- include "observability-mcp.validate.networkPolicy" . -}}
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

{{/*
The stores, and the connectors they and Grafana make: names are slugs and
unique (they become object names), resource URLs are unique (two connectors
at one URL is a gateway that cannot tell them apart), and every connector
is complete.
*/}}
{{- define "observability-mcp.validate.stores" -}}
{{- $names := dict -}}
{{- $urls := dict -}}
{{- range $i, $s := .Values.stores -}}
{{- if not $s.name -}}
{{- fail (printf "observability-mcp: `stores[%d]` has no `name`. The name is the suffix of every object the connector renders (`observability-mcp-<name>`)." $i) -}}
{{- end -}}
{{- if not (regexMatch "^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$" $s.name) -}}
{{- fail (printf "observability-mcp: store name %q is not a lowercase slug of at most 40 characters ([a-z0-9-], starting and ending alphanumeric). It becomes a Kubernetes object name and a label value." $s.name) -}}
{{- end -}}
{{- if eq $s.name "grafana" -}}
{{- fail "observability-mcp: a store is named `grafana`, which is the name of the Grafana connector's objects (`observability-mcp-grafana`). Name the store something else." -}}
{{- end -}}
{{- if hasKey $names $s.name -}}
{{- fail (printf "observability-mcp: two stores are named %q. Each renders `observability-mcp-%s`; the second would replace the first." $s.name $s.name) -}}
{{- end -}}
{{- $_ := set $names $s.name true -}}
{{- include "observability-mcp.validate.connector" (dict "root" $ "label" (printf "stores[%s]" $s.name) "resourceURL" $s.resourceURL "outbound" ($s.outbound | default dict) "replicaCount" ($s.replicaCount | default 1) "urls" $urls) -}}
{{- include "observability-mcp.validate.store" (dict "root" $ "s" $s) -}}
{{- end -}}
{{- if .Values.grafana.enabled -}}
{{- $g := .Values.grafana -}}
{{- include "observability-mcp.validate.connector" (dict "root" $ "label" "grafana" "resourceURL" $g.resourceURL "outbound" ($g.outbound | default dict) "replicaCount" ($g.replicaCount | default 1) "urls" $urls) -}}
{{- if not $g.url -}}
{{- fail "observability-mcp: `grafana.url` is empty. It is where the proxy's outbound side forwards the Grafana calls; the stock mcp-grafana is given no other address." -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "observability-mcp.validate.connector" -}}
{{- $l := .label -}}
{{- if not .resourceURL -}}
{{- fail (printf "observability-mcp: `%s` has no `resourceURL`. It is the connector's RFC 8707 resource identifier: the proxy requires every token's `aud` to equal it, and publishes it in its protected-resource metadata. Empty, no client could ever obtain a token the connector accepts." $l) -}}
{{- end -}}
{{- if hasKey .urls .resourceURL -}}
{{- fail (printf "observability-mcp: `%s` has the same `resourceURL` (%s) as another connector. One URL is one OAuth resource and one path on the gateway; two connectors at it cannot be told apart." $l .resourceURL) -}}
{{- end -}}
{{- $_ := set .urls .resourceURL true -}}
{{- range $f := (list "tokenEndpoint" "clientId" "audience") -}}
{{- if not (index $.outbound $f) -}}
{{- fail (printf "observability-mcp: `%s.outbound.%s` is empty. The proxy's outbound side needs tokenEndpoint, clientId and audience to get the token it presents downstream; with one missing the stock servers would call with no credential and every tool would fail with an authorisation error." $l $f) -}}
{{- end -}}
{{- end -}}
{{- if and .root.Values.podDisruptionBudget.enabled (lt (int .replicaCount) 2) -}}
{{- fail (printf "observability-mcp: `podDisruptionBudget.enabled` is true but `%s` has replicaCount %d. A budget of one available on a single replica blocks every node drain. Run two replicas, or leave the budget off." $l (int .replicaCount)) -}}
{{- end -}}
{{- end -}}

{{- define "observability-mcp.validate.store" -}}
{{- $s := .s -}}
{{- if not ($s.vmauth).url -}}
{{- fail (printf "observability-mcp: `stores[%s].vmauth.url` is empty. It is the store's vmauth base URL, where the proxy's outbound side forwards; with none the stock servers have nothing to read." $s.name) -}}
{{- end -}}
{{- if not (regexMatch "^https?://[^/\\s]+$" $s.vmauth.url) -}}
{{- fail (printf "observability-mcp: `stores[%s].vmauth.url` %q must be a base URL: scheme and host, no path. The stock servers add their own paths (`/prometheus/...`, `/select/...`) after the proxy's outbound listener, so a path here would be silently dropped." $s.name $s.vmauth.url) -}}
{{- end -}}
{{- if not (include "observability-mcp.storeSignals" $s | fromJsonArray) -}}
{{- fail (printf "observability-mcp: store %q has every signal switched off (`signals`). A connector with nothing to read exposes no tools." $s.name) -}}
{{- end -}}
{{- range $c := ($s.clusters | default list) -}}
{{- if not (regexMatch "^[A-Za-z0-9][A-Za-z0-9._-]*$" (toString $c)) -}}
{{- fail (printf "observability-mcp: store %q lists the cluster %q, which is not a plain label value ([A-Za-z0-9._-]). The list only fills in the connector's instructions text; put anything else in `instructions`." $s.name (toString $c)) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
An allowlist the aggregator would refuse at start fails here instead, at
render: an exposed tool name must be <prefix>_<tool> in [a-zA-Z0-9_-] and
at most 64 characters, and an allowlist may not be empty.
*/}}
{{- define "observability-mcp.validate.tools" -}}
{{- if .Values.stores -}}
{{- range $k, $u := (pick .Values.upstreams "victoriametrics" "victorialogs" "victoriatraces") -}}
{{- if not $u.image.tag -}}
{{- fail (printf "observability-mcp: `upstreams.%s.image.tag` is empty. The stock server is pinned, never floating." $k) -}}
{{- end -}}
{{- if not $u.tools -}}
{{- fail (printf "observability-mcp: `upstreams.%s.tools` is empty. The aggregator refuses an empty allowlist: it would expose nothing." $k) -}}
{{- end -}}
{{- if not (regexMatch "^[a-zA-Z0-9-]{1,32}$" $u.prefix) -}}
{{- fail (printf "observability-mcp: `upstreams.%s.prefix` %q must match [a-zA-Z0-9-]{1,32}; exposed tool names are <prefix>_<tool>." $k $u.prefix) -}}
{{- end -}}
{{- range $t := $u.tools -}}
{{- if not (regexMatch "^[a-zA-Z0-9_-]{1,64}$" (printf "%s_%s" $u.prefix $t)) -}}
{{- fail (printf "observability-mcp: `upstreams.%s.tools` lists %q, which would be exposed as %q: a tool name must match [a-zA-Z0-9_-] and be at most 64 characters." $k $t (printf "%s_%s" $u.prefix $t)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if .Values.grafana.enabled -}}
{{- if not .Values.upstreams.grafana.image.tag -}}
{{- fail "observability-mcp: `upstreams.grafana.image.tag` is empty. The stock server is pinned, never floating." -}}
{{- end -}}
{{- if not .Values.upstreams.grafana.enabledTools -}}
{{- fail "observability-mcp: `upstreams.grafana.enabledTools` is empty. The stock mcp-grafana then enables its whole default set (81 tools, writes among them); name the categories (search, dashboard)." -}}
{{- end -}}
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
{{- if and .Values.stores (not .Values.networkPolicy.egress.vmauth) -}}
{{- fail "observability-mcp: `networkPolicy.egress.vmauth` is empty, so the rendered policy would let a store's pod reach the store's vmauth from NOWHERE, and every tool call would time out. Name the peers the stores' vmauth are reached at." -}}
{{- end -}}
{{- if and .Values.grafana.enabled (not .Values.networkPolicy.egress.grafana) -}}
{{- fail "observability-mcp: `networkPolicy.egress.grafana` is empty while the Grafana connector is enabled, so the rendered policy would let its pod reach Grafana from NOWHERE, and every tool call would time out. Name the peers Grafana is reached at." -}}
{{- end -}}
{{- end -}}
{{- end -}}

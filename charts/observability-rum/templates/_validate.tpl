{{/*
Every refusal in this chart, and the fixture that proves each one
(tests/invalid/observability-rum/<name>.yaml, each declaring the message it
expects). What the schema can say (types, required keys, unknown keys) it
says; what it cannot is here.

Two groups, and the split is not a taste. The Alloy subchart renders its
configuration from `global.observabilityRum` while the parent's templates
render the rest, and Helm renders subchart templates FIRST: a bad value
would otherwise reach the configuration template before any check ran and
surface as a nil dereference instead of a sentence. So the checks on what
the configuration reads (`validate.config`, global values only) are also the
first thing the configuration template runs. The checks on everything else
are `validate`.

  - otlp.endpoint empty or not http(s)        -> validate.config
  - no apps                                   -> validate.config
  - an app name that is not a short slug,
    reserved, or repeated                     -> validate.config
  - an apiKeySecret without name or key       -> validate.config
  - allowedOrigins empty, a wildcard, or
    not an exact origin                       -> validate.config
  - maxPayloadSize malformed or above the cap -> validate.config
  - a port out of range, taken by Alloy, or
    repeated                                  -> validate.config
  - rateLimit.strategy other than global      -> validate.config
  - sourcemaps.download true                  -> validate.config
  - sourcemaps.sync without a bucket/region   -> validate.config
  - alloy.fullnameOverride empty, alloy.rbac.create true,
    a replaced configuration, networkPolicy
    with no peers, a malformed alert window   -> validate
*/}}

{{- define "observability-rum.validate" -}}
{{- include "observability-rum.validate.config" . -}}
{{- include "observability-rum.validate.alloy" . -}}
{{- include "observability-rum.validate.networkPolicy" . -}}
{{- include "observability-rum.validate.rules" . -}}
{{- include "observability-rum.validate.dashboards" . -}}
{{- end -}}

{{- define "observability-rum.validate.config" -}}
{{- include "observability-rum.validate.otlp" . -}}
{{- include "observability-rum.validate.apps" . -}}
{{- include "observability-rum.validate.defaults" . -}}
{{- include "observability-rum.validate.sourcemaps" . -}}
{{- end -}}

{{/*
No OTLP endpoint. The pipeline's only destination; there is no default,
because a default is a guess about somebody's gateway, and an exporter
pointed at nothing buffers and drops without a word.
*/}}
{{- define "observability-rum.validate.otlp" -}}
{{- $e := .Values.global.observabilityRum.otlp.endpoint | default "" -}}
{{- if not $e -}}
{{- fail "observability-rum: `global.observabilityRum.otlp.endpoint` is empty. This chart writes browser telemetry to the estate's OTLP gateway and never to a store; with no endpoint it would receive events and send them nowhere. Set the gateway's OTLP/HTTP base URL, e.g. http://<gateway-service>.<namespace>.svc:4318 ." -}}
{{- end -}}
{{- if not (regexMatch "^https?://[^/?#\\s]+(/[^?#\\s]*)?$" $e) -}}
{{- fail (printf "observability-rum: `global.observabilityRum.otlp.endpoint` %q is not an http(s) URL. Give the gateway's OTLP/HTTP base URL; the exporter appends /v1/logs and /v1/traces." $e) -}}
{{- end -}}
{{- end -}}

{{- define "observability-rum.validate.apps" -}}
{{- $c := .Values.global.observabilityRum -}}
{{- if not $c.apps -}}
{{- fail "observability-rum: `global.observabilityRum.apps` is empty. Each web app is one entry (name, apiKeySecret, allowedOrigins); with none there is no receiver to render." -}}
{{- end -}}
{{- $apps := include "observability-rum.apps" . | fromYamlArray -}}
{{- $names := dict -}}
{{- $ports := dict -}}
{{- range $a := $apps -}}
{{- /* A name is the Service port name too (15 characters, a DNS label), the
       Alloy component label and the app's identity in every query. */ -}}
{{- if not (regexMatch "^[a-z]([a-z0-9-]{0,13}[a-z0-9])?$" $a.name) -}}
{{- fail (printf "observability-rum: app name %q is not a slug of at most 15 characters (lowercase letters, digits and hyphens, starting with a letter). The name is the Service port name, the receiver's identity and the `app` every query groups by." $a.name) -}}
{{- end -}}
{{- if eq $a.name "http-metrics" -}}
{{- fail "observability-rum: app name \"http-metrics\" is reserved (the Alloy subchart's own metrics port)." -}}
{{- end -}}
{{- if hasKey $names $a.name -}}
{{- fail (printf "observability-rum: app name %q appears twice. The name is the identity the receiver stamps; two receivers sharing it would pool two apps' errors under one fingerprint space." $a.name) -}}
{{- end -}}
{{- $_ := set $names $a.name true -}}
{{- if or (not $a.secretName) (not $a.secretKey) -}}
{{- fail (printf "observability-rum: app %q has no `apiKeySecret` (name and key both required). A receiver without a key accepts a post from anyone; the key is public and rotatable, and still the one check that tells an app's own bundle from a stray script." $a.name) -}}
{{- end -}}
{{- if not $a.origins -}}
{{- fail (printf "observability-rum: app %q has no `allowedOrigins`. An empty list turns CORS off and an app with no origin is not an app; list the exact origin(s) its pages are served from, e.g. https://app.example (with the same-origin route, the app's own)." $a.name) -}}
{{- end -}}
{{- range $o := $a.origins -}}
{{- if contains "*" $o -}}
{{- fail (printf "observability-rum: app %q allowedOrigins %q is a wildcard. A wildcard lets any site's page post to this receiver with a browser's blessing; list exact origins." $a.name $o) -}}
{{- end -}}
{{- if not (regexMatch "^https?://[A-Za-z0-9]([A-Za-z0-9.-]*[A-Za-z0-9])?(:[0-9]{1,5})?$" $o) -}}
{{- fail (printf "observability-rum: app %q allowedOrigins %q is not an exact origin. An origin is scheme, host and optional port with no path and no trailing slash, e.g. https://app.example ." $a.name $o) -}}
{{- end -}}
{{- end -}}
{{- if or (eq (int $a.maxBytes) 0) -}}
{{- fail (printf "observability-rum: app %q maxPayloadSize %q is not a size like 256KiB or 1MiB." $a.name $a.maxPayload) -}}
{{- end -}}
{{- if gt (int $a.maxBytes) 1048576 -}}
{{- fail (printf "observability-rum: app %q maxPayloadSize %q is above the 1MiB ceiling. A browser batch is a few KiB; Alloy buffers a whole request in memory, and the limit is already only a soft one (docs/frontend.md)." $a.name $a.maxPayload) -}}
{{- end -}}
{{- if or (lt (int $a.port) 1024) (gt (int $a.port) 65535) -}}
{{- fail (printf "observability-rum: app %q port %d is outside 1024-65535." $a.name (int $a.port)) -}}
{{- end -}}
{{- if eq (int $a.port) 12345 -}}
{{- fail (printf "observability-rum: app %q port 12345 is Alloy's own HTTP server (metrics, readiness and its UI); a receiver cannot share it." $a.name) -}}
{{- end -}}
{{- if hasKey $ports (toString (int $a.port)) -}}
{{- fail (printf "observability-rum: apps %q and %q both use port %d. One receiver per port; give each app its own." (get $ports (toString (int $a.port))) $a.name (int $a.port)) -}}
{{- end -}}
{{- $_ := set $ports (toString (int $a.port)) $a.name -}}
{{- if or (le (float64 $a.rate) 0.0) (le (float64 $a.burst) 0.0) -}}
{{- fail (printf "observability-rum: app %q rateLimit needs a positive rate and burst. A zero is not 'unlimited' here; an unbounded public endpoint is not an option this chart offers." $a.name) -}}
{{- end -}}
{{- if or (le (float64 $a.traces) 0.0) (gt (float64 $a.traces) 1.0) -}}
{{- fail (printf "observability-rum: app %q sampling.traces %v is outside (0, 1]. To receive no traces, do not send them from the SDK." $a.name $a.traces) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
`per_app` rate limiting is keyed by the app name the PAYLOAD claims, which
the sender chooses: a client that varies it gets a fresh bucket per request,
so the limit limits nothing. `global` is keyed by nothing the sender can
touch, and one receiver per app makes it per app anyway.
*/}}
{{- define "observability-rum.validate.defaults" -}}
{{- $rl := .Values.global.observabilityRum.defaults.rateLimit -}}
{{- if and (hasKey $rl "strategy") (ne ($rl.strategy | toString) "global") -}}
{{- fail (printf "observability-rum: `defaults.rateLimit.strategy` %q is refused; only `global` is allowed. Alloy's `per_app` strategy keys its buckets on the app name in the PAYLOAD, which the sender chooses, so it limits nothing; with one receiver per app, `global` is already a per-app limit." $rl.strategy) -}}
{{- end -}}
{{- if or (le (float64 $rl.rate) 0.0) (le (float64 $rl.burst) 0.0) -}}
{{- fail "observability-rum: `defaults.rateLimit` needs a positive rate and burst. A zero is not 'unlimited' here." -}}
{{- end -}}
{{- $size := .Values.global.observabilityRum.defaults.maxPayloadSize | toString -}}
{{- if or (eq (include "observability-rum.bytes" $size | int) 0) (gt (include "observability-rum.bytes" $size | int) 1048576) -}}
{{- fail (printf "observability-rum: `defaults.maxPayloadSize` %q is not a size between 1KiB and 1MiB." $size) -}}
{{- end -}}
{{- end -}}

{{- define "observability-rum.validate.sourcemaps" -}}
{{- $s := .Values.global.observabilityRum.sourcemaps -}}
{{- /* Alloy downloads a map from the page's own origin by default, taking
       the URL from the (client-reported) script URL in the payload: the
       receiver would fetch whatever a stray post named, and the maps would
       have to be public. Maps come from the directory or the sync, only. */ -}}
{{- if $s.download -}}
{{- fail "observability-rum: `sourcemaps.download: true` is refused. It makes the receiver fetch a source map from a URL the BROWSER names (a server-side request an anonymous poster steers) and means the maps are served publicly from the app. Source maps come from `sourcemaps.directory` or `sourcemaps.sync`." -}}
{{- end -}}
{{- $sync := $s.sync -}}
{{- if $sync.enabled -}}
{{- if not $sync.bucket -}}
{{- fail "observability-rum: `sourcemaps.sync.enabled` needs `sourcemaps.sync.bucket`. The sync copies one bucket prefix, read-only, into a volume the receivers read; with no bucket it would sync nothing and every stack trace would stay minified." -}}
{{- end -}}
{{- if and (not $sync.region) (not $sync.endpointUrl) -}}
{{- fail "observability-rum: `sourcemaps.sync` needs `region` (or an `endpointUrl` for an S3-compatible store); the client refuses to start without one." -}}
{{- end -}}
{{- if or (hasPrefix "/" ($sync.prefix | default "")) (hasSuffix "/" ($sync.prefix | default "")) -}}
{{- fail "observability-rum: `sourcemaps.sync.prefix` must not start or end with a slash; the layout is <prefix>/<app>/<release>/<path>.map." -}}
{{- end -}}
{{- if not (regexMatch "^[1-9][0-9]*[smh]$" $sync.interval) -}}
{{- fail (printf "observability-rum: `sourcemaps.sync.interval` %q is not a duration like 5m." $sync.interval) -}}
{{- end -}}
{{- else -}}
{{- if not (regexMatch "^/[A-Za-z0-9._/-]*[A-Za-z0-9._-]$" $s.directory) -}}
{{- fail (printf "observability-rum: `sourcemaps.directory` %q is not an absolute path." $s.directory) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "observability-rum.validate.alloy" -}}
{{- if not .Values.alloy.fullnameOverride -}}
{{- fail "observability-rum: `alloy.fullnameOverride` is empty. The Service, the ServiceAccount a cloud identity binds by name and the Role binding all need a name that does not move with the release; the default is observability-rum." -}}
{{- end -}}
{{- /* The subchart's default Role can read every Secret and pod in the
       namespace; this chart renders its own, for the app keys only. */ -}}
{{- if .Values.alloy.rbac.create -}}
{{- fail "observability-rum: `alloy.rbac.create: true` is refused. The subchart's default Role can read every Secret and pod in the namespace and a ClusterRole besides; a public write endpoint does not need that. This chart renders its own Role: `get` on the named app-key Secrets, nothing else." -}}
{{- end -}}
{{- if not (contains "observability-rum.alloyConfig" (.Values.alloy.alloy.configMap.content | default "")) -}}
{{- fail "observability-rum: `alloy.alloy.configMap.content` was replaced. The configuration is generated from `global.observabilityRum` (one receiver per app, the pipeline, the fingerprint, the privacy rules); a hand-written one would drop all of that without a word. Leave it, or use a different chart." -}}
{{- end -}}
{{- if not .Values.alloy.alloy.configMap.create -}}
{{- fail "observability-rum: `alloy.alloy.configMap.create: false` is refused; the generated configuration is rendered through the subchart's ConfigMap." -}}
{{- end -}}
{{- end -}}

{{- define "observability-rum.validate.networkPolicy" -}}
{{- $n := .Values.networkPolicy -}}
{{- if and $n.enabled (not $n.ingressFrom) -}}
{{- fail "observability-rum: `networkPolicy.enabled` with no `networkPolicy.ingressFrom`. The first policy that selects a pod default-denies it: with no peer listed, the gateway could not reach any receiver and every browser post would time out. List the gateway's pods, or leave the policy off." -}}
{{- end -}}
{{- end -}}

{{- define "observability-rum.validate.rules" -}}
{{- $r := .Values.rules -}}
{{- if $r.enabled -}}
{{- $dur := "^[1-9][0-9]*[mhdw]$" -}}
{{- range $path, $v := dict "newIssue.window" $r.newIssue.window "newIssue.history" $r.newIssue.history "regressed.window" $r.regressed.window "regressed.quietFor" $r.regressed.quietFor "regressed.history" $r.regressed.history "errorRate.window" $r.errorRate.window "recording.webVitals.window" $r.recording.webVitals.window -}}
{{- if not (regexMatch $dur ($v | toString)) -}}
{{- fail (printf "observability-rum: `rules.%s` %q is not a LogsQL duration (a whole number and one of m, h, d, w, e.g. 15m or 7d)." $path $v) -}}
{{- end -}}
{{- end -}}
{{- if not (or $r.newIssue.enabled $r.regressed.enabled (and $r.errorRate.enabled (or (gt (float64 $r.errorRate.perSession) 0.0) (gt (float64 $r.errorRate.perMinute) 0.0))) $r.recording.webVitals.enabled) -}}
{{- fail "observability-rum: `rules.enabled` with every rule switched off renders a VMRule with no groups, which the operator refuses. Enable one, or set `rules.enabled: false`." -}}
{{- end -}}
{{- if or (lt (int $r.maxFingerprints) 1) (gt (int $r.maxFingerprints) 100) -}}
{{- fail "observability-rum: `rules.maxFingerprints` must be between 1 and 100. It caps how many fingerprints can fire per rule per evaluation; without a cap one bad release is one notification per error." -}}
{{- end -}}
{{- if or (lt (float64 $r.errorRate.perSession) 0.0) (lt (float64 $r.errorRate.perMinute) 0.0) (lt (int $r.errorRate.minSessions) 1) -}}
{{- fail "observability-rum: `rules.errorRate` thresholds cannot be negative and `minSessions` is at least 1 (0 turns a threshold off)." -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "observability-rum.validate.dashboards" -}}
{{- $d := .Values.dashboards -}}
{{- if and $d.enabled (or (not $d.datasources.logs) (not $d.datasources.traces)) -}}
{{- fail "observability-rum: `dashboards.enabled` needs `dashboards.datasources.logs` and `.traces`, the UIDs this Grafana was provisioned with. A dashboard that names no datasource shows nothing; set both, or turn the dashboards off." -}}
{{- end -}}
{{- end -}}

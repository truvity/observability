{{/*
Every helper that the Alloy subchart's `tpl` also calls reads ONLY
`.Values.global.observabilityRum` (and `.Release`): in that context the
parent's values do not exist but `global` does. That is the whole reason
the apps live under `global`; see values.yaml.
*/}}

{{/* The fixed name of every object this chart and its Alloy share. */}}
{{- define "observability-rum.fullname" -}}
{{- .Values.alloy.fullnameOverride | default "observability-rum" | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "observability-rum.labels" -}}
app.kubernetes.io/name: {{ .Chart.Name }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end -}}

{{/* The labels the Alloy subchart puts on (and selects) its pods. */}}
{{- define "observability-rum.alloySelector" -}}
app.kubernetes.io/name: alloy
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
A size such as 256KiB or 1MiB, in bytes. Anything else is "0", which the
validation refuses before it is ever used.
*/}}
{{- define "observability-rum.bytes" -}}
{{- if regexMatch "^[1-9][0-9]*KiB$" . -}}
{{- mul (trimSuffix "KiB" . | int) 1024 -}}
{{- else if regexMatch "^[1-9][0-9]*MiB$" . -}}
{{- mul (trimSuffix "MiB" . | int) 1048576 -}}
{{- else -}}
0
{{- end -}}
{{- end -}}

{{/*
The apps with every default applied, as YAML (include returns text, so
callers `fromYamlArray` it). Reads `.Values.global.observabilityRum` only.

Per app: name, id (the Alloy-safe identifier), port, serviceName,
environment, secretName, secretKey, origins, prefixes, maxPayload,
maxBytes, rate, burst, traces, repository (the share of browser traces kept).
*/}}
{{- define "observability-rum.apps" -}}
{{- $c := .Values.global.observabilityRum -}}
{{- $d := $c.defaults -}}
{{- $out := list -}}
{{- range $i, $a := $c.apps -}}
{{- $rl := $a.rateLimit | default dict -}}
{{- $sm := $a.sourcemaps | default dict -}}
{{- $origins := $a.allowedOrigins | default list -}}
{{- $prefixes := list -}}
{{- if $sm.minifiedPathPrefixes -}}
{{- $prefixes = $sm.minifiedPathPrefixes -}}
{{- else -}}
{{- range $o := $origins -}}
{{- $prefixes = append $prefixes (printf "%s/" (trimSuffix "/" $o)) -}}
{{- end -}}
{{- end -}}
{{- $size := $a.maxPayloadSize | default $d.maxPayloadSize | toString -}}
{{- $sampling := $a.sampling | default dict -}}
{{- $key := $a.apiKeySecret | default dict -}}
{{- $app := dict
      "name" $a.name
      "id" (replace "-" "_" $a.name)
      "port" ($a.port | default (add ($d.firstPort | int) $i))
      "serviceName" ($a.serviceName | default (printf "%s-browser" $a.name))
      "environment" ($a.environment | default "")
      "secretName" ($key.name | default "")
      "secretKey" ($key.key | default "")
      "origins" $origins
      "prefixes" $prefixes
      "repository" ($sm.repository | default "")
      "maxPayload" $size
      "maxBytes" (include "observability-rum.bytes" $size | int)
      "rate" (ternary $rl.rate $d.rateLimit.rate (hasKey $rl "rate"))
      "burst" (ternary $rl.burst $d.rateLimit.burst (hasKey $rl "burst"))
      "traces" (ternary $sampling.traces 1.0 (hasKey $sampling "traces")) -}}
{{- $out = append $out $app -}}
{{- end -}}
{{- toYaml $out -}}
{{- end -}}

{{/*
A LogsQL duration (a whole number and one of m, h, d, w) in minutes, so a
per-minute rate can divide by the window it was counted over.
*/}}
{{- define "observability-rum.minutes" -}}
{{- $n := regexFind "^[0-9]+" . | int -}}
{{- if hasSuffix "w" . -}}{{- mul $n 10080 -}}
{{- else if hasSuffix "d" . -}}{{- mul $n 1440 -}}
{{- else if hasSuffix "h" . -}}{{- mul $n 60 -}}
{{- else -}}{{- $n -}}
{{- end -}}
{{- end -}}

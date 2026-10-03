{{/* The rendered VMRule object's name. */}}
{{- define "platform-alerts.fullname" -}}
{{- if .Values.fullnameOverride -}}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := default .Chart.Name .Values.nameOverride -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Every enabled group that needs `stores`, so one refusal can name them all.
*/}}
{{- define "platform-alerts.storeGroups" -}}
{{- $needing := list -}}
{{- if .Values.groups.writePath.enabled -}}{{- $needing = append $needing "writePath" -}}{{- end -}}
{{- if .Values.groups.storeLimits.enabled -}}{{- $needing = append $needing "storeLimits" -}}{{- end -}}
{{- join ", " $needing -}}
{{- end -}}

{{/*
A rule's labels: the estate's common labels with this rule's severity on
top, so `severity` can never be shadowed by a common label. An optional
`omit` names one common label to leave off (see `groups.pendingPods.
keepClusterLabel`).
*/}}
{{- define "platform-alerts.labels" -}}
{{- $severity := .severity -}}
{{- $common := deepCopy .root.Values.commonLabels -}}
{{- if .omit -}}{{- $_ := unset $common .omit -}}{{- end -}}
{{- $labels := merge (dict "severity" $severity) $common -}}
{{- toYaml $labels -}}
{{- end -}}

{{/*
A rule's runbook link, or nothing when no base URL is configured. An
annotation with an empty value reads as a broken link, so it is omitted
rather than rendered blank.
*/}}
{{- define "platform-alerts.runbook" -}}
{{- if .root.Values.runbookBaseUrl -}}
runbook_url: {{ printf "%s/%s" (trimSuffix "/" .root.Values.runbookBaseUrl) .alert | quote }}
{{- end -}}
{{- end -}}

{{/*
A deadman for a series that must exist, per cluster.

A bare `absent(sel)` is true only when NO series matches, and a metrics
store can hold several clusters' series: the series vanishing from one
cluster leaves the others' behind, `absent()` stays false, and the outage is
silent. This renders, for a non-empty `clusterLabel`:

  (group by (<cluster>, <by...>) (max_over_time(sel{<cluster>!=""}[<absentLookback>]))
     unless group by (<cluster>, <by...>) (sel{<cluster>!=""}))
  or (absent(sel) unless on() group(max_over_time(sel[<absentLookback>])))

The first line fires once per cluster (and per `by` label value) that HAD
the series within `absentLookback` and no longer does; its result carries
those labels, which the alert keeps (the rule omits the cluster label from
its static labels). Only series that carry the cluster label count there
(`sel{<cluster>!=""}`), so a series written before the cluster label was
attached and since replaced cannot keep it firing for the lookback. The second keeps the whole-store `absent()` for "never
existed / everything gone", and is silenced while the first can still see
the series, so one outage is one alert. Empty `clusterLabel` renders the
bare `absent()` byte for byte. `cur` is a suffix applied to the "now" side
only (`== 1`); `by` lists extra labels to keep per series.
*/}}
{{- define "platform-alerts.absentGuard" -}}
{{- $r := .root -}}
{{- $cur := default "" .cur -}}
{{- if $r.Values.clusterLabel -}}
{{- $l := join ", " (concat (list $r.Values.clusterLabel) (default (list) .by)) -}}
{{- $lb := $r.Values.absentLookback -}}
{{- $sl := printf `%s, %s!=""}` (trimSuffix "}" .sel) $r.Values.clusterLabel -}}
(group by ({{ $l }}) (max_over_time({{ $sl }}[{{ $lb }}])) unless group by ({{ $l }}) ({{ $sl }}{{ $cur }})) or (absent({{ .sel }}{{ $cur }}) unless on() group(max_over_time({{ .sel }}[{{ $lb }}])))
{{- else -}}
absent({{ .sel }}{{ $cur }})
{{- end -}}
{{- end -}}

{{/*
The annotation suffix naming the cluster a per-cluster absent alert is
about; empty for the whole-store alert, whose result has no cluster label.
*/}}
{{- define "platform-alerts.onCluster" -}}
{{- if .Values.clusterLabel -}}{{ printf "{{ with $labels.%s }} on cluster {{ . }}{{ end }}" .Values.clusterLabel }}{{- end -}}
{{- end -}}

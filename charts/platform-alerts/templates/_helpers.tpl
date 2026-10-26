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
The runbook link of a rule whose runbook is a section of one document (the
CloudNativePG runbooks, groups.cnpg.runbookUrl): <url>#<alert, lower-case>,
the anchor a Markdown heading of the alert's name gets. Called with (dict
"root" $ "url" <the document's URL> "alert" <name>); an empty url falls back
to platform-alerts.runbook.
*/}}
{{- define "platform-alerts.runbookAnchor" -}}
{{- if .url -}}
runbook_url: {{ printf "%s#%s" .url (lower .alert) | quote }}
{{- else -}}
{{- include "platform-alerts.runbook" (dict "root" .root "alert" .alert) -}}
{{- end -}}
{{- end -}}

{{/*
A deadman for a series that must exist, per cluster.

A bare `absent(sel)` is true only when NO series matches, and a metrics
store can hold several clusters' series: the series vanishing from one
cluster leaves the others' behind, `absent()` stays false, and the outage is
silent. This renders, for a non-empty `clusterLabel`:

  (group by (<cluster>, <by...>) (max_over_time(sel'[<absentLookback>]))
     unless group by (<cluster>, <by...>) (sel'))
  or (absent(sel) unless on() group(max_over_time(sel[<absentLookback>])))

where sel' is sel with `<cluster>!=""` added. A series without the cluster
label (a stale one from before a relabel, say) would sit in the lookback side
under a label set the current side can never match, and the first line would
stay true until it aged out; requiring the label on both sides leaves it out
of the per-cluster comparison. The whole-store line keeps the plain sel.

The first line fires once per cluster (and per `by` label value) that HAD
the series within `absentLookback` and no longer does; its result carries
those labels, which the alert keeps (the rule omits the cluster label from
its static labels). The second keeps the whole-store `absent()` for "never
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
{{- $sc := printf `%s{%s!=""}` .sel $r.Values.clusterLabel -}}
{{- if hasSuffix "}" .sel -}}{{- $sc = printf `%s, %s!=""}` (trimSuffix "}" .sel) $r.Values.clusterLabel -}}{{- end -}}
(group by ({{ $l }}) (max_over_time({{ $sc }}[{{ $lb }}])) unless group by ({{ $l }}) ({{ $sc }}{{ $cur }})) or (absent({{ .sel }}{{ $cur }}) unless on() group(max_over_time({{ .sel }}[{{ $lb }}])))
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

{{/*
The rules of ONE probe group (`groups.probes`, or one entry of
`groups.probeGroups`): the alert, its deadman and, when `certExpiry` is on,
the certificate alert. Takes `root` (the chart's `$`), `g` (the group, every
key present) and `b` (the aggregation `by (...)` prefix, see vmrule.yaml).
Rendered at column 0; the caller indents it under `rules:`.
*/}}
{{- define "platform-alerts.probeRules" -}}
{{- $r := .root -}}
{{- $g := .g -}}
{{- $b := .b -}}
# An HTTP probe that is not answering 2xx from this cluster.
#
# `probe_success` is the blackbox exporter's own series for a probe
# of `observability-emitters` (`metrics.scrape.probes`): 1 while the
# URL answers as the module accepts (any 2xx by default), 0 for a
# refused connection, a TLS failure or any other status. `max by
# (probe)` so a probe that resolves to several targets is down only
# when all are.
- alert: {{ $g.alertName }}
  expr: >-
    max by ({{ $b }}probe) (probe_success{probe=~"{{ $g.probe }}"}) == 0
  for: {{ $g.for }}
  labels:
    {{- include "platform-alerts.labels" (dict "root" $r "severity" $g.severity) | nindent 4 }}
  annotations:
    {{- if $g.summary }}
    summary: {{ $g.summary | quote }}
    {{- else }}
    summary: "HTTP probe {{`{{ $labels.probe }}`}} has not answered 2xx for {{ $g.for }}"
    {{- end }}
    {{- if $g.description }}
    description: {{ $g.description | quote }}
    {{- else }}
    description: >-
      The probe {{`{{ $labels.probe }}`}} (charts/observability-emitters
      `metrics.scrape.probes`) has read probe_success == 0 for {{ $g.for }}: the URL
      refused the connection, failed TLS or answered something the
      probe's module does not accept (any 2xx, by default) from this
      cluster's own egress.
    {{- end }}
    {{- with (include "platform-alerts.runbook" (dict "root" $r "alert" $g.alertName)) }}{{ . | nindent 4 }}{{- end }}
# The deadman for the rule above.
#
# `probe_success == 0` evaluates to nothing, not to a value, when
# the series does not exist: a probe whose series never reached the
# store (the exporter down, the VMProbe not selected, the series
# filed under labels the store's readers do not see) makes
# {{ $g.alertName }} silent while the probe is not working at all.
# `absent()` is true exactly then. With the default selector it
# fires only when NO probe reports; name a probe (`probe`) to watch
# one, which is what a single-probe estate wants.
- alert: {{ $g.alertName }}Absent
  expr: >-
    {{ include "platform-alerts.absentGuard" (dict "root" $r "sel" (printf `probe_success{probe=~"%s"}` $g.probe) "by" (list "probe")) }}
  for: {{ $g.absentFor }}
  labels:
    {{- include "platform-alerts.labels" (dict "root" $r "severity" (default $g.severity $g.absentSeverity) "omit" $r.Values.clusterLabel) | nindent 4 }}
  annotations:
    summary: "No probe_success series for {{ $g.probe }} for {{ $g.absentFor }}{{ include "platform-alerts.onCluster" $r }}"
    description: >-
      probe_success{probe=~"{{ $g.probe }}"} has not been seen in
      {{ $g.absentFor }}{{ include "platform-alerts.onCluster" $r }}, so {{ $g.alertName }} cannot fire even if the
      URL is down. Check the blackbox exporter and the VMProbe
      (charts/observability-emitters `metrics.scrape.probes`), that the
      agent's scrape of the probe is up, and that its series carry the
      cluster's tenancy labels.
    {{- with (include "platform-alerts.runbook" (dict "root" $r "alert" (printf "%sAbsent" $g.alertName))) }}{{ . | nindent 4 }}{{- end }}
{{- if $g.certExpiry.enabled }}
# The TLS certificate a probe saw expires within `withinDays`.
# `probe_ssl_earliest_cert_expiry` is a Unix time and exists only
# for an https target; the earliest certificate of the chain.
- alert: {{ $g.certExpiry.alertName }}
  expr: >-
    min by ({{ $b }}probe) (probe_ssl_earliest_cert_expiry{probe=~"{{ $g.probe }}"}) - time() < {{ mul (int $g.certExpiry.withinDays) 86400 }}
  for: {{ $g.certExpiry.for }}
  labels:
    {{- include "platform-alerts.labels" (dict "root" $r "severity" $g.certExpiry.severity) | nindent 4 }}
  annotations:
    summary: "The TLS certificate seen by probe {{`{{ $labels.probe }}`}} expires within {{ $g.certExpiry.withinDays }} days"
    description: >-
      The earliest certificate in the chain the probe
      {{`{{ $labels.probe }}`}} (charts/observability-emitters
      `metrics.scrape.probes`) was shown expires in less than
      {{ $g.certExpiry.withinDays }} days.
    {{- with (include "platform-alerts.runbook" (dict "root" $r "alert" $g.certExpiry.alertName)) }}{{ . | nindent 4 }}{{- end }}
{{- end }}
{{- end -}}

{{/*
One `groups.probeGroups` entry with its defaults applied. A map of free keys
cannot carry defaults in values.yaml, so they are filled here. `alertName` and
`probe` have none and are required by the schema: a default name would make two
groups fire as one alert, a default selector would make a group read every probe
in the store.
*/}}
{{- define "platform-alerts.probeGroupDefaults" -}}
{{- $e := .entry -}}
{{- $cert := default (dict) $e.certExpiry -}}
{{- $g := dict "alertName" $e.alertName "probe" $e.probe "for" (default "5m" $e.for) "severity" (default "warning" $e.severity) "summary" (default "" $e.summary) "description" (default "" $e.description) "absentFor" (default "15m" $e.absentFor) "absentSeverity" (default "" $e.absentSeverity) -}}
{{- $_ := set $g "certExpiry" (dict "enabled" (default false $cert.enabled) "alertName" (default (printf "%sCertExpiring" $e.alertName) $cert.alertName) "withinDays" (default 14 $cert.withinDays) "for" (default "1h" $cert.for) "severity" (default "warning" $cert.severity)) -}}
{{- toJson $g -}}
{{- end -}}

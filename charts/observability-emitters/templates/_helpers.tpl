{{/*
Names and labels for the objects this chart renders itself. The log agent
is named by its own chart.
*/}}
{{- define "observability-emitters.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "observability-emitters.fullname" -}}
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

{{- define "observability-emitters.labels" -}}
app.kubernetes.io/name: {{ include "observability-emitters.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: observability-emitters
{{- end -}}

{{- define "observability-emitters.gateway.fullname" -}}
{{- printf "%s-gateway" (include "observability-emitters.fullname" .) | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "observability-emitters.gateway.selectorLabels" -}}
app.kubernetes.io/name: {{ include "observability-emitters.name" . }}-gateway
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end -}}

{{/*
The gateway's pod labels: the selector's two, plus the two that are
descriptive only.

It exists because the chart-wide label set and the gateway's selector both
carry `app.kubernetes.io/name` with DIFFERENT values, so emitting both on
one pod template writes the key twice. YAML takes the last of a duplicate
key rather than refusing it, so the result is a pod template that parses,
applies, and matches its own selector only by luck of ordering.
*/}}
{{- define "observability-emitters.gateway.labels" -}}
{{ include "observability-emitters.gateway.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: observability-emitters
{{- end -}}

{{/*
A Kubernetes label key as the meta label vmagent discovers it under.

`__meta_kubernetes_namespace_label_<name>` is the label key with every
character outside [a-zA-Z0-9_] replaced by an underscore, which is what
turns `tenancy.example.com/project` into
`tenancy_example_com_project`. Written once here rather than in each
relabel rule, because getting it wrong produces a rule that matches
nothing and therefore silently applies the fallback tenant to the whole
cluster.
*/}}
{{- define "observability-emitters.metaLabel" -}}
{{- printf "__meta_kubernetes_namespace_label_%s" (regexReplaceAll "[^a-zA-Z0-9_]" . "_") -}}
{{- end -}}

{{/*
The field the log store carries the tenant in.

vlagent cannot rename a field, so a namespace label arrives under this
name and no other. The chart derives it and then requires it to appear in
the log agent's own `streamFields` — see templates/_validate.tpl, and
docs/safety.md for what it costs.
*/}}
{{- define "observability-emitters.logs.tenantField" -}}
{{- printf "kubernetes.namespace_labels.%s" .Values.tenancy.namespaceLabels.project -}}
{{- end -}}

{{/*
The tenancy stamp, as target relabeling.

Order is the mechanism: each rule overwrites what the one before it set,
so the least specific source is applied first and the most specific wins.
Everything starts with the fallback, a namespace's layer label replaces
it, and a namespace's project label replaces that.

Written as target relabeling rather than as external labels, because
external labels are added at remote-write time and only where the label is
absent — a target that already carries `tenant` would keep its own. Target
relabeling replaces unconditionally, which is the point: an application
does not choose its tenant.

These rules can only see a namespace's labels because the scrape class
they belong to sets `attachMetadata.namespace`, and they reach every
scrape object on the cluster because that class is the default one.
*/}}
{{- define "observability-emitters.tenancy.relabelConfigs" -}}
{{- $t := .Values.tenancy -}}
- target_label: {{ $t.envLabel }}
  replacement: {{ $t.env | quote }}
- target_label: {{ $t.tenantLabel }}
  replacement: {{ $t.fallbackTenant | quote }}
{{- with $t.namespaceLabels.layer }}
- source_labels: [{{ include "observability-emitters.metaLabel" . }}]
  regex: (.+)
  target_label: {{ $t.tenantLabel }}
  replacement: $1
{{- end }}
- source_labels: [{{ include "observability-emitters.metaLabel" $t.namespaceLabels.project }}]
  regex: (.+)
  target_label: {{ $t.tenantLabel }}
  replacement: $1
{{- end -}}

{{/*
The VMAgent spec this chart renders, with `metrics.spec` merged over it.

It is a helper rather than template body so that the refusals in
_validate.tpl can inspect the MERGED result: a check against the values
file alone would miss everything the escape hatch changed, which is
exactly where a security property gets turned off by accident.
*/}}
{{- define "observability-emitters.vmagent.spec" -}}
{{- $root := . -}}
{{- $v := .Values.metrics -}}
{{- $creds := .Values.writeCredentials -}}
{{- $rw := list -}}
{{- range $d := $v.destinations -}}
{{- $entry := dict "url" $d.url -}}
{{- if $creds.secretName -}}
{{- $_ := set $entry "bearerTokenSecret" (dict "name" $creds.secretName "key" $creds.key) -}}
{{- end -}}
{{- $rw = append $rw $entry -}}
{{- end -}}
{{- $scrapeConfigs := list -}}
{{- if $v.scrape.kubelet -}}
{{- $scrapeConfigs = append $scrapeConfigs (include "observability-emitters.scrapeConfig.kubelet" $root) -}}
{{- end -}}
{{- if $v.scrape.cadvisor -}}
{{- $scrapeConfigs = append $scrapeConfigs (include "observability-emitters.scrapeConfig.cadvisor" $root) -}}
{{- end -}}
{{- $base := dict
    "image" (dict "repository" $v.image.repository "tag" $v.image.tag)
    "replicaCount" ($v.replicaCount | int)
    "resources" $v.resources
    "scrapeInterval" (toString $root.Values.interval)
    "selectAllByDefault" true
    "statefulMode" true
    "statefulStorage" (dict "volumeClaimTemplate" (dict "spec" (dict
        "accessModes" (list "ReadWriteOnce")
        "storageClassName" $v.queue.storageClassName
        "resources" (dict "requests" (dict "storage" $v.queue.size)))))
    "overrideHonorLabels" true
    "disableSelfServiceScrape" true
    "remoteWrite" $rw
    "scrapeClasses" (list (dict
        "name" "tenancy"
        "default" true
        "attachMetadata" (dict "namespace" true)
        "relabelConfigs" (fromYamlArray (include "observability-emitters.tenancy.relabelConfigs" $root))))
    "globalScrapeMetricRelabelConfigs" (list (dict
        "action" "labeldrop"
        "regex" (printf "exported_(%s|%s)" $root.Values.tenancy.tenantLabel $root.Values.tenancy.envLabel)))
-}}
{{- if not $v.queue.storageClassName -}}
{{- $_ := unset (index $base "statefulStorage" "volumeClaimTemplate" "spec") "storageClassName" -}}
{{- end -}}
{{- if $scrapeConfigs -}}
{{- $_ := set $base "inlineScrapeConfig" (printf "%s\n" (join "\n" $scrapeConfigs)) -}}
{{- end -}}
{{- toYaml (mergeOverwrite $base (deepCopy ($v.spec | default dict))) -}}
{{- end -}}

{{/*
Tenancy for a target that belongs to no namespace.

The kubelet and cAdvisor run on a node, and a node is cluster-scoped:
there is no namespace to read a label from, so these targets carry the
env and the fallback tenant and nothing else. Writing it out rather than
reusing the namespace rules is the point — those rules would render four
lines that can never match, which reads as a bug for as long as anyone
looks at it.
*/}}
{{- define "observability-emitters.tenancy.clusterScopedRelabelConfigs" -}}
{{- $t := .Values.tenancy -}}
- target_label: {{ $t.envLabel }}
  replacement: {{ $t.env | quote }}
- target_label: {{ $t.tenantLabel }}
  replacement: {{ $t.fallbackTenant | quote }}
{{- end -}}

{{/*
The node's own two endpoints.

They are inline scrape configs rather than scrape objects because nothing
on a cluster owns them: the kubelet is installed by no chart, so there is
no chart whose business it is to author its ServiceMonitor, and the
Prometheus Operator's own answer — a ServiceMonitor against a Service the
operator synthesises — needs an operator this estate does not run.
Everything else this agent collects arrives as a PodMonitor or a
ServiceMonitor written by whoever owns the thing being watched.

`honor_labels: false`, here as everywhere: a kubelet that exported its own
`tenant` label would otherwise keep it.

No series allow-list is applied. A dropped series is invisible until the
first incident that needed it, so narrowing this belongs to an estate that
has measured its own cardinality, not to a default that guesses which
metrics somebody's dashboard uses.
*/}}
{{- define "observability-emitters.scrapeConfig.kubelet" -}}
- job_name: kubelet
  scheme: https
  honor_labels: false
  kubernetes_sd_configs:
    - role: node
  tls_config:
    ca_file: {{ include "observability-emitters.serviceAccountDir" . }}/ca.crt
    insecure_skip_verify: true
  bearer_token_file: {{ include "observability-emitters.serviceAccountDir" . }}/token
  relabel_configs:
    - action: labelmap
      regex: __meta_kubernetes_node_label_(.+)
{{ include "observability-emitters.tenancy.clusterScopedRelabelConfigs" . | indent 4 }}
{{- end -}}

{{- define "observability-emitters.scrapeConfig.cadvisor" -}}
- job_name: cadvisor
  scheme: https
  honor_labels: false
  metrics_path: /metrics/cadvisor
  kubernetes_sd_configs:
    - role: node
  tls_config:
    ca_file: {{ include "observability-emitters.serviceAccountDir" . }}/ca.crt
    insecure_skip_verify: true
  bearer_token_file: {{ include "observability-emitters.serviceAccountDir" . }}/token
  relabel_configs:
    - action: labelmap
      regex: __meta_kubernetes_node_label_(.+)
{{ include "observability-emitters.tenancy.clusterScopedRelabelConfigs" . | indent 4 }}
{{- end -}}

{{/*
Where kubelet's projected service account token is mounted.

Kubernetes' own fixed path, written once. It is mechanism, not a secret
path — the file is the pod's own identity and every pod in every cluster
has it at this address — and hack/leak-canary.sh says so in its header,
because the pattern that catches a parameter-store path also matches this
one.
*/}}
{{- define "observability-emitters.serviceAccountDir" -}}
/var/run/secrets/kubernetes.io/serviceaccount
{{- end -}}

{{- define "observability-emitters.gateway.config" -}}
{{- $root := . -}}
{{- $t := .Values.tenancy -}}
{{- $v := .Values.otlp -}}
{{- $full := include "observability-emitters.gateway.fullname" . -}}
extensions:
  file_storage:
    directory: /var/lib/otelcol/queue
    # The collector is killed with a queue on disk often enough that a
    # corrupt database has to be survivable: without this the process
    # crash-loops on the file it cannot open, and the only way out is
    # deleting the volume — which is the data it was holding.
    recreate: true
    compaction:
      on_start: true
      directory: /var/lib/otelcol/queue
{{- if $v.events.enabled }}
  k8s_leader_elector:
    auth_type: serviceAccount
    lease_name: {{ $full }}
    lease_namespace: {{ $root.Release.Namespace }}
{{- end }}

receivers:
  otlp:
    protocols:
      grpc:
        endpoint: 0.0.0.0:{{ $v.service.grpcPort }}
      http:
        endpoint: 0.0.0.0:{{ $v.service.httpPort }}
{{- if $v.events.enabled }}
  # Kubernetes Events are not container logs and do not come from the log
  # agent: they are an API object, read here. Only the lease holder reads,
  # or every replica ingests every Event.
  k8s_events:
    auth_type: serviceAccount
    k8s_leader_elector: k8s_leader_elector
{{- end }}

processors:
  # Two processors, in this order, and the order is the security property.
  #
  # k8sattributes resolves the sending pod and copies the NAMESPACE's own
  # labels onto the resource, under names of this chart's choosing rather
  # than the estate's. Nothing is decided here.
  #
  # transform/tenancy then writes `{{ $t.tenantLabel }}` and `{{ $t.envLabel }}` from
  # those, unconditionally, with `set` — which overwrites whatever an SDK
  # put there. It finishes by deleting the intermediate attributes, so the
  # namespace's raw labels do not reach the store as a second vocabulary.
  #
  # Doing it the obvious way — letting k8sattributes write `{{ $t.tenantLabel }}`
  # directly — is what this avoids: that processor leaves an attribute that
  # is already present alone, so an SDK that set its own tenant would keep
  # it, and the label would be the application's claim about itself.
  k8sattributes:
    auth_type: serviceAccount
    passthrough: false
    extract:
      metadata:
        - k8s.namespace.name
        - k8s.pod.name
        - k8s.pod.uid
        - k8s.node.name
      labels:
{{- with $t.namespaceLabels.layer }}
        - tag_name: observability.tenancy.layer
          key: {{ . | quote }}
          from: namespace
{{- end }}
        - tag_name: observability.tenancy.project
          key: {{ $t.namespaceLabels.project | quote }}
          from: namespace
    pod_association:
      - sources:
          - from: resource_attribute
            name: k8s.pod.uid
      - sources:
          - from: resource_attribute
            name: k8s.pod.ip
      - sources:
          - from: connection
  transform/tenancy:
    error_mode: ignore
{{- range $signal := list "metric" "log" "trace" }}
    {{ $signal }}_statements:
      - context: resource
        statements:
          - set(attributes[{{ $t.envLabel | quote }}], {{ $t.env | quote }})
          - set(attributes[{{ $t.tenantLabel | quote }}], {{ $t.fallbackTenant | quote }})
{{- if $t.namespaceLabels.layer }}
          - set(attributes[{{ $t.tenantLabel | quote }}], attributes["observability.tenancy.layer"]) where attributes["observability.tenancy.layer"] != nil
{{- end }}
          - set(attributes[{{ $t.tenantLabel | quote }}], attributes["observability.tenancy.project"]) where attributes["observability.tenancy.project"] != nil
          - delete_key(attributes, "observability.tenancy.layer")
          - delete_key(attributes, "observability.tenancy.project")
{{- end }}
  # Delta metrics and deduplication do not mix: the store keeps one sample
  # per interval, and dropping one sample of a delta series loses the
  # increment it carried rather than a repetition of a total. An SDK's
  # default temporality is the caller's business, so the conversion happens
  # here and is not configurable.
  deltatocumulative: {}
  batch: {}

exporters:
{{- range $d := $v.destinations.metrics }}
  # No `sending_queue` here, and it is not an omission: the Prometheus
  # remote-write exporter does not have one. Its durability is a
  # write-ahead log, per exporter, on the same volume the others queue on.
  prometheusremotewrite/{{ $d.name }}:
    endpoint: {{ printf "%s/api/v1/write" (trimSuffix "/" $d.url) | quote }}
    wal:
      directory: /var/lib/otelcol/wal/{{ $d.name }}
    headers:
      Authorization: "Bearer ${env:OBSERVABILITY_WRITE_TOKEN}"
{{- end }}
{{- range $d := $v.destinations.logs }}
  otlphttp/logs-{{ $d.name }}:
    logs_endpoint: {{ printf "%s/insert/opentelemetry/v1/logs" (trimSuffix "/" $d.url) | quote }}
    headers:
      Authorization: "Bearer ${env:OBSERVABILITY_WRITE_TOKEN}"
      # Without this header every resource attribute becomes a stream
      # field, and an SDK's resource carries the pod's UID and start time —
      # so every restart mints a stream the store never reuses.
      VL-Stream-Fields: {{ join "," $v.streamFields | quote }}
    sending_queue:
      enabled: true
      storage: file_storage
    retry_on_failure:
      enabled: true
{{- end }}
{{- range $d := $v.destinations.traces }}
  otlphttp/traces-{{ $d.name }}:
    traces_endpoint: {{ printf "%s/insert/opentelemetry/v1/traces" (trimSuffix "/" $d.url) | quote }}
    headers:
      Authorization: "Bearer ${env:OBSERVABILITY_WRITE_TOKEN}"
    sending_queue:
      enabled: true
      storage: file_storage
    retry_on_failure:
      enabled: true
{{- end }}

service:
  extensions:
    - file_storage
{{- if $v.events.enabled }}
    - k8s_leader_elector
{{- end }}
  telemetry:
    metrics:
      readers:
        - pull:
            exporter:
              prometheus:
                host: 0.0.0.0
                port: 8888
  pipelines:
{{- $metricsExporters := list }}
{{- range $d := $v.destinations.metrics }}{{ $metricsExporters = append $metricsExporters (printf "prometheusremotewrite/%s" $d.name) }}{{ end }}
{{- if $metricsExporters }}
    metrics:
      receivers: [otlp]
      processors: [k8sattributes, transform/tenancy, deltatocumulative, batch]
      exporters: [{{ join ", " $metricsExporters }}]
{{- end }}
{{- $logExporters := list }}
{{- range $d := $v.destinations.logs }}{{ $logExporters = append $logExporters (printf "otlphttp/logs-%s" $d.name) }}{{ end }}
{{- if $logExporters }}
    logs:
      receivers: [otlp{{ if $v.events.enabled }}, k8s_events{{ end }}]
      processors: [k8sattributes, transform/tenancy, batch]
      exporters: [{{ join ", " $logExporters }}]
{{- end }}
{{- $traceExporters := list }}
{{- range $d := $v.destinations.traces }}{{ $traceExporters = append $traceExporters (printf "otlphttp/traces-%s" $d.name) }}{{ end }}
{{- if $traceExporters }}
    traces:
      receivers: [otlp]
      processors: [k8sattributes, transform/tenancy, batch]
      exporters: [{{ join ", " $traceExporters }}]
{{- end }}
{{- end -}}

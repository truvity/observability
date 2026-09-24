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
The scoping-key stamp, as target relabeling.

The names are literals here and in every other stamping site, on purpose:
they are OpenTelemetry's, spelled without dots because a Prometheus label
cannot carry one, and `pkg/tenancy` filters on the same literals. A name
that could be configured is a name that could stop matching the filter,
which is an empty result rather than an error. tests/agreement_test.go
reads these back out of the rendered manifest and fails on any drift.

Written as target relabeling rather than as external labels, because
external labels are added at remote-write time and only where the label is
absent — a target that already carries `k8s_cluster_name` would keep its
own. Target relabeling replaces unconditionally, which is the point: an
application does not choose its cluster or its namespace.

The namespace comes from `__meta_kubernetes_namespace`, which service
discovery carries for every namespaced role without being asked; the
cluster and the tier are constants for the cluster. The Helm release is
passed through from the pod's own label for navigation, and is not a key.

These rules reach every scrape object on the cluster because the scrape
class they belong to is the default one.
*/}}
{{- define "observability-emitters.tenancy.relabelConfigs" -}}
{{- $t := .Values.tenancy -}}
- target_label: k8s_cluster_name
  replacement: {{ $t.cluster | quote }}
- target_label: deployment_environment_name
  replacement: {{ $t.environment | quote }}
- source_labels: [__meta_kubernetes_namespace]
  target_label: k8s_namespace_name
- source_labels: [__meta_kubernetes_pod_label_app_kubernetes_io_instance]
  regex: (.+)
  target_label: app_kubernetes_io_instance
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
        "relabelConfigs" (fromYamlArray (include "observability-emitters.tenancy.relabelConfigs" $root))))
    "globalScrapeMetricRelabelConfigs" (list (dict
        "action" "labeldrop"
        "regex" "exported_(k8s_cluster_name|k8s_namespace_name|deployment_environment_name)"))
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
The stamp for a target that belongs to no namespace.

The kubelet and cAdvisor run on a node, and a node is cluster-scoped:
there is no namespace in service discovery, so these targets carry the
cluster and the tier as target labels and nothing else. Writing it out
rather than reusing the namespace rules is the point — those rules would
render lines that can never match, which reads as a bug for as long as
anyone looks at it.

The series themselves do carry a namespace, though: cAdvisor labels every
container series with the pod's `namespace`, and the kubelet does the same
for its per-pod gauges. That is copied into the key at metric-relabel
time, after the scrape, so that a grant on a namespace reaches the
container metrics of that namespace. It is the target's own statement
about which namespace a container runs in, and the target is the node
agent, which is the platform's.
*/}}
{{- define "observability-emitters.tenancy.clusterScopedRelabelConfigs" -}}
{{- $t := .Values.tenancy -}}
- target_label: k8s_cluster_name
  replacement: {{ $t.cluster | quote }}
- target_label: deployment_environment_name
  replacement: {{ $t.environment | quote }}
{{- end -}}

{{- define "observability-emitters.tenancy.nodeMetricRelabelConfigs" -}}
- source_labels: [namespace]
  regex: (.+)
  target_label: k8s_namespace_name
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
`k8s_cluster_name` label would otherwise keep it.

No series allow-list is applied. A dropped series is invisible until the
first incident that needed it, so narrowing this belongs to an estate that
has measured its own cardinality, not to a default that guesses which
metrics somebody's dashboard uses.

LABELS are a different question from series, and this is where that was
learned. Copying every node label onto every node metric -- `labelmap` over
`__meta_kubernetes_node_label_(.+)`, which is the conventional snippet --
is unbounded by construction: the labels belong to the cloud provider, not
to this chart. On EKS a node carries around forty of them
(`eks_amazonaws_com_instance_*`, karpenter, topology), so every kubelet and
cadvisor series arrived with 46 to 52 labels, past VictoriaMetrics'
`-maxLabelsPerTimeseries=40`.

The store then IGNORED those series and answered 200. The agent reported
888k rows written, zero errors, zero dropped; the store held none of them;
and the only record was a warning in the store's log. Measured on a live
cluster, because nothing else can see it.

So the node identity is one label, `node`, and anything further is asked
for by name through `metrics.scrape.nodeLabels`.
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
{{ include "observability-emitters.nodeLabelRelabelConfigs" . | indent 4 }}
{{ include "observability-emitters.tenancy.clusterScopedRelabelConfigs" . | indent 4 }}
  metric_relabel_configs:
{{ include "observability-emitters.tenancy.nodeMetricRelabelConfigs" . | indent 4 }}
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
{{ include "observability-emitters.nodeLabelRelabelConfigs" . | indent 4 }}
{{ include "observability-emitters.tenancy.clusterScopedRelabelConfigs" . | indent 4 }}
  metric_relabel_configs:
{{ include "observability-emitters.tenancy.nodeMetricRelabelConfigs" . | indent 4 }}
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
    # A fresh volume is empty, and the extension refuses to start on a
    # directory that does not exist rather than creating it — so without
    # this every first boot on a new PersistentVolume crash-loops before
    # the first byte is queued. Found by running the rendered file
    # against the binary; no render can tell you a directory is missing.
    create_directory: true
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
  # Three processors, in this order, and the order is the security
  # property.
  #
  # transform/disown removes the namespace an SDK may have stated about
  # itself, under both spellings. It has to run FIRST, because
  # k8sattributes writes an attribute only when it is absent or empty —
  # so a resource that arrived already carrying `k8s.namespace.name`
  # would keep the application's claim, and the namespace is the key.
  #
  # k8sattributes then resolves the sending pod and writes the NAMESPACE's
  # name from the pod object, not from anything the sender said. The
  # cluster and the tier are constants from this values file; the
  # processor cannot know either.
  #
  # transform/tenancy finishes by writing those two with `set`, which
  # overwrites whatever an SDK put there, and on the log pipeline copies
  # the namespace to the spelling the container-log agent uses, so one
  # store holds one name for it.
  transform/disown:
    error_mode: ignore
{{- range $signal := list "metric" "log" "trace" }}
    {{ $signal }}_statements:
      - context: resource
        statements:
          - delete_key(attributes, "k8s.namespace.name")
          - delete_key(attributes, "kubernetes.pod_namespace")
{{- end }}
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
        # The Helm release, for navigation. Named explicitly rather than
        # left to the processor's default pattern, so the attribute a
        # reader searches for is the one the rendered file says.
        - tag_name: k8s.pod.labels.app.kubernetes.io/instance
          key: app.kubernetes.io/instance
          from: pod
    # The connection comes first. The other two sources read the pod's
    # identity from attributes the SENDER supplied, which for an
    # in-cluster application is a claim about itself; the peer address of
    # the socket it opened is not. They remain for a sender whose address
    # resolves to no pod — a host-network pod carries its node's.
    pod_association:
      - sources:
          - from: connection
      - sources:
          - from: resource_attribute
            name: k8s.pod.uid
      - sources:
          - from: resource_attribute
            name: k8s.pod.ip
  transform/tenancy:
    error_mode: ignore
{{- range $signal := list "metric" "log" "trace" }}
    {{ $signal }}_statements:
      - context: resource
        statements:
          - set(attributes["k8s.cluster.name"], {{ $t.cluster | quote }})
          - set(attributes["deployment.environment.name"], {{ $t.environment | quote }})
{{- if eq $signal "log" }}
          # The container-log agent cannot rename a field, so its native
          # spelling is the log-path key and this writer yields to it.
          - set(attributes["kubernetes.pod_namespace"], attributes["k8s.namespace.name"]) where attributes["k8s.namespace.name"] != nil
{{- end }}
{{- end }}
  # Delta metrics and deduplication do not mix: the store keeps one sample
  # per interval, and dropping one sample of a delta series loses the
  # increment it carried rather than a repetition of a total. An SDK's
  # default temporality is the caller's business, so the conversion happens
  # here and is not configurable.
  delta_to_cumulative: {}
  batch: {}

exporters:
{{- range $d := $v.destinations.metrics }}
  # No `sending_queue` here, and it is not an omission: the Prometheus
  # remote-write exporter does not have one. Its durability is a
  # write-ahead log, per exporter, on the same volume the others queue on.
  prometheus_remote_write/{{ $d.name }}:
    endpoint: {{ printf "%s/api/v1/write" (trimSuffix "/" $d.url) | quote }}
    wal:
      directory: /var/lib/otelcol/wal/{{ $d.name }}
    headers:
      Authorization: "Bearer ${env:OBSERVABILITY_WRITE_TOKEN}"
    # A resource attribute does not become a label on its own: this
    # exporter puts the resource on a `target_info` series and nothing
    # else, so a series would reach the store carrying no cluster and no
    # namespace, and every scoped query would miss it. These three are
    # promoted onto every series, under the dotted names — the exporter
    # spells them with underscores on the way out, which is how they
    # match the metrics agent's labels and the proxy's filters. Only
    # these three: the rest of the resource, the pod UID among it, stays
    # on `target_info`.
    resource_constant_labels:
      included:
        - k8s.cluster.name
        - k8s.namespace.name
        - deployment.environment.name
{{- end }}
{{- range $d := $v.destinations.logs }}
  otlp_http/logs-{{ $d.name }}:
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
  otlp_http/traces-{{ $d.name }}:
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
{{- range $d := $v.destinations.metrics }}{{ $metricsExporters = append $metricsExporters (printf "prometheus_remote_write/%s" $d.name) }}{{ end }}
{{- if $metricsExporters }}
    metrics:
      receivers: [otlp]
      processors: [transform/disown, k8sattributes, transform/tenancy, delta_to_cumulative, batch]
      exporters: [{{ join ", " $metricsExporters }}]
{{- end }}
{{- $logExporters := list }}
{{- range $d := $v.destinations.logs }}{{ $logExporters = append $logExporters (printf "otlp_http/logs-%s" $d.name) }}{{ end }}
{{- if $logExporters }}
    logs:
      receivers: [otlp{{ if $v.events.enabled }}, k8s_events{{ end }}]
      processors: [transform/disown, k8sattributes, transform/tenancy, batch]
      exporters: [{{ join ", " $logExporters }}]
{{- end }}
{{- $traceExporters := list }}
{{- range $d := $v.destinations.traces }}{{ $traceExporters = append $traceExporters (printf "otlp_http/traces-%s" $d.name) }}{{ end }}
{{- if $traceExporters }}
    traces:
      receivers: [otlp]
      processors: [transform/disown, k8sattributes, transform/tenancy, batch]
      exporters: [{{ join ", " $traceExporters }}]
{{- end }}
{{- end -}}

{{/*
The node's identity, and only the labels asked for by name.

`node` is the conventional name for it and the one every dashboard and
recording rule joins on. Each entry in `metrics.scrape.nodeLabels` is a
node label copied under its own sanitized name -- opt in, because each one
lands on EVERY node series and the ceiling is the store's, not this
chart's.
*/}}
{{- define "observability-emitters.nodeLabelRelabelConfigs" -}}
- source_labels: [__meta_kubernetes_node_name]
  target_label: node
{{- range .Values.metrics.scrape.nodeLabels }}
- source_labels: [__meta_kubernetes_node_label_{{ . | replace "." "_" | replace "/" "_" | replace "-" "_" }}]
  target_label: {{ . | replace "." "_" | replace "/" "_" | replace "-" "_" }}
{{- end }}
{{- end -}}

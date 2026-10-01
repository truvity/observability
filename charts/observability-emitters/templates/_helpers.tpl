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
The owner stamp (opt-in: `tenancy.owners`).

`tenancy.owners` maps an owner to the namespace patterns it owns. A
pattern is a namespace name where `*` stands for any run of namespace
characters (`team-*`); this turns one into the RE2 fragment the relabel
rules and the gateway's OTTL both anchor.
*/}}
{{- define "observability-emitters.owners.regex" -}}
{{- $parts := list -}}
{{- range $p := . -}}
{{- $parts = append $parts (replace "*" "[a-z0-9-]*" (toString $p)) -}}
{{- end -}}
{{- printf "(?:%s)" (join "|" $parts) -}}
{{- end -}}

{{/*
The owner stamp as the metrics agent's GLOBAL relabeling (`inlineRelabelConfig`,
VictoriaMetrics' `-remoteWrite.relabelConfig`): applied to every series
after scrape-level relabeling and immediately before it is sent. That is
the one point where `k8s_namespace_name` is final: kube-state-metrics,
cAdvisor and the kubelet each re-derive it from the OBJECT's namespace in
their own metric relabeling, so a rule written earlier (in the scrape
class) would read the exporter pod's namespace and be wrong for exactly
the series that matter.

Order: clear any `owner` the series arrived with (an application does not
choose its owner), then one rule per owner that only fires while `owner`
is still empty (the guard is the trailing `;` on the joined source), so
the first owner in alphabetical order wins on an overlapping glob, then
the default for whatever is still empty.
*/}}
{{- define "observability-emitters.owners.metricRelabelConfigs" -}}
{{- $t := .Values.tenancy -}}
- action: labeldrop
  regex: owner
{{- range $owner, $patterns := $t.owners }}
- action: replace
  source_labels: [k8s_namespace_name, owner]
  separator: ";"
  regex: {{ printf "%s;" (include "observability-emitters.owners.regex" $patterns) | quote }}
  target_label: owner
  replacement: {{ $owner | quote }}
{{- end }}
{{- if $t.defaultOwner }}
- action: replace
  source_labels: [owner]
  regex: "^$"
  target_label: owner
  replacement: {{ $t.defaultOwner | quote }}
{{- end }}
{{- end -}}

{{/*
The same stamp for the OTLP gateway, as OTTL statements on the resource,
written after `k8sattributes` has resolved the namespace from the pod
object. One statement per owner, each guarded on `owner` still being
unset, then the default.
*/}}
{{- define "observability-emitters.owners.ottl" -}}
{{- $t := .Values.tenancy -}}
{{- range $owner, $patterns := $t.owners }}
- set(attributes["owner"], {{ $owner | quote }}) where attributes["owner"] == nil and attributes["k8s.namespace.name"] != nil and IsMatch(attributes["k8s.namespace.name"], {{ printf "^%s$" (include "observability-emitters.owners.regex" $patterns) | quote }})
{{- end }}
{{- if $t.defaultOwner }}
- set(attributes["owner"], {{ $t.defaultOwner | quote }}) where attributes["owner"] == nil
{{- end }}
{{- end -}}

{{/*
`remote`'s effective state: whether it is in use, and the one
destination entry it expands into for a given signal (before that
signal's own low-level list is consulted at all).

Everything downstream reads THESE helpers rather than `.Values.metrics.
destinations` / `.Values.otlp.destinations.*` / `.Values.writeCredentials`
directly — vmagent.spec, gateway.config, the CA-volume helpers below and
_validate.tpl's own destination/credential checks all go through here,
so `remote` and the low-level form can never drift into two different
answers about what actually gets written where.

Unlike `victoria-logs-collector.remoteWrite` (a real Helm subchart's own
values — see `remote`'s own values.yaml comment for why THAT one stays
untouched), `metrics.destinations`, `otlp.destinations.*` and
`writeCredentials` are this chart's OWN values, computable at render
time like anything else it renders itself.
*/}}
{{- define "observability-emitters.remote.inUse" -}}
{{- $remote := .Values.remote | default dict -}}
{{- ne (trimSuffix "" (toString ($remote.url | default ""))) "" -}}
{{- end -}}

{{- define "observability-emitters.remote.signals" -}}
{{- $remote := .Values.remote | default dict -}}
{{- $signals := $remote.signals -}}
{{- if not $signals -}}
{{- $signals = list "metrics" "logs" "traces" -}}
{{- end -}}
{{- toYaml $signals -}}
{{- end -}}

{{/*
The effective write credential: `remote.tokenSecret` once `remote` is
in use (refused together with a non-empty `writeCredentials.
secretName` — see _validate.tpl), `writeCredentials` otherwise.
*/}}
{{- define "observability-emitters.effectiveWriteCredentials" -}}
{{- if eq (include "observability-emitters.remote.inUse" .) "true" -}}
{{- $remote := .Values.remote -}}
{{- toYaml (dict "secretName" $remote.tokenSecret.name "key" $remote.tokenSecret.key) -}}
{{- else -}}
{{- toYaml .Values.writeCredentials -}}
{{- end -}}
{{- end -}}

{{/*
`metrics.destinations`, effective: `remote`'s own one-entry list when
`remote` is in use and "metrics" is one of its `signals` — the metrics
agent's own `/api/v1/write` suffix appended here, the one place that
suffix is added rather than asked of the caller — or the low-level list
otherwise.
*/}}
{{- define "observability-emitters.effectiveMetricsDestinations" -}}
{{- $signals := include "observability-emitters.remote.signals" . | fromYamlArray -}}
{{- if and (eq (include "observability-emitters.remote.inUse" .) "true") (has "metrics" $signals) -}}
{{- $remote := .Values.remote -}}
{{- $entry := dict "name" $remote.name "url" (printf "%s/api/v1/write" (trimSuffix "/" $remote.url)) -}}
{{- if ($remote.caSecret).name -}}
{{- $_ := set $entry "caSecret" $remote.caSecret -}}
{{- end -}}
{{- toYaml (list $entry) -}}
{{- else -}}
{{- toYaml .Values.metrics.destinations -}}
{{- end -}}
{{- end -}}

{{/*
`otlp.destinations.<signal>`, effective: `remote`'s own one-entry list
when `remote` is in use and `signal` is one of its `signals` — the base
URL as-is, since every OTLP exporter already appends its own path — or
the low-level list otherwise. Called once per signal:
`(dict "root" . "signal" "metrics"|"logs"|"traces")`.
*/}}
{{- define "observability-emitters.effectiveOtlpDestinations" -}}
{{- $root := .root -}}
{{- $signal := .signal -}}
{{- $signals := include "observability-emitters.remote.signals" $root | fromYamlArray -}}
{{- if and (eq (include "observability-emitters.remote.inUse" $root) "true") (has $signal $signals) -}}
{{- $remote := $root.Values.remote -}}
{{- $entry := dict "name" $remote.name "url" $remote.url -}}
{{- if ($remote.caSecret).name -}}
{{- $_ := set $entry "caSecret" $remote.caSecret -}}
{{- end -}}
{{- toYaml (list $entry) -}}
{{- else -}}
{{- toYaml (index $root.Values.otlp.destinations $signal) -}}
{{- end -}}
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
{{- $creds := include "observability-emitters.effectiveWriteCredentials" . | fromYaml -}}
{{- $rw := list -}}
{{- range $d := (include "observability-emitters.effectiveMetricsDestinations" . | fromYamlArray) -}}
{{- $entry := dict "url" $d.url -}}
{{- if $creds.secretName -}}
{{- $_ := set $entry "bearerTokenSecret" (dict "name" $creds.secretName "key" $creds.key) -}}
{{- end -}}
{{- /*
A destination's own CA, for a `url` whose certificate is not publicly
trusted. The operator mounts a `SecretOrConfigMap` itself — unlike the
OTLP gateway below, this chart writes no volume for it.
*/ -}}
{{- if $d.caSecret -}}
{{- $_ := set $entry "tlsConfig" (dict "ca" (dict "secret" (dict "name" $d.caSecret.name "key" $d.caSecret.key))) -}}
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
    "securityContext" (dict
        "runAsNonRoot" true
        "runAsUser" 65534
        "runAsGroup" 65534
        "fsGroup" 65534
        "fsGroupChangePolicy" "OnRootMismatch"
        "seccompProfile" (dict "type" "RuntimeDefault")
        "allowPrivilegeEscalation" false
        "capabilities" (dict "drop" (list "ALL")))
    "remoteWrite" $rw
    "scrapeClasses" (list (dict
        "name" "tenancy"
        "default" true
        "relabelConfigs" (fromYamlArray (include "observability-emitters.tenancy.relabelConfigs" $root))))
    "globalScrapeMetricRelabelConfigs" (list (dict
        "action" "labeldrop"
        "regex" "exported_(k8s_cluster_name|k8s_namespace_name|deployment_environment_name)"))
-}}
{{- if $root.Values.tenancy.owners -}}
{{- $_ := set $base "inlineRelabelConfig" (fromYamlArray (include "observability-emitters.owners.metricRelabelConfigs" $root)) -}}
{{- end -}}
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
The cadvisor scrape's own default churn drop — cadvisor only, never the
kubelet job. See `metrics.scrape.cadvisorDrop` in values.yaml for the
measurement and the override shape (replace each list wholesale, or add
to it through the paired `extra*` field, or turn the whole thing off).

Three steps, in order:

  1. Drop the exact names in `metricNames` (+ `extraMetricNames`)
     outright.
  2. Drop every `_bucket` series EXCEPT the names in `keepBucketMetrics`
     (+ `extraKeepBucketMetrics`). RE2 (what Prometheus/vmagent relabel
     regexes compile with) has no negative lookahead, so "all `_bucket`
     series but these" is two rules rather than one: a `replace` stamps
     a scratch label on the kept names ONLY, then a `drop` matches
     `<name>;<scratch>` against a pattern that only a `_bucket` name
     with an EMPTY scratch label satisfies — which is every `_bucket`
     series except the ones just stamped — and a `labeldrop` removes
     the scratch label from whatever survives.
  3. Clear the cgroup-path `id` label, but ONLY on a series that
     already carries a non-empty `container` label — NOT an
     unconditional `labeldrop`. cadvisor also exports node-level
     cgroups that are neither a pod nor a container — the root (`id:
     "/"`), the pod-manager slice (`/kubepods.slice` and its QoS
     children), systemd units (`/system.slice/containerd.service`,
     `/system.slice/kubelet.service`), and more — and EVERY one of
     those carries `container=""` and `pod=""`: `id` is their ONLY
     distinguishing label. A blanket `labeldrop` merges every one of
     them, on one node, into ONE identical label set, and
     vmagent/vmsingle deduplication then keeps an arbitrary sample —
     silent data corruption, not a churn saving, and the exact defect
     this chart shipped once before a review caught it against a
     fixture that happened to carry only a container-level series.
     `container` non-empty is what makes `id` redundant: kubelet
     guarantees at most one container of a given name in a given pod
     at a time, so (`namespace`,`pod`,`container`) already uniquely
     names the series once `container` is set, and `id` adds nothing a
     query could not already get from the three. A pod-level rollup
     (`pod` set, `container` empty — cadvisor's own per-pod network
     counters, for instance) is left with `id` untouched, on the same
     reasoning run the other way: nothing already identifies it
     without `id`, and (`namespace`,`pod`) is not enough on its own to
     rule out a second, unrelated cgroup this chart has not measured.
     Verified against every dashboard this repository ships
     (charts/observability-dashboards) and every rule
     (charts/platform-alerts): none of them groups, filters or joins on
     `id`. `pod`, `namespace`, `container` and `uid` are not touched.

An empty `metricNames`/`keepBucketMetrics` after a consumer's own
override is valid: an empty step 1 drops nothing by name, and an empty
`keepBucketMetrics` drops every `_bucket` series with no exception.
*/}}
{{- define "observability-emitters.scrapeConfig.cadvisorChurnDropMetricRelabelConfigs" -}}
{{- $cd := .Values.metrics.scrape.cadvisorDrop -}}
{{- if $cd.enabled -}}
{{- $names := concat ($cd.metricNames | default list) ($cd.extraMetricNames | default list) -}}
{{- $keep := concat ($cd.keepBucketMetrics | default list) ($cd.extraKeepBucketMetrics | default list) -}}
{{- $steps := list -}}
{{- if $names -}}
{{- $steps = append $steps (printf "- action: drop\n  source_labels: [__name__]\n  regex: ^(%s)$" (join "|" $names)) -}}
{{- end -}}
{{- if $keep -}}
{{- $steps = append $steps (printf "- action: replace\n  source_labels: [__name__]\n  regex: ^(%s)$\n  target_label: __cadvisor_keep_bucket__\n  replacement: \"yes\"" (join "|" $keep)) -}}
{{- end -}}
{{- $steps = append $steps "- action: drop\n  source_labels: [__name__, __cadvisor_keep_bucket__]\n  separator: \";\"\n  regex: ^.*_bucket;$" -}}
{{- $steps = append $steps "- action: labeldrop\n  regex: __cadvisor_keep_bucket__" -}}
{{- $steps = append $steps "- action: replace\n  source_labels: [container, id]\n  regex: (.+);.+\n  target_label: id\n  replacement: \"\"" -}}
{{- join "\n" $steps -}}
{{- end -}}
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
{{/*
The `metrics_path` label, the way kube-prometheus writes it.

kube-prometheus copies the scrape path onto every kubelet and cadvisor
series (`__metrics_path__` into `metrics_path`), and the kubernetes-mixin
dashboards and rules select on it: the kubelet dashboard's own `cluster`
variable is `up{job="kubelet", metrics_path="/metrics"}`, so without the
label that variable is empty and every panel of the dashboard says "No
data" while the series are in the store. Both jobs here scrape the node
directly (no API-server proxy), so `__metrics_path__` holds exactly what
the mixin expects: `/metrics` for the kubelet job (the default path) and
`/metrics/cadvisor` for the cadvisor job. One constant value per job, so no
series is added.
*/}}
{{- define "observability-emitters.nodeMetricsPathRelabelConfig" -}}
- source_labels: [__metrics_path__]
  target_label: metrics_path
{{- end -}}

{{/*
The cadvisor series' `job` label, the way kube-prometheus writes it.

kube-prometheus scrapes cadvisor from the kubelet's own service, so every
cadvisor series carries `job="kubelet", metrics_path="/metrics/cadvisor"`,
and the kubernetes-mixin recording rules (`k8s.rules.container_*`) and
dashboards select exactly that. Under a job of its own the rules could never
match a series. The vmagent-internal `job_name` stays `cadvisor`, because
vmagent needs unique names and this keeps the churn drop and the scrape's
own logs and `/targets` page addressable; only the STORED label changes,
by a relabel step (relabel_configs run after `job` is set from the name, so
`replace` wins). The two node jobs stay distinguishable in the store by
`metrics_path`, which the step above set, and `up` carries both.
`metrics.scrape.cadvisorAsKubeletJob: false` leaves `job="cadvisor"`.
*/}}
{{- define "observability-emitters.cadvisorJobLabelRelabelConfig" -}}
- target_label: job
  replacement: kubelet
{{- end -}}

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
{{ include "observability-emitters.nodeMetricsPathRelabelConfig" . | indent 4 }}
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
{{ include "observability-emitters.nodeMetricsPathRelabelConfig" . | indent 4 }}
{{- if .Values.metrics.scrape.cadvisorAsKubeletJob }}
{{ include "observability-emitters.cadvisorJobLabelRelabelConfig" . | indent 4 }}
{{- end }}
  metric_relabel_configs:
{{ include "observability-emitters.tenancy.nodeMetricRelabelConfigs" . | indent 4 }}
{{- with (include "observability-emitters.scrapeConfig.cadvisorChurnDropMetricRelabelConfigs" .) }}
{{ . | indent 4 }}
{{- end }}
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
{{- $effMetricsDestinations := include "observability-emitters.effectiveOtlpDestinations" (dict "root" $root "signal" "metrics") | fromYamlArray -}}
{{- $effLogsDestinations := include "observability-emitters.effectiveOtlpDestinations" (dict "root" $root "signal" "logs") | fromYamlArray -}}
{{- $effTracesDestinations := include "observability-emitters.effectiveOtlpDestinations" (dict "root" $root "signal" "traces") | fromYamlArray -}}
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
{{- if $t.owners }}
          - delete_key(attributes, "owner")
      - context: {{ ternary "datapoint" (ternary "log" "span" (eq $signal "log")) (eq $signal "metric") }}
        statements:
          - delete_key(attributes, "owner")
{{- end }}
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
{{- if $t.owners }}
{{- include "observability-emitters.owners.ottl" $root | trim | nindent 10 }}
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
{{- range $d := $effMetricsDestinations }}
  # No `sending_queue` here, and it is not an omission: the Prometheus
  # remote-write exporter does not have one. Its durability is a
  # write-ahead log, per exporter, on the same volume the others queue on.
  prometheus_remote_write/{{ $d.name }}:
    endpoint: {{ printf "%s/api/v1/write" (trimSuffix "/" $d.url) | quote }}
    wal:
      directory: /var/lib/otelcol/wal/{{ $d.name }}
    headers:
      Authorization: "Bearer ${env:OBSERVABILITY_WRITE_TOKEN}"
    {{- if $d.caSecret }}
    tls:
      ca_file: {{ include "observability-emitters.otlp.caFile" (dict "signal" "metrics" "name" $d.name) | quote }}
    {{- end }}
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
{{- if $t.owners }}
        - owner
{{- end }}
{{- end }}
{{- range $d := $effLogsDestinations }}
  otlp_http/logs-{{ $d.name }}:
    logs_endpoint: {{ printf "%s/insert/opentelemetry/v1/logs" (trimSuffix "/" $d.url) | quote }}
    headers:
      Authorization: "Bearer ${env:OBSERVABILITY_WRITE_TOKEN}"
      # Without this header every resource attribute becomes a stream
      # field, and an SDK's resource carries the pod's UID and start time —
      # so every restart mints a stream the store never reuses.
      VL-Stream-Fields: {{ join "," $v.streamFields | quote }}
    {{- if $d.caSecret }}
    tls:
      ca_file: {{ include "observability-emitters.otlp.caFile" (dict "signal" "logs" "name" $d.name) | quote }}
    {{- end }}
    sending_queue:
      enabled: true
      storage: file_storage
    retry_on_failure:
      enabled: true
{{- end }}
{{- range $d := $effTracesDestinations }}
  otlp_http/traces-{{ $d.name }}:
    traces_endpoint: {{ printf "%s/insert/opentelemetry/v1/traces" (trimSuffix "/" $d.url) | quote }}
    headers:
      Authorization: "Bearer ${env:OBSERVABILITY_WRITE_TOKEN}"
    {{- if $d.caSecret }}
    tls:
      ca_file: {{ include "observability-emitters.otlp.caFile" (dict "signal" "traces" "name" $d.name) | quote }}
    {{- end }}
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
{{- range $d := $effMetricsDestinations }}{{ $metricsExporters = append $metricsExporters (printf "prometheus_remote_write/%s" $d.name) }}{{ end }}
{{- if $metricsExporters }}
    metrics:
      receivers: [otlp]
      processors: [transform/disown, k8sattributes, transform/tenancy, delta_to_cumulative, batch]
      exporters: [{{ join ", " $metricsExporters }}]
{{- end }}
{{- $logExporters := list }}
{{- range $d := $effLogsDestinations }}{{ $logExporters = append $logExporters (printf "otlp_http/logs-%s" $d.name) }}{{ end }}
{{- if $logExporters }}
    logs:
      receivers: [otlp{{ if $v.events.enabled }}, k8s_events{{ end }}]
      processors: [transform/disown, k8sattributes, transform/tenancy, batch]
      exporters: [{{ join ", " $logExporters }}]
{{- end }}
{{- $traceExporters := list }}
{{- range $d := $effTracesDestinations }}{{ $traceExporters = append $traceExporters (printf "otlp_http/traces-%s" $d.name) }}{{ end }}
{{- if $traceExporters }}
    traces:
      receivers: [otlp]
      processors: [transform/disown, k8sattributes, transform/tenancy, batch]
      exporters: [{{ join ", " $traceExporters }}]
{{- end }}
{{- end -}}

{{/*
A destination's own CA, for the OTLP gateway.

The gateway is an OpenTelemetry Collector, which has no Kubernetes API
access and no concept of a Secret: an exporter's `tls.ca_file` is a path
on disk, so a CA this chart is handed as a Secret name has to become a
mounted file before an exporter can point at it. These three agree on
one path per destination — the file (`caFile`), the volume that provides
it (`caVolumes`), and the mount that puts it there (`caVolumeMounts`) —
so a `caSecret` on one destination cannot drift from where its own
exporter looks for it.

Each destination with `caSecret` set gets its OWN volume, named for the
signal and the destination together: two destinations naming the same
Secret still each get their own mount, which costs one more Secret
projection and buys not needing to reason about whether two destinations
sharing a volume could ever disagree about what THAT volume holds.
*/}}
{{- define "observability-emitters.otlp.caFile" -}}
{{- printf "/etc/observability-emitters/ca/%s-%s/ca.crt" .signal (.name | trunc 40 | trimSuffix "-") -}}
{{- end -}}

{{- define "observability-emitters.otlp.caVolumeName" -}}
{{- printf "ca-%s-%s" .signal .name | trunc 63 | trimSuffix "-" -}}
{{- end -}}

{{- define "observability-emitters.otlp.caVolumes" -}}
{{- $root := . -}}
{{- range $signal := (list "metrics" "logs" "traces") }}
{{- range $d := (include "observability-emitters.effectiveOtlpDestinations" (dict "root" $root "signal" $signal) | fromYamlArray) }}
{{- if $d.caSecret }}
- name: {{ include "observability-emitters.otlp.caVolumeName" (dict "signal" $signal "name" $d.name) }}
  secret:
    secretName: {{ $d.caSecret.name | quote }}
    items:
      - key: {{ $d.caSecret.key | quote }}
        path: ca.crt
{{- end }}
{{- end }}
{{- end }}
{{- end -}}

{{- define "observability-emitters.otlp.caVolumeMounts" -}}
{{- $root := . -}}
{{- range $signal := (list "metrics" "logs" "traces") }}
{{- range $d := (include "observability-emitters.effectiveOtlpDestinations" (dict "root" $root "signal" $signal) | fromYamlArray) }}
{{- if $d.caSecret }}
- name: {{ include "observability-emitters.otlp.caVolumeName" (dict "signal" $signal "name" $d.name) }}
  mountPath: {{ printf "/etc/observability-emitters/ca/%s-%s" $signal ($d.name | trunc 40 | trimSuffix "-") }}
  readOnly: true
{{- end }}
{{- end }}
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

{{/*
kube-state-metrics' own naming, replicated from its vendored
_helpers.tpl (`kube-state-metrics.fullname` / `.serviceAccountName` /
`.crsConfigMapName`) — a parent chart cannot `include` a subchart's own
define by name (Helm scopes template names per chart), so this is the
same handful of lines, read from THIS chart's namespaced view of the
subchart's values, rather than a second helper that could drift from
what the subchart itself computes. Needed only because
`kubeStateMetrics.customResources` renders objects of its OWN (a
ConfigMap the subchart's Deployment mounts by name, a ClusterRole bound
to the subchart's own ServiceAccount) that must agree with names Helm
computed for a chart this one does not template.
*/}}
{{- define "observability-emitters.kubeStateMetrics.fullname" -}}
{{- $ksm := index .Values "kube-state-metrics" -}}
{{- if $ksm.fullnameOverride -}}
{{- $ksm.fullnameOverride | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- $name := $ksm.nameOverride | default "kube-state-metrics" -}}
{{- if contains $name .Release.Name -}}
{{- .Release.Name | trunc 63 | trimSuffix "-" -}}
{{- else -}}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "observability-emitters.kubeStateMetrics.serviceAccountName" -}}
{{- $ksm := index .Values "kube-state-metrics" -}}
{{- $sa := $ksm.serviceAccount | default dict -}}
{{- if or (not (hasKey $sa "create")) $sa.create -}}
{{- $sa.name | default (include "observability-emitters.kubeStateMetrics.fullname" .) -}}
{{- else -}}
{{- $sa.name | default "default" -}}
{{- end -}}
{{- end -}}

{{- define "observability-emitters.kubeStateMetrics.crsConfigMapName" -}}
{{- $ksm := index .Values "kube-state-metrics" -}}
{{- $crs := $ksm.customResourceState | default dict -}}
{{- if and $crs.name (ne $crs.name "") -}}
{{- $crs.name -}}
{{- else -}}
{{- printf "%s-customresourcestate-config" (include "observability-emitters.kubeStateMetrics.fullname" .) -}}
{{- end -}}
{{- end -}}

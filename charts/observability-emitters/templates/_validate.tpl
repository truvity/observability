{{/*
Every refusal in this chart.

The rule for what belongs here is the repository's: a value whose wrong
setting is SILENT. An agent that will not start is loud and is fixed in
ten minutes. An agent that starts, scrapes the whole cluster and stamps
the wrong cluster on all of it is an install that reports healthy while
the people who needed the data cannot see it and the people who should
not see it can. So does an agent buffering to a volume that is discarded
on every rollout, or one replicating to a single destination because the
second URL was a typo.

Every one of these has a fixture under
tests/invalid/observability-emitters/ that must keep failing, and every
message says what is wrong, what it would have caused, and what to write
instead.
*/}}

{{- define "observability-emitters.validate" -}}
{{- include "observability-emitters.validate.enabled" . -}}
{{- include "observability-emitters.validate.tenancy" . -}}
{{- include "observability-emitters.validate.credentials" . -}}
{{- include "observability-emitters.validate.destinations" . -}}
{{- include "observability-emitters.validate.metrics" . -}}
{{- include "observability-emitters.validate.logs" . -}}
{{- include "observability-emitters.validate.otlp" . -}}
{{- include "observability-emitters.validate.licence" . -}}
{{- end -}}

{{/*
An install that emits nothing.

Three optional emitters and no fourth thing in the chart, so all three off
is a release that renders a namespace's worth of nothing and reports
Synced. The cluster then has no telemetry and one more green application
saying otherwise.
*/}}
{{- define "observability-emitters.validate.enabled" -}}
{{- if not (or .Values.metrics.enabled .Values.logs.enabled .Values.otlp.enabled) -}}
{{- fail "observability-emitters: every emitter is disabled, so this release would collect nothing at all and still report Synced. Enable at least one of metrics.enabled, logs.enabled or otlp.enabled, or do not install this chart on this cluster." -}}
{{- end -}}
{{- end -}}

{{/*
Tenancy: the two values, and the two ways of losing the key quietly.

A blank value here does not fail anything at runtime. It produces
telemetry stamped `k8s_cluster_name=""`, which is stored, costs what every
other series costs, and matches no grant the proxy injects — because a
grant names a cluster, and no cluster is called nothing. So it is
invisible to every person who might have acted on it, and nothing anywhere
says so. That is why both are asked for rather than defaulted.

The name shape is the same one `pkg/tenancy` enforces, for the same
reason: the cluster name is interpolated into a filter expression on the
read side, and one carrying `|` or `.*` widens the grant it appears in
rather than looking odd. The tier is held to the same shape because it
is stamped into the same places, even though no filter selects on it.
*/}}
{{- define "observability-emitters.validate.tenancy" -}}
{{- $shape := "^[a-z0-9]([a-z0-9-]*[a-z0-9])?$" -}}
{{- $t := .Values.tenancy -}}
{{- if not $t.cluster -}}
{{- fail "observability-emitters: `tenancy.cluster` is empty. Every sample, log line and span this cluster emits would carry k8s_cluster_name=\"\" (k8s.cluster.name on logs and spans), and the cluster is half of the scoping key: a grant names a cluster and namespaces on it, so telemetry from a cluster called nothing matches no grant at all — stored, paid for, and invisible to everyone. Name this cluster; it is matched exactly, so it is the estate's own vocabulary and not a display name." -}}
{{- end -}}
{{- if not (regexMatch $shape (toString $t.cluster)) -}}
{{- fail (printf "observability-emitters: `tenancy.cluster` is %q, which is not a plain name (%s). Cluster names are interpolated into the filter expression the proxy applies to every query, so one carrying `|`, `)` or `.*` would widen a grant rather than look odd. Such a name is refused, never escaped." (toString $t.cluster) $shape) -}}
{{- end -}}
{{- if not $t.environment -}}
{{- fail "observability-emitters: `tenancy.environment` is empty. It is the value of `deployment.environment.name` on every signal this cluster emits — the environment tier a dashboard pins and a person reads, `production`, `staging`, `development`, `test` or the estate's own word. It is never a key, so a blank one selects nothing wrongly; it is refused because a tier of \"\" on every series is a dimension that exists and says nothing, and nobody notices until the first dashboard that groups by it. Name the tier." -}}
{{- end -}}
{{- if not (regexMatch $shape (toString $t.environment)) -}}
{{- fail (printf "observability-emitters: `tenancy.environment` is %q, which is not a plain name (%s). It is stamped into the same places the cluster name is, and held to the same shape." (toString $t.environment) $shape) -}}
{{- end -}}
{{- end -}}

{{/*
The write credential.

Empty is not "no authentication" in any useful sense: the stores demand
one, so an emitter without it gets 401 on every write, buffers until the
buffer is full, and then drops the oldest — while the pod stays Ready and
the agent stays green.
*/}}
{{- define "observability-emitters.validate.credentials" -}}
{{- if not .Values.writeCredentials.secretName -}}
{{- fail "observability-emitters: `writeCredentials.secretName` is empty, so every emitter would write unauthenticated. The stores demand a bearer, so each write is refused, each agent buffers until its buffer is full and then drops the oldest data — with every pod Ready throughout. Name the Secret the estate created; this chart never creates one and never reads it." -}}
{{- end -}}
{{- if not .Values.writeCredentials.key -}}
{{- fail "observability-emitters: `writeCredentials.key` is empty. Name the key inside the Secret that holds the token." -}}
{{- end -}}
{{- end -}}

{{/*
Destinations.

An enabled emitter with nowhere to write is the shape this chart exists to
prevent twice over: it reports healthy, it fills a volume, and then it
drops. A duplicate name is refused because the names become exporter ids
and directory names — two destinations sharing one would share a queue
and one of them would never be written to.
*/}}
{{- define "observability-emitters.validate.destinations" -}}
{{- $sites := list -}}
{{- if .Values.metrics.enabled -}}
{{- $sites = append $sites (dict "key" "metrics.destinations" "value" .Values.metrics.destinations "named" true) -}}
{{- end -}}
{{- if .Values.logs.enabled -}}
{{- $sites = append $sites (dict "key" "victoria-logs-collector.remoteWrite" "value" ((index .Values "victoria-logs-collector").remoteWrite) "named" false) -}}
{{- end -}}
{{- if .Values.otlp.enabled -}}
{{- range $signal := list "metrics" "logs" "traces" -}}
{{- $sites = append $sites (dict "key" (printf "otlp.destinations.%s" $signal) "value" (index $.Values.otlp.destinations $signal) "named" true) -}}
{{- end -}}
{{- end -}}
{{- range $site := $sites -}}
{{- $entries := $site.value | default list -}}
{{- if not $entries -}}
{{- fail (printf "observability-emitters: `%s` is empty while its emitter is enabled. An agent with nowhere to write collects everything, keeps it in its buffer, and drops the oldest when the buffer is full — with a Ready pod and a green sync for as long as it takes anyone to notice. List every replica of the store this signal belongs to: replication is the writer's job here, so every destination gets every sample." $site.key) -}}
{{- end -}}
{{- $names := dict -}}
{{- $urls := dict -}}
{{- range $i, $d := $entries -}}
{{- if not $d.url -}}
{{- fail (printf "observability-emitters: `%s[%d]` has no `url`." $site.key $i) -}}
{{- end -}}
{{- if hasKey $urls (toString $d.url) -}}
{{- fail (printf "observability-emitters: `%s` lists %q twice. Two entries for one address is not redundancy — it is one store receiving every sample twice, and half the buffer it looked like there was." $site.key (toString $d.url)) -}}
{{- end -}}
{{- $_ := set $urls (toString $d.url) true -}}
{{- if $site.named -}}
{{- if not $d.name -}}
{{- fail (printf "observability-emitters: `%s[%d]` has no `name`. The name becomes an exporter id and a queue directory, so it has to exist and be distinct." $site.key $i) -}}
{{- end -}}
{{- if not (regexMatch "^[a-z0-9]([a-z0-9-]*[a-z0-9])?$" (toString $d.name)) -}}
{{- fail (printf "observability-emitters: `%s[%d].name` is %q, which is not a plain name. It becomes a directory on the queue volume and a component id in the collector's configuration." $site.key $i (toString $d.name)) -}}
{{- end -}}
{{- if hasKey $names (toString $d.name) -}}
{{- fail (printf "observability-emitters: `%s` uses the name %q twice. The names become exporter ids and queue directories: two destinations sharing one share a queue, and only one of them is ever written to." $site.key (toString $d.name)) -}}
{{- end -}}
{{- $_ := set $names (toString $d.name) true -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
The metrics agent.

Everything here is checked against the MERGED spec — what `metrics.spec`
left after it — because an escape hatch that can turn off the security
property is not an escape hatch.
*/}}
{{- define "observability-emitters.validate.metrics" -}}
{{- if .Values.metrics.enabled -}}
{{- $spec := fromYaml (include "observability-emitters.vmagent.spec" .) -}}
{{- /*
Sharding instead of replicating.

`-remoteWrite.shardByURL` splits the series between the destinations
rather than sending each to all of them. With a zone-redundant pair each
replica would hold half the data — every query answers, every dashboard
draws, and half of every result is missing. There is no configuration in
which this chart wants it, so it is refused rather than defaulted off.
*/}}
{{- range $flag, $_ := ($spec.extraArgs | default dict) -}}
{{- if hasPrefix "remoteWrite.shardByURL" $flag -}}
{{- fail (printf "observability-emitters: the metrics agent sets `%s`. That flag SHARDS the series between the remote-write destinations instead of replicating to all of them, so a zone-redundant pair would hold half the data each. Every query would still answer and every dashboard would still draw — with half of every result missing, and nothing reporting it. Replication is the writer's job in this design: remove the flag." $flag) -}}
{{- end -}}
{{- end -}}
{{- /*
The queue with nothing behind it.
*/}}
{{- if not $spec.statefulMode -}}
{{- fail "observability-emitters: the metrics agent has `statefulMode: false`. Without it the operator renders a Deployment and the persistent queue lands on `/tmp`, which is an emptyDir: every rollout, eviction and node replacement discards whatever had not been delivered yet, and on a node with an ephemeral-storage budget the queue counts against that budget as well. The agent stays Ready throughout. Leave `statefulMode` alone." -}}
{{- end -}}
{{- /* `hasKey`, not truthiness: `emptyDir: {}` is the ordinary way to write
one and an empty map is false in a template, so a truthiness test would
pass exactly the value it exists to refuse. */ -}}
{{- if hasKey ($spec.statefulStorage | default dict) "emptyDir" -}}
{{- fail "observability-emitters: the metrics agent's `statefulStorage` is an emptyDir, which is the same loss `statefulMode` was turned on to prevent — the queue is discarded with the pod. Give it a volumeClaimTemplate, sized in multiples of 500MB with at least 500Mi per destination: the operator divides that storage request by the number of destinations to derive the per-destination cap." -}}
{{- end -}}
{{- if not ((($spec.statefulStorage).volumeClaimTemplate).spec) -}}
{{- fail "observability-emitters: the metrics agent has no `statefulStorage.volumeClaimTemplate`, so its persistent queue has no volume behind it. Set `metrics.queue.size`." -}}
{{- end -}}
{{- /*
The application choosing its own cluster or namespace.
*/}}
{{- if not $spec.overrideHonorLabels -}}
{{- fail "observability-emitters: the metrics agent has `overrideHonorLabels: false`. With it false, a label a target exports itself WINS over the label this agent stamps — so any workload that exposes its own `k8s_namespace_name` or `k8s_cluster_name` metric label chooses where its series are filed, which is the one thing the whole tenancy design says it cannot do. It can write into another team's data or hide its own from the people responsible for it, and the render, the sync and the dashboards all look correct. Leave it true." -}}
{{- end -}}
{{- if not $spec.selectAllByDefault -}}
{{- fail "observability-emitters: the metrics agent has `selectAllByDefault: false`, which — with no selectors set — means it selects NO scrape objects at all: not a narrower set, none. The agent starts, stays Ready, and scrapes nothing but the inline node jobs. A component whose PodMonitor is ignored looks exactly like a component with nothing wrong." -}}
{{- end -}}
{{- $classes := $spec.scrapeClasses | default list -}}
{{- $default := dict -}}
{{- range $c := $classes -}}
{{- if $c.default -}}{{- $default = $c -}}{{- end -}}
{{- end -}}
{{- if not $default -}}
{{- fail "observability-emitters: the metrics agent has no default scrape class, so nothing stamps the scoping key on the scrape objects this chart does not own — which is all of them. Every series from every PodMonitor on the cluster would carry no cluster and no namespace, and would match no grant." -}}
{{- end -}}
{{- /*
And the class has to actually stamp.

`mergeOverwrite` replaces a list wholesale, so a caller who adds one
scrape class of their own replaces the tenancy one — and a replacement
that happens to be the default class would pass the check above while
stamping nothing at all. The rules themselves are checked, not just their
container, and they are checked for the two KEYS: a class that stamps
the tier and nothing else has stamped nothing a grant can select on.
*/}}
{{- $targets := dict -}}
{{- range $r := ($default.relabelConfigs | default list) -}}
{{- $_ := set $targets (toString (or $r.target_label $r.targetLabel)) true -}}
{{- end -}}
{{- range $label := list "k8s_cluster_name" "k8s_namespace_name" -}}
{{- if not (hasKey $targets $label) -}}
{{- fail (printf "observability-emitters: the metrics agent's default scrape class writes no %q label. That is half of the scoping key, and nothing else would stamp it on any scrape object this chart does not own — which is all of them — so every series would carry one dimension and the grants would select on a dimension that is not there. If you replaced `scrapeClasses` through `metrics.spec`, note that a list is replaced wholesale rather than merged: the stamping rules went with it." $label) -}}
{{- end -}}
{{- end -}}

{{- /*
The one interval.
*/}}
{{- if ne (toString $spec.scrapeInterval) (toString .Values.interval) -}}
{{- fail (printf "observability-emitters: `interval` is %q but the metrics agent's `scrapeInterval` is %q. They are one number, and it is the same number as the store's `-dedup.minScrapeInterval`: deduplication keeps one sample per window, so a window wider than the scrape interval silently discards good samples and a narrower one deduplicates nothing. Neither says anything at the time. Set `interval` and leave `metrics.spec.scrapeInterval` alone." (toString .Values.interval) (toString $spec.scrapeInterval)) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
The log agent.

Four failures, all of them silent, and the first one is a design
constraint rather than a mistake anyone made.
*/}}
{{- define "observability-emitters.validate.logs" -}}
{{- if .Values.logs.enabled -}}
{{- $vlc := index .Values "victoria-logs-collector" -}}
{{- /*
The stream fields.

A LogsQL stream filter — which is what the proxy injects — selects only
on stream fields. The namespace key on this path is the agent's own
`kubernetes.pod_namespace`, an upstream default; the cluster key is
`k8s.cluster.name`, which arrives from `extraFields` and has to be added.
A key that is an ordinary field rather than a stream field is a key every
scoped query misses, silently.
*/}}
{{- $stream := ($vlc.collector).streamFields | default list -}}
{{- range $required := list "k8s.cluster.name" "kubernetes.pod_namespace" -}}
{{- if not (has $required $stream) -}}
{{- fail (printf "observability-emitters: %q is not in `victoria-logs-collector.collector.streamFields`, so it is an ordinary field rather than part of the log stream — and a LogsQL stream filter, which is what the proxy injects, only selects on stream fields. It is half of the scoping key, so every scoped log query would return nothing at all. Add it; it is constant for the lifetime of a pod, which is the rule for a stream field." $required) -}}
{{- end -}}
{{- end -}}
{{- /*
The checkpoint and the buffer.
*/}}
{{- if hasKey ((($vlc.persistence).volume) | default dict) "emptyDir" -}}
{{- fail "observability-emitters: `victoria-logs-collector.persistence.volume` is an emptyDir. Two things live on it: the per-destination buffer, and the CHECKPOINT that records how far into each container's log file the agent has read. An agent that forgets its checkpoint re-reads every file from the beginning on every rollout and ships every line a second time — a duplicate is not a gap, so nothing alerts, nothing looks broken, and the first sign is the bill. Leave it empty to keep upstream's hostPath at `extraArgs.tmpDataPath`." -}}
{{- end -}}
{{- /*
The credential, and the cap.

The log agent's remote-write entries are upstream's own shape, so its
credential lives on them rather than in `writeCredentials` — which is
where the other two emitters take it from. The asymmetry is upstream's
and it is checked rather than hidden: an entry with no credential is
refused, because the store answers 401, the agent buffers, and the buffer
drops its oldest data while every pod stays Ready.

The cap is refused missing for a worse reason. This buffer is on a
hostPath, so an uncapped one does not fill a volume — it fills the NODE's
disk, and a node under disk pressure evicts every pod on it, not only this
one. A collection agent that can take a node down is worth one required
value.
*/}}
{{- range $i, $d := ($vlc.remoteWrite | default list) -}}
{{- if not (or $d.bearerTokenFile $d.bearerToken (($d.headers).Authorization) $d.basicAuth) -}}
{{- fail (printf "observability-emitters: `victoria-logs-collector.remoteWrite[%d]` carries no credential — no `bearerTokenFile`, no `bearerToken`, no `basicAuth` and no Authorization header. The store answers 401 to every write, the agent buffers until the buffer is full and then drops the oldest lines, and the pod is Ready throughout. Mount the Secret named in `writeCredentials.secretName` through `extraVolumes`/`extraVolumeMounts` and point `bearerTokenFile` at it; the agent re-reads that file when it changes, so a rotation needs no restart." $i) -}}
{{- end -}}
{{- if not $d.maxDiskUsagePerURL -}}
{{- fail (printf "observability-emitters: `victoria-logs-collector.remoteWrite[%d]` has no `maxDiskUsagePerURL`, so its buffer is uncapped — and this buffer is on a hostPath, so it does not fill a volume, it fills the NODE's disk. A node under disk pressure evicts every pod on it, not only this one, which is how a collection agent takes down the workloads it was watching. Cap it." $i) -}}
{{- end -}}
{{- end -}}

{{- /*
The write path.
*/}}
{{- range $i, $d := ($vlc.remoteWrite | default list) -}}
{{- $path := (urlParse (toString $d.url)).path | default "" -}}
{{- if and $path (ne $path "/") (ne $path "/insert/native") -}}
{{- fail (printf "observability-emitters: `victoria-logs-collector.remoteWrite[%d].url` has the path %q. The only path that accepts this agent's protocol is `/insert/native`, and a wrong one answers 404 — which vlagent treats as a permanent rejection and DROPS the block rather than retrying it. The loss is silent, unrecoverable and proportional to how long it takes somebody to look. Leave the path off and the agent appends the right one." $i $path) -}}
{{- end -}}
{{- end -}}
{{- /*
The scrape kind.
*/}}
{{- if ($vlc.podMonitor).vm -}}
{{- fail "observability-emitters: `victoria-logs-collector.podMonitor.vm` is true, which renders a VMPodScrape instead of a PodMonitor. Every scrape object on a cluster this chart collects from is a Prometheus Operator kind, and that is not a style rule: it is the only thing keeping the agent under them replaceable. One object in the vendor's own spelling is the first of the ones that follow it, and by then swapping the agent means rewriting every chart that authors a scrape." -}}
{{- end -}}
{{- /*
The cluster and tier mirror.

Helm evaluates a subchart's values before any template runs, so this
chart cannot write the agent's static fields itself: they have to be
written twice, and two values that are supposed to be equal stop being
equal the first time somebody changes one. So the JSON is parsed and
both keys are checked against `tenancy`. A wrong cluster here is the
worst shape of wrong — every container log on the cluster filed under a
cluster that does not exist, or under another one.
*/}}
{{- $wantExtra := printf "{%q:%q,%q:%q}" "k8s.cluster.name" (toString .Values.tenancy.cluster) "deployment.environment.name" (toString .Values.tenancy.environment) -}}
{{- $got := toString (($vlc.collector).extraFields | default "") -}}
{{- $parsed := fromJson $got -}}
{{- if or (not (kindIs "map" $parsed)) (hasKey $parsed "Error") -}}
{{- fail (printf "observability-emitters: `victoria-logs-collector.collector.extraFields` is %q, which is not a JSON object. It is how the container-log agent stamps the cluster and the tier on every line — the agent cannot rename a field, but it can add a static one, and these two are static for the cluster. Helm evaluates a subchart's values before any template runs, so this chart cannot write it for you. Write exactly: %s" $got $wantExtra) -}}
{{- end -}}
{{- range $field, $want := dict "k8s.cluster.name" (toString .Values.tenancy.cluster) "deployment.environment.name" (toString .Values.tenancy.environment) -}}
{{- $have := toString (index $parsed $field | default "") -}}
{{- if ne $have $want -}}
{{- fail (printf "observability-emitters: `victoria-logs-collector.collector.extraFields` carries %q as %q but `tenancy` says %q. The two are written twice because Helm evaluates a subchart's values before any template runs, which is why they are checked rather than trusted: a disagreement here files every container log on this cluster under the wrong cluster, where no grant for this one reaches it. Write exactly: %s" $field $have $want $wantExtra) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
The gateway.

The stream fields are the one that matters. "Never add non-constant fields
to streams" is the vendor's own sentence, and the default this chart
overrides is the worst possible version of it: with no `VL-Stream-Fields`
header VictoriaLogs treats EVERY resource attribute as a stream field, and
an OpenTelemetry SDK's resource carries the pod's UID and its start time.
Every restart then mints a stream that is never written to again, and the
store degrades over weeks in a way that looks like growth.
*/}}
{{- define "observability-emitters.validate.otlp" -}}
{{- if .Values.otlp.enabled -}}
{{- $fields := .Values.otlp.streamFields | default list -}}
{{- if not $fields -}}
{{- fail "observability-emitters: `otlp.streamFields` is empty, so no `VL-Stream-Fields` header is sent — and with none, VictoriaLogs treats EVERY resource attribute as a log stream field. An OpenTelemetry SDK's resource carries the pod's UID and its start time, so every restart of every workload mints a stream that is never written to again. The store does not fail; it degrades, over weeks, in a way that reads as growth. Name the fields." -}}
{{- end -}}
{{- /*
The allow-list is in the chart rather than in values on purpose: an
allow-list a caller can extend is a comment. The Helm release attribute
is deliberately absent from it — it is a pod-level navigation handle,
and a stream field is a cardinality decision.
*/}}
{{- $allowed := list "k8s.cluster.name" "kubernetes.pod_namespace" "deployment.environment.name" "k8s.namespace.name" "service.name" "service.namespace" "service.instance.id" "k8s.pod.name" "k8s.container.name" "k8s.node.name" -}}
{{- range $f := $fields -}}
{{- if not (has (toString $f) $allowed) -}}
{{- fail (printf "observability-emitters: `otlp.streamFields` contains %q, which is not one of the attributes that are constant for the lifetime of a pod (%s). A stream field that changes per request — an address, a user id, a trace id — creates a new stream for every value it takes, and that is the documented way to wreck this store. It fails slowly and it does not recover on its own, which is why the list is the chart's and not a value." (toString $f) (join ", " $allowed)) -}}
{{- end -}}
{{- end -}}
{{- range $required := list "k8s.cluster.name" "kubernetes.pod_namespace" -}}
{{- if not (has $required $fields) -}}
{{- fail (printf "observability-emitters: %q is not in `otlp.streamFields`, so it is an ordinary field rather than part of the log stream — and the stream filter the proxy injects only selects on stream fields. It is half of the scoping key, so every scoped log query would miss everything this gateway wrote, while the container-log agent's half of the same store still answered. Add it." $required) -}}
{{- end -}}
{{- end -}}
{{- if not .Values.otlp.queue.size -}}
{{- fail "observability-emitters: `otlp.queue.size` is empty, so the gateway's exporters would have no volume behind them: the OTLP queues and the remote-write write-ahead log both live on it. Without it every replica holds its undelivered data in memory and loses it on the next rollout — and a rollout is the most likely moment for a store to be briefly unreachable." -}}
{{- end -}}
{{- if lt (int .Values.otlp.replicaCount) 1 -}}
{{- fail "observability-emitters: `otlp.replicaCount` is below 1. An enabled gateway with no replica is an OTLP endpoint that refuses every connection, and an SDK that cannot export drops spans silently after its own queue fills." -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
The Enterprise boundary, as it applies here.

An Enterprise image without a licence key RUNS — it refuses only the
Enterprise features — so nothing reports that the estate is in breach of
the vendor's terms. A tag is the only thing a chart can see.
*/}}
{{- define "observability-emitters.validate.licence" -}}
{{- $vlc := index .Values "victoria-logs-collector" -}}
{{- $tags := list
    (dict "key" "metrics.image.tag" "value" .Values.metrics.image.tag)
    (dict "key" "victoria-logs-collector.image.tag" "value" (($vlc.image).tag))
    (dict "key" "victoria-logs-collector.image.variant" "value" (($vlc.image).variant))
-}}
{{- range $tag := $tags -}}
{{- if $tag.value -}}
{{- if contains "enterprise" (lower (toString $tag.value)) -}}
{{- fail (printf "observability-emitters: %s is %q. This chart wraps the COMMUNITY edition only. An Enterprise image without a licence key starts and serves, refusing only the Enterprise features, so nothing reports that the estate is in breach of the vendor's terms — which is why this is a refusal and not a warning. See docs/doctrine.md." $tag.key (toString $tag.value)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $l := $vlc.license | default dict -}}
{{- if or $l.key (($l.secret).name) -}}
{{- fail "observability-emitters: `victoria-logs-collector.license` carries a licence key. A licence key is only useful to an Enterprise binary, and this chart renders none: it wraps the community edition, which is Apache 2.0 and free for any number of tenants." -}}
{{- end -}}
{{- range $flag, $_ := (.Values.metrics.spec.extraArgs | default dict) -}}
{{- if or (eq $flag "license") (eq $flag "licenseFile") (hasPrefix "license." $flag) -}}
{{- fail (printf "observability-emitters: `metrics.spec.extraArgs` sets `%s`. A licence flag is an Enterprise flag, and this chart never renders one." $flag) -}}
{{- end -}}
{{- end -}}
{{- end -}}

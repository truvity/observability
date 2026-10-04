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
{{- include "observability-emitters.validate.owners" . -}}
{{- include "observability-emitters.validate.credentials" . -}}
{{- include "observability-emitters.validate.remote" . -}}
{{- include "observability-emitters.validate.destinations" . -}}
{{- include "observability-emitters.validate.metrics" . -}}
{{- include "observability-emitters.validate.logs" . -}}
{{- include "observability-emitters.validate.kubeStateMetrics" . -}}
{{- include "observability-emitters.validate.nodeExporter" . -}}
{{- include "observability-emitters.validate.otlp" . -}}
{{- include "observability-emitters.validate.cloudwatchLogs" . -}}
{{- include "observability-emitters.validate.probes" . -}}
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
The owner stamp (`tenancy.owners`, opt-in).

An owner is stamped as a label value on every series and as a field on
every OTLP signal, and routed on by Alertmanager, so both halves are held
to a plain shape. A pattern is a namespace name where `*` stands for any
run of namespace characters; anything else (`.`, `|`, `(`) would be read
as regular expression by the relabel rules and widen an owner's reach, so
it is refused, never escaped. Two owners claiming the SAME namespace is a
misconfiguration that would be resolved silently by order, so it is
refused wherever it can be seen: an identical pattern, or a literal
namespace another owner's glob already covers. Two overlapping globs
(`a*` and `*b`) cannot be told apart from two disjoint ones without
enumerating namespaces; the alphabetically first owner wins there, which
docs/tenancy-owner.md states.
*/}}
{{- define "observability-emitters.validate.owners" -}}
{{- $t := .Values.tenancy -}}
{{- $nameShape := "^[a-z0-9]([a-z0-9._-]{0,61}[a-z0-9])?$" -}}
{{- $patShape := "^[a-z0-9*]([a-z0-9*-]{0,61}[a-z0-9*])?$" -}}
{{- if and $t.defaultOwner (not $t.owners) -}}
{{- fail "observability-emitters: `tenancy.defaultOwner` is set but `tenancy.owners` is empty. The owner stamp is off until at least one owner is declared, so the default would be written nowhere; declare the owners, or drop the default." -}}
{{- end -}}
{{- if $t.owners -}}
{{- if and $t.defaultOwner (not (regexMatch $nameShape (toString $t.defaultOwner))) -}}
{{- fail (printf "observability-emitters: `tenancy.defaultOwner` is %q, which is not a plain name (%s). It is stamped as a label value and matched by Alertmanager routes." (toString $t.defaultOwner) $nameShape) -}}
{{- end -}}
{{- range $owner, $patterns := $t.owners -}}
{{- if not (regexMatch $nameShape (toString $owner)) -}}
{{- fail (printf "observability-emitters: `tenancy.owners` has the owner %q, which is not a plain name (%s). The name is stamped as a label value on every series and matched by Alertmanager routes." (toString $owner) $nameShape) -}}
{{- end -}}
{{- if not $patterns -}}
{{- fail (printf "observability-emitters: `tenancy.owners.%s` lists no namespace pattern. An owner that owns nothing stamps nothing, and looks configured; list the namespaces it owns or remove it." $owner) -}}
{{- end -}}
{{- range $p := $patterns -}}
{{- if not (regexMatch $patShape (toString $p)) -}}
{{- fail (printf "observability-emitters: `tenancy.owners.%s` has the pattern %q, which is not a namespace name or a glob of one (%s). Only lowercase letters, digits, `-` and `*` are accepted: a `.` or `|` would be read as a regular expression and widen the owner's reach, so it is refused rather than escaped. An empty pattern would match nothing, or with a careless rewrite everything." $owner (toString $p) $patShape) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- range $a, $pa := $t.owners -}}
{{- range $b, $pb := $t.owners -}}
{{- if ne $a $b -}}
{{- range $p := $pa -}}
{{- range $q := $pb -}}
{{- if eq (toString $p) (toString $q) -}}
{{- fail (printf "observability-emitters: the namespace pattern %q is listed under both `tenancy.owners.%s` and `tenancy.owners.%s`. A namespace has one owner; which one an alert is routed to must not depend on map order." (toString $p) $a $b) -}}
{{- end -}}
{{- if and (not (contains "*" (toString $q))) (regexMatch (printf "^%s$" (include "observability-emitters.owners.regex" (list $p))) (toString $q)) -}}
{{- fail (printf "observability-emitters: the namespace %q under `tenancy.owners.%s` is also matched by the pattern %q under `tenancy.owners.%s`. A namespace has one owner; narrow the glob or drop the namespace." (toString $q) $b (toString $p) $a) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
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
{{- $creds := include "observability-emitters.effectiveWriteCredentials" . | fromYaml -}}
{{- if not $creds.secretName -}}
{{- if eq (include "observability-emitters.remote.inUse" .) "true" -}}
{{- fail "observability-emitters: `remote.tokenSecret.name` is empty, so every emitter would write unauthenticated. Name the Secret the estate created; this chart never creates one and never reads it." -}}
{{- else -}}
{{- fail "observability-emitters: `writeCredentials.secretName` is empty, so every emitter would write unauthenticated. The stores demand a bearer, so each write is refused, each agent buffers until its buffer is full and then drops the oldest data — with every pod Ready throughout. Name the Secret the estate created; this chart never creates one and never reads it." -}}
{{- end -}}
{{- end -}}
{{- if not $creds.key -}}
{{- if eq (include "observability-emitters.remote.inUse" .) "true" -}}
{{- fail "observability-emitters: `remote.tokenSecret.key` is empty. Name the key inside the Secret that holds the token." -}}
{{- else -}}
{{- fail "observability-emitters: `writeCredentials.key` is empty. Name the key inside the Secret that holds the token." -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
`remote`: the single-destination sugar, mutually exclusive with the
low-level form for every signal it covers.

`victoria-logs-collector.remoteWrite` is deliberately NOT in this list
— see `remote`'s own values.yaml comment for why that one stays
low-level regardless: it is a real Helm subchart's own values, which
this chart cannot compute from `remote` the way it computes its own.
*/}}
{{- define "observability-emitters.validate.remote" -}}
{{- if eq (include "observability-emitters.remote.inUse" .) "true" -}}
{{- $remote := .Values.remote -}}
{{- /* An empty `remote.tokenSecret.name`/`.key` is already caught by
observability-emitters.validate.credentials, which reads the SAME
effective credential this block resolves `remote` into — no second
check needed here. */ -}}
{{- $caSecret := $remote.caSecret | default dict -}}
{{- if and $caSecret.name (not $caSecret.key) -}}
{{- fail "observability-emitters: `remote.caSecret.name` is set but `remote.caSecret.key` is empty." -}}
{{- end -}}
{{- if and $caSecret.key (not $caSecret.name) -}}
{{- fail "observability-emitters: `remote.caSecret.key` is set but `remote.caSecret.name` is empty." -}}
{{- end -}}
{{- if .Values.writeCredentials.secretName -}}
{{- fail "observability-emitters: `remote.url` and `writeCredentials.secretName` are both set. `remote.tokenSecret` IS the write credential once `remote` is in use — the two would only ever agree by writing the same Secret name twice, which is the exact mirror this chart refuses elsewhere rather than trusts. Clear `writeCredentials`, or clear `remote.url` and use the low-level destinations instead." -}}
{{- end -}}
{{- $signals := include "observability-emitters.remote.signals" . | fromYamlArray -}}
{{- if has "metrics" $signals -}}
{{- if gt (len (.Values.metrics.destinations | default list)) 0 -}}
{{- fail "observability-emitters: `remote.signals` includes \"metrics\" but `metrics.destinations` is also set. `remote` already expands into one entry there for this signal — the two forms are refused together so a destination is never named twice from two different places. Remove `remote` from `signals`, or clear `metrics.destinations` and let `remote` provide it." -}}
{{- end -}}
{{- if gt (len ((.Values.otlp.destinations).metrics | default list)) 0 -}}
{{- fail "observability-emitters: `remote.signals` includes \"metrics\" but `otlp.destinations.metrics` is also set. `remote` already expands into one entry there for this signal — the two forms are refused together. Remove `remote` from `signals`, or clear `otlp.destinations.metrics` and let `remote` provide it." -}}
{{- end -}}
{{- end -}}
{{- if has "logs" $signals -}}
{{- if gt (len ((.Values.otlp.destinations).logs | default list)) 0 -}}
{{- fail "observability-emitters: `remote.signals` includes \"logs\" but `otlp.destinations.logs` is also set. `remote` already expands into one entry there for this signal — the two forms are refused together. Remove \"logs\" from `remote.signals`, or clear `otlp.destinations.logs` and let `remote` provide it. (`victoria-logs-collector.remoteWrite`, the container-log agent's OWN write path, is untouched by `remote` either way — see its own comment.)" -}}
{{- end -}}
{{- end -}}
{{- if has "traces" $signals -}}
{{- if gt (len ((.Values.otlp.destinations).traces | default list)) 0 -}}
{{- fail "observability-emitters: `remote.signals` includes \"traces\" but `otlp.destinations.traces` is also set. `remote` already expands into one entry there for this signal — the two forms are refused together. Remove \"traces\" from `remote.signals`, or clear `otlp.destinations.traces` and let `remote` provide it." -}}
{{- end -}}
{{- end -}}
{{- /*
`remote.replicas`: the other halves of an HA store pair.

Three things go wrong silently here. The same address twice is not
redundancy — it is one store taking every sample twice, spelled two ways
(a trailing slash defeats the plain string comparison the destination
lists make, so it is normalised here). The same credential twice is
refused because a store authenticates one user per bearer token: a pair
written with one token reaches one of them, or neither. And the log agent
is a real subchart whose list this chart cannot compute, so it is CHECKED
rather than trusted: an agent that writes to only one half sends every log
line to one store while the other, and the dashboards over it, look healthy.
*/ -}}
{{- if ($remote.replicas | default list) -}}
{{- $seenUrl := dict (trimSuffix "/" (toString $remote.url)) "`remote.url`" -}}
{{- $seenName := dict (toString $remote.name) "`remote.name`" -}}
{{- $seenToken := dict (printf "%s/%s" $remote.tokenSecret.name $remote.tokenSecret.key) "`remote.tokenSecret`" -}}
{{- range $i, $r := $remote.replicas -}}
{{- $where := printf "`remote.replicas[%d]`" $i -}}
{{- $u := trimSuffix "/" (toString $r.url) -}}
{{- if hasKey $seenUrl $u -}}
{{- fail (printf "observability-emitters: %s has the same address as %s (%s). Two entries for one address is not a pair — it is one store receiving every sample twice, and half the buffer it looked like there was. Give the replica the address of the OTHER store." $where (index $seenUrl $u) $u) -}}
{{- end -}}
{{- $_ := set $seenUrl $u $where -}}
{{- if hasKey $seenName (toString $r.name) -}}
{{- fail (printf "observability-emitters: %s uses the name %q that %s already has. The name becomes an exporter id and a queue directory: two destinations sharing one share a queue, and only one of them is ever written to." $where (toString $r.name) (index $seenName (toString $r.name))) -}}
{{- end -}}
{{- $_ := set $seenName (toString $r.name) $where -}}
{{- $tk := printf "%s/%s" $r.tokenSecret.name $r.tokenSecret.key -}}
{{- if hasKey $seenToken $tk -}}
{{- fail (printf "observability-emitters: %s writes with the credential %s, the same Secret and key as %s. A store holds one user per bearer token, so a second destination on the same token is the same user: the pair is written through one identity, and the store cannot tell the halves apart or revoke one without the other. Give each destination a Secret of its own." $where $tk (index $seenToken $tk)) -}}
{{- end -}}
{{- $_ := set $seenToken $tk $where -}}
{{- end -}}
{{- if and .Values.logs.enabled (has "logs" $signals) -}}
{{- $vlc := index .Values "victoria-logs-collector" -}}
{{- $want := dict -}}
{{- $_ := set $want (trimSuffix "/" (toString $remote.url)) true -}}
{{- range $r := $remote.replicas -}}
{{- $_ := set $want (trimSuffix "/" (toString $r.url)) true -}}
{{- end -}}
{{- $have := dict -}}
{{- $files := dict -}}
{{- range $i, $d := ($vlc.remoteWrite | default list) -}}
{{- $_ := set $have (trimSuffix "/" (toString $d.url)) true -}}
{{- $f := toString ($d.bearerTokenFile | default "") -}}
{{- if and $f (hasKey $files $f) -}}
{{- fail (printf "observability-emitters: `victoria-logs-collector.remoteWrite[%d]` reads its bearer from %q, the same file as an earlier entry. With `remote.replicas` each half of the pair is written with its own credential; point each entry's `bearerTokenFile` at its own mounted Secret." $i $f) -}}
{{- end -}}
{{- if $f -}}{{- $_ := set $files $f true -}}{{- end -}}
{{- end -}}
{{- range $u, $_ := $want -}}
{{- if not (hasKey $have $u) -}}
{{- fail (printf "observability-emitters: `remote.replicas` is set and covers \"logs\", but `victoria-logs-collector.remoteWrite` has no entry for %s. The container-log agent is upstream's own list, so this chart cannot expand `remote` into it: list one entry per destination (`remote.url` and each replica), each with its own `bearerTokenFile` and `maxDiskUsagePerURL`. An agent that writes to one half sends every log line to one store only." $u) -}}
{{- end -}}
{{- end -}}
{{- range $u, $_ := $have -}}
{{- if not (hasKey $want $u) -}}
{{- fail (printf "observability-emitters: `victoria-logs-collector.remoteWrite` has an entry for %s, which is neither `remote.url` nor a `remote.replicas[].url`. The log agent and the other emitters would write to different stores." $u) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
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
{{- $root := . -}}
{{- $sites := list -}}
{{- if .Values.metrics.enabled -}}
{{- $sites = append $sites (dict "key" "metrics.destinations" "value" (include "observability-emitters.effectiveMetricsDestinations" . | fromYamlArray) "named" true) -}}
{{- end -}}
{{- if .Values.logs.enabled -}}
{{- $sites = append $sites (dict "key" "victoria-logs-collector.remoteWrite" "value" ((index .Values "victoria-logs-collector").remoteWrite) "named" false) -}}
{{- end -}}
{{- if .Values.otlp.enabled -}}
{{- range $signal := list "metrics" "logs" "traces" -}}
{{- $sites = append $sites (dict "key" (printf "otlp.destinations.%s" $signal) "value" (include "observability-emitters.effectiveOtlpDestinations" (dict "root" $root "signal" $signal) | fromYamlArray) "named" true) -}}
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
kube-state-metrics.

Off by default, and every refusal below fires only while it is on. The
common thread: this is a cluster-wide reader, so a mistake here does not
fail loudly the way a wrong credential does — it under-reads silently
(a Role instead of a ClusterRole, a `namespaces` filter) or over-reports
silently (a second unsharded replica, an allow-list of `*`), and the
worst of the four is a series that IS collected but is filed under the
wrong namespace forever, which is the one this block spends the most
words on.
*/}}
{{- define "observability-emitters.validate.kubeStateMetrics" -}}
{{- if .Values.kubeStateMetrics.enabled -}}
{{- $ksm := index .Values "kube-state-metrics" -}}
{{- /*
The ConfigMap this chart's own templates/
kubestatemetrics-customresourcestate.yaml renders. values.yaml pins
`customResourceState.enabled: true` / `create: false` unconditionally
whenever `kubeStateMetrics.enabled` is, so a consumer-authored `config:`
here would never reach kube-state-metrics — `create: false` means
upstream never renders ITS OWN ConfigMap from it, and the one this
chart renders instead carries only the presets under
`kubeStateMetrics.customResources`. Configured and silently ignored is
the exact trap this chart refuses everywhere else.
*/}}
{{- if gt (len (($ksm.customResourceState).config | default dict)) 0 -}}
{{- fail "observability-emitters: `kube-state-metrics.customResourceState.config` is set, but this chart pins `customResourceState.create: false` whenever `kubeStateMetrics.enabled` is true — kube-state-metrics reads its config from the ConfigMap THIS chart renders instead (templates/kubestatemetrics-customresourcestate.yaml, built from `kubeStateMetrics.customResources`), so a hand-written `config:` here is never read at all. Ask for the same resources through `kubeStateMetrics.customResources` (a named preset), or open an issue if the one you need has no preset yet." -}}
{{- end -}}
{{- /*
Nothing scrapes it.
*/}}
{{- if not .Values.metrics.enabled -}}
{{- fail "observability-emitters: `kubeStateMetrics.enabled` is true but `metrics.enabled` is false. kube-state-metrics is scraped by this chart's OWN metrics agent, through the default scrape class every ServiceMonitor on the cluster picks up — with the agent off, nothing scrapes it at all. The pod runs, stays Ready, and answers every /metrics request; nothing ever reads one. Enable `metrics`, or leave `kubeStateMetrics` off." -}}
{{- end -}}
{{- if not (($ksm.prometheus).monitor).enabled -}}
{{- fail "observability-emitters: `kube-state-metrics.prometheus.monitor.enabled` is false, so no ServiceMonitor is rendered for it. The same failure this chart's own `selectAllByDefault` refusal guards against for the metrics agent applies here from the other side: a component with no scrape object looks exactly like a component with nothing wrong." -}}
{{- end -}}
{{- /*
Namespace scope.

kube-state-metrics is the cluster's own object state, not one release's:
a Role scoped to this namespace, or a `namespaces` filter, makes it
under-read silently — the pod stays Ready, every configured collector
still exports SOMETHING (its own namespace's objects, or whichever ones
`namespaces` names), and the gap is every OTHER namespace's, discovered
only when somebody goes looking for data that was never collected in
the first place.
*/}}
{{- if $ksm.namespaces -}}
{{- fail (printf "observability-emitters: `kube-state-metrics.namespaces` is %q. kube-state-metrics is the cluster's own object state; a namespace filter makes it silently under-read everything outside that list — Ready throughout, and no series absent, only every OTHER namespace's data that was never collected. Leave it empty." (toString $ksm.namespaces)) -}}
{{- end -}}
{{- if not (($ksm.rbac).useClusterRole) -}}
{{- fail "observability-emitters: `kube-state-metrics.rbac.useClusterRole` is false. This chart installs one kube-state-metrics for the whole cluster, and a Role reads only its own namespace — silently: the pod stays Ready and reports the namespaces it CAN see as though they were the whole cluster. Leave `useClusterRole: true`." -}}
{{- end -}}
{{- if not (($ksm.rbac).create) -}}
{{- if not (($ksm.rbac).useExistingRole) -}}
{{- fail "observability-emitters: `kube-state-metrics.rbac.create` is false and no `useExistingRole` is named. With no ClusterRole bound, every List call this pod makes is 403 — and kube-state-metrics does not crash or go NotReady on that, it exports zero series for the kinds it could not list and answers /metrics successfully for the rest. Name an existing ClusterRole, or leave `rbac.create: true`." -}}
{{- end -}}
{{- end -}}
{{- /*
Replicating without sharding.

The exact failure charts/observability-stack's own disabled copy exists
to avoid (see its values.yaml, "Two installs of kube-state-metrics
export the same series twice and every rate() over them is wrong"), now
possible a second way: two replicas of the same collector, neither
sharded, both listing the whole cluster and both scraped by the same
default scrape class.
*/}}
{{- if and (gt (int ($ksm.replicas | default 1)) 1) (not (($ksm.autosharding).enabled)) -}}
{{- fail (printf "observability-emitters: `kube-state-metrics.replicas` is %d with `autosharding.enabled` false. Unsharded replicas each list the WHOLE cluster and export the same series independently, so every scrape sees every series twice (three times at 3, and so on) — every rate() and sum() over them is silently that many times too high, with two Ready pods and no error anywhere. Either turn on `autosharding.enabled`, which splits the cluster between replicas, or leave `replicas: 1`." (int ($ksm.replicas | default 1))) -}}
{{- end -}}
{{- /*
The label and annotation allow-lists.

Both default empty, upstream's own safe default, and the refusal is only
for `*`: kube-state-metrics keys a series' labels off a workload's OWN
label values, so a wildcard multiplies series by every distinct value
combination a workload happens to use — the identical cardinality trap
`metrics.scrape.nodeLabels` above documents for the kubelet and cAdvisor
scrapes, measured there as a store silently IGNORING every series past
its own per-series label limit.
*/}}
{{- range $listName := list "metricLabelsAllowlist" "metricAnnotationsAllowList" -}}
{{- range $entry := (index $ksm $listName | default list) -}}
{{- if contains "[*]" (toString $entry) -}}
{{- fail (printf "observability-emitters: `kube-state-metrics.%s` contains %q, which allows EVERY label or annotation a workload carries onto its series, for that resource. kube-state-metrics keys a series' labels off the workload's own values, so a wildcard multiplies series by every distinct combination a workload happens to use — measured elsewhere in this chart as a store silently IGNORING every series past its own per-series label limit. Name the labels a dashboard or a rule actually needs, e.g. `namespaces=[team]`." $listName (toString $entry)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- /*
The namespace stamp, the reason this block exists.

See `values.yaml`'s own comment on this key for the full mechanism. In
short: the VictoriaMetrics operator's ServiceMonitor conversion stamps
`namespace`, `pod`, `container` and `service` as TARGET labels — the
kube-state-metrics POD's own identity — and `overrideHonorLabels: true`
means a same-named label the SERIES itself carries (kube-state-metrics'
own `namespace`, `pod`, `container`, sometimes `service`) survives the
scrape only as `exported_<name>`, because the target's value always
wins under `honor_labels: false`. A rule that reads `namespace` directly
— this chart's own first attempt at this fix — reads the WRONG one.

So this checks the whole chain, against the MERGED values, the same
"checked against the merged result" reasoning `metrics.spec` above is
held to: for each of the four names, a `labeldrop` of the bare,
target-stamped name; a `replace` restoring it from `exported_<name>`;
and a `labeldrop` cleaning up `exported_<name>` afterwards; then,
reading only from the now-corrected `namespace`, the `k8s_namespace_name`
derivation and its own leading `labeldrop`. And because each step
depends on the one before it, the ORDER is checked too, not just that
every step exists somewhere in the list.
*/}}
{{- $rules := ((($ksm.prometheus).monitor).http).metricRelabelings | default list -}}
{{- $names := list "namespace" "pod" "container" "service" -}}
{{- $bareDropIdx := dict -}}
{{- $exportedDropIdx := dict -}}
{{- $restoreIdx := dict -}}
{{- range $n := $names -}}
{{- $_ := set $bareDropIdx $n -1 -}}
{{- $_ := set $exportedDropIdx $n -1 -}}
{{- $_ := set $restoreIdx $n -1 -}}
{{- end -}}
{{- $k8sNsDropIdx := -1 -}}
{{- $finalDeriveIdx := -1 -}}
{{- range $i, $r := $rules -}}
{{- $action := toString ($r.action | default "replace") -}}
{{- if eq $action "labeldrop" -}}
{{- $anchored := printf "^(?:%s)$" (toString $r.regex) -}}
{{- if regexMatch $anchored "k8s_namespace_name" -}}
{{- $k8sNsDropIdx = $i -}}
{{- end -}}
{{- range $n := $names -}}
{{- if regexMatch $anchored $n -}}
{{- $_ := set $bareDropIdx $n $i -}}
{{- end -}}
{{- if regexMatch $anchored (printf "exported_%s" $n) -}}
{{- $_ := set $exportedDropIdx $n $i -}}
{{- end -}}
{{- end -}}
{{- else if eq $action "replace" -}}
{{- $src := $r.sourceLabels | default list -}}
{{- $tgt := toString $r.targetLabel -}}
{{- if eq (len $src) 1 -}}
{{- $from := toString (index $src 0) -}}
{{- range $n := $names -}}
{{- if and (eq $from (printf "exported_%s" $n)) (eq $tgt $n) -}}
{{- $_ := set $restoreIdx $n $i -}}
{{- end -}}
{{- end -}}
{{- if and (eq $from "namespace") (eq $tgt "k8s_namespace_name") -}}
{{- $finalDeriveIdx = $i -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if lt $k8sNsDropIdx 0 -}}
{{- fail "observability-emitters: `kube-state-metrics.prometheus.monitor.http.metricRelabelings` carries no `labeldrop` of `k8s_namespace_name`. Without it, the k8s_namespace_name derivation below would be layered on top of whatever this release's default scrape class already stamped rather than replacing it." -}}
{{- end -}}
{{- range $n := $names -}}
{{- if lt (get $bareDropIdx $n) 0 -}}
{{- fail (printf "observability-emitters: `kube-state-metrics.prometheus.monitor.http.metricRelabelings` carries no `labeldrop` of %q. That label is stamped by the VictoriaMetrics operator's ServiceMonitor conversion from the kube-state-metrics POD's own identity, not from the object a series describes, and `overrideHonorLabels: true` means it always wins over the series' own same-named field. Left undropped, a series with no exported_%s (a genuinely cluster-scoped kind) would keep it as if it were real data." $n $n) -}}
{{- end -}}
{{- if lt (get $restoreIdx $n) 0 -}}
{{- fail (printf "observability-emitters: `kube-state-metrics.prometheus.monitor.http.metricRelabelings` carries no rule restoring %q from `exported_%s`. Under `overrideHonorLabels: true`, a kube-state-metrics series that carries its own %q collides with the target's stamp of the same name and survives ONLY as `exported_%s` — without this rule the object's own value is lost outright, not merely mislabelled. Add: `- sourceLabels: [exported_%s]` / `  regex: (.+)` / `  targetLabel: %s`." $n $n $n $n $n $n) -}}
{{- end -}}
{{- if lt (get $exportedDropIdx $n) 0 -}}
{{- fail (printf "observability-emitters: `kube-state-metrics.prometheus.monitor.http.metricRelabelings` carries no `labeldrop` of %q. Without it every kube-state-metrics series keeps a redundant `exported_%s` label alongside the restored %q, doubling the same value under two names on every series this component produces." (printf "exported_%s" $n) $n $n) -}}
{{- end -}}
{{- if ge (get $bareDropIdx $n) 0 -}}
{{- if ge (get $restoreIdx $n) 0 -}}
{{- if not (lt (get $bareDropIdx $n) (get $restoreIdx $n)) -}}
{{- fail (printf "observability-emitters: `kube-state-metrics.prometheus.monitor.http.metricRelabelings` restores %q before dropping the target's own stamp of it, not after. The restore has to read `exported_%s` into a %q that has ALREADY been cleared of the target-stamped value, or the restore is immediately shadowed by the drop that follows it — reorder so the `labeldrop` of %q comes first." $n $n $n $n) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if ge (get $restoreIdx $n) 0 -}}
{{- if ge (get $exportedDropIdx $n) 0 -}}
{{- if not (lt (get $restoreIdx $n) (get $exportedDropIdx $n)) -}}
{{- fail (printf "observability-emitters: `kube-state-metrics.prometheus.monitor.http.metricRelabelings` drops %q before the rule that reads it to restore %q. Reorder so the restore runs first." (printf "exported_%s" $n) $n) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if lt $finalDeriveIdx 0 -}}
{{- fail "observability-emitters: `kube-state-metrics.prometheus.monitor.http.metricRelabelings` carries no rule mapping `namespace` to `k8s_namespace_name`. Without it, `k8s_namespace_name` is never derived from the (corrected) object namespace at all, and every kube_* series is left with whatever this release's default scrape class stamped instead — this release's own namespace, not the namespace of the object each series describes." -}}
{{- end -}}
{{- if ge (get $restoreIdx "namespace") 0 -}}
{{- if not (lt (get $restoreIdx "namespace") $finalDeriveIdx) -}}
{{- fail "observability-emitters: `kube-state-metrics.prometheus.monitor.http.metricRelabelings` derives `k8s_namespace_name` from `namespace` before the rule that restores `namespace` from `exported_namespace`. Reordered this way, `k8s_namespace_name` is derived from the target's own (wrong) namespace, not the object's — move the `namespace`-to-`k8s_namespace_name` rule after the restore." -}}
{{- end -}}
{{- end -}}
{{- if not (lt $k8sNsDropIdx $finalDeriveIdx) -}}
{{- fail "observability-emitters: `kube-state-metrics.prometheus.monitor.http.metricRelabelings` derives `k8s_namespace_name` before its own leading `labeldrop`. The derive has to write into a label that has already been cleared, or the old stamp and the new derivation could both apply depending on relabel-engine specifics that should not matter here — reorder so the `labeldrop` of `k8s_namespace_name` comes first." -}}
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
{{- include "observability-emitters.validate.otlpExternal" . -}}
{{- end -}}
{{- end -}}

{{/*
External OTLP ingest (`otlp.external`): the ways to accept data from
outside the cluster and file it under an identity the caller chose.
*/}}
{{- define "observability-emitters.validate.otlpExternal" -}}
{{- $e := .Values.otlp.external -}}
{{- if $e.enabled -}}
{{- if not $e.headers -}}
{{- fail "observability-emitters: `otlp.external.enabled` is true but `otlp.external.headers` is empty. The headers are the ONLY source of the caller's identity — the payload's own claims are deleted — so with none, every external record would be stored with no identity at all, or (were it not dropped) with whatever the sender wrote. Map each header the gateway sets to the attribute it becomes, e.g. `x-roster-subject: enduser.id`." -}}
{{- end -}}
{{- $v := .Values.otlp -}}
{{- if or (eq (int $e.httpPort) (int $v.service.grpcPort)) (eq (int $e.httpPort) (int $v.service.httpPort)) (eq (int $e.httpPort) 8888) -}}
{{- fail (printf "observability-emitters: `otlp.external.httpPort` (%d) is the same as the in-cluster OTLP ports or the collector's own telemetry port. External and in-cluster traffic must never share a receiver — they have opposite identity rules (a pod's identity is its connection, an external caller's is a header the gateway set). Use a port of its own." (int $e.httpPort)) -}}
{{- end -}}
{{- $seen := dict -}}
{{- range $kind, $m := dict "headers" $e.headers "attributes" ($e.attributes | default dict) -}}
{{- range $k, $val := $m -}}
{{- $attr := ternary $val $k (eq $kind "headers") | toString -}}
{{- if not (regexMatch "^[A-Za-z0-9_.-]+$" $attr) -}}
{{- fail (printf "observability-emitters: `otlp.external.%s` names the attribute %q, which is not a plain attribute name ([A-Za-z0-9_.-]). The name is written into the collector's own transform language." $kind $attr) -}}
{{- end -}}
{{- if or (hasPrefix "k8s." $attr) (hasPrefix "kubernetes." $attr) -}}
{{- fail (printf "observability-emitters: `otlp.external.%s` writes the attribute %q. Everything under `k8s.` / `kubernetes.` is the scoping key (cluster and namespace) and is derived by the collector, never taken from a request — a header that could set it would let a caller file its data under another workload's namespace. Use another attribute name." $kind $attr) -}}
{{- end -}}
{{- if has $attr (list "telemetry.source" "deployment.environment.name") -}}
{{- fail (printf "observability-emitters: `otlp.external.%s` writes the attribute %q, which the chart stamps itself on every external record (`telemetry.source=external`, the tier from `tenancy.environment`). Use another attribute name." $kind $attr) -}}
{{- end -}}
{{- if hasKey $seen $attr -}}
{{- fail (printf "observability-emitters: `otlp.external` writes the attribute %q twice (headers and static attributes together). The second write would silently win; keep one." $attr) -}}
{{- end -}}
{{- $_ := set $seen $attr true -}}
{{- end -}}
{{- end -}}
{{- if not $e.networkPolicy.ingressFrom -}}
{{- fail "observability-emitters: `otlp.external.enabled` is true but `otlp.external.networkPolicy.ingressFrom` is empty. The collector trusts the identity headers on this port, so the port must be reachable from the gateway's Envoy pods ONLY: any other pod that could connect would set the headers itself. Name the peers (a namespaceSelector and podSelector in one entry)." -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
node-exporter's refusals.

Every one is a way for the DaemonSet to be Ready, scraped and WRONG or
silent, which is the failure this whole file exists for:

  - no metrics agent, or no ServiceMonitor: nothing reads it;
  - the pod's own network or PID namespace: `node_network_*` and
    `node_processes_*` then describe a pod, not a node, and every series
    still carries the node's name;
  - a `relabelings` list that lost `job`, `instance` or `node`: the
    dashboard and the k8s-stack's `node.rules` select on
    `job="node-exporter"` and group on `instance` and `node`, so the rules
    record nothing and the dashboard's pickers are empty, with the series
    sitting in the store.
*/}}
{{- define "observability-emitters.validate.nodeExporter" -}}
{{- if .Values.nodeExporter.enabled -}}
{{- $ne := index .Values "prometheus-node-exporter" -}}
{{- if not .Values.metrics.enabled -}}
{{- fail "observability-emitters: `nodeExporter.enabled` is true but `metrics.enabled` is false. node-exporter is scraped by this chart's OWN metrics agent, through the ServiceMonitor every scrape object on the cluster is — with the agent off, nothing scrapes it at all. The pods run, stay Ready and answer every /metrics request; nothing ever reads one. Enable `metrics`, or leave `nodeExporter` off." -}}
{{- end -}}
{{- $mon := ($ne.prometheus).monitor | default dict -}}
{{- if not $mon.enabled -}}
{{- fail "observability-emitters: `prometheus-node-exporter.prometheus.monitor.enabled` is false, so no ServiceMonitor is rendered for node-exporter. A component with no scrape object looks exactly like a component with nothing wrong: the DaemonSet is Ready everywhere and no node series ever reaches a store." -}}
{{- end -}}
{{- if not $ne.hostNetwork -}}
{{- fail "observability-emitters: `prometheus-node-exporter.hostNetwork` is false. node-exporter would then read the POD's network namespace: `node_network_*` reports the pod's one virtual interface, and every series is still labelled with the node's name. Leave it true." -}}
{{- end -}}
{{- if not $ne.hostPID -}}
{{- fail "observability-emitters: `prometheus-node-exporter.hostPID` is false. node-exporter would then read the POD's PID namespace, so `node_processes_*` and `node_procs_*` describe a pod of two processes while carrying the node's name. Leave it true." -}}
{{- end -}}
{{- if not (($ne.hostRootFsMount).enabled) -}}
{{- fail "observability-emitters: `prometheus-node-exporter.hostRootFsMount.enabled` is false. Without the host's root filesystem the filesystem collector reads the container's own mounts: `node_filesystem_*` describes an overlay, not the node's disks, and the disk-full panels and alerts read healthy while the node fills. Leave it true." -}}
{{- end -}}
{{- $set := dict -}}
{{- range $r := ($mon.relabelings | default list) -}}
{{- if $r.targetLabel -}}
{{- $_ := set $set $r.targetLabel true -}}
{{- end -}}
{{- end -}}
{{- range $want := list "job" "instance" "node" -}}
{{- if not (hasKey $set $want) -}}
{{- fail (printf "observability-emitters: `prometheus-node-exporter.prometheus.monitor.relabelings` has no step setting `%s`. The node-exporter dashboard and the k8s-stack's `node.rules` and `kube-prometheus-node-recording.rules` select on `job=\"node-exporter\"` and group on `instance` and `node`, so without it the rules record nothing and the dashboard's pickers are empty, with the series sitting in the store. The default list sets all three; keep them when you replace it." $want) -}}
{{- end -}}
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

{{/*
HTTP probes: the exporter they need, and the module they name.

A probe with no exporter renders a VMProbe pointing at a Service that does
not exist, which the agent scrapes, fails, and reports as `probe_success`
absent — an alert on `== 0` then never fires, and the endpoint is watched
by nothing. An unknown module is the same silence one level down: the
exporter answers 400 and no `probe_success` series is ever written.
*/}}
{{- define "observability-emitters.validate.probes" -}}
{{- $probes := (.Values.metrics.scrape.probes | default list) -}}
{{- if $probes -}}
{{- if not .Values.blackboxExporter.enabled -}}
{{- fail "observability-emitters: `metrics.scrape.probes` lists probes but `blackboxExporter.enabled` is false. A probe is a VMProbe aimed at the blackbox exporter; without it the probe points at a Service that does not exist, no `probe_success` series is ever written, and an alert on `probe_success == 0` never fires — the endpoint is watched by nothing while everything reports healthy. Set `blackboxExporter.enabled: true`." -}}
{{- end -}}
{{- $names := dict -}}
{{- range $p := $probes -}}
{{- if hasKey $names $p.name -}}
{{- fail (printf "observability-emitters: `metrics.scrape.probes` has two probes named %q. The name is the `probe` label and the VMProbe's name, so the second would replace the first. Rename one." $p.name) -}}
{{- end -}}
{{- $_ := set $names $p.name true -}}
{{- $m := default "http_2xx" $p.module -}}
{{- if not (hasKey ($.Values.blackboxExporter.modules | default dict) $m) -}}
{{- fail (printf "observability-emitters: probe %q names module %q, which is not a key of `blackboxExporter.modules` (%s). The exporter would answer 400 to every probe and write no `probe_success` series, so the alert on it could never fire. Name an existing module, or define it under `blackboxExporter.modules`." $p.name $m (keys $.Values.blackboxExporter.modules | sortAlpha | join ", ")) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
The CloudWatch reader (`cloudwatchLogs`).

Each of these is a reader that starts, reports healthy and reads nothing
(or writes nowhere): an empty region or group is a poll that finds no log
group, a missing destination is a pipeline with no exporter, and a missing
stream-field list files every resource attribute as a stream field.
*/}}
{{- define "observability-emitters.validate.cloudwatchLogs" -}}
{{- $c := .Values.cloudwatchLogs -}}
{{- if $c.enabled -}}
{{- if not $c.region -}}
{{- fail "observability-emitters: `cloudwatchLogs.enabled` is true but `cloudwatchLogs.region` is empty. The receiver would poll no region and read nothing, while the pod stays Ready. Name the AWS region of the log group." -}}
{{- end -}}
{{- if not (hasPrefix "/" ($c.logGroup | default "")) -}}
{{- fail "observability-emitters: `cloudwatchLogs.logGroup` must be the log group's name, starting with `/` (for an EKS control plane, `/aws/eks/<cluster>/cluster`). It is matched as a prefix when the receiver discovers groups, so a short or empty value would read every group the role can see." -}}
{{- end -}}
{{- if not $c.streamPrefixes -}}
{{- fail "observability-emitters: `cloudwatchLogs.streamPrefixes` is empty, so the receiver would read EVERY stream of the log group: for an EKS control plane that is the API server, authenticator, controller manager and scheduler logs as well as the audit log, several times the volume, and not what `match` was written for. Name the stream prefix (`kube-apiserver-audit-`)." -}}
{{- end -}}
{{- $dest := include "observability-emitters.effectiveOtlpDestinations" (dict "root" . "signal" "logs") | fromYamlArray -}}
{{- if not $dest -}}
{{- fail "observability-emitters: `cloudwatchLogs.enabled` is true but there is no log destination (`otlp.destinations.logs` or `remote`). The pipeline would have no exporter, and everything it read would be discarded." -}}
{{- end -}}
{{- if not .Values.otlp.streamFields -}}
{{- fail "observability-emitters: `cloudwatchLogs` writes through the gateway's `otlp.streamFields`, and that list is empty, so no `VL-Stream-Fields` header would be sent and VictoriaLogs would treat every resource attribute as a stream field. Name the fields (see `otlp.streamFields`)." -}}
{{- end -}}
{{- if has $c.namespace (list "kube-system" "default") -}}
{{- fail (printf "observability-emitters: `cloudwatchLogs.namespace` is %q, a namespace workloads run in. Everything this reader writes is filed under it, and a read grant on that namespace would then read the API server's audit events. Use a namespace of its own (the default, `kube-audit`)." $c.namespace) -}}
{{- end -}}
{{- end -}}
{{- end -}}

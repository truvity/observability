{{/*
Every refusal in this chart.

The rule for what belongs here: a value whose wrong setting is SILENT.
A store that refuses to start is loud and gets fixed in ten minutes; a
store that starts with a retention of ninety months, a proxy that admits
every token, a Grafana whose dashboards never update, or a backup job
against an empty source are all installs that report success. Those fail
the render instead, and every one of them has a fixture under
tests/invalid/observability-stack/ that must keep failing.

Each `fail` says what is wrong, what it would have caused, and what to
write instead. An error message that only says "invalid value" is a
second debugging session.
*/}}

{{- define "observability-stack.validate" -}}
{{- include "observability-stack.validate.ha" . -}}
{{- include "observability-stack.validate.retention" . -}}
{{- include "observability-stack.validate.disk" . -}}
{{- include "observability-stack.validate.resources" . -}}
{{- include "observability-stack.validate.licence" . -}}
{{- include "observability-stack.validate.selfAlerts" . -}}
{{- include "observability-stack.validate.mirrors" . -}}
{{- include "observability-stack.validate.notifier" . -}}
{{- include "observability-stack.validate.notifications" . -}}
{{- include "observability-stack.validate.tenancy" . -}}
{{- include "observability-stack.validate.grafana" . -}}
{{- include "observability-stack.validate.routeOverlap" . -}}
{{- end -}}

{{/*
Zone redundancy.

None of the three stores replicates across a zone in a way that survives
one, so `ha` means two independent installs with the writers holding the
redundancy. With fewer than two zones there is nothing to be redundant
across, and an install labelled highly available is one nobody looks at
again.
*/}}
{{- define "observability-stack.validate.ha" -}}
{{- if .Values.ha -}}
{{- if lt (len .Values.zones) 2 -}}
{{- fail (printf "observability-stack: `ha` is true with %d zone(s). No store here replicates across a zone: high availability means two independent installs and writers that send to both, so it needs at least two entries in `zones`. Set `ha: false` for a single-zone install — it is the supported shape, not a lesser one." (len .Values.zones)) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Retention, per store, always with a unit.

All three binaries read a bare number as MONTHS. `retentionPeriod: 90`
is not ninety days, it is seven and a half years, and the first anyone
hears of it is the volume filling up a quarter later. The month suffix is
a capital `M`; a lowercase `m` is rejected by the binary at startup,
which at least is loud.
*/}}
{{- define "observability-stack.validate.retention" -}}
{{- $shape := "^[0-9]+(h|d|w|M|y)$" -}}
{{- $sites := list
    (dict "key" "victoria-metrics-k8s-stack.vmsingle.spec.retentionPeriod" "value" ((((index .Values "victoria-metrics-k8s-stack").vmsingle).spec).retentionPeriod))
    (dict "key" "victoria-logs-single.server.retentionPeriod" "value" (((index .Values "victoria-logs-single").server).retentionPeriod))
    (dict "key" "victoria-traces-single.server.retentionPeriod" "value" (((index .Values "victoria-traces-single").server).retentionPeriod))
-}}
{{- range $site := $sites -}}
{{- $v := $site.value -}}
{{- if $v -}}
{{- if not (regexMatch $shape (toString $v)) -}}
{{- fail (printf "observability-stack: %s is %q, which has no unit this chart accepts. A bare number is read as MONTHS by every store in this family, so `90` is seven and a half years of data on a volume sized for three months. Write the unit: h, d, w, M (capital, months) or y — for example `90d`." $site.key (toString $v)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Disk guards, which are not the same flag on each product.

The metrics store has NO `-retention.maxDiskUsagePercent` and no
`-retention.maxDiskSpaceUsageBytes`; its only guard is
`-storage.minFreeDiskSpaceBytes`, and reaching it means read-only, not
"drop the oldest". The log and trace stores have all three — and the two
retention guards are MUTUALLY EXCLUSIVE: the binary calls Fatal and does
not start when both are set, which turns a values typo into a store that
never comes back.

Also refused: an `*AuthKey` flag on a store. Those keys OVERRIDE
`-httpAuth.*` rather than adding to it, so setting one takes an endpoint
out of the store's own authentication and puts it behind a secret in a
query string, where it lands in every access log.
*/}}
{{- define "observability-stack.validate.disk" -}}
{{- $vm := (((index .Values "victoria-metrics-k8s-stack").vmsingle).spec) | default dict -}}
{{- range $flag := list "retention.maxDiskUsagePercent" "retention.maxDiskSpaceUsageBytes" -}}
{{- if hasKey ($vm.extraArgs | default dict) $flag -}}
{{- fail (printf "observability-stack: victoria-metrics-k8s-stack.vmsingle.spec.extraArgs has `%s`, which single-node VictoriaMetrics does not have. That flag exists only on VictoriaLogs and VictoriaTraces; the metrics store would refuse to start on an unknown flag. Its disk guard is `storage.minFreeDiskSpaceBytes`, which is already set." $flag) -}}
{{- end -}}
{{- end -}}
{{- $logs := ((index .Values "victoria-logs-single").server) | default dict -}}
{{- $logsPercent := or $logs.retentionMaxDiskUsagePercent (index ($logs.extraArgs | default dict) "retention.maxDiskUsagePercent") -}}
{{- $logsBytes := or $logs.retentionDiskSpaceUsage (index ($logs.extraArgs | default dict) "retention.maxDiskSpaceUsageBytes") -}}
{{- if and $logsPercent $logsBytes -}}
{{- fail "observability-stack: the log store has both a percentage and a byte disk guard (`retentionMaxDiskUsagePercent` and `retentionDiskSpaceUsage`, in either their own keys or extraArgs). VictoriaLogs refuses to start when both are set — it is Fatal, not a warning — so this renders a store that never comes up. Keep one and clear the other." -}}
{{- end -}}
{{- $traces := ((index .Values "victoria-traces-single").server) | default dict -}}
{{- $tracesPercent := or $traces.retentionMaxDiskUsagePercent (index ($traces.extraArgs | default dict) "retention.maxDiskUsagePercent") -}}
{{- $tracesBytes := or $traces.retentionDiskSpaceUsage (index ($traces.extraArgs | default dict) "retention.maxDiskSpaceUsageBytes") -}}
{{- if and $tracesPercent $tracesBytes -}}
{{- fail "observability-stack: the trace store has both a percentage and a byte disk guard. VictoriaTraces refuses to start when both are set, the same way VictoriaLogs does. Keep one and clear the other." -}}
{{- end -}}
{{- range $where, $args := dict "victoria-metrics-k8s-stack.vmsingle.spec.extraArgs" ($vm.extraArgs | default dict) "victoria-logs-single.server.extraArgs" ($logs.extraArgs | default dict) "victoria-traces-single.server.extraArgs" ($traces.extraArgs | default dict) -}}
{{- range $flag, $_ := $args -}}
{{- if hasSuffix "AuthKey" $flag -}}
{{- fail (printf "observability-stack: %s sets `%s`. An authKey flag does not add to `-httpAuth.*`, it REPLACES it for those endpoints: with one set, basic auth is never checked and the endpoint is reachable by anyone who has the key in a query string. Leave it unset and the store's own credentials guard the endpoint." $where $flag) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Resources: requests equal to limits, and an INTEGER CPU.

VictoriaMetrics sizes its thread pool from the cgroup CPU quota and
rounds DOWN, so `1500m` buys one thread and the remaining 500m is paid
for and never used. And a component whose requests are below its limits
is in a lower QoS class, so it is evicted first under node pressure —
which is the moment the store is most needed and the alerting path most
load-bearing.

Unset is not neutral either: with `resources` empty the operator applies
its own defaults, and its default CPU for VMSingle is 1200m, which is
the rounding failure by default.
*/}}
{{- define "observability-stack.validate.resources" -}}
{{- $sites := list
    (dict "key" "vmauth.resources" "value" .Values.vmauth.resources)
    (dict "key" "vmalert.resources" "value" .Values.vmalert.resources)
    (dict "key" "alertmanager.resources" "value" .Values.alertmanager.resources)
    (dict "key" "victoria-metrics-k8s-stack.vmsingle.spec.resources" "value" ((((index .Values "victoria-metrics-k8s-stack").vmsingle).spec).resources))
    (dict "key" "victoria-metrics-k8s-stack.victoria-metrics-operator.resources" "value" ((index (index .Values "victoria-metrics-k8s-stack") "victoria-metrics-operator").resources))
    (dict "key" "victoria-logs-single.server.resources" "value" (((index .Values "victoria-logs-single").server).resources))
    (dict "key" "victoria-traces-single.server.resources" "value" (((index .Values "victoria-traces-single").server).resources))
    (dict "key" "grafana.resources" "value" (.Values.grafana).resources)
-}}
{{- range $site := $sites -}}
{{- $r := $site.value | default dict -}}
{{- if $r -}}
{{- $requests := $r.requests | default dict -}}
{{- $limits := $r.limits | default dict -}}
{{- if or (not $requests.cpu) (not $limits.cpu) (not $requests.memory) (not $limits.memory) -}}
{{- fail (printf "observability-stack: %s does not set both requests and limits for cpu and memory. Leaving one side out is how a store ends up in a lower QoS class and is evicted first under node pressure, which is the moment it is most needed." $site.key) -}}
{{- end -}}
{{- if not (regexMatch "^[0-9]+$" (toString $requests.cpu)) -}}
{{- fail (printf "observability-stack: %s.requests.cpu is %q. The VictoriaMetrics binaries size their thread pool from the cgroup CPU quota and round it DOWN, so a fractional value such as 1500m buys exactly one thread and pays for 1.5. Write a whole number of cores: \"1\", \"2\", \"4\"." $site.key (toString $requests.cpu)) -}}
{{- end -}}
{{- if ne (toString $requests.cpu) (toString $limits.cpu) -}}
{{- fail (printf "observability-stack: %s has requests.cpu %q and limits.cpu %q. They must be equal: a component whose requests are below its limits is Burstable, and Burstable pods are evicted before Guaranteed ones." $site.key (toString $requests.cpu) (toString $limits.cpu)) -}}
{{- end -}}
{{- if ne (toString $requests.memory) (toString $limits.memory) -}}
{{- fail (printf "observability-stack: %s has requests.memory %q and limits.memory %q. They must be equal, for the same reason the CPUs must." $site.key (toString $requests.memory) (toString $limits.memory)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
The Enterprise boundary, as a refusal.

The VictoriaMetrics family ships a community edition (Apache 2.0) and an
Enterprise edition whose binaries need a licence key. An Enterprise image
pulled by accident RUNS — it refuses only the Enterprise features — so
nothing reports that the estate is now in breach of the vendor's terms.
A tag is the only thing a chart can see, so a tag containing `enterprise`
is refused, and no `-license` or `-licenseFile` flag is ever rendered.

The vmauth version floor lives here too. `default_vm_access_claim` needs
v1.147.0, and v1.147.0 through v1.151.x matched `match_claims` values
UNANCHORED (GHSA-f99m-22fh-qw96), so a claim value of `admin` also
matched `not-admin-really` — an authorisation bypass in the exact
mechanism this chart selects users with.
*/}}
{{- define "observability-stack.validate.licence" -}}
{{- $vmks := index .Values "victoria-metrics-k8s-stack" -}}
{{- $tags := list
    (dict "key" "vmauth.image.tag" "value" .Values.vmauth.image.tag)
    (dict "key" "backup.metrics.image.tag" "value" .Values.backup.metrics.image.tag)
    (dict "key" "backup.image.tag" "value" .Values.backup.image.tag)
    (dict "key" "victoria-metrics-k8s-stack.vmsingle.spec.image.tag" "value" ((($vmks.vmsingle).spec).image).tag)
    (dict "key" "victoria-metrics-k8s-stack.victoria-metrics-operator.image.tag" "value" (((index $vmks "victoria-metrics-operator").image)).tag)
    (dict "key" "victoria-logs-single.server.image.tag" "value" ((((index .Values "victoria-logs-single").server).image)).tag)
    (dict "key" "victoria-traces-single.server.image.tag" "value" ((((index .Values "victoria-traces-single").server).image)).tag)
    (dict "key" "victoria-logs-single.server.image.variant" "value" ((((index .Values "victoria-logs-single").server).image)).variant)
    (dict "key" "victoria-traces-single.server.image.variant" "value" ((((index .Values "victoria-traces-single").server).image)).variant)
-}}
{{- range $tag := $tags -}}
{{- if $tag.value -}}
{{- if contains "enterprise" (lower (toString $tag.value)) -}}
{{- fail (printf "observability-stack: %s is %q. This chart wraps the COMMUNITY edition only. An Enterprise image without a licence key starts and serves, refusing only the Enterprise features, so nothing reports that the estate is in breach of the vendor's terms — which is why this is a refusal and not a warning. See docs/doctrine.md." $tag.key (toString $tag.value)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $licenceSites := list
    (dict "key" "victoria-metrics-k8s-stack.global.license" "value" (($vmks.global).license))
    (dict "key" "victoria-logs-single.license" "value" ((index .Values "victoria-logs-single").license))
    (dict "key" "victoria-traces-single.license" "value" ((index .Values "victoria-traces-single").license))
-}}
{{- range $site := $licenceSites -}}
{{- $l := $site.value | default dict -}}
{{- if or $l.key (($l.keyRef).name) (($l.secret).name) -}}
{{- fail (printf "observability-stack: %s carries a licence key. A licence key is only useful to an Enterprise binary, and this chart renders none: it wraps the community edition, which is Apache 2.0 and free for any number of tenants or clusters. Remove it, or install the vendor's own chart directly." $site.key) -}}
{{- end -}}
{{- end -}}
{{- range $where, $args := dict "vmauth.extraArgs" .Values.vmauth.extraArgs "victoria-metrics-k8s-stack.vmsingle.spec.extraArgs" ((($vmks.vmsingle).spec).extraArgs | default dict) "victoria-logs-single.server.extraArgs" ((((index .Values "victoria-logs-single").server).extraArgs) | default dict) "victoria-traces-single.server.extraArgs" ((((index .Values "victoria-traces-single").server).extraArgs) | default dict) -}}
{{- range $flag, $_ := ($args | default dict) -}}
{{- if or (eq $flag "license") (eq $flag "licenseFile") (hasPrefix "license." $flag) -}}
{{- fail (printf "observability-stack: %s sets `%s`. A licence flag is an Enterprise flag, and this chart never renders one: the community binaries ignore it at best and refuse to start at worst, and an install that needs it is an install this chart is the wrong shape for." $where $flag) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $tag := trimPrefix "v" (toString .Values.vmauth.image.tag) -}}
{{- if not (semverCompare ">=1.152.0" $tag) -}}
{{- fail (printf "observability-stack: vmauth.image.tag is %q. The floor for this design is v1.152.0: `default_vm_access_claim` arrived in v1.147.0, and every build from v1.138.0, where claim matching was introduced, to v1.151.x matched `match_claims` values unanchored (GHSA-f99m-22fh-qw96), so `admin` also matched `not-admin-really` — an authorisation bypass in the mechanism that selects which user a token is. Pin v1.152.0 or later." (toString .Values.vmauth.image.tag)) -}}
{{- end -}}
{{- end -}}

{{/*
Self-alerts: metric-name pairs that must arrive together, and a rule
enabled with a metric name but no usable threshold — shapes the schema
cannot express on its own, mirroring the exact refusal
`charts/platform-alerts` already gives its own `stores[].freeSpaceMetric`
pair.
*/}}
{{- define "observability-stack.validate.selfAlerts" -}}
{{- $sa := .Values.selfAlerts -}}
{{- if and $sa.cardinality.hourlyCurrentSeriesMetric (not $sa.cardinality.hourlyMaxSeriesMetric) -}}
{{- fail "observability-stack: `selfAlerts.cardinality.hourlyCurrentSeriesMetric` is set but `hourlyMaxSeriesMetric` is not. StoreCardinalityNearLimit (hourly) would compare a gauge against nothing; set both, or neither." -}}
{{- end -}}
{{- if and $sa.cardinality.hourlyMaxSeriesMetric (not $sa.cardinality.hourlyCurrentSeriesMetric) -}}
{{- fail "observability-stack: `selfAlerts.cardinality.hourlyMaxSeriesMetric` is set but `hourlyCurrentSeriesMetric` is not. StoreCardinalityNearLimit (hourly) would compare a gauge against nothing; set both, or neither." -}}
{{- end -}}
{{- if and $sa.cardinality.dailyCurrentSeriesMetric (not $sa.cardinality.dailyMaxSeriesMetric) -}}
{{- fail "observability-stack: `selfAlerts.cardinality.dailyCurrentSeriesMetric` is set but `dailyMaxSeriesMetric` is not. StoreCardinalityNearLimit (daily) would compare a gauge against nothing; set both, or neither." -}}
{{- end -}}
{{- if and $sa.cardinality.dailyMaxSeriesMetric (not $sa.cardinality.dailyCurrentSeriesMetric) -}}
{{- fail "observability-stack: `selfAlerts.cardinality.dailyMaxSeriesMetric` is set but `dailyCurrentSeriesMetric` is not. StoreCardinalityNearLimit (daily) would compare a gauge against nothing; set both, or neither." -}}
{{- end -}}
{{- range $store := list "logs" "traces" -}}
{{- $g := index $sa.diskGuard $store -}}
{{- if and $g.freeSpaceMetric (not $g.freeSpaceLimitMetric) -}}
{{- fail (printf "observability-stack: `selfAlerts.diskGuard.%s.freeSpaceMetric` is set but `freeSpaceLimitMetric` is not. StoreDiskNearGuard for that store would compare a gauge against nothing; set both, or neither." $store) -}}
{{- end -}}
{{- if and $g.freeSpaceLimitMetric (not $g.freeSpaceMetric) -}}
{{- fail (printf "observability-stack: `selfAlerts.diskGuard.%s.freeSpaceLimitMetric` is set but `freeSpaceMetric` is not. StoreDiskNearGuard for that store would compare a gauge against nothing; set both, or neither." $store) -}}
{{- end -}}
{{- end -}}
{{- if and $sa.logStreamChurn.streamsCreatedMetric (not (gt ($sa.logStreamChurn.maxCreatedPerWindow | int) 0)) -}}
{{- fail "observability-stack: `selfAlerts.logStreamChurn.streamsCreatedMetric` is set but `maxCreatedPerWindow` is not above zero, so LogStreamsChurning would fire on the first stream ever created. State the rate your own estate's normal field cardinality produces." -}}
{{- end -}}
{{- if and $sa.writer.bufferMetric (not (gt ($sa.writer.maxBufferBytes | int) 0)) -}}
{{- fail "observability-stack: `selfAlerts.writer.bufferMetric` is set but `maxBufferBytes` is not above zero, so WriterBufferGrowing would fire on any buffered byte at all. State the size a healthy buffer holds between flushes." -}}
{{- end -}}
{{- end -}}

{{/*
The mirrors.

Helm evaluates a subchart's values before any template runs, so a parent
chart cannot compute them: a value that has to reach an upstream chart
has to be written there as well. That is the whole of the duplication in
values.yaml, and it is enforced rather than documented, because two
numbers that are supposed to be equal stop being equal the first time
somebody changes one of them — and a deduplication window wider than the
scrape interval silently discards good samples rather than failing.
*/}}
{{- define "observability-stack.validate.mirrors" -}}
{{- $vmks := index .Values "victoria-metrics-k8s-stack" -}}
{{- $metricsOn := and $vmks.enabled ($vmks.vmsingle).enabled -}}
{{- $logsOn := and (index .Values "victoria-logs-single").enabled (((index .Values "victoria-logs-single").server).enabled) -}}
{{- $tracesOn := and (index .Values "victoria-traces-single").enabled (((index .Values "victoria-traces-single").server).enabled) -}}
{{- $dedup := index ((($vmks.vmsingle).spec).extraArgs | default dict) "dedup.minScrapeInterval" -}}
{{- if and $metricsOn $dedup (ne (toString $dedup) (toString .Values.interval)) -}}
{{- fail (printf "observability-stack: `interval` is %q but victoria-metrics-k8s-stack.vmsingle.spec.extraArgs['dedup.minScrapeInterval'] is %q. They are one value: deduplication keeps one sample per window, so a window wider than the scrape interval silently drops good samples, and a narrower one deduplicates nothing. Set both to %q." (toString .Values.interval) (toString $dedup) (toString .Values.interval)) -}}
{{- end -}}
{{- $want := .Values.storeCredentials.secretName -}}
{{- if not $want -}}
{{- fail "observability-stack: `storeCredentials.secretName` is empty, so the stores would run with no `-httpAuth.*` at all and anything that can reach a Service could read every namespace's data around the proxy. Name the Secret the estate created; this chart never creates one." -}}
{{- end -}}
{{- $envSites := list -}}
{{- if $metricsOn -}}{{- $envSites = append $envSites (dict "key" "victoria-metrics-k8s-stack.vmsingle.spec.extraEnvs" "value" ((($vmks.vmsingle).spec).extraEnvs)) -}}{{- end -}}
{{- if $logsOn -}}{{- $envSites = append $envSites (dict "key" "victoria-logs-single.server.env" "value" (((index .Values "victoria-logs-single").server).env)) -}}{{- end -}}
{{- if $tracesOn -}}{{- $envSites = append $envSites (dict "key" "victoria-traces-single.server.env" "value" (((index .Values "victoria-traces-single").server).env)) -}}{{- end -}}
{{- range $site := $envSites -}}
{{- $found := dict -}}
{{- range $env := ($site.value | default list) -}}
{{- if or (eq $env.name "VM_httpAuth_username") (eq $env.name "VM_httpAuth_password") -}}
{{- $_ := set $found $env.name ((($env.valueFrom).secretKeyRef).name | default "") -}}
{{- end -}}
{{- end -}}
{{- range $name := list "VM_httpAuth_username" "VM_httpAuth_password" -}}
{{- $got := index $found $name -}}
{{- if not (hasKey $found $name) -}}
{{- fail (printf "observability-stack: %s has no `%s` entry, so that store would accept unauthenticated requests from anywhere in the cluster — around the proxy, and around every filter the proxy would have applied. Add it, reading from the Secret named in `storeCredentials.secretName`." $site.key $name) -}}
{{- end -}}
{{- if ne $got $want -}}
{{- fail (printf "observability-stack: %s reads `%s` from Secret %q, but `storeCredentials.secretName` is %q. The proxy authenticates to the stores with the credentials from `storeCredentials`, so a store reading a different Secret is a store the proxy cannot reach — and nothing says so until the first query returns 401. Set both to the same name." $site.key $name $got $want) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Tenancy.

The names in a grant are interpolated into a filter expression, so this
is the same security boundary `pkg/tenancy` enforces in Go and for the
same reason: a namespace named `a|b` or `.*` would not look odd in a
rendered filter, it would widen the grant it appears in. Such a name is
refused rather than escaped.

The paths are checked too. vmauth has no deny primitive that the operator
exposes, so a route that must not exist is a route that must not be
written — and the operator's default for a targetRef without `paths` is
`/.*`, which includes `/internal/*`, where the store's own authKey flags
would override its `-httpAuth.*`.
*/}}
{{- define "observability-stack.validate.notifier" -}}
{{- if .Values.vmalert.enabled -}}
{{- if and (not .Values.alertmanager.enabled) (not .Values.alertmanager.notifierUrl) -}}
{{- fail "observability-stack: vmalert is enabled, Alertmanager is not, and `alertmanager.notifierUrl` is empty. vmalert would evaluate every rule and send the result nowhere — which looks exactly like an estate with no problems, for as long as nobody checks. Enable Alertmanager, or name the one the estate already runs." -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Notifications: the one router, refused into existence rather than left a
free-form passthrough. See docs/notifications.md and docs/safety.md.
*/}}
{{- define "observability-stack.validate.notifications" -}}
{{- $n := .Values.notifications | default dict -}}
{{- $slack := $n.slack | default dict -}}
{{- $webhooks := $n.webhook | default list -}}
{{- $severities := $n.severities | default dict -}}
{{- $routes := $n.routes | default list -}}
{{- $also := $n.also | default list -}}
{{- $webhookNames := dict -}}
{{- range $w := $webhooks -}}{{- $_ := set $webhookNames $w.name true -}}{{- end -}}
{{- $configured := or ($slack.webhookSecret).name (gt (len $webhooks) 0) -}}
{{- if and .Values.alertmanager.enabled (not $configured) -}}
{{- fail "observability-stack: `alertmanager.enabled` is true and `notifications` configures no receiver kind — no `notifications.slack.webhookSecret` and no `notifications.webhook` entries. Alertmanager then routes to the `blackhole` shape this chart exists to retire: vmalert evaluates every rule and the result reaches nobody, and nothing about the install looks unhealthy. Configure at least one receiver kind under `notifications`, or set `alertmanager.enabled: false` and point `alertmanager.notifierUrl` at one the estate already runs." -}}
{{- end -}}
{{- /*
`notifications.externalUrl` and `vmalert.externalUrl` are one fact — the
base URL a link leaving the cluster should point at — kept as two
values only because `vmalert.externalUrl` has to keep working on its
own for an install with `alertmanager.enabled: false`, which has no
`notifications` block at all. Two inputs for one fact is refused rather
than left to disagree quietly: see docs/doctrine.md, "One input, two
shapes".
*/ -}}
{{- if and $n.externalUrl .Values.vmalert.externalUrl (ne $n.externalUrl .Values.vmalert.externalUrl) -}}
{{- fail (printf "observability-stack: notifications.externalUrl is %q and vmalert.externalUrl is %q. They are the same fact — the base URL a link leaving the cluster should point at — so a difference between them is a difference nobody notices until an alert fires and one link works while the other does not. Set them to the same value, or leave notifications.externalUrl unset and let it default to vmalert.externalUrl." (toString $n.externalUrl) (toString .Values.vmalert.externalUrl)) -}}
{{- end -}}
{{- $effectiveExternalUrl := $n.externalUrl | default .Values.vmalert.externalUrl -}}
{{- if and $configured (not $effectiveExternalUrl) -}}
{{- fail "observability-stack: `notifications` configures a receiver but neither `notifications.externalUrl` nor `vmalert.externalUrl` is set. One of them is the base of the Grafana link this chart puts in every Slack message; without it, every link a message carries points at nothing a person can open." -}}
{{- end -}}
{{- /*
A receiver kind with no default route for a severity is the blackhole
again, one layer down: the wrapping route's own receiver is a no-op, so
a severity `severities` does not cover reaches it silently.
*/ -}}
{{- if $configured -}}
{{- range $tier := list "critical" "warning" -}}
{{- if not (hasKey $severities $tier) -}}
{{- fail (printf "observability-stack: `notifications` configures a receiver but `notifications.severities.%s` is not set. Every route this chart renders falls back to a no-op receiver when none of `severities`, `routes` or `also` match, so a %s alert with no default reaches nobody and looks routed." $tier $tier) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- /*
A severity naming a receiver that does not exist. `slack` is the one
literal keyword; anything else must be a name from `notifications.webhook`.
*/ -}}
{{- range $tier, $cfg := $severities -}}
{{- if and $cfg.receiver (ne $cfg.receiver "slack") (not (hasKey $webhookNames $cfg.receiver)) -}}
{{- fail (printf "observability-stack: notifications.severities.%s.receiver is %q, which is neither \"slack\" nor the name of an entry in notifications.webhook. A route to a receiver that is not configured looks like a route and reaches nobody." $tier (toString $cfg.receiver)) -}}
{{- end -}}
{{- if and (eq $cfg.receiver "slack") (not ($slack.webhookSecret).name) -}}
{{- fail (printf "observability-stack: notifications.severities.%s.receiver is \"slack\" but notifications.slack.webhookSecret is not set. A route to a receiver kind that is not configured looks like a route and reaches nobody." $tier) -}}
{{- end -}}
{{- end -}}
{{- /*
A route matching outside the vocabulary the collectors actually stamp.
Anything else matches nothing a rule carries and pages nobody while
looking exactly like a route that works.
*/ -}}
{{- range $i, $r := $routes -}}
{{- range $k, $_ := ($r.match | default dict) -}}
{{- if not (has $k (list "k8s_cluster_name" "k8s_namespace_name")) -}}
{{- fail (printf "observability-stack: notifications.routes[%d].match has key %q. The collectors this chart's rules run against stamp exactly two dimensions on every alert — k8s_cluster_name and k8s_namespace_name — so a route on anything else (tenant, env, team, …) matches nothing any rule actually carries." $i (toString $k)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- /*
`also` names a webhook that is not configured. This bridge exists to
reach a webhook beside the normal route; a name that resolves to nothing
is a route that reaches nobody, same as the severities check above.
*/ -}}
{{- range $i, $a := $also -}}
{{- if not (hasKey $webhookNames $a.receiver) -}}
{{- fail (printf "observability-stack: notifications.also[%d].receiver is %q, which is not the name of any notifications.webhook entry." $i (toString $a.receiver)) -}}
{{- end -}}
{{- end -}}
{{- /*
The deadman's own repeat interval against the far end's timeout, when the
estate has stated one: the heartbeat has to land comfortably inside it, or
a single delayed delivery reads as the estate being down.
*/ -}}
{{- $watchdog := .Values.alertmanager.watchdog -}}
{{- if and $watchdog.secretName $watchdog.timeout -}}
{{- $repeatS := include "observability-stack.durationSeconds" $watchdog.repeatInterval | int64 -}}
{{- $timeoutS := include "observability-stack.durationSeconds" $watchdog.timeout | int64 -}}
{{- if ge $repeatS $timeoutS -}}
{{- fail (printf "observability-stack: alertmanager.watchdog.repeatInterval is %q and alertmanager.watchdog.timeout is %q. The heartbeat must land comfortably INSIDE the far end's own timeout, or a single delayed delivery reads as the estate being down when it is not. repeatInterval must be strictly less than timeout." (toString $watchdog.repeatInterval) (toString $watchdog.timeout)) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "observability-stack.validate.tenancy" -}}
{{- $shape := "^[a-z0-9]([a-z0-9-]*[a-z0-9])?$" -}}
{{- $fieldShape := "^[a-zA-Z0-9_][a-zA-Z0-9_./-]*$" -}}
{{- $audienceShape := "^\\S+$" -}}
{{- $t := .Values.tenancy -}}
{{- if and $t.principals (not $t.issuerUrl) -}}
{{- fail "observability-stack: `tenancy.principals` is set but `tenancy.issuerUrl` is empty. vmauth verifies a token against the issuer's OIDC discovery document; with no issuer there is nothing to verify a signature against, and a proxy that trusts an unverified token is worse than no proxy at all." -}}
{{- end -}}
{{- /*
The audience, which is the other half of "whose token is this".

The issuer above says the token was signed by the right issuer. Nothing
in vmauth says it was minted for THIS PROXY: it validates a token's
expiry and, under OIDC discovery, its issuer, and stops. There is no
audience option, and `aud` is never inspected. The only place the check
can be made is `matchClaims`, which is where the pin goes.
*/}}
{{- if and $t.principals (not $t.audience) -}}
{{- fail "observability-stack: `tenancy.principals` is set but `tenancy.audience` is empty. vmauth validates a token's EXPIRY and, under OIDC discovery, its ISSUER, and nothing else: it has no audience option and never inspects `aud` on its own. So each reader below would be selected by its group alone, and ANY unexpired token that issuer minted would be admitted whatever client it was minted for — a token the same person holds for another application of the same issuer reads their namespaces here, and nothing reports it, because the token verifies and the filters apply. Set it to the client id this proxy's tokens are minted under; it is pinned into every reader's `matchClaims` as `aud`, which is the only place vmauth can be made to check it." -}}
{{- end -}}
{{- if and $t.audience (not (regexMatch $audienceShape (toString $t.audience))) -}}
{{- fail (printf "observability-stack: `tenancy.audience` is %q, which is not an identifier (%s): it carries whitespace or a newline. A client id may otherwise be anything the issuer assigned — a dot, an `@`, a colon — and is ESCAPED where it is rendered rather than refused here, because the issuer chooses it and this chart does not. What this shape refuses is a value that arrived from the wrong place: a file read with its trailing newline, a heredoc, or two ids in one string." (toString $t.audience) $audienceShape) -}}
{{- end -}}
{{- if eq (toString $t.claimName) "aud" -}}
{{- fail "observability-stack: `tenancy.claimName` is `aud`, which is the claim `tenancy.audience` is pinned under. Both are entries in one `matchClaims` map, so one would overwrite the other — and whichever survived would decide either which principal a token is or which client it was minted for, never both. Name the groups claim something else." -}}
{{- end -}}
{{- /*
The keys.

Each is a value with a default, and the defaults are what
charts/observability-emitters stamps. A key is interpolated into a filter
expression exactly as a name is, so a log field carrying stream-filter
syntax is refused for the same reason a namespace name is; the metrics
keys are held to the Prometheus label shape by the schema. And the two
keys of one signal must differ: one name for both dimensions is a filter
that selects on one of them and ignores the other.
*/}}
{{- range $key, $field := dict "logsClusterField" $t.logsClusterField "logsNamespaceField" $t.logsNamespaceField -}}
{{- if not (regexMatch $fieldShape (toString $field)) -}}
{{- fail (printf "observability-stack: `tenancy.%s` is %q, which is not a log field name (%s). A log field name may carry dots, and nothing that is stream-filter syntax: a quote, a brace, a comma, an equals sign, a `|`, a colon or a space would end the filter early or open a second alternative beside it, and the grant would be wider than the one somebody wrote. Such a name is refused, never escaped. Leave it at the default, which is the field charts/observability-emitters writes." $key (toString $field) $fieldShape) -}}
{{- end -}}
{{- end -}}
{{- if eq (toString $t.clusterLabel) (toString $t.namespaceLabel) -}}
{{- fail (printf "observability-stack: `tenancy.clusterLabel` and `tenancy.namespaceLabel` are both %q. The two are the scoping key; a metrics filter with one name for both dimensions selects on one of them and ignores the other, so every grant would be wider or narrower than written." (toString $t.clusterLabel)) -}}
{{- end -}}
{{- if eq (toString $t.logsClusterField) (toString $t.logsNamespaceField) -}}
{{- fail (printf "observability-stack: `tenancy.logsClusterField` and `tenancy.logsNamespaceField` are both %q. One stream filter would carry one dimension twice and the other not at all." (toString $t.logsClusterField)) -}}
{{- end -}}
{{- $groups := dict -}}
{{- range $i, $p := $t.principals -}}
{{- if not $p.group -}}
{{- fail (printf "observability-stack: tenancy.principals[%d] has no `group`. The group is what the token's claim is matched against; without it the entry selects nobody." $i) -}}
{{- end -}}
{{- if hasKey $groups $p.group -}}
{{- fail (printf "observability-stack: group %q appears twice in `tenancy.principals`. The second entry would be unreachable, so a grant somebody wrote would silently not apply." $p.group) -}}
{{- end -}}
{{- $_ := set $groups $p.group true -}}
{{- if not $p.grants -}}
{{- fail (printf "observability-stack: principal %q has no grants. A principal that may read nothing is written by leaving it out, not by granting it nothing." $p.group) -}}
{{- end -}}
{{- $clusters := dict -}}
{{- range $g := $p.grants -}}
{{- if not (regexMatch $shape (toString $g.cluster)) -}}
{{- fail (printf "observability-stack: principal %q has cluster %q, which is not a plain name (%s). The cluster is half of the scoping key, and names are interpolated into a filter expression, so one carrying `|`, `)` or `.*` would widen the grant rather than look odd. Such a name is refused, never escaped." $p.group (toString $g.cluster) $shape) -}}
{{- end -}}
{{- if hasKey $clusters $g.cluster -}}
{{- fail (printf "observability-stack: principal %q is granted cluster %q twice. Merge them, or one grant is silently ignored." $p.group $g.cluster) -}}
{{- end -}}
{{- $_ := set $clusters $g.cluster true -}}
{{- if and $g.allNamespaces $g.namespaces -}}
{{- fail (printf "observability-stack: principal %q grants cluster %q with both `allNamespaces` and a `namespaces` list. One of them is wrong, and guessing which is how a grant quietly widens." $p.group $g.cluster) -}}
{{- end -}}
{{- if and (not $g.allNamespaces) (not $g.namespaces) -}}
{{- fail (printf "observability-stack: principal %q grants cluster %q with neither `namespaces` nor `allNamespaces`. An empty list is refused rather than read as \"everything\": a project that expands to no namespaces is the likeliest way a grant widens by accident. A grant is namespaces on a cluster; a project is the derivation that produces the list, and it lives with whoever writes this file." $p.group $g.cluster) -}}
{{- end -}}
{{- range $ns := ($g.namespaces | default list) -}}
{{- if not (regexMatch $shape (toString $ns)) -}}
{{- fail (printf "observability-stack: principal %q grants namespace %q, which is not a plain name (%s). It would be interpolated into a filter expression, where `|` or `.*` widens the grant." $p.group (toString $ns) $shape) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- /*
The trace route cannot be scoped, so it is not rendered until somebody
says that is acceptable.

vmauth applies a principal's grant by substituting it into the route it
forwards on. VictoriaTraces' Jaeger and Tempo select APIs accept no
query argument to substitute it into — their handlers take a tenant id
from headers, `hidden_fields_filters` (which hides fields, not rows) and
`allow_partial_response`, and nothing else — so a reader given those
paths reads every namespace's spans on every cluster, whatever
`defaultVMAccessClaim` says beside it.

Rendering it anyway, because it looks like the metrics and logs routes,
is exactly the failure this chart was fixed to remove. So it is a
refusal with a value whose name says what accepting it means.
*/}}
{{- if and $t.principals (include "observability-stack.tracesEnabled" .) -}}
{{- if not $t.allowUnfilteredTraceReads -}}
{{- fail "observability-stack: a trace store is enabled and `tenancy.principals` is set, but `tenancy.allowUnfilteredTraceReads` is not. The proxy enforces a grant by substituting the principal's filter into the route it forwards on, and VictoriaTraces' Jaeger and Tempo select APIs accept NO query argument to substitute it into: their handlers take a tenant id from headers, `hidden_fields_filters` (which hides fields from a result, not rows) and `allow_partial_response`, and nothing else. There is no way through this proxy to give one principal a narrower view of traces than another, so the trace read route would be an unscoped route sitting beside two scoped ones and looking identical to them. Either turn the trace store off, or set `tenancy.allowUnfilteredTraceReads: true` and record that every principal who can reach the proxy reads every namespace's spans on every cluster. Metrics and logs are unaffected either way." -}}
{{- end -}}
{{- end -}}
{{- /*
And the enforcement the other two routes DO carry, asserted here rather
than assumed.

A `targetRef` whose `query_args` lost its placeholder renders cleanly,
installs, answers every query and scopes none of them — the claim is
still computed, still correct, still in the manifest, and still
discarded. This is the one refusal in this file that guards against an
edit to this chart rather than against a value somebody wrote.
*/}}
{{- range $signal := (list "metrics" "logs") -}}
{{- $arg := include (printf "observability-stack.filterArg.%s" $signal) $ -}}
{{- $placeholder := include (printf "observability-stack.filterPlaceholder.%s" $signal) $ -}}
{{- $args := fromYamlArray (include (printf "observability-stack.readQueryArgs.%s" $signal) $) -}}
{{- $found := false -}}
{{- range $a := $args -}}
{{- if and (eq $a.name $arg) (has $placeholder $a.values) -}}
{{- $found = true -}}
{{- end -}}
{{- end -}}
{{- if not $found -}}
{{- fail (printf "observability-stack: the %s read route does not carry %s=%s. vmauth applies a `vm_access` claim ONLY by substituting a placeholder into the route, so without it every principal's query reaches the store unfiltered while `defaultVMAccessClaim` beside it still states the grant. Nothing downstream reports that: the render succeeds, the install succeeds, and a query for one namespace returns exactly what it would have returned if the filter had been applied." $signal $arg $placeholder) -}}
{{- end -}}
{{- end -}}
{{- /*
And the one flag that would hand the filter back to the caller.

vmauth forwards a client's query argument only when it does NOT clash
with one the route already set — that clash is what stops a reader
sending its own `extra_filters` alongside the enforced one.
`-mergeQueryArgs` names the arguments exempted from that rule, and the
exemption is total: the client's value is added rather than dropped.

On the log path an extra `extra_stream_filters` is AND-ed, so it could
only narrow. On the metrics path vmselect treats each `extra_filters`
as an ALTERNATIVE and ORs them, so a caller adding `{}` reads
everything — the whole grant, undone by one query argument, with the
claim and the route both still correct.

The flag's own default is empty and this chart sets no route argument
that anyone would legitimately want merged, so naming one of these here
is refused rather than trusted.
*/}}
{{- range $arg, $value := ($.Values.vmauth.extraArgs | default dict) -}}
{{- if eq (toString $arg) "mergeQueryArgs" -}}
{{- range $merged := (splitList "," (toString $value)) -}}
{{- if has (trim $merged) (list "extra_filters" "extra_filters[]" "extra_stream_filters") -}}
{{- fail (printf "observability-stack: `vmauth.extraArgs.mergeQueryArgs` names %q, which is the argument this chart enforces a principal's grant with. vmauth drops a client query argument that CLASHES with one the route already set, and that drop is the only thing stopping a reader from sending its own filter; `mergeQueryArgs` exempts an argument from it entirely. vmselect treats each `extra_filters` as an ALTERNATIVE and ORs them, so a caller adding an empty one reads every cluster and namespace — with the claim, the route and the filter all still exactly right. Remove it, or stop enforcing tenancy here." (trim $merged)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $paths := concat
    (fromYamlArray (include "observability-stack.readPaths.metrics" .))
    (fromYamlArray (include "observability-stack.readPaths.logs" .))
    (fromYamlArray (include "observability-stack.readPaths.traces" .))
    (fromYamlArray (include "observability-stack.writePaths.metrics" .))
    (fromYamlArray (include "observability-stack.writePaths.logs" .))
    (fromYamlArray (include "observability-stack.writePaths.traces" .))
-}}
{{- range $path := $paths -}}
{{- if regexMatch "^/(\\.\\*|\\*)?$" $path -}}
{{- fail (printf "observability-stack: %q is not a route, it is every route — including the write endpoints and `/internal/*`. Name the paths." $path) -}}
{{- end -}}
{{- range $denied := $.Values.vmauth.deniedPaths -}}
{{- if regexMatch $denied $path -}}
{{- fail (printf "observability-stack: the route %q matches the denied path %q. `/internal/*` carries the partition and snapshot APIs, and those endpoints have their own query-string auth keys which OVERRIDE `-httpAuth.*` — so a route to them through this proxy is a route around the stores' own authentication." $path $denied) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
No store's route may SWALLOW another store's.

vmauth matches `src_paths` in declaration order and stops at the first
hit, and both the writer and the reader declare their three stores in one
`url_map`: metrics, then logs, then traces. So a path belonging to an
earlier store that also matches a later store's path silently takes that
store's traffic, and the sender is told nothing useful — the wrong store
answers, with its own opinion of a request it was never meant to see.

That is not hypothetical. `/insert/.*` on the log store matched
`/insert/opentelemetry/v1/traces`, so every span went to the log store
and came back 400, and the trace store sat empty for as long as it took
somebody to send a span and then go and ASK the store whether it had
arrived. A 200 from the collector says nothing; the store is the only
witness.

This walks the declared order and refuses any earlier path that matches
a later one. A path's regular expression is probed with a concrete
string, because `regexMatch` compares a pattern against text and two
patterns cannot be compared directly.
*/}}
{{- define "observability-stack.validate.routeOverlap" -}}
{{- $groups := list
    (dict "kind" "read" "signal" "metrics" "paths" (fromYamlArray (include "observability-stack.readPaths.metrics" .)))
    (dict "kind" "read" "signal" "logs" "paths" (fromYamlArray (include "observability-stack.readPaths.logs" .)))
    (dict "kind" "read" "signal" "traces" "paths" (fromYamlArray (include "observability-stack.readPaths.traces" .)))
    (dict "kind" "write" "signal" "metrics" "paths" (fromYamlArray (include "observability-stack.writePaths.metrics" .)))
    (dict "kind" "write" "signal" "logs" "paths" (fromYamlArray (include "observability-stack.writePaths.logs" .)))
    (dict "kind" "write" "signal" "traces" "paths" (fromYamlArray (include "observability-stack.writePaths.traces" .)))
-}}
{{- range $i, $earlier := $groups -}}
{{- range $j, $later := $groups -}}
{{- if and (lt $i $j) (eq $earlier.kind $later.kind) -}}
{{- range $pattern := $earlier.paths -}}
{{- range $path := $later.paths -}}
{{- /* A concrete stand-in for whatever the later path's own wildcards
     would accept, so one pattern can be tested against the other. */ -}}
{{- $probe := $path | replace ".*" "x" | replace ".+" "x" | replace "[^/]+" "x" -}}
{{- if regexMatch (printf "^%s$" $pattern) $probe -}}
{{- fail (printf "observability-stack: the %s route %q on the %s store is declared before the %s store's %q and MATCHES it, so the %s store would answer every request meant for the %s store and the %s store would be unreachable. Narrow the first path; the store's own endpoint list is the right source for it." $earlier.kind $pattern $earlier.signal $later.signal $path $earlier.signal $later.signal $later.signal) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Grafana.

Everything here is a default whose wrong value leaves a Grafana that
looks fine: queries that return another team's data, a session that
survives its token, dashboards that never update, an alerting engine
nobody watches.
*/}}
{{- define "observability-stack.validate.grafana" -}}
{{- $g := .Values.grafana | default dict -}}
{{- if $g.enabled -}}
{{- $ini := index $g "grafana.ini" | default dict -}}
{{- range $section := list "unified_alerting" "alerting" -}}
{{- $s := index $ini $section | default dict -}}
{{- if $s.enabled -}}
{{- fail (printf "observability-stack: grafana.ini's [%s] is enabled. Alerting in this stack is vmalert and Alertmanager: the rules live in charts/platform-alerts where each one carries the incident that earned it, and a second alerting engine brings its own rules, its own silences and its own notification policies — a second place to look at three in the morning, and one nobody remembers to check." $section) -}}
{{- end -}}
{{- end -}}
{{- $oauth := index $ini "auth.generic_oauth" | default dict -}}
{{- if $oauth.enabled -}}
{{- if not $oauth.use_refresh_token -}}
{{- fail "observability-stack: grafana.ini's [auth.generic_oauth] has `use_refresh_token` unset or false, which is Grafana's own default and the reason for a particular support ticket: the session outlives the access token, so Grafana keeps the person signed in for up to 30 days while forwarding a token that expired an hour ago. The UI works, every query returns 401, and signing out and back in fixes it just long enough to make the report unreproducible." -}}
{{- end -}}
{{- if not $oauth.role_attribute_strict -}}
{{- fail "observability-stack: grafana.ini's [auth.generic_oauth] has `role_attribute_strict` unset or false, so a person whose claims map to no role is given the default one instead of being refused. An unmapped viewer is a support ticket; an unmapped editor is an incident." -}}
{{- end -}}
{{- end -}}
{{- $db := $ini.database | default dict -}}
{{- $lock := $db.locking_attempt_timeout_sec | default 0 | int -}}
{{- if or (lt $lock 60) (gt $lock 300) -}}
{{- fail (printf "observability-stack: grafana.ini's [database] `locking_attempt_timeout_sec` is %d; it must be between 60 and 300. Grafana takes a database lock through its schema migration at startup, and the default of 0 means \"do not wait\" — so the second replica of a rolling update finds the lock held and crash-loops through the migration." $lock) -}}
{{- end -}}
{{/*
More than one replica needs a database more than one replica can share.

Grafana's default is SQLite on the pod's own filesystem. With two
replicas that is either two databases with one dashboard each, or -- on a
shared ReadWriteOnce volume -- one file two processes write, which is the
shape that answers:

    500  database is locked (SQLITE_BUSY)

not at startup, but on whichever request happens to collide. Half the UI
works. Measured on a live install: two replicas, one volume, and a
console that failed on about one click in three.

So the replica count and the database are ONE decision, and the chart
refuses to let them be made separately. `[database] type` is the
caller's: `postgres` and `mysql` are both shared, `sqlite3` is not, and
unset means sqlite3.
*/}}
{{- $dbType := ($db.type | default "sqlite3") -}}
{{- if and (gt (int ($g.replicas | default 1)) 1) (eq $dbType "sqlite3") -}}
{{- fail (printf "observability-stack: Grafana is enabled with %d replicas and grafana.ini's [database] `type` is %q. SQLite is a file on one pod: with more than one replica each holds its own dashboards, users and preferences, or -- sharing one ReadWriteOnce volume -- they collide and the console answers 500 `database is locked` on whichever request loses, while the rest of the UI keeps working. Point `[database]` at a shared database, or run one replica." (int ($g.replicas | default 1)) $dbType) -}}
{{- end -}}

{{/*
A store nobody can query.

Enabling a store provisions it, gives it a volume, writes to it and
retains it. Whether anyone can READ it is a separate value -- the
datasource list -- and the two are edited in different parts of this
file. A store with no datasource is not broken: it ingests, it answers,
its dashboards are simply absent, and the only symptom is that nobody
ever looks at it.

That happened to the trace store, which had no datasource in this
chart's own defaults.

Only when Grafana is enabled here. An estate pointing its own Grafana at
this stack's proxy provisions datasources somewhere this chart cannot
see.
*/}}
{{- $dsTypes := dict
    "metrics" (list "prometheus" "victoriametrics-metrics-datasource")
    "logs" (list "victoriametrics-logs-datasource")
    "traces" (list "jaeger" "tempo" "victoriametrics-traces-datasource")
-}}
{{- $stores := dict
    "metrics" "victoria-metrics-k8s-stack"
    "logs" "victoria-logs-single"
    "traces" "victoria-traces-single"
-}}
{{- $declared := list -}}
{{- range $file, $doc := ($g.datasources | default dict) -}}
{{- range $ds := ($doc.datasources | default list) -}}
{{- $declared = append $declared (toString $ds.type) -}}
{{- end -}}
{{- end -}}
{{- range $signal, $subchart := $stores -}}
{{- if (index $.Values $subchart).enabled -}}
{{- $wanted := index $dsTypes $signal -}}
{{- $found := false -}}
{{- range $t := $declared -}}
{{- if has $t $wanted -}}
{{- $found = true -}}
{{- end -}}
{{- end -}}
{{- if not $found -}}
{{- fail (printf "observability-stack: the %s store is enabled and no Grafana datasource has a type that reads it (any of %s). The store will ingest, retain and answer for as long as it is up, and nobody will ever see it -- which is not a failure anything reports. Add a datasource, or turn the store off." $signal (join ", " $wanted)) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- if not (($g.admin).existingSecret) -}}
{{- fail "observability-stack: Grafana is enabled with no `grafana.admin.existingSecret`. The Grafana chart then generates a random admin password into a Secret of its own, freshly on every render: every `helm upgrade` rotates it, the release's manifest differs from itself when nothing changed, and the only person who can still log in is whoever wrote down the last one. Name a Secret the estate created." -}}
{{- end -}}
{{- $provider := ((($g.sidecar).dashboards).provider) | default dict -}}
{{- $interval := $provider.updateIntervalSeconds | default 0 | int -}}
{{- if le $interval 10 -}}
{{- fail (printf "observability-stack: grafana.sidecar.dashboards.provider.updateIntervalSeconds is %d. At 10 or below Grafana watches the filesystem instead of polling it, and a Kubernetes ConfigMap projection is a symlink swap that fires no watch event — so a dashboard change never lands and nothing reports an error. Use a value above 10." $interval) -}}
{{- end -}}
{{- range $file, $doc := ($g.datasources | default dict) -}}
{{- range $ds := ($doc.datasources | default list) -}}
{{- if not (($ds.jsonData).oauthPassThru) -}}
{{- fail (printf "observability-stack: Grafana datasource %q (in %s) does not set `jsonData.oauthPassThru: true`. Without it every query reaches the proxy as GRAFANA's identity rather than the signed-in person's, so the proxy scopes nothing and a viewer sees every namespace on every cluster Grafana can see. That is the failure this whole chart exists to prevent, and it looks exactly like a working dashboard." (toString $ds.name) $file) -}}
{{- end -}}
{{- if not (hasKey $ds "version") -}}
{{- fail (printf "observability-stack: Grafana datasource %q (in %s) has no `version`. With more than one replica Grafana only updates a provisioned datasource whose version is greater than or equal to the stored one, so an edit without a bump lands on a fresh install and nowhere else." (toString $ds.name) $file) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

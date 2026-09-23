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
{{- include "observability-stack.validate.mirrors" . -}}
{{- include "observability-stack.validate.notifier" . -}}
{{- include "observability-stack.validate.tenancy" . -}}
{{- include "observability-stack.validate.grafana" . -}}
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
{{- fail (printf "observability-stack: %s carries a licence key. A licence key is only useful to an Enterprise binary, and this chart renders none: it wraps the community edition, which is Apache 2.0 and free for any number of tenants. Remove it, or install the vendor's own chart directly." $site.key) -}}
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
{{- fail (printf "observability-stack: vmauth.image.tag is %q. The floor for this design is v1.152.0: `default_vm_access_claim` arrived in v1.147.0, and every build from v1.147.0 to v1.151.x matched `match_claims` values unanchored (GHSA-f99m-22fh-qw96), so `admin` also matched `not-admin-really` — an authorisation bypass in the mechanism that selects which user a token is. Pin v1.152.0 or later." (toString .Values.vmauth.image.tag)) -}}
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
{{- fail "observability-stack: `storeCredentials.secretName` is empty, so the stores would run with no `-httpAuth.*` at all and anything that can reach a Service could read every tenant's data around the proxy. Name the Secret the estate created; this chart never creates one." -}}
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
same reason: a tenant named `a|b` or `.*` would not look odd in a
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

{{- define "observability-stack.validate.tenancy" -}}
{{- $shape := "^[a-z0-9]([a-z0-9-]*[a-z0-9])?$" -}}
{{- $t := .Values.tenancy -}}
{{- if and $t.principals (not $t.issuerUrl) -}}
{{- fail "observability-stack: `tenancy.principals` is set but `tenancy.issuerUrl` is empty. vmauth verifies a token against the issuer's OIDC discovery document; with no issuer there is nothing to verify a signature against, and a proxy that trusts an unverified token is worse than no proxy at all." -}}
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
{{- $envs := dict -}}
{{- range $g := $p.grants -}}
{{- if not (regexMatch $shape (toString $g.env)) -}}
{{- fail (printf "observability-stack: principal %q has env %q, which is not a plain name (%s). Names are interpolated into a filter expression, so one carrying `|`, `)` or `.*` would widen the grant rather than look odd. Such a name is refused, never escaped." $p.group (toString $g.env) $shape) -}}
{{- end -}}
{{- if hasKey $envs $g.env -}}
{{- fail (printf "observability-stack: principal %q is granted env %q twice. Merge them, or one grant is silently ignored." $p.group $g.env) -}}
{{- end -}}
{{- $_ := set $envs $g.env true -}}
{{- if and $g.allTenants $g.tenants -}}
{{- fail (printf "observability-stack: principal %q grants env %q with both `allTenants` and a `tenants` list. One of them is wrong, and guessing which is how a grant quietly widens." $p.group $g.env) -}}
{{- end -}}
{{- if and (not $g.allTenants) (not $g.tenants) -}}
{{- fail (printf "observability-stack: principal %q grants env %q with neither `tenants` nor `allTenants`. An empty list is refused rather than read as \"everything\": a list that is empty because a derivation produced nothing is the likeliest way a grant widens by accident." $p.group $g.env) -}}
{{- end -}}
{{- range $tenant := ($g.tenants | default list) -}}
{{- if not (regexMatch $shape (toString $tenant)) -}}
{{- fail (printf "observability-stack: principal %q grants tenant %q, which is not a plain name (%s). It would be interpolated into a filter expression, where `|` or `.*` widens the grant." $p.group (toString $tenant) $shape) -}}
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
{{- fail (printf "observability-stack: Grafana datasource %q (in %s) does not set `jsonData.oauthPassThru: true`. Without it every query reaches the proxy as GRAFANA's identity rather than the signed-in person's, so the proxy scopes nothing and a viewer sees every tenant Grafana can see. That is the failure this whole chart exists to prevent, and it looks exactly like a working dashboard." (toString $ds.name) $file) -}}
{{- end -}}
{{- if not (hasKey $ds "version") -}}
{{- fail (printf "observability-stack: Grafana datasource %q (in %s) has no `version`. With more than one replica Grafana only updates a provisioned datasource whose version is greater than or equal to the stored one, so an edit without a bump lands on a fresh install and nowhere else." (toString $ds.name) $file) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

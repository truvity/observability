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

{{- /*
Alertmanager pair and NetworkPolicy (alertmanager.replicaCount > 1): NO
policy in this chart selects the Alertmanager pods, and none is added for
the pair, because the first policy that selects a pod default-denies it.
The mesh (9094, TCP and UDP, pod to pod) is therefore not blocked by any
policy rendered here. A consumer who adds their own policy to those pods
must admit 9094/TCP and 9094/UDP between the replicas, 9093/TCP from
vmalert and karma, or the pair splits and pages twice.
*/ -}}
{{- define "observability-stack.validate" -}}
{{- include "observability-stack.validate.mode" . -}}
{{- include "observability-stack.validate.ha" . -}}
{{- include "observability-stack.validate.retention" . -}}
{{- include "observability-stack.validate.disk" . -}}
{{- include "observability-stack.validate.resources" . -}}
{{- include "observability-stack.validate.licence" . -}}
{{- include "observability-stack.validate.selfAlerts" . -}}
{{- include "observability-stack.validate.slackWorkspaces" . -}}
{{- include "observability-stack.validate.evaluateOnly" . -}}
{{- include "observability-stack.validate.mirrors" . -}}
{{- include "observability-stack.validate.vendoredSyncSources" . -}}
{{- include "observability-stack.validate.seLinux" . -}}
{{- include "observability-stack.validate.backupPrefixes" . -}}
{{- include "observability-stack.validate.watchdogSource" . -}}
{{- include "observability-stack.validate.scrapeFrom" . -}}
{{- include "observability-stack.validate.clientsFrom" . -}}
{{- include "observability-stack.validate.notifier" . -}}
{{- include "observability-stack.validate.remoteEvaluators" . -}}
{{- include "observability-stack.validate.notifications" . -}}
{{- include "observability-stack.validate.karma" . -}}
{{- include "observability-stack.validate.tenancy" . -}}
{{- include "observability-stack.validate.writers" . -}}
{{- include "observability-stack.validate.alertReaders" . -}}
{{- include "observability-stack.validate.readers" . -}}
{{- include "observability-stack.validate.grafana" . -}}
{{- include "observability-stack.validate.routeOverlap" . -}}
{{- include "observability-stack.validate.k8sStackClusterLabel" . -}}
{{- end -}}

{{/*
`mode: operator-only` — a CONTRACT, not an override.

Setting it does not turn anything else off: every component the mode
does not run — vmauth, both vmalerts, Alertmanager, Grafana, backups,
the self-alerts, the metrics self-scrape, all three stores, and
`tenancy.principals`/`writers`/`readers`/`alertReaders` — must ALSO be turned off
(or left empty) explicitly, or this refuses. The alternative — the mode
silently forcing each of those off — is the shape this chart refuses
everywhere else a value could be computed instead of checked (see
docs/doctrine.md's note on mirrors): a caller who read `vmauth.enabled:
true` in their own values file and got no vmauth would be debugging a
proxy that was never going to exist, with nothing in this file saying so.
*/}}
{{- define "observability-stack.validate.mode" -}}
{{- $mode := .Values.mode | default "full" -}}
{{- if not (has $mode (list "full" "operator-only" "replica")) -}}
{{- fail (printf "observability-stack: `mode` is %q, which is none of \"full\", \"operator-only\" or \"replica\"." (toString $mode)) -}}
{{- end -}}
{{- if eq $mode "replica" -}}
{{- include "observability-stack.validate.replica" . -}}
{{- end -}}
{{- if eq $mode "operator-only" -}}
{{- $vmks := index .Values "victoria-metrics-k8s-stack" -}}
{{- if not $vmks.enabled -}}
{{- fail "observability-stack: `mode` is \"operator-only\" but `victoria-metrics-k8s-stack.enabled` is false. Operator-only mode is FOR the operator: leave the subchart itself on, with its own `vmsingle` and the rest off underneath it — this chart's own defaults already shape it that way." -}}
{{- end -}}
{{- if not (index $vmks "victoria-metrics-operator").enabled -}}
{{- fail "observability-stack: `mode` is \"operator-only\" but `victoria-metrics-k8s-stack.victoria-metrics-operator.enabled` is false. There would be nothing left for this install to run at all — not the operator, and, by the refusals beside this one, none of the stores, the proxy or Grafana either." -}}
{{- end -}}
{{- if ($vmks.vmsingle).enabled -}}
{{- fail "observability-stack: `mode` is \"operator-only\" but `victoria-metrics-k8s-stack.vmsingle.enabled` is true. Operator-only means no store: a cluster with no store of its own runs the operator so charts/observability-emitters' VMAgent custom resource has a controller to reconcile it, and writes on to a store elsewhere. Set `victoria-metrics-k8s-stack.vmsingle.enabled: false`, or drop `mode` back to \"full\"." -}}
{{- end -}}
{{- /*
The vendored chart's sync Job fetches rules and dashboards from upstream
sources over the network at deploy time and applies them directly —
gated on its OWN `syncJob.enabled`, upstream default `true`, independent
of `defaultRules`/`defaultDashboards` (which only shape what it fetches,
not whether it runs). Left at that default, operator-only mode would run
a Job every upgrade that has nothing to apply anything TO: no vmalert for
a fetched VMRule, no Grafana for a fetched dashboard.
*/ -}}
{{- if ($vmks.syncJob).enabled -}}
{{- fail "observability-stack: `mode` is \"operator-only\" but `victoria-metrics-k8s-stack.syncJob.enabled` is true. That Job fetches rules and dashboards from upstream sources and applies them directly, on its own switch — independent of `defaultRules`/`defaultDashboards` — and in this mode there is no vmalert for a fetched rule or Grafana for a fetched dashboard to reach. Set `victoria-metrics-k8s-stack.syncJob.enabled: false`, or drop `mode` back to \"full\"." -}}
{{- end -}}
{{- if (index .Values "victoria-logs-single").enabled -}}
{{- fail "observability-stack: `mode` is \"operator-only\" but `victoria-logs-single.enabled` is true. Operator-only ships no log store either — set it to `false`, or drop `mode` back to \"full\"." -}}
{{- end -}}
{{- if (index .Values "victoria-traces-single").enabled -}}
{{- fail "observability-stack: `mode` is \"operator-only\" but `victoria-traces-single.enabled` is true. Operator-only ships no trace store either — set it to `false`, or drop `mode` back to \"full\"." -}}
{{- end -}}
{{- if .Values.vmauth.enabled -}}
{{- fail "observability-stack: `mode` is \"operator-only\" but `vmauth.enabled` is true. There is no store here for the proxy to authorise reads or writes against — set `vmauth.enabled: false`, or drop `mode` back to \"full\"." -}}
{{- end -}}
{{- if .Values.vmalert.enabled -}}
{{- fail "observability-stack: `mode` is \"operator-only\" but `vmalert.enabled` is true. There is no store here for either vmalert to evaluate rules against — set `vmalert.enabled: false`, or drop `mode` back to \"full\"." -}}
{{- end -}}
{{- if .Values.alertmanager.enabled -}}
{{- fail "observability-stack: `mode` is \"operator-only\" but `alertmanager.enabled` is true. With vmalert refused above, nothing here would ever call it — set `alertmanager.enabled: false`, or drop `mode` back to \"full\"." -}}
{{- end -}}
{{- if .Values.karma.enabled -}}
{{- fail "observability-stack: `mode` is \"operator-only\" but `karma.enabled` is true. karma is a console for an Alertmanager, and this mode runs none — set `karma.enabled: false`, or drop `mode` back to \"full\"." -}}
{{- end -}}
{{- if (.Values.grafana).enabled -}}
{{- fail "observability-stack: `mode` is \"operator-only\" but `grafana.enabled` is true. There is no datasource here for it to read — set `grafana.enabled: false`, or drop `mode` back to \"full\"." -}}
{{- end -}}
{{- if .Values.backup.enabled -}}
{{- fail "observability-stack: `mode` is \"operator-only\" but `backup.enabled` is true. There is no store here to back up — set `backup.enabled: false`, or drop `mode` back to \"full\"." -}}
{{- end -}}
{{- if .Values.selfAlerts.enabled -}}
{{- fail "observability-stack: `mode` is \"operator-only\" but `selfAlerts.enabled` is true. Every one of its rules watches a component this mode does not run — set `selfAlerts.enabled: false`, or drop `mode` back to \"full\"." -}}
{{- end -}}
{{- if .Values.metricsSelfScrape.enabled -}}
{{- fail "observability-stack: `mode` is \"operator-only\" but `metricsSelfScrape.enabled` is true. There is no metrics store here for it to scrape into — set `metricsSelfScrape.enabled: false`, or drop `mode` back to \"full\"." -}}
{{- end -}}
{{- if .Values.tenancy.principals -}}
{{- fail "observability-stack: `mode` is \"operator-only\" but `tenancy.principals` is set. With `vmauth.enabled` refused above, no VMUser this chart renders would ever exist for a reader's grant to reach — configured and unreachable is the exact trap this chart refuses everywhere else. Clear `tenancy.principals`, or drop `mode` back to \"full\"." -}}
{{- end -}}
{{- if .Values.tenancy.writers -}}
{{- fail "observability-stack: `mode` is \"operator-only\" but `tenancy.writers` is set. There is no proxy here for a writer's bearer token to authenticate against. Clear `tenancy.writers`, or drop `mode` back to \"full\"." -}}
{{- end -}}
{{- if .Values.tenancy.readers -}}
{{- fail "observability-stack: `mode` is \"operator-only\" but `tenancy.readers` is set. There is no proxy here for a reader's bearer token to authenticate against. Clear `tenancy.readers`, or drop `mode` back to \"full\"." -}}
{{- end -}}
{{- if .Values.tenancy.alertReaders -}}
{{- fail "observability-stack: `mode` is \"operator-only\" but `tenancy.alertReaders` is set. There is no vmalert here for it to read. Clear `tenancy.alertReaders`, or drop `mode` back to \"full\"." -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
`mode: replica` -- the second half of a pair, a CONTRACT like
operator-only: it runs the three stores and nothing else, and refuses
every setting that says otherwise instead of quietly ignoring it.
*/}}
{{- define "observability-stack.validate.replica" -}}
{{- $vmks := index .Values "victoria-metrics-k8s-stack" -}}
{{- if not (include "observability-stack.ha" . | fromYaml).enabled -}}
{{- fail "observability-stack: `mode` is \"replica\" but `ha.enabled` is not true. A replica is the second half of a pair and exists only beside a primary release (`mode: full` with `ha: {enabled: true, peer: ...}`); on its own it is a store with no proxy, no alerting and no backup. Set `ha: {enabled: true}` and `zones`, or run `mode: full`. (The boolean `ha: true` is the legacy switch and does not turn the pair on.)" -}}
{{- end -}}
{{- if not $vmks.enabled -}}
{{- fail "observability-stack: `mode` is \"replica\" but `victoria-metrics-k8s-stack.enabled` is false. The metrics store is a VMSingle that subchart renders; leave it on." -}}
{{- end -}}
{{- if (index $vmks "victoria-metrics-operator").enabled -}}
{{- fail "observability-stack: `mode` is \"replica\" but `victoria-metrics-k8s-stack.victoria-metrics-operator.enabled` is true. The primary release runs the operator, and it reconciles this release's VMSingle (it watches the namespace). A second operator would fight it over every object, so set `victoria-metrics-k8s-stack.victoria-metrics-operator.enabled: false`." -}}
{{- end -}}
{{- if ne ($vmks.syncJob).enabled false -}}
{{- fail "observability-stack: `mode` is \"replica\" but `victoria-metrics-k8s-stack.syncJob.enabled` is not false. That Job fetches rules and dashboards from upstream and applies them directly, on its own switch and defaulting to true upstream: in a replica there is no vmalert or Grafana for it to feed, and the primary already ran it. Set `victoria-metrics-k8s-stack.syncJob.enabled: false`." -}}
{{- end -}}
{{- if not ($vmks.vmsingle).enabled -}}
{{- fail "observability-stack: `mode` is \"replica\" but `victoria-metrics-k8s-stack.vmsingle.enabled` is false. A replica holds the same stores as the primary; with the metrics store off the pair would have a log and a trace replica and one metrics store." -}}
{{- end -}}
{{- if .Values.vmauth.enabled -}}
{{- fail "observability-stack: `mode` is \"replica\" but `vmauth.enabled` is true. The proxy belongs to the primary release, which reads this release's stores through `ha.peer`; a second proxy would be a second, unaudited way in. Leave it unset or false." -}}
{{- end -}}
{{- if .Values.vmalert.enabled -}}
{{- fail "observability-stack: `mode` is \"replica\" but `vmalert.enabled` is true. The primary release renders one vmalert per store replica, this one's included, so identical rules are evaluated against each store by the same chart values. A vmalert here would evaluate every rule a second time. Leave it unset or false." -}}
{{- end -}}
{{- if .Values.alertmanager.enabled -}}
{{- fail "observability-stack: `mode` is \"replica\" but `alertmanager.enabled` is true. The Alertmanager pair belongs to the primary release. Leave it unset or false." -}}
{{- end -}}
{{- if .Values.karma.enabled -}}
{{- fail "observability-stack: `mode` is \"replica\" but `karma.enabled` is true. karma is a console for the primary's Alertmanager. Set `karma.enabled: false`." -}}
{{- end -}}
{{- if (.Values.grafana).enabled -}}
{{- fail "observability-stack: `mode` is \"replica\" but `grafana.enabled` is true. Grafana reads through the primary's proxy. Set `grafana.enabled: false`." -}}
{{- end -}}
{{- if .Values.backup.enabled -}}
{{- fail "observability-stack: `mode` is \"replica\" but `backup.enabled` is true. Only the primary backs up: both releases would write the same bucket prefix, and a backup deletes everything under its prefix that is not in its own source, so two of them take turns erasing each other's snapshots. Set `backup.enabled: false` here." -}}
{{- end -}}
{{- if .Values.selfAlerts.enabled -}}
{{- fail "observability-stack: `mode` is \"replica\" but `selfAlerts.enabled` is true. Every rule it holds watches a component this mode does not run, or is the primary's own. `selfAlerts.storeMemory.enabled` is the one rule a replica can carry, and it has its own switch." -}}
{{- end -}}
{{- if .Values.tenancy.principals -}}
{{- fail "observability-stack: `mode` is \"replica\" but `tenancy.principals` is set. There is no proxy here for a reader's grant to reach; grants live on the primary. Clear it." -}}
{{- end -}}
{{- if .Values.tenancy.writers -}}
{{- fail "observability-stack: `mode` is \"replica\" but `tenancy.writers` is set. There is no proxy here for a writer's token to authenticate against. Clear it." -}}
{{- end -}}
{{- if .Values.tenancy.readers -}}
{{- fail "observability-stack: `mode` is \"replica\" but `tenancy.readers` is set. There is no proxy here for a reader's token to authenticate against. Clear it." -}}
{{- end -}}
{{- if .Values.tenancy.alertReaders -}}
{{- fail "observability-stack: `mode` is \"replica\" but `tenancy.alertReaders` is set. There is no vmalert here for it to read. Clear it." -}}
{{- end -}}
{{- end -}}

{{/*
Zone redundancy, and the pair.

None of the three stores replicates across a zone in a way that survives
one, so `ha` means two independent installs with the writers holding the
redundancy. With fewer than two zones there is nothing to be redundant
across, and an install labelled highly available is one nobody looks at
again.

What `ha` then requires is what makes the second install a pair rather
than a second install: on the primary, the other release's store
addresses, a proxy that prefers this release and has more than one pod; on
both, the stores' own zone spread on the shared pair label and the replica
label on their scrape. The last two are values of the vendored charts,
which Helm evaluates before any template, so this chart cannot write them
and refuses instead (the rule for every mirror here).
*/}}
{{- define "observability-stack.validate.ha" -}}
{{- $mode := .Values.mode | default "full" -}}
{{- $haCfg := include "observability-stack.ha" . | fromYaml -}}
{{- if or $haCfg.enabled $haCfg.legacy -}}
{{- if lt (len .Values.zones) 2 -}}
{{- fail (printf "observability-stack: `ha` is enabled with %d zone(s). No store here replicates across a zone: high availability means two independent installs and writers that send to both, so it needs at least two entries in `zones`. Set `ha: false` for a single-zone install — it is the supported shape, not a lesser one." (len .Values.zones)) -}}
{{- end -}}
{{- end -}}
{{- if $haCfg.enabled -}}
{{- if eq $mode "operator-only" -}}
{{- fail "observability-stack: `ha.enabled` is true with `mode: operator-only`. An operator-only install has no store to pair." -}}
{{- end -}}
{{- $vmks := index .Values "victoria-metrics-k8s-stack" -}}
{{- $logs := index .Values "victoria-logs-single" -}}
{{- $traces := index .Values "victoria-traces-single" -}}
{{- $peer := $haCfg.peer -}}
{{- if eq $mode "full" -}}
{{- $eff := include "observability-stack.effectiveEnabled" . | fromYaml -}}
{{- if not $eff.vmauth -}}
{{- fail "observability-stack: `ha.enabled` is true on the primary but the proxy is off. The pair is read through `vmauth` (`first_available` over both stores); with it off nothing fails over and the second store is never read." -}}
{{- end -}}
{{- if ne .Values.vmauth.loadBalancingPolicy "first_available" -}}
{{- fail (printf "observability-stack: `ha.enabled` is true but `vmauth.loadBalancingPolicy` is %q. A read must go to this release's store and fall over to the peer only when it fails: `least_loaded` sends half of every dashboard to a store that may be empty or still catching up. Set `first_available`." (toString .Values.vmauth.loadBalancingPolicy)) -}}
{{- end -}}
{{- if lt (int .Values.vmauth.replicaCount) 2 -}}
{{- fail (printf "observability-stack: `ha.enabled` is true but `vmauth.replicaCount` is %d. The proxy is what both stores are read through; one pod takes every read and every cross-cluster write with its zone. Set it to 2 or more." (int .Values.vmauth.replicaCount)) -}}
{{- end -}}
{{- if and ($vmks.enabled) ($vmks.vmsingle).enabled (not $peer.metrics) -}}
{{- fail "observability-stack: `ha.enabled` is true but `ha.peer.metrics` is empty. The proxy needs the other release's metrics store (`http://<service>.<namespace>.svc:8428`) to fall over to, and the second vmalert needs it to evaluate against." -}}
{{- end -}}
{{- if and $logs.enabled (not $peer.logs) -}}
{{- fail "observability-stack: `ha.enabled` is true but `ha.peer.logs` is empty. Name the other release's log store (`http://<service>.<namespace>.svc:9428`), or turn the log store off in both releases." -}}
{{- end -}}
{{- if and $traces.enabled (not $peer.traces) -}}
{{- fail "observability-stack: `ha.enabled` is true but `ha.peer.traces` is empty. Name the other release's trace store (`http://<service>.<namespace>.svc:10428`), or turn the trace store off in both releases." -}}
{{- end -}}
{{- if and (not $logs.enabled) $peer.logs -}}
{{- fail "observability-stack: `ha.peer.logs` is set but the log store is off here. Both releases of a pair run the same stores; turn it off in both and clear the address." -}}
{{- end -}}
{{- if and (not $traces.enabled) $peer.traces -}}
{{- fail "observability-stack: `ha.peer.traces` is set but the trace store is off here. Both releases of a pair run the same stores; turn it off in both and clear the address." -}}
{{- end -}}
{{- else -}}
{{- if or $peer.metrics $peer.logs $peer.traces -}}
{{- fail "observability-stack: `ha.peer` is set on a `mode: replica` release. The replica is read by the primary, which names it; the replica names nobody. Clear it." -}}
{{- end -}}
{{- end -}}
{{- /* The same spread and the same label on every enabled store, either role. */ -}}
{{- if and $vmks.enabled ($vmks.vmsingle).enabled -}}
{{- include "observability-stack.validate.pairSpread" (list . "metrics" "victoria-metrics-k8s-stack.vmsingle.spec" (($vmks.vmsingle).spec | default dict).podMetadata (($vmks.vmsingle).spec | default dict).topologySpreadConstraints) -}}
{{- end -}}
{{- if $logs.enabled -}}
{{- include "observability-stack.validate.pairSpread" (list . "logs" "victoria-logs-single.server" ($logs.server | default dict).podLabels ($logs.server | default dict).topologySpreadConstraints) -}}
{{- include "observability-stack.validate.pairReplicaLabel" (list . "victoria-logs-single.server.serviceMonitor" (($logs.server | default dict).serviceMonitor | default dict)) -}}
{{- end -}}
{{- if $traces.enabled -}}
{{- include "observability-stack.validate.pairSpread" (list . "traces" "victoria-traces-single.server" ($traces.server | default dict).podLabels ($traces.server | default dict).topologySpreadConstraints) -}}
{{- include "observability-stack.validate.pairReplicaLabel" (list . "victoria-traces-single.server.serviceMonitor" (($traces.server | default dict).serviceMonitor | default dict)) -}}
{{- end -}}
{{- else -}}
{{- if or $haCfg.peer.metrics $haCfg.peer.logs $haCfg.peer.traces -}}
{{- fail "observability-stack: `ha.peer` is set but `ha.enabled` is not true. Nothing would read it; set `ha: true` (with `zones`), or clear `ha.peer`." -}}
{{- end -}}
{{- if .Values.selfAlerts.divergence.enabled -}}
{{- fail "observability-stack: `selfAlerts.divergence.enabled` is true but `ha.enabled` is not. The rule compares the two stores of a pair; a single install has one." -}}
{{- end -}}
{{- end -}}
{{- if and .Values.selfAlerts.divergence.enabled $haCfg.enabled (ne $mode "full") -}}
{{- fail "observability-stack: `selfAlerts.divergence.enabled` is true on a `mode: replica` release. The rule compares both stores and lives on the primary." -}}
{{- end -}}
{{- end -}}

{{/*
One store's half of the pair's zone spread, as the vendored chart's own
values carry it: a pod label `observability.pair: <pair.name>-<store>` and a
`topologySpreadConstraints` entry across `topology.kubernetes.io/zone`,
`DoNotSchedule`, selecting that label. Both are REQUIRED, and a hard
constraint on purpose: a zonal volume pins a replica to its zone, so a
spread that merely prefers lets the two replicas of a pair land in one
zone, which is the single-zone install with a second pod to pay for.
Arguments: (root, store, where, pod labels, constraints).
*/}}
{{- define "observability-stack.validate.pairSpread" -}}
{{- $root := index . 0 -}}
{{- $store := index . 1 -}}
{{- $where := index . 2 -}}
{{- $podMeta := index . 3 | default dict -}}
{{- $constraints := index . 4 | default list -}}
{{- /* A VMSingle names its pod labels under `podMetadata.labels`; the two StatefulSets' `podLabels` are the labels themselves. */ -}}
{{- $labels := ternary ($podMeta.labels | default dict) $podMeta (hasKey $podMeta "labels") -}}
{{- $want := printf "%s-%s" (include "observability-stack.ha" $root | fromYaml).name $store -}}
{{- if ne (index $labels "observability.pair" | default "") $want -}}
{{- fail (printf "observability-stack: `ha.enabled` is true but the %s store's pod label `observability.pair` is %q, not %q. Set `observability.pair: %s` under `%s` (`podMetadata.labels` for the VMSingle, `podLabels` for the two StatefulSets): the pair's zone spread and PodDisruptionBudget select on it, and the same value must be on both releases' pods. docs/high-availability.md has the block to paste." $store (index $labels "observability.pair" | default "") $want $want $where) -}}
{{- end -}}
{{- $ok := false -}}
{{- range $c := $constraints -}}
{{- $sel := ($c.labelSelector | default dict).matchLabels | default dict -}}
{{- if and (eq ($c.topologyKey | default "") "topology.kubernetes.io/zone") (eq ($c.whenUnsatisfiable | default "") "DoNotSchedule") (eq (index $sel "observability.pair" | default "") $want) -}}
{{- $ok = true -}}
{{- end -}}
{{- end -}}
{{- if not $ok -}}
{{- fail (printf "observability-stack: `ha.enabled` is true but `%s.topologySpreadConstraints` has no entry that spreads the %s store over zones. Add one with `topologyKey: topology.kubernetes.io/zone`, `whenUnsatisfiable: DoNotSchedule` and `labelSelector.matchLabels: {observability.pair: %s}`. It is required, not advisory: a zonal volume pins a replica to its zone, so a spread that only prefers can leave both replicas of the pair in one zone. docs/high-availability.md has the block to paste." $where $store $want) -}}
{{- end -}}
{{- end -}}

{{/*
A pair's replica label on a vendored store's own ServiceMonitor, which is
the only way a label reaches that scrape: `observability_replica` with this
release's letter, in `serviceMonitor.relabelings`. The divergence alert
tells the two stores apart by it. Arguments: (root, where, serviceMonitor).
*/}}
{{- define "observability-stack.validate.pairReplicaLabel" -}}
{{- $root := index . 0 -}}
{{- $where := index . 1 -}}
{{- $sm := index . 2 -}}
{{- if $sm.enabled -}}
{{- $want := include "observability-stack.pair.replica" $root -}}
{{- $ok := false -}}
{{- range $r := ($sm.relabelings | default list) -}}
{{- if and (eq ($r.targetLabel | default "") "observability_replica") (eq ($r.replacement | default "") $want) -}}
{{- $ok = true -}}
{{- end -}}
{{- end -}}
{{- if not $ok -}}
{{- fail (printf "observability-stack: `ha.enabled` is true but `%s.relabelings` has no entry setting `observability_replica` to %q. The store's own scrape has to say which replica it is, or the divergence alert cannot tell the pair apart: add `{action: replace, targetLabel: observability_replica, replacement: %s}`. docs/high-availability.md has the block to paste." $where $want $want) -}}
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

That is `resources.policy: guaranteed`. `burstable`, the default, is the
same judgement made for an estate that measured otherwise — components
using a few millicores, and CPU limits deliberately absent because the
estate measured CFS throttling. It relaxes the CPU half only, and only
where the reason above does not reach:

  - `requests.cpu` may be fractional and below the limit: the request is
    a scheduling hint, and no thread pool is sized from it;
  - `limits.cpu` may be absent (no quota, nothing to round); set, it is
    still a whole number, because the QUOTA is what rounds down;
  - both requests stay required: a pod with none is BestEffort, evicted
    before anything else;
  - memory stays request == limit, with a limit required. VictoriaMetrics
    sizes its caches from the cgroup MEMORY limit, so with none it sizes
    them from the node and the node OOM-kills it; and a pod whose memory
    use cannot exceed its request is never in the kubelet's first
    eviction tier under memory pressure — most of what `guaranteed` buys,
    kept.
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
    (dict "key" "backup.resources" "value" .Values.backup.resources)
-}}
{{- if .Values.karma.enabled -}}
{{- $sites = append $sites (dict "key" "karma.resources" "value" .Values.karma.resources) -}}
{{- end -}}
{{- $policy := (.Values.resources).policy | default "burstable" -}}
{{- range $site := $sites -}}
{{- $r := $site.value | default dict -}}
{{- if $r -}}
{{- $requests := $r.requests | default dict -}}
{{- $limits := $r.limits | default dict -}}
{{- /*
A `null` that SURVIVED to here. Helm deletes a null from this chart's own
values, so for vmauth, the vmalerts, Alertmanager and the backup jobs a
`limits: {cpu: null}` simply removes the default. For a key under a
vendored subchart (the operator, the stores, Grafana) Helm passes the
null through instead, and the rendered object carries `cpu: null` —
which the API server reads as a CPU limit of 0 and refuses (measured with
`just apply`: "must be less than or equal to cpu limit of 0"; VMSingle's
CRD refuses the null outright). Refused here, where the fix can be named.
The exception is this chart's own vmauth, vmalert and Alertmanager: their
templates drop a null (`observability-stack.resources`), so a values file
written when those carried a default CPU limit keeps meaning "no limit".
*/ -}}
{{- range $side, $m := dict "requests" $requests "limits" $limits -}}
{{- range $res, $q := $m -}}
{{- if and (kindIs "invalid" $q) (not (has $site.key (list "vmauth.resources" "vmalert.resources" "alertmanager.resources" "karma.resources"))) -}}
{{- fail (printf "observability-stack: %s.%s.%s is null. Helm deletes a null from this chart's own values, but passes it through unchanged to a vendored subchart's, and the API server then reads it as a quantity of 0 and refuses the object. A default on this component cannot be removed through values: under `resources.policy: burstable`, give it a whole number of cores well above the request instead (the node's core count leaves it effectively unthrottled)." $site.key $side (toString $res)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if eq $policy "burstable" -}}
{{- if or (not $requests.cpu) (not $requests.memory) (not $limits.memory) -}}
{{- fail (printf "observability-stack: %s does not set requests.cpu, requests.memory and limits.memory, which `resources.policy: burstable` still requires. A pod with no requests is BestEffort and is evicted before anything else; and with no memory limit VictoriaMetrics sizes its caches from the NODE's memory and is OOM-killed by it. Only the CPU limit may be left out." $site.key) -}}
{{- end -}}
{{- if and $limits.cpu (not (regexMatch "^[0-9]+$" (toString $limits.cpu))) -}}
{{- fail (printf "observability-stack: %s.limits.cpu is %q under `resources.policy: burstable`. The request may be fractional, the limit may not: the VictoriaMetrics binaries size their thread pool from the cgroup CPU QUOTA — the limit — and round it DOWN, so 1500m buys exactly one thread. Write a whole number of cores, or leave the CPU limit out." $site.key (toString $limits.cpu)) -}}
{{- end -}}
{{- if ne (toString $requests.memory) (toString $limits.memory) -}}
{{- fail (printf "observability-stack: %s has requests.memory %q and limits.memory %q. Under `resources.policy: burstable` too they must be equal: a pod whose memory use cannot exceed its request stays out of the kubelet's first eviction tier under memory pressure, which is what keeps a Burstable store from being evicted first." $site.key (toString $requests.memory) (toString $limits.memory)) -}}
{{- end -}}
{{- else -}}
{{- if or (not $requests.cpu) (not $limits.cpu) (not $requests.memory) (not $limits.memory) -}}
{{- fail (printf "observability-stack: %s does not set both requests and limits for cpu and memory. Leaving one side out is how a store ends up in a lower QoS class and is evicted first under node pressure, which is the moment it is most needed." $site.key) -}}
{{- end -}}
{{- if not (regexMatch "^[0-9]+$" (toString $requests.cpu)) -}}
{{- fail (printf "observability-stack: %s.requests.cpu is %q. The VictoriaMetrics binaries size their thread pool from the cgroup CPU quota and round it DOWN, so a fractional value such as 1500m buys exactly one thread and pays for 1.5. Write a whole number of cores: \"1\", \"2\", \"4\" — or, for an estate that measured its components at a few millicores, set `resources.policy: burstable`, which allows a fractional request." $site.key (toString $requests.cpu)) -}}
{{- end -}}
{{- if ne (toString $requests.cpu) (toString $limits.cpu) -}}
{{- fail (printf "observability-stack: %s has requests.cpu %q and limits.cpu %q. They must be equal: a component whose requests are below its limits is Burstable, and Burstable pods are evicted before Guaranteed ones. An estate that measured otherwise and accepts that trade sets `resources.policy: burstable`." $site.key (toString $requests.cpu) (toString $limits.cpu)) -}}
{{- end -}}
{{- if ne (toString $requests.memory) (toString $limits.memory) -}}
{{- fail (printf "observability-stack: %s has requests.memory %q and limits.memory %q. They must be equal, for the same reason the CPUs must." $site.key (toString $requests.memory) (toString $limits.memory)) -}}
{{- end -}}
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
{{- fail "observability-stack: `selfAlerts.cardinality.hourlyCurrentSeriesMetric` is set but `hourlyMaxSeriesMetric` is not. MetricStoreCardinalityNearLimit would compare a gauge against nothing; set both, or neither." -}}
{{- end -}}
{{- if and $sa.cardinality.hourlyMaxSeriesMetric (not $sa.cardinality.hourlyCurrentSeriesMetric) -}}
{{- fail "observability-stack: `selfAlerts.cardinality.hourlyMaxSeriesMetric` is set but `hourlyCurrentSeriesMetric` is not. MetricStoreCardinalityNearLimit would compare a gauge against nothing; set both, or neither." -}}
{{- end -}}
{{- if and $sa.cardinality.dailyCurrentSeriesMetric (not $sa.cardinality.dailyMaxSeriesMetric) -}}
{{- fail "observability-stack: `selfAlerts.cardinality.dailyCurrentSeriesMetric` is set but `dailyMaxSeriesMetric` is not. MetricStoreDailyCardinalityNearLimit would compare a gauge against nothing; set both, or neither." -}}
{{- end -}}
{{- if and $sa.cardinality.dailyMaxSeriesMetric (not $sa.cardinality.dailyCurrentSeriesMetric) -}}
{{- fail "observability-stack: `selfAlerts.cardinality.dailyMaxSeriesMetric` is set but `dailyCurrentSeriesMetric` is not. MetricStoreDailyCardinalityNearLimit would compare a gauge against nothing; set both, or neither." -}}
{{- end -}}
{{- $diskGuardAlertPrefix := dict "metrics" "MetricStore" "logs" "LogStore" "traces" "TraceStore" -}}
{{- range $store := list "metrics" "logs" "traces" -}}
{{- $g := index $sa.diskGuard $store -}}
{{- if and $g.freeSpaceMetric (not $g.freeSpaceLimitMetric) -}}
{{- fail (printf "observability-stack: `selfAlerts.diskGuard.%s.freeSpaceMetric` is set but `freeSpaceLimitMetric` is not. %sDiskNearGuard would compare a gauge against nothing; set both, or neither." $store (index $diskGuardAlertPrefix $store)) -}}
{{- end -}}
{{- if and $g.freeSpaceLimitMetric (not $g.freeSpaceMetric) -}}
{{- fail (printf "observability-stack: `selfAlerts.diskGuard.%s.freeSpaceLimitMetric` is set but `freeSpaceMetric` is not. %sDiskNearGuard would compare a gauge against nothing; set both, or neither." $store (index $diskGuardAlertPrefix $store)) -}}
{{- end -}}
{{- end -}}
{{- if and $sa.gateway.queueSizeMetric (not $sa.gateway.queueCapacityMetric) -}}
{{- fail "observability-stack: `selfAlerts.gateway.queueSizeMetric` is set but `queueCapacityMetric` is not. GatewayQueueFilling would compare a gauge against nothing; set both, or neither." -}}
{{- end -}}
{{- if and $sa.gateway.queueCapacityMetric (not $sa.gateway.queueSizeMetric) -}}
{{- fail "observability-stack: `selfAlerts.gateway.queueCapacityMetric` is set but `queueSizeMetric` is not. GatewayQueueFilling would compare a gauge against nothing; set both, or neither." -}}
{{- end -}}
{{- if and $sa.sourceAbsent.enabled (not $sa.enabled) -}}
{{- fail "observability-stack: `selfAlerts.sourceAbsent.enabled` is true but `selfAlerts.enabled` is not. SelfAlertSourceAbsent watches the metric names the self-alerts are configured with; with the self-alerts off there is nothing for it to watch." -}}
{{- end -}}
{{- /*
Every metric name here is optional, by design, since none could be
confirmed for certain against this chart's pins. But `selfAlerts.enabled`
with NOTHING configured renders a VMRule with an empty rule list — an
object that looks like coverage and is not, the exact shape this file
exists to refuse elsewhere. So at least one thing has to actually render:
one metric name, or one enabled backup whose *SnapshotOlderThanWindow can
watch it.
*/ -}}
{{- if $sa.enabled -}}
{{- $anyRule := or
    $sa.ingest.metric
    (and $sa.cardinality.hourlyCurrentSeriesMetric $sa.cardinality.hourlyMaxSeriesMetric)
    (and $sa.cardinality.dailyCurrentSeriesMetric $sa.cardinality.dailyMaxSeriesMetric)
    $sa.logStore.metric
    $sa.traceStore.metric
    (and $sa.gateway.queueSizeMetric $sa.gateway.queueCapacityMetric)
    $sa.gateway.exportFailedMetricPrefix
    $sa.gateway.enqueueFailedMetricPrefix
    (and $sa.diskGuard.metrics.freeSpaceMetric $sa.diskGuard.metrics.freeSpaceLimitMetric)
    (and $sa.diskGuard.logs.freeSpaceMetric $sa.diskGuard.logs.freeSpaceLimitMetric)
    (and $sa.diskGuard.traces.freeSpaceMetric $sa.diskGuard.traces.freeSpaceLimitMetric)
    (and .Values.backup.enabled .Values.backup.metrics.enabled)
    (and .Values.backup.enabled .Values.backup.logs.enabled)
    (and .Values.backup.enabled .Values.backup.traces.enabled)
    $sa.logStreamChurn.streamsCreatedMetric
    $sa.traceStreamChurn.streamsCreatedMetric
    $sa.writer.bufferMetric
    $sa.writer.droppedPacketsMetric
    $sa.proxyConcurrency.limitedRequestsMetric
    (and ((.Values.notifications | default dict).slack | default dict).workspaces $sa.slackDelivery.enabled)
-}}
{{- if not $anyRule -}}
{{- fail "observability-stack: `selfAlerts.enabled` is true but no rule would actually render — no metric name is set anywhere under `selfAlerts`, and no `backup.<store>.enabled` is true either. A VMRule with an empty rule list looks like coverage and is not. Confirm at least one metric name against your own component's /metrics and set it here, or leave `selfAlerts.enabled: false` until you have." -}}
{{- end -}}
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
{{- $observabilityStackEffective := include "observability-stack.effectiveEnabled" . | fromYaml -}}
{{- $vmks := index .Values "victoria-metrics-k8s-stack" -}}
{{- $metricsOn := and $vmks.enabled ($vmks.vmsingle).enabled -}}
{{- $logsOn := and (index .Values "victoria-logs-single").enabled (((index .Values "victoria-logs-single").server).enabled) -}}
{{- $tracesOn := and (index .Values "victoria-traces-single").enabled (((index .Values "victoria-traces-single").server).enabled) -}}
{{- $dedup := index ((($vmks.vmsingle).spec).extraArgs | default dict) "dedup.minScrapeInterval" -}}
{{- if and $metricsOn $dedup (ne (toString $dedup) (toString .Values.interval)) -}}
{{- fail (printf "observability-stack: `interval` is %q but victoria-metrics-k8s-stack.vmsingle.spec.extraArgs['dedup.minScrapeInterval'] is %q. They are one value: deduplication keeps one sample per window, so a window wider than the scrape interval silently drops good samples, and a narrower one deduplicates nothing. Set both to %q." (toString .Values.interval) (toString $dedup) (toString .Values.interval)) -}}
{{- end -}}
{{- $want := .Values.storeCredentials.secretName -}}
{{- /*
Required only when there is a store to authenticate at all: `mode:
operator-only` refuses every store back on before this runs, so an
operator-only install that leaves `storeCredentials.secretName` at its
own default has no store reading a Secret that does not exist either.
*/ -}}
{{- if and (not $want) (or $metricsOn $logsOn $tracesOn) -}}
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
{{- /*
The log and trace stores' own ServiceMonitor, MIRROR of
storeCredentials the same way their `env` is above: the store runs with
`-httpAuth.*` on, so a scrape with the wrong basic-auth Secret gets 401
forever, the same silent failure as a writer reading the wrong Secret.
*/ -}}
{{- $smSites := list -}}
{{- if $logsOn -}}{{- $smSites = append $smSites (dict "key" "victoria-logs-single.server.serviceMonitor.basicAuth" "value" (((index .Values "victoria-logs-single").server).serviceMonitor)) -}}{{- end -}}
{{- if $tracesOn -}}{{- $smSites = append $smSites (dict "key" "victoria-traces-single.server.serviceMonitor.basicAuth" "value" (((index .Values "victoria-traces-single").server).serviceMonitor)) -}}{{- end -}}
{{- range $site := $smSites -}}
{{- $sm := $site.value | default dict -}}
{{- if $sm.enabled -}}
{{- $auth := $sm.basicAuth | default dict -}}
{{- $u := (($auth.username).name) | default "" -}}
{{- $p := (($auth.password).name) | default "" -}}
{{- if or (ne $u $want) (ne $p $want) -}}
{{- fail (printf "observability-stack: %s names Secret(s) %q / %q, but `storeCredentials.secretName` is %q. The store answers 401 to a scrape whose basic auth is not the credential it was started with, and a ServiceMonitor whose target always 401s looks identical to one that is not there at all. Set both to the same name." $site.key $u $p $want) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- /*
The metrics store's self-scrape, MIRROR of `disableSelfServiceScrape`.

`templates/selfscrape.yaml` is this chart's REPLACEMENT for the
operator's own self-scrape, on the same terms
`charts/observability-emitters` replaces its agent's: the operator's
version is a `VMServiceScrape` regardless of what is written into it —
"Scrape objects are always the Prometheus Operator kinds" (docs/
safety.md) — and it has no `basicAuth`, so a scrape that reaches this
store 401s forever. Flipping `disableSelfServiceScrape` back to false
does not just resurrect a doctrine violation, it resurrects the exact
401 this release closes: the operator's object still carries no
credential, whatever this chart's own ServiceMonitor does beside it.
*/ -}}
{{- if and $metricsOn $observabilityStackEffective.metricsSelfScrape (ne ((($vmks.vmsingle).spec).disableSelfServiceScrape) true) -}}
{{- fail "observability-stack: `metricsSelfScrape.enabled` is true but `victoria-metrics-k8s-stack.vmsingle.spec.disableSelfServiceScrape` is not `true`. The operator then reconciles ITS OWN VMServiceScrape for this VMSingle alongside this chart's ServiceMonitor — a kind docs/safety.md rules out on its own, and one with no `basicAuth` either way: every scrape it drives still 401s against a store running `-httpAuth.*`. Leave `disableSelfServiceScrape: true`." -}}
{{- end -}}
{{- /*
The Prometheus-Operator converter, for the ServiceMonitor objects this
chart itself renders.

Measured on a live install: this chart shipped two releases believing
"the VictoriaMetrics operator converts the Prometheus kinds today"
(docs/safety.md) while its own
`victoria-metrics-k8s-stack.victoria-metrics-operator.operator.
disable_prometheus_converter` was `true` — a single switch for all six
of the operator's per-kind converters, with no per-owner or
per-namespace scope. Every `ServiceMonitor` this chart renders was
exactly as inert as one authored in the wrong kind outright: nothing
ever converted it to the native `VMServiceScrape` vmagent watches, so
nothing ever scraped it. See docs/safety.md, "The doctrine's own promise
was broken from this chart's first commit".

Refused whenever this chart renders at least one ServiceMonitor of its
own (`metricsSelfScrape`, Alertmanager's own scrape, or the
log/trace stores' own `serviceMonitor`) and either the blanket switch or an explicit env
override leaves the ServiceMonitor converter off.
*/ -}}
{{- $anyServiceMonitor := or $observabilityStackEffective.alertmanager (and $metricsOn $observabilityStackEffective.metricsSelfScrape) (and $logsOn (((index .Values "victoria-logs-single").server).serviceMonitor).enabled) (and $tracesOn (((index .Values "victoria-traces-single").server).serviceMonitor).enabled) -}}
{{- if $anyServiceMonitor -}}
{{- $vmOperator := index $vmks "victoria-metrics-operator" -}}
{{- $converterOff := ($vmOperator.operator).disable_prometheus_converter -}}
{{- $envOverrides := dict -}}
{{- range $e := ($vmOperator.env | default list) -}}
{{- $_ := set $envOverrides $e.name $e.value -}}
{{- end -}}
{{- if or $converterOff (eq (index $envOverrides "VM_ENABLEDPROMETHEUSCONVERTER_SERVICESCRAPE") "false") -}}
{{- fail "observability-stack: this chart renders a ServiceMonitor of its own (Alertmanager's own scrape, `metricsSelfScrape`, or the log/trace stores' `serviceMonitor`), but `victoria-metrics-k8s-stack.victoria-metrics-operator.operator.disable_prometheus_converter` is `true` or `VM_ENABLEDPROMETHEUSCONVERTER_SERVICESCRAPE` is explicitly \"false\" in its `env`. vmagent only watches the native VictoriaMetrics kinds, so nothing converts this object and nothing scrapes it — docs/safety.md, \"The doctrine's own promise was broken from this chart's first commit\"." -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
`victoria-metrics-k8s-stack.defaultRules.sources.alertmanager`/`.vmalert`
and the matching `defaultDashboards` entries, MIRROR of
`alertmanager.enabled`/`vmalert.enabled`/`grafana.enabled` (this
chart's OWN, top-level — not the vendored ones of the same name, which
stay `false` always because this chart runs its own Alertmanager/
vmalert/Grafana instead of the vendored copies).

values.yaml overrides the four sources tied to `alertmanager.enabled`/
`vmalert.enabled` to a literal `true` — correct by default, since those
two default `true` themselves ("mode: full"). `grafana.enabled` here
defaults `false`, so `defaultDashboards.dashboards.grafana-overview`
is left at upstream's own (always-`false`) default rather than guessed.

Either way, a caller who sets a DIFFERENT value than this chart's own
default — `alertmanager.enabled: false`, `vmalert.enabled: false`, or
`grafana.enabled: true` while `defaultDashboards.enabled` is also
`true` — must update the matching vendored source/dashboard entry too:
Helm cannot compute a subchart's value from this chart's own
(docs/reference.md, "Why some values appear twice"), so a caller who
changes only one half gets exactly the silent gap this refusal exists
to close (found live, 2026-09-29: `alertmanager.rules`, `vmalert.rules`
and three dashboards vanished on a cutover with nothing saying so).
*/}}
{{- define "observability-stack.validate.vendoredSyncSources" -}}
{{- $observabilityStackEffective := include "observability-stack.effectiveEnabled" . | fromYaml -}}
{{- $vmks := index .Values "victoria-metrics-k8s-stack" -}}
{{- /*
The vendored sync Job renders NOTHING — no ConfigMap, no VMRule, no
dashboard — while its own `syncJob.enabled` is off (vmks's default is
`true`; `mode: operator-only` sets it `false` itself, since there is no
vmalert/Grafana for anything it would fetch to reach). Every check below
is dead while that is the case, so none of them run: a values file that
turns `alertmanager.enabled`/`vmalert.enabled` off ALONGSIDE `syncJob.
enabled: false` has nothing left for these sources to disagree with.
*/ -}}
{{- $syncJobEnabled := ($vmks.syncJob).enabled -}}
{{- $syncJobOn := and $vmks.enabled (or (kindIs "invalid" $syncJobEnabled) $syncJobEnabled) -}}
{{- $ruleSources := ($vmks.defaultRules).sources | default dict -}}
{{- $dashboards := ($vmks.defaultDashboards).dashboards | default dict -}}
{{- $dashSources := ($vmks.defaultDashboards).sources | default dict -}}
{{- $dashboardsSyncing := and $syncJobOn ($vmks.defaultDashboards).enabled -}}
{{- /*
`defaultRules.create: false` (set above) does NOT by itself turn the
rule sync off — vmks's own gate is `defaultRules.enabled OR .create`,
and `enabled` stays at vmks's default `true` unless a caller (like
tests/cases/observability-stack/vendored-rules-off) turns it off too,
the documented way to opt out of the vendored rule set entirely. While
BOTH are false, `$ruleSources` never reaches the ConfigMap either, so
these four checks have nothing to be wrong about.
*/ -}}
{{- $rulesEnabled := ($vmks.defaultRules).enabled -}}
{{- $rulesSyncing := and $syncJobOn (or (kindIs "invalid" $rulesEnabled) $rulesEnabled ($vmks.defaultRules).create) -}}
{{- $checks := list
  (dict "flag" "alertmanager.enabled" "effective" $observabilityStackEffective.alertmanager "active" $rulesSyncing "path" "victoria-metrics-k8s-stack.defaultRules.sources.alertmanager.enabled" "got" ($ruleSources.alertmanager).enabled)
  (dict "flag" "vmalert.enabled" "effective" $observabilityStackEffective.vmalert "active" $rulesSyncing "path" "victoria-metrics-k8s-stack.defaultRules.sources.vmalert.enabled" "got" ($ruleSources.vmalert).enabled)
  (dict "flag" "alertmanager.enabled" "effective" $observabilityStackEffective.alertmanager "active" $dashboardsSyncing "path" "victoria-metrics-k8s-stack.defaultDashboards.dashboards.alertmanager-overview.enabled" "got" (index $dashboards "alertmanager-overview").enabled)
  (dict "flag" "vmalert.enabled" "effective" $observabilityStackEffective.vmalert "active" $dashboardsSyncing "path" "victoria-metrics-k8s-stack.defaultDashboards.sources.victoriametrics-vmalert.enabled" "got" (index $dashSources "victoriametrics-vmalert").enabled)
  (dict "flag" "grafana.enabled" "effective" (.Values.grafana).enabled "active" $dashboardsSyncing "path" "victoria-metrics-k8s-stack.defaultDashboards.dashboards.grafana-overview.enabled" "got" (index $dashboards "grafana-overview").enabled)
-}}
{{- range $c := $checks -}}
{{- if $c.active -}}
{{- /*
`got` is whatever this chart (or a caller overriding it further) put at
that path — a real boolean for the four this chart's own values.yaml
sets, but upstream's OWN default for `grafana-overview` is still its
original template STRING (this chart does not override it — see
values.yaml's own comment), which `ne` cannot compare against a
boolean at all. Every one of upstream's own such strings references a
vendored flag this chart hard-codes `false`, so a string here always
means "off", the same as `_validate.tpl`'s sibling checks resolve it.
*/ -}}
{{- $got := $c.got -}}
{{- if kindIs "string" $got -}}{{- $got = false -}}{{- end -}}
{{- if and $c.effective (ne $got true) -}}
{{- fail (printf "observability-stack: `%s` is true, but `%s` is %s, not `true`. Left this way, the vendored sync job renders one fewer VMRule source or dashboard than this install's own Alertmanager/vmalert/Grafana actually has — silently, since a source or dashboard it did not fetch is simply absent from the ConfigMap, not an error. Set `%s: true`." $c.flag $c.path (toYaml $c.got) $c.path) -}}
{{- end -}}
{{- if and (not $c.effective) (ne $got false) -}}
{{- fail (printf "observability-stack: `%s` is false, but `%s` is %s, not `false`. Left this way, the vendored sync job renders `alertmanager.rules`/`vmalert.rules`/a dashboard for a component this install does not run — permanently unhealthy or empty, not an error either. Set `%s: false`." $c.flag $c.path (toYaml $c.got) $c.path) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
`backup.seLinuxLevel`, MIRROR of each enabled store's OWN
`securityContext.seLinuxOptions.level` — see values.yaml's own doc
comment on `backup.seLinuxLevel` for the incident this closes.

Unlike every other mirror above, this chart cannot RENDER the upstream
side even when it agrees to: the CronJobs it renders itself get the
level straight from `templates/backup.yaml` (same chart, no computation
needed to reach it), but the store's own securityContext is a field on
a vendored dependency's object — a VMSingle CR for metrics, a
StatefulSet's pod template for logs/traces — and Helm evaluates a
subchart's own values before any template in THIS chart runs. There is
no hook this chart's `_validate.tpl` or `templates/backup.yaml` can use
to compute a value into `victoria-metrics-k8s-stack.vmsingle.spec...`
after the fact; the upstream side has to be written by hand, the same
as `interval` and `storeCredentials` above, and checked here.
*/}}
{{- define "observability-stack.validate.seLinux" -}}
{{- $level := .Values.backup.seLinuxLevel | default "" -}}
{{- if $level -}}
{{- if not .Values.backup.enabled -}}
{{- fail "observability-stack: `backup.seLinuxLevel` is set but `backup.enabled` is false. It only shapes the backup CronJobs' pod `securityContext` — with backups off there is nothing for it to do." -}}
{{- end -}}
{{- $shape := "^s[0-9]+(-s[0-9]+)?:c[0-9]+(\\.c[0-9]+)?(,c[0-9]+(\\.c[0-9]+)?)*$" -}}
{{- if not (regexMatch $shape $level) -}}
{{- fail (printf "observability-stack: `backup.seLinuxLevel` is %q, which is not the shape of an SELinux MCS level. It has to look like \"s0:c123,c456\" — a sensitivity (`s0`, optionally `s0-sN`) and one or more comma-separated categories (`cN`, or a range `cN.cM`) — the exact string an SELinux-enforcing node (Bottlerocket's default) assigns a pod as its `securityContext.seLinuxOptions.level`. Read it off the running store pod (or the node's own audit log); an invented value that does not match what the node actually assigned buys nothing." $level) -}}
{{- end -}}
{{- $vmks := index .Values "victoria-metrics-k8s-stack" -}}
{{- $logsCfg := index .Values "victoria-logs-single" -}}
{{- $tracesCfg := index .Values "victoria-traces-single" -}}
{{- $sites := list -}}
{{- if and .Values.backup.metrics.enabled $vmks.enabled ($vmks.vmsingle).enabled -}}
{{- $sites = append $sites (dict "key" "victoria-metrics-k8s-stack.vmsingle.spec.securityContext.seLinuxOptions.level" "got" ((((($vmks.vmsingle).spec).securityContext).seLinuxOptions).level | default "")) -}}
{{- end -}}
{{- if and .Values.backup.logs.enabled $logsCfg.enabled ($logsCfg.server).enabled -}}
{{- $sites = append $sites (dict "key" "victoria-logs-single.server.podSecurityContext.seLinuxOptions.level" "got" (((($logsCfg.server).podSecurityContext).seLinuxOptions).level | default "")) -}}
{{- end -}}
{{- if and .Values.backup.traces.enabled $tracesCfg.enabled ($tracesCfg.server).enabled -}}
{{- $sites = append $sites (dict "key" "victoria-traces-single.server.podSecurityContext.seLinuxOptions.level" "got" (((($tracesCfg.server).podSecurityContext).seLinuxOptions).level | default "")) -}}
{{- end -}}
{{- range $site := $sites -}}
{{- if not $site.got -}}
{{- fail (printf "observability-stack: `backup.seLinuxLevel` is %q but %s is empty. Its backup CronJob and this store mount the SAME ReadWriteOnce volume on the SAME node; on an SELinux-enforcing node each pod gets its own random MCS categories, and a volume labelled for the store's categories is unreadable to a backup pod with different ones. This chart cannot set the store's own field for you — see `backup.seLinuxLevel`'s doc comment in values.yaml for why — so set %s to the exact same string, %q." $level $site.key $site.key $level) -}}
{{- else if ne $site.got $level -}}
{{- fail (printf "observability-stack: `backup.seLinuxLevel` is %q but %s is already %q — a DIFFERENT level. The backup CronJob needs the SAME MCS categories as the store's own pod to read its volume; two different levels is the exact \"permission denied\" this value exists to prevent. Make them match, or drop `backup.seLinuxLevel` if %s was set for an unrelated reason and must stay as it is." $level $site.key $site.got $site.key) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Backup prefixes: no two in use may be equal, and none may sit inside
another.

Both mechanisms DELETE at their destination: vmbackup removes whatever
under `-dst` is not part of the backup it is writing, and `rclone sync`
removes whatever under its destination the source does not have. So a
logs prefix of `a` beside a metrics prefix of `a/metrics` is a log backup
that deletes the metrics backup on every run — and both jobs exit zero,
every time. Equal is the same failure in its simplest form. Compared on
whole path segments: `metrics` and `metrics-full` do not nest.
*/}}
{{- define "observability-stack.validate.backupPrefixes" -}}
{{- $b := .Values.backup -}}
{{- if $b.enabled -}}
{{- $inUse := list -}}
{{- if $b.metrics.enabled -}}
{{- $inUse = append $inUse (dict "key" "backup.metrics.prefix" "value" (toString $b.metrics.prefix)) -}}
{{- $inUse = append $inUse (dict "key" "backup.metrics.fullPrefix" "value" (toString $b.metrics.fullPrefix)) -}}
{{- end -}}
{{- if $b.logs.enabled -}}
{{- $inUse = append $inUse (dict "key" "backup.logs.prefix" "value" (toString $b.logs.prefix)) -}}
{{- end -}}
{{- if $b.traces.enabled -}}
{{- $inUse = append $inUse (dict "key" "backup.traces.prefix" "value" (toString $b.traces.prefix)) -}}
{{- end -}}
{{- range $i, $a := $inUse -}}
{{- range $j, $o := $inUse -}}
{{- if lt $i $j -}}
{{- if or (eq $a.value $o.value) (hasPrefix (printf "%s/" $a.value) $o.value) (hasPrefix (printf "%s/" $o.value) $a.value) -}}
{{- fail (printf "observability-stack: %s is %q and %s is %q, and one is the other or sits inside it. vmbackup and `rclone sync` both DELETE whatever at their destination the source does not have, so one of these backups would erase the other on every run and still exit zero. Give each store a prefix of its own that is not a parent of another's." $a.key $a.value $o.key $o.value) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
`networkPolicy.clientsFrom`: the same `ipBlock`-alone refusal as
`scrapeFrom`, for the same reason. An EMPTY `from`, which in a
NetworkPolicy admits every source, is the schema's to refuse
(`minItems: 1`) before any template runs.
*/}}
{{- define "observability-stack.validate.clientsFrom" -}}
{{- range $i, $c := (.Values.networkPolicy.clientsFrom | default list) -}}
{{- range $j, $peer := ($c.from | default list) -}}
{{- if and (not $peer.podSelector) (not $peer.namespaceSelector) (hasKey $peer "ipBlock") -}}
{{- fail (printf "observability-stack: networkPolicy.clientsFrom[%d].from[%d] admits a client by `ipBlock` alone. A pod IP is reassigned on every reschedule, eviction and rollout, so this rule works today and stops working silently the first time the client pod moves. Name the client by a `podSelector`, with a `namespaceSelector` when it runs outside this release's namespace." $i $j) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
A Watchdog from somewhere, always.

Two sources, and the chart picks whichever one is not already covering
it (see `observability-stack.vendoredWatchdogPresent` in _helpers.tpl):
`victoria-metrics-k8s-stack`'s own vendored default rule set — ON here
by default, and the reason it stays on, because it is also where
several rules with no `charts/platform-alerts` equivalent come from —
or, when that is turned off, this chart's own `templates/watchdog.yaml`.
Turning BOTH off at once is the one combination that leaves the status
box's deadman (docs/statusbox.md, "internal → status, pulled") with
nothing to read: not a rule this chart carries, and not one the
vendored set carries either.
*/}}
{{- define "observability-stack.validate.watchdogSource" -}}
{{- $observabilityStackEffective := include "observability-stack.effectiveEnabled" . | fromYaml -}}
{{- $vmks := index .Values "victoria-metrics-k8s-stack" -}}
{{- if and $vmks.enabled $observabilityStackEffective.vmalert -}}
{{- if and (not (include "observability-stack.vendoredWatchdogPresent" .)) (not .Values.vmalert.watchdog.enabled) -}}
{{- fail "observability-stack: victoria-metrics-k8s-stack's vendored default rule set is off (`defaultRules.enabled: false`, or its `general.rules` group specifically disabled) AND `vmalert.watchdog.enabled` is false. Between them, this install renders no Watchdog alert at all — not the vendored one, not this chart's own — and the status box's deadman (docs/statusbox.md) depends on one existing to read. Turn one of the two back on." -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
A store's scraper, named by address rather than by identity.

`networkPolicy.scrapeFrom` admits whatever it is given at face value —
that is the same choice `proxyFrom` and `writersFrom` already make, and
this file does not second-guess it by trying to confirm a selector
matches a real pod, which it cannot know at render time.

What it CAN see is a peer that identifies a scraper by `ipBlock` alone: a
pod IP, reassigned on every reschedule, eviction and rollout. Such a rule
renders, installs and works — right up to the first time the scraper
pod moves, at which point the store stops being scraped exactly the way
it already was before this value existed, and just as silently. This is
the same principle `tenancy` is built on for the read side: "a viewer's
reach follows from their identity, not from which address they happened
to query" (docs/doctrine.md) — a scraper's admission should follow from
what it IS, not from an address it holds today.
*/}}
{{- define "observability-stack.validate.scrapeFrom" -}}
{{- range $i, $peer := (.Values.networkPolicy.scrapeFrom | default list) -}}
{{- if and (not $peer.podSelector) (not $peer.namespaceSelector) (hasKey $peer "ipBlock") -}}
{{- fail (printf "observability-stack: networkPolicy.scrapeFrom[%d] admits a scraper by `ipBlock` alone. A pod IP is reassigned on every reschedule, eviction and rollout, so this rule works today and stops working silently the first time the scraper pod moves — the same failure this value exists to fix, reintroduced by the value meant to fix it. Name the scraper by a `podSelector` (and a `namespaceSelector` if it runs outside this release's namespace), the way this chart's own default does." $i) -}}
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
{{- $observabilityStackEffective := include "observability-stack.effectiveEnabled" . | fromYaml -}}
{{- if $observabilityStackEffective.vmalert -}}
{{- $mode := ((.Values.notifications | default dict).mode) | default "route" -}}
{{- if and (eq $mode "route") (not $observabilityStackEffective.alertmanager) (not .Values.alertmanager.notifierUrl) -}}
{{- fail "observability-stack: vmalert is enabled, Alertmanager is not, and `alertmanager.notifierUrl` is empty. vmalert would evaluate every rule and send the result nowhere — which looks exactly like an estate with no problems, for as long as nobody checks. Enable Alertmanager, name the one the estate already runs, or set `notifications.mode: evaluate-only` for the explicit \"evaluate every rule, notify nobody yet\" shape." -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Slack workspaces: one Slack app (one bot token) per workspace.

Before this, `notifications.slack.webhookSecret` was ONE incoming webhook
shared by every channel, and `channel` was chosen per route. A webhook
ignores the `channel` a message asks for and posts to the one channel it
was created for, so a second channel looked routed and was not. The
token of a Slack app honours `channel`, so the webhook is gone and this
block replaces it. Everything below refuses a shape that looks wired up
and delivers nowhere.

Runs before `evaluateOnly` so a leftover `webhookSecret` is refused with
its migration even when the rest of the file is otherwise (validly or
not) evaluate-only.
*/ -}}
{{- define "observability-stack.validate.slackWorkspaces" -}}
{{- $n := .Values.notifications | default dict -}}
{{- $slack := $n.slack | default dict -}}
{{- if hasKey $slack "webhookSecret" -}}
{{- fail "observability-stack: `notifications.slack.webhookSecret` was removed. One incoming webhook ignores the `channel` a message asks for and always posts to the one channel it was created for, so routing to several channels through it could not work. Replace it with one entry per Slack workspace under `notifications.slack.workspaces`: `[{name: <short-name>, appTokenSecret: {name: <Secret>, key: <key>}}]`, holding that workspace's Slack app bot token (xoxb-...). Then each severity, route and `catchAll` destination may name a `workspace` (optional when exactly one is declared). See docs/notifications.md, \"Slack\"." -}}
{{- end -}}
{{- $workspaces := $slack.workspaces | default list -}}
{{- $names := dict -}}
{{- range $i, $w := $workspaces -}}
{{- if not $w.name -}}
{{- fail (printf "observability-stack: notifications.slack.workspaces[%d] has an empty `name`. The name is how a severity, route or catch-all picks this workspace, and it names the mounted Secret volume." $i) -}}
{{- end -}}
{{- if hasKey $names $w.name -}}
{{- fail (printf "observability-stack: notifications.slack.workspaces has two entries named %q. A `workspace` that could mean either is a destination nobody can read, and the two would mount one volume name twice." (toString $w.name)) -}}
{{- end -}}
{{- $_ := set $names $w.name true -}}
{{- if not (($w.appTokenSecret).name) -}}
{{- fail (printf "observability-stack: notifications.slack.workspaces[%d] (%s) has an empty `appTokenSecret.name`. A workspace whose bot token Secret is unnamed cannot send: the route, the receiver and the schema would all agree it exists, and it would deliver nothing." $i (toString $w.name)) -}}
{{- end -}}
{{- if not (($w.appTokenSecret).key) -}}
{{- fail (printf "observability-stack: notifications.slack.workspaces[%d] (%s) has an empty `appTokenSecret.key`. The key is the file under the mounted Secret that Alertmanager reads the bot token from; without it there is no file to read." $i (toString $w.name)) -}}
{{- end -}}
{{- end -}}
{{- with $slack.failureReceiver -}}
{{- if not $workspaces -}}
{{- fail "observability-stack: `notifications.slack.failureReceiver` is set but `notifications.slack.workspaces` is empty. It names where the \"Slack is not delivering\" alert goes, and with no Slack workspace there is no such alert to route." -}}
{{- end -}}
{{- if eq . "slack" -}}
{{- fail "observability-stack: `notifications.slack.failureReceiver` is \"slack\". The alert says Slack is failing to deliver; routed to Slack it would fail to deliver itself. Name a webhook from `notifications.webhook`, or `telegram`." -}}
{{- end -}}
{{- $isTelegram := and (eq . "telegram") ((($n.telegram | default dict).botTokenSecret).name) -}}
{{- $isWebhook := false -}}
{{- range $w := ($n.webhook | default list) -}}{{- if eq $w.name $slack.failureReceiver -}}{{- $isWebhook = true -}}{{- end -}}{{- end -}}
{{- if not (or $isTelegram $isWebhook) -}}
{{- fail (printf "observability-stack: notifications.slack.failureReceiver is %q, which is neither the name of an entry in notifications.webhook nor \"telegram\" (with notifications.telegram configured). An alert routed to a receiver that is not configured looks routed and reaches nobody." (toString .)) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
`notifications.mode: evaluate-only` — the explicit, named opt-out for a
consumer with no Slack or webhook credential YET, docs/notifications.md,
"Evaluate, notify nobody yet". `route` (the default) changes nothing
here; every check below applies only once the mode is evaluate-only, and
each one refuses a combination that would otherwise render a receiver,
or an Alertmanager, that the mode makes pointless: vmalert never notifies
anybody while it is set, so anything configured to be notified is
configured to be unreachable.

This runs BEFORE `validate.notifications`, so a fixture testing one of
these refusals is never preempted by a later check that also happens to
trip over an unconfigured severity or receiver.
*/ -}}
{{- define "observability-stack.validate.evaluateOnly" -}}
{{- $observabilityStackEffective := include "observability-stack.effectiveEnabled" . | fromYaml -}}
{{- $n := .Values.notifications | default dict -}}
{{- if eq ($n.mode | default "route") "evaluate-only" -}}
{{- if $observabilityStackEffective.alertmanager -}}
{{- fail "observability-stack: `notifications.mode` is `evaluate-only` and `alertmanager.enabled` is true (or left at its default). Evaluate-only means vmalert evaluates every rule and sends the result to nobody — on purpose, visible in vmalert's own UI and API, not silently — so there is nothing for Alertmanager to route and this chart does not render it in this mode. Set `alertmanager.enabled: false`, or drop `notifications.mode` back to `route` and configure a receiver." -}}
{{- end -}}
{{- if .Values.alertmanager.notifierUrl -}}
{{- fail "observability-stack: `notifications.mode` is `evaluate-only` and `alertmanager.notifierUrl` is set. Evaluate-only renders vmalert's `-notifier.blackhole`, which vmalert itself refuses to combine with any notifier URL: `-notifier.url`, `-notifier.config` and `-notifier.blackhole` are mutually exclusive. Unset `alertmanager.notifierUrl`, or drop `notifications.mode` back to `route` and point vmalert at the Alertmanager it names." -}}
{{- end -}}
{{- $slack := $n.slack | default dict -}}
{{- $receiverConfigured := or $slack.workspaces (($n.telegram | default dict).botTokenSecret).name (gt (len ($n.webhook | default list)) 0) (gt (len ($n.severities | default dict)) 0) (gt (len ($n.routes | default list)) 0) (gt (len ($n.also | default list)) 0) $n.catchAll (gt (len ($n.drop | default list)) 0) -}}
{{- if $receiverConfigured -}}
{{- fail "observability-stack: `notifications.mode` is `evaluate-only` and `notifications` also configures a receiver, a severity, a route or an `also` bridge (or a `catchAll` or `drop`). Evaluate-only means nobody is notified yet: a receiver configured beside it looks wired up and is never reached, because vmalert never sends the notification it would carry. Remove the receiver configuration, or drop `notifications.mode` back to `route`." -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
Notifications: the one router, refused into existence rather than left a
free-form passthrough. See docs/notifications.md and docs/safety.md.
*/}}
{{- define "observability-stack.validate.notifications" -}}
{{- $observabilityStackEffective := include "observability-stack.effectiveEnabled" . | fromYaml -}}
{{- $n := .Values.notifications | default dict -}}
{{- $mode := ($n.mode) | default "route" -}}
{{- $slack := $n.slack | default dict -}}
{{- $telegram := $n.telegram | default dict -}}
{{- $telegramConfigured := ($telegram.botTokenSecret).name -}}
{{- $webhooks := $n.webhook | default list -}}
{{- $severities := $n.severities | default dict -}}
{{- $routes := $n.routes | default list -}}
{{- $also := $n.also | default list -}}
{{- $webhookNames := dict -}}
{{- $slackWorkspaceNames := dict -}}
{{- range $w := ($slack.workspaces | default list) -}}{{- $_ := set $slackWorkspaceNames $w.name true -}}{{- end -}}
{{- range $w := $webhooks -}}{{- $_ := set $webhookNames $w.name true -}}{{- end -}}
{{- $configured := or $slack.workspaces $telegramConfigured (gt (len $webhooks) 0) -}}
{{- if and $observabilityStackEffective.alertmanager (not $configured) (ne $mode "evaluate-only") -}}
{{- fail "observability-stack: `alertmanager.enabled` is true and `notifications` configures no receiver kind — no `notifications.slack.workspaces`, no `notifications.telegram.botTokenSecret` and no `notifications.webhook` entries. Alertmanager then routes to the `blackhole` shape this chart exists to retire: vmalert evaluates every rule and the result reaches nobody, and nothing about the install looks unhealthy. Configure at least one receiver kind under `notifications`, set `alertmanager.enabled: false` and point `alertmanager.notifierUrl` at one the estate already runs, or set `notifications.mode: evaluate-only` for the explicit \"evaluate every rule, notify nobody yet\" shape if there is no channel yet." -}}
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
{{- /*
With `catchAll` set, a tier `severities` leaves out lands there instead,
so it is no longer the blackhole this refuses.
*/ -}}
{{- if and $configured (not $n.catchAll) -}}
{{- range $tier := list "critical" "warning" -}}
{{- if not (hasKey $severities $tier) -}}
{{- fail (printf "observability-stack: `notifications` configures a receiver but `notifications.severities.%s` is not set. Every route this chart renders falls back to a no-op receiver when none of `severities`, `routes` or `also` match, so a %s alert with no default reaches nobody and looks routed." $tier $tier) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- /*
`telegram` is a keyword only once `notifications.telegram` is configured.
Before 0.10.0 it was an ordinary webhook name, and an install that
bridges to Telegram through a webhook it NAMED `telegram` must render
exactly as it did; the two together are ambiguous and refused.
*/ -}}
{{- if and $telegramConfigured (hasKey $webhookNames "telegram") -}}
{{- fail "observability-stack: `notifications.telegram` is configured and `notifications.webhook` also has an entry named \"telegram\". A severity with `receiver: telegram` could then mean either one, and a route whose meaning depends on which the chart picked is a route nobody can read. Rename the webhook entry." -}}
{{- end -}}
{{- /*
A severity naming a receiver that does not exist. `slack` and `telegram`
are the literal keywords; anything else must be a name from
`notifications.webhook`.
*/ -}}
{{- /*
Every severity tier, and `catchAll`, which has the same shape and the
same ways to name a receiver that is not there.
*/ -}}
{{- $targets := list -}}
{{- range $tier, $cfg := $severities -}}
{{- $targets = append $targets (dict "where" (printf "notifications.severities.%s" $tier) "cfg" $cfg) -}}
{{- end -}}
{{- with $n.catchAll -}}
{{- $targets = append $targets (dict "where" "notifications.catchAll" "cfg" .) -}}
{{- end -}}
{{- range $t := $targets -}}
{{- $cfg := $t.cfg -}}
{{- $isTelegram := and (eq $cfg.receiver "telegram") $telegramConfigured -}}
{{- if and $cfg.receiver (ne $cfg.receiver "slack") (not $isTelegram) (not (hasKey $webhookNames $cfg.receiver)) -}}
{{- fail (printf "observability-stack: %s.receiver is %q, which is neither \"slack\", \"telegram\" (with notifications.telegram configured) nor the name of an entry in notifications.webhook. A route to a receiver that is not configured looks like a route and reaches nobody." $t.where (toString $cfg.receiver)) -}}
{{- end -}}
{{- if and (not $isTelegram) (or (hasKey $cfg "chatId") (hasKey $cfg "messageThreadId")) -}}
{{- fail (printf "observability-stack: %s sets `chatId` or `messageThreadId` but its receiver is %q, not a configured `telegram`. Those keys pick a Telegram chat; on any other receiver they would be read by nothing." $t.where (toString $cfg.receiver)) -}}
{{- end -}}
{{- if and $isTelegram $cfg.channel -}}
{{- fail (printf "observability-stack: %s.receiver is \"telegram\" and it also sets `channel`. `channel` is a Slack channel; a Telegram tier lands in `notifications.telegram.chatId`, or in this tier's own `chatId`/`messageThreadId`. Remove `channel`." $t.where) -}}
{{- end -}}
{{- if and $cfg.workspace (ne $cfg.receiver "slack") -}}
{{- fail (printf "observability-stack: %s sets `workspace` but its receiver is %q, not `slack`. A workspace picks the Slack app whose bot token posts; on any other receiver it would be read by nothing." $t.where (toString $cfg.receiver)) -}}
{{- end -}}
{{- if and $cfg.mention (ne $cfg.receiver "slack") -}}
{{- fail (printf "observability-stack: %s sets `mention` but its receiver is %q, not `slack`. A mention is Slack's `<!here>`/`<!channel>` in the message text; on any other receiver it would be read by nothing." $t.where (toString $cfg.receiver)) -}}
{{- end -}}
{{- if eq $cfg.receiver "slack" -}}
{{- include "observability-stack.validate.slackDestination" (list $t.where $cfg.channel $cfg.workspace $slackWorkspaceNames) -}}
{{- end -}}
{{- end -}}
{{- /*
A route matching outside the vocabulary the collectors actually stamp.
Anything else matches nothing a rule carries and pages nobody while
looking exactly like a route that works.
*/ -}}
{{- range $i, $r := $routes -}}
{{- range $tier := list "critical" "warning" -}}
{{- $tcfg := index $severities $tier | default dict -}}
{{- if and (index $r $tier) (eq ($tcfg.receiver | default "") "telegram") $telegramConfigured -}}
{{- fail (printf "observability-stack: notifications.routes[%d].%s overrides a channel, but notifications.severities.%s.receiver is \"telegram\". A route's per-tier value is a Slack channel name; a Telegram tier has no channel to override, so it would be read by nothing. Remove it, or route that tier to Slack." $i $tier $tier) -}}
{{- end -}}
{{- end -}}
{{- range $tier := list "critical" "warning" -}}
{{- $tcfg := index $severities $tier | default dict -}}
{{- $override := index $r $tier -}}
{{- if and $override (eq ($tcfg.receiver | default "") "slack") -}}
{{- $where := printf "notifications.routes[%d].%s" $i $tier -}}
{{- if kindIs "map" $override -}}
{{- include "observability-stack.validate.slackDestination" (list $where ($override.channel | default $tcfg.channel) ($override.workspace | default $tcfg.workspace) $slackWorkspaceNames) -}}
{{- else -}}
{{- include "observability-stack.validate.slackDestination" (list $where $override $tcfg.workspace $slackWorkspaceNames) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- range $k, $_ := ($r.match | default dict) -}}
{{- if not (has $k (concat (list "k8s_cluster_name" "k8s_namespace_name") (without (list (toString ($n.ownerLabel | default ""))) ""))) -}}
{{- fail (printf "observability-stack: notifications.routes[%d].match has key %q. The collectors this chart's rules run against stamp exactly two dimensions on every alert — k8s_cluster_name and k8s_namespace_name — so a route on anything else (tenant, env, team, …) matches nothing any rule actually carries. A route on the owning company needs `notifications.ownerLabel` set to the label the emitters stamp (`tenancy.owners` in observability-emitters)." $i (toString $k)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- /*
`also` names a webhook that is not configured. This bridge exists to
reach a webhook beside the normal route; a name that resolves to nothing
is a route that reaches nobody, same as the severities check above.
*/ -}}
{{- range $i, $a := $also -}}
{{- $where := printf "notifications.also[%d]" $i -}}
{{- if eq (toString $a.receiver) "slack" -}}
{{- if hasKey $webhookNames "slack" -}}
{{- fail (printf "observability-stack: %s.receiver is \"slack\" and notifications.webhook also has an entry named \"slack\". The entry could mean either, and a route whose meaning depends on which the chart picked is a route nobody can read. Rename the webhook entry." $where) -}}
{{- end -}}
{{- include "observability-stack.validate.slackDestination" (list $where $a.channel $a.workspace $slackWorkspaceNames) -}}
{{- else -}}
{{- if and $telegramConfigured (eq (toString $a.receiver) "telegram") -}}
{{- fail (printf "observability-stack: %s.receiver is \"telegram\". `also` delivers to a webhook or to Slack, not to Telegram; a Telegram destination is a severity tier's, a catchAll's, or the Slack failure receiver." $where) -}}
{{- end -}}
{{- if not (hasKey $webhookNames $a.receiver) -}}
{{- fail (printf "observability-stack: notifications.also[%d].receiver is %q, which is not the name of any notifications.webhook entry (and is not \"slack\")." $i (toString $a.receiver)) -}}
{{- end -}}
{{- range $k := list "channel" "workspace" "mention" -}}
{{- if index $a $k -}}
{{- fail (printf "observability-stack: %s sets `%s` but its receiver is %q, not `slack`. It picks a Slack destination; on a webhook it would be read by nothing." $where $k (toString $a.receiver)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- /*
`notifications.alertmanagerUrl` is embedded in an Alertmanager template,
so it must be an absolute http(s) URL with no trailing slash and none of
the characters that would end the template string or open an action.
*/ -}}
{{- with $n.alertmanagerUrl -}}
{{- if or (not (regexMatch "^https?://[^\\s/\"'`{}\\\\<>]" (toString .))) (regexMatch "[\\s\"'`{}\\\\<>]" (toString .)) (hasSuffix "/" (toString .)) -}}
{{- fail (printf "observability-stack: notifications.alertmanagerUrl is %q. It must be an absolute http:// or https:// URL with a host, without a trailing slash and without whitespace, quotes, braces or backslashes: the chart appends `/#/silences/new?...` to it and embeds it in an Alertmanager message template." (toString .)) -}}
{{- end -}}
{{- end -}}
{{- /*
The deadman's own repeat interval against the far end's timeout, when the
estate has stated one: the heartbeat has to land comfortably inside it, or
a single delayed delivery reads as the estate being down.
*/ -}}
{{- $watchdog := .Values.alertmanager.watchdog -}}
{{- /*
`alertmanager.cluster.reconnectTimeout` becomes `--cluster.reconnect-timeout`,
a Go duration; anything else makes Alertmanager exit at start.
*/ -}}
{{- $reconnect := toString (($.Values.alertmanager.cluster | default dict).reconnectTimeout | default "") -}}
{{- if not (regexMatch "^([0-9]+(\\.[0-9]+)?(ns|us|µs|ms|s|m|h))+$" $reconnect) -}}
{{- fail (printf "observability-stack: alertmanager.cluster.reconnectTimeout is %q. It must be a Go duration such as 5m, 90s or 1h30m: it is passed as Alertmanager's --cluster.reconnect-timeout, which refuses anything else and would crash-loop every replica." $reconnect) -}}
{{- end -}}
{{- if and $watchdog.tokenKey (not $watchdog.secretName) -}}
{{- fail "observability-stack: alertmanager.watchdog.tokenKey is set but alertmanager.watchdog.secretName is empty. The token qualifies the deadman receiver, which only exists with a Secret to read its URL from; set secretName, or remove tokenKey." -}}
{{- end -}}
{{- if and $watchdog.secretName $watchdog.timeout -}}
{{- $repeatS := include "observability-stack.durationSeconds" $watchdog.repeatInterval | int64 -}}
{{- $timeoutS := include "observability-stack.durationSeconds" $watchdog.timeout | int64 -}}
{{- if ge $repeatS $timeoutS -}}
{{- fail (printf "observability-stack: alertmanager.watchdog.repeatInterval is %q and alertmanager.watchdog.timeout is %q. The heartbeat must land comfortably INSIDE the far end's own timeout, or a single delayed delivery reads as the estate being down when it is not. repeatInterval must be strictly less than timeout." (toString $watchdog.repeatInterval) (toString $watchdog.timeout)) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
One Slack destination — a severity tier, the catch-all, or a route's
per-tier override — as `(where channel workspace workspaceNames)`.
*/ -}}
{{- define "observability-stack.validate.slackDestination" -}}
{{- $where := index . 0 -}}
{{- $channel := index . 1 -}}
{{- $workspace := index . 2 -}}
{{- $names := index . 3 -}}
{{- if not $names -}}
{{- fail (printf "observability-stack: %s sends to Slack but notifications.slack.workspaces is empty. A route to a receiver kind that is not configured looks like a route and reaches nobody." $where) -}}
{{- end -}}
{{- if not $channel -}}
{{- fail (printf "observability-stack: %s sends to Slack with an empty `channel`. A token posts to the channel a message names; with none named Slack refuses every message, and the route looks wired up and delivers nothing." $where) -}}
{{- end -}}
{{- if and (not $workspace) (gt (len $names) 1) -}}
{{- fail (printf "observability-stack: %s sends to Slack without a `workspace`, and notifications.slack.workspaces declares %d. With exactly one workspace the name may be left out; with two or more it is required, because the same channel name is a different place in each." $where (len $names)) -}}
{{- end -}}
{{- if and $workspace (not (hasKey $names $workspace)) -}}
{{- fail (printf "observability-stack: %s names workspace %q, which no entry in notifications.slack.workspaces declares. A destination in a workspace with no bot token looks routed and reaches nobody." $where (toString $workspace)) -}}
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
{{- if and $p.group (or $p.groups $p.name) -}}
{{- fail (printf "observability-stack: tenancy.principals[%d] sets `group` beside `groups` or `name`. One principal is selected either by one group or by a list of them; guessing which was meant is how a token gets the wrong reach." $i) -}}
{{- end -}}
{{- if and (not $p.group) (not $p.groups) -}}
{{- fail (printf "observability-stack: tenancy.principals[%d] has neither `group` nor `groups`. The group is what the token's claim is matched against; without it the entry selects nobody." $i) -}}
{{- end -}}
{{- if and $p.groups (not $p.name) -}}
{{- fail (printf "observability-stack: tenancy.principals[%d] sets `groups` without a `name`. There is no single group to name the VMUser after, and two such entries would collide." $i) -}}
{{- end -}}
{{- $principalName := $p.name | default $p.group | default "" -}}
{{- range $g := ($p.groups | default (list $p.group)) -}}
{{- if not $g -}}
{{- fail (printf "observability-stack: principal %q lists an empty group. It would match nothing, or with a careless rewrite everything." $principalName) -}}
{{- end -}}
{{- if hasKey $groups $g -}}
{{- fail (printf "observability-stack: group %q appears twice in `tenancy.principals`. The second entry would be unreachable, so a grant somebody wrote would silently not apply." $g) -}}
{{- end -}}
{{- $_ := set $groups $g true -}}
{{- end -}}
{{- if not $p.grants -}}
{{- fail (printf "observability-stack: principal %q has no grants. A principal that may read nothing is written by leaving it out, not by granting it nothing." $principalName) -}}
{{- end -}}
{{- $clusters := dict -}}
{{- range $g := $p.grants -}}
{{- if not (regexMatch $shape (toString $g.cluster)) -}}
{{- fail (printf "observability-stack: principal %q has cluster %q, which is not a plain name (%s). The cluster is half of the scoping key, and names are interpolated into a filter expression, so one carrying `|`, `)` or `.*` would widen the grant rather than look odd. Such a name is refused, never escaped." $principalName (toString $g.cluster) $shape) -}}
{{- end -}}
{{- if hasKey $clusters $g.cluster -}}
{{- fail (printf "observability-stack: principal %q is granted cluster %q twice. Merge them, or one grant is silently ignored." $principalName $g.cluster) -}}
{{- end -}}
{{- $_ := set $clusters $g.cluster true -}}
{{- if and $g.allNamespaces $g.namespaces -}}
{{- fail (printf "observability-stack: principal %q grants cluster %q with both `allNamespaces` and a `namespaces` list. One of them is wrong, and guessing which is how a grant quietly widens." $principalName $g.cluster) -}}
{{- end -}}
{{- if and (not $g.allNamespaces) (not $g.namespaces) -}}
{{- fail (printf "observability-stack: principal %q grants cluster %q with neither `namespaces` nor `allNamespaces`. An empty list is refused rather than read as \"everything\": a project that expands to no namespaces is the likeliest way a grant widens by accident. A grant is namespaces on a cluster; a project is the derivation that produces the list, and it lives with whoever writes this file." $principalName $g.cluster) -}}
{{- end -}}
{{- range $ns := ($g.namespaces | default list) -}}
{{- if not (regexMatch $shape (toString $ns)) -}}
{{- fail (printf "observability-stack: principal %q grants namespace %q, which is not a plain name (%s). It would be interpolated into a filter expression, where `|` or `.*` widens the grant." $principalName (toString $ns) $shape) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- /*
This principal's OWN audience, and its OWN route restriction — both
optional, both checked here rather than left to the schema, because
what can go wrong with either is a value the schema cannot see is
wrong: an audience that is present but says nothing, or a restriction
that, combined with what else this principal set or what this install
has enabled, renders no route at all.
*/}}
{{- if and (hasKey $p "audience") (not (regexMatch $audienceShape (toString $p.audience))) -}}
{{- fail (printf "observability-stack: principal %q sets `audience` to %q, which is not an identifier (%s). Like `tenancy.audience`, a client id may otherwise carry a dot, a `|` or an `@` — the issuer assigns it, this chart does not — and is escaped and anchored where it is rendered, so it pins itself and nothing else. What is refused is a value no issuer mints: empty, or carrying whitespace or a newline, which is how a value that arrived from the wrong place looks. Leave `audience` out entirely to fall back to `tenancy.audience`, rather than setting it to nothing." $principalName (toString $p.audience) $audienceShape) -}}
{{- end -}}
{{- $routes := $p.routes | default (list "metrics" "logs" "traces") -}}
{{- $rendersTraces := and (has "traces" $routes) (include "observability-stack.tracesEnabled" $) -}}
{{- if not (or (has "metrics" $routes) (has "logs" $routes) $rendersTraces) -}}
{{- fail (printf "observability-stack: principal %q's `routes` (%s) renders no route at all. Metrics and logs render whenever named; `traces` renders only when a trace store is enabled (independent of `tenancy.allowUnfilteredTraceReads`, checked next). A principal that may read nothing is written by leaving it out of `tenancy.principals` entirely, not by restricting it to nothing." $principalName (join ", " $routes)) -}}
{{- end -}}
{{- if $p.vmalertAPI -}}
{{- if not (has "metrics" $routes) -}}
{{- fail (printf "observability-stack: principal %q sets `vmalertAPI: true` but its `routes` (%s) does not include `metrics`. The alerts and rules routes belong to a metrics reader: they are vmalert's view of the same store, and a principal with no metrics route has no business reading what its rules say." $principalName (join ", " $routes)) -}}
{{- end -}}
{{- if not $.Values.tenancy.allowUnfilteredAlertReads -}}
{{- fail (printf "observability-stack: principal %q sets `vmalertAPI: true` but `tenancy.allowUnfilteredAlertReads` is not. vmalert's alerts and rules have no per-namespace or per-cluster concept to filter on, so this principal's own grant would NOT scope them: it would read every alert and every rule's expression and labels this install's metrics vmalert holds, cluster-wide. Set `tenancy.allowUnfilteredAlertReads: true` and record that, or remove `vmalertAPI`." $principalName) -}}
{{- end -}}
{{- if not (include "observability-stack.effectiveEnabled" $ | fromYaml).vmalert -}}
{{- fail (printf "observability-stack: principal %q sets `vmalertAPI: true` but `vmalert.enabled` is false. There is no vmalert for these routes to read." $principalName) -}}
{{- end -}}
{{- end -}}
{{- if and $p.metricsQueryOnly (not (has "metrics" $routes)) -}}
{{- fail (printf "observability-stack: principal %q sets `metricsQueryOnly: true` but its `routes` (%s) does not include `metrics`. metricsQueryOnly narrows the metrics route to its two query paths; with no metrics route requested there is nothing for it to narrow, and the flag would mean nothing." $principalName (join ", " $routes)) -}}
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

Gated on whether a principal actually WANTS the trace route rather than
on `principals` being non-empty: a principal whose own `routes` excludes
`traces` (see above) never gets this targetRef, so an install with
traces enabled for other reasons and every principal scoped to metrics
or logs alone has nothing here to accept unscoped. Every principal's
default is unchanged — all three routes — so an install that sets no
`routes` anywhere is refused exactly as before.
*/}}
{{- $anyPrincipalWantsTraces := false -}}
{{- range $p := $t.principals -}}
{{- if has "traces" ($p.routes | default (list "metrics" "logs" "traces")) -}}
{{- $anyPrincipalWantsTraces = true -}}
{{- end -}}
{{- end -}}
{{- if and $anyPrincipalWantsTraces (include "observability-stack.tracesEnabled" .) -}}
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
    (fromYamlArray (include "observability-stack.readPaths.alerts" .))
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
Writer cluster pins.

A writer authenticates with a bearer token, not a person's identity, and
until `cluster` existed nothing stopped it from claiming to be a cluster
it is not: the collector's own config says `k8s_cluster_name=whatever`,
and vmauth forwarded it unchecked. `cluster`, when a writer sets it,
closes that — see values.yaml's own comment on `tenancy.writers` for the
per-signal mechanism and how each was confirmed to override rather than
duplicate.

`cluster` stays OPTIONAL, unlike `destinations`: the unscoped, local
writer — the shape charts/observability-emitters has always used, one
collector writing to the store beside it — needs no pin at all, and
leaving it unset is how that writer keeps rendering exactly as it did
before this value existed. It is a REMOTE writer, one whose collector
runs on a different cluster than this install, that leaving it unset
would be the silent failure this exists to close.
*/}}
{{- define "observability-stack.validate.writers" -}}
{{- $t := .Values.tenancy -}}
{{- $shape := "^[a-z0-9]([a-z0-9-]*[a-z0-9])?$" -}}
{{- $names := dict -}}
{{- $clusters := dict -}}
{{- range $w := $t.writers -}}
{{- if hasKey $names $w.name -}}
{{- fail (printf "observability-stack: writer %q appears twice in `tenancy.writers`. The second `VMUser` this chart renders would collide with the first's object name, and the operator drops all but one from vmauth's config with no error outside `status.currentSyncError` — a writer that silently stops being able to write." $w.name) -}}
{{- end -}}
{{- $_ := set $names $w.name true -}}
{{- if hasKey $w "cluster" -}}
{{- if not (regexMatch $shape (toString $w.cluster)) -}}
{{- fail (printf "observability-stack: writer %q sets `cluster` to %q, which is not a plain name (%s). It is forced onto every series, log record and span this writer sends — interpolated into the query argument the same way a reader's grant is — so a value carrying `|` or `.*` would not pin the writer to one cluster, it would widen what it can claim to be." $w.name (toString $w.cluster) $shape) -}}
{{- end -}}
{{- if and $t.ownCluster (eq (toString $w.cluster) (toString $t.ownCluster)) -}}
{{- fail (printf "observability-stack: writer %q sets `cluster: %s`, which is this install's own cluster (`tenancy.ownCluster`). Pinning a writer to the install's own cluster is the UNSCOPED writer's job — one with no `cluster` at all, writing to the store beside it exactly as charts/observability-emitters has always done. Leave `cluster` unset for a local writer, or name the remote cluster this writer actually runs on." $w.name (toString $w.cluster)) -}}
{{- end -}}
{{- if hasKey $clusters $w.cluster -}}
{{- fail (printf "observability-stack: writers %q and %q both set `cluster: %s`. Two bearer tokens pinned to the same cluster identity is almost always a `name` that was meant to be different and was not; if two collectors genuinely write for the same cluster, this chart has no objection to it happening under two names — it only refuses the name collision above from going unnoticed as a cluster collision instead." (index $clusters $w.cluster) $w.name (toString $w.cluster)) -}}
{{- end -}}
{{- $_ := set $clusters $w.cluster $w.name -}}
{{- end -}}
{{- end -}}
{{- /*
The one flag that would hand a writer's own claimed cluster back to it,
the write-side counterpart of the `mergeQueryArgs` refusal in
`validate.tenancy`. vmauth drops a client query argument that clashes
with one the route already set, and that drop is the only reason a
writer cannot simply send its own `extra_label`/`extra_fields` and claim
to be any cluster it likes; `mergeQueryArgs` exempts an argument from
that rule entirely, and BOTH stores keep the LAST value for a duplicate
name — the writer's own, sent after this chart's forced one, would win.
*/ -}}
{{- range $arg, $value := ($.Values.vmauth.extraArgs | default dict) -}}
{{- if eq (toString $arg) "mergeQueryArgs" -}}
{{- range $merged := (splitList "," (toString $value)) -}}
{{- if has (trim $merged) (list "extra_label" "extra_label[]" "extra_fields" "extra_fields[]") -}}
{{- fail (printf "observability-stack: `vmauth.extraArgs.mergeQueryArgs` names %q. That is the argument a writer's `cluster` pin is enforced with: vmauth drops a client query argument that clashes with one the route already set, and that drop is the only reason a writer cannot simply send its own %s and claim to be any cluster it likes. `mergeQueryArgs` exempts it from that rule entirely, and both VictoriaLogs/VictoriaTraces' `extra_fields` and VictoriaMetrics' `extra_label` keep the LAST value for a duplicate name — the writer's own, sent after this chart's, would win. Remove it, or stop pinning writers by cluster." (trim $merged) (trim $merged)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
`tenancy.alertReaders`: the same "nothing scopes this route" refusal
`allowUnfilteredTraceReads` already exists for, one route earlier.

vmalert's own `/api/v1/alerts` has no per-namespace or per-cluster
concept at all — there is no filter to substitute into it the way a
principal's grant substitutes into the metrics and logs routes. A
reader given this route reads every active alert this install's METRICS
vmalert is evaluating, cluster-wide, whatever else is configured beside
it. Rendering it anyway, because it looks like an ordinary read route,
is the same failure the trace refusal exists to prevent.
*/}}
{{- define "observability-stack.validate.alertReaders" -}}
{{- $observabilityStackEffective := include "observability-stack.effectiveEnabled" . | fromYaml -}}
{{- $t := .Values.tenancy -}}
{{- if and $t.alertReaders (not $t.allowUnfilteredAlertReads) -}}
{{- fail "observability-stack: `tenancy.alertReaders` is set but `tenancy.allowUnfilteredAlertReads` is not. vmalert's own `/api/v1/alerts` has no per-namespace or per-cluster concept to filter on, so a reader given this route reads every active alert this install's metrics vmalert is evaluating, cluster-wide. Set `tenancy.allowUnfilteredAlertReads: true` and record that every reader below sees every alert, or remove `tenancy.alertReaders`." -}}
{{- end -}}
{{- if and $t.alertReaders (not $observabilityStackEffective.vmalert) -}}
{{- fail "observability-stack: `tenancy.alertReaders` is set but `vmalert.enabled` is false. There is no vmalert for this route to read." -}}
{{- end -}}
{{- range $r := $t.alertReaders -}}
{{- if and $r.alertmanager (not $observabilityStackEffective.alertmanager) -}}
{{- fail (printf "observability-stack: `tenancy.alertReaders` entry %q sets `alertmanager: true` but `alertmanager.enabled` is false. There is no Alertmanager for this route to read." $r.name) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
`tenancy.readers`: a static bearer token that may QUERY the metrics store,
scoped by a grant (docs/notifications.md, "Evaluating another store's
rules"). The shapes (a plain name, a Secret name AND key, at least one
grant) are the schema's; what it cannot see is checked here, with the
grant checks the principals get — a grant that is empty, or names both
spellings, or names a cluster twice, is a reader that reads more or less
than somebody meant.
*/}}
{{- define "observability-stack.validate.readers" -}}
{{- $t := .Values.tenancy -}}
{{- if $t.readers -}}
{{- $eff := include "observability-stack.effectiveEnabled" . | fromYaml -}}
{{- if not $eff.vmauth -}}
{{- fail "observability-stack: `tenancy.readers` is set but this install renders no vmauth (`mode` is not \"full\", or `vmauth.enabled` is false). A reader's bearer token authenticates against the proxy; with none, the reader is configured and unreachable. Set it on the install that runs the proxy, or empty the list." -}}
{{- end -}}
{{- $fullname := include "observability-stack.fullname" . -}}
{{- $shape := "^[a-z0-9]([a-z0-9-]*[a-z0-9])?$" -}}
{{- $names := dict -}}
{{- range $r := $t.readers -}}
{{- if not (regexMatch $shape (toString $r.name)) -}}
{{- fail (printf "observability-stack: `tenancy.readers` entry %q is not a DNS label (lower-case alphanumerics and hyphens, starting and ending with an alphanumeric)." (toString $r.name)) -}}
{{- end -}}
{{- if hasKey $names $r.name -}}
{{- fail (printf "observability-stack: reader %q appears twice in `tenancy.readers`. The second `VMUser` this chart renders would collide with the first's object name, and the operator drops all but one from vmauth's config with no error outside `status.currentSyncError` — a reader that silently stops being able to read." $r.name) -}}
{{- end -}}
{{- $_ := set $names $r.name true -}}
{{- if gt (len (printf "%s-reader-%s" $fullname $r.name)) 63 -}}
{{- fail (printf "observability-stack: reader %q makes the VMUser name %q longer than 63 characters, and a truncated name can collide with another reader's. Shorten it." $r.name (printf "%s-reader-%s" $fullname $r.name)) -}}
{{- end -}}
{{- range $p := $t.principals -}}
{{- $pn := $p.name | default $p.group | default "" -}}
{{- $slug := regexReplaceAll "[^a-z0-9]+" (lower $pn) "-" | trimAll "-" | trunc 40 | trimSuffix "-" -}}
{{- if eq $slug (printf "reader-%s" $r.name) -}}
{{- fail (printf "observability-stack: reader %q and principal %q would render the same VMUser object name. Rename one of them." $r.name $pn) -}}
{{- end -}}
{{- end -}}
{{- if not $r.grants -}}
{{- fail (printf "observability-stack: reader %q has no grants. A reader with no grant would read nothing, or with a careless rewrite everything; a reader that may read everything on a cluster says so with `allNamespaces: true`." $r.name) -}}
{{- end -}}
{{- $clusters := dict -}}
{{- range $g := $r.grants -}}
{{- if not (regexMatch $shape (toString $g.cluster)) -}}
{{- fail (printf "observability-stack: reader %q has cluster %q, which is not a plain name (%s). Names are interpolated into a filter expression, so one carrying `|`, `)` or `.*` would widen the grant." $r.name (toString $g.cluster) $shape) -}}
{{- end -}}
{{- if hasKey $clusters $g.cluster -}}
{{- fail (printf "observability-stack: reader %q is granted cluster %q twice. Merge them, or one grant is silently ignored." $r.name $g.cluster) -}}
{{- end -}}
{{- $_ := set $clusters $g.cluster true -}}
{{- if and $g.allNamespaces $g.namespaces -}}
{{- fail (printf "observability-stack: reader %q grants cluster %q with both `allNamespaces` and a `namespaces` list. One of them is wrong, and guessing which is how a grant quietly widens." $r.name $g.cluster) -}}
{{- end -}}
{{- if and (not $g.allNamespaces) (not $g.namespaces) -}}
{{- fail (printf "observability-stack: reader %q grants cluster %q with neither `namespaces` nor `allNamespaces`. An empty grant is refused rather than read as \"everything\": say `allNamespaces: true` if that is meant." $r.name $g.cluster) -}}
{{- end -}}
{{- range $ns := ($g.namespaces | default list) -}}
{{- if not (regexMatch $shape (toString $ns)) -}}
{{- fail (printf "observability-stack: reader %q grants namespace %q, which is not a plain name (%s). It would be interpolated into a filter expression, where `|` or `.*` widens the grant." $r.name (toString $ns) $shape) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- /* The grant is enforced by a query argument the route sets; a client one that is merged instead of dropped would win. */ -}}
{{- range $arg, $value := ($.Values.vmauth.extraArgs | default dict) -}}
{{- if eq (toString $arg) "mergeQueryArgs" -}}
{{- range $merged := (splitList "," (toString $value)) -}}
{{- if has (trim $merged) (list "extra_filters" "extra_filters[]") -}}
{{- fail (printf "observability-stack: `vmauth.extraArgs.mergeQueryArgs` names %q while `tenancy.readers` is set. A reader's grant is enforced as that query argument on its route, and vmauth drops a client's clashing one only when it is NOT merged: with it merged the caller's own filter is sent beside the grant's, and a caller who may send its own filter may send a wider one. Remove it." (trim $merged)) -}}
{{- end -}}
{{- end -}}
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
{{- /*
Which identity a datasource carries follows whether there is a proxy.

With vmauth (the default), every datasource forwards the signed-in
person's token (`oauthPassThru`) and the proxy scopes it — refused
otherwise, below.

Without it there is nothing to scope a token and nothing that accepts
one: a store running `-httpAuth.*` answers 401 to a bearer token. So the
rule flips. A datasource must NOT pass the person's token through, and
must authenticate as `storeCredentials` — basic auth, whose user and
password are environment references Grafana expands at provisioning, to
variables `grafana.envValueFrom` reads from `storeCredentials`' own
Secret and keys. Checked as a MIRROR, the same way the stores' own `env`
is: a datasource reading a different Secret is a dashboard that 401s on
every panel, and a password written into values is a credential in git.
*/ -}}
{{- $observabilityStackEffective := include "observability-stack.effectiveEnabled" $ | fromYaml -}}
{{- $envRefShape := "^\\$(\\{|__env\\{)([A-Za-z_][A-Za-z0-9_]*)\\}$" -}}
{{- $sc := $.Values.storeCredentials -}}
{{- range $file, $doc := ($g.datasources | default dict) -}}
{{- range $ds := ($doc.datasources | default list) -}}
{{- if $observabilityStackEffective.vmauth -}}
{{- if not (($ds.jsonData).oauthPassThru) -}}
{{- fail (printf "observability-stack: Grafana datasource %q (in %s) does not set `jsonData.oauthPassThru: true`. Without it every query reaches the proxy as GRAFANA's identity rather than the signed-in person's, so the proxy scopes nothing and a viewer sees every namespace on every cluster Grafana can see. That is the failure this whole chart exists to prevent, and it looks exactly like a working dashboard." (toString $ds.name) $file) -}}
{{- end -}}
{{- else -}}
{{- if ($ds.jsonData).oauthPassThru -}}
{{- fail (printf "observability-stack: Grafana datasource %q (in %s) sets `jsonData.oauthPassThru: true`, but `vmauth` is off. There is no proxy to scope the person's token, and the store it now reaches directly runs `-httpAuth.*`: it answers 401 to a bearer token on every panel. Without the proxy a datasource authenticates as `storeCredentials` instead — drop `oauthPassThru` and set `basicAuth: true`, `basicAuthUser: ${VAR}` and `secureJsonData.basicAuthPassword: ${VAR}` (docs/reference.md, \"Grafana without the proxy\")." (toString $ds.name) $file) -}}
{{- end -}}
{{- $user := toString ($ds.basicAuthUser | default "") -}}
{{- $password := toString (($ds.secureJsonData).basicAuthPassword | default "") -}}
{{- if or (not $ds.basicAuth) (not $user) (not $password) -}}
{{- fail (printf "observability-stack: Grafana datasource %q (in %s) does not authenticate to the store, and `vmauth` is off. Every store runs `-httpAuth.*` from `storeCredentials`, so a datasource without its credential answers 401 on every panel. Set `basicAuth: true`, `basicAuthUser: ${VAR}` and `secureJsonData.basicAuthPassword: ${VAR}`, each variable read by `grafana.envValueFrom` from Secret %q (docs/reference.md, \"Grafana without the proxy\")." (toString $ds.name) $file $sc.secretName) -}}
{{- end -}}
{{- range $pair := list (list "basicAuthUser" $user $sc.usernameKey) (list "secureJsonData.basicAuthPassword" $password $sc.passwordKey) -}}
{{- $field := index $pair 0 -}}
{{- $value := index $pair 1 -}}
{{- $wantKey := index $pair 2 -}}
{{- if not (regexMatch $envRefShape $value) -}}
{{- fail (printf "observability-stack: Grafana datasource %q (in %s) sets `%s` to a literal rather than an environment reference (`${VAR}` or `$__env{VAR}`). A literal store credential in values is a credential in git and in the release's manifest; read it from `storeCredentials`' Secret through `grafana.envValueFrom` instead." (toString $ds.name) $file $field) -}}
{{- end -}}
{{- $var := regexReplaceAll $envRefShape $value "${2}" -}}
{{- $ref := ((index ($g.envValueFrom | default dict) $var) | default dict).secretKeyRef | default dict -}}
{{- if or (ne (toString $ref.name) (toString $sc.secretName)) (ne (toString $ref.key) (toString $wantKey)) -}}
{{- fail (printf "observability-stack: Grafana datasource %q (in %s) reads `%s` from $%s, but `grafana.envValueFrom.%s` reads Secret %q key %q rather than `storeCredentials`' Secret %q key %q. The store checks exactly that credential, so any other one is a datasource that 401s on every panel. MIRROR: set `grafana.envValueFrom.%s.secretKeyRef` to {name: %s, key: %s}." (toString $ds.name) $file $field $var $var (toString $ref.name) (toString $ref.key) $sc.secretName $wantKey $var $sc.secretName $wantKey) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if not (hasKey $ds "version") -}}
{{- fail (printf "observability-stack: Grafana datasource %q (in %s) has no `version`. With more than one replica Grafana only updates a provisioned datasource whose version is greater than or equal to the stored one, so an edit without a bump lands on a fresh install and nowhere else." (toString $ds.name) $file) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- /*
The session-signing key's Secret. Configurable all along
(`grafana.envValueFrom.GF_SECURITY_SECRET_KEY.secretKeyRef`), and
deliberately left EMPTY by default because the Secret is the estate's to
name — any Secret and key, the admin one included. Left empty on an
enabled Grafana the pod spec names a Secret called "", which the API
server rejects on apply; refused here instead, with the fix in the
message. An estate that removed the variable outright (`null`) is not
checked: that is a choice, not an oversight.
*/ -}}
{{- $secretKey := index ($g.envValueFrom | default dict) "GF_SECURITY_SECRET_KEY" -}}
{{- if and $secretKey (not (($secretKey.secretKeyRef).name)) -}}
{{- fail "observability-stack: Grafana is enabled and `grafana.envValueFrom.GF_SECURITY_SECRET_KEY.secretKeyRef.name` is empty. That is the key Grafana signs sessions and encrypts datasource secrets with; name the Secret (and `key`) that holds it — it may be the same Secret as `grafana.admin.existingSecret`, so it needs a key, not a Secret of its own." -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
`tenancy.clusterLabel`, MIRROR of `victoria-metrics-k8s-stack.global.clusterLabel`.

The vendored stack's sync job rewrites the `cluster` label in the default
rules it fetches to this value. It is a subchart value, which Helm evaluates
before any template here runs, so this chart cannot compute it from
`tenancy.clusterLabel`; it is written in both places and checked. A
disagreement means the recorded series carry a label the rest of the estate
never stamps, and joins across clusters on a shared store.
*/}}
{{- define "observability-stack.validate.k8sStackClusterLabel" -}}
{{- $vmks := index .Values "victoria-metrics-k8s-stack" -}}
{{- if $vmks.enabled -}}
{{- $got := toString (($vmks.global).clusterLabel | default "") -}}
{{- $want := toString .Values.tenancy.clusterLabel -}}
{{- if ne $got $want -}}
{{- fail (printf "observability-stack: `tenancy.clusterLabel` is %q but `victoria-metrics-k8s-stack.global.clusterLabel` is %q. The vendored default rules join and aggregate on the second; every series in the stores carries the first. Left this way the recorded k8s-stack series carry no cluster identity, and on a shared store the joins match the same namespace and pod across clusters. Set `victoria-metrics-k8s-stack.global.clusterLabel: %s`." $want $got $want) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
karma, the alert console, and the notification links that point at it.

The point of karma here is the AUTHOR of a silence: Alertmanager takes
`createdBy` as free text, and karma with header authentication rewrites it
to the signed-in user and applies silence ACLs. Every refusal below is a
way for that to quietly not be true.

- no `authentication.header.name` and no `authentication.none`: a console
  that silences pages would run anonymously by accident;
- a header name with no `valueRe`, or a groups header with no
  `groupValueRe`: karma refuses to start (its own rule), and the Pod would
  crash-loop instead of this render failing;
- ACL rules and groups karma cannot honour: a rule naming a group that is
  not declared protects nothing, and groups with no header authentication
  never match a user;
- `notifications.console: karma` with no `consoleUrl`, or with karma off:
  a message whose silence link points at nothing.
*/}}
{{- define "observability-stack.validate.karma" -}}
{{- $n := .Values.notifications | default dict -}}
{{- $console := $n.console | default "alertmanager" -}}
{{- if not (has $console (list "alertmanager" "karma")) -}}
{{- fail (printf "observability-stack: notifications.console is %q, which is neither \"alertmanager\" nor \"karma\"." (toString $console)) -}}
{{- end -}}
{{- /*
`consoleUrl` is embedded in an Alertmanager template exactly like
`alertmanagerUrl` is, so it is held to the same shape.
*/ -}}
{{- with $n.consoleUrl -}}
{{- if or (not (regexMatch "^https?://[^\\s/\"'`{}\\\\<>]" (toString .))) (regexMatch "[\\s\"'`{}\\\\<>]" (toString .)) (hasSuffix "/" (toString .)) -}}
{{- fail (printf "observability-stack: notifications.consoleUrl is %q. It must be an absolute http:// or https:// URL with a host, without a trailing slash and without whitespace, quotes, braces or backslashes: the chart appends `/?m=...` to it and embeds it in an Alertmanager message template." (toString .)) -}}
{{- end -}}
{{- end -}}
{{- if eq $console "karma" -}}
{{- if not $n.consoleUrl -}}
{{- fail "observability-stack: notifications.console is \"karma\" but notifications.consoleUrl is empty. The Silence and View links are built on karma's external base URL (for example https://karma.example.com), and the chart cannot guess it." -}}
{{- end -}}
{{- if not .Values.karma.enabled -}}
{{- fail "observability-stack: notifications.console is \"karma\" but karma.enabled is false. The message would link to a console this release does not run. Set `karma.enabled: true`, or leave `notifications.console` at \"alertmanager\"." -}}
{{- end -}}
{{- end -}}
{{- if .Values.karma.enabled -}}
{{- $k := .Values.karma -}}
{{- $eff := include "observability-stack.effectiveEnabled" . | fromYaml -}}
{{- $h := $k.authentication.header -}}
{{- if and $k.authentication.none $h.name -}}
{{- fail "observability-stack: karma.authentication.none is true and karma.authentication.header.name is set. They contradict: one says karma runs anonymously, the other that it trusts a header. Keep one." -}}
{{- end -}}
{{- if and (not $k.authentication.none) (not $h.name) -}}
{{- fail "observability-stack: karma.enabled is true with no karma.authentication.header.name. A console that silences pages must not run anonymously by accident: without authentication karma creates every silence under whatever name the browser sends, which is the free-text `createdBy` problem it is here to solve. Set `karma.authentication.header.name` to the header your SSO gateway sets (for example X-Auth-Request-Email), or acknowledge an anonymous console with `karma.authentication.none: true`." -}}
{{- end -}}
{{- if $h.name -}}
{{- if not (regexMatch "^[A-Za-z0-9-]+$" (toString $h.name)) -}}
{{- fail (printf "observability-stack: karma.authentication.header.name is %q. It must be an HTTP header name: letters, digits and dashes." (toString $h.name)) -}}
{{- end -}}
{{- if not $h.valueRe -}}
{{- fail "observability-stack: karma.authentication.header.name is set but karma.authentication.header.valueRe is empty. karma requires `value_re` whenever a header name is set, and refuses to start without it. The default is ^(.+)$." -}}
{{- end -}}
{{- end -}}
{{- if and $h.groupName (not $h.groupValueRe) -}}
{{- fail "observability-stack: karma.authentication.header.groupName is set but karma.authentication.header.groupValueRe is empty. karma requires `group_value_re` whenever a groups header name is set, and refuses to start without it." -}}
{{- end -}}
{{- if and (or $h.groupValueRe $h.groupValueSeparator) (not $h.groupName) -}}
{{- fail "observability-stack: karma.authentication.header.groupValueRe or groupValueSeparator is set without groupName. karma would read no groups header, and the value would do nothing." -}}
{{- end -}}
{{- if and $k.authorization.groups (not $h.name) -}}
{{- fail "observability-stack: karma.authorization.groups is set without karma.authentication.header.name. Groups map the user names the authentication layer passes, and with no header authentication there are none: no ACL scoped to a group would ever match." -}}
{{- end -}}
{{- $groupNames := dict -}}
{{- range $g := $k.authorization.groups -}}
{{- if or (not $g.name) (not $g.members) -}}
{{- fail "observability-stack: every karma.authorization.groups entry needs a `name` and a non-empty `members` list." -}}
{{- end -}}
{{- if hasKey $groupNames $g.name -}}
{{- fail (printf "observability-stack: karma.authorization.groups declares %q twice." (toString $g.name)) -}}
{{- end -}}
{{- $_ := set $groupNames $g.name true -}}
{{- end -}}
{{- range $i, $r := $k.acl.silences -}}
{{- if not (has (toString $r.action) (list "allow" "block" "requireMatcher")) -}}
{{- fail (printf "observability-stack: karma.acl.silences[%d].action is %q. karma knows allow, block and requireMatcher." $i (toString $r.action)) -}}
{{- end -}}
{{- range $g := (($r.scope).groups | default list) -}}
{{- if not (hasKey $groupNames $g) -}}
{{- fail (printf "observability-stack: karma.acl.silences[%d].scope.groups names %q, which karma.authorization.groups does not declare. A rule scoped to an unknown group applies to nobody." $i (toString $g)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $names := dict -}}
{{- if $eff.alertmanager -}}
{{- $_ := set $names "alertmanager" true -}}
{{- /* An Alertmanager pair names its replicas alertmanager-0, -1, ... */ -}}
{{- if gt (int $.Values.alertmanager.replicaCount) 1 -}}
{{- range $r := until (int $.Values.alertmanager.replicaCount) -}}
{{- $_ := set $names (printf "alertmanager-%d" $r) true -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- range $i, $s := ($k.alertmanagers | default list) -}}
{{- if or (not $s.name) (not $s.uri) -}}
{{- fail (printf "observability-stack: karma.alertmanagers[%d] needs a `name` and a `uri`." $i) -}}
{{- end -}}
{{- if hasKey $names $s.name -}}
{{- fail (printf "observability-stack: karma.alertmanagers[%d].name is %q, which is already taken. This release's own Alertmanager is named \"alertmanager\" and every name must be unique." $i (toString $s.name)) -}}
{{- end -}}
{{- $_ := set $names $s.name true -}}
{{- end -}}
{{- if not $names -}}
{{- fail "observability-stack: karma.enabled is true but karma has no Alertmanager to read: alertmanager.enabled is false and karma.alertmanagers is empty. Name the Alertmanager the estate runs in karma.alertmanagers." -}}
{{- end -}}
{{- if and $k.history.enabled (not $k.history.uri) -}}
{{- fail "observability-stack: karma.history.enabled is true but karma.history.uri is empty. karma needs the Prometheus-compatible endpoint that holds the ALERTS series." -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
`vmalert.remoteEvaluators`: one extra VMAlert per entry, evaluating the
VMRules labelled `observability.truvity.io/evaluator: <name>` against
another store and notifying through this install's Alertmanager. Every
check refuses a shape that renders and then evaluates nothing, or notifies
nobody, or evaluates the same rule twice. docs/notifications.md,
"Evaluating another store's rules".
*/}}
{{- define "observability-stack.validate.remoteEvaluators" -}}
{{- $evs := .Values.vmalert.remoteEvaluators | default list -}}
{{- if $evs -}}
{{- $eff := include "observability-stack.effectiveEnabled" . | fromYaml -}}
{{- $fullname := include "observability-stack.fullname" . -}}
{{- $mode := ((.Values.notifications | default dict).mode) | default "route" -}}
{{- if not $eff.vmalert -}}
{{- fail "observability-stack: `vmalert.remoteEvaluators` is set but this install renders no vmalert (`mode` is not \"full\", or `vmalert.enabled` is false). A remote evaluator notifies through this install's Alertmanager; `mode: replica` and `mode: operator-only` have none. Set it on the full install, or empty the list." -}}
{{- end -}}
{{- if eq $mode "evaluate-only" -}}
{{- fail "observability-stack: `vmalert.remoteEvaluators` is set and `notifications.mode` is `evaluate-only`. Evaluate-only sends nothing to anybody, which is the very thing a remote evaluator exists to avoid: its alerts would be evaluated and notify nobody. Drop `notifications.mode` back to `route`, or empty the list." -}}
{{- end -}}
{{- $names := dict -}}
{{- range $i, $e := $evs -}}
{{- if not $e.name -}}
{{- fail (printf "observability-stack: vmalert.remoteEvaluators[%d] has an empty `name`. The name is the value of the `observability.truvity.io/evaluator` label its rules carry and names its VMAlert." $i) -}}
{{- end -}}
{{- if not (regexMatch "^[a-z0-9]([-a-z0-9]*[a-z0-9])?$" (toString $e.name)) -}}
{{- fail (printf "observability-stack: vmalert.remoteEvaluators[%d] name %q is not a DNS label (lower-case alphanumerics and hyphens, starting and ending with an alphanumeric)." $i (toString $e.name)) -}}
{{- end -}}
{{- if gt (len (printf "%s-remote-%s" $fullname $e.name)) 52 -}}
{{- fail (printf "observability-stack: vmalert.remoteEvaluators[%d] name %q makes the VMAlert name %q longer than 52 characters, which the pod and Service names derived from it cannot carry. Shorten it." $i (toString $e.name) (printf "%s-remote-%s" $fullname $e.name)) -}}
{{- end -}}
{{- if hasKey $names $e.name -}}
{{- fail (printf "observability-stack: vmalert.remoteEvaluators has two entries named %q. Two evaluators for one name would both evaluate the same rules and name one VMAlert twice." (toString $e.name)) -}}
{{- end -}}
{{- $_ := set $names $e.name true -}}
{{- $ds := $e.datasource | default dict -}}
{{- if not $ds.url -}}
{{- fail (printf "observability-stack: vmalert.remoteEvaluators[%d] (%s) has no `datasource.url`. It is the other store's read endpoint; without it there is nothing to evaluate against." $i (toString $e.name)) -}}
{{- end -}}
{{- if not (regexMatch "^https?://[^\\s]+$" (toString $ds.url)) -}}
{{- fail (printf "observability-stack: vmalert.remoteEvaluators[%d] (%s) `datasource.url` %q is not an http(s) URL." $i (toString $e.name) (toString $ds.url)) -}}
{{- end -}}
{{- $auth := $ds.auth | default dict -}}
{{- if eq (not (not $auth.bearer)) (not (not $auth.basic)) -}}
{{- fail (printf "observability-stack: vmalert.remoteEvaluators[%d] (%s) needs `datasource.auth` with exactly one of `bearer` ({secretName, key}) or `basic` ({secretName, usernameKey, passwordKey}), each naming an EXISTING Secret. An unauthenticated read of another store is refused, and a credential is never a value here." $i (toString $e.name)) -}}
{{- end -}}
{{- with $auth.bearer -}}
{{- if not (and .secretName .key) -}}
{{- fail (printf "observability-stack: vmalert.remoteEvaluators[%d] (%s) `datasource.auth.bearer` must set both `secretName` and `key`: the Secret and the key holding the token." $i (toString $e.name)) -}}
{{- end -}}
{{- end -}}
{{- with $auth.basic -}}
{{- if not (and .secretName .usernameKey .passwordKey) -}}
{{- fail (printf "observability-stack: vmalert.remoteEvaluators[%d] (%s) `datasource.auth.basic` must set `secretName`, `usernameKey` and `passwordKey`." $i (toString $e.name)) -}}
{{- end -}}
{{- end -}}
{{- with $ds.caBundle -}}
{{- if eq (not (not .configMap)) (not (not .secret)) -}}
{{- fail (printf "observability-stack: vmalert.remoteEvaluators[%d] (%s) `datasource.caBundle` must name exactly one of `configMap` or `secret` ({name, key})." $i (toString $e.name)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

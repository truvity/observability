{{- define "observability-proxy.validate" -}}
{{- include "observability-proxy.validate.mode" . -}}
{{- include "observability-proxy.validate.vmauth" . -}}
{{- include "observability-proxy.validate.resources" . -}}
{{- include "observability-proxy.validate.tenancy" . -}}
{{- include "observability-proxy.validate.tenancyAuthz" . -}}
{{- include "observability-proxy.validate.writers" . -}}
{{- include "observability-proxy.validate.alertReaders" . -}}
{{- include "observability-proxy.validate.readers" . -}}
{{- include "observability-proxy.validate.routeOverlap" . -}}
{{- include "observability-proxy.validate.scrapeFrom" . -}}
{{- end -}}

{{/*
`mode`: the proxy belongs to a `mode: full` install. A replica never runs
one (it is the primary release's), and operator-only runs none of the
stores it would sit in front of, so this chart has nothing to render there.
Do not install it beside such an install.
*/}}
{{- define "observability-proxy.validate.mode" -}}
{{- $mode := .Values.mode | default "full" -}}
{{- if ne $mode "full" -}}
{{- fail (printf "observability-proxy: `mode` is %q. The proxy (vmauth, its VMUsers, tenancy-authz and their network policies) belongs to a `mode: full` install; the stack renders none of it in any other mode, so this chart has nothing to render. Do not install it beside this install." (toString $mode)) -}}
{{- end -}}
{{- end -}}

{{/*
The proxy's own refusals, from the stack's `ha`, `licence` and `resources`
checks: the ones that name `vmauth`.
*/}}
{{- define "observability-proxy.validate.vmauth" -}}
{{- $haCfg := include "observability-proxy.ha" . | fromYaml -}}
{{- if $haCfg.enabled -}}
{{- $eff := include "observability-proxy.effectiveEnabled" . | fromYaml -}}
{{- if not $eff.vmauth -}}
{{- fail "observability-proxy: `ha.enabled` is true on the primary but the proxy is off. The pair is read through `vmauth` (`first_available` over both stores); with it off nothing fails over and the second store is never read. Set `vmauth.enabled: true` (or leave it unset) or turn `ha` off." -}}
{{- end -}}
{{- if ne .Values.vmauth.loadBalancingPolicy "first_available" -}}
{{- fail (printf "observability-proxy: `ha.enabled` is true but `vmauth.loadBalancingPolicy` is %q. A read must go to this release's store and fall over to the peer only when it fails: `least_loaded` sends half of every dashboard to the replica, including one still replaying its buffer after a restart. Set it to \"first_available\"." .Values.vmauth.loadBalancingPolicy) -}}
{{- end -}}
{{- if lt (int .Values.vmauth.replicaCount) 2 -}}
{{- fail (printf "observability-proxy: `ha.enabled` is true but `vmauth.replicaCount` is %d. The proxy is what both stores are read through; one pod takes every read and every cross-cluster write with its zone. Set it to 2 or more." (int .Values.vmauth.replicaCount)) -}}
{{- end -}}
{{- end -}}
{{- /*
Community edition only, and the version floor (see the stack's
`validate.licence`): an Enterprise image runs without a key and breaches the
vendor's terms silently, and v1.147.0 through v1.151.x matched `match_claims`
values UNANCHORED (GHSA-f99m-22fh-qw96).
*/ -}}
{{- if contains "enterprise" (lower (toString .Values.vmauth.image.tag)) -}}
{{- fail (printf "observability-proxy: vmauth.image.tag is %q. This chart wraps the COMMUNITY edition only. An Enterprise image without a licence key starts and serves, refusing only the Enterprise features, so nothing reports that the estate is in breach of the vendor's terms. Use the community tag." (toString .Values.vmauth.image.tag)) -}}
{{- end -}}
{{- range $flag, $_ := (.Values.vmauth.extraArgs | default dict) -}}
{{- if or (eq $flag "license") (eq $flag "licenseFile") (hasPrefix "license." $flag) -}}
{{- fail (printf "observability-proxy: vmauth.extraArgs sets `%s`. A licence flag is an Enterprise flag, and this chart never renders one. Remove it." $flag) -}}
{{- end -}}
{{- end -}}
{{- $tag := trimPrefix "v" (toString .Values.vmauth.image.tag) -}}
{{- if not (semverCompare ">=1.152.0" $tag) -}}
{{- fail (printf "observability-proxy: vmauth.image.tag is %q. The floor for this design is v1.152.0: `default_vm_access_claim` arrived in v1.147.0, and every build from v1.138.0, where claim matching was introduced, to v1.151.x matched `match_claims` values unanchored (GHSA-f99m-22fh-qw96), an authorisation bypass in the exact mechanism this chart selects users with. Use v1.152.0 or later." (toString .Values.vmauth.image.tag)) -}}
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
{{- define "observability-proxy.validate.scrapeFrom" -}}
{{- range $i, $peer := (.Values.networkPolicy.scrapeFrom | default list) -}}
{{- if and (not $peer.podSelector) (not $peer.namespaceSelector) (hasKey $peer "ipBlock") -}}
{{- fail (printf "observability-proxy: networkPolicy.scrapeFrom[%d] admits a scraper by `ipBlock` alone. A pod IP is reassigned on every reschedule, eviction and rollout, so this rule works today and stops working silently the first time the scraper pod moves — the same failure this value exists to fix, reintroduced by the value meant to fix it. Name the scraper by a `podSelector` (and a `namespaceSelector` if it runs outside this release's namespace), the way this chart's own default does." $i) -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{- define "observability-proxy.validate.tenancy" -}}
{{- $shape := "^[a-z0-9]([a-z0-9-]*[a-z0-9])?$" -}}
{{- $fieldShape := "^[a-zA-Z0-9_][a-zA-Z0-9_./-]*$" -}}
{{- $audienceShape := "^\\S+$" -}}
{{- $t := .Values.tenancy -}}
{{- if and $t.principals (not $t.issuerUrl) -}}
{{- fail "observability-proxy: `tenancy.principals` is set but `tenancy.issuerUrl` is empty. vmauth verifies a token against the issuer's OIDC discovery document; with no issuer there is nothing to verify a signature against, and a proxy that trusts an unverified token is worse than no proxy at all." -}}
{{- end -}}
{{- /* The vm_access reader (opt-in). */ -}}
{{- if $t.vmAccessReader.enabled -}}
{{- /* With `tenancy.authz` on, the issuer and the audience are the service's: see validate.tenancyAuthz. */ -}}
{{- if and (not $t.authz.enabled) (not $t.issuerUrl) -}}
{{- fail "observability-proxy: `tenancy.vmAccessReader.enabled` is set but `tenancy.issuerUrl` is empty. vmauth verifies a token against the issuer's OIDC discovery document; with no issuer there is nothing to verify a signature against." -}}
{{- end -}}
{{- $vrAud := $t.vmAccessReader.audience | default $t.audience -}}
{{- if and (not $t.authz.enabled) (not $vrAud) -}}
{{- fail "observability-proxy: `tenancy.vmAccessReader.enabled` is set but neither `tenancy.vmAccessReader.audience` nor `tenancy.audience` is. vmauth never inspects `aud` on its own, so the reader would admit ANY unexpired token of the issuer that carries a `vm_access` claim, whatever client it was minted for; the pin in `matchClaims` is the only place the check can be made." -}}
{{- end -}}
{{- if and (not $t.authz.enabled) (not (regexMatch $audienceShape (toString $vrAud))) -}}
{{- fail (printf "observability-proxy: the vm_access reader's audience %q is not an identifier (%s): it carries whitespace or a newline." (toString $vrAud) $audienceShape) -}}
{{- end -}}
{{- $vrRoutes := $t.vmAccessReader.routes | default (list "metrics" "logs" "traces") -}}
{{- if not (or (has "metrics" $vrRoutes) (has "logs" $vrRoutes) (and (has "traces" $vrRoutes) (include "observability-proxy.tracesEnabled" .))) -}}
{{- fail (printf "observability-proxy: `tenancy.vmAccessReader.routes` (%s) renders no route at all (`traces` renders only when a trace store is enabled). Leave the reader off instead." (join ", " $vrRoutes)) -}}
{{- end -}}
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
{{- fail "observability-proxy: `tenancy.principals` is set but `tenancy.audience` is empty. vmauth validates a token's EXPIRY and, under OIDC discovery, its ISSUER, and nothing else: it has no audience option and never inspects `aud` on its own. So each reader below would be selected by its group alone, and ANY unexpired token that issuer minted would be admitted whatever client it was minted for — a token the same person holds for another application of the same issuer reads their namespaces here, and nothing reports it, because the token verifies and the filters apply. Set it to the client id this proxy's tokens are minted under; it is pinned into every reader's `matchClaims` as `aud`, which is the only place vmauth can be made to check it." -}}
{{- end -}}
{{- if and $t.audience (not (regexMatch $audienceShape (toString $t.audience))) -}}
{{- fail (printf "observability-proxy: `tenancy.audience` is %q, which is not an identifier (%s): it carries whitespace or a newline. A client id may otherwise be anything the issuer assigned — a dot, an `@`, a colon — and is ESCAPED where it is rendered rather than refused here, because the issuer chooses it and this chart does not. What this shape refuses is a value that arrived from the wrong place: a file read with its trailing newline, a heredoc, or two ids in one string." (toString $t.audience) $audienceShape) -}}
{{- end -}}
{{- if eq (toString $t.claimName) "aud" -}}
{{- fail "observability-proxy: `tenancy.claimName` is `aud`, which is the claim `tenancy.audience` is pinned under. Both are entries in one `matchClaims` map, so one would overwrite the other — and whichever survived would decide either which principal a token is or which client it was minted for, never both. Name the groups claim something else." -}}
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
{{- fail (printf "observability-proxy: `tenancy.%s` is %q, which is not a log field name (%s). A log field name may carry dots, and nothing that is stream-filter syntax: a quote, a brace, a comma, an equals sign, a `|`, a colon or a space would end the filter early or open a second alternative beside it, and the grant would be wider than the one somebody wrote. Such a name is refused, never escaped. Leave it at the default, which is the field charts/observability-emitters writes." $key (toString $field) $fieldShape) -}}
{{- end -}}
{{- end -}}
{{- if eq (toString $t.clusterLabel) (toString $t.namespaceLabel) -}}
{{- fail (printf "observability-proxy: `tenancy.clusterLabel` and `tenancy.namespaceLabel` are both %q. The two are the scoping key; a metrics filter with one name for both dimensions selects on one of them and ignores the other, so every grant would be wider or narrower than written." (toString $t.clusterLabel)) -}}
{{- end -}}
{{- if eq (toString $t.logsClusterField) (toString $t.logsNamespaceField) -}}
{{- fail (printf "observability-proxy: `tenancy.logsClusterField` and `tenancy.logsNamespaceField` are both %q. One stream filter would carry one dimension twice and the other not at all." (toString $t.logsClusterField)) -}}
{{- end -}}
{{- $groups := dict -}}
{{- range $i, $p := $t.principals -}}
{{- if and $p.group (or $p.groups $p.name) -}}
{{- fail (printf "observability-proxy: tenancy.principals[%d] sets `group` beside `groups` or `name`. One principal is selected either by one group or by a list of them; guessing which was meant is how a token gets the wrong reach." $i) -}}
{{- end -}}
{{- if and (not $p.group) (not $p.groups) -}}
{{- fail (printf "observability-proxy: tenancy.principals[%d] has neither `group` nor `groups`. The group is what the token's claim is matched against; without it the entry selects nobody." $i) -}}
{{- end -}}
{{- if and $p.groups (not $p.name) -}}
{{- fail (printf "observability-proxy: tenancy.principals[%d] sets `groups` without a `name`. There is no single group to name the VMUser after, and two such entries would collide." $i) -}}
{{- end -}}
{{- $principalName := $p.name | default $p.group | default "" -}}
{{- range $g := ($p.groups | default (list $p.group)) -}}
{{- if not $g -}}
{{- fail (printf "observability-proxy: principal %q lists an empty group. It would match nothing, or with a careless rewrite everything." $principalName) -}}
{{- end -}}
{{- if hasKey $groups $g -}}
{{- fail (printf "observability-proxy: group %q appears twice in `tenancy.principals`. The second entry would be unreachable, so a grant somebody wrote would silently not apply." $g) -}}
{{- end -}}
{{- $_ := set $groups $g true -}}
{{- end -}}
{{- if not $p.grants -}}
{{- fail (printf "observability-proxy: principal %q has no grants. A principal that may read nothing is written by leaving it out, not by granting it nothing." $principalName) -}}
{{- end -}}
{{- $clusters := dict -}}
{{- range $g := $p.grants -}}
{{- if not (regexMatch $shape (toString $g.cluster)) -}}
{{- fail (printf "observability-proxy: principal %q has cluster %q, which is not a plain name (%s). The cluster is half of the scoping key, and names are interpolated into a filter expression, so one carrying `|`, `)` or `.*` would widen the grant rather than look odd. Such a name is refused, never escaped." $principalName (toString $g.cluster) $shape) -}}
{{- end -}}
{{- if hasKey $clusters $g.cluster -}}
{{- fail (printf "observability-proxy: principal %q is granted cluster %q twice. Merge them, or one grant is silently ignored." $principalName $g.cluster) -}}
{{- end -}}
{{- $_ := set $clusters $g.cluster true -}}
{{- if and $g.allNamespaces $g.namespaces -}}
{{- fail (printf "observability-proxy: principal %q grants cluster %q with both `allNamespaces` and a `namespaces` list. One of them is wrong, and guessing which is how a grant quietly widens." $principalName $g.cluster) -}}
{{- end -}}
{{- if and (not $g.allNamespaces) (not $g.namespaces) -}}
{{- fail (printf "observability-proxy: principal %q grants cluster %q with neither `namespaces` nor `allNamespaces`. An empty list is refused rather than read as \"everything\": a project that expands to no namespaces is the likeliest way a grant widens by accident. A grant is namespaces on a cluster; a project is the derivation that produces the list, and it lives with whoever writes this file." $principalName $g.cluster) -}}
{{- end -}}
{{- range $ns := ($g.namespaces | default list) -}}
{{- if not (regexMatch $shape (toString $ns)) -}}
{{- fail (printf "observability-proxy: principal %q grants namespace %q, which is not a plain name (%s). It would be interpolated into a filter expression, where `|` or `.*` widens the grant." $principalName (toString $ns) $shape) -}}
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
{{- fail (printf "observability-proxy: principal %q sets `audience` to %q, which is not an identifier (%s). Like `tenancy.audience`, a client id may otherwise carry a dot, a `|` or an `@` — the issuer assigns it, this chart does not — and is escaped and anchored where it is rendered, so it pins itself and nothing else. What is refused is a value no issuer mints: empty, or carrying whitespace or a newline, which is how a value that arrived from the wrong place looks. Leave `audience` out entirely to fall back to `tenancy.audience`, rather than setting it to nothing." $principalName (toString $p.audience) $audienceShape) -}}
{{- end -}}
{{- $routes := $p.routes | default (list "metrics" "logs" "traces") -}}
{{- $rendersTraces := and (has "traces" $routes) (include "observability-proxy.tracesEnabled" $) -}}
{{- if not (or (has "metrics" $routes) (has "logs" $routes) $rendersTraces) -}}
{{- fail (printf "observability-proxy: principal %q's `routes` (%s) renders no route at all. Metrics and logs render whenever named; `traces` renders only when a trace store is enabled (independent of `tenancy.allowUnfilteredTraceReads`, checked next). A principal that may read nothing is written by leaving it out of `tenancy.principals` entirely, not by restricting it to nothing." $principalName (join ", " $routes)) -}}
{{- end -}}
{{- if $p.vmalertAPI -}}
{{- if not (has "metrics" $routes) -}}
{{- fail (printf "observability-proxy: principal %q sets `vmalertAPI: true` but its `routes` (%s) does not include `metrics`. The alerts and rules routes belong to a metrics reader: they are vmalert's view of the same store, and a principal with no metrics route has no business reading what its rules say." $principalName (join ", " $routes)) -}}
{{- end -}}
{{- if not $.Values.tenancy.allowUnfilteredAlertReads -}}
{{- fail (printf "observability-proxy: principal %q sets `vmalertAPI: true` but `tenancy.allowUnfilteredAlertReads` is not. vmalert's alerts and rules have no per-namespace or per-cluster concept to filter on, so this principal's own grant would NOT scope them: it would read every alert and every rule's expression and labels this install's metrics vmalert holds, cluster-wide. Set `tenancy.allowUnfilteredAlertReads: true` and record that, or remove `vmalertAPI`." $principalName) -}}
{{- end -}}
{{- if not (include "observability-proxy.effectiveEnabled" $ | fromYaml).vmalert -}}
{{- fail (printf "observability-proxy: principal %q sets `vmalertAPI: true` but `vmalert.enabled` is false. There is no vmalert for these routes to read." $principalName) -}}
{{- end -}}
{{- end -}}
{{- if and $p.metricsQueryOnly (not (has "metrics" $routes)) -}}
{{- fail (printf "observability-proxy: principal %q sets `metricsQueryOnly: true` but its `routes` (%s) does not include `metrics`. metricsQueryOnly narrows the metrics route to its two query paths; with no metrics route requested there is nothing for it to narrow, and the flag would mean nothing." $principalName (join ", " $routes)) -}}
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
{{- if and $t.vmAccessReader.enabled (has "traces" ($t.vmAccessReader.routes | default (list "metrics" "logs" "traces"))) -}}
{{- $anyPrincipalWantsTraces = true -}}
{{- end -}}
{{- if and $anyPrincipalWantsTraces (include "observability-proxy.tracesEnabled" .) -}}
{{- if not $t.allowUnfilteredTraceReads -}}
{{- fail "observability-proxy: a trace store is enabled and `tenancy.principals` is set, but `tenancy.allowUnfilteredTraceReads` is not. The proxy enforces a grant by substituting the principal's filter into the route it forwards on, and VictoriaTraces' Jaeger and Tempo select APIs accept NO query argument to substitute it into: their handlers take a tenant id from headers, `hidden_fields_filters` (which hides fields from a result, not rows) and `allow_partial_response`, and nothing else. There is no way through this proxy to give one principal a narrower view of traces than another, so the trace read route would be an unscoped route sitting beside two scoped ones and looking identical to them. Either turn the trace store off, or set `tenancy.allowUnfilteredTraceReads: true` and record that every principal who can reach the proxy reads every namespace's spans on every cluster. Metrics and logs are unaffected either way." -}}
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
{{- $placeholder := include (printf "observability-proxy.filterPlaceholder.%s" $signal) $ -}}
{{- $args := fromYamlArray (include (printf "observability-proxy.readQueryArgs.%s" $signal) $) -}}
{{- range $arg := (fromYamlArray (include (printf "observability-proxy.filterArgs.%s" $signal) $)) -}}
{{- $found := false -}}
{{- range $a := $args -}}
{{- if and (eq $a.name $arg) (has $placeholder $a.values) -}}
{{- $found = true -}}
{{- end -}}
{{- end -}}
{{- if not $found -}}
{{- fail (printf "observability-proxy: the %s read route does not carry %s=%s. A metrics route must pin BOTH `extra_filters` and `extra_filters[]`: vmselect ORs the array spelling in and vmauth drops a client argument only when the route sets the same name. vmauth applies a `vm_access` claim ONLY by substituting a placeholder into the route, so without it every principal's query reaches the store unfiltered while `defaultVMAccessClaim` beside it still states the grant. Nothing downstream reports that: the render succeeds, the install succeeds, and a query for one namespace returns exactly what it would have returned if the filter had been applied." $signal $arg $placeholder) -}}
{{- end -}}
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
{{- fail (printf "observability-proxy: `vmauth.extraArgs.mergeQueryArgs` names %q, which is the argument this chart enforces a principal's grant with. vmauth drops a client query argument that CLASHES with one the route already set, and that drop is the only thing stopping a reader from sending its own filter; `mergeQueryArgs` exempts an argument from it entirely. vmselect treats each `extra_filters` as an ALTERNATIVE and ORs them, so a caller adding an empty one reads every cluster and namespace — with the claim, the route and the filter all still exactly right. Remove it, or stop enforcing tenancy here." (trim $merged)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- $paths := concat
    (fromYamlArray (include "observability-proxy.readPaths.metrics" .))
    (fromYamlArray (include "observability-proxy.readPaths.logs" .))
    (fromYamlArray (include "observability-proxy.readPaths.traces" .))
    (fromYamlArray (include "observability-proxy.readPaths.alerts" .))
    (fromYamlArray (include "observability-proxy.writePaths.metrics" .))
    (fromYamlArray (include "observability-proxy.writePaths.logs" .))
    (fromYamlArray (include "observability-proxy.writePaths.traces" .))
-}}
{{- range $path := $paths -}}
{{- if regexMatch "^/(\\.\\*|\\*)?$" $path -}}
{{- fail (printf "observability-proxy: %q is not a route, it is every route — including the write endpoints and `/internal/*`. Name the paths." $path) -}}
{{- end -}}
{{- range $denied := $.Values.vmauth.deniedPaths -}}
{{- if regexMatch $denied $path -}}
{{- fail (printf "observability-proxy: the route %q matches the denied path %q. `/internal/*` carries the partition and snapshot APIs, and those endpoints have their own query-string auth keys which OVERRIDE `-httpAuth.*` — so a route to them through this proxy is a route around the stores' own authentication." $path $denied) -}}
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
{{- define "observability-proxy.validate.writers" -}}
{{- $t := .Values.tenancy -}}
{{- $shape := "^[a-z0-9]([a-z0-9-]*[a-z0-9])?$" -}}
{{- $names := dict -}}
{{- $clusters := dict -}}
{{- range $w := $t.writers -}}
{{- if hasKey $names $w.name -}}
{{- fail (printf "observability-proxy: writer %q appears twice in `tenancy.writers`. The second `VMUser` this chart renders would collide with the first's object name, and the operator drops all but one from vmauth's config with no error outside `status.currentSyncError` — a writer that silently stops being able to write." $w.name) -}}
{{- end -}}
{{- $_ := set $names $w.name true -}}
{{- if hasKey $w "cluster" -}}
{{- if not (regexMatch $shape (toString $w.cluster)) -}}
{{- fail (printf "observability-proxy: writer %q sets `cluster` to %q, which is not a plain name (%s). It is forced onto every series, log record and span this writer sends — interpolated into the query argument the same way a reader's grant is — so a value carrying `|` or `.*` would not pin the writer to one cluster, it would widen what it can claim to be." $w.name (toString $w.cluster) $shape) -}}
{{- end -}}
{{- if and $t.ownCluster (eq (toString $w.cluster) (toString $t.ownCluster)) -}}
{{- fail (printf "observability-proxy: writer %q sets `cluster: %s`, which is this install's own cluster (`tenancy.ownCluster`). Pinning a writer to the install's own cluster is the UNSCOPED writer's job — one with no `cluster` at all, writing to the store beside it exactly as charts/observability-emitters has always done. Leave `cluster` unset for a local writer, or name the remote cluster this writer actually runs on." $w.name (toString $w.cluster)) -}}
{{- end -}}
{{- if hasKey $clusters $w.cluster -}}
{{- fail (printf "observability-proxy: writers %q and %q both set `cluster: %s`. Two bearer tokens pinned to the same cluster identity is almost always a `name` that was meant to be different and was not; if two collectors genuinely write for the same cluster, this chart has no objection to it happening under two names — it only refuses the name collision above from going unnoticed as a cluster collision instead." (index $clusters $w.cluster) $w.name (toString $w.cluster)) -}}
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
{{- fail (printf "observability-proxy: `vmauth.extraArgs.mergeQueryArgs` names %q. That is the argument a writer's `cluster` pin is enforced with: vmauth drops a client query argument that clashes with one the route already set, and that drop is the only reason a writer cannot simply send its own %s and claim to be any cluster it likes. `mergeQueryArgs` exempts it from that rule entirely, and both VictoriaLogs/VictoriaTraces' `extra_fields` and VictoriaMetrics' `extra_label` keep the LAST value for a duplicate name — the writer's own, sent after this chart's, would win. Remove it, or stop pinning writers by cluster." (trim $merged) (trim $merged)) -}}
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
{{- define "observability-proxy.validate.alertReaders" -}}
{{- $observabilityStackEffective := include "observability-proxy.effectiveEnabled" . | fromYaml -}}
{{- $t := .Values.tenancy -}}
{{- if and $t.alertReaders (not $t.allowUnfilteredAlertReads) -}}
{{- fail "observability-proxy: `tenancy.alertReaders` is set but `tenancy.allowUnfilteredAlertReads` is not. vmalert's own `/api/v1/alerts` has no per-namespace or per-cluster concept to filter on, so a reader given this route reads every active alert this install's metrics vmalert is evaluating, cluster-wide. Set `tenancy.allowUnfilteredAlertReads: true` and record that every reader below sees every alert, or remove `tenancy.alertReaders`." -}}
{{- end -}}
{{- if and $t.alertReaders (not $observabilityStackEffective.vmalert) -}}
{{- fail "observability-proxy: `tenancy.alertReaders` is set but `vmalert.enabled` is false. There is no vmalert for this route to read." -}}
{{- end -}}
{{- range $r := $t.alertReaders -}}
{{- if and $r.alertmanager (not $observabilityStackEffective.alertmanager) -}}
{{- fail (printf "observability-proxy: `tenancy.alertReaders` entry %q sets `alertmanager: true` but `alertmanager.enabled` is false. There is no Alertmanager for this route to read." $r.name) -}}
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
{{- define "observability-proxy.validate.readers" -}}
{{- $t := .Values.tenancy -}}
{{- if $t.readers -}}
{{- $eff := include "observability-proxy.effectiveEnabled" . | fromYaml -}}
{{- if not $eff.vmauth -}}
{{- fail "observability-proxy: `tenancy.readers` is set but this install renders no vmauth (`mode` is not \"full\", or `vmauth.enabled` is false). A reader's bearer token authenticates against the proxy; with none, the reader is configured and unreachable. Set it on the install that runs the proxy, or empty the list." -}}
{{- end -}}
{{- $fullname := include "observability-proxy.fullname" . -}}
{{- $shape := "^[a-z0-9]([a-z0-9-]*[a-z0-9])?$" -}}
{{- $names := dict -}}
{{- range $r := $t.readers -}}
{{- if not (regexMatch $shape (toString $r.name)) -}}
{{- fail (printf "observability-proxy: `tenancy.readers` entry %q is not a DNS label (lower-case alphanumerics and hyphens, starting and ending with an alphanumeric)." (toString $r.name)) -}}
{{- end -}}
{{- if hasKey $names $r.name -}}
{{- fail (printf "observability-proxy: reader %q appears twice in `tenancy.readers`. The second `VMUser` this chart renders would collide with the first's object name, and the operator drops all but one from vmauth's config with no error outside `status.currentSyncError` — a reader that silently stops being able to read." $r.name) -}}
{{- end -}}
{{- $_ := set $names $r.name true -}}
{{- if gt (len (printf "%s-reader-%s" $fullname $r.name)) 63 -}}
{{- fail (printf "observability-proxy: reader %q makes the VMUser name %q longer than 63 characters, and a truncated name can collide with another reader's. Shorten it." $r.name (printf "%s-reader-%s" $fullname $r.name)) -}}
{{- end -}}
{{- range $p := $t.principals -}}
{{- $pn := $p.name | default $p.group | default "" -}}
{{- $slug := regexReplaceAll "[^a-z0-9]+" (lower $pn) "-" | trimAll "-" | trunc 40 | trimSuffix "-" -}}
{{- if eq $slug (printf "reader-%s" $r.name) -}}
{{- fail (printf "observability-proxy: reader %q and principal %q would render the same VMUser object name. Rename one of them." $r.name $pn) -}}
{{- end -}}
{{- end -}}
{{- if not $r.grants -}}
{{- fail (printf "observability-proxy: reader %q has no grants. A reader with no grant would read nothing, or with a careless rewrite everything; a reader that may read everything on a cluster says so with `allNamespaces: true`." $r.name) -}}
{{- end -}}
{{- $clusters := dict -}}
{{- range $g := $r.grants -}}
{{- if not (regexMatch $shape (toString $g.cluster)) -}}
{{- fail (printf "observability-proxy: reader %q has cluster %q, which is not a plain name (%s). Names are interpolated into a filter expression, so one carrying `|`, `)` or `.*` would widen the grant." $r.name (toString $g.cluster) $shape) -}}
{{- end -}}
{{- if hasKey $clusters $g.cluster -}}
{{- fail (printf "observability-proxy: reader %q is granted cluster %q twice. Merge them, or one grant is silently ignored." $r.name $g.cluster) -}}
{{- end -}}
{{- $_ := set $clusters $g.cluster true -}}
{{- if and $g.allNamespaces $g.namespaces -}}
{{- fail (printf "observability-proxy: reader %q grants cluster %q with both `allNamespaces` and a `namespaces` list. One of them is wrong, and guessing which is how a grant quietly widens." $r.name $g.cluster) -}}
{{- end -}}
{{- if and (not $g.allNamespaces) (not $g.namespaces) -}}
{{- fail (printf "observability-proxy: reader %q grants cluster %q with neither `namespaces` nor `allNamespaces`. An empty grant is refused rather than read as \"everything\": say `allNamespaces: true` if that is meant." $r.name $g.cluster) -}}
{{- end -}}
{{- range $ns := ($g.namespaces | default list) -}}
{{- if not (regexMatch $shape (toString $ns)) -}}
{{- fail (printf "observability-proxy: reader %q grants namespace %q, which is not a plain name (%s). It would be interpolated into a filter expression, where `|` or `.*` widens the grant." $r.name (toString $ns) $shape) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- /* The grant is enforced by a query argument the route sets; a client one that is merged instead of dropped would win. */ -}}
{{- range $arg, $value := ($.Values.vmauth.extraArgs | default dict) -}}
{{- if eq (toString $arg) "mergeQueryArgs" -}}
{{- range $merged := (splitList "," (toString $value)) -}}
{{- if has (trim $merged) (list "extra_filters" "extra_filters[]") -}}
{{- fail (printf "observability-proxy: `vmauth.extraArgs.mergeQueryArgs` names %q while `tenancy.readers` is set. A reader's grant is enforced as that query argument on its route, and vmauth drops a client's clashing one only when it is NOT merged: with it merged the caller's own filter is sent beside the grant's, and a caller who may send its own filter may send a wider one. Remove it." (trim $merged)) -}}
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
{{- define "observability-proxy.validate.routeOverlap" -}}
{{- $groups := list
    (dict "kind" "read" "signal" "metrics" "paths" (fromYamlArray (include "observability-proxy.readPaths.metrics" .)))
    (dict "kind" "read" "signal" "logs" "paths" (fromYamlArray (include "observability-proxy.readPaths.logs" .)))
    (dict "kind" "read" "signal" "traces" "paths" (fromYamlArray (include "observability-proxy.readPaths.traces" .)))
    (dict "kind" "write" "signal" "metrics" "paths" (fromYamlArray (include "observability-proxy.writePaths.metrics" .)))
    (dict "kind" "write" "signal" "logs" "paths" (fromYamlArray (include "observability-proxy.writePaths.logs" .)))
    (dict "kind" "write" "signal" "traces" "paths" (fromYamlArray (include "observability-proxy.writePaths.traces" .)))
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
{{- fail (printf "observability-proxy: the %s route %q on the %s store is declared before the %s store's %q and MATCHES it, so the %s store would answer every request meant for the %s store and the %s store would be unreachable. Narrow the first path; the store's own endpoint list is the right source for it." $earlier.kind $pattern $earlier.signal $later.signal $path $earlier.signal $later.signal $later.signal) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

{{/*
`tenancy.authz`: the Envoy external authorisation service in front of vmauth.

Every refusal here is a way the arrangement renders, installs and then does
not do what it says — and most of them fail in the unsafe direction or
silently: a reader that trusts a service that is not there (everyone is
refused), a gateway that cannot reach the service (everyone is refused, or
with a fail-open policy, nobody is checked), a principal whose narrower
routes the single minted claim would widen.
*/}}
{{- define "observability-proxy.validate.tenancyAuthz" -}}
{{- $t := .Values.tenancy -}}
{{- $a := $t.authz -}}
{{- if and $a.retireGroupReaders (not $a.enabled) -}}
{{- fail "observability-proxy: `tenancy.authz.retireGroupReaders` is set but `tenancy.authz.enabled` is not. The person readers would be dropped and nothing would grant people access in their place. Turn `tenancy.authz` on, or leave `retireGroupReaders` off." -}}
{{- end -}}
{{- if $a.enabled -}}
{{- $eff := include "observability-proxy.effectiveEnabled" . | fromYaml -}}
{{- if not $eff.vmauth -}}
{{- fail "observability-proxy: `tenancy.authz.enabled` is set but this install renders no vmauth (`mode` is not \"full\", or `vmauth.enabled` is false). The service exists to feed vmauth's vm_access reader; with no proxy it would be configured and unreachable." -}}
{{- end -}}
{{- if not $t.vmAccessReader.enabled -}}
{{- fail "observability-proxy: `tenancy.authz.enabled` is set but `tenancy.vmAccessReader.enabled` is not. The service answers with a token whose `vm_access` claim is the person's grant, and the vm_access reader is the only VMUser that applies one; without it vmauth would find no user for the minted token and refuse every person." -}}
{{- end -}}
{{- $upstream := $a.upstreamIssuer | default $t.issuerUrl -}}
{{- if not $upstream -}}
{{- fail "observability-proxy: `tenancy.authz.enabled` is set but neither `tenancy.authz.upstreamIssuer` nor `tenancy.issuerUrl` is. There is no identity provider to verify a person's token against, and a service that trusts an unverified token grants whatever its claims say." -}}
{{- end -}}
{{- if not (regexMatch "^https?://[^/?#@\\s]+$" (toString $upstream)) -}}
{{- fail (printf "observability-proxy: the upstream issuer %q is not a bare http(s) URL (no path, query or credentials). The service compares it to a token's `iss` as a string and fetches `/.well-known/openid-configuration` from it." (toString $upstream)) -}}
{{- end -}}
{{- $person := (include "observability-proxy.authz.personAudiences" . | fromYaml).audiences -}}
{{- if not $person -}}
{{- fail "observability-proxy: `tenancy.authz.enabled` is set but there is no person audience: `tenancy.authz.personAudiences` is empty and so is `tenancy.audience`. No token would be exchanged and every person would be refused." -}}
{{- end -}}
{{- range $aud := $person -}}
{{- if not (regexMatch "^\\S+$" (toString $aud)) -}}
{{- fail (printf "observability-proxy: `tenancy.authz.personAudiences` has %q, which is not an identifier (blank, or carrying whitespace)." (toString $aud)) -}}
{{- end -}}
{{- end -}}
{{- if not (regexMatch "^\\S+$" (toString $a.mintedAudience)) -}}
{{- fail (printf "observability-proxy: `tenancy.authz.mintedAudience` is %q, which is not an identifier (blank, or carrying whitespace). The vm_access reader pins it in `aud`." (toString $a.mintedAudience)) -}}
{{- end -}}
{{- if has $a.mintedAudience $person -}}
{{- fail (printf "observability-proxy: `tenancy.authz.mintedAudience` %q is also a person audience. A minted token would be taken for a person's if it ever came back, and the vm_access reader could not tell the two apart by audience." (toString $a.mintedAudience)) -}}
{{- end -}}
{{- if and $t.vmAccessReader.audience (ne (toString $t.vmAccessReader.audience) (toString $a.mintedAudience)) -}}
{{- fail (printf "observability-proxy: `tenancy.vmAccessReader.audience` is %q but `tenancy.authz` is on, so the reader pins `tenancy.authz.mintedAudience` (%q). Two audiences for one reader is a setting that silently does not apply; clear `vmAccessReader.audience`." (toString $t.vmAccessReader.audience) (toString $a.mintedAudience)) -}}
{{- end -}}
{{- if not (regexMatch "^[0-9]+(s|m|h)$" (toString $a.tokenTTL)) -}}
{{- fail (printf "observability-proxy: `tenancy.authz.tokenTTL` is %q: write whole seconds, minutes or hours (30s, 5m, 1h)." (toString $a.tokenTTL)) -}}
{{- end -}}
{{- $ttl := include "observability-proxy.durationSeconds" $a.tokenTTL | int -}}
{{- if or (lt $ttl 30) (gt $ttl 3600) -}}
{{- fail (printf "observability-proxy: `tenancy.authz.tokenTTL` %q is outside 30s..1h. Shorter and every query pays for a signature; longer and a person's removal from a group is honoured that much later." (toString $a.tokenTTL)) -}}
{{- end -}}
{{- /*
The people's principals: the ones the service will union. A principal that
narrows its routes cannot be honoured: the service mints ONE claim per
person and the vm_access reader gives every claim the same routes, so the
narrowing would be silently dropped and the person would read more than the
principal says. Refused, not guessed.
*/ -}}
{{- $mine := (include "observability-proxy.authz.principals" . | fromYaml).principals -}}
{{- if not $mine -}}
{{- fail "observability-proxy: `tenancy.authz.enabled` is set but no entry of `tenancy.principals` is a person's principal (its audience, its own or `tenancy.audience`, is not in the person audiences). The service would have nothing to grant and would refuse everyone." -}}
{{- end -}}
{{- range $p := $t.principals -}}
{{- $aud := $p.audience | default $t.audience -}}
{{- if has $aud $person -}}
{{- $label := $p.name | default $p.group -}}
{{- if or $p.routes $p.metricsQueryOnly $p.vmalertAPI -}}
{{- fail (printf "observability-proxy: principal %q is a person's principal but sets `routes`, `metricsQueryOnly` or `vmalertAPI`. With `tenancy.authz` on, the service mints one claim per person and the vm_access reader gives every claim the same routes, so the narrowing would be dropped and the person would read more than this principal says. Give a narrower population its own audience (and keep it out of `personAudiences`), or leave the narrowing off." $label) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if $a.retireGroupReaders -}}
{{- if not $a.securityPolicy.enabled -}}
{{- fail "observability-proxy: `tenancy.authz.retireGroupReaders` is set but `tenancy.authz.securityPolicy.enabled` is not. This chart cannot see a SecurityPolicy you render yourself, so it cannot know that the route people read through is behind tenancy-authz; retiring the person readers on a route without the service would lock every person out. Let the chart render the policy (`securityPolicy.enabled: true`, `targetRefs` naming the route), or leave `retireGroupReaders` off." -}}
{{- end -}}
{{- end -}}
{{- if eq $a.signingKey.source "existingSecret" -}}
{{- if not $a.signingKey.existingSecret -}}
{{- fail "observability-proxy: `tenancy.authz.signingKey.source` is existingSecret but `tenancy.authz.signingKey.existingSecret` is empty. Name the Secret holding the key as the file `privateKey`, or use `source: eso`." -}}
{{- end -}}
{{- else if $a.signingKey.existingSecret -}}
{{- fail "observability-proxy: `tenancy.authz.signingKey.existingSecret` is set but `tenancy.authz.signingKey.source` is eso, so the Secret would be ignored and a generated one used. Set `source: existingSecret`." -}}
{{- end -}}
{{- if $a.securityPolicy.enabled -}}
{{- if not $a.securityPolicy.targetRefs -}}
{{- fail "observability-proxy: `tenancy.authz.securityPolicy.enabled` is set but `tenancy.authz.securityPolicy.targetRefs` is empty. The SecurityPolicy attaches the service to the HTTPRoute(s) people read through, and this chart does not render the route, so it cannot guess which. Name them, or set `securityPolicy.enabled: false` and render the policy yourself (extAuth.http to the Service this chart renders, path `/check`, headersToBackend `authorization`, failOpen false)." -}}
{{- end -}}
{{- range $i, $ref := $a.securityPolicy.targetRefs -}}
{{- if not $ref.name -}}
{{- fail (printf "observability-proxy: `tenancy.authz.securityPolicy.targetRefs[%d]` has no name." $i) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if .Values.networkPolicy.enabled -}}
{{- if not $a.networkPolicy.gatewayFrom -}}
{{- fail "observability-proxy: `tenancy.authz.enabled` and `networkPolicy.enabled` are set but `tenancy.authz.networkPolicy.gatewayFrom` is empty. The service's NetworkPolicy denies everything it does not name, and Envoy's pods are not in this release; the service fails closed, so a policy that hides it from the gateway is an outage for every person. Name the Envoy proxy pods (NetworkPolicyPeer objects)." -}}
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
{{- define "observability-proxy.validate.resources" -}}
{{- $sites := list
    (dict "key" "vmauth.resources" "value" .Values.vmauth.resources)
-}}
{{- if .Values.tenancy.authz.enabled -}}
{{- $sites = append $sites (dict "key" "tenancy.authz.resources" "value" .Values.tenancy.authz.resources) -}}
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
templates drop a null (`observability-proxy.resources`), so a values file
written when those carried a default CPU limit keeps meaning "no limit".
*/ -}}
{{- range $side, $m := dict "requests" $requests "limits" $limits -}}
{{- range $res, $q := $m -}}
{{- if and (kindIs "invalid" $q) (not (has $site.key (list "vmauth.resources" "vmalert.resources" "alertmanager.resources" "karma.resources" "tenancy.authz.resources"))) -}}
{{- fail (printf "observability-proxy: %s.%s.%s is null. Helm deletes a null from this chart's own values, but passes it through unchanged to a vendored subchart's, and the API server then reads it as a quantity of 0 and refuses the object. A default on this component cannot be removed through values: under `resources.policy: burstable`, give it a whole number of cores well above the request instead (the node's core count leaves it effectively unthrottled)." $site.key $side (toString $res)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- if eq $policy "burstable" -}}
{{- if or (not $requests.cpu) (not $requests.memory) (not $limits.memory) -}}
{{- fail (printf "observability-proxy: %s does not set requests.cpu, requests.memory and limits.memory, which `resources.policy: burstable` still requires. A pod with no requests is BestEffort and is evicted before anything else; and with no memory limit VictoriaMetrics sizes its caches from the NODE's memory and is OOM-killed by it. Only the CPU limit may be left out." $site.key) -}}
{{- end -}}
{{- if and $limits.cpu (not (regexMatch "^[0-9]+$" (toString $limits.cpu))) -}}
{{- fail (printf "observability-proxy: %s.limits.cpu is %q under `resources.policy: burstable`. The request may be fractional, the limit may not: the VictoriaMetrics binaries size their thread pool from the cgroup CPU QUOTA — the limit — and round it DOWN, so 1500m buys exactly one thread. Write a whole number of cores, or leave the CPU limit out." $site.key (toString $limits.cpu)) -}}
{{- end -}}
{{- if ne (toString $requests.memory) (toString $limits.memory) -}}
{{- fail (printf "observability-proxy: %s has requests.memory %q and limits.memory %q. Under `resources.policy: burstable` too they must be equal: a pod whose memory use cannot exceed its request stays out of the kubelet's first eviction tier under memory pressure, which is what keeps a Burstable store from being evicted first." $site.key (toString $requests.memory) (toString $limits.memory)) -}}
{{- end -}}
{{- else -}}
{{- if or (not $requests.cpu) (not $limits.cpu) (not $requests.memory) (not $limits.memory) -}}
{{- fail (printf "observability-proxy: %s does not set both requests and limits for cpu and memory. Leaving one side out is how a store ends up in a lower QoS class and is evicted first under node pressure, which is the moment it is most needed." $site.key) -}}
{{- end -}}
{{- if not (regexMatch "^[0-9]+$" (toString $requests.cpu)) -}}
{{- fail (printf "observability-proxy: %s.requests.cpu is %q. The VictoriaMetrics binaries size their thread pool from the cgroup CPU quota and round it DOWN, so a fractional value such as 1500m buys exactly one thread and pays for 1.5. Write a whole number of cores: \"1\", \"2\", \"4\" — or, for an estate that measured its components at a few millicores, set `resources.policy: burstable`, which allows a fractional request." $site.key (toString $requests.cpu)) -}}
{{- end -}}
{{- if ne (toString $requests.cpu) (toString $limits.cpu) -}}
{{- fail (printf "observability-proxy: %s has requests.cpu %q and limits.cpu %q. They must be equal: a component whose requests are below its limits is Burstable, and Burstable pods are evicted before Guaranteed ones. An estate that measured otherwise and accepts that trade sets `resources.policy: burstable`." $site.key (toString $requests.cpu) (toString $limits.cpu)) -}}
{{- end -}}
{{- if ne (toString $requests.memory) (toString $limits.memory) -}}
{{- fail (printf "observability-proxy: %s has requests.memory %q and limits.memory %q. They must be equal, for the same reason the CPUs must." $site.key (toString $requests.memory) (toString $limits.memory)) -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}
{{- end -}}

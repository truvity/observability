# Changelog

Prose bullets, written for the consumer: what changes in the render, what
must be done first, and whether a default moved. Newest first.

A version missing from this file changed nothing for a consumer — it is a
patch cut for dependency bumps alone, and its GitHub Release lists them.

## Unreleased

- **`pkg/tenancy` and `charts/observability-stack`** — the logs filter
  names the field the log store actually has. Until now both rendered the
  same string for both signals, so a reader querying logs through the
  proxy was filtered on `tenant` — a field the log path does not have and
  cannot have, because vlagent can rename no field and a namespace label
  arrives as `kubernetes.namespace_labels.<key>`. The query did not fail;
  it returned nothing, which reads as "my service logged nothing".
  **Breaking, and deliberately so:** `tenancy.logsTenantField` and
  `tenancy.logsEnvField` on the chart, `LogsTenantField` and
  `LogsEnvField` on `tenancy.Config`, are now required whenever there is a
  principal, and there is no default — every default anyone would write is
  right on one estate and silently wrong on the next. With
  `charts/observability-emitters` they are
  `kubernetes.namespace_labels.<tenancy.namespaceLabels.project>` and
  `tenancy.envLabel`, which that chart already refuses to render without.
  Two further changes follow from LogsQL rather than from taste: the field
  name is quoted and held to a field shape (`^[a-zA-Z0-9_][a-zA-Z0-9_./-]*$`)
  rather than to the plain-name shape a tenant is held to, since a real
  field name carries dots and a slash; and a principal now gets **one**
  stream filter with its grants as `or` alternatives instead of one entry
  per grant, because VictoriaLogs AND-s every `extra_stream_filters`
  argument it is given — two entries naming two environments intersected
  in nothing, so the principal with the most access got the emptiest
  screen. The metrics and traces paths are unchanged. See docs/safety.md.

- **`charts/observability-emitters`** — per-cluster collection: vmagent as
  a `VMAgent` the operator reconciles, vlagent from the vendor's own
  DaemonSet chart, and an OpenTelemetry gateway this chart renders itself.
  Each is optional, each replicates to every destination it is given with
  its own on-disk buffer, and each stamps `tenant` and `env` from the
  **namespace's** labels — an application that sets them itself has them
  overwritten. Install `charts/observability-crds` first: the chart
  renders `PodMonitor` objects, and on a cluster without those CRDs every
  other chart's monitor template renders nothing at all, silently, with a
  successful sync. Twenty-seven refusals, each with a fixture, and the three
  worth knowing before you write the values file: `overrideHonorLabels`
  cannot be turned off, because a target that exports its own `tenant`
  label would otherwise choose its own tenant; `remoteWrite.shardByURL` is
  refused outright, because it splits the series between a redundant pair
  instead of replicating to both and every query still answers with half
  of every result missing; and a buffer on an emptyDir is refused for all
  three emitters, including the log agent's, where the same volume holds
  the checkpoint that stops it re-reading every container log from the
  beginning on each rollout. Five values have no default and are asked for
  rather than guessed — `tenancy.env`, `tenancy.fallbackTenant`,
  `tenancy.namespaceLabels.project`, `writeCredentials.secretName` and a
  destination list per emitter — because each of them renders, runs and
  reports healthy when it is wrong. **One thing to carry out of the
  chart:** on the log path the tenancy stream field is
  `kubernetes.namespace_labels.<your project label key>` and **not**
  `tenant`, because vlagent cannot rename a field; a proxy filtering on
  `tenant` against those streams returns an empty result rather than an
  error. See docs/safety.md.
- **`charts/observability-stack`** — one install of the store: the
  VictoriaMetrics family from the vendor's own pinned charts, with the
  proxy, the two vmalerts, Alertmanager, the network policies and the
  backups this chart renders itself. Reads go through vmauth, which
  verifies the caller's token against an OIDC issuer and injects the
  filters that token is entitled to; `pkg/tenancy` renders the same
  principals into the claim an issuer mints, and a test compares the two
  so they cannot drift. Single-replica: `ha` is accepted and refuses fewer
  than two zones, and the zone-redundant behaviour lands in a later
  release. Install `charts/observability-crds` first and have cert-manager
  present — the operator's own `crds.enabled` is off here, and its webhook
  certificate comes from cert-manager rather than from a self-signed CA
  the chart would regenerate on every upgrade. Seventeen refusals, each
  with a fixture: a retention without a unit (a bare number is months), the
  two disk guards that are mutually exclusive at the binary, a fractional
  CPU (the store rounds it down and buys one thread), an `enterprise` image
  tag, a licence flag, a vmauth below v1.152.0, a Grafana datasource
  without `oauthPassThru`, and the rest in docs/safety.md. Three values are
  written twice because Helm cannot compute a subchart's values; the chart
  refuses to render when a pair disagrees.
- **`charts/observability-crds`** — the CustomResourceDefinitions this stack
  needs, as a release of their own: the VictoriaMetrics operator's, and the
  `PodMonitor`, `ServiceMonitor`, `ScrapeConfig` and `Probe` kinds every
  component authors its scrape objects in. Install it at a wave ahead of
  the stack with `prune: false` and `ServerSideApply=true`, and turn the
  operator chart's own `crds.enabled` off — Helm never upgrades a CRD it
  installed from a chart's `crds/` directory, so a set with two owners is
  a schema that drifts behind the controller reading it. Both upstreams are
  pinned; every render ends with the kinds it carries and the version each
  one stores, which is what a bump is reviewed against. Install it before
  any chart that offers a monitor template and before switching such a
  value on: a chart whose monitor is gated on
  `.Capabilities.APIVersions.Has "monitoring.coreos.com/v1"` renders
  nothing when the kind is absent and still reports a successful install.
- **`charts/platform-alerts`** — the rules that fire when something has
  stopped working while everything still looks green: a CronJob no longer
  being scheduled, a store whose write path has died, a volume that was
  never mounted, a store approaching its own read-only limit. There is no
  default store list: the chart refuses to render until the counters are
  named, because a guessed metric name renders cleanly and then never
  fires.
- **`pkg/tenancy`** — one input, two shapes: a vmauth configuration and
  the `vm_access` claim an issuer mints. A tenant or environment name
  outside the plain-name shape is refused rather than escaped, because a
  name carrying `|` or `.*` would widen the grant it appears in.

# Changelog

Prose bullets, written for the consumer: what changes in the render, what
must be done first, and whether a default moved. Newest first.

A version missing from this file changed nothing for a consumer — it is a
patch cut for dependency bumps alone, and its GitHub Release lists them.

## Unreleased

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

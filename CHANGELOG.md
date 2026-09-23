# Changelog

Prose bullets, written for the consumer: what changes in the render, what
must be done first, and whether a default moved. Newest first.

A version missing from this file changed nothing for a consumer — it is a
patch cut for dependency bumps alone, and its GitHub Release lists them.

## Unreleased

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

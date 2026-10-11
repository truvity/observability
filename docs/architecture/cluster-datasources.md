# Per-cluster datasources (withdrawn)

Decision record for restructure R2.2. The first slice, per-cluster
datasources behind per-cluster static bearer tokens, was merged and then
withdrawn before any release. This page keeps the reasoning so the idea is
not rebuilt by accident. It follows [observability-cluster.md](observability-cluster.md)
and is the platform side of the logical datasources in
[contracts.md](../contracts.md).

Status: withdrawn (decision E2 A). Nothing from the slice shipped in a
release: `tenancy.clusterDatasources` (observability-stack) and
`datasources.*` (observability-cluster) are gone, and no render moved.

## What was withdrawn

- `observability-stack` `tenancy.clusterDatasources[]`: one bearer-token
  `VMUser` per cluster, with the cluster forced as a literal `extra_filters` /
  `extra_stream_filters` on the metrics and logs routes.
- `observability-cluster` `datasources.{enabled,proxyUrl,metricsType,clusters[]}`:
  `GrafanaDatasource` `<cluster>-metrics` and `<cluster>-logs`, each holding
  that cluster's token.

## Why

- In Grafana OSS any signed-in user can query any datasource. A per-cluster
  datasource therefore only controls what a dashboard presents, never what a
  person may read: the person can open Explore on `<other>-metrics`.
- People already reach the stores with their own forwarded sluis token: every
  datasource uses `oauthPassThru`, and vmauth verifies that token. The
  isolation that matters is already decided there, per person.
- A static per-cluster token isolates nothing that the person's own token does
  not, and adds a long-lived credential per cluster to mint, store, rotate and
  leak.

Isolation stays in vmauth, driven by the person's token.

## Chosen direction

- **Per-person filters from the viewer's sluis token** (E1 A, decided). The
  namespace rules stay sluis groups. Each grant group carries a `vm_access`
  fragment through sluis's existing `claims.<group>` policy block (config only,
  no sluis code change), and the token's `vm_access` claim is the union of the
  fragments of the person's groups. Per-grant-set readers are a fallback only
  if sluis cannot carry the fragments.
- **One datasource set per store, as today.** No per-cluster datasource copies.
- **Cluster Grafana operator signs in with a short-lived sluis workload JWT**,
  through `client.useKubeAuth` and a token sidecar (E3 A), not a static service
  account token.
- **Operator account role None, plus Edit on its own folder** (E4 A). It can
  rewrite its own dashboards and nothing else.
- **Traces are fleet-wide** (D3): VictoriaTraces takes no filter argument, so
  there is no per-cluster trace route, and the trace datasource is shared.
- **Alerting runs in vmalert**, not in Grafana.

## Open points

- Whether the fragments follow the token's audience scoping. Asked of the
  sluis owner.
- vmauth ANDs `logs_extra_stream_filters`, so a person with several namespaces
  must get ONE stream filter that lists them all. Building that filter from
  the claim is still to be proven.

## What stays true

- A dashboard carries no cluster filter of its own beyond what the viewer's
  token allows; the same JSON works on every cluster.
- Fleet-wide dashboards stay central.
- A compromised cluster can rewrite only its own folder's dashboards (the
  folder permission above).

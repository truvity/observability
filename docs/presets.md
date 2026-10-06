# Chart presets

A preset is a values file shipped inside a chart, in `charts/<chart>/presets/<name>.yaml`.
A consumer lists it before its own values:

```yaml
# Argo CD Application source
helm:
  valueFiles:
    - presets/scheduling-durable.yaml
  valuesObject: {...}      # wins over the preset, key by key
```

or `helm install ... -f charts/<chart>/presets/<name>.yaml -f my-values.yaml`.

## Why a file and not a default

A subchart's values are read before any template runs, so a chart cannot
compute them from its own. A value that is the same on every estate of a given
shape (the node pool the stores sit on, the metric names a family of stores
exports) therefore used to be written out again in every consumer. A preset
carries it once. It is not a default: nothing reads it unless it is listed, so a
consumer that does not list one renders exactly what it rendered before.

Rules the presets keep:

- Maps merge, lists are replaced. A preset that sets a list (`notifications.drop`,
  `stores`, a collector's `remoteWrite`) is replaced whole by your own value for
  that key; copy the entries you still want.
- A preset never names an estate: no host, no account, no cluster. Where a value
  needs one (a write URL), the preset leaves it to you.
- Each preset is exercised by a case in `tests/cases/<chart>/*-preset`
  (`presets` file listing it) and compared with the same values written out in
  full (`*-explicit`): `tests/presets_test.go`.

## The presets

| Chart | Preset | What it sets |
|---|---|---|
| observability-stack | `operator-only` | `mode: operator-only` and every explicit `false` the mode demands (stores, sync Job, Grafana, backup, self-alerts) |
| observability-stack | `self-alerts-victoria` | the metric names of every `selfAlerts` rule that has an upstream series (the rules still need `selfAlerts.enabled`) |
| observability-stack | `rules-no-apiserver` | `kube-apiserver-availability.rules` off (nothing scrapes a managed control plane) |
| observability-stack | `rules-on-demand-nodes` | `KubeCPUOvercommit`, `KubeMemoryOvercommit` off (nodes are created on demand) |
| observability-stack | `notifications-drop-vendored` | `notifications.drop` for the vendored groups (general, kubernetes-*, node-network, vm-health, vmoperator, vmsingle) |
| observability-stack | `scheduling-durable` | stores and backup Jobs on a `durable` pool (selector `karpenter.sh/nodepool: durable`, tolerations `durable`, `arch`) |
| observability-stack | `scheduling-arm64` | the operator on arm64 nodes |
| observability-emitters | `local-write` | `writeCredentials`, metrics and OTLP destinations, and the log agent's mounts and `remoteWrite` for a stack installed as `observability-stack` in the `observability` namespace |
| observability-emitters | `logs-collector-remote` | the log agent's token and CA mounts for the `remote` form |
| observability-emitters | `scheduling-durable`, `scheduling-arm64` | the gateway and CloudWatch receiver on `durable`; the metrics agent and blackbox exporter on arm64 |
| observability-grafana, observability-rum | `scheduling-arm64` | arm64 nodes |
| platform-alerts | `stores-victoria` | `stores` for the metrics and log store of a VictoriaMetrics family install |
| platform-alerts | `keep-cluster-label` | `keepClusterLabel: true` on the cluster-scoped groups, for a store that holds several clusters |

The log agent's `collector.extraFields` (cluster and environment as JSON) is not
in a preset: it mirrors `tenancy` in your values and Helm cannot derive it. The
chart checks the two agree.

## A trap when you drop a value for a preset

A preset's value comes from a values file, so your own values must not leave an
EMPTY parent key behind when you delete the value the preset now carries. A key
with no children is null, and Helm treats a null map as "delete this key's
chart defaults": the chart's own defaults under it are gone, silently. Remove the
parent key together with its last child.

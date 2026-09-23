# Reference

Every value, its default, and what it does.

## `charts/platform-alerts`

### Top level

| Value | Type | Default | What it does |
|---|---|---|---|
| `nameOverride` | string | `""` | Replaces the chart name in the rendered object's name. |
| `fullnameOverride` | string | `""` | Replaces the rendered object's name entirely. |
| `ruleLabels` | map | `{}` | Labels on the `VMRule` object. vmalert selects rule objects by these; an install running more than one vmalert must set them. |
| `commonLabels` | map | `{}` | Labels added to every rule. The Alertmanager routing tree reads these. `severity` always wins over a common label of the same name. |
| `runbookBaseUrl` | string | `""` | Base for each alert's `runbook_url`; the alert name is appended. Empty renders no annotation at all rather than a blank one. |
| `interval` | duration | `1m` | How often vmalert evaluates these groups. |
| `namespaceSelector` | regex | `".*"` | Objects in namespaces that do not match are ignored by the Kubernetes-object rules. |
| `stores` | list | `[]` | One entry per store process. **Required** whenever a group that watches stores is enabled. |

### `stores[]`

| Field | Required | What it does |
|---|---|---|
| `name` | yes | Labels the alert. Not a Kubernetes object name. |
| `rowsMetric` | when `groups.writePath.enabled` | The counter the write-path deadman watches. |
| `freeSpaceMetric` | with `freeSpaceLimitMetric` | The store's own exported free-space gauge. |
| `freeSpaceLimitMetric` | with `freeSpaceMetric` | The store's own exported limit at which it stops accepting writes. |

A store with neither free-space field gets no read-only rule. Setting one
without the other is refused.

For the VictoriaMetrics family:

```yaml
stores:
  - name: metrics
    rowsMetric: vm_rows_inserted_total
    freeSpaceMetric: vm_free_disk_space_bytes
    freeSpaceLimitMetric: vm_free_disk_space_limit_bytes
  - name: logs
    rowsMetric: vl_rows_ingested_total
```

Read these off your own stores' `/metrics` rather than copying them: they
differ between versions, and a wrong name is a rule that never fires.

### `groups.backups`

| Value | Type | Default | What it does |
|---|---|---|---|
| `enabled` | bool | `true` | Renders the group. |
| `maxSuccessAge` | duration | `26h` | Age of the last successful run past which `CronJobNotSucceeding` fires. A daily job's schedule plus slack. |
| `severity` | enum | `critical` | Severity of `CronJobNotSucceeding`. |
| `failedSeverity` | enum | `warning` | Severity of `BackupJobFailed`. |

Alerts: `CronJobNotSucceeding`, `BackupJobFailed`.

### `groups.writePath`

| Value | Type | Default | What it does |
|---|---|---|---|
| `enabled` | bool | `true` | Renders one deadman per store. Requires `stores`. |
| `window` | duration | `5m` | Window over which the ingestion rate is measured. |
| `for` | duration | `10m` | How long the rate must stay at zero before firing. |
| `severity` | enum | `critical` | Severity of `WritePathDead`. |

Alerts: `WritePathDead`, one per store, labelled `store`.

### `groups.volumes`

| Value | Type | Default | What it does |
|---|---|---|---|
| `enabled` | bool | `true` | Renders the group. |
| `minCapacityRatio` | number, 0 < x ≤ 1 | `0.5` | Fires when the mounted filesystem is smaller than this fraction of the claim. |
| `severity` | enum | `critical` | Severity of `VolumeSmallerThanClaimed`. |

Alerts: `VolumeSmallerThanClaimed`.

### `groups.storeLimits`

| Value | Type | Default | What it does |
|---|---|---|---|
| `enabled` | bool | `true` | Renders one rule per store that exports both free-space metrics. Requires `stores`. |
| `headroomFactor` | number, > 1 | `2` | Fires while free space is still this multiple of the store's own read-only limit. |
| `severity` | enum | `warning` | Severity of `StoreApproachingReadOnly`. |

Alerts: `StoreApproachingReadOnly`, one per qualifying store, labelled
`store`.

## Published artifacts

| Artifact | Where |
|---|---|
| `platform-alerts` | `oci://ghcr.io/truvity/charts/platform-alerts` |

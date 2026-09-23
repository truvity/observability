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

## `pkg/tenancy`

`go get github.com/truvity/observability`

### `Config`

| Field | Required | What it does |
|---|---|---|
| `ClaimName` | yes | The token claim carrying the caller's groups, e.g. `groups`. |
| `Principals` | yes | One entry per named population. |
| `TenantLabel` | no, `tenant` | The label key the collectors stamp with the tenant. |
| `EnvLabel` | no, `env` | The label key the collectors stamp with the environment. |
| `MetricsBackend`, `LogsBackend` | for `RenderVMAuth` | Where the proxy forwards. |
| `TracesBackend` | no | Omitted renders no trace route. |

### `Principal` and `Grant`

| Field | Required | What it does |
|---|---|---|
| `Principal.Group` | yes | Matched against the claim. Not a display name or an address. |
| `Principal.Grants` | yes | What this group may read. A principal that may read nothing is written by leaving it out. |
| `Grant.Env` | yes | One environment. Granting the same environment twice to one principal is refused. |
| `Grant.Tenants` | one of | Tenants by name. |
| `Grant.AllTenants` | one of | Every tenant in that environment. |

`Tenants` and `AllTenants` are mutually exclusive and one is required. An
empty `Tenants` list is **refused**, never read as "everything": a list
empty because a derivation produced nothing is the likeliest way a grant
widens by accident.

Names must match `^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`. This is a security
boundary rather than a style rule — names are interpolated into a filter
expression, so one containing `|`, `)` or `.*` would widen the grant. Such
a name is refused rather than escaped.

### Methods

| Method | Returns |
|---|---|
| `Validate() error` | Every problem found, joined, not just the first. |
| `RenderClaim(Principal) (Claim, error)` | The `vm_access` body an issuer mints. |
| `RenderVMAuth(issuer string) (VMAuthConfig, error)` | The proxy's `users` list, one entry per principal, reads `first_available` with retry on 500/502/503. |

Rendering does not mutate its input, and output order is stable: grants
sort by environment and tenants sort by name, so an unrelated change
produces no diff.

## Published artifacts

| Artifact | Where |
|---|---|
| `platform-alerts` | `oci://ghcr.io/truvity/charts/platform-alerts` |
| `pkg/tenancy` | `github.com/truvity/observability` |

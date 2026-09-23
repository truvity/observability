# Safety

What can break, what this chart refuses in order to prevent it, and the
failure that earned each rule. Thresholds are stated against a measured
healthy range, because a threshold without one is a guess that will either
never fire or always fire.

## The refusals: `observability-crds`

Fixtures under `tests/invalid/observability-crds/`.

| Refusal | The failure it prevents |
|---|---|
| An unknown key | A misspelled set name leaves a set installed that the consumer believed they had turned off, and two owners then take turns overwriting the same CustomResourceDefinition. |
| Every set disabled | The release installs no CustomResourceDefinition at all and still reports Synced and Healthy. The failure surfaces much later, in the controller that wanted the kind, as an error nobody connects back to this release. The upstream Envoy Gateway CRDs chart defaults both of its sets to false and is exactly this trap. |
| A set that is not a boolean | `victoriaMetrics: "false"` is a non-empty string, which Helm's `if` reads as true: the set the consumer meant to disable installs anyway. |

There is no refusal for a kind that upstream has removed, because a chart
cannot see what a cluster already has. That question is answered by the
inventory at the foot of the render and by `kubectl diff` before the sync;
docs/adoption.md says how.

## The refusals: `platform-alerts`

Each of these fails the render, and each has a fixture under
`tests/invalid/platform-alerts/` that must keep failing.

| Refusal | The failure it prevents |
|---|---|
| An unknown key | In an alerting chart a silently ignored key is a rule that never fires. Schema is strict everywhere. |
| Every group disabled | The release installs, reports success, and alerts on nothing. |
| A store-watching group enabled with an empty `stores` | The deadman would watch nothing while appearing installed. |
| A store with no `rowsMetric` | The deadman cannot see that store, but the install looks complete. |
| `minCapacityRatio` above 1 | A mounted filesystem is always somewhat smaller than its claim, so this fires on every healthy volume and is then silenced everywhere. |
| `headroomFactor` of 1 or less | The alert arrives at the moment writes already fail, which is the event it exists to precede. |
| A severity outside `critical`/`warning`/`info` | An alert whose severity has no branch in the routing tree fires into nowhere. |
| One free-space metric without the other | The read-only rule would compare a gauge against nothing. |
| A duration that is not one | vmalert rejects the rule at load, long after the chart reported a successful install. |

## The rules, and the incident behind each

### The write-path deadman

A metrics store went read-only and kept serving reads for twenty minutes
with nothing noticing.

The obvious rule — alert on the store's own read-only flag — **cannot fire
in its own headline case**, because that sample is written into the store
that is refusing writes. Reads keep working when writes do not, so
dashboards stay up and queries answer. Watching the rows counter stop is
the only rule that can see it.

The expression is `(sum(rate(<rowsMetric>[window])) or vector(0)) == 0`.
The `or vector(0)` is load-bearing: it makes the rule fire for both shapes
of the failure, the counter that stops advancing and the counter that
disappears because the store is gone. Without it, a store that vanishes
produces an empty result, and an empty result is not an alert.

### A CronJob that stopped being scheduled

A backup silently stopped for two days. Nothing failed, because nothing
ran: a CronJob that is suspended, deleted, or whose controller is not
creating Jobs produces no failed Job at all. "No failures" is not
"working". Only the age of the last **success** can see it, which is why
`CronJobNotSucceeding` is the rule that matters and `BackupJobFailed` is
the companion.

Measured healthy range for a daily job: the age resets below 24h on every
run. The default threshold of 26h is that plus enough slack for a slow run
or a retry.

### A volume that was never mounted

A pod reported `1/1 Running` while writing to the node's root filesystem,
because its real volume was never mounted. Everything looked healthy and
the data was not where anyone thought.

The tell is capacity: a mounted filesystem is always *somewhat* smaller
than the claim that satisfied it — measured healthy range 0.95 to 0.99 of
the claim, the difference being filesystem overhead — but it is never a
fraction of it. The default threshold of 0.5 sits roughly two times below
that healthy range, so overhead can never reach it and a wrong mount
always does.

### A store approaching read-only

The threshold comes from the store's **own exported limit**, never from a
constant written into the chart. A constant is how a rule either never
fires (the limit is lower than you assumed) or always fires (it is
higher). A store that exports no such limit gets no rule, which is the
honest outcome rather than a rule built on a guess.

## The Enterprise boundary

The VictoriaMetrics family ships a community edition (Apache 2.0) and an
Enterprise edition whose binaries require a licence key. A chart that
quietly pulled an Enterprise image, or rendered an Enterprise-only flag,
would put its consumer in breach of the vendor's terms while everything
still ran — so the boundary is a refusal, not a note.

Nothing in this repository uses: downsampling, multiple retentions or
retention filters, vmstorage auto-discovery, `vmbackupmanager`,
`vmgateway`, per-tenant or query statistics, automatic TLS issuing, mTLS
between components or as a routing key, IP filters in vmauth, vmalert
multitenancy, rules read from object storage, Kafka or Pub/Sub
integrations, or FIPS builds. The stack chart, when it lands, refuses an
image tag containing `enterprise` and any `-license` flag, each with a
fixture under `tests/invalid/`.

What the design does rely on — vmauth's JWT verification, OIDC discovery,
claim matching and the `vm_access` claim, `vmbackup`, the partition
snapshot API, cardinality limits, deduplication, `-httpAuth` — is all
community. The vmauth version floor is **v1.147.0**, where
`default_vm_access_claim` arrived.

## A convention this chart cannot enforce

**A backup job must refuse an empty source.** `rclone sync` against an
empty source deletes the destination and exits zero — a backup that finds
nothing to back up reports success and destroys the copy you had. The job
must fail loudly instead. No rule here can see that, because from the
outside it is a successful run; it belongs in the job, and it is written
here so the next person to write a backup job reads it.

## What is deliberately not here

No rule fires on a store's read-only flag, on a "backup succeeded" gauge
alone, or on anything else whose absence is indistinguishable from health.
Every rule in this chart can see its own failure case.

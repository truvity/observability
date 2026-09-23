# Doctrine

The design rules: what this repository owns, what the consuming estate
owns, and why the shape is what it is.

## What this repository owns

The **mechanism** of a self-hosted observability stack: how the stores are
laid out, how a query is scoped to its tenant, how the collectors stamp
what they collect, and which rules catch a failure that is silent by
nature. All of it configurable, none of it naming an estate.

## What the consuming estate owns

Every particular: the issuer URL and its audience, the label keys that
carry tenancy, the hostnames, the buckets, the Slack receivers, the
retention, and every secret — which arrives as the *name* of a Secret, never
as a value. An estate supplies these from its own private repository, and
the leak canary in CI is what keeps that boundary from eroding one
convenient default at a time.

## Tenancy is a label pair, enforced at the door

A tenant is not an instance. Running a store per team multiplies the
operational surface by the number of teams, and — for the log and trace
stores, which cannot query across their own tenant ids — makes the
fleet-wide question unanswerable exactly when an incident demands it.

So telemetry carries a `tenant` and an `env` label, stamped by the
collectors from the namespace's own labels, and isolation happens at query
time: a proxy in front of the stores reads the caller's token and injects
the filters that token is entitled to. A viewer's reach follows from their
identity, not from which address they happened to query.

The consequence worth stating plainly: **an application cannot choose its
own tenant.** The label is applied by the platform, from metadata the
application does not control.

## Replication is the writer's job

None of the three stores replicates across a zone in a way that survives
one. The documented pattern for all of them is the same: two independent
instances, the collectors sending to both with a per-destination buffer,
and a proxy in front of reads. That is what "high availability" means
here — the writers hold the redundancy, and the store stays a simple
single-node process that a person can reason about at three in the
morning.

Cluster mode exists and buys sharding. It is the escape hatch for a volume
this shape cannot carry, not the starting point.

## Rules are proven, not asserted

Every rule in `platform-alerts` was written after an incident, carries the
incident in a comment, states its threshold against a measured healthy
range, and has a negative fixture that must fail. A rule nobody has seen
fire is indistinguishable from a rule that cannot fire, and the second
kind is what this chart exists to prevent.

A rule whose failure case it cannot itself observe does not belong here.
That is why there is no alert on a store's read-only flag: the sample
carrying it is written into the store that has stopped accepting writes.

## Refusals over defaults

Where a wrong value would be silently harmful, the chart refuses rather
than guessing. There is no default store list, because a guessed metric
name renders cleanly and never fires. Every refusal has a fixture; a
refusal without one is a rule that will quietly stop working.

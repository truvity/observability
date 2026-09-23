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

One dimension, two names. On metrics and on spans the tenant is a label
called `tenant`; in the log store it is a **field** whose name the log
agent chose, because vlagent can rename no field and a namespace label
arrives as `kubernetes.namespace_labels.<key>`. Isolation is still one
pair of dimensions and one grant, but the read side has to be told both
names and refuses to render a filter until it is. A filter naming a field
the streams do not have does not fail: it returns nothing, and nothing is
the one answer a person will believe. docs/safety.md has the mechanism.

"Injects" is the load-bearing word, and it is a separate thing from
deciding. The proxy works out what the caller is entitled to and then
applies it by substituting that filter into the route it forwards on —
two halves, of which only the first is visible in the rendered
configuration. A route missing the second half forwards every query
unfiltered while the grant beside it still reads correctly, so the
filter is part of what a read route IS here: `pkg/tenancy` cannot
construct one without it, and the chart refuses to render one that lost
it.

Which is also why **traces are not scoped by this proxy, and the charts
say so instead of pretending.** The trace store's select APIs accept no
argument a proxy could put a filter in, so that route is admitted by a
value whose name states what admitting it means, or it is not rendered.
An unenforceable path that looks enforced is the failure this whole
section exists to avoid; adding one for a third signal would be the same
failure with better manners.

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

## CRDs are owned, and owned separately

Helm never upgrades a CRD installed from a chart's `crds/` directory, so a
chart that ships its CRDs that way installs a schema once and then diverges
from the controller that reads it, silently, for as long as the install
lives. And a CRD that arrives as a side effect of whichever chart happened
to install it has no owner at all: removing that chart takes the kind, and
every object of it, with it.

So the CustomResourceDefinitions are a release of their own, applied ahead
of the controllers, never pruned, and the controllers are told not to
manage them. That is `charts/observability-crds`.

### Two upstreams, two mechanisms, and why

The chart carries two sets, and it fetches them differently. This is a
measurement, not a preference.

The **VictoriaMetrics operator's** CRDs come from upstream's own
`victoria-metrics-operator-crds` chart, declared as a dependency at an
exact version and vendored into the chart's `charts/` directory by
`just crds`. Upstream publishes that chart with descriptions stripped —
3.0 MB against the 8.8 MB of the same twenty-five definitions in the
operator repository — and it is upstream's own statement of which CRDs
belong to a release, which is exactly the thing we want to track. Vendoring
the archive rather than resolving it at render time keeps the render a
function of the checked-out tree: `just golden` and `just lint` reach no
registry, and a bump is a diff.

Both sets are installed without their `description` fields, which is how
upstream publishes its own CRD bundles: they are the whole of the size
difference — 934 KB against 350 KB for the four Prometheus kinds — and a
golden nobody can read asserts nothing. The `description` keys dropped are
only those with a scalar value; a property *named* `description` is a
mapping and is left alone, because dropping one would remove a field from
the schema rather than a sentence from it.

The **Prometheus Operator's** four scrape kinds come from ocictl's
`crdctl`, pinned in `hack/crds/prometheus-operator/crdctl.yaml`, which is
how every other CRD-only chart in this estate is built. There is no
upstream chart carrying those four alone; `crdctl` fetches the directory
and `hack/crds.sh` keeps the four by name, failing if one of those names
has changed rather than quietly installing nine.

Mixing the two has one trap, and it is closed in `lint`: a chart renders
with whatever archive is in its `charts/` directory, so moving the version
in `Chart.yaml` and leaving the vendored archive behind installs the old
CRDs while every check passes and the golden does not move. `helm
dependency list` reports that as `wrong version`, and the lint recipe reads
it.

### The golden is a kind list

A CRD chart's golden render is megabytes of upstream schema, and the
question a bump has to answer is none of it: did a kind disappear, get
renamed, or move the version it stores? So the chart renders the answer —
every kind, its group, the versions it serves and the one it stores — as a
comment block generated from the same files it installs, and that block is
the last document of every golden. A bump that drops a kind is a few
readable lines of diff. Nothing about it is maintained by hand, because a
hand-maintained list is one that goes stale at the first bump and then
asserts something that is no longer true.

## Two collection paths, on purpose

Our own software is OpenTelemetry-only: it speaks OTLP to a gateway in its
own cluster and knows nothing about a store. Infrastructure and
third-party components are wired as they come — a Prometheus endpoint
through a scrape object, a container's stdout through the log agent, OTLP
where a component happens to offer it.

Collapsing the two would mean one of two things, and both are worse than
the seam. Either every third-party component has to be instrumented with
an SDK we do not control, or our own software has to learn a store's
protocol and lose the property that makes it portable. The stores do not
know which agent sent them data, so the seam costs nothing at the far end.

**The collection layer is not itself vendor-neutral, and that was
considered.** An OpenTelemetry-only collection layer — a daemonset
collector with `filelog` and the `prometheus` receiver fed by the Target
Allocator, `prometheusremotewrite` out so every pre-built rule keeps its
metric names — works. It was not taken because the store-native agents
give by default the buffer and checkpoint semantics a collector needs
declared and fixtured, and because it needs a second operator and a second
admission webhook. The trigger for revisiting it: a decision that
collection must be vendor-neutral too, or a second store family behind the
same agents.

Two rules keep that a swap rather than a redesign, and they are why they
look pedantic:

- **Scrape objects are always the Prometheus Operator kinds.** The
  VictoriaMetrics operator converts them today and the Target Allocator
  reads the same objects. One `VMPodScrape` is the first of the thirty
  that follow it.
- **The CRDs for those kinds are declared**, in `observability-crds`,
  rather than arriving as a side effect of whichever chart installed them
  first.

## The OpenTelemetry operator is not used

The collector is an ordinary workload this repository renders. The
operator's value is zero-code instrumentation for Java, Node.js, Python
and .NET; for Go its path is a privileged eBPF sidecar on single-container
pods, and software we own carries the SDK already. A second operator with
a second admission webhook, to render a StatefulSet a chart can render, is
cost with nothing behind it.

Rendering the collector's configuration here rather than passing it
through an upstream chart is the other half of that. Every refusal this
repository makes about the gateway is a statement about that file — which
attributes may become stream fields, that the tenancy statements run in an
order that cannot be reordered, that every exporter has durability behind
it. A configuration that arrives as an opaque passthrough can be checked
for none of it.

## Rules are proven, not asserted

Every rule in `platform-alerts` was written after an incident, carries the
incident in a comment, states its threshold against a measured healthy
range, and has a negative fixture that must fail. A rule nobody has seen
fire is indistinguishable from a rule that cannot fire, and the second
kind is what this chart exists to prevent.

A rule whose failure case it cannot itself observe does not belong here.
That is why there is no alert on a store's read-only flag: the sample
carrying it is written into the store that has stopped accepting writes.

## One input, two shapes

An estate can hold the tenant mapping in the proxy's configuration or in
the token its issuer mints. Both are reasonable; which one fits depends on
where the estate would rather make the change.

`pkg/tenancy` renders both from one input rather than offering two
functions to keep in step. A difference between them is a difference
between what a token says a person may read and what the proxy lets them
read — and nothing surfaces that difference until someone sees data they
should not, or fails to see data they should.

## Community edition only

Everything here wraps the community edition of the VictoriaMetrics family,
which is Apache 2.0 and free to use for any number of tenants, companies
or customers. The Enterprise edition is a different thing: its binaries
need a licence key, and running them without one is a breach of the
vendor's terms, not a configuration mistake.

So the charts stay inside the community boundary by construction. They
default to community images and refuse an image tag containing
`enterprise`; they never render a `-license` or `-licenseFile` flag; they
offer no per-tenant retention, because retention filters are Enterprise;
they secure the path between components with a bearer and the stores'
own `-httpAuth`, because mTLS between components is Enterprise; and they
never wrap `vmbackupmanager` or `vmgateway`. The tenancy mechanism rests
on vmauth's JWT support, which is community from v1.137.0 and complete
for this design from v1.147.0, and safe for it only from v1.152.0
(see [safety.md](safety.md)) — and which the vendor itself now
recommends over the Enterprise gateway it replaces.

Grafana and its VictoriaMetrics data source plugin are AGPL-3.0. Running
them unmodified is fine; this repository references them by name and
never vendors their code into an MIT-licensed tree.

## Refusals over defaults

Where a wrong value would be silently harmful, the chart refuses rather
than guessing. There is no default store list, because a guessed metric
name renders cleanly and never fires. Every refusal has a fixture; a
refusal without one is a rule that will quietly stop working.

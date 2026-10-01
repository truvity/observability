# Owner label: which company an alert belongs to

An estate that runs workloads for more than one company needs an alert to
reach the owning company's chat, not one shared channel. The scoping key
(cluster and namespace) says where a workload runs, never whose it is.
`tenancy.owners` adds one more label, `owner`, derived from the namespace.

It is opt-in. Left empty, nothing in any render moves.

## What it does

`observability-emitters`:

```yaml
tenancy:
  cluster: example-cluster
  environment: production
  owners:
    acme: [checkout, "payments-*"]
    globex: ["globex-*"]
  defaultOwner: acme          # optional
```

A pattern is a namespace name; `*` stands for any run of namespace
characters. Every series, OTLP log record and span from a matching
namespace carries `owner="acme"` (a label on metrics, a field or attribute
named `owner` on logs and spans).

`platform-alerts` (`ownerLabel: owner`) keeps the label in the `by (...)` of
its aggregating rules, and `observability-stack`
(`notifications.ownerLabel: owner`) lets a route match on it:

```yaml
notifications:
  ownerLabel: owner
  routes:
    - match: {owner: globex}
      critical: {channel: "#alerts-critical", workspace: globex}
```

## How it is derived, and why there

- Metrics: the metrics agent's global relabeling
  (`VMAgent.spec.inlineRelabelConfig`), applied after every scrape-level
  rule and just before sending. That is the one point where
  `k8s_namespace_name` is final: kube-state-metrics, cAdvisor and the
  kubelet re-derive it from the OBJECT's namespace in their own metric
  relabeling, so a rule in the scrape class would read the exporter pod's
  namespace and be wrong for exactly the series that matter. An `owner`
  the series arrived with is removed first: an application does not choose
  its owner.
- OTLP signals: the gateway's `transform/tenancy` processor, after
  `k8sattributes` has resolved the namespace from the pod object. A
  self-claimed `owner` is deleted first. On the OTLP metrics path the
  exporter promotes `owner` to a label beside the cluster and namespace.
- First match wins. Two owners may not list the same pattern, and a literal
  namespace another owner's glob already covers is refused. Two
  overlapping globs (`a*`, `*b`) cannot be told from disjoint ones without
  the namespaces; the alphabetically first owner wins there.
- `defaultOwner` covers everything unmatched, including cluster-scoped
  series (nodes) that have no namespace. Unset, those carry no `owner`.

`owner` is not a scoping key: no grant selects on it, and `pkg/tenancy` is
unchanged. It is routing metadata, derived from a key that is.

## What it does not cover

- Container logs collected by the log agent. That agent adds only static
  fields and cannot stamp per namespace, so those lines carry no `owner`;
  filter them by namespace. Logs and spans that arrive through the OTLP
  gateway do carry it.
- Alerts whose rule does not keep the label: any rule you write that
  aggregates must add `owner` to its `by (...)`. Rules with no namespace
  (the store rules) carry none; put `owner` in `commonLabels` if wanted.
- Recording rules and dashboards: unchanged. A new label is one more
  dimension of low cardinality (one value per owner), and a `by (...)` that
  omits it simply aggregates across owners as before.
- Logs-based alerts and anything the stack does not derive from a metric.
- A namespace that changes owner: series already stored keep the old value.

## Safety

An owner of the wrong value sends an alert to the wrong company's chat,
which for a partner estate is a disclosure. The refusals exist for that:
owner names and patterns are shape-checked (no regular-expression
metacharacters), an empty pattern list is refused, a namespace claimed by
two owners is refused, a default with no owners is refused, and a route on
`owner` is refused unless `notifications.ownerLabel` says the label exists.

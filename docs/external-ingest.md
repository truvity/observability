# External OTLP ingest: identity from the gateway, not the payload

`observability-emitters` can accept OTLP from outside the cluster (an AWS
Lambda function, a batch job, a partner) on a second receiver, and file it
under an identity the sender cannot choose. Off by default; with it off
nothing in any render moves.

```yaml
otlp:
  external:
    enabled: true
    headers:                       # header the gateway sets -> attribute it becomes
      x-roster-subject: enduser.id
      x-roster-account: cloud.account.id
      x-roster-owner: owner
    attributes:                    # static, for what no header carries
      project: serverless
    networkPolicy:
      ingressFrom:                 # the gateway's Envoy pods, and only them
        - namespaceSelector:
            matchLabels: {kubernetes.io/metadata.name: gateway-system}
          podSelector:
            matchLabels: {app.kubernetes.io/name: envoy}
```

## The trust model

Three parties, three jobs.

1. **The public route** (an Envoy Gateway `HTTPRoute` with a
   `SecurityPolicy`) verifies the caller's JWT, and writes the verified
   claims into request headers (`claimToHeaders`). It must strip every
   client-supplied copy of those headers BEFORE it sets its own (a
   `ClientTrafficPolicy` with `earlyRequestHeaders.remove`): without that a
   client's own `x-roster-subject` can win over the claim. It must also
   have no anonymous rule, and should limit methods and paths
   (`POST /v1/traces|metrics|logs`) and body size.
2. **The network** admits the collector's external port from the route's
   pods only (`otlp.external.networkPolicy.ingressFrom`, refused empty).
   The headers are believed because of WHO sent them: a pod that could
   connect to the port could write them itself. A cluster whose CNI does
   not enforce NetworkPolicy does not have this layer.
3. **The collector** (this chart) believes the headers listed in
   `otlp.external.headers` and nothing the payload says about who it is.

The collector therefore does not authenticate anything. It is the last
step of a chain whose first two steps are outside this chart.

## What the collector does, in order

A separate receiver (`otlp/external`, HTTP only, port `httpPort`, default
4319, `include_metadata: true`) feeds three pipelines of their own
(`metrics/external`, `logs/external`, `traces/external`). The in-cluster
receiver on 4317/4318 does not read headers and is not changed; the two
never share a port, and the same port is refused. The pipelines end in the
same exporters as the in-cluster ones, so writes replicate and go through
the same writer.

1. `transform/external-disown` deletes what the client may not choose, from
   the resource, the scope, and every span, log record and data point.
2. `resource/external-identity` writes each mapped header into its
   attribute (`from_context: metadata.<header>`).
3. `filter/external-unidentified` DROPS any data whose resource lacks a
   mapped attribute (header missing or empty). Every header in `headers` is
   required. The request is still answered 200 (the gateway decided);
   the data is not stored anonymous. Count the drop on the collector's
   `otelcol_processor_filter_*` metrics.
4. `transform/external-stamp` writes the chart's own stamps: the cluster
   and tier (`tenancy.*`, as on every path), the namespace
   (`otlp.external.namespace`, default `external`), `telemetry.source=external`
   and the static `attributes`.
5. `batch`, last: the headers belong to one request, and after batching a
   resource can be merged from several. Everything above runs before it.

## What is deleted

Always, and not removable:

- everything under `k8s.` and `kubernetes.` (the scoping key is cluster and
  namespace; a caller that could set `k8s.namespace.name` could file its
  data under another workload),
- `telemetry.source`,
- every attribute named in `headers` or `attributes` (so an attribute the
  deployment decides is never a client's).

By default, and extensible through `otlp.external.deleteAttributes`:
`owner`, `project`, `deployment.environment.name`, `service.namespace`,
`cloud.account.id`, `aws.account.id`, `aws.iam.role`, `aws.iam.role.arn`,
`enduser.id`, `enduser.role`, `enduser.scope`, `access.subject`. The list is
what identifies or routes: tenancy (`owner`, `project`, the tier), and the
identity keys a reader might trust. Deleting the default `cloud.account.id`
matters in particular: the Lambda layer's platform logs carry the account
as a resource attribute, and the verified header replaces it.

Everything else a sender states passes through: `service.name`, `faas.*`,
`cloud.provider`, `cloud.region` and the like describe the workload and
grant nothing. Whatever is on a record is a claim by the sender unless it
is in the list above or comes from a header.

## The header contract

The header names are yours (they are the keys of `headers`; lowercase). The
route sets them from verified claims, for example:

| Header | Attribute | Source |
|---|---|---|
| `x-roster-subject` | `enduser.id` | the token's subject (`aws:<account>:role/<path><name>`) |
| `x-roster-account` | `cloud.account.id` | the account claim |
| `x-roster-owner` | `owner` | the tenancy claim, if the issuer provides one |

A tenancy the issuer does not provide is static: `attributes: {owner: acme}`.

Refused at render: an empty `headers`; an attribute under `k8s.` /
`kubernetes.`, or `telemetry.source` / `deployment.environment.name`, in
`headers` or `attributes`; one attribute written twice; the external port
equal to an in-cluster port or 8888; an empty `networkPolicy.ingressFrom`.

## Where it lands

- Namespace: the static `otlp.external.namespace`. It is the scoping key a
  read grant selects on, so grant readers `external` (or the value you
  chose). It is never taken from a request.
- Metrics: `owner`, `cloud.account.id` and `telemetry.source` are promoted
  to labels beside the cluster, namespace and tier; the rest of the
  resource stays on `target_info`. `enduser.id` is on `target_info` (one
  series per role, not on every sample).
- Logs and traces: every resource attribute is a searchable field. Stream
  fields are unchanged (`otlp.streamFields`).

## Limits

`otlp.external.maxRequestBodyBytes` (default 4 MiB, measured after
decompression) caps one request; larger is answered 400. Rate limiting is
the route's and the edge's job, before the collector.

## Checking it

`tests/external_ingest_test.go` runs the REAL rendered external processors
in the pinned collector image, posts OTLP carrying forged identity and
tenancy attributes (resource, scope and record level) with the trusted
headers, and asserts that the output carries the header identity and
nothing forged, and that data without a required header is dropped. It
skips without Docker; `EXTERNAL_INGEST=require` makes that a failure. The
manual check against a live install: post OTLP/JSON with a forged
`owner` through the route with a real token, then query the store for the
stored `owner` and `enduser.id`.

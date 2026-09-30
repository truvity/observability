# observability-mcp: the store, read-only, over MCP

`charts/observability-mcp` lets an AI agent read the metrics store (and,
in later phases, logs, traces and dashboards) through the Model Context
Protocol, without the agent, or the MCP server, holding a credential to
the store.

## The shape

One Deployment per enabled server, two containers:

```
 client ──► gateway ──► proxy :8080 ──► upstream 127.0.0.1:8081   (the stock MCP server)
                          │                      │
                          │  validates the       │ calls the store on
                          │  caller's token,     ▼ 127.0.0.1:8429
                          │  strips it        proxy (outbound side) ──► the store's proxy (vmauth)
                          ▼                      injects a token it exchanged for the pod's
                   issuer (JWKS)                 own ServiceAccount token
```

- **`upstream`** is the stock server, pinned, in HTTP transport, on
  loopback, holding no credential and declaring no container port.
- **`proxy`** is `resource-proxy` from truvity/access-roster, the pod's
  only container port. Inbound it validates the caller's access token
  (signature against the issuer's JWKS, `iss`, and `aud` equal to this
  server's own `resourceURL`), serves the RFC 9728 protected-resource
  metadata, and removes the caller's `Authorization` header before the
  request reaches the upstream. Outbound it listens on loopback and
  forwards the upstream's store calls to the store's proxy with a token it
  obtained by an RFC 8693 exchange of the pod's projected ServiceAccount
  token. The caller's token is never forwarded.

Who may reach a server is decided by the issuer's policy for the resource
URL, not by this chart. The access is read-only because the upstream's
write and admin tools are disabled and because the exchanged token can
only use the store proxy's read routes.

Objects per enabled server, all named `observability-mcp-<server>`:
ServiceAccount (no automounted token; one projected token, audience
`serviceAccountToken.audience`), Service, Deployment, NetworkPolicy, and
optionally a PodDisruptionBudget. Sessions of the streamable-HTTP
transport are held in the upstream's memory, so the default is one
replica; a second replica needs session affinity at the gateway.

## Values

| Value | Default | |
|---|---|---|
| `issuerURL` | `""` | The access-token issuer. Required while a server is enabled. |
| `proxy.image.repository` | `ghcr.io/truvity/access-roster/resource-proxy` | |
| `proxy.image.tag` | `""` | **No default.** Required while a server is enabled: the image is another repository's release. `proxy.image.digest` pins it further. |
| `serviceAccountToken.audience` / `.expirationSeconds` | `access-issuer` / `600` | The projected token the proxy exchanges. |
| `servers.<name>.enabled` | `false` | Only `metrics` is implemented. |
| `servers.<name>.resourceURL` | `""` | The RFC 8707 resource identifier, https. Required while enabled. |
| `servers.<name>.scope` | `openid` | |
| `servers.<name>.outbound.{target,tokenEndpoint,clientId,audience}` | `""` | All four required while enabled. |
| `servers.metrics.image` | `ghcr.io/victoriametrics/mcp-victoriametrics:v1.20.2@sha256:bcbf84f9...` | |
| `servers.metrics.entrypointPath` | `/prometheus` | Where the store's proxy serves the Prometheus API. |
| `servers.metrics.disabledTools` | see `values.yaml` | Replaces the upstream's own default list. |
| `networkPolicy.ingressFrom` | `[]` | Peers allowed on the proxy's port: the gateway. Required while `networkPolicy.enabled`. |
| `networkPolicy.egress.{issuer,vmauth}` | `[]` | Peers the issuer and the store's proxy are reached at. Required while enabled. |
| `podDisruptionBudget.enabled` | `false` | Refused with fewer than two replicas. |

## What the upstream is, and how it is run

`servers.metrics` runs the stock
[`mcp-victoriametrics`](https://github.com/VictoriaMetrics/mcp-victoriametrics)
(v1.20.2, released 2026-04-15) from
`ghcr.io/victoriametrics/mcp-victoriametrics`. The image is alpine-based
and runs as the `mcp` user, uid 1000 (the chart states the uid, because
`runAsNonRoot` cannot verify a name).

| Env | Value | Why |
|---|---|---|
| `MCP_SERVER_MODE` | `http` | Streamable HTTP; the MCP endpoint is `/mcp`. |
| `MCP_LISTEN_ADDR` | `127.0.0.1:8081` | Loopback only. |
| `VM_INSTANCE_TYPE` | `single` | See below. |
| `VM_INSTANCE_ENTRYPOINT` | `http://127.0.0.1:8429/prometheus` | The proxy's outbound listener plus `entrypointPath`. |
| `MCP_DISABLED_TOOLS` | see below | Replaces the upstream default. |
| `MCP_DISABLE_RESOURCES` | `true` | The resources are the embedded documentation. |
| `MCP_LOG_FORMAT` | `json` | |

`VM_INSTANCE_BEARER_TOKEN` is not set: the upstream runs with no
credential. (It still sends `Authorization: Bearer ` with an empty token;
the proxy's outbound side must replace the header, not add one.)

### The entrypoint, and what the store must serve

In `single` mode the upstream calls `<entrypoint>/api/v1/query` and
`<entrypoint>/vmalert/api/v1/alerts`; in `cluster` mode
`<entrypoint>/select/<tenant>/prometheus/...`, which the store's proxy does
not serve. With `entrypointPath: /prometheus` the paths on the wire are:

| Tool | Path through the store's proxy | Served by `observability-stack` read routes today |
|---|---|---|
| `query`, `query_range` | `/prometheus/api/v1/query`, `/query_range` | yes |
| `series`, `labels`, `label_values`, `metrics` | `/prometheus/api/v1/series`, `/labels`, `/label/<name>/values` | yes |
| `tsdb_status` | `/prometheus/api/v1/status/tsdb` | yes |
| `metrics_metadata` | `/prometheus/api/v1/metadata` | only with `tenancy.allowUnfilteredMetricMetadata` |
| `alerts` | `/prometheus/vmalert/api/v1/alerts` | only with `vmalertAPI` (unfiltered) |
| `rules` | `/prometheus/vmalert/api/v1/rules` | only with `vmalertAPI` (unfiltered) |

The stack's OIDC read routes carry the per-principal filter and cover the
query tools. The alerts and rules routes are opt-in, per principal:
`tenancy.principals[].vmalertAPI: true` in `observability-stack` (0.16.0)
adds `/prometheus/vmalert/api/v1/alerts` and `.../rules` for that principal,
forwarded to the metrics vmalert with the first path part dropped
(`drop_src_path_prefix_parts: 1`), so vmalert receives
`/vmalert/api/v1/...`, a path it serves. **The consumer sets the key on the
principal the MCP server's exchanged token selects** (the MCP machine
principal), together with `tenancy.allowUnfilteredAlertReads: true`.

These two routes are **not scoped**: vmalert has no per-namespace or
per-cluster concept, so the principal's grants do not limit what it reads
there. It sees every active alert, and every rule's expression and labels,
that the install's metrics vmalert holds, cluster-wide. That is the same
caveat as `tenancy.alertReaders`. Without the key the `alerts` and `rules`
tools fail and the rest work. `hack/vmalert-api-proof.sh` proves the route
against real vmauth, VictoriaMetrics and vmalert.

### Tools

The upstream's tools are: `query`, `query_range`, `metrics`,
`metrics_metadata`, `labels`, `label_values`, `series`, `rules`, `alerts`,
`tsdb_status`, `explain_query`, `prettify_query` (enabled here) and
`export`, `flags`, `metric_relabel_debug`, `downsampling_filters_debug`,
`retention_filters_debug`, `test_rules`, `metric_statistics`,
`active_queries`, `top_queries`, `tenants`, `documentation`, plus seven
VictoriaMetrics Cloud tools. All of them are read or local; none writes
to the store. `disabledTools` turns off:

- the upstream's own defaults: `export`, `flags`, `metric_relabel_debug`,
  `downsampling_filters_debug`, `retention_filters_debug`, `test_rules`;
- `metric_statistics`, `active_queries`, `top_queries`: routes the store's
  proxy does not admit, because they return other principals' query text or
  every namespace's metric names with counts;
- `tenants`: a cluster-only listing;
- `documentation`: an embedded index that dominates the upstream's memory
  and is nothing an agent needs from a store;
- the Cloud tools (`deployments`, `cloud_providers`, `regions`, `tiers`,
  `access_tokens`, `rule_filenames`, `rule_file`).

The setting replaces the upstream default, so the list carries it.

### Liveness

The upstream's own `/health/liveness` and `/health/readiness` are on
loopback, which the kubelet cannot reach; the pod's probes are the proxy's
`/healthz` and `/readyz`.

## What the proxy image must do

The chart fixes the contract it expects of `resource-proxy`; every name is
written once, in `templates/_helpers.tpl` (`observability-mcp.proxyEnv`):

`LISTEN`, `UPSTREAM`, `ISSUER_URL`, `RESOURCE_URL`, `SCOPE`,
`OUTBOUND_LISTEN`, `OUTBOUND_TARGET`, `OUTBOUND_SA_TOKEN_FILE`,
`OUTBOUND_TOKEN_ENDPOINT`, `OUTBOUND_CLIENT_ID`, `OUTBOUND_AUDIENCE`.

Two behaviours follow from the upstream and are not optional:

- **The upstream serves MCP only at `/mcp`.** `UPSTREAM` is
  `http://127.0.0.1:8081/mcp`, and clients connect to the resource URL (for
  example `https://mcp.example.com/metrics`), so the proxy must map the
  inbound path onto the path of `UPSTREAM` rather than append to it.
- **The outbound side replaces `Authorization`.**

## Network

Ingress: the peers in `networkPolicy.ingressFrom`, on the proxy's port.
Egress, and nothing else: the issuer (ports from `issuerURL` and
`outbound.tokenEndpoint`), the store's proxy (the port from
`outbound.target`), and cluster DNS. Both peer lists are required while the
policy is on.

## Refusals

| Shape | Why |
|---|---|
| `issuerURL` empty | Nothing to validate a token against. |
| `proxy.image.tag` empty | The proxy image is another repository's release; an empty tag renders an image that cannot be pulled. |
| An enabled server with `resourceURL` empty | No client could ever get a token with the right `aud`. |
| An enabled server with any of `outbound.{target,tokenEndpoint,clientId,audience}` empty | The upstream would call the store with no credential. |
| `networkPolicy.ingressFrom`, `egress.issuer` or `egress.vmauth` empty while enabled | A rule that admits, or reaches, nobody looks like a scoped one. |
| `podDisruptionBudget.enabled` with fewer than two replicas | A budget of one on one replica blocks every drain. |
| `logs`, `traces` or `dashboards` enabled | Values stubs in this release. |

An unknown key fails the schema. Each refusal has a fixture under
`tests/invalid/observability-mcp/`.

## Later phases (values stubs)

Facts recorded for when each is wired; none is rendered today.

| Server | Stock image | Release | Notes |
|---|---|---|---|
| `logs` | `ghcr.io/victoriametrics/mcp-victorialogs` | v1.9.0 | `VL_INSTANCE_ENTRYPOINT`, `MCP_SERVER_MODE=http`, `MCP_LISTEN_ADDR`, `MCP_DISABLED_TOOLS`; MCP at `/mcp`. The store's proxy routes `/select/logsql/.*`. |
| `traces` | `ghcr.io/victoriametrics/mcp-victoriatraces` | v1.5.0 | `VT_INSTANCE_ENTRYPOINT`, same MCP variables. The store's proxy routes `/select/jaeger/.*`, `/select/tempo/.*`, and only with `tenancy.allowUnfilteredTraceReads`. |
| `dashboards` | `docker.io/grafana/mcp-grafana` | v1.6.3 | Flags, not env: `-t streamable-http --address 127.0.0.1:8081 --enabled-tools=search,dashboard --disable-write`, plus `--allowed-hosts` (it validates `Host`). Its credential is a Grafana service-account token (`GRAFANA_SERVICE_ACCOUNT_TOKEN_FILE`), which is a different wiring from the proxy's outbound side. |

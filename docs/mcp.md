# observability-mcp: the stores and Grafana, read-only, over MCP

`charts/observability-mcp` lets an AI agent read observability data through
the Model Context Protocol, without the agent, or any MCP server, holding a
credential to the store. It knows two things and nothing about an estate's
topology:

- **stores**: a Victoria store is VictoriaMetrics, VictoriaLogs and
  VictoriaTraces behind ONE vmauth. Each entry in `stores` renders **one
  connector**: one OAuth resource, one Deployment, every signal's tools in
  one MCP server, grouped by prefix (`metrics_`, `logs_`, `traces_`).
- **grafana**: one optional connector over a Grafana, dashboards only.

Which stores exist, what they are called, which clusters each holds, and
that Grafana reads several of them, is the consumer's configuration. An
estate with two stores and one Grafana sets two `stores` entries and
`grafana.enabled`; one with a single store sets one.

## The shape

A store's pod:

```
 client ─► gateway ─► proxy :8080 ─► aggregator 127.0.0.1:8081 ─► metrics  127.0.0.1:8082  ┐
                        │  inbound                 (tools only)  ─► logs     127.0.0.1:8083  ├─ the stock servers
                        │  validates the                         ─► traces   127.0.0.1:8084  ┘
                        │  caller's token,                                   │
                        │  strips it                                         │ call the store on 127.0.0.1:8429
                        ▼                                                    ▼
                  issuer (JWKS)             proxy (outbound side) ─► the store's vmauth
                                            injects a token it exchanged for the pod's own
                                            ServiceAccount token
```

A Grafana pod is the proxy and the stock `mcp-grafana`, with no aggregator:

```
 client ─► gateway ─► proxy :8080 ─► mcp-grafana 127.0.0.1:8081 ─► proxy (outbound side) ─► Grafana
```

- **proxy** is `resource-proxy` from truvity/access-roster, the pod's only
  MCP port. Inbound it validates the caller's access token (signature
  against the issuer's JWKS, `iss`, and `aud` equal to the connector's
  `resourceURL`), serves the RFC 9728 protected-resource metadata, and
  removes the caller's `Authorization` header before the request goes on.
  Outbound it listens on loopback and forwards to the store's vmauth (or to
  Grafana) with a token it obtained by an RFC 8693 exchange of the pod's
  projected ServiceAccount token. The caller's token is never forwarded.
- **aggregator** is `cmd/mcp-aggregator` in this repository (below).
- **the stock servers** are
  [`mcp-victoriametrics`](https://github.com/VictoriaMetrics-Community/mcp-victoriametrics),
  [`mcp-victorialogs`](https://github.com/VictoriaMetrics-Community/mcp-victorialogs)
  and
  [`mcp-victoriatraces`](https://github.com/VictoriaMetrics-Community/mcp-victoriatraces),
  pinned by tag and digest, on loopback, holding no credential and declaring
  no container port.

Who may reach a connector is decided by the issuer's policy for its
`resourceURL`, not by this chart. The access is read-only because the tool
allowlists contain only reads, the servers' write and admin tools are
disabled too, and the exchanged token can only use the store's read routes.

Objects per connector, all named `observability-mcp-<name>` (`<name>` is the
store's `name`; `grafana` for the Grafana connector): ServiceAccount (no
automounted token; one projected token, audience
`serviceAccountToken.audience`), Service, Deployment, NetworkPolicy, a
ConfigMap with the aggregator's configuration (stores only), and optionally
a PodDisruptionBudget. The name is the store's, not the release's, so two
releases of the chart enabling different connectors in one namespace do not
collide.

## Values

| Value | Default | |
|---|---|---|
| `issuerURL` | `""` | The access-token issuer. Required while anything is enabled. |
| `clusterLabel`, `logsClusterField` | `k8s_cluster_name`, `k8s.cluster.name` | The stack's `tenancy.clusterLabel` / `logsClusterField`. Only fill in the instructions text. |
| `proxy.image.tag` | `""` | **No default.** Required: the image is another repository's release. `proxy.image.digest` pins it further. |
| `aggregator.image.tag` | `""` | Empty is this chart's own `appVersion`: one release builds both. |
| `aggregator.callTimeout` | `60s` | How long one tool call may run before the caller gets an error naming the backend. |
| `upstreams.<server>.image` | pinned tag and digest | See below. |
| `upstreams.<server>.tools` | the verified allowlist | Replace to narrow (a list replaces, it does not merge). |
| `stores[]` | `[]` | One connector each; see below. |
| `grafana.*` | disabled | The Grafana connector; see below. |
| `networkPolicy.ingressFrom` | `[]` | The gateway. Required while `networkPolicy.enabled`. |
| `networkPolicy.egress.{issuer,vmauth,grafana}` | `[]` | Where the issuer, the stores' vmauth and Grafana are reached. Required for what is enabled. |
| `networkPolicy.metricsFrom` | `[]` | Optional: who may scrape the aggregator's `/metrics` (port 9090). |
| `podDisruptionBudget.enabled` | `false` | Refused with fewer than two replicas. |

### A store

```yaml
stores:
  - name: primary                       # objects: observability-mcp-primary
    resourceURL: https://mcp.example.com/victoria/primary
    clusters: [alpha, beta]             # optional; only writes the instructions
    vmauth: {url: "http://vmauth.monitoring.svc:8427"}
    outbound:
      tokenEndpoint: https://issuer.example.com/token
      clientId: observability-mcp-primary
      audience: primary-store
    # optional: scope, signals: {metrics, logs, traces}, metricsMetadata,
    #           instructions, replicaCount, vmauth.metricsPath,
    #           vmauth.caBundle, vmauth.podPort
```

The same chart renders one store and Grafana, or N stores and Grafana
(`tests/cases/observability-mcp/single-store`, `two-stores`, `everything`, `private-ca`),
and a store alone (`minimal`).

### What the consumer must set, per connector

| For | The consumer sets |
|---|---|
| each store | `name`, `resourceURL`; `vmauth.url`; `outbound.{tokenEndpoint,clientId,audience}`; its `clusters` if it wants them named; the gateway route for the path of `resourceURL`. In the issuer's policy: a `resources` row for the `resourceURL`, and a workload identity plus exchange client (`clientId`, minting `audience`). In the store's stack: a machine principal for that audience (see "What the store must serve"). |
| Grafana | `grafana.{resourceURL,url}`; `outbound.{tokenEndpoint,clientId,audience}`; in the issuer's policy the same two rows, with `audience` the one Grafana's `workloadAuth` expects; in the Grafana chart, `workloadAuth` (see "The Grafana connector"). |
| the chart | `issuerURL`, `proxy.image.tag`, and `networkPolicy`'s peers. |

The gateway must route `/.well-known/oauth-protected-resource/<path of
each resourceURL>` and the path itself to that connector's Service. The
**bare** `/.well-known/oauth-protected-resource` (no path) on a host shared
by several connectors must go to exactly one of them; the proxy answers
both forms for its own resource.

## The aggregator

`cmd/mcp-aggregator` serves the allowlisted tools of several MCP servers as
one. It is generic: a YAML file lists backends, and the chart generates that
file from a store. It names no product.

```yaml
listen: 127.0.0.1:8081       # loopback: the proxy is what faces the network
path: /mcp
adminListen: ":9090"         # /healthz, /readyz, /metrics; nothing of MCP
serverName: observability-mcp-primary
callTimeout: 60s
instructions: |              # advertised verbatim
  Read-only access to the observability store "primary": ...
backends:
  - prefix: metrics          # exposed name: <prefix>_<tool>
    url: http://127.0.0.1:8082/mcp   # must be loopback
    tools: [query, query_range, ...] # the allowlist
```

- **Tools only.** It advertises the `tools` capability (no `listChanged`: the
  set is static) and answers `resources/*`, `prompts/*`,
  `completion/complete` and `logging/setLevel` with JSON-RPC method not
  found. The stock Victoria servers offer thousands of documentation
  resources (about 7,000 and 9 MB across the three, colliding across servers)
  and prompts that cannot be switched off; none reaches a client. The
  `instructions` come from configuration, not from the servers.
- **Startup.** It connects to every backend, runs `tools/list`, and keeps
  only allowlisted tools, with description, input schema and annotations
  verbatim. It **refuses to become ready** if an allowlisted tool is missing,
  so a server upgrade cannot silently change the surface; it retries until
  the backends answer. A tool the backend offers that is not allowlisted is
  logged and counted (`mcp_aggregator_unlisted_tools`) and never exposed.
- **Names.** `<prefix>_<tool>` must match `^[a-zA-Z0-9_-]{1,64}$`; the file is
  refused otherwise, and so is an empty allowlist, a prefix used twice, an
  unknown key, or a backend that is not on loopback.
- **Calls.** `tools/call` goes to the backend that owns the prefix, with the
  arguments byte for byte and the result as the backend returned it. Each
  call opens its own upstream session and closes it when the call ends: none
  is pooled, none can go stale, and no upstream session id is ever visible to
  a client. A per-call timeout applies, and a dropped request cancels the
  backend call.
- **Partial failure.** `tools/list` is served from the static snapshot. If
  one backend is down after start, only its tools fail, with an error result
  that names the backend ("the logs backend is unavailable ... other backends
  are unaffected") which the model can read and report; the pod stays ready.
- **Authorization hook.** `Aggregator.Authorize` runs before every call with
  the exposed tool name. It is unused: every role that reaches a connector
  reads every store, and the proxy in front decides who may reach it. It
  exists so a narrower audience for one tool has a place to live that is not
  a client's good manners.
- **Observability.** `mcp_aggregator_tool_calls_total{tool,outcome}`,
  `mcp_aggregator_tool_call_duration_seconds{tool}`,
  `mcp_aggregator_ready`, `mcp_aggregator_tools`,
  `mcp_aggregator_unlisted_tools{backend}` and
  `mcp_aggregator_sync_failures_total{backend,reason}`. Labels carry the
  tool name and an outcome, never an argument. Its log has one line per call:
  tool, backend, outcome, duration.

### Protocol revisions

Built on the official Go SDK (`github.com/modelcontextprotocol/go-sdk`,
v1.8.0). As a **server** over streamable HTTP it negotiates 2026-07-28,
2025-11-25, 2025-06-18, 2025-03-26 and 2024-11-05, and runs in the SDK's
**stateless** mode, which is what makes both ends work:

- A 2026-07-28 client (`server/discover`, no handshake, no `Mcp-Session-Id`)
  is served natively. That revision is refused by a stateful SDK server; the
  aggregator is stateless.
- A 2025-06-18 client's `initialize` is answered with 2025-06-18 and **no
  session id**, and its later requests carry none: each is answered on its
  own. The GET stream is not offered (405), which the spec allows.
- As a **client** toward the stock servers (which speak 2025-06-18) it tries
  `server/discover`, falls back to `initialize` on a refusal, and remembers
  what the backend negotiated so it does not probe again.

Cancellation: for 2026-07-28 callers a dropped request cancels the backend
call. The older revisions cancel with a notification that a stateless server
has no session to deliver to, so for them the per-call timeout is what
bounds a call nobody is waiting for.

One trap, recorded because it cost a test run: the SDK's server transport
stamps the inbound request's protocol revision into the handler's context,
and its client transport reads the same key to choose the outbound
`Mcp-Protocol-Version` header. A backend call derived from the handler's
context speaks the caller's revision to a backend that was never asked. The
aggregator therefore takes only the cancellation from the caller's context.

## What the store must serve, tool by tool

The chart points each stock server at the proxy's outbound listener
(`127.0.0.1:8429`) plus a path prefix: `/prometheus` for metrics (single-node
mode, `VM_INSTANCE_TYPE=single`; cluster mode would call
`/select/<tenant>/prometheus/...`), and nothing for logs and traces, which add
`/select/logsql` and `/select/jaeger/api` themselves. The proxy forwards to
the store's vmauth, which serves these paths to a principal with
`routes: [metrics, logs, traces]` and `vmalertAPI: true`:

| Server | Tool | Path | Served by the stack's read routes |
|---|---|---|---|
| metrics | `query`, `query_range` | `/prometheus/api/v1/query`, `/query_range` | yes |
| | `metrics`, `labels`, `label_values`, `series` | `/prometheus/api/v1/label/__name__/values`, `/labels`, `/label/<name>/values`, `/series` | yes |
| | `tsdb_status` | `/prometheus/api/v1/status/tsdb` | yes |
| | `alerts`, `rules` | `/prometheus/vmalert/api/v1/alerts`, `/rules` | only with `vmalertAPI: true` |
| | `explain_query` | none (parses locally; its metadata lookups are best-effort) | n/a |
| | `metrics_metadata` | `/prometheus/api/v1/metadata` | **only with `tenancy.allowUnfilteredMetricMetadata`**; **not exposed by default**: set the store's `metricsMetadata: true` when the stack allows it |
| | `prettify_query` | `/prometheus/prettify-query` | **no** (see below); **dropped** |
| logs | `hits`, `facets`, `stats_query`, `stats_query_range`, `field_names`, `field_values`, `stream_field_names`, `stream_field_values`, `stream_ids`, `streams`, `query` | `/select/logsql/<tool>` | yes (`/select/logsql/.*`) |
| | `flags` | `/flags` (admin, at the root) | **no**; **dropped** |
| traces | `traces`, `trace`, `services`, `service_operations`, `dependencies` | `/select/jaeger/api/...` | yes (`/select/jaeger/.*`), for a principal admitted to unfiltered trace reads |
| all three | `documentation` | embedded | **dropped**: documentation belongs in resources, which the aggregator does not serve |

So a store's aggregated surface is **26 tools** by default (10 metrics, 11
logs, 5 traces), 27 with `metricsMetadata`; the Grafana connector adds a
connector of 7.

The two dropped tools fail by design and are not shipped: `prettify_query`
in `mcp-victoriametrics` v1.20.2 formats locally only when the parse
**fails** and otherwise calls `<entrypoint>/prettify-query`, a path no read
route admits, so it errors on every valid query (a small upstream bug);
`flags` calls an admin path at the store's root. `tests/mcp_routes_test.go`
keeps the table honest: every allowlisted tool's path must match a route in
the rendered `tenancy-mcp-reader` case, and the dropped ones must not;
`hack/mcp-paths-proof.sh` observes the same paths from the real binaries.

**What the stack needs for the connector's principal.** The exchanged token
selects a machine principal in the store's `observability-stack`:

```yaml
tenancy:
  allowUnfilteredAlertReads: true      # vmalertAPI is unfiltered
  allowUnfilteredTraceReads: true      # the trace API cannot be scoped
  principals:
    - group: <the connector's workload group>
      audience: <the connector's outbound.audience>
      # routes unset: metrics, logs and traces
      vmalertAPI: true
      grants:
        - {cluster: <each cluster the store holds>, allNamespaces: true}
```

`vmalertAPI` (added in 0.16.0) gives the metrics vmalert's alerts and rules at
`/prometheus/vmalert/api/v1/alerts` and `.../rules`, forwarded with the first
path part dropped so vmalert receives `/vmalert/api/v1/...`. Both routes are
**not scoped**: vmalert has no per-namespace concept, so the principal's
grants do not limit what it reads there (every active alert, every rule's
expression and labels). Traces are unscoped too. Metrics and logs are scoped
by the principal's grants. Without a route a tool fails and the rest work.

The stack's NetworkPolicy for vmauth must admit this chart's namespace; the
consumer does that through the stack's existing peer values.

### The stock servers' environment

| Env | Value | Why |
|---|---|---|
| `MCP_SERVER_MODE` | `http` | Streamable HTTP; the MCP endpoint is `/mcp`. |
| `MCP_LISTEN_ADDR` | `127.0.0.1:808x` | Loopback only. |
| `VM_INSTANCE_TYPE`, `VM_INSTANCE_ENTRYPOINT` | `single`, `http://127.0.0.1:8429/prometheus` | Metrics: the proxy's outbound listener plus the prefix. |
| `VL_INSTANCE_ENTRYPOINT`, `VT_INSTANCE_ENTRYPOINT` | `http://127.0.0.1:8429` | Logs, traces. |
| `MCP_DISABLED_TOOLS` | see `values.yaml` | Defence in depth; replaces the server's own default list, so it names that too. |
| `MCP_DISABLE_RESOURCES` | `true` | Metrics only: the other two have no such switch; the aggregator drops their resources. |

No `*_BEARER_TOKEN`: the servers hold no credential. (They still send
`Authorization: Bearer ` with an empty token; the proxy's outbound side
replaces the header, not adds one.) The logs and traces servers send
`AccountID: 0` and `ProjectID: 0` and accept a per-call tenant argument;
the store's filters, not the tenant, are what scope a read.

## The Grafana connector

`grafana.enabled` renders `observability-mcp-grafana`: the stock
[`mcp-grafana`](https://github.com/grafana/mcp-grafana) v1.6.3, run with
`--enabled-tools=search,dashboard --disable-write`, which exposes seven
read-only tools (`search_dashboards`, `search_folders`, `get_dashboard_by_uid`,
`get_dashboard_summary`, `get_dashboard_property`,
`get_dashboard_panel_queries`, `list_dashboard_versions`); the default build
exposes 81, writes among them, and its one write tool in this set,
`update_dashboard`, is what `--disable-write` removes. It is not aggregated:
`proxy → mcp-grafana`. Flags, not environment, configure it:
`--transport=streamable-http`, `--address=127.0.0.1:8081`,
`--allowed-hosts=<the resource URL's host>,127.0.0.1:8081` (it validates the
`Host` header, and the proxy forwards the public one), `--usage-stats=disabled`.

### How it authenticates to Grafana: no stored secret

Three ways were weighed:

- **(a) an access-issuer workload token**, injected by the proxy's outbound
  side with Grafana as `OUTBOUND_TARGET`, accepted through Grafana's
  `[auth.jwt]` and mapped to a fixed Viewer. Nothing is stored.
- (b) a service-account token minted by a bootstrap Job: a long-lived secret,
  a Job with Grafana admin to mint it, and a rotation story.
- (c) a static service-account token in a Secret: the one being avoided.

The chart implements **(a)**. `mcp-grafana` is given `GRAFANA_URL=http://
127.0.0.1:8429` and **no token** at all; the proxy's outbound side replaces
`Authorization` with the workload token, and Grafana verifies it. Grafana
supports it without a custom header: `[auth.jwt] header_name = Authorization`
strips a leading `Bearer ` (`pkg/services/authn/clients/jwt.go`, v13.1.1,
checked against the pinned chart's Grafana).

Grafana must be configured to accept that token. That is an **opt-in,
default-off** value in `charts/observability-grafana`,
`global.observabilityGrafana.workloadAuth`; see [grafana.md](grafana.md),
"Workload sign-in". The consumer sets, in both charts, the **same**
audience: the Grafana chart's `workloadAuth.audience` and this chart's
`grafana.outbound.audience`; and registers an exchange client
(`grafana.outbound.clientId`) in the issuer's policy that may mint it.

## Network

Ingress: the peers in `networkPolicy.ingressFrom`, on the proxy's MCP port;
and, for a store and only when `networkPolicy.metricsFrom` is set, those
peers on the aggregator's admin port (9090). Egress, and nothing else: the
issuer (ports from `issuerURL` and `outbound.tokenEndpoint`), the store's
vmauth or Grafana (the port from `vmauth.url` / `grafana.url`, or `vmauth.podPort` / `grafana.podPort` when set; see the Service-port trap below), and cluster
DNS. The kubelet's probes are not subject to the policy: a store pod's
readiness is the proxy's `/readyz` **and** the aggregator's `/readyz` (on the
admin port), so it is not ready until every allowlisted tool was found. The
stock servers' own health endpoints are on loopback, which the kubelet
cannot reach.

**The Service-port trap.** The egress port is derived from `vmauth.url` or
`grafana.url`, which name a **Service** (for example `grafana.monitoring.svc:80`),
but a NetworkPolicy matches the **pod's** port, after the Service has
translated it. A Service on 80 in front of a pod on 3000 therefore renders an
egress rule for 80 that matches nothing, and every tool call times out behind
a policy that looks right. Set `grafana.podPort` (or `stores[].vmauth.podPort`)
to the port the pod listens on: the egress rule then uses it. Unset, the
URL's port is used, which is right only when the Service port equals the pod
port, or the URL names the pod directly. For a cross-cluster https URL there
is no pod peer: the egress peer in `networkPolicy.egress.{vmauth,grafana}` is
an `ipBlock`, the port is the URL's, and `podPort` stays unset.

The aggregator's admin port is the pod's only port besides the proxy's, and
it serves health and metrics, not MCP; `tests/mcp_pod_test.go` asserts
exactly those two ports, that every stock server listens on loopback and
holds no credential, and that the egress is three rules.

Several replicas of a store connector are safe (the aggregator is stateless
and opens a session per call); the Grafana connector keeps a session in
`mcp-grafana`'s memory, so its default is one replica, and a second needs
session affinity at the gateway.

## What the proxy image must do

The chart fixes the contract it expects of `resource-proxy`; every name is
written once, in `templates/_helpers.tpl` (`observability-mcp.proxyEnv`):
`LISTEN`, `UPSTREAM`, `ISSUER_URL`, `RESOURCE_URL`, `SCOPE`,
`OUTBOUND_LISTEN`, `OUTBOUND_TARGET`, `OUTBOUND_SA_TOKEN_FILE`,
`OUTBOUND_TOKEN_ENDPOINT`, `OUTBOUND_CLIENT_ID`, `OUTBOUND_AUDIENCE`, and,
only with a `caBundle`, `OUTBOUND_CA_FILE`.

- **Path mapping.** `UPSTREAM` is `http://127.0.0.1:8081/mcp` and clients
  connect to the resource URL (for example
  `https://mcp.example.com/victoria/primary`), so the proxy maps the whole
  resource path onto `UPSTREAM`'s path.
- **The outbound side replaces `Authorization`.**
- **A private CA for the outbound target.** When a connector sets `caBundle`
  (`stores[].vmauth.caBundle`, `grafana.caBundle`), the chart mounts the PEM
  bundle read-only into the proxy container at
  `/etc/observability-mcp/outbound-ca/ca.pem` and sets
  `OUTBOUND_CA_FILE` to it. The proxy appends the bundle to the system roots
  and uses the result for `OUTBOUND_TARGET` only (not for the issuer). A
  connector without `caBundle` gets neither the mount nor the variable. This
  needs the resource-proxy release that adds `OUTBOUND_CA_FILE`; an older proxy
  ignores the variable and fails the TLS handshake with an unknown-authority
  error.

## Refusals

| Shape | Why |
|---|---|
| `issuerURL` empty | Nothing to validate a token against. |
| `proxy.image.tag` empty | The proxy image is another repository's release. |
| a store with no `name`, a name that is not a slug, `grafana`, or a repeat | The name is an object name; two would collide. |
| a connector with `resourceURL` empty, or the same as another's | No client could get a token with the right `aud`; one URL is one resource. |
| a connector with `outbound.{tokenEndpoint,clientId,audience}` empty | The servers would call with no credential. |
| a store with no `vmauth.url`, or one with a path | Nothing to read; a path would be dropped. |
| `caBundle` with both `configMap` and `secret`, with neither, or on an http URL | One source only; a bundle for an http target would silently do nothing. |
| `podPort` outside 1-65535 | Not a port. |
| a store with every signal off; a cluster that is not a plain label value | Nothing to expose; the list only fills the instructions. |
| an allowlist that is empty or would produce a tool name over 64 characters | The aggregator refuses it at start; here it is refused at render. |
| `grafana.url` empty; `upstreams.grafana.enabledTools` empty | Nothing to forward to; mcp-grafana would enable everything, writes included. |
| `networkPolicy.ingressFrom`, `egress.issuer`, `egress.vmauth` (with a store) or `egress.grafana` (with Grafana) empty | A rule that admits, or reaches, nobody looks like a scoped one. |
| `podDisruptionBudget.enabled` with fewer than two replicas | A budget of one on one replica blocks every drain. |

An unknown key fails the schema (a leftover 0.16 `servers:` among them).
Each refusal has a fixture under `tests/invalid/observability-mcp/`.

## Release

`cmd/mcp-aggregator` is built by the repository's release
(`.goreleaser.yaml`, `kos`): a multi-arch (linux/amd64, linux/arm64) image at
`ghcr.io/truvity/observability/mcp-aggregator:<version>` on a
distroless-static non-root base, and a tar.gz per architecture. The chart's
`aggregator.image.tag` defaults to its own `appVersion`, the same version.
`hack/check-image-refs.py` refuses a chart that names an own-registry image
the release does not build.

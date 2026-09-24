# Emitting telemetry from a service

The guide for whoever wires a service's SDK. It assumes the estate has
installed `charts/observability-emitters` on the service's cluster and
that the service runs in a namespace the estate's network policy admits
to the gateway. Everything an estate-specific guide would add is a
hostname; nothing here is one.

## The one address

The gateway the emitters chart renders, as a Service in its namespace:
`otlp-grpc` on 4317 and `otlp-http` on 4318, plaintext, no client
authentication. It is the **only** address a service sends to — never a
store, and never the proxy in front of the stores. The gateway holds the
queue, the retries and the credential, so a service that goes down
mid-batch loses nothing and a service that is compromised holds no token
at all. All three signals go to the same address.

## What is already true, and what is yours

Already done by the estate: the network path, the stores, retention, the
read grants — and **your stdout**, which a node agent already collects
into the log store. You do not need OTLP logs to have logs.

Yours: the SDK, the endpoint taken from configuration (unset must mean
*do not export*, not *localhost and retry forever*), `service.name`,
and verifying against the store rather than the sender.

The conventional SDK variables need no code:

```
OTEL_EXPORTER_OTLP_ENDPOINT=http://<gateway>:4318
OTEL_EXPORTER_OTLP_PROTOCOL=http/protobuf
OTEL_SERVICE_NAME=<your service>
```

## Resource attributes

| Attribute | Who sets it |
|---|---|
| `service.name` | **You. Required.** A log stream field: stable for the life of the pod, low-cardinality. Never a request id, a tenant or a version. |
| `service.version` | You, optional. A pod-level field, not a stream. |
| `k8s.namespace.name`, `kubernetes.pod_namespace` | **The gateway**, from the pod behind the connection. Anything you send under either key is **deleted** before the pod is resolved. |
| `k8s.cluster.name`, `deployment.environment.name` | **The gateway**, from the values the chart was installed with. Overwritten if you set them. |
| `k8s.pod.name`, `k8s.pod.uid`, `k8s.node.name` | the gateway |
| anything else | you; a pod-level field |

The namespace is the scoping key — it decides who may read your
telemetry — so it comes from the socket, not from your claim about
yourself. Setting it is neither an error nor a warning; it is discarded.
Build nothing on it.

## Logs

Your stdout is already collected, on every node, into the same store
under the same namespace. An OTLP log exporter usually buys a second
copy of the same lines — and a service whose logs exist only over OTLP
loses them exactly when the exporter is the thing that is broken.
Prefer structured stdout; send OTLP logs only for records that must
carry a span id, and then only those.

Three fields are the log stream: `k8s.cluster.name`,
`kubernetes.pod_namespace`, `service.name`. Everything else on a record
is a searchable field. A stream field that varies per pod, per request
or per deployment multiplies streams without bound, and the store
degrades slowly rather than refusing.

## Metrics

OTLP metrics are converted to remote-write on the way in. Two
consequences you cannot see from the sender:

- **only three resource attributes become labels** — cluster, namespace,
  tier. The rest of your resource lands on a `target_info` series and
  nowhere else. A label you expect to filter by must be a *metric*
  attribute.
- **a series with too many labels is ignored and the write answers 200.**
  The store does not refuse, warn, or count it anywhere a sender looks.
  It is the easiest way to emit nothing while every dashboard says the
  pipeline is healthy. `vm_rows_ignored_total` on the store is the
  counter; the stack's self-alerts watch it.

Delta temporality is accepted and converted; configure nothing.

## Traces

Sampling is yours: a parent-based ratio sampler with the ratio in
configuration. Spans are read **unscoped** by everyone with any grant —
the trace store's select APIs take no argument a proxy could put a
filter in — so span contents carrying no personal data is a rule for the
code that emits them, and nothing downstream can enforce it.

## Verifying

**A 200 from the exporter means the collector queued your batch. It is
not evidence.** Every fault this pipeline has had returned 200 to the
sender. Ask the store, with the stores' own credentials and a
port-forward:

- logs: `POST /select/logsql/query` with `query=<a string from your line>`;
- traces: `GET /select/jaeger/api/services` — is your service listed?
- metrics: `GET /metrics` on the store, `vm_rows_ignored_total`, sampled
  **twice** — the total is cumulative and carries old scars; a rising
  `too_many_labels` means your series are being dropped;
- the gateway's own log: `Dropping data` with `not retryable` is a
  permanent rejection by the store; `sending queue is full` means the
  export side is blocked and you are losing the overflow.

## Traps that have each cost a day

- **A 200 is not storage.** First, because every other entry was found
  by believing one.
- **A pod that exports within seconds of starting can be attributed to
  the previous occupant of its IP.** The gateway resolves the sender
  from the connection, and its cache can still hold the pod that had
  that address a moment ago. The namespace is the scoping key, so this
  is a correctness problem. Do not export from an init path or a job
  that exits in seconds without checking where it landed.
- **The three datasource URLs do not resemble each other.** The metrics
  datasource takes a `/prometheus` base; the logs plugin takes the
  **root** and appends its own query path; the trace datasource takes
  `/select/jaeger`. The wrong one saves cleanly, passes its health
  check, and fails only on a query.
- **An empty store looks exactly like a quiet service.** If you are the
  first to send a signal, prove it arrived before trusting its absence.

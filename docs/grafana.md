# Grafana across stores

`charts/observability-grafana` is one Grafana as the read UI over several
observability stores. Each store is an install of `observability-stack`
(often in another cluster); this chart is the place people look at all of
them.

It wraps the same `grafana` subchart, at the same exact version, that
`observability-stack` vendors as its optional Grafana. Use that option when
one install wants its own Grafana beside it. Use this chart when a single
Grafana has to span installs, which is a separate release with its own
lifecycle: it cannot be a mode of the stack, because the stack is one
install and this is a reader of several.

## What it owns

- **A datasource set per store.** For each store, both a
  `victoriametrics-metrics-datasource` (Explore, MetricsQL) and a
  `prometheus`-typed datasource on the same `/prometheus` URL, plus logs and
  traces. Community dashboards list only prometheus-typed datasources, so
  without the second row every one of their panels says "No data". Stable
  uids: `<store>-prom`, `<store>-metrics`, `<store>-logs`,
  `<store>-traces`. Dashboards reference uids, never names.
- **Sign-in through an OIDC issuer**, with the settings each of which has a
  support ticket behind it (below).
- **A database of its own.** External PostgreSQL, so more than one replica
  is safe.
- **Dashboards from git only**: the sidecar and its file provider.
- **A home dashboard**, when the consumer names one.

What stays with the consumer: the store URLs, the issuer and client id,
Secret names, the public hostname, the home path, and everything about the
pod (replicas, scheduling, image registry, the CA mount). The chart carries
mechanism; the consumer carries data.

## The values

Two namespaces, and the split is forced by Helm. A parent chart's values are
invisible to a subchart, which sees its own values and `global`. The
subchart renders the datasource mount, the ini file and the environment, so
everything it must know is read from `global.observabilityGrafana`. The
`grafana` key is the subchart's own values, for the pod.

```yaml
global:
  observabilityGrafana:
    rootUrl: https://grafana.example.org
    caFile: /etc/ssl/private-ca/ca.crt        # optional, per store too
    stores:
      - name: store-a                          # uids store-a-prom, ...
        url: https://store-a.example.org       # the ROOT
        default: true                          # exactly one
      - name: store-b
        url: https://store-b.example.org
        logs: true                             # default true
        traces: {enabled: true}                # default true; bearerEnv optional
    renamedDatasources: []                     # old names, see below
    oauth:
      name: SSO
      issuer: https://issuer.example.org       # /authorize /token /userinfo derive from it
      clientId: grafana
      clientSecretRef: {name: grafana-client, key: client-secret}
      roleAttributePath: "contains(groups[*], 'example:admin') && 'Admin' || 'Editor'"
      scopes: "openid profile email groups offline_access"
      sessionMaxLifetime: 24h
    database:
      host: postgres.example.org:5432
      name: grafana
      sslMode: verify-full
      caFile: /etc/ssl/private-ca/ca.crt
      secretRef: {name: grafana-pg-app, usernameKey: username, passwordKey: password}
    secretKeyRef: {name: grafana-secret-key, key: secret_key}
    dashboards:
      home:
        path: /tmp/dashboards/Fleet/fleet-overview.json   # only rendered when set
grafana:
  replicas: 2
  extraConfigmapMounts: [...]     # the CA, mounted where caFile says
  sidecar:
    image: {...}                  # a registry mirror, if nodes cannot reach quay
    dashboards:
      provider:
        updateIntervalSeconds: 30 # default; must stay above 10
```

A few consequences of the split:

- `caFile` is a path inside the pod, not a Secret name. Mounting it is a pod
  concern, so the consumer mounts it through `grafana.extraConfigmapMounts`
  (or `extraSecretMounts`) and names the path.
- The sidecar's settings (`grafana.sidecar.dashboards.*`) are the
  subchart's own keys with safe defaults set by this chart. The subchart
  does not template them, so they cannot follow `global`.
- Credentials (database login, client secret, session key) are read from
  Secrets as environment variables, so none reaches a ConfigMap. The
  client secret used to be a mounted file; it is now an environment
  variable, so rotating it needs a pod restart.
- The datasources are a ConfigMap of this chart's own, named after a hash of
  its content. The subchart hashes only its own ConfigMap into the pod
  template, so a change to a plain ConfigMap would roll nothing and
  provisioning only runs at start. The hashed name rolls the pods; Argo CD
  or Helm prunes the old ConfigMap.
- `grafana.extraContainerVolumes` and `grafana.extraVolumeMounts` carry that
  mount. A consumer that sets either list replaces the chart's entry, so it
  must repeat it; use `extraConfigmapMounts` and `extraSecretMounts` for
  your own.

## Why it is not the security boundary

Every datasource forwards the signed-in person's own token
(`oauthPassThru`). The store's proxy verifies it and injects the filters
their grant allows. A viewer who opens a datasource they hold no grant for
gets an empty panel, not somebody else's data. Grafana's own roles decide
navigation, never reach, which is why the role mapping can be generous.

`traces.bearerEnv` is the exception, on purpose: it sends a fixed bearer
token from an environment variable you supply through
`grafana.envValueFrom`, instead of the person's token. That datasource is
unscoped, so use it only for a store whose trace API cannot scope.

## Logs and traces link to each other

On by default (`correlate: true`), and only inside one store: store A's logs
open store A's traces and back, never another store's. A store that turns off
`logs` or `traces` gets no link, since the other end does not exist.

- **Logs to traces.** The logs datasource carries a derived field `TraceID`
  on the structured field `trace_id`, which is where VictoriaLogs puts the
  trace id of an OpenTelemetry log record. The value becomes a link that
  opens `<name>-traces` on that id.
- **Traces to logs.** The traces datasource carries `tracesToLogsV2` aimed at
  `<name>-logs`, with the LogsQL query `trace_id:"<trace id>"` from five
  minutes before the span to five minutes after it. It is a custom query
  because the built-in filters write LogQL, which VictoriaLogs does not read.
- **What it does not cover.** A log line that only embeds a `traceparent` or
  `trace_id=` in its message text has no `trace_id` field and gets no link;
  parse it into a field at ingestion if you want one.

Set `correlate: false` for plain datasources.

## The traps, in plain words

- **No second datasource, empty dashboards.** Grafana's dashboard variables
  of type `prometheus` list only prometheus-typed datasources. The plugin
  type alone leaves every community dashboard on "No data". The chart
  renders both per store. `default: true` lands on the prometheus-typed row,
  for the same reason.
- **The logs URL is the root, the metrics URL is `/prometheus`, the traces
  URL is `/select/jaeger`.** The logs plugin appends `/select/logsql/query`
  itself; giving it that path asks for
  `/select/logsql/select/logsql/query` and the store answers
  `unsupported path requested`. Give `url` the root and the chart builds
  the three.
- **A renamed datasource crash-loops new pods.** Provisioning matches an
  existing datasource by name, but the database also enforces a unique uid.
  Rename a datasource and keep its uid, and a new pod finds no row by the new
  name, inserts one, hits "data source with the same uid already exists",
  and dies before it serves. List the old name in `renamedDatasources`:
  Grafana deletes it before the inserts, and deleting a name that is gone
  is a no-op. Dashboards reference the uid, so nothing that points at the
  datasource breaks. The chart refuses a listed name that is a current
  datasource name.
- **An update interval of 10 seconds or less means dashboards never
  update.** At 10 or below Grafana stops polling and watches the
  filesystem, and a ConfigMap volume changes by swapping a symlink, which
  raises no watch event. Nothing reports an error. The chart refuses 10 or
  less; the default is 30.
- **`use_refresh_token` off keeps people signed in on a dead token.** The
  session outlives the access token; the UI works and every query answers
  401. The chart sets it and refuses it off.
- **No role mapping makes everyone a Viewer**, who in Grafana OSS has no
  Explore. `role_attribute_strict` is on, so an unmapped person is refused
  instead of handed a default; the mapping is therefore required.
- **Two replicas need a database both can reach.** A SQLite file is either
  two databases or one file two processes write (`database is locked`).
  `locking_attempt_timeout_sec` is 120 so the second replica waits for the
  schema migration instead of crash-looping.
- **A missing session key means Grafana's published built-in key.** Name a
  Secret, or set `secretKeyRef.builtInKey: true` to say you know. Changing
  the key later invalidates what was encrypted with the old one.
- **The home path is a promise.** It is rendered only when set, because a
  path to a dashboard the sidecar does not have breaks the home page.
  Build it from the sidecar folder (`/tmp/dashboards`), the folder
  annotation's value and the ConfigMap key. `observability-dashboards` ships
  a `fleet-overview` dashboard for it from 0.12.0; set the path only at a
  chart version that ships the file.
- **Alerting is off** (`unified_alerting`, `alerting`), analytics are off,
  the login form is disabled, and the session is capped at 24 hours by
  default. Alerting belongs to vmalert and Alertmanager.

- **Name an admin Secret for a deterministic render.** Left empty, the
  subchart generates a random admin password into a Secret of its own on
  every `helm template`, so a renderer with no cluster to look up (Argo CD)
  sees a different Secret each time. Set `grafana.admin.existingSecret`.
  The login form is disabled either way; the admin account is only a
  break-glass.

## Refusals

| Condition | Why |
|---|---|
| `grafana.sidecar.dashboards.provider.updateIntervalSeconds` of 10 or less | dashboards never update |
| no `default` store, or more than one | the datasource variable has no starting point |
| a datasource uid produced twice | the database refuses the second row, pods crash-loop |
| a `renamedDatasources` entry equal to a current datasource name | deleted and recreated on every start |
| no `oauth.issuer`, `clientId`, `clientSecretRef.name` or `roleAttributePath` | nobody could sign in |
| no `rootUrl` | sign-in never completes |
| no `database.host` or `database.secretRef.name` | replicas cannot share state |
| no `secretKeyRef.name` without `builtInKey: true` | the built-in signing key |
| `use_refresh_token` or `role_attribute_strict` overridden off | described above |
| an unknown key anywhere in `global.observabilityGrafana` | a typo that would apply nothing |

Each has a fixture under `tests/invalid/observability-grafana/`, and
`hack/lint-fixtures.sh` checks it fails for the refusal it is named for.

# Changelog

Prose bullets, written for the consumer: what changes in the render, what
must be done first, and whether a default moved. Newest first, one
`## vX.Y.Z` heading per tag.

## Unreleased

- **Behaviour change** `pkg/statusbox/ec2`: the rendered Gatus configurations, the extra CA bundle and the setup script are no longer in the user-data. With an estate's catalogue (two near-identical configs, SSH and `SelfRegister` on) the user-data was over 17 KB against EC2's 16,384-byte limit, because gzip+base64 inside a gzipped bootstrap saves nothing and the duplication was paid twice. `NewEC2` now writes each as an encrypted, content-addressed object in the box's own bucket, `s3://<Bucket>/<BucketPrefix>/config/<sha256>.<yaml|pem|sh>` (`aws.s3.BucketObjectv2` children of the component, `Box.ConfigObjects`; the launch template and group depend on them), and the user-data carries only each object's key and sha256. The box fetches them with the instance role before Gatus starts, verifies the digest and, on a failure or mismatch after five attempts, abandons its launch so a bad config never goes InService; a warm-pool instance fetches at its first boot. A changed config still changes the user-data and rolls the instance. No IAM change (the existing `<BucketPrefix>/*` `s3:GetObject` covers `config/`); the Pulumi caller must be able to `s3:PutObject` there (and use `KMSKeyARN` when set). An instance may no longer be named `config`. No secret is in the objects: configs reference SSM-backed `${ENV}` names as before. The first deploy after upgrading replaces the instance (new launch template version). A new test renders an estate-sized catalogue (about 40 endpoints, both instances, Telegram, ping, SSH, SelfRegister) and asserts the user-data is at most 12 KiB. See `docs/statusbox.md`, "Configs and the setup script are S3 objects".

## v0.66.0

- **Added** `pkg/statusbox/ec2`: optional `Args.SelfRegister` (`deploy/pulumi/status`: `EC2Inputs.SelfRegister`, an `*ec2.SelfRegisterArgs` with `RoleARN`, `HostedZoneID`, `RecordName` and `TTL`, default 60). The instance role gains exactly one `sts:AssumeRole`, on `RoleARN`. At every boot into service the box reads its private IP from IMDSv2, assumes that role with the AWS CLI and UPSERTs the A record, three attempts, fail-safe. It runs as an `ExecStartPost` of `statusbox-boot.service`, after the lifecycle hook is completed, so it never delays InService, and only when the target lifecycle state is `InService`, so warm-pool pre-warming never registers. The name therefore follows every replacement and warm-spare takeover. About 1.7 KB of user-data. The cross-account role (trusting the instance role, limited to that one record) must exist before a box boots with the feature; otherwise the box logs the failures and the name stays stale. Nil changes nothing and every golden is byte-identical. See `docs/statusbox.md`, "Self-registered DNS name".

## v0.65.0

- **Added** `pkg/statusbox/ec2`: optional `Args.SSH` (`deploy/pulumi/status`: `EC2Inputs.SSH`, an `*ec2.SSHArgs`) gives the box SSH through opkssh and OpenBAO host certificates, rendered by `pkg/hostaccess` of `github.com/truvity/tailscale` v1.24.2 (a new module dependency; `ec2.HostaccessVersion` must equal it). Inputs: `OPKSSH` (issuer, client id, expiration, user, group), `HostCert` (OpenBAO address, CA bundle, namespace, auth mount and role, server-ID header, ssh mount and role, principal patterns) and `IngressCIDRs` for TCP 22. The bootstrap writes `/etc/hostaccess/*` and runs the setup script (downloaded from the release, sha256-checked) before the install phase that completes the lifecycle hook, fail-safe and time-boxed: a failure leaves no SSH, never an unready box. It adds about 1.1 KB to the user-data (11,582 bytes with SSH in the test fixture, limit 16,384). Needs a Graviton `InstanceType`; adds no IAM (OpenBAO's AWS auth uses `sts:GetCallerIdentity`); the box needs egress to github.com, which the security group already allows. With SSH set the security group gains TCP 22 from `IngressCIDRs` and its description changes, which replaces the group; nil changes nothing, and every golden is byte-identical. See `docs/statusbox.md`, "SSH (optional)".
- **Added** `pkg/statusbox`: a platform host can declare its own probe. `Catalogue.HostProbes` (`map[string]Probe`, keyed by the `PlatformHosts` entry; `deploy/pulumi/status`: `Inputs.HostProbes` and `CatalogueInputs.HostProbes`) carries `Probe{Path, ExpectStatus}`; the endpoint URL becomes `https://<host><Path>` and the condition `[STATUS] == <ExpectStatus>`, with the certificate-expiry condition kept. For a host that is POST-only or path-only by design, whose healthy answer to a GET is an error status (for example 404, which still proves DNS, TLS and the route). A company host gains the same through `CompanyHost.ExpectStatus` (`HostGroup.ExpectStatus`), applied when `StatusPath` is set. A host with no entry and a zero `ExpectStatus` renders exactly as before, so every golden is unchanged.

## v0.64.0

- **Added** `platform-alerts`: `groups.pulumiDrift` (off by default), three alerts over the gauges a scheduled job pushes once per run, `pulumi_stack_drift_changes{scope,stack}` and `pulumi_stack_diff_error{scope,stack}`: `PulumiDriftDetected` (changes planned for `for`, default 2d), `PulumiDiffFailing` (the preview itself failed for `for`) and `PulumiDriftSignalStale` (no sample for `staleAfter`, the deadman, read off the error gauge, which is sent for every stack on every run). `runbookUrl` sets one runbook link for all three. The expressions read `last_over_time` over `lookback` because the series are pushed, not scraped. New values `groups.pulumiDrift.*`; the schema gains the key. Nothing renders unless the group is enabled, so existing installs do not move.
- **Added** `pkg/statusbox/ec2`: optional `Args.SessionManager` (`deploy/pulumi/status`: `EC2Inputs.SessionManager`). When true, the AWS managed policy `AmazonSSMManagedInstanceCore` is attached to the instance role `<prefix>-status`, so the SSM agent that Amazon Linux 2023 ships registers the instance and `aws ssm start-session` opens a break-glass shell; without it the agent logs credential errors and there is no shell. False (the default) adds nothing, so existing stacks do not change; no user-data change either way. A permissions boundary on the role must allow the `ssm:`, `ssmmessages:` and `ec2messages:` actions or the attachment grants nothing.
- **Behaviour change** `observability-stack`: every full metrics reader principal's route (the vmauth `VMUser` `src_paths`) and `pkg/tenancy`'s `MetricsReadPaths` now also admit `/prometheus/api/v1/rules`, `/prometheus/api/v1/query_exemplars` and `/prometheus/api/v1/format_query`. Grafana's Prometheus datasource calls the first two when Explore opens and the third from the format button. vmauth answered each of those calls from a verified JWT user with 401 and `WWW-Authenticate: Basic`, the same as a request with no credentials, so the browser opened a sign-in prompt that no password satisfies. The store, without `-vmalert.proxyURL`, answers `rules` and `query_exemplars` from empty placeholders and does not implement `format_query` (400 `unsupported path requested`), so the change reads no series. `metricsQueryOnly` readers are unchanged. Every golden with a reader `VMUser` gains the three lines. No values change, and no value restores the previous render: the paths are fixed, as the rest of the read route is. To keep the old routes, stay on the previous release.
- **Added** `rulecheck` semantic lint, run by `just rulecheck` and `go run ./cmd/rulecheck` after the parse check and on every rendered golden. Parsing proves a rule is valid, not that it is right on a store holding several clusters; four checks hold the rules to the shapes releases 0.43.3, 0.44.1, 0.45.1 and 0.46.0 had to fix. `cluster-label`: an aggregation (`sum`, `avg`, `max`, `count`, `group`, ...) must keep the cluster label (`by (...)` names it, `without (...)` does not drop it), and an `on (...)` match must include it; an empty `on()` and anything under `scalar()` are the deliberate whole-store forms and pass. `absent-guard`: every `absent()` must carry the platform-alerts guard, the whole-store `absent(sel) unless on() group(max_over_time(sel[..]))` as one arm of an `or` whose other arm is the per-cluster `unless` over `group by (<cluster>)`, with `<cluster>!=""` on both sides (v0.44.1). `pod-heartbeat`: a heartbeat or deadman alert must read its series through `sum without (pod, instance)` (or a `by` without them), and an `*Absent` alert must not aggregate `by (pod)` (v0.43.3). `source-absent`: every metric a self-alert reads must be watched by `SelfAlertSourceAbsent` (kube-state-metrics, cAdvisor and node-exporter families excepted, as the chart does), and with `-require-source-absent` a self-alert group without that rule fails too (v0.46.0). The cluster checks apply to a VMRule that names the cluster label somewhere, so a render with the label switched off is left alone; `-cluster-label` names another label, `-no-lint` runs the parse check only. Intentional exceptions are `DefaultAllowlist` entries in `pkg/rulecheck/lint.go`, each with its reason: `WritePathDead` (sums one store's own counter on purpose) and the `SlackNotificationsFailing` source (Alertmanager's own counter, not a `selfAlerts` value). No chart render changes from this alone. The lint reads rules with `github.com/VictoriaMetrics/metricsql`, a new module dependency.
- **Added** `rulecheck coverage`: per cluster, the rule groups and rules the catalog has and that cluster does not render (disabled), and the self-alert sources without `sourceAbsent` coverage. A bare file is a cluster, a bare directory is one cluster per YAML file, `name=path[,path]` names and merges; `-catalog` adds a render with everything on; `-format markdown|json`, `-o file`. `just rulecheck-coverage` runs it over the goldens, and CI's new `rulecheck-coverage` job uploads `coverage.json` and `coverage.md` as the `rulecheck-coverage` artifact and prints the report in the job summary.
- **Behaviour change: `observability-stack`'s `StoreReplicaDivergence` alerts keep the cluster label.** `MetricStoreReplicaDivergence`, `LogStoreReplicaDivergence` and `TraceStoreReplicaDivergence` (`selfAlerts.divergence.enabled`, the primary of a pair) summed each replica's rows rate with a bare `sum(...)`, which dropped `tenancy.clusterLabel` (default `k8s_cluster_name`), so the alert arrived with no cluster and could not be routed or told apart (the v0.45.1 class, missed there). Both sides now read `sum by (<clusterLabel>) (...)`, so they still match one to one and the alert carries the label; with `tenancy.clusterLabel` empty the expression is unchanged. Thresholds, window and `for` are unchanged. Found by the new `cluster-label` check.

## v0.63.1

- **Added** `pkg/statusbox/ec2`: optional `Args.PermissionsBoundary` (`deploy/pulumi/status`: `EC2Inputs.PermissionsBoundary`), the full ARN of an IAM permissions boundary set on the instance role `<prefix>-status`. Accounts that deny `iam:CreateRole` for a role without their boundary failed the apply; set it there. Empty keeps the role as before, so existing stacks do not change. The Lightsail backend creates no IAM role.
- **Fixed** (security): `golang.org/x/net` v0.59.0 -> v0.60.0 and the toolchain go1.27.1 -> go1.27.2, closing GO-2026-6617 and GO-2026-6613 (HTTP/2 in `golang.org/x/net` and in the standard library's `net/http`). No render change.

## v0.63.0

- **Added** `pkg/statusbox`: optional Telegram alerting. `Catalogue.Telegram` (`TelegramProvider`: two `Secrets.AlertURLs` keys, the bot token and the chat id) renders Gatus's native `alerting.telegram` and adds a `telegram` alert (same failure threshold, send-on-resolved) to every endpoint the deadman group pages through; `Providers.Telegram` adds the company signals too. Nil renders exactly what it did.
- **Added** `deploy/pulumi/status`: `EC2Inputs.TelegramTokenParameter` and `TelegramChatIDParameter` (SSM SecureStrings, both or neither; Lightsail: `Inputs.TelegramToken` and `TelegramChatID`) enable it on the paging instance.
- **Added** `pkg/statusbox/ec2`: `Args.PingURLParameter` (`EC2Inputs.PingURLParameter`), an SSM SecureString with a healthchecks.io-style ping URL. A systemd timer GETs it every 60 s while every local Gatus answers `/health`, and `<url>/fail` otherwise; the URL lives only in a root-only file under `/run`. Unset installs no units. The role may read the extra parameter. The rendered user-data changes by one `SB_PING_PARAM` line and the setup script.

## v0.62.1

- **Behaviour change** (fix(alert-ingress)): in `sqs` and `both` mode the NetworkPolicy also allows TCP 80 to the EKS Pod Identity agent (`169.254.170.23/32`), which serves the pod's AWS credentials over plain HTTP on a link-local address. Without it, a CNI that enforces the policy blocks the credential call and every queue receive fails with `dial tcp 169.254.170.23:80: i/o timeout`. New value `networkPolicy.egress.podIdentityAgent` (default `true`); set it `false` when the pod gets credentials another way (IRSA needs only the 443 rule). `http` mode renders as before.

## v0.62.0

- **Behaviour change** `alert-ingress`: in `sqs` and `both` mode, a `SubscriptionConfirmation` found in the queue is now confirmed when its SNS signature verifies and its topic is on the allow-list (the same `SubscribeURL` host check as the webhook), then deleted and counted in the new `alert_ingress_sqs_confirmed_total`; a failed confirmation leaves the message for retry and the dead-letter queue. Before, such messages were deleted unconfirmed, which left a cross-account subscription made by the topic owner pending. Opt out with `input.sqs.confirmSubscriptions: false` (new, default `true`; it renders into the config in `sqs` and `both` mode, so those goldens move; `http` mode renders as before). Unsubscribe confirmations are still logged and deleted. See [docs/alert-ingress.md](docs/alert-ingress.md#sqs-input).

## v0.61.0

`pkg/statusbox/ec2` (new): an EC2 backend for the status box, beside the Lightsail one, which is unchanged and stays the default. `ec2.NewEC2` creates a launch template (Amazon Linux 2023, IMDSv2, `t4g.nano` by default), an instance profile, a security group and an Auto Scaling group of exactly one instance in two zones with a warm pool of one stopped instance. Gatus and cloudflared run as systemd units, with no Docker and no Tailscale; Gatus's SQLite databases are replicated to an S3 bucket by Litestream, restored when the instance goes in service, never while it sits in the warm pool. Secrets are not in user-data: `Args` takes SSM Parameter Store names, which the instance reads at boot through a role scoped to exactly those parameters, the bucket prefix and the group. A local timer marks the instance unhealthy when Gatus or cloudflared stay down. See [docs/statusbox.md](docs/statusbox.md) ("EC2 backend"). `pkg/statusbox` gains small exported helpers for the second backend (`ValidateInstances`, `ValidateSecretNames`, `WrapUserData`, `ChecksumFor` and others); `Args.validate` and `CloudInit` render exactly what they did.

`deploy/pulumi/status`: `Inputs.Backend` (new, `lightsail` by default, `ec2` opts in) with `Inputs.EC2` (VPC and subnet ids, peer CIDRs, replica bucket and prefix, optional KMS key and instance type, and the SSM parameter names of the tunnel token, the OIDC client secret, the alerts-read token and the deadman token). With the field empty the render and resources are exactly what they were; with `ec2` no tailnet key is minted and no secret value is an input. The release now also attaches the Gatus binaries the EC2 box installs, `gatus_<tag>_linux_{arm64,amd64}`, built from the upstream tag `setup.sh` pins and listed in `checksums.txt`.

## v0.60.0

`deploy/pulumi/alertqueue` (new): a Pulumi Go package that provisions the queue alert-ingress polls. `alertqueue.Deploy` creates one standard SQS queue and its dead-letter queue (SQS-managed SSE, or a customer key through `KMSKeyARN`), the redrive policy (`MaxReceiveCount` default 100), a visibility timeout (default 300 s), 14-day retention on both queues, and a queue policy that allows `sqs:SendMessage` from SNS only when `aws:SourceArn` is one of `TopicARNs` (any account or region). An optional `AlarmTopicARN` adds a CloudWatch alarm on the dead-letter queue's depth with ALARM and OK actions. It outputs the queue ARN and URL, the dead-letter queue ARN and a ready IAM policy document for the polling role; it creates no role, no Pod Identity association and no SNS subscription (the topic owners subscribe the queue). Inputs are validated. Nothing in any chart render changes. See [docs/alert-ingress.md](docs/alert-ingress.md#provisioning-the-queue-with-deploypulumialertqueue). The queue policy example under "SQS input" named the wrong action (`sns:SendMessage`); it is `sqs:SendMessage`.

`alert-ingress`: an SQS input mode. `input.mode` is `http` (the default, so an existing install renders byte-identical), `sqs` or `both`; with `sqs` or `both` the pods poll one SQS queue that SNS topics publish to (raw message delivery off) and put each message through the same pipeline as the webhook: signature, topic allow-list, mappings, Alertmanager. A message is deleted only after Alertmanager accepts the alert, so a failure retries after the visibility timeout and the queue's redrive policy decides when it goes to the dead-letter queue; messages that cannot succeed and carry nothing (malformed, unsupported type) are deleted and counted, while those rejected for an unknown topic or a bad signature are counted and left on the queue so the redrive policy moves them to the dead-letter queue (recommended: a high `maxReceiveCount`). Confirmation messages in the queue are logged and deleted, never followed. Credentials are the default AWS chain only (Pod Identity, or IRSA through the new `serviceAccount.annotations`). New values: `input.*`, `networkPolicy.egress.sqs`, and two rules, `rules.sqsReceiveFailing` and `rules.sqsMessageAge`, both off by default. New series `alert_ingress_sqs_*`. The new keys are not in older chart versions' schema, so a consumer sets them only after bumping its pin to this release. See [docs/alert-ingress.md](docs/alert-ingress.md#sqs-input).

## v0.59.0

`pkg/tenancy`: `DeriveMachineReaders` and `DeriveStoreReaders` (new). From an estate's machine identities (groups of the form `<cluster>:<namespace>:<role>`) they derive which workloads may read the stores: one namespace of one cluster for a given role, or every namespace of every served cluster for a connector role. The roles and the middle segment are inputs; the output carries yaml tags matching the telemetry proxy's reader values. Nothing in any render changes.

## v0.58.0

`pkg/statusbox`: `Spec.Validate` (new). An estate's status pages (entities by code, the public page's OIDC issuer and client, the tunnel's hostnames) are checked when the estate's configuration is loaded, by the rules `Args.validate` applies at deploy time: a public page needs an issuer and a client, an entity code is a valid instance name, each entity has a display name and a hostname, and the tunnel carries only hostnames an entity has. Nothing in the render changes.

## v0.57.0

`observability-portal` (new chart) and `apps/portal` (new app): the entrypoint
page of an estate. A TypeScript, React and Vite single-page app with a generic
built-in catalog and no estate data, behind an unprivileged nginx, published as
`ghcr.io/truvity/observability/portal` at every tag beside the charts. An estate
adds its entries and sections through `portal.extraEntries`, `orientation`,
`commandLine` and `guides` in the chart's values, which render a ConfigMap the
page loads at runtime as `/config/portal.json`; the document has a JSON Schema
(`apps/portal/schema/portal.schema.json`) the page, the chart and the tests all
use. See [docs/portal.md](docs/portal.md). No existing render changes. The
toolchain adds `nodejs` 26 and `yarn-berry` 4.14.1 to `devbox.json`.

## v0.56.1

`deploy/pulumi/status` requires `pulumi-tailscale/sdk` v0.29.0 (it had pulled in
v0.29.1), so a consumer on v0.29.0 keeps its provider plugin version.

## v0.56.0

`observability-projects` (new chart): the generic alerts of every project
namespace from a list of rows (`projects: [{name}]`), for an L3 `-projects`
Application: restarts (OOM kill, restart rate, slow crash loop) and a
Deployment with no available replica, one VMRule, the namespace matcher built
from the rows. The same rules as `platform-alerts`' `restarts` and
`workloadAbsent` groups, which a consumer moving onto this chart turns off.

## v0.55.0

- **feat(charts): chart presets, values files shipped inside the chart.** A
  subchart's values cannot be computed from the parent's, so a value that is
  the same on every estate of a given shape used to be written out again in
  every consumer. Each file in `charts/<chart>/presets/` is a values file a
  consumer lists BEFORE its own (Argo CD `helm.valueFiles: [presets/<name>.yaml]`,
  or `helm -f`); its own values still win key by key. Nothing reads a preset
  unless it is listed, so no default moves and every existing render is
  byte-identical (the existing goldens do not change). New:
  `observability-stack` `operator-only` (the `mode: operator-only` explicit
  `false`s), `self-alerts-victoria` (the verified metric names of
  every `selfAlerts` rule that has an upstream series), `rules-no-apiserver`
  and `rules-on-demand-nodes` (the vendored rules a managed control plane and
  an on-demand node fleet cannot feed), `notifications-drop-vendored` (the
  vendored alert groups kept out of routing; a list, so it replaces your own
  `notifications.drop`), `scheduling-durable` and `scheduling-arm64`;
  `observability-emitters` `local-write` (writer wiring to a stack installed in
  the same cluster with the stack's default names, including the container-log
  agent's mounts and `remoteWrite`), `logs-collector-remote` (the log agent's
  mounts for the `remote` form), `scheduling-durable` and `scheduling-arm64`;
  `observability-grafana` and `observability-rum` `scheduling-arm64`;
  `platform-alerts` `stores-victoria` and `keep-cluster-label`. The log agent's
  `collector.extraFields` still mirrors `tenancy` in your values: Helm cannot
  derive a subchart value. Each preset case in `tests/cases` is rendered beside
  the same values written out in full and the two goldens must be equal
  (`tests/presets_test.go`); `hack/golden.sh` takes a `presets` file per case.

## v0.54.0

`deploy/pulumi/status`: the status box's Pulumi mechanism moves in from the
estate repository (no chart change). It renders both Gatus instances from one
catalogue (`OpsCatalogue`, `PlatformHosts`, `HostsByCompany`), mints the box's
one-shot tailnet join key and creates the Lightsail machine. Providers, host
groups, secrets and the tunnel token are the caller's `Inputs`.

`deploy/pulumi/credentials`: the install's credentials (store basic-auth
password, write token, the management cluster's status-box and evaluator
bearers, the metrics-backup Pod Identity role) move in from the estate
repository (no chart change). Resource types and logical names are unchanged;
rotation levers, SSM paths and namespaces are the caller's `Inputs`.

Pulumi packages move in from the estate repository (no chart change):
`deploy/pulumi/releaseassets` (verified release-asset download),
`deploy/pulumi/otlplayer` (publishes the OTLP Lambda layer) and
`deploy/pulumi/lambdaprobe` (the layer plus a heartbeat probe function; the
caller passes a `Config`, the permissions-boundary ARN and the provider).
Resource types and logical names are unchanged from the code they replace.

## v0.53.0

- **Behaviour change (feat(observability-emitters)): more per-node series
  dropped on CI node pools.** Only on nodes of `kubeletDrop.ciNodePools`
  (default empty, so nothing moves without it): the `_bucket` series of
  `kubelet_pod_worker_duration_seconds`, `kubelet_cgroup_manager_duration_seconds`,
  `kubelet_pod_start_duration_seconds`, `kubelet_pod_start_total_duration_seconds`,
  `kubelet_pod_start_sli_duration_seconds`, `kubelet_image_pull_duration_seconds`,
  `dra_operations_duration_seconds` and
  `authentication_token_cache_request_duration_seconds` (`_sum`/`_count` stay;
  `ciNodePoolBucketMetrics`), and, new, the cadvisor `container_fs_*` series
  and the per-interface network packet/error counters
  (`ciNodePoolCadvisorMetrics`; `container_network_*_bytes_total` stay). The
  kubelet dashboard's quantile panels for the dropped histograms and the
  packet/error network panels have no data for short-lived CI nodes; long-lived
  nodes are unchanged. Reason: every replaced CI node minted ~2k such series,
  which on a CI-heavy store was most of the day's new series.

- **feat(observability-stack): a per-cluster threshold for
  `TooHighChurnRate24h`.** The vmsingle self-monitoring alert fires when 24h
  of new series exceeds three times the hourly active series, which a store
  holding short-lived CI workloads reaches without anything being wrong. Set
  `victoria-metrics-k8s-stack.defaultRules.rules.TooHighChurnRate24h.spec.expr`
  to upstream's expression with a higher factor (see `docs/reference.md`).
  The alert keeps its name, `for`, labels and annotations, so silences and
  routes still match. Nothing is set by default: the render is unchanged and
  upstream's factor of 3 applies. (A chart-level factor value is not
  possible: the sync job reads the rule override from the subchart's own
  values, which Helm cannot compute from the parent's.)

## v0.52.1

- Dependency updates.

## v0.52.0

- **Behaviour change (feat(observability-emitters)): no pod-level
  kube-state-metrics series for CI namespaces.** By default every
  `kube_pod_*` series of a pod in a namespace matching `arc-runners-.*|ci-.*`
  is dropped at scrape time, except `kube_pod_status_unschedulable`,
  `kube_pod_container_status_last_terminated_reason` and
  `kube_pod_container_status_restarts_total` (what the PodUnschedulable,
  ContainerOOMKilled and restart alerts read). Other namespaces and the
  non-pod kube-state-metrics families are untouched. In those namespaces the
  upstream KubePodCrashLooping, KubePodNotReady and KubeContainerWaiting
  alerts have nothing to read, the `kube_pod_info` based namespace and pod
  selectors of the Kubernetes views dashboards stop listing them, and the
  `k8s.rules` recordings that join the pod series have no output there.
  Opt out with `metrics.scrape.kubeStateMetricsDrop.ephemeralNamespaces: ""`
  (the previous render, byte for byte) or `enabled: false`; widen the keep
  list with `extraEphemeralKeepMetrics`.

## v0.51.1

- **Behaviour change (fix(observability-emitters)): the CI node-pool helper
  label never reaches storage.** Only with `ciNodePools` set; leaving it
  unset restores the previous render. `metrics.scrape.kubeletDrop.ciNodePools` (v0.51.0) copies the
  node's pool into the temporary target label `kubelet_ci_nodepool_tmp` and
  dropped it in the job's `metric_relabel_configs`, which never apply to the
  series the agent generates per target (`up`, `scrape_duration_seconds`,
  `scrape_samples_scraped`, ...); those stored the label, on every pool, and
  so did recording rules built from them. The agent's global
  `inlineRelabelConfig` now ends with a `labeldrop` of it, rendered only when
  `ciNodePools` is set (the render is otherwise unchanged) and kept when
  `metrics.spec` sets its own `inlineRelabelConfig`. Series already stored
  keep the label until they age out.

## v0.51.0

- **Behaviour change (feat(observability-emitters)): less series churn from
  CI pods and replaced nodes.** Default drops on the node scrapes.
  (1) The kubelet job no longer stores eight histogram `_bucket` families
  that no shipped rule or dashboard reads
  (`rest_client_rate_limiter_duration_seconds`,
  `rest_client_response_size_bytes`, `rest_client_request_size_bytes`,
  `volume_operation_total_seconds`, `kubelet_http_requests_duration_seconds`,
  `csi_operations_seconds`, `workqueue_queue_duration_seconds`,
  `workqueue_work_duration_seconds`); their `_sum` and `_count` stay, and the
  buckets the kubelet dashboard takes a quantile of stay too. (2) The
  cadvisor job, in namespaces matching `arc-runners-.*|ci-.*`, keeps only
  CPU usage, working-set memory, OOM events, CPU throttling and network
  series (the ones a rule or dashboard reads) and drops everything else,
  including memory usage, cache, rss and swap. Other namespaces are
  untouched. Opt out with `metrics.scrape.kubeletDrop.bucketMetrics: []` and
  `metrics.scrape.cadvisorDrop.ephemeralNamespaces: ""`; widen with
  `extraBucketMetrics` / `extraEphemeralKeepMetrics`. New and off by
  default: `metrics.scrape.kubeletDrop.ciNodePools` (Karpenter NodePool
  names); on those nodes only, the `_bucket` series of the three kept
  kubelet histograms are dropped too. A render that sets none of the new
  values to a non-default is unchanged by that key.

## v0.50.1

- **Behaviour change (fix): `PodSecurityAuditViolations` counted the audit
  reader's own startup log.** The expression matched the phrase
  `pod-security.kubernetes.io/audit-violations` anywhere in a log line, and
  the CloudWatch reader logs its OTTL config, which contains that phrase,
  at every start: each restart counted as a violation. It now filters on the
  field `audit.violations:*`, which only a forwarded audit event carries, and
  still groups by cluster and `audit.namespace`. The alert's name, labels and
  window are unchanged.

## v0.50.0

- **feat: Pod Security audit violations from an EKS audit log.**
  `observability-emitters` gains `cloudwatchLogs` (off by default): a
  one-replica reader of an AWS CloudWatch log group through the
  `awscloudwatch` receiver, which keeps only the events carrying a phrase
  (default `pod-security.kubernetes.io/audit-violations`) and writes them to
  the gateway's log destinations under a namespace of its own
  (`kube-audit`), with the event's namespace in `audit.namespace`.
  `platform-alerts` gains `groups.podSecurityAudit` (off by default), the
  warning `PodSecurityAuditViolations`, per cluster and namespace over 24h.
  Both are new keys, refused by an older chart's schema, so gate their use on
  the pin; an install that sets neither renders exactly as before.

## v0.49.0

- **Behaviour change (feat): `platform-alerts` `groups.restarts` gains
  `ContainerRestartingSlowly`, a slow crash
  loop.** A container that dies every hour or two restarts successfully each
  time and resets its back-off, so neither the upstream `KubePodCrashLooping`
  nor `ContainerRestartingOften` (1h window) ever fires. The new alert, in
  `groups.restarts`, fires for 15m when
  `increase(kube_pod_container_status_restarts_total[6h]) > 3`, per cluster,
  namespace, pod and container, severity `warning`. New keys, all in
  `groups.restarts`: `slowRestartWindow` (6h), `slowMaxRestarts` (3),
  `slowRestartFor` (15m), `slowRestartSeverity` (warning) and
  `slowRestartExcludeNamespaces`, a regular expression of namespaces to skip,
  default `arc-.*|ci-.*` (CI runner namespaces restart pods by design); set
  it empty to exclude nothing. The group stays off by default, so an install
  that has not enabled `groups.restarts` renders exactly as before; one that
  has gains this alert. The chart schema accepts the new keys; an older
  chart's schema refuses them, so gate their use on the pin.

## v0.48.0

- **feat(alert-ingress): CloudWatch alarm notifications map to Alertmanager
  alerts, and a mapping can resolve one.** An alarm's SNS message (JSON:
  `AlarmName`, `NewStateValue`, `NewStateReason`, `Trigger`, ...) matches
  with an ordinary mapping; the documented one
  ([docs/alert-ingress.md](docs/alert-ingress.md#cloudwatch-alarms)) sets
  `alertname` to the alarm name, severity from `severity=` in the alarm
  description (default `warning`), `source=cloudwatch`, `account` and
  `region` labels and the reason as an annotation. Two new optional fields
  make it possible, and an install that sets neither renders and behaves
  exactly as before:
  - `mappings[].alert.resolved`: a template; when it renders to `true` the
    alert is posted already ended, clearing the alert with the same labels.
    `OK` resolves, `ALARM` and `INSUFFICIENT_DATA` fire. Anything but an
    explicit `true` leaves the alert firing.
  - `mappings[].resolveAfter`: a Go duration (no `d`) overriding the
    top-level `resolveAfter` for that mapping, because an alarm sends one
    message per state change and the 1h default would end the alert of an
    alarm still in `ALARM`.
  - The chart schema accepts both. An older chart's schema refuses both
    keys, so a consumer gates their use on the pin.

## v0.47.0

- **feat(lambdaext): the generic Lambda OTLP extension now lives here.** It
  moved from `truvity/access-roster` (`internal/lambdaext`), because it is
  not specific to that issuer: any Lambda function in any language can
  export OpenTelemetry data with its IAM role's identity and no stored
  secret. It asks STS for the role's identity token
  (`sts:GetWebIdentityToken`), trades it at the issuer (RFC 8693) for a
  token audienced at the OTLP endpoint, runs an OTLP/HTTP proxy on
  `127.0.0.1:4318` that adds the bearer, and forwards Lambda Telemetry API
  platform events as OTLP logs. The protocol and every `ACCESS_ROSTER_*`
  setting are unchanged, so a function moves between the two layers by
  swapping the layer alone.
  - New public package `github.com/truvity/observability/lambdaext` and
    command `cmd/otlp-lambda`. The token exchange is a small client inside
    the package: this module does not import access-roster.
  - The release now attaches `otlp-lambda-layer_<version>_linux_amd64.zip`
    and `..._linux_arm64.zip` (root: `extensions/otlp-lambda`), covered by
    `checksums.txt`. The release publishes no layer version; the consumer
    does, from the zip.
  - See [docs/integrations/aws-lambda.md](docs/integrations/aws-lambda.md).
    No chart render changes.

## v0.46.0

- **feat(observability-stack): opt-in `selfAlerts.sourceAbsent`.** Every
  self-alert reads `rate()` of a store or writer counter, and an empty
  vector reads as healthy, so the day the scrape stops (a NetworkPolicy
  refusing the scraper, a relabel dropping the target) they all go quiet at
  once. `selfAlerts.sourceAbsent.enabled: true` renders `SelfAlertSourceAbsent`:
  per cluster, a source series seen within `lookback` (default `1d`) that has
  none now, with `tenancy.clusterLabel` required non-empty on both sides, plus
  a whole-store `absent()` branch (the `platform-alerts` guard shape,
  v0.44.1). It watches every metric name set under `selfAlerts` (stores,
  cardinality, stream churn, writers, gateway, proxy, disk guard) and carries
  a `source` label naming the one that went. Values: `enabled` (default
  `false`, so no render changes), `lookback` (`1d`), `for` (`15m`),
  `severity` (`warning`). Refused with `selfAlerts.enabled` off, or with no
  metric name set. A metric name that never existed on the store fires the
  whole-store branch, so unset names you do not want watched.

## v0.45.1

- **Behaviour change: `observability-stack` self-alerts keep the cluster
  label.** On a store that holds several clusters' series, the self-alert
  expressions that aggregated with `sum(...) by (reason|url|exporter|
  integration|pod)`, or with a plain `sum(rate(...))`, dropped
  `tenancy.clusterLabel` (default `k8s_cluster_name`), so a remote
  cluster's writer or gateway alert arrived with no cluster and could not be
  routed or told apart. They now aggregate with `sum without (pod, instance)`
  (the v0.43.3 pattern; `RemoteEvaluatorFailing` keeps `pod` and drops only
  `instance`), so the cluster label survives along with the labels the old
  `by (...)` kept (`reason`, `url`, `exporter`, `integration`). Affected:
  `MetricStoreIgnoringRows`, `LogStoreDroppingRows`, `TraceStoreDroppingRows`,
  `LogStoreStreamsChurning`, `TraceStoreStreamsChurning`,
  `WriterDroppingPackets`, `GatewayExportFailing`, `GatewayEnqueueFailing`,
  `ProxyAtConcurrencyLimit`, `SlackNotificationsFailing`,
  `RemoteEvaluatorFailing`. Thresholds, windows and `for` are unchanged; an
  alert that fired before fires now, with extra labels (the series' other
  labels such as `job` and `namespace` also survive, where a bare `sum()`
  dropped them). The rendered rules change in every case that renders them.

## v0.45.0

- **feat(alert-ingress): static labels on unmapped events.** A new
  `unmapped` value (`unmapped.labels`, a map of static strings, and
  `unmapped.severity`) is added to every `CloudEventUnmapped` alert, so the
  routing tree can route it, for example on the same `k8s_cluster_name` key
  the mappings use. Names must be Prometheus label names; `alertname`,
  `severity` and names starting with `__` are refused, at render time and
  again when the binary loads its configuration. The defaults (no labels,
  severity warning) render exactly what earlier versions did.

## v0.44.1

- **Behaviour change: `platform-alerts` absent guards ignore series without
  the cluster label.** With `clusterLabel` set, every `*Absent` alert (the
  probe, ACK, ESO, Argo CD, NATS, node-claim and Kargo ones) compared the
  series seen within `absentLookback` against the ones present now, per
  cluster. A stale series that lacks the cluster label (left over from
  before a relabel) landed in the lookback side under a label set the
  current side could never match, so the alert fired, with the probe
  healthy, until it aged out of the window. The per-cluster comparison now
  requires `<clusterLabel>!=""` on both sides; the whole-store `absent()`
  line is unchanged. Alerts for a series that was labelled and stopped
  still fire. No opt-out: the previous expression paged on a healthy
  target.

## v0.44.0

- **`platform-alerts` gains four generic groups, all off by default, and
  the backup rules their own namespace selector.** Nothing changes until a
  group is enabled. Every expression aggregates by the cluster label, and
  each group has `keepClusterLabel` for a store that evaluates several
  clusters' series.
  - `groups.envoyRoutes`: per-HTTPRoute 5xx ratio
    (`EnvoyRoute5xxRatioHigh` / `...Critical`, 5% / 25%) and p99 upstream
    latency (`EnvoyRouteP99LatencyHigh` / `...Critical`, 2s / 10s), read from
    Envoy Gateway's `envoy_cluster_upstream_rq_xx` and `_rq_time_bucket`
    (one Envoy cluster per route rule, `httproute/<ns>/<route>/rule/<n>`),
    with a minimum-traffic guard (`minRequestsPerSecond`).
  - `groups.certificates`: cert-manager `CertificateExpiringSoon` (14d),
    `CertificateExpiryCritical` (3d) and `CertificateNotReady` (15m).
  - `groups.restarts`: `ContainerOOMKilled` and `ContainerRestartingOften`.
  - `groups.workloadAbsent`: `DeploymentNoAvailableReplicas` (no available
    replica for 10m while spec replicas is above zero).
  - `groups.backups.namespaceSelector` (empty inherits the top-level
    `namespaceSelector`, so the render is unchanged by default) lets the
    CronJob and Job-failure rules cover namespaces the volume rules should
    not. `groups.backups.keepClusterLabel` (default false) is the same
    switch the other groups have.

## v0.43.3

- **Behaviour change: `alert-ingress`'s `AlertIngressHeartbeatMissing` sums
  the heartbeat counter across replicas.** The provider delivers each
  heartbeat to one pod behind the Service, so with two replicas the rule,
  evaluated per pod, fired critical for the quieter pod while heartbeats
  were arriving. The `increase` half is now `sum without (pod, instance)`;
  the `absent_over_time` half is unchanged. No opt-out: the previous
  expression paged on a healthy path.

## v0.43.2

- **Behaviour change: `alert-ingress`'s egress NetworkPolicy allows
  Alertmanager's real port.** Sprig's `urlParse` has no `port` key, so the port in
  `alertmanager.url` (`http://name.ns.svc:9093`) was never read and the
  rule fell back to the scheme default (80, or 443 for https): every post
  to Alertmanager timed out wherever the NetworkPolicy is enabled. The port
  is now split off the URL's host; a URL without one still gets the scheme
  default. No values change; there is no opt-out, because the previous
  output never let a post through.

## v0.43.1

- **Behaviour change: smctl's cache directory moves to `/cache/maps`.** In installs with
  `sourcemaps.smctl.enabled`, the rendered `cache.dir` is now `/cache/maps`
  inside the same emptyDir, so the config checksum changes and the sourcemaps
  pod rolls once. No opt-out is needed: the cache is only a cache and smctl
  empties it at every start anyway.

- fix(observability-rum): `smctl serve` no longer crash-loops at start with
  `reset cache dir: unlinkat //cache: read-only file system`. smctl empties
  its cache directory by removing and recreating it, which fails on the
  volume's mount point under a read-only root filesystem. The rendered
  config now points `cache.dir` at `/cache/maps`, a subdirectory of the
  writable emptyDir mounted at `/cache`. The mount, the size limit, the
  read-only root filesystem and the restricted security context are
  unchanged; only the ConfigMap (and so the pod's config checksum) moves,
  in installs with `sourcemaps.smctl.enabled`.

## v0.43.0

- feat(alert-ingress): AWS Budgets (plain-text) and Cost Anomaly Detection
  (JSON) alerts reach Alertmanager through the receiver. New template
  helper `reFind PATTERN TEXT` pulls a label out of a plain-text message
  (first capture group, empty on a miss). `docs/alert-ingress.md` gains the
  two example mappings (`CloudBudgetThreshold`, `CloudCostAnomaly`) and what
  the AWS side must configure. No default moves; the render is unchanged.

- **`tenancy.readers`: static-bearer, read-only metrics query readers.**
  Default `[]`: the render is byte-identical to before (all goldens
  unchanged). Each entry (`name`, `tokenSecret: {name, key}` of an existing
  Secret, `grants` shaped like a principal's) renders one VMUser,
  `<release>-reader-<name>`, whose only routes are
  `/prometheus/api/v1/query` and `/prometheus/api/v1/query_range`, with the
  grant forced onto them as literal `extra_filters` (one per cluster, ORed
  by the store). No write, log, trace, vmalert or admin route. It is the
  credential for another install's `vmalert.remoteEvaluators` entry: add a
  reader on the store being read, and an evaluator on the central install
  whose `datasource.auth.bearer` names a Secret with the same token
  (docs/notifications.md, "End to end"). Refused: empty, non-DNS-label or
  duplicate names, a missing Secret name or key, an empty or ambiguous
  grant, `mergeQueryArgs` naming `extra_filters`, and any install with no
  proxy (`mode: replica` / `operator-only`, `vmauth.enabled: false`).

## v0.42.0

- **`vmalert.remoteEvaluators`: evaluate selected rules against another
  store and notify through this install's Alertmanager.** Default `[]`:
  the render is byte-identical to before (all goldens unchanged). Each
  entry renders one extra VMAlert (`<release>-remote-<name>`) whose
  datasource is another metrics store (bearer or basic auth from an
  existing Secret, optional CA), whose notifiers are the main alerter's
  (the Alertmanager pair or `alertmanager.notifierUrl`) and whose
  remoteWrite/remoteRead are the local store. It evaluates the VMRules
  labelled `observability.truvity.io/evaluator: <name>`; once the list is
  non-empty the main metrics and logs alerters exclude every rule carrying
  that label key, so no rule has two owners. One replica per entry, no
  `-peer` twin. New self-alert `RemoteEvaluatorFailing`
  (`selfAlerts.remoteEvaluator`). Refused: empty, invalid or duplicate
  names, no `datasource.url`, auth that is not exactly one of
  bearer/basic with Secret and keys named, `notifications.mode:
  evaluate-only`, and installs that render no vmalert. The consumer opens
  egress to the other store. docs/notifications.md, "Evaluating another
  store's rules".

- **Docs: two values comments corrected, one refusal fixture added.** No
  render moves. `observability-stack`'s `mode` comment now says what each
  mode resolves the four `null` toggles to (`vmauth`, `vmalert`,
  `alertmanager`, `metricsSelfScrape`); `alert-ingress`'s `selfMonitor`
  comment no longer claims `false` is refused (it is allowed).
  tests/invalid/observability-mcp gains a fixture for a `caBundle` that
  names neither a `configMap` nor a `secret`.

- **Docs: the architecture diagrams and pages match the current charts.**
  No render moves. docs/target-state.md draws the whole estate as one
  Mermaid diagram (every emitter, the external and browser receivers, the
  store pair, Alertmanager, karma, alert-ingress, the readers, the status
  box) and the pages it links to each carry a focused one: the HA pair's
  reads and writes, the external-ingest trust boundary, the browser path
  through Alloy and `smctl serve`, the status box's pull, the router, the
  MCP pod. Stale "not yet released" and "designed" markers are gone; the
  README's status column, docs/reference.md (`notifications.mode`,
  `metricsSelfScrape`, `nodeExporter`, `metrics.scrape.nodeLabels`,
  `groups.probes.absentFor`, the dashboards chart's own values, the
  operator's `env`, the published artifacts), docs/alert-ingress.md's
  refusals, docs/dashboards.md's folders and set, docs/notifications.md's
  Alertmanager pair and self-alert list, docs/safety.md's emitter refusals
  and docs/adoption.md's upgrade steps now say what the charts do. Links
  to the retired doctrine anchors are fixed.

## v0.41.1

- **`rulecheck`: no more `text file busy` when the rule tools run in
  parallel.** Parallel callers (several tests in one process) could fork a
  child while another goroutine still had the freshly written parser binary
  open for writing, and exec of that binary failed with ETXTBSY. Binaries are
  now materialized under a process-wide lock (temp file beside, `Sync`,
  `Close`, `chmod 0755`, rename into place), and a start that still hits
  ETXTBSY is retried up to five times with a short backoff. No behaviour
  change otherwise.

## v0.41.0

- **`observability-emitters`: external OTLP ingest, `otlp.external`** (off by
  default; no existing render moves). The gateway collector accepts OTLP from
  outside the cluster (an AWS Lambda, through a public route that has already
  verified a JWT) on a SEPARATE receiver and port (`otlp/external`, HTTP,
  `httpPort` 4319, `include_metadata`) with three pipelines of their own that
  end in the same exporters. Identity comes from request headers the route
  sets (`otlp.external.headers`, header to attribute), never from the payload:
  client-supplied identity and tenancy attributes (`k8s.*`, `kubernetes.*`,
  `telemetry.source`, plus the `deleteAttributes` list: `owner`, `project`,
  `cloud.account.id`, `aws.*` role keys, `enduser.*` ...) are deleted from
  resource, scope and every record first; the headers are written in; data
  missing a required header is dropped; then the chart stamps cluster, tier,
  the static `external` namespace and `telemetry.source=external`. Renders a
  Service port, a container port and a NetworkPolicy (the in-cluster ports
  stay open to all; the external port admits only
  `networkPolicy.ingressFrom`, the gateway's Envoy pods). Refused: no header
  map, a header or static attribute writing `k8s.*`/`kubernetes.*` (or the
  chart's own stamps), the same port as an in-cluster one, empty
  `ingressFrom`. Metrics promote `owner`, `cloud.account.id` and
  `telemetry.source` to labels when this is on. The route must strip the
  client's copies of the headers first. A test runs the real rendered
  processors in the pinned collector with forged attributes. See
  docs/external-ingest.md.

## v0.40.0

- **Behaviour change: existing goldens moved.** `observability-rum`'s VMRule
  rules now carry `record: ""` (alerts) / `alert: ""` (recording rules): the
  same rules, written the way the API server stores them; no opt-out and none
  needed. `observability-stack`'s operator Deployment gains one env var,
  `VM_PROMETHEUSCONVERTERADDARGOCDIGNOREANNOTATIONS=true` (every stack golden
  that renders the operator); to restore the previous output set
  `victoria-metrics-k8s-stack.operator.env` to the four converter entries only.
  `observability-rum`'s removed `sourcemaps.sync` is below.
- **`observability-rum`: source maps from `smctl serve`; the object-store sync
  is gone.** `global.observabilityRum.sourcemaps.smctl` renders one small
  service that serves any app's source map from an OCI registry (GHCR or ECR)
  to Alloy: a ConfigMap built from the values, a Deployment
  (`ghcr.io/truvity/ocictl/smctl` 0.7.1, pinned by digest; non-root, read-only
  root filesystem, an `emptyDir` cache), a Service on `:8080`, a ServiceAccount
  and a NetworkPolicy (ingress from the Alloy pods only; egress to DNS and
  443). Alloy's `location` keeps the contract
  `http://<fullname>-sourcemaps:8080/<app>/{{ .Release }}` with each app's
  `minifiedPathPrefixes`. `smctl.repositoryTemplate` is REQUIRED (no
  organisation default in a public chart; `{app}` stands for the app's name; an
  app can override with `apps[].sourcemaps.repository`). `smctl.auth.mode` is
  `anonymous`, `ecr` (the ServiceAccount's identity: Pod Identity, or IRSA
  through `smctl.serviceAccount.annotations`) or `dockerConfig` (a Secret you
  name). Off by default. docs/frontend.md says how the release job publishes
  with `smctl push` after goreleaser.
  **Removed: `sourcemaps.sync`** (the `aws s3 sync` plus busybox pair); the
  schema now refuses the key. Nothing in this repository used it. There is ONE
  source of maps: `sourcemaps.directory` (now defaulting to empty, meaning
  `/sourcemaps`) together with `smctl.enabled` is refused.
- **`observability-rum`: the VMRule is in sync.** The CRD defaults `record` on an
  alert and `alert` on a recording rule to `""`, the API server stores them, and
  ArgoCD diffed the rendered rule against the stored one forever. Both keys are
  now written on every rule (one empty); `tests/vmrule_defaults_test.go` holds
  the goldens to it. The rendered rules mean the same thing.
- **`observability-stack`: the operator marks converted scrape objects for
  ArgoCD.** `victoria-metrics-k8s-stack.operator.env` gains
  `VM_PROMETHEUSCONVERTERADDARGOCDIGNOREANNOTATIONS=true`. The operator copies a
  converted ServiceMonitor's annotations (ArgoCD's tracking id) onto the
  VMServiceScrape it creates, which ArgoCD listed as part of the Application with
  no status; it is now `IgnoreExtraneous`. Scraping is unchanged. The operator
  Deployment restarts once.
- **`observability-dashboards`: "Frontend Issues" and "Frontend Overview".** Two
  new optional dashboards (`dashboards.frontend-issues`,
  `dashboards.frontend-overview`, off by default, folder `folders.frontend`,
  default `Frontend`) for a Grafana that loads dashboards from its own namespace
  only. They read browser telemetry from a log store through a VictoriaLogs
  `datasource` variable (default `datasources.logs`; a viewer picks another logs
  datasource) and link to traces through `datasources.traces`; the render refuses
  either empty while one of them is on. They are the same files, with the same
  uids, as `charts/observability-rum`'s own (`dashboards.enabled`, default
  unchanged): ship from one chart. `just dashboard-lint` accepts the datasource
  variable as it always did. Nothing existing renders differently.

## v0.39.0

New chart `observability-rum`: browser telemetry through Grafana Faro and a
self-hosted Alloy, with "issues" built on the log store instead of an error
tracker (see docs/frontend.md). Nothing existing renders differently.

- **`charts/observability-rum`** wraps the official `grafana/alloy` chart
  (1.13.0, Apache-2.0, pinned and vendored as an archive). `apps[]`
  (`global.observabilityRum.apps`) renders one `faro.receiver` per app, each on
  its own port with its own API key (read from a Secret by name), exact origin
  list, `global` rate limit and payload cap (default 256KiB, at most 1MiB),
  `sourcemaps.download` off. The app is stamped by the receiver that accepted
  the request (`service.name`, `app`, `telemetry.source`, and the environment
  when set); the payload's own app attributes are deleted. Output goes to the
  OTLP gateway named by `otlp.endpoint`, never to a store. A ClusterIP Service
  has one named port per app; the gateway route is the estate's
  (docs/frontend.md has the same-origin shape).
- Every exception row gets `error.fingerprint`, computed in Alloy: the first
  16 hex of sha256 over the app, the exception type, the message with URLs,
  quoted strings, UUIDs, hex and numbers normalised, and the first
  own-code frame (`file:function`, after symbolication, no line or column). It
  is a log record attribute, never a stream field. `error.message` is the
  normalised message, `error.frame` the frame.
- Privacy defaults: query strings and fragments are removed from every
  absolute URL in every attribute and span attribute, the SDK's user block is
  dropped, the anonymous session id is kept.
- Optional source maps: `sourcemaps.sync` copies an object-store prefix
  read-only into a volume served to Alloy, with credentials from the pod's
  ServiceAccount only; otherwise Alloy reads `sourcemaps.directory`.
- Alerts (LogsQL, `observability.rule-type: vlogs`): `FrontendNewIssue`,
  `FrontendIssueRegressed` and `FrontendErrorRateHigh`, with windows and
  thresholds as values and at most `rules.maxFingerprints` fingerprints firing
  per evaluation. Optional recording rules for p75 web vitals
  (`rules.recording.webVitals`, off); the logs vmalert supports recording
  rules.
- Two dashboards, "Frontend Issues" and "Frontend Overview", shipped as
  ConfigMaps by the chart (`dashboards.*`).
- Refusals, each with a fixture: an empty OTLP endpoint, a wildcard or empty
  `allowedOrigins`, `sourcemaps.download: true`, a `per_app` rate limit, an
  app with no key Secret, a payload limit above 1MiB, a repeated app name or
  port, `alloy.rbac.create: true`, a replaced Alloy configuration, and more
  (docs/safety.md).
- `dashboardlint` accepts the VictoriaLogs datasource's field-values variable
  query where rules 2 and 3 ask for `label_values()`. The default
  `just dashboard-lint` also lints `charts/observability-rum/dashboards/`.

## v0.38.0

`platform-alerts`: an alert for Pod Security admission rejections.

- New group `groups.podSecurity` (off by default) with one alert,
  `PodSecurityAdmissionRejected`: a ReplicaSet, StatefulSet, Job or
  DaemonSet whose pod create was refused by Pod Security `enforce` records a
  `FailedCreate` Event "violates PodSecurity ...", and the workload does not
  start. No pod exists, so no metric sees it; the alert reads the Events
  `observability-emitters` already ships to the log store (`otlp.events`),
  and fires per cluster, namespace and owning controller (`warning`,
  `for: 0s`, 15m look-back). `keepClusterLabel` keeps each Event's own
  cluster on a store that holds several, as for `pendingPods`.
- It is LogsQL, so it renders a second VMRule, `<release>-platform-alerts-logs`,
  marked `observability.rule-type: vlogs` (the label the logs vmalert selects
  on; the metrics vmalert ignores it). Nothing changes for an install that
  leaves the group off: the existing VMRule renders byte for byte as before.
  Enable it only where a logs vmalert runs. `warn`-mode violations are not
  Events and are not matched.

## v0.37.0

`observability-emitters`: write every signal to both halves of an HA store
pair (see docs/high-availability.md, "The writers"). Without the new keys
every render is unchanged.

- **`remote.replicas[]`**: `{name, url, tokenSecret, caSecret?}`. Every signal
  `remote` covers is written to `remote.url` and to each replica: one more
  `remoteWrite` on the metrics agent and one more exporter per signal in the
  gateway, each with its own on-disk buffer (the persistent queue, the
  `file_storage` queue and the write-ahead log) and its own bearer. A replica
  needs a credential of its own, because a store holds one user per token.
  Refused: a repeated URL or name, a replica sharing a credential with
  another entry, and, with `logs` among the signals, a
  `victoria-logs-collector.remoteWrite` that is not exactly `remote.url` plus
  the replicas or that reads two entries' bearer from one file (the log
  agent's list is upstream's and stays hand-written). `shardByURL` stays
  refused. A cluster running both halves itself uses the same list with
  in-cluster endpoints.
- Each low-level destination (`metrics.destinations[]`,
  `otlp.destinations.*[]`) takes an optional `tokenSecret` of its own; the
  gateway reads each from its own environment variable.
- `otlp.queue.maxBatches` (default `0`, renders nothing) caps each
  exporter's `sending_queue`, so one stalled destination cannot use the
  room of the others.
- The log agent's URL keeps refusing a path: the path is the endpoint, not a
  prefix, so a replica is reached by its own host.
- **Behaviour change: `observability-stack`'s `WriterBufferGrowing`,
  `WriterDroppingPackets` and `GatewayQueueFilling` are per destination.**
  Only rendered with `selfAlerts.writer`/`selfAlerts.gateway` metric names set.
  `WriterDroppingPackets` is now summed `by (url)` (it was one global sum) and
  its descriptions name the destination (`url`) or exporter. New
  `selfAlerts.writer.bufferMetricsExtra` and `droppedPacketsMetricsExtra` add
  the log agent's metric names to the same rules; empty by default, so a
  render with one name differs only in those lines. Revert by summing
  yourself in an own rule: the old expression had no per-URL meaning.

## v0.36.1

`alert-ingress`: a safe certificate cache, an alert on rejected messages,
mappings that see the SNS envelope, and an optional separate metrics port.

- **Behaviour change: the default render gains a second VMRule group,
  `alert-ingress.rejected`, with the warning `AlertIngressMessagesRejected`.**
  The receiver now counts every refused message in
  `alert_ingress_rejected_total{reason}` (`malformed`, `signature`,
  `unknown_topic`, `confirmation`, `unsupported_type`; a closed set) and the
  rule fires when the rate per reason stays above `rules.rejectedMessages.ratePerSecond`
  (default 0.05) for `rules.rejectedMessages.for` (default 15m) over
  `rules.rejectedMessages.window` (default 5m). Set
  `rules.rejectedMessages.enabled: false` to keep the old render; raise the
  rate if your edge sees internet noise. `rules.heartbeat.enabled` (default
  true) now switches the existing deadman group; with both off no VMRule is
  rendered. `alert_ingress_messages_total{outcome="rejected"}` is unchanged.
- Fix: the signing-certificate cache was an unlocked map hit by concurrent
  requests, which can crash the process with "concurrent map writes". It is
  now mutex-guarded, bounded (32 entries), expires after 6 hours, and only
  ever holds certificates fetched from the pinned signing host.
- Mappings can match the SNS envelope. `_sns.TopicArn`, `_sns.Subject`,
  `_sns.Message` (the raw text, for plain-text publishers such as budget
  notifications), `_sns.MessageId` and `_sns.Type` are readable in `match`
  and in every `alert` template; they come from the signed envelope and a
  message body cannot overwrite them. New `matchRegex` (RE2, searched in the
  text at a path) sits beside `match`; both must hold. Existing mappings
  are unchanged.
- Distinct findings stay distinct alerts: labels are templates over the
  message, so set `account`, `region`, `finding_type` and `finding_id` from
  the finding (see the GuardDuty example in `docs/alert-ingress.md`).
  Template helper `atLeast VALUE THRESHOLD` is a numeric `>=` that accepts
  int, float or numeric-string values, so
  `{{ if atLeast .detail.severity 7 }}critical{{ else }}warning{{ end }}`
  no longer needs the `7.0` literal; an absent value is false, a
  non-numeric one falls back to `CloudEventUnmapped`.
- New `service.metricsPort` (default `0`, unchanged: one port). Set it and
  `/healthz` and `/metrics` are served only on that port, which the Service
  does not expose; probes and the PodMonitor follow it. Route only
  `POST /` of the webhook port at the edge.

## v0.36.0

`observability-stack`: HA mode. A pair of stores across zones, as two
releases of one chart: a primary, and a stores-only `mode: replica`. The
single install is the default and every existing render is unchanged; none
of this applies until you opt in. See docs/high-availability.md.

- **`mode: replica`** renders only the three stores (the VMSingle, the log
  store, the trace store), their NetworkPolicies and scrape objects. No
  proxy, vmalert, Alertmanager, karma, Grafana, backup or operator: the
  primary's operator reconciles the replica's VMSingle. It is a contract like
  `operator-only` and refuses, naming the key, a component left on, the
  operator or the sync Job not turned off, `backup.enabled: true` (two
  releases would erase each other's snapshots), `tenancy.*`, and `ha` that is
  not enabled.
- **`ha` accepts an object.** `ha: {enabled: true, name, replica, peer:
  {metrics, logs, traces}}`. The boolean `ha: true` is the legacy switch and
  keeps doing what it did (accepted, refused with fewer than two `zones`),
  so no existing values file changes meaning. `ha: null` is now the default
  (it was `false`; both mean the single install).
- **On the primary, `ha.enabled`**: every read route carries both stores
  (`static.urls`, this release first) with `first_available` and the existing
  retry codes; `vmauth.replicaCount` must be 2 or more, spread over zones
  with a PodDisruptionBudget; one vmalert per store replica (a `-peer` twin
  of each, same rules, interval, labels and notifiers, state kept in its own
  store) so four instead of two; a PodDisruptionBudget per store pair; the
  store scrape carries `observability_replica`. Writers are not fanned out by
  the proxy.
- **Refused without a zone spread.** Under `ha.enabled` each store must carry
  the shared `observability.pair` pod label and a `DoNotSchedule`
  `topologySpreadConstraints` over `topology.kubernetes.io/zone` on it: a
  zonal volume pins a replica to its zone. A zone loss keeps that replica
  down until the zone returns.
- **Two opt-in store alerts.** `selfAlerts.storeMemory` (a store container's
  working set above 80% of its memory limit for 15m; either mode) and
  `selfAlerts.divergence` (rows-ingested rate ratio between replica `a` and
  `b`, a tolerance with a conservative default threshold of 0.25: measure a
  week first). Both default off and render without `selfAlerts.enabled`.
- Store resource defaults are unchanged; a replica takes the same ones.

## v0.35.1

`platform-alerts`: the `*Absent` guards fire per cluster, and one dashboard
loses two panels that could never show data.

- **Behaviour change: every `*Absent` alert is now per cluster, not only when
  every cluster is missing the series.** A bare `absent(...)` is true only
  when no series matches in the whole store, so on a store that holds several
  clusters a controller, exporter or probe vanishing from ONE cluster was
  silent. With a `clusterLabel` (the default), `KargoStateMetricsAbsent`,
  `KargoControllerAbsent`, the probes group's `<alertName>Absent`,
  `NodeClaimMetricsAbsent`, `NATSMetricsAbsent`, `ArgoCDMetricsAbsent`,
  `ESOWebhookAbsent`, `ESOMetricsAbsent` and `ACKControllerAbsent` now fire
  for a cluster that HAD the series within the new `absentLookback` (default
  `1d`) and does not now, and keep the whole-store `absent()` for "never
  existed / everything gone", so a fresh install with no data still alerts.
  The ACK and probe guards are also per namespace and per probe, and with
  `expectedNamespaces` a cluster that reports any listed controller must
  report them all. The per-cluster alert carries that cluster's own
  `clusterLabel` value (the whole-store one carries none), whatever
  `keepClusterLabel` says and whether or not `commonLabels` names a cluster:
  the common label would stamp every cluster with the store's. A cluster,
  controller or probe removed on purpose stops alerting once the lookback has
  passed; the guard reads a day of the series on each evaluation. `clusterLabel: ""`
  renders the bare `absent()` byte for byte. Proof against a real
  VictoriaMetrics: `just platform-alerts-absent-proof`.
- **Behaviour change: the External Secrets dashboard drops its two webhook
  latency panels** ("Webhook latency [5m]" and the "webhook latency" panel in
  the webhook row). They read `controller_runtime_webhook_latency_seconds_bucket`,
  which no External Secrets Operator version in use exports, so they could
  never show data. The other webhook panels stay.

## v0.35.0

`observability-mcp`: zero-gap rolling updates, and spread for connectors that
run more than one replica.

- **Behaviour change: every connector Deployment now renders
  `strategy: RollingUpdate` with `maxUnavailable: 0` and `maxSurge: 1`.** A
  rollout starts the new pod and waits for it to be ready before stopping an
  old one, so a restart no longer drops below the desired count (with one
  replica that means one extra pod during the rollout).
- **With `replicaCount` above 1, a connector's pods carry two soft topology
  spread constraints** (`maxSkew: 1`, `ScheduleAnyway`) on
  `topology.kubernetes.io/zone` and `kubernetes.io/hostname`, selecting that
  connector's own pods. Single-replica connectors render none. There is no new
  value; the constraints are not configurable.

## v0.34.0

`observability-dashboards`, `observability-emitters`, `platform-alerts`: a
truth pass over the shipped dashboards, and alert groups for Argo CD, External
Secrets Operator and AWS Controllers for Kubernetes.

- **Behaviour change: panels that could never hold data are gone from the
  default dashboards.** Each was checked against a real store and has no
  series to read: Kubelet "Config Error Count" (the metric went with dynamic
  kubelet configuration); VictoriaMetrics operator "Prometheus Converter Watch
  events"; twelve vmagent panels (the agent's own hourly and daily series
  limit, the six stream-aggregation panels and the four Kafka panels, with
  their rows); fifteen Node Exporter Full panels on collectors a virtual
  machine's exporter does not run or hardware it does not have (hardware
  temperature and fan speed, cooling devices, power supply, CPU frequency
  scaling, systemd units and sockets, detailed process states, PID and thread
  limits, interrupt detail, network speed and saturation), with the two rows
  that held only those. The panels beside them keep their data and are
  re-spread across the row. Re-enable one by adding it back to your own copy
  of the dashboard.
- **Behaviour change: four panels read a series that exists.** Envoy Clusters
  "Downstream Network Traffic" selects connection-manager prefixes `https?-.*`
  (it matched `http-.*`, which no listener's prefix equals); VictoriaLogs
  "Memory usage" no longer adds `vm_cache_size_bytes`, which VictoriaLogs does
  not export, so its first two series render; the "Kubernetes Resource Count"
  panels of the Global and Namespaces views count `kube_<kind>_created`
  instead of summing `kube_<kind>_labels` (kube-state-metrics writes no
  `*_labels` series while its label allow-list is empty, the default).
- **Behaviour change: the two CloudNativePG dashboards are titled apart.** They
  were both `CloudNativePG ($cluster)`, with the same URL slug. They are now
  `CloudNativePG / Clusters ($cluster)` and `CloudNativePG / Operator
  ($cluster)`; the uids are unchanged, so links and bookmarks still open.
- **Behaviour change: Fleet overview tiles no longer invent a zero.** No tile
  falls back to `or vector(0)`. Where a count is zero because the series only
  exists when something is wrong (firing alerts, pods not Ready, full volumes,
  rows dropped), the zero is read off a series of the same source that proves
  it is scraped (`or (0 * count(...))`); where the source writes a series for
  every object (restarts, nodes not Ready, pods running) there is no fallback.
  A missing source now reads **No data** (the cluster-row tiles' no-value text
  is `No data`; the component tiles keep `n/a`, and "Newest Postgres backup
  age" reads `No data` where no backup is reported). The CrashLoopBackOff and
  ImagePullBackOff tiles query `kube_pod_container_status_waiting_reason`,
  which kube-state-metrics writes only while a container is waiting (it skips
  running containers), so their zero comes from the always-present
  `kube_pod_container_status_waiting`. Nothing trimmed that metric: no emitter
  change was needed to make it appear.
- **Behaviour change: the kubelet scrape drops `kubernetes_feature_enabled`.**
  The feature-gate gauge is one series per gate per node per stage, the
  largest metric the kubelet job stored on a real install, and nothing here
  reads it. New value `metrics.scrape.kubeletDrop` (`enabled: true`,
  `metricNames: [kubernetes_feature_enabled]`, `extraMetricNames: []`), on the
  kubelet job only, shaped like `cadvisorDrop`. `enabled: false` restores the
  series; `extraMetricNames` widens the list without replacing it. Every
  `observability-emitters` golden that renders the kubelet job moves by that
  one `drop` step.
- **`dashboards.external-secrets`** (default **off**): External Secrets
  Operator's own dashboard (the project's `docs/snippets/dashboard.json`,
  Apache-2.0, pinned to the chart's release tag and rewritten to the
  contract), in the `Platform` folder, uid `truvity-obs-external-secrets`:
  error rates by controller, not-Ready ExternalSecrets, provider API calls,
  the admission webhook and the controllers. It needs the operator chart's
  `serviceMonitor.enabled: true` (a new optional source,
  `external-secrets-metrics`); without it every panel is empty, hence the
  default. The Fleet overview has no External Secrets row: the chart cannot
  render a row only where a source is on, so there is none.
- **`platform-alerts` `groups.argocd`** (default **off**): six alerts on Argo
  CD's own metrics (`ArgoCDAppSyncFailed`, `ArgoCDAppUnhealthy`,
  `ArgoCDAppOutOfSync`, `ArgoCDClusterConnectionLost`, `ArgoCDGitFetchFailing`,
  `ArgoCDMetricsAbsent`). `outOfSync.enabled: false` drops the out-of-sync
  alert for an estate whose applications are out of sync by design.
- **`platform-alerts` `groups.eso`** (default **off**): nine alerts, the
  highest-severity pair on the admission webhook (`ESOWebhookDown`,
  `ESOWebhookAbsent`: both validating webhooks fail closed, so while it is down
  every write of an ExternalSecret or a store is refused), then
  `ESOExternalSecretNotReady`, `ESOSecretStoreNotReady`,
  `ESOClusterSecretStoreNotReady`, `ESOReconcileErrors`,
  `ESOProviderAPIErrors`, `ESOWorkqueueStuck` and `ESOMetricsAbsent`.
- **`platform-alerts` `groups.ack`** (default **off**): `ACKControllerDown`,
  `ACKControllerAbsent`, `ACKReconcileErrors`, `ACKTerminalReconcileErrors` and
  `ACKReconcilePanics` on controller-runtime's metrics in the controllers'
  namespaces (`namespace`, default `ack-.*`). Name the controllers' namespaces
  in `expectedNamespaces` to get one `absent` per controller.
- **`platform-alerts` `groups.kargo`**: three controller rules beside the
  promotion rules, each its own switch and **off by default** so a group that
  is already on renders what it did: `controllerAbsent.enabled`
  (`KargoControllerAbsent`), `reconcileErrors.enabled`
  (`KargoControllerReconcileErrors`) and `workqueueDepth.enabled`
  (`KargoControllerWorkqueueStuck`), with `controllerJob` naming the scrape job.
  New `keepClusterLabel`, as for the other groups.

## v0.33.1

`observability-emitters`, `platform-alerts`: probe series carry the tenancy
labels; probes alert when absent.

- **Behaviour change: probe series now carry `k8s_cluster_name`,
  `deployment_environment_name` and `k8s_namespace_name`, so their `owner`
  follows the release namespace.** Every `VMProbe` stamps them in its own
  target relabeling (the namespace is the release's). The operator does not apply the
  agent's default scrape class to a `VMProbe`, so since v0.29.0 the
  `probe_*` series were scraped healthy and stored with no cluster label, and
  any reader scoped by that label (every scoped query grant) saw nothing.
  Only the probe goldens change.
- **Behaviour change: the new `<alertName>Absent` rule fires when no probe
  reports.** The `platform-alerts` probes group gains it (default
  `HTTPProbeDownAbsent`), `absent(probe_success{probe=~"<probe>"})` for
  `groups.probes.absentFor` (default `15m`) at `groups.probes.absentSeverity`
  (empty, the default, takes `severity`). `probe_success == 0` is silent when
  the series is missing, which is how this went unnoticed. A new alert rule
  appears wherever the probes group is on; set `probe` to a name to watch one
  probe, as the default selector fires only when no probe reports.

## v0.33.0

`platform-alerts`: a NATS and JetStream alert group, off by default.

- **Behaviour change: a new `groups.nats` key, `enabled: false`.** The
  default render is unchanged. Turn it on where the upstream NATS chart and
  its exporter PodMonitor run: it renders `platform-alerts.nats` with nine
  alerts (broker down, too few cluster routes, no JetStream meta leader,
  stream with no leader, consumer backlog growing, slow consumers,
  JetStream storage above 80%, broker memory above 90% of its limit, and a
  deadman). `job` defaults to `nats/nats`; the memory alert needs
  cadvisor and kube-state-metrics series for the broker container. There is
  no authorization-error alert: the exporter has no such metric. On a store
  that holds several clusters set `keepClusterLabel: true`, as for
  `pendingPods`, so the alert names the cluster the broker is in.

## v0.32.0

`observability-dashboards`: an OpenBAO dashboard.

- **`dashboards.openbao`** (default **off**): the OpenBAO server's health
  from its own metrics (`vault_*`), in the `Platform` folder, uid
  `truvity-obs-openbao`. Rows: availability (pods reporting, sealed pods,
  active nodes, healthy voters, failure tolerance, audit failures, and the
  seal and active state by pod), Raft (Autopilot voter health, commit index,
  applied index behind the leader, leader contact, leadership changes, write
  rate), requests (rate and p50/p99 latency, logins), tokens and leases, and
  the audit devices. It needs the server's telemetry on and a PodMonitor on
  the server pods (truvity/openbao `openbao-ops` >= v0.27.0:
  `serverMetrics`); without it every panel is empty, hence the default.
  Turn it on in the Grafana whose store holds that cluster's metrics. Nothing
  else in the render changes: with the key left alone the default set is
  byte-identical. The panels on the Autopilot, token and lease gauges are
  joined to `vault_core_active`, because the server keeps exporting a former
  leader's last value after a leadership change.

## v0.31.2

`observability-dashboards`: the Keycloak heap panels read the right metric.

- **Behaviour change: the Keycloak dashboard's heap panels read
  `jvm_memory_usage_after_gc`.** The two heap-after-GC panels queried
  `jvm_memory_usage_after_gc_percent`, a name Keycloak does not export, so
  they showed nothing. The metric is a ratio (0 to 1) under the new name.
  Only the `everything` golden moves; the dashboard is still off by default.

## v0.31.1

`statusbox`: RESOLVED posts say the check recovered.

- The deadman's chat post used one shared sentence for both states, so a
  recovery read "failed twice in a row". The rendered `alerting.custom` now
  sets `placeholders.ALERT_TRIGGERED_OR_RESOLVED`: TRIGGERED keeps its
  meaning, RESOLVED reads "RESOLVED: the check is passing again". The body
  is now `<group>/<name> - <state text>` and no longer uses
  `[ALERT_DESCRIPTION]` (the alert's own description is unchanged). Same
  channel, same fields.

## v0.31.0

`observability-dashboards`: a Keycloak dashboard, off by default.

- **Behaviour change: a new `dashboards.keycloak` key, `enabled: false`.**
  The default render is unchanged; only the `everything` golden, which turns
  every dashboard on, gains the ConfigMap. The dashboard (Platform folder)
  reads Keycloak's own metrics, a new optional source `keycloak-metrics`
  scraped only where truvity/keycloak's chart turns its `serviceMonitor` on
  (and `metrics.httpHistograms` for the latency panels). Turn it on together
  with that ServiceMonitor.

## v0.30.0

`observability-stack`: karma hides the Watchdog alert by default.

- **Behaviour change: karma opens with `alertname!=Watchdog`.** The
  always-firing Watchdog (the deadman heartbeat) no longer clutters the
  console. New value `karma.filters.default` (list of strings, rendered as
  karma's `filters.default`); the default is `[alertname!=Watchdog]`. It
  only applies when the UI opens with no `?q=`: remove the filter in the UI
  to see Watchdog. Set your own list to replace it, or `[]` to show
  everything. Only installs with `karma.enabled` render differently.

## v0.29.0

`observability-emitters`, `platform-alerts`: a real blackbox exporter for
HTTP probes.

- **Behaviour change: `metrics.scrape.probes` is answered by a blackbox
  exporter, not scraped directly.** The list keeps its shape (`name`,
  `url`, `interval`, and a new optional `module`), but each probe now
  renders a `VMProbe` aimed at the exporter instead of an inline scrape of
  the URL, and `up{job="http-probe"}` no longer exists: read
  `probe_success{probe="<name>"}` (1 while the module accepts the answer,
  0 otherwise), plus `probe_http_status_code`, `probe_duration_seconds` and
  `probe_ssl_earliest_cert_expiry`. The series carry the same stamped
  `k8s_cluster_name` and `deployment_environment_name`, `probe=<name>` and
  `job="http-probe"`. The old path handed the response body to the agent as
  Prometheus text, so a JSON health document made it log `cannot unmarshal
  Prometheus line` on every scrape and tripped `TooManyErrorLogs`. There is
  no compatibility path: set `blackboxExporter.enabled: true` with the
  probes, and move any alert off `up{job="http-probe"}` (`platform-alerts`
  `groups.probes` does it for you). Installs that set no probes render
  byte-identically.
- **`blackboxExporter`** (emitters, default off): the upstream
  `prom/blackbox-exporter:v0.28.0` as one Deployment with its own
  ServiceAccount (no token), Service and ConfigMap, Pod Security
  `restricted`, `resources`, `nodeSelector`, `tolerations` and an optional
  `networkPolicy.enabled` that admits the exporter's port only from the
  metrics agent. `modules` defaults to `http_2xx` (GET, any 2xx,
  redirects not followed, IPv4) and merges with what you add. Neither the
  VictoriaMetrics operator nor the vendored k8s-stack chart ships a
  blackbox exporter, so this is the chart's own component.
- **Refused:** probes without `blackboxExporter.enabled`, a probe whose
  `module` is not a key of `blackboxExporter.modules`, and two probes with
  one name.
- **`platform-alerts` `groups.probes`** now alerts on
  `max by (probe) (probe_success{probe=~"<probe>"}) == 0` (the `for`,
  `alertName`, `severity`, `summary` and `description` values are
  unchanged). The new **`groups.probes.certExpiry`** (default off) adds
  `HTTPProbeCertExpiring`: `probe_ssl_earliest_cert_expiry` less than
  `withinDays` (default 14) away, for `1h`.

## v0.28.0

`observability-stack`: a moved Alertmanager replica is forgotten in minutes.

- **Behaviour change: an Alertmanager pair renders one more flag.** Only
  installs with `alertmanager.replicaCount` above 1 move (the
  `alertmanager-pair` and `everything` goldens): the VMAlertmanager gains
  `extraArgs: {cluster.reconnect-timeout: "5m"}`. One replica renders
  byte-identically to before.
- A rescheduled replica comes back at a new IP; the survivor kept the old
  address as a failed peer for Alertmanager's default 6h, so
  `alertmanager_cluster_failed_peers` stayed above 0 and
  `AlertmanagerClusterFailedPeers` fired on a healthy mesh. Set
  `alertmanager.cluster.reconnectTimeout` (a Go duration, default `5m`) to
  move it; anything that is not a Go duration is refused. Restoring the old
  behaviour is `reconnectTimeout: 6h`. `--cluster.reconnect-interval` is
  untouched.

## v0.27.0

`observability-stack`: an Alertmanager pair is safe to run.

- **No change at the default.** `alertmanager.replicaCount` stays `1`, and
  every golden render is byte-identical to before, except the `everything`
  case, which already set `replicaCount: 2`.
- **Behaviour change: an Alertmanager pair renders differently.** Only
  installs with `alertmanager.replicaCount` above 1 move (the `everything`
  golden): the pod safeguards and per-replica notifiers and karma servers
  below. The opt-out is `replicaCount: 1`; the budget, affinity and spread
  are each overridable.
- `alertmanager.replicaCount` above 1 now renders, on the VMAlertmanager, a
  `podDisruptionBudget` (`maxUnavailable: 1`), preferred pod anti-affinity
  on `kubernetes.io/hostname`, and a topology spread on
  `topology.kubernetes.io/zone` (`ScheduleAnyway`). Override them with
  `alertmanager.podDisruptionBudget`, `alertmanager.affinity` and
  `alertmanager.topologySpreadConstraints`.
- Both vmalerts then send to every replica (one `notifiers` entry per pod,
  through the operator's headless Service) instead of one load-balanced
  URL; the replicas dedup through the mesh. `alertmanager.notifierUrl`, when
  set, still wins.
- karma lists each replica as a server with the same `cluster` value
  (`alertmanager-0`, `alertmanager-1`, ...), as its documentation asks for
  an HA cluster. Those names are reserved against `karma.alertmanagers`.
- No NetworkPolicy is added for the Alertmanager pods: the first policy
  selecting them would default-deny them. None rendered here blocks the
  mesh (9094 TCP and UDP).

`observability-stack`, `observability-emitters`, `platform-alerts`,
`pkg/statusbox`: a deadman that watches the router and pages on its own
channel, and an outside probe.

- **`alertmanager.watchdog.tokenKey`** (default empty): a key of the same
  Secret whose value is sent as `Authorization: Bearer <token>` with the
  heartbeat, read with `credentials_file`. Default renders unchanged.
- **`tenancy.alertReaders[].alertmanager`** (default `false`): also routes
  the reader to Alertmanager's `/api/v2/alerts` (exact path). vmauth cannot
  match a method and `POST` on that path creates alerts, so the edge in front
  must admit GET only. Refused without `alertmanager.enabled`.
- **`metrics.scrape.probes`** (emitters, default empty): HTTP probes as
  scrapes of a URL, kept for `up{job="http-probe"}` (replaced by a
  blackbox exporter in the entry above); **`groups.probes`**
  (platform-alerts, default off): the alert on `up == 0`, with a settable
  `alertName`, `summary` and `description` (plain text, default empty).
- **`statusbox.Catalogue.Deadman`**: the deadman group (vmalert Watchdog,
  Alertmanager Watchdog, and not-firing checks) on its own `custom`
  provider and the two-failure threshold; `Providers` no longer reaches
  it. **Behaviour change:** every alert ref the library renders now carries
  `send-on-resolved: true`; with no `Providers` and no `Deadman` nothing
  changes.
- `hack/gatus-deadman-proof.sh` (`just gatus-deadman-proof`) proves the
  group on the real Gatus image.

## v0.26.0

`observability-stack`: named links in Slack notifications.

- **Behaviour change: Slack links render as named links.** The raw
  `Grafana:`, `Silence:` and `View:` lines of the Slack message are
  replaced by named mrkdwn links. `<url|Grafana>` ends each alert's summary
  line (it is about that alert); Silence and View are about the alert group,
  so they are ONE line after the alerts, `<url|Silence> · <url|View>`
  (`console: karma`), `<url|Silence>` (`alertmanagerUrl`), or absent
  (neither). Every default Slack golden changes; the Telegram message does
  not.
- The Slack title is a link: `title_link` is the View URL with
  `console: karma`, the Grafana URL otherwise.
- Escaping: `&` inside a link is written `&amp;` (Slack's "Escaping
  text" rules). The Grafana URL's cluster and namespace and the
  Alertmanager silence filter's names and values now pass through
  `urlquery`, so a `|` or `>` in a label value cannot end the link.

## v0.25.1

`observability-stack`, `observability-emitters`: every pod the charts create,
except the two node-level DaemonSets, meets the Pod Security `restricted`
profile.

- **Behaviour change: default renders gain a `securityContext` on every pod.**
  Every existing golden moves, by security-context stanzas only. To restore
  the previous output for the VMSingle set
  `victoria-metrics-k8s-stack.vmsingle.spec.securityContext: null`, for the
  VMAgent set `metrics.spec.securityContext` to the value you want, for the
  subcharts and the gateway override their `podSecurityContext` /
  `otlp.podSecurityContext`. The VMAlert, VMAuth, VMAlertmanager and the
  backup Jobs have no opt-out: their context is part of the profile.
- **The VictoriaMetrics-operator objects set a `securityContext`.** The
  VMAlert, VMAuth and VMAlertmanager the stack renders and the VMAgent the
  emitters render now run as `65534` with `fsGroup: 65534`
  (`fsGroupChangePolicy: OnRootMismatch`), a `RuntimeDefault` seccomp profile,
  no privilege escalation and every capability dropped. The operator inlines
  the pod and container fields of that one object, so it also covers the
  config reloader and its init container. The default for the VMSingle is
  `victoria-metrics-k8s-stack.vmsingle.spec.securityContext`, with the same
  contents; `backup.seLinuxLevel` still merges into it. **The first start
  after the upgrade changes the ownership of the existing metrics volume**
  (it was written by root); that is one pass over the data, not one per start.
  The VMAgent takes the same default from the chart and `metrics.spec` still
  overrides it.
- **Seccomp on the subcharts' pods.** `victoria-logs-single.server`,
  `victoria-traces-single.server` and the operator's `podSecurityContext`
  gain `seccompProfile: RuntimeDefault` beside the non-root user they already
  set. `victoria-metrics-k8s-stack.syncJob` runs as `65534` with the profile
  and no capabilities.
- **The backup CronJobs run as `65534`** with `fsGroup: 65534`, the profile and
  no capabilities on every container (`backup.seLinuxLevel` is unchanged). The
  credential-process tools init container copies with `cp -R` instead of
  `cp -a`, because a non-root user cannot preserve ownership.
- **The OTLP gateway** sets `runAsNonRoot`, `runAsUser: 10001` (the image's own
  user), a `RuntimeDefault` profile on the pod and a new
  `otlp.containerSecurityContext` (no escalation, no capabilities) on the
  container. `otlp.podSecurityContext` keeps its `fsGroup: 10001`.

## v0.25.0

`observability-stack`: karma as an optional console, and Slack links that
open it.

- **New component `karma`, OFF by default** (`karma.enabled`). Alertmanager
  takes a silence's author as free text; karma, behind an SSO gateway that
  sets an identity header, rewrites it to the signed-in user on every
  silence it proxies and enforces silence ACLs. Renders a Deployment, a
  Service, a ServiceAccount and a ConfigMap (karma config, plus the ACL
  file), image `ghcr.io/prymitive/karma:v0.133`. This release's own
  Alertmanager is karma's first server (`proxy: true`);
  `karma.alertmanagers` appends more. `karma.enabled` is refused without
  `karma.authentication.header.name`, unless `karma.authentication.none:
  true` acknowledges an anonymous console. `karma.extraConfig` is merged
  last and is not validated. A default install renders nothing new.
- **New `networkPolicy.karmaFrom`**: who may reach karma (ingress on 8080,
  default the release's namespace). No egress policy, like every other
  policy here. No policy selects the Alertmanager pods, so none is added
  for karma's traffic.
- **New `notifications.console`** (`alertmanager`, the default, or
  `karma`), `notifications.consoleUrl` and `notifications.silenceMinutes`
  (default 60). With `console: karma` the Slack `Silence:` link opens
  karma's silence form prefilled with the alert's labels and a `View:` link
  opens karma filtered to the alert group; the Telegram message is
  unchanged. With the default, every existing render is byte-identical.

## v0.24.0

`observability-dashboards`: the CloudNativePG instance dashboards, as an
opt-in metric source.

- **New dashboard `cnpg-cluster`, OFF by default** (`dashboards.cnpg-cluster.enabled`).
  It is the instance half of the upstream CloudNativePG cluster dashboard:
  server health, connections, transactions, storage, WAL archiving,
  replication lag, backups and checkpoints, 56 panels. It reads the
  instance exporter (`:9187` on each Postgres pod), an optional source named
  `cnpg-instance-metrics` that a default install does not scrape; turn the
  dashboard on together with the Postgres cluster chart's `PodMonitor`.
  Instances are selected by the exporter's own `cluster` label (the Postgres
  cluster) and the collector's `k8s_cluster_name` (the install), never by a
  fixed job. Its first panel, "Postgres instances reporting", reads `no data:
  instance metrics not scraped` while the source is off, so absence is
  visible, not a blank wall.
- **Behaviour change: the default Fleet overview gains four Postgres tiles** on the CloudNativePG line
  (instances up, instances not up, maximum replication lag, newest backup
  age). They read the same optional source and show `n/a` until it is
  scraped; they link to `cnpg-cluster`. There is no value to hide them: the
  dashboard is a fixed file, so a default install now renders one more line of
  tiles reading `n/a`. Nothing else on the page moves, and `cnpg-operator`
  (operator series only) is unchanged and still on.
- **Allow-lists:** `hack/dashboards/available-metrics.yaml` gains the 42
  exporter families the two dashboards read (41 `cnpg_*` and one
  `barman_cloud_cloudnative_pg_io_*`), each taken from a live scrape and
  each held only for a dashboard that declares `requires:
  [cnpg-instance-metrics]`; `available-labels.yaml` gains the matching jobs.
  `cnpg_pg_settings_setting` is kept for eight settings only
  (`block_size`, `effective_cache_size`, `maintenance_work_mem`,
  `max_connections`, `random_page_cost`, `seq_page_cost`, `shared_buffers`,
  `work_mem`), and a test refuses a panel that selects another. The catalog
  no longer records `deferred: [cnpg-instance-metrics]` on `cnpg-operator`.
- Panels the store cannot serve are dropped at import with their reason
  printed (the CPU panels need a recording rule no store holds; the whole
  configuration table would read as the full configuration). The checkpoint
  panels read the PostgreSQL 17 `pg_stat_checkpointer` names, so on an older
  Postgres they are empty. No default moved and no existing dashboard
  changed except the Fleet overview's added line.

## v0.23.1

- No change to any chart or render. v0.23.0 was tagged on the commit
  before its release commit, so this patch carries only the `## v0.23.0`
  heading in this file; the charts are byte-identical to v0.23.0.

## v0.23.0

`observability-stack` notifications: links that work, and Slack for `also`.

- **Behaviour change: the `Silence:` line disappears unless
  `notifications.alertmanagerUrl` is set.** The new value (default empty) is
  the externally reachable base URL of this Alertmanager's UI, no trailing
  slash. Set, it is the silence link's base; empty, the message (Slack and
  Telegram) carries no silence line. There is no default on purpose: the
  link used to be built on Alertmanager's own pod address, which nobody can
  open, and the Grafana base is no substitute because the chart refuses
  Grafana-managed alerting, so Grafana has no silence page. A set value is
  refused unless it is an absolute `http(s)://` URL without a trailing slash.
  The silence filter no longer ends in a stray `%2C` before `%7D`.
- **Behaviour change: the VMAlertmanager's `spec.externalURL` no longer comes
  from `vmalert.externalUrl`.** That value is the Grafana base, a different
  fact; `spec.externalURL` is now rendered only from
  `notifications.alertmanagerUrl`. `vmalert.externalUrl` keeps its own use,
  vmalert's `-external.url`.
- The title no longer reads `on my-cluster/` for an alert with no namespace:
  it is `on my-cluster` then, and `on my-cluster/my-namespace` otherwise. The
  Grafana link omits `&var-namespace=` the same way.
- `notifications.also[]` may deliver to Slack: `receiver: slack` with
  `channel` (required), `workspace` (required with two or more workspaces)
  and an optional `mention`. It reuses the receiver a primary route renders
  for the same destination. Telegram stays refused; `channel`, `workspace`
  and `mention` on a webhook entry are refused.
- **Behaviour change: `also` entries no longer stop at the first match.**
  Every `also` route now renders `continue: true`, so an alert matching two
  entries reaches both, beside its primary route. Before, the first matching
  entry won and masked the rest.

## v0.22.0

Feature (opt-in, default off; no existing render moves): an `owner` label for
routing an alert to the owning company. `observability-emitters`
`tenancy.owners` (owner to namespace patterns, optional `defaultOwner`) stamps
`owner` on every series (the metrics agent's global relabeling, after the
scrape-level rules, so kube-state-metrics and cAdvisor series are right) and
on every OTLP log record, span and metric (the gateway). `platform-alerts`
`ownerLabel` keeps it in the aggregating rules' `by (...)`;
`observability-stack` `notifications.ownerLabel` lets a route `match` on it.
Container logs from the log agent carry no `owner`; see
`docs/tenancy-owner.md`.

`observability-emitters`: documentation only, no render change. A change to
a `kubeStateMetrics.customResources` preset is picked up live by
kube-state-metrics' own config reload (since v2.8.0; the vendored subchart
runs v2.19.1), with no pod restart; `docs/kube-state-metrics.md` says how to
check, and a new test pins the no-`subPath` mount the reload depends on.

## v0.21.1

Published by the automatic patch workflow; it carries two new opt-in options (an HTTPRoute for the connectors and configurable DNS egress) and a behaviour change to the default DNS egress rule.

`observability-mcp` and `alert-ingress`: the NetworkPolicy's DNS egress rule
no longer names the `kube-system` namespace. A new optional key,
`networkPolicy.egress.dns`, takes NetworkPolicy peers (passed through
verbatim) to narrow it again.

- **Behaviour change**: the DNS egress rule (UDP and TCP 53) now has no `to`,
  so it admits port 53 to any destination, where it used to admit only pods in
  `kube-system`. The old default blocked every lookup on clusters whose
  resolver is not a `kube-system` pod (managed Kubernetes, node-local DNS), so
  the pods could not resolve the issuer or Alertmanager. A consumer that wants
  the old scope sets `networkPolicy.egress.dns` to
  `[{namespaceSelector: {matchLabels: {kubernetes.io/metadata.name: kube-system}}}]`,
  or to the resolver's service CIDR as an `ipBlock`. Every other egress rule is
  unchanged.

`observability-mcp`: an opt-in HTTPRoute for the connectors, shipped with
their Services. A new optional key; the default is off and no existing
render moves.

- `httpRoute` (`enabled`, `parentRefs`, `hostnames`, `name`, `annotations`,
  `labels`) renders ONE `gateway.networking.k8s.io/v1` HTTPRoute in the
  release namespace. Per enabled connector (each store, and Grafana) it has a
  PathPrefix rule for the resourceURL's path and one for
  `/.well-known/oauth-protected-resource<path>`, both to that connector's
  Service on the proxy port; plus one Exact rule for the bare
  `/.well-known/oauth-protected-resource` to the Grafana connector when
  enabled, else the first store. No request timeout (`timeouts.request: 0s`),
  because MCP holds responses open as server-sent event streams. The name
  is `observability-mcp-connectors`; set `httpRoute.name` to change it.
- Refused at render: `httpRoute.enabled` with no `parentRefs`, a resourceURL
  with no path (PathPrefix `/` would take the whole host), two connectors at
  one path, and, when `hostnames` is set, a resourceURL on another host.
- Why the route belongs here and not in a separate gateway release: a route
  applied before its Services exist reads `BackendNotFound` and degrades
  whatever owns it. See docs/mcp.md, "Exposing connectors through a Gateway".

## v0.21.0

`observability-mcp`: a private CA for the outbound target, and the pod port
for the NetworkPolicy. Both are new optional keys; no existing render moves.

- A store's `vmauth.caBundle` (and `grafana.caBundle`) takes
  `{configMap: {name, key}}` or `{secret: {name, key}}`, exactly one, a PEM
  bundle for a private CA that signs an https target. The chart mounts it
  read-only into the proxy and sets `OUTBOUND_CA_FILE` to it; a connector
  without `caBundle` renders neither. It needs the resource-proxy release that
  adds `OUTBOUND_CA_FILE`. Both sources at once, neither, or `caBundle` on an
  http URL is refused at render.
- `vmauth.podPort` and `grafana.podPort` (integer, 1-65535) set the port of the
  NetworkPolicy's egress rule to the target. The port was always derived from
  the URL, which names a Service, while a NetworkPolicy matches the pod's port:
  a Service on 80 in front of a pod on 3000 rendered a rule for 80 that matched
  nothing, and every tool call timed out. Unset, the behaviour is unchanged;
  the trap, and the `ipBlock` case for a cross-cluster https URL, are
  documented in `values.yaml` and `docs/mcp.md`.

## v0.20.0

`observability-stack`, `observability-emitters`: CPU requests sized from
measurement, Burstable by default.

- **Behaviour change: CPU requests drop and the CPU limit is gone on every
  component default, so every pod rolls and its QoS class becomes
  Burstable.** Measured on a three-cluster install, the whole stack used
  well under a tenth of the cores its defaults reserved (vmsingle p95 about
  70m, vlogs about 15m, vmagent about 15m, everything else under 5m), and a
  pair of one-core requests per component tripped node-loss overcommit
  alerts on autoscaled clusters. The new requests are about 1.5 x p95,
  rounded up, with a floor of 10m, and more for the stores, whose merges,
  compaction and queries are bursty and grow with the data (the window was
  a few days, so keep headroom and re-measure):

  | Component | CPU request before | CPU request now |
  |---|---|---|
  | vmsingle | 2 | 500m |
  | victoria-logs-single | 2 | 250m |
  | victoria-traces-single | 1 | 100m |
  | vmauth | 1 | 100m |
  | vmalert (each) | 1 | 50m |
  | victoria-metrics-operator | 1 | 50m |
  | emitters vmagent (`metrics.resources`) | 1 | 100m |
  | emitters OTLP gateway collector (`otlp.resources`, per replica) | 1 | 50m |

  Alertmanager and Grafana were not measured: their request stays at 1
  core and only their CPU limit is gone. Memory is unchanged everywhere
  and stays request == limit, so the pods stay out of the kubelet's first
  eviction tier. With no CPU limit a merge or a heavy query takes idle
  cores instead of being throttled, and VictoriaMetrics sizes its thread
  pool from the node's cores instead of a quota. Rolling the stores
  restarts them: do it in a quiet window.
- **Behaviour change: `resources.policy` defaults to `burstable`** (it was
  `guaranteed`). Only what is ACCEPTED differs; a values file that wrote
  its own requests equal to limits still renders. To keep the Guaranteed
  class, set `resources.policy: guaranteed` and write each component's own
  `resources` with a whole-number CPU limit equal to its request: the
  defaults carry no CPU limit, so the render refuses them under
  `guaranteed` and names the component.
- A `limits: {cpu: null}` written for vmauth, a vmalert or Alertmanager
  (the way to drop their old default CPU limit) still means "no limit";
  the chart now strips the null itself.

## v0.19.0

`platform-alerts` and `observability-stack`: alert on what fails when nodes
are provisioned on demand, and switch off the alert that cannot.

- **`platform-alerts`: opt-in `groups.pendingPods`, alert `PodUnschedulable`.**
  Fires for one pod that has been unschedulable (`kube_pod_status_unschedulable
  == 1`) for `for` (default `10m`, severity `warning`). Off by default, so no
  existing render changes. It needs `kube-state-metrics`' `pods` collector,
  which `observability-emitters` already collects by default.
  Two options for a store that holds several clusters: `namespaceSelector`
  (this group's own, empty inherits the top-level one) and
  `keepClusterLabel` (the series' own cluster label survives instead of
  being overwritten by `commonLabels`; default `false`).
- **`observability-emitters`: opt-in `kubeStateMetrics.customResources.nodeClaims`.**
  Exports Karpenter's NodeClaims (`karpenter.sh/v1`) as
  `nodeclaim_status_condition{nodeclaim,nodepool,type,reason}`, one series
  per condition (Launched, Registered, Initialized, Ready and two more) per
  NodeClaim, 1 when True and 0 when False or Unknown. Grants only
  `get`/`list`/`watch` on `nodeclaims.karpenter.sh`. It composes with the
  `kargo` preset. Off by default; existing renders do not change. On EKS
  Auto Mode Karpenter's own metrics cannot be scraped, but its NodeClaim
  objects can be read.
- **`platform-alerts`: opt-in `groups.nodeClaims`, alerts `NodeClaimNotReady`
  and `NodeClaimMetricsAbsent`.** `NodeClaimNotReady` fires when a
  NodeClaim's Launched, Registered or Initialized condition has not been
  True for `for` (default `10m`, inside Karpenter's 15 minute registration
  TTL, after which it deletes the claim and retries). `NodeClaimMetricsAbsent`
  is the deadman for the series (`absentFor`, default `30m`). Off by default;
  `keepClusterLabel` as for `pendingPods`. NodeClaims are cluster-scoped, so
  no namespace filter applies.
- **`observability-stack`: documented and tested, no default moved.**
  `victoria-metrics-k8s-stack.defaultRules.rules.<Alert>: {enabled: false}`
  drops one upstream alert from what the sync job applies (checked against
  the vendored sync job image). An estate on Karpenter or EKS Auto Mode,
  where requests above allocatable is the normal state, uses it for
  `KubeCPUOvercommit` and `KubeMemoryOvercommit` and leaves the
  namespace-quota alerts on.

## v0.18.0

`observability-stack`: a Slack destination can ping people.

- **Additive: `mention: here | channel` on a Slack destination.** Beside
  `channel`/`workspace` in `notifications.severities.<tier>`,
  `notifications.catchAll` and the object form of a route's per-tier
  override, it starts the message text with Slack's `<!here>` /
  `<!channel>`, for FIRING notifications only (a resolved one pings
  nobody). Unset is no mention, and nothing renders differently for an
  install that does not set it: a destination without a mention keeps its
  receiver name. A destination with one is its own receiver,
  `slack-<workspace>--<channel>--<mention>`, so `critical` can ping
  `@here` while `warning` posts quietly to the same channel. A `mention`
  on a non-Slack receiver, or a value outside the enum, is refused.
- `observability-mcp`: one connector per Victoria store, and one for Grafana.

- **BREAKING (values interface, `observability-mcp`; a chart no release has
  asked anyone to adopt): `servers.*` is replaced by `stores[]` and
  `grafana`.** 0.16.0's chart rendered one Deployment per `servers.<name>`
  and only `metrics` worked. It has no consumer yet, so it is replaced
  rather than migrated; a leftover `servers:` is refused by the schema. The
  chart now knows two things and nothing about an estate's topology: a
  **store** (VictoriaMetrics, VictoriaLogs and VictoriaTraces behind one
  vmauth) and **Grafana**. Each `stores[]` entry (`name`, `resourceURL`,
  `vmauth.url`, `outbound.{tokenEndpoint,clientId,audience}`, optional
  `clusters`, `signals`, `metricsMetadata`, `instructions`) renders ONE
  connector, `observability-mcp-<name>`: a pod with the resource-proxy, the
  new aggregator and the three stock servers, exposing 26 read-only tools
  grouped as `metrics_*`, `logs_*` and `traces_*`. `grafana.enabled` renders
  `observability-mcp-grafana`: the proxy and the stock `mcp-grafana`,
  dashboards only (seven tools, `--disable-write`). Pinned images with
  digests, the verified read-only allowlists, the tool prefixes and the
  instructions template are chart defaults; nothing is enabled by default.
  The same chart renders one store with Grafana, several stores with
  Grafana, or a store alone (goldens `single-store`, `two-stores`,
  `minimal`, `everything`). What the consumer sets per connector, and what
  each tool needs the store's vmauth to serve (two tools were dropped
  because their path is served by no read route), is in
  [docs/mcp.md](docs/mcp.md).
- **New `cmd/mcp-aggregator`, released as an image**
  (`ghcr.io/truvity/observability/mcp-aggregator`, linux/amd64 and arm64,
  and a tar.gz per architecture; the chart's `aggregator.image.tag` defaults
  to its own appVersion). A generic, config-driven MCP server over several
  backends: tools only (resources and prompts are answered method-not-found),
  a static allowlist, `<prefix>_<tool>` names, descriptions and schemas
  verbatim, arguments and results passed through untouched, a session per
  backend call and none exposed, a per-call timeout and cancellation, partial
  failure that fails only the dead backend's tools, refusal to become ready
  when an allowlisted tool is missing, Prometheus metrics and health
  endpoints, and an unused per-tool authorization hook. Built on the official
  Go SDK (v1.8.0) in its stateless mode, so one process answers both the
  2026-07-28 revision (no sessions) and the 2025-06-18 handshake.
- **`observability-grafana`: opt-in `global.observabilityGrafana.workloadAuth`.**
  Off by default, and with it off every existing render is byte for byte what
  it was. On, `grafana.ini` gains an `[auth.jwt]` section that accepts an
  access-issuer workload token in `Authorization: Bearer`, verified against
  the issuer's JWKS with `iss` and `aud` pinned, and gives the identity
  (login `workload:<sub>`) a Viewer role that is a constant in the config: no
  claim can ask for more, and the server-admin flag is never set. The
  audience is what gates it; the chart refuses one equal to the human
  sign-in client's. This is how the Grafana connector authenticates with no
  stored secret. See [docs/grafana.md](docs/grafana.md), "Workload sign-in".
- **New refusals** (`observability-mcp`): a store with no or a duplicate
  `name`, a name that is not a slug or is `grafana`; a connector with no or a
  duplicate `resourceURL`, or incomplete `outbound`; a store with no
  `vmauth.url` or one carrying a path; every signal off; an allowlist that is
  empty or would expose a name over 64 characters; `grafana.url` empty; an
  empty `enabledTools`; a NetworkPolicy peer list empty for what is enabled.
  (`observability-grafana`): `workloadAuth` enabled with an empty `issuer`,
  `jwksUrl` or `audience`, or with `audience` equal to `oauth.clientId`.
- **New opt-in peer list** `networkPolicy.metricsFrom` (who may scrape the
  aggregator's `/metrics`), and `networkPolicy.egress.grafana`.
- **New case** `tenancy-mcp-reader` in `observability-stack` (a machine
  reader with all three routes, `vmalertAPI` and the acknowledgements a store
  connector needs), a new golden only; `tests/mcp_routes_test.go` proves every
  exposed tool's path is served by it and the dropped ones are not, and
  `hack/mcp-paths-proof.sh` observes the same paths from the real stock
  binaries. No existing golden of another chart changes.
- **Behaviour change (`observability-mcp` only):** the `minimal` and
  `everything` goldens of `observability-mcp` moved, because the values they
  render from were replaced (see the BREAKING bullet above): `servers.metrics`
  is now a `stores[]` entry, and a store's pod carries the aggregator and the
  three stock servers where 0.16.0's carried one. No opt-out: the chart has no
  consumer and the old shape is gone. Every golden of every other chart is
  unchanged, `observability-grafana`'s included (the new opt-in renders
  nothing while off).
- The release workflow builds both images; `hack/check-image-refs.py` now
  looks for an own-registry image at any depth of a chart's values.

## v0.17.0

`observability-stack`: Slack posts with a bot token per workspace.

- **BREAKING (values interface): `notifications.slack.webhookSecret` is
  removed; use `notifications.slack.workspaces`.** One incoming webhook
  ignores the `channel` a message asks for and always posts to the one
  channel it was created for, so routing to several channels through it
  could not work. A leftover `webhookSecret` is refused at render, and the
  message names the replacement. Migration: create one Slack app per
  workspace (manifest in [docs/notifications.md](docs/notifications.md),
  "Slack": bot scopes `chat:write` and `chat:write.public`), store its bot
  token (`xoxb-...`) in a Secret, and replace

  ```yaml
  slack:
    webhookSecret: {name: slack-webhook, key: url}
  ```

  with

  ```yaml
  slack:
    workspaces:
      - name: acme
        appTokenSecret: {name: slack-bot, key: token}
  ```

  The token is mounted and read with Alertmanager's `app_token_file`
  (v0.30.0 or later; the pinned operator deploys v0.34.0), never
  interpolated. Receivers are now named `slack-<workspace>--<channel>`
  instead of `slack-<channel>`; nothing else refers to a receiver name.
- **`workspace` on every Slack destination.** `severities.<tier>`,
  `catchAll` and a route's per-tier override gain an optional `workspace`:
  omitted it means the one declared workspace, and with two or more
  declared it is required. A route's per-tier value may now be a channel
  string (unchanged) or `{channel, workspace}`. One receiver renders per
  distinct (workspace, channel). Several workspaces are supported.
- **New refusals:** a leftover `webhookSecret`; a `workspace` no entry
  declares; `workspace` omitted while two or more are declared; an empty
  Slack channel; two workspaces with one name; a workspace with an empty
  name, secret name or key; `workspace` on a non-Slack destination; a
  `failureReceiver` naming Slack, an unconfigured receiver, or set with no
  workspaces. `update_message` is not rendered: Alertmanager v0.34.0
  crashes loading it beside `app_token_file`.
- **New alert `SlackNotificationsFailing`** (`selfAlerts.slackDelivery`):
  `alertmanager_notifications_failed_total{integration="slack"}` increased
  over 15 minutes. It renders whenever a workspace is declared, without
  `selfAlerts.enabled`. It is never routed to Slack: a route at the top of
  the tree (`continue: false`) sends it to the new optional
  `notifications.slack.failureReceiver` (a webhook name, or `telegram`).
  Unset, it goes to the null receiver: visible in vmalert, reaches nobody.
- **Alertmanager is now scraped.** A new `ServiceMonitor`
  (`<release>-alertmanager-scrape`, port 9093, `/metrics`, no credentials)
  renders whenever Alertmanager does, so `alertmanager_notifications_*`
  lands in the metrics store and `SlackNotificationsFailing` can fire. The
  VMAlertmanager sets `disableSelfServiceScrape: true`, so the operator's
  own `VMServiceScrape` is not created beside it. The existing refusal for
  a disabled ServiceMonitor converter now covers this object too. No
  NetworkPolicy selects the Alertmanager pods, so the agent is not
  refused; a test holds that line.
- **Behaviour change:** every existing golden with a Slack receiver moves.
  The Slack receivers and their volume and mount names change as above,
  a `SlackNotificationsFailing` rule and its top route render, and every
  golden with Alertmanager gains the scrape `ServiceMonitor` and
  `disableSelfServiceScrape: true`.
  `selfAlerts.slackDelivery.enabled: false` drops the rule (the top route
  stays, harmlessly). New cases `notifications-slack-one-workspace`,
  `notifications-slack-two-workspaces` and
  `notifications-slack-failure-receiver`.

## v0.16.0

A new chart, `observability-mcp`: read-only MCP servers over the store.

- **New chart `observability-mcp`.** One Deployment per enabled server, each
  the stock upstream MCP server on loopback (no credential, no container
  port) beside a `resource-proxy` sidecar, the pod's only port, that
  validates the caller's access token against the issuer and holds the one
  credential the store sees, obtained by exchanging the pod's projected
  ServiceAccount token. `servers.metrics` runs the stock
  `mcp-victoriametrics` v1.20.2 in single-node mode against the store
  proxy's `/prometheus` read path, with every non-read, debugging, Cloud and
  cross-principal tool disabled. `logs`, `traces` and `dashboards` are
  disabled values stubs (enabling one is refused). Nothing is enabled by
  default, the proxy image has no default tag, and an enabled server with no
  `issuerURL`, `resourceURL`, complete `outbound` settings or NetworkPolicy
  peers is refused. The NetworkPolicy lets the pod reach the issuer, the
  store's proxy and cluster DNS, and nothing else. See
  [docs/mcp.md](docs/mcp.md), which also records what the store's proxy must
  route for the `alerts` and `rules` tools.
- **`observability-stack`: opt-in `tenancy.principals[].vmalertAPI`.** A
  principal with `vmalertAPI: true` also gets the metrics vmalert's alerts
  and rules, at `/prometheus/vmalert/api/v1/alerts` and `.../rules`,
  forwarded as `/vmalert/api/v1/...`. **Unfiltered**: alerts and rules are
  not tenant-scoped, so the principal's grants do not limit them; it
  requires `tenancy.allowUnfilteredAlertReads` and is refused when `routes`
  excludes `metrics` or `vmalert.enabled` is false. Off by default; no
  existing render changes. New case `tenancy-vmalert-api` and
  `hack/vmalert-api-proof.sh`.
- The release publishes it to `oci://ghcr.io/truvity/charts/observability-mcp`.
  No existing chart, default or golden changes.

## v0.15.3

`observability-grafana` links each store's logs to its traces and back.

- **Behaviour change: every store that provisions both a logs and a traces
  datasource now carries cross-links, on by default.** The logs datasource
  gains a derived field `TraceID` on the structured field `trace_id` (where
  VictoriaLogs stores an OpenTelemetry log record's trace id) that opens the
  SAME store's `<name>-traces` on that id; the traces datasource gains
  `tracesToLogsV2` pointing at the same store's `<name>-logs`, with the
  LogsQL query `trace_id:"<id>"` from five minutes before the span to five
  minutes after. A store with `logs: false` or `traces.enabled: false` gets
  neither. The `minimal` and `everything` goldens move, and because the
  datasources ConfigMap name carries a hash of its content, the Grafana pods
  roll once. Set `global.observabilityGrafana.correlate: false` to keep the
  plain datasources (the `no-correlation` case renders that). A log line
  that only embeds a `traceparent` or `trace_id=` in its message text has no
  `trace_id` field and gets no link.

## v0.15.2

`RecordingRulesNoData` no longer fires for a recording that is empty by
design.

- **Behaviour change: the vendored `RecordingRulesNoData` alert ignores
  `count:up0`.** Upstream's alert (vmalert's self-monitoring group) fires when
  a recording rule produced no samples for 30 minutes. `count:up0`, from
  kube-prometheus's `general.rules`, is `count without(instance, pod, node)
  (up == 0)`: it has data only while a target is down, so on a healthy estate
  it is empty and the alert fired exactly when nothing was wrong. The
  expression is now
  `sum(vmalert_recording_rules_last_evaluation_samples{recording!~"count:up0"})
  without(id) < 1`, set through `victoria-metrics-k8s-stack.defaultRules.rules.
  RecordingRulesNoData.spec.expr`, so the alert's `for`, labels and
  annotations stay upstream's. `count:up0` itself is kept and still records
  the moment a target goes down; every other recording, `count:up1`
  included, is still watched. Every render with the sync job's config moves by
  that one `rules:` block. To get upstream's alert back, set
  `victoria-metrics-k8s-stack.defaultRules.rules.RecordingRulesNoData.spec.expr`
  to upstream's own expression, `sum(vmalert_recording_rules_last_evaluation_samples)
  without(id) < 1`. `hack/recording-nodata-proof.sh` runs the real sync job and a real
  store.

## v0.15.1

node-exporter no longer has a CPU limit. Nothing changes for an install that
leaves `nodeExporter.enabled` at its default.

- **Behaviour change: the node-exporter DaemonSet drops its CPU limit
  (`nodeExporter.resources.limits.cpu`, was `100m`).** The 10m CPU request,
  the 64Mi memory request and the 64Mi memory limit are unchanged. The
  collectors all run in parallel when a scrape lands, so a scrape is a short
  burst well past 100m on a pod that averages about 1m; measured on
  production-sized nodes, 30 to 40% of active CFS periods were throttled,
  which fired `CPUThrottlingHigh` and added 0.2 to 0.3 s to each scrape. The
  request still reserves the scheduling share and the memory limit and the
  lean collector set bound a misbehaving exporter. Only the render of the
  opt-in `node-exporter` case moves. To keep a CPU limit, set
  `nodeExporter.resources.limits.cpu` yourself.

## v0.15.0

`charts/observability-emitters` gains a node-exporter, off by default, and the
dashboards and the allow-lists learn what an optional source is. Nothing
changes for an install that leaves `nodeExporter.enabled` at its default: no
existing golden render moves.

- **New: `nodeExporter.enabled` (default `false`) runs node-exporter as a
  DaemonSet, from the upstream `prometheus-node-exporter` chart vendored
  under `charts/`.** Pinned to 4.55.1 (node-exporter 1.11.1), the version
  the vendored `victoria-metrics-k8s-stack` carries and leaves disabled, for
  the reason kube-state-metrics is pinned to its copy: one render of the
  upstream chart per estate. It is off by default because a cluster may
  already run one (a second install exports every `node_*` series twice),
  and because it runs with the host's network and PID namespaces and the
  host's `/proc`, `/sys` and root filesystem mounted read-only. It runs as
  `nobody` with a read-only root filesystem and listens on the node's own
  address rather than every interface.
- **Scheduling is the log collector's.** `system-node-critical` (a node too
  full for it is a node with no metrics, so it may preempt), tolerate every
  taint (`operator: Exists`, NoExecute included), `kubernetes.io/os: linux`,
  and small measured resources: requests 10m CPU and 64Mi, limits 100m CPU
  and 64Mi (memory request equals limit). Where it must not run, pass the
  same `affinity` value a log collector takes, under
  `prometheus-node-exporter.affinity`, for instance a `nodeAffinity`
  `NotIn` over the node pools of ephemeral CI nodes. Upstream merges that
  over its own default (no Fargate, no virtual-kubelet nodes); a
  `nodeAffinity` you set replaces that default. `nodeSelector` and
  `tolerations` are upstream's own keys, under the same name.
- **A lean collector set.** Upstream turns on about forty collectors; this
  chart turns them all off (`--collector.disable-defaults`) and names 26:
  `arp`, `conntrack`, `cpu`, `diskstats`, `entropy`, `filefd`, `filesystem`,
  `hwmon`, `kernel_hung`, `loadavg`, `meminfo`, `netclass`, `netdev`,
  `netstat`, `pressure`, `processes`, `schedstat`, `sockstat`, `softnet`,
  `stat`, `tcpstat`, `time`, `timex`, `udp_queues`, `uname` and `vmstat`:
  the families the node-exporter dashboard and `k8s-views-nodes` query, and
  what `node.rules` and `kube-prometheus-node-recording.rules` record from.
  Left out: `interrupts` (a series per CPU per IRQ), `systemd` (needs the
  host's D-Bus socket), `cpufreq`, `thermal_zone`, `rapl` and
  `powersupplyclass` (per CPU or sensor, absent on a cloud VM),
  `zoneinfo`, `slabinfo`, and the storage and network-fabric collectors
  (`nfs`, `xfs`, `zfs`, `btrfs`, `mdadm`, `nvme`, `ipvs`, `infiniband` and
  the like). The pod interfaces the CNIs mint per pod (`veth*`,
  `eni<hash>`, `lxc*`, `cali*`, `cilium_*`, `flannel*`) are excluded from
  `netdev` and `netclass`, and the per-pod mounts under
  `/var/lib/kubelet/{pods,plugins}` and the container runtime's own are
  excluded from `filesystem`: a node's series count is decided by what is
  plumbed and mounted, and those churn with every pod. Measured against the
  real binary on an 8-CPU machine with nine interfaces, this set holds 776
  series against 1023 for upstream's defaults; count on roughly 600 to 800
  for an 8-vCPU node, and about 20 more per CPU (`cpu`, `softnet` and
  `schedstat` are per CPU).
- **The scrape is a `ServiceMonitor`, and stores `job="node-exporter"`.**
  The kube-prometheus convention the dashboard and the k8s-stack's rules
  select on (the operator would otherwise name the job for the Service),
  with `instance` and `node` both set to the node's name (a stable identity
  rather than `ip:9100`, and what the rules group on). The cluster and the
  tier come from the default scrape class, like every other series. Node
  series carry **no `k8s_namespace_name`**: a node belongs to no namespace,
  so a grant on the exporter's own namespace does not read them, only a
  grant on all namespaces of the cluster does, the same as the kubelet's and
  cAdvisor's node-level series. The exporter pod's own `namespace` and `pod`
  labels stay, because `node.rules` joins on them (`node_cpu_seconds_total`
  to `kube_pod_info`, to find each series' node).
- **New refusals**, each a way for the DaemonSet to be Ready, scraped and
  wrong or silent: `nodeExporter.enabled` with `metrics.enabled` false; the
  `ServiceMonitor` disabled; `hostNetwork`, `hostPID` or
  `hostRootFsMount.enabled` false; and a `relabelings` list without a step
  setting `job`, `instance` and `node` (docs/safety.md).
- **The dashboards allow-lists have an optional source.**
  `hack/dashboards/available-metrics.yaml` and `available-labels.yaml` no
  longer list node-exporter as absent: it is a source marked `onlyWith:
  node-exporter`, which counts only for a dashboard whose catalog entry says
  `requires: [node-exporter]`. Every other dashboard is still held to the
  default install, where the `node_*` series are absent, so a dashboard that
  does not declare the dependency and reads one still fails the test. The
  label check now judges node-exporter's job (for a dashboard that requires
  it) instead of exempting the dashboard. Only the families that exist on
  every Linux node are claimed: not `hwmon`, `pressure`, `conntrack` or
  `schedstat`, which depend on the kernel or the hardware.
- **New dashboard: `k8s-views-nodes`** (dotdc/grafana-dashboards-kubernetes
  v3.0.8, Apache-2.0, the same pin as the other Kubernetes views, with its
  notice): one node's CPU, memory, disk, network and pods. **Off by default**
  (`dashboards.k8s-views-nodes.enabled: false`), like the source it needs;
  `node-exporter-full` keeps its existing default. Its panels on
  conntrack and CPU throttling are dropped at import (not claimed, above).
- **A finding to read before enabling the k8s-stack's node recording
  rules.** `node.rules` works with these labels: it joins on
  `(k8s_cluster_name, namespace, pod)` and every output carries the
  cluster. `kube-prometheus-node-recording.rules` does not: its
  `instance:node_*:rate:sum` rules group by `instance` alone and
  `cluster:node_cpu:sum_rate5m` and `:ratio` aggregate with no `by`, so five
  of its six outputs carry no cluster label and, on a store several clusters
  write to, merge across clusters. The cluster-label rewrite of 0.14.2 can
  only rename a `cluster` an expression already names. Enable `node.rules`;
  leave the other group off on a shared store.
- **Proof.** `hack/node-exporter-proof.sh` (`just node-exporter-proof`): the
  chart's rendered DaemonSet args on the real node-exporter, scraped by the
  pinned vmagent with the chart's rendered relabel steps into the pinned
  VictoriaMetrics, then the real sync job's `node.rules` and
  `kube-prometheus-node-recording.rules` evaluated by the pinned vmalert.
  With the chart's `job` step the series are stored as
  `job="node-exporter"` with `k8s_cluster_name` and all eleven rules of the
  two groups record; with the step removed the four `node.rules` outputs
  that read node-exporter record nothing.

## v0.14.3

Grafana no longer starts before its dashboards exist, and the dashboard
allow-list stops claiming series no scrape produces.

- **Behaviour change: `observability-grafana` now runs the dashboards sidecar
  as a native sidecar that Grafana waits for.** Grafana watches the home
  dashboard's directory once, at start. It used to start before the sidecar
  had written the first ConfigMaps, so that directory did not exist yet:
  Grafana logged `failed to watch home dashboard directory ... no such file
  or directory`, and the home dashboard did not hot-reload until the pod was
  restarted (the page itself still loaded, the file appearing seconds later).
  The chart now sets `grafana.sidecar.dashboards.initDashboards: true`,
  `restartPolicy: Always` and a startup probe on the sidecar's `/healthz`,
  which answers only after the first sync: the sidecar moves from
  `containers` to `initContainers` (as `grafana-init-sc-dashboard`), keeps
  watching for the life of the pod, and Grafana starts once it is healthy.
  The dashboards, folders and reload behaviour are unchanged. Native
  sidecars need Kubernetes 1.29 or later; on an older cluster set
  `grafana.sidecar.dashboards.initDashboards: false` (and clear
  `restartPolicy` and `startupProbe`) to get the previous layout.
- The maintainers' allow-list of live metrics
  (`hack/dashboards/available-metrics.yaml`) no longer lists
  `kube_state_metrics_`: it is kube-state-metrics' self-telemetry, served on a
  port the chart does not scrape (`kube-state-metrics.selfMonitor` is off).
  `kube_horizontalpodautoscaler_` moves to an optional source: the collector
  is on, but a cluster with no HorizontalPodAutoscaler produces no such
  series. No dashboard queries either, and no chart render changes.

## v0.14.2

The vendored Kubernetes recording rules now join and aggregate on the label
every series in these stores actually carries.

- **Behaviour change: the recording rules the vendored
  `victoria-metrics-k8s-stack` installs now carry `k8s_cluster_name`.**
  Upstream's default rules (`k8s.rules.container_cpu_usage_seconds_total`,
  `k8s.rules.container_memory_*`, `k8s.rules.container_cpu_limits` and
  `_requests`, `k8s.rules.pod_owner`, and the alerting rules that join the
  same way) join and aggregate on a label named `cluster`. No series here
  has one: emitters stamp the cluster as `k8s_cluster_name`, and the tenancy
  readers filter on it. So the recorded series carried no cluster identity
  (a reader filtered on `k8s_cluster_name` could not see them, and a
  per-cluster dashboard could not use them), and on a store several clusters
  write to, a join on `(namespace, pod, cluster)` matched the same namespace
  and pod across clusters, giving a duplicate-series error (HTTP 422) or
  merged data. vmalert showed those groups healthy on a single-cluster
  store, which is why nothing caught it. The chart now sets
  `victoria-metrics-k8s-stack.global.clusterLabel` to `k8s_cluster_name`
  (was upstream's `cluster`); the sync job rewrites the fetched rules to it
  when it runs. After upgrading, the recorded series are written with the new
  label: series recorded before the upgrade, which have no
  `k8s_cluster_name`, stop receiving samples and go stale, and any query or
  dashboard of your own that read them without it keeps working only until
  they age out of retention. Every golden render of `observability-stack`
  changes by that one line in the sync job's ConfigMap.
- **New refusal: `tenancy.clusterLabel` and
  `victoria-metrics-k8s-stack.global.clusterLabel` must be equal.** The
  second is a subchart value and cannot be computed from the first, so it is
  a mirror (docs/reference.md, "Why some values appear twice"). An install
  that sets a non-default `tenancy.clusterLabel` must now set the vendored
  one to the same name. There is no separate opt-out to the old `cluster`
  label: it would put the recorded series back on a label nothing stamps.
- **`hack/k8s-stack-cluster-label-proof.sh` (new, run by hand).** Runs the
  vendored sync job's real image against the rendered config and a
  throwaway API server, reads back the recording rule it applies, then
  evaluates the rewritten and the upstream form on a real
  victoria-metrics with two clusters that share a namespace and pod name.

## v0.14.1

`pkg/rulecheck` parses every VMRule expression on the real VictoriaMetrics and
VictoriaLogs binaries, and this repository's own CI now runs it.

- **`pkg/rulecheck` and `cmd/rulecheck` (new).** Every alerting and
  recording expression in a VMRule is parsed by the real VictoriaMetrics
  (MetricsQL, `/api/v1/query`) or VictoriaLogs (`type: vlogs`, LogsQL,
  `/select/logsql/stats_query`) release binary, so an expression the
  operator's admission webhook would refuse is found before it reaches a
  cluster. The library is `rulecheck.Rules` / `Load` (read VMRule
  documents), `Check(ctx, rules, Options{VMVersion, VLVersion, BinDir,
  CacheDir})` (returns one `Finding` per refused expression, with the
  rule's source, resource, group, type and expression, the parser and its
  version, and the parser's own error), and `ChartVersions` /
  `RenderedVersions` (the parser tags, read from the vendored stack chart
  or from a rendered one, so they follow the chart and are not written
  down twice). The binaries are downloaded from the projects' GitHub
  releases, verified against each release's published sha256, and cached;
  `BinDir` (`-bin-dir`, `RULECHECK_BIN_DIR`) serves offline use. A group of
  an unknown `type` and an empty rule set are errors, never skips. No
  chart changes and no default moves.
- **`just rulecheck`, and a `rulecheck` CI job.** Runs the CLI over the
  golden renders (or the paths given) and exits non-zero on any refused
  expression. It is part of `just check`, so every rule the charts render
  is parsed in CI: platform-alerts, the stack's own self-alerts and
  Watchdog, and alert-ingress's. The vendored default rules the upstream
  chart's sync job fetches at install time are in no render, so they are
  not covered.

## v0.14.0

`pkg/statusbox` covers what a consumer with its own host type needs to
render through `RenderGatus` instead of keeping a parallel copy.

- **`CompanyHost.Component` (new, optional).** A caller-side label for the
  product that owns a host, so a consumer can keep one host type end to end.
  It is never rendered: no byte of the Gatus YAML depends on it, so leaving
  it empty changes nothing. Backward compatible.
- The leading YAML comment `RenderGatus` writes when `Catalogue.Providers`
  is empty is the library's own text, not a consumer's; a consumer that
  used to prepend its own wording sees a comment-only difference and no
  difference in the parsed config.

New chart `observability-grafana`: one Grafana as the read UI across several
stores. Additive; no existing render changes.

- **`charts/observability-grafana`** wraps the same `grafana` subchart
  (12.7.3) that `observability-stack` vendors, for an estate whose Grafana
  spans installs rather than sitting beside one. Per store it provisions
  both a `victoriametrics-metrics-datasource` and a `prometheus`-typed
  datasource on the same URL (community dashboards list only the latter),
  logs and traces, with stable uids `<store>-prom`, `-metrics`, `-logs`,
  `-traces` and `isDefault` on exactly one prometheus-typed row.
  `renamedDatasources` renders `deleteDatasources`, so a renamed
  datasource no longer crash-loops new pods on "data source with the same
  uid already exists".
- It also owns sign-in against an OIDC issuer (`use_refresh_token`,
  `role_attribute_strict`, a required role mapping, the client secret from
  a Secret), an external PostgreSQL, alerting and analytics off, and the
  git-only dashboards sidecar and provider (`updateIntervalSeconds`
  defaults to 30). The home dashboard path is rendered only when the
  consumer sets it.
- **Values live under `global.observabilityGrafana`**, not at the top
  level: Helm hides a parent's values from a subchart, and the subchart
  renders the datasource mount and the ini file. Pod-level settings
  (replicas, scheduling, mounts, images) stay under `grafana`.
  `docs/grafana.md` has the interface and the reasoning.
- Refuses: a dashboard sidecar interval of 10 seconds or less, no or two
  `default` stores, a duplicate datasource uid, a `renamedDatasources`
  entry that is a current datasource name, a missing issuer, client id,
  client secret, role mapping, root URL, database or session key
  (`secretKeyRef.builtInKey: true` opts into the built-in one), and
  `use_refresh_token` or `role_attribute_strict` overridden off.
- The chart is added to the release workflow's list, so the tag publishes
  it to `oci://ghcr.io/truvity/charts/observability-grafana`.

## v0.13.1

A reader can now be selected by SEVERAL groups, so a role held under one
group per cluster reads every cluster it is entitled to instead of only the
first one vmauth happens to try.

- **`tenancy.principals[]` accepts `groups` and `name`** as an alternative to
  `group`. A token carrying ANY of the listed groups selects the principal
  and reads EVERY grant it lists. Each group is escaped on its own and
  joined into one anchored alternation
  (`^(cluster-a:role|cluster-b:role)$`), so the only unescaped `|` is the
  one the chart wrote; `name` becomes the VMUser's name. A principal sets
  `group`, or `groups` with a `name`, never both; a group may appear in one
  principal only; each refusal has a fixture under `tests/invalid/`.
  `pkg/tenancy.Principal` gains the same `Groups` and `Name` fields, and
  the chart and the library are compared for the new shape in
  `tests/agreement_test.go`. No existing render changes and no default
  moves: a principal that sets `group` renders byte-for-byte as before, and
  the new `tenancy-multi-group` golden case is a new file.
- **Why it exists.** vmauth selects the FIRST user a token matches and never
  a union of several. With one principal per cluster, each selected by that
  cluster's group, a token holding all of the groups is filtered to whichever
  cluster's user comes first. One principal with every group and one grant per
  cluster reads them all; metrics OR the grants' selectors, and the logs
  filter is one stream filter with the grants as `or` alternatives.
- **Read the consequence before using it.** A token holding only ONE of
  the listed groups reads EVERY cluster in the principal's grants. That is
  correct exactly when every holder of one spelling holds all of them; if
  roles can diverge by cluster, mint per-person `vm_access` claims
  (`pkg/tenancy.RenderClaim`) instead.
- **`hack/multi-group-reader-proof.sh`** proves it against real vmauth,
  VictoriaMetrics and VictoriaLogs (`just multi-group-reader-proof`): a
  token with all the groups, and one with a single group, read every
  cluster's series and log lines; an unmatched group or audience gets 401;
  the one-principal-per-cluster shape reads one cluster only. Run by hand,
  not part of `check`.

## v0.13.0

The `observability-emitters` cadvisor series now carry the labels the
kube-prometheus recording rules select on.

- **Behaviour change: every cadvisor series is now stored with
  `job="kubelet"` instead of `job="cadvisor"`**, keeping
  `metrics_path="/metrics/cadvisor"` (the label 0.12.1 added, which is what
  tells them from the kubelet's own `metrics_path="/metrics"` series). It is
  the identity kube-prometheus gives the same series, and the k8s-stack's
  default recording rules (`k8s.rules.container_cpu_usage_seconds_total`,
  `.container_memory_working_set_bytes`, `.container_memory_cache`,
  `.container_memory_rss`, `.container_memory_swap`) and the mixin
  dashboards select `job="kubelet", metrics_path="/metrics/cadvisor"`, so
  under the old label those rules matched nothing and everything built on
  them (`node_namespace_pod_container:*`) stayed empty. The scrape is still
  named `cadvisor` inside vmagent (names must be unique); a relabel step
  sets the stored `job`. The `observability-emitters` goldens change to
  match. Two consequences to know before you upgrade:
  - **Every cadvisor series changes its `job` label once**, so each cluster
    sees a one-time burst of new series of about the cadvisor series count
    (the old series go stale and age out of the store on their own).
  - **A query, dashboard or alert outside this repo that selects
    `job="cadvisor"` must switch to `job="kubelet",
    metrics_path="/metrics/cadvisor"`.** Shipped dashboards and rules here
    never selected it. `up{job="kubelet"}` now has two series per node, one
    per `metrics_path`; a query that counts kubelets should keep filtering
    `metrics_path="/metrics"`, as the shipped kubelet dashboard does.

  `metrics.scrape.cadvisorAsKubeletJob: false` restores `job="cadvisor"`; its
  golden (`cadvisor-own-job`) is byte for byte the 0.12.1 render. Proof:
  `just metrics-path-proof` stores the fixture's cadvisor series as
  `job="kubelet", metrics_path="/metrics/cadvisor"`, evaluates the upstream
  `sum_irate` recording rule against them (data; none with the opt-out) and
  checks the kubelet dashboard's "Running Kubelets" still counts one per
  node.

Dashboards for the platform components an install runs: ArgoCD,
cert-manager, CloudNativePG, NATS, Envoy Gateway and Kargo, and a line of
health tiles for each on the home page. Additive; every new dashboard is on
by default and can be turned off one key at a time.

- **Behaviour change: the default `observability-dashboards` render gains
  nine ConfigMaps and the Fleet overview gains rows.** The new dashboards
  are `argocd`, `cert-manager`, `cnpg-operator`, `nats-server`,
  `nats-jetstream`, `envoy-gateway`, `envoy-proxy`, `envoy-clusters` and
  `kargo`, in a new folder `Platform` (`folders.platform`, one
  `dashboards.<name>.enabled` key each, default true). The Fleet overview
  home page gains a second per-cluster block, "Platform components", of one
  line of "is it healthy?" tiles per component: ArgoCD (applications not
  Synced, not Healthy, targets down), cert-manager (certificates expiring in
  14 days, not Ready, targets down), NATS (servers down, JetStream disabled,
  slow consumers in the last hour), Envoy (5xx per second, 5xx share of
  requests, proxies down) and CloudNativePG (operator reconcile errors in
  the last hour, operator down). Kargo's tiles were already in the cluster
  row. A tile for a component a cluster does not run reads "n/a", not a
  green 0. Both existing goldens (`minimal`, `everything`) change to match;
  no existing dashboard other than the Fleet overview changes. Set
  `dashboards.<name>.enabled: false` to drop a dashboard; the Fleet
  overview's tiles link to `argocd`, `cert-manager`, `nats-jetstream`,
  `envoy-proxy` and `cnpg-operator`, so disabling one of those leaves a link
  that opens "not found".
- **Where each one comes from** (source, licence and pinned ref are in
  `THIRD_PARTY_NOTICES.md` and `dashboards/catalog.yaml`): ArgoCD, argo-cd
  v3.5.3 `examples/dashboard.json`; cert-manager, the cert-manager mixin
  v1.6.0 overview; CloudNativePG, `cloudnative-pg/grafana-dashboards`
  cluster-v0.0.5; NATS, the prometheus-nats-exporter v0.20.2 walkthrough
  dashboards (server and JetStream); Envoy Gateway, envoyproxy/gateway
  v1.9.2 `envoy-gateway-global`, `envoy-proxy-global` and `envoy-clusters`.
  All Apache-2.0. `kargo` is authored here (no upstream ships one): Stages
  and Promotions from the state-metrics preset, and the controller's
  reconcile and work-queue health. Upstream pickers that collide with the
  contract's names are renamed (ArgoCD's `cluster` is the application
  destination, Envoy's `cluster` and `namespace` are an Envoy cluster and a
  route namespace).
- **Panels left out, on purpose.** Every panel reads only metrics and labels
  in the allow-lists. ArgoCD: the ApplicationSet controller row (that
  controller is not scraped). CloudNativePG: every instance panel, since the
  `cnpg_*` instance exporter is not scraped yet (recorded as
  `deferred: [cnpg-instance-metrics]` in the catalog); what remains is the
  operator's own row plus reconcile rate, duration and queue depth. Envoy
  Gateway: the Wasm row, the three TCP listener panels of `envoy-proxy`
  (`envoy-tcp-listeners`) and the outlier-detection panel of `envoy-clusters`
  (`envoy-outlier-detection`), since Envoy creates those series only when the
  feature is used; the panic counter panel is kept and reads 0 when the
  counter does not exist yet. cert-manager: the two ACME client panels
  (`acme-issuer`), created only when an ACME issuer is used. NATS: the
  cumulative message and byte counters are drawn as rates, and the
  walkthrough dashboards' metric names are rewritten from the exporter's
  default prefix to the `nats_` prefix the NATS chart runs it with
  (`metricPrefixRewrite` in `sources.yaml`).
- **The allow-lists grow.** `hack/dashboards/available-metrics.yaml` lists
  each platform family by exact name (not by prefix), and
  `hack/dashboards/verify-live.sh` (maintainers, before a release; not CI)
  prints, per source, which listed names a live store lacks.
  `hack/dashboards/available-labels.yaml` gains one job per scrape with the
  job names the converters produce: a ServiceMonitor's job is its Service
  name, a PodMonitor's is `<namespace>/<PodMonitor name>`. A dashboard that
  reads anything else still fails the query test.
- **Import tooling.** `hack/dashboards.py` gains an `adapt: platform` mode
  (`hack/dashboards/platform_adapt.py`, with a small PromQL scanner in
  `promql_scan.py`): it drops the rows and panels a source names, converts
  legacy `graph` panels, adds the cluster filter to every selector, and
  refuses a replacement that matches nothing.

## v0.12.1

A fix for the shipped `kubelet` dashboard, which read "No data" on every
install, and a check that would have caught it.

- **Behaviour change: the `kubelet` and `cadvisor` scrape jobs of
  `observability-emitters` now write a `metrics_path` label** on every
  series they collect (`/metrics` on the kubelet job, `/metrics/cadvisor`
  on the cadvisor job), and the `observability-emitters` goldens change to
  match. It is the label kube-prometheus adds to the same jobs, and the
  kubernetes-mixin dashboards and rules select on it: the `kubelet`
  dashboard's `cluster` variable is `up{job="kubelet",
  metrics_path="/metrics"}`, so without the label the variable was empty and
  every panel showed "No data" although the `kubelet_*` series were in the
  store. It adds no series (one constant value per job), and it appears on
  the series from the next scrape; the dashboard works from then on. There
  is no switch for it; a query of your own that selects on the absence of
  `metrics_path` on these two jobs is the only thing it could affect.
- **The dashboard query check now reads label matchers, not only metric
  names.** `hack/dashboards/available-labels.yaml` says which scrape-derived
  labels (`job`, `metrics_path`, `instance`, `node`, the tenancy labels)
  each scrape job's series carry and which values the chart fixes; a test
  fails any shipped dashboard whose `label="value"` selector can never
  match, and another proves the node jobs' claims against the rendered
  goldens. Catalog entries for dashboards that depend on an optional source
  (`node-exporter-full`, `alertmanager`) say so with `requires:` and are
  exempt until that source is enabled. `hack/metrics-path-proof.sh` runs the
  rendered jobs in a real vmagent against a real VictoriaMetrics and shows
  the dashboard variable returning the cluster.

## v0.12.0

The first operational dashboards in `observability-dashboards`: a home
page that answers "is anything wrong, and where?", and the Kubernetes
drill-down under it. Additive; every new dashboard is on by default and
can be turned off one key at a time.

- **Behaviour change: the default `observability-dashboards` render gains
  four ConfigMaps** (`fleet-overview`, `k8s-views-global`,
  `k8s-views-namespaces`, `k8s-views-pods`) and the two existing goldens
  (`minimal`, `everything`) change to match. Every existing dashboard also
  gains a navigation row (a tag-based links row, and one tag) so the whole
  set links to itself; no query or panel of an existing dashboard changes.
  Set `dashboards.<name>.enabled: false` to drop any of the four.
- **Fleet overview** (folder `Fleet`, the designated home page). Firing
  alerts by severity with a table of alert, cluster, namespace and
  severity, then one row per selected cluster (`repeat` by `$cluster`,
  multi-select, default All): pods not Ready, CrashLoopBackOff,
  ImagePullBackOff, restarts in the last hour, nodes NotReady, nodes under
  memory, disk or PID pressure, PVCs over 85% full, rows dropped or
  ignored by the stores, and Kargo stages not healthy and promotions
  errored (they read `n/a` where the Kargo state metrics are not enabled).
  The write path (rows ingested, rows dropped or ignored, per store) is
  charted beneath. Every tile links down, carrying datasource, cluster,
  namespace and time range.
- **Kubernetes views** (folder `Kubernetes`): cluster, namespace and pod
  drill-down, adapted from `dotdc/grafana-dashboards-kubernetes` v3.0.8
  (Apache-2.0; source, licence and pinned ref are recorded in
  `dashboards/catalog.yaml`). The `cluster` label becomes
  `k8s_cluster_name`; every panel says what it shows and what to do; a
  series in a namespace or pod panel opens the next level down. Cluster
  and node utilisation is answered from cadvisor's root cgroup. Left out
  on purpose: `k8s-views-nodes` (built on node-exporter, which no install
  runs yet), the node-exporter panels of the global view, and the resource
  counts of kinds outside kube-state-metrics' 11-collector allow-list
  (configmaps, secrets, services, endpoints, ingresses, network policies).
- **New value `home`** (default `fleet-overview`): the dashboard designated
  as the home page. It annotates that ConfigMap
  `observability-dashboards/home: "true"`, and the render refuses a name
  that is not an enabled shipped dashboard. Point Grafana at it with
  `grafana.ini` `dashboards.default_home_dashboard_path:
  <sidecar folder>/<folders.fleet>/fleet-overview.json` (default
  `/tmp/dashboards/Fleet/fleet-overview.json`), or set it as the org's home
  dashboard by uid `truvity-obs-fleet-overview`. New values `folders.fleet`
  and `folders.kubernetes`, and one `dashboards.<name>` key per new
  dashboard.
- **Licence compliance for vendored dashboards.** New
  `THIRD_PARTY_NOTICES.md` (one entry per vendored dashboard: upstream, URL,
  pinned ref, SPDX licence, copyright, modification) and `LICENSES/`, both
  generated from `hack/dashboards/sources.yaml`, copied into the chart
  directory so they ship in the package, and checked by a test. Every
  upstream is Apache-2.0 (VictoriaMetrics, VictoriaLogs, VictoriaTraces,
  kube-prometheus, rfmoz/grafana-dashboards, dotdc); none ships a NOTICE
  file. Each vendored dashboard's `description` now names its upstream and
  says it was modified.
- **Only metrics a store holds.** `hack/dashboards/available-metrics.yaml`
  lists what is scraped today; `tests/dashboard_queries_test.go` fails a
  dashboard flagged `queryCheck` that reads anything else, and parses every
  query on the pinned VictoriaMetrics (`just dashboard-queries`). A
  dashboard for node-exporter, ArgoCD, cert-manager, CNPG, NATS, Envoy
  Gateway or Karpenter therefore fails until that source ships.

## v0.11.3

One defect, seen live on an install where several clusters write into one
metrics store (the documented shared-store topology, told apart only by
the `k8s_cluster_name` label):

- **Behaviour change: `platform-alerts` rules now match per cluster.**
  Two clusters can share a namespace, a Job name or a PVC name. The
  rules joined series on `(namespace, job_name)`,
  `(namespace, persistentvolumeclaim)` and similar, without the cluster
  label, so on a shared store the join found the same key twice and the
  whole query failed with a duplicate-series error (HTTP 422). vmalert
  reported the rule `health: err` and it never fired, silently.
  `BackupJobFailed` and `VolumeSmallerThanClaimed` were dead that way.
  Three more were wrong without failing: `CronJobNotSucceeding` took the
  newest success across all clusters, so one cluster's fresh success hid
  another's stale one; `KargoPromotionErrored` paired a promotion with a
  Stage condition from a different cluster; and the newest-Job and
  suspend clauses inside `BackupJobFailed` compared across clusters. Every
  `on (...)` and `by (...)` list in those rules now leads with the
  cluster label, and each alert carries the label of the cluster it is
  about. Nothing changes on a single-cluster store: where the label is
  absent on both sides it matches on empty, and the rules fire exactly as
  before (proved against VictoriaMetrics in
  `hack/platform-alerts-cluster-proof.sh`). New value `clusterLabel`
  (default `k8s_cluster_name`, the name `observability-stack`'s
  `tenancy.clusterLabel` defaults to; set both if you renamed it). The
  opt-out is `clusterLabel: ""`, which renders the 0.11.2 expressions byte
  for byte. Not changed, on purpose: `WritePathDead` (sums one store's own
  counter) and the `KargoStateMetricsAbsent` deadman (fires only when no
  cluster exports the series). Caveat: the label must be on both sides of
  each join or on neither, which remote emitters guarantee by stamping it
  on every series; a store that stamps it on some metrics only should set
  `clusterLabel: ""`.

## v0.11.2

No render changes. Two guard rails in CI, so two independent release
sessions cannot repeat a mistake by memory alone:

- The leak canary now also fails on any tracker-key-shaped token
  (two to six capitals, a dash, digits) anywhere in the tree, CHANGELOG
  included, with a short commented allow-list of public vocabulary. The
  one such key that reached 0.11.1 is rewritten out of the comments that
  carried it.
- A pull request that changes an existing golden must add a
  bold "Behaviour change" bullet to CHANGELOG.md
  (`hack/default-change-guard.sh`, `just default-change-guard`). Adding a new golden is unaffected.

## v0.11.1

Two defects, both found live on an estate's first cutover to
`observability-stack` 0.11.0:

- **The logs/traces backup CronJobs could not run as shipped, with store
  auth on** (mandatory since 0.11.0). `backup.image`'s only HTTP client,
  busybox wget, has no `--user`/`--password` — confirmed directly against
  the pinned `rclone/rclone:1.73.0` (`wget: unrecognized option`); both
  jobs' snapshot-API calls now build the `Authorization: Basic` header by
  hand (`--header`, base64 of `user:pass`, `base64 -w0` so a longer
  generated credential is never wrapped across lines). Separately, both
  jobs handed `rclone` an `s3://bucket/path` destination, which rclone
  reads as a remote named `s3` that does not exist ("didn't find section
  in config file") and does nothing, successfully; `backup.destination`
  is now translated into rclone's own connection-string syntax
  (`:s3,env_auth=true:bucket/path`) for these two jobs only — the metrics
  job's `vmbackup` already understood the URL form directly, and
  `env_auth=true` resolves credentials from the identical chain
  `vmbackup`'s AWS SDK already does, under every `backup.auth.mode`, so
  nothing else changes per mode. `backup.destination` and every
  `backup.<store>.prefix` are unchanged; only the rendering for the two
  rclone-driven jobs moved. Proved end to end against real
  `victoria-logs`/`victoria-traces` binaries, store auth on, and a real
  S3-compatible endpoint: `hack/backup-logs-traces-proof.sh` (`just
  backup-logs-traces-proof`).
- **The vendored `alertmanager.rules`/`vmalert.rules` VMRule sources, and
  the `alertmanager-overview`/`victoriametrics-vmalert`/`grafana-overview`
  dashboards, silently disappeared** whenever `victoria-metrics-k8s-
  stack`'s OWN `alertmanager.enabled`/`vmalert.enabled`/`grafana.enabled`
  are off — which they are, unconditionally, in every install of this
  chart, because it runs its own Alertmanager/vmalert/Grafana instead.
  Upstream gates those four sync-job sources on the VENDORED flags, so
  they were gated on a condition that was never true, in every install,
  with nothing saying so; an estate adopting this chart had to force all
  four back on by hand after they vanished on cutover. `values.yaml` now
  overrides the two rule sources tied to `alertmanager.enabled`/
  `vmalert.enabled` to `true` (correct by default, since those two
  default `true` themselves) and leaves the `grafana-overview` dashboard
  at upstream's own default (`grafana.enabled` here defaults `false`,
  unlike the other two). New refusal,
  `observability-stack.validate.vendoredSyncSources`: a caller who turns
  `alertmanager.enabled`/`vmalert.enabled`/`grafana.enabled` off (or
  `grafana.enabled` + `defaultDashboards.enabled` on) must now also set
  the matching `victoria-metrics-k8s-stack.defaultRules.sources.*`/
  `.defaultDashboards.*` entry — Helm cannot compute a subchart's value
  from this chart's own (docs/reference.md, "Why some values appear
  twice"), so a caller who changes only one half now gets a render-time
  failure naming exactly what to set, instead of the same silent gap.
  New regression gate: `tests/vendored_sync_sources_test.go`, and
  `tests/cases/observability-stack/everything` now also exercises
  `defaultDashboards.enabled: true`.

## v0.11.0

`charts/observability-stack` becomes adoptable by an estate run by one
operator. Every rule below was a hard requirement shaped for a
multi-team install; each is now a value whose default is the old
behaviour, so an existing values file renders byte-for-byte as it did
under 0.10.0 — except for the two fixes at the end of this entry, whose
golden diffs are exactly their own lines. New goldens `small-estate` and
`notifications-catchall` cover the new shapes. See docs/reference.md,
"The single-operator estate".

- **Store credentials stay mandatory.** No switch. docs/adoption.md,
  "Store credentials: who needs them", now lists every client that needs
  the credential and which of them the chart wires itself; `/health` and
  `/ping` need none.
- **`resources.policy: guaranteed | burstable`** (default `guaranteed`,
  today's rule). `burstable` accepts a fractional CPU request below the
  limit and no CPU limit at all; a CPU limit that is set stays a whole
  number (the quota is what the thread pool rounds), and memory stays
  request == limit with a limit required (VictoriaMetrics sizes its
  caches from it, and it keeps the pod out of the kubelet's first
  eviction tier). New refusal, under either policy: a `null` in a
  vendored subchart's `resources`, which Helm passes through and the API
  server reads as a limit of 0.
- **Alert routing.** `notifications.groupBy` and
  `notifications.inhibit.{enabled, equal}` (defaults: today's
  `k8s_cluster_name`/`k8s_namespace_name` shape). `notifications.catchAll`
  routes `info`, severity-less alerts and any tier `severities` leaves out
  to a receiver of your choice — and routes `Watchdog` to nobody when no
  deadman receiver is configured. `notifications.drop` sends exact
  matches to the null receiver ahead of every route.
  `notifications.telegram.chatIdSecret` reads the chat id from a Secret
  with Alertmanager's `chat_id_file` (Alertmanager v0.31.0+), instead of
  `chatId`. docs/notifications.md, "Inhibition", documents the hazard of
  an `equal` label your alerts do not carry.
- **Backup prefixes**: `backup.metrics.prefix` / `.fullPrefix`,
  `backup.logs.prefix`, `backup.traces.prefix` (defaults `metrics`,
  `metrics-full`, `logs`, `traces`). Refused: two prefixes in use that
  are equal or nest — vmbackup and `rclone sync` delete at their
  destination whatever the source does not have.
- **Grafana without the proxy.** With `vmauth.enabled: false` the
  datasource refusal flips: no `oauthPassThru`, basic auth from
  `storeCredentials` through `grafana.envValueFrom` environment
  references, checked as a mirror; Grafana's pods are admitted to the
  stores' NetworkPolicies in that mode only. An enabled Grafana with
  `GF_SECURITY_SECRET_KEY`'s Secret left unnamed is now refused at
  render rather than by the API server.
- **`networkPolicy.clientsFrom`**: extra in-cluster clients of the
  stores (a prober, a collector, a hand-made job), each admitted to the
  stores it names. Refused: an empty `from` (admits everything) and a
  peer named by `ipBlock` alone.

Two fixes change a DEFAULT render, each with an opt-out that renders the
0.10.0 output byte for byte (proved by the goldens
`single-inhibit-unguarded` and `platform-alerts/suspended-not-ignored`):

- **Behaviour change: the inhibit rule is guarded**
  (`notifications.inhibit.requireLabels`, default `true`). Alertmanager
  compares a label missing on both alerts as equal, so a critical
  without `k8s_cluster_name`/`k8s_namespace_name` muted every warning
  without them — install-wide, silently. It hit on a default install:
  `platform-alerts`' `CronJobNotSucceeding` (critical) and
  `BackupJobFailed` (warning) aggregate `by (namespace, …)`, dropping
  `k8s_namespace_name`, so one CronJob not succeeding muted every failed
  backup Job's warning on the cluster. The rule now carries a
  `<label> =~ ".+"` source matcher per `equal` label; warnings it muted
  by accident start arriving. `requireLabels: false` restores it.
- **Behaviour change: `platform-alerts`' backup rules skip suspended
  CronJobs** (`groups.backups.ignoreSuspended`, default `true`). A
  deliberately suspended backup fired `CronJobNotSucceeding` and
  `BackupJobFailed` forever. Both now `unless` on
  `kube_cronjob_spec_suspend == 1` — never `and == 0`, so a cluster
  without that series keeps the old behaviour rather than going silent.
  Proved against a real VictoriaMetrics by
  `just platform-alerts-suspended-proof`. `ignoreSuspended: false`
  restores the old expressions.

## v0.10.0

Two additions to `charts/observability-stack` an estate needs before it
can move a hand-made alerting and backup setup onto this chart. Both
are additive: an existing values file renders byte-for-byte as it did
under 0.9.1 (every existing golden is unchanged).

- **Telegram as a receiver kind** (`notifications.telegram`), beside
  `slack` and `webhook`, in any combination, and counted by the "no
  receiver configured" refusal. The bot token comes from an EXISTING
  Secret (`botTokenSecret: {name, key}`), mounted into the
  VMAlertmanager pod and read with `bot_token_file` — never a value,
  never in the rendered config. `chatId`, an optional
  `messageThreadId` (a forum topic), `parseMode` (default `HTML`) and
  `sendResolved` (default `true`); a tier sends to it with
  `severities.<tier>.receiver: telegram`, and may override the chat or
  thread for that tier. The shipped message carries the Slack
  template's facts in Telegram HTML, which Alertmanager escapes itself;
  any other `parseMode` requires your own `message`. `telegram` only
  becomes a keyword once `notifications.telegram` is set, so an install
  that already routes to a webhook NAMED `telegram` renders unchanged.
  See docs/notifications.md, "Telegram".
- **One ServiceAccount per backup store** (`backup.<store>.serviceAccount:
  {create, name, annotations}`, for `metrics`, `logs` and `traces`),
  under `backup.auth.mode: ambient` (and, for `metrics`,
  `credentialProcess`), so each store's backup can assume its own
  least-privilege role — an IRSA annotation per ServiceAccount, or an
  EKS Pod Identity association per name. A store with nothing set keeps
  running as the release-wide `backup.auth.serviceAccount`; once every
  enabled store has its own, the release-wide one is no longer
  rendered. Refused under `auth.mode: secret`, and for annotations the
  chart would not render. See docs/reference.md, "Least-privilege
  backups".

## v0.9.1

Metric-churn reduction: a default DROP on `charts/observability-emitters`'
cadvisor scrape, from a measurement against a real install (see
docs/safety.md, "Metric churn: what cadvisor never has read", and
docs/reference.md's own `metrics.scrape.cadvisorDrop` row for the full
argument and the override shape).

- **Behaviour change: the cadvisor scrape now drops four classes of
  series by default** — every consumer of `charts/observability-emitters`
  will see these stop being stored on the next upgrade, with no values
  change required:
  - `container_tasks_state`
  - `container_memory_failures_total`
  - `container_blkio_device_usage_total`
  - every `_bucket` histogram series cadvisor emits, **except**
    `go_sched_latencies_seconds_bucket`, which stays (heavily queried on
    the install this was measured against; the other three names and
    every other `_bucket` series read **zero** queries ever, per the
    store's own TSDB `requestsCount`).

  cadvisor and kube-state-metrics together accounted for roughly 97% of
  a store's daily new-series churn on that install; cadvisor alone for
  around 114,000 series a day, almost entirely through pod-recreation
  churn on its own identity labels (`id`, `uid`, `container`, `image`).
  kube-state-metrics' own churn is unchanged by this release — see
  docs/kube-state-metrics.md, "Reviewed for churn (0.9.1), unchanged":
  its existing collector allow-list is this repository's control there,
  and `kube_pod_status_reason` (heavily queried) is untouched.

  **Also new by default: cadvisor's `id` label (the cgroup path) is
  CLEARED from every SURVIVING series that already carries a non-empty
  `container` label — never unconditionally.** cadvisor also exports
  node-level cgroups that are neither a pod nor a container (the root
  `id: "/"`, `/kubepods.slice` and its QoS children, systemd units such
  as `/system.slice/containerd.service`), every one of them with
  `container=""` and `pod=""`, where `id` is the ONLY label telling
  them apart — an unconditional `labeldrop` would merge every one of
  those, on one node, into one identical label set and let
  vmagent/vmsingle deduplication keep an arbitrary sample. `container`
  non-empty is what makes `id` redundant: (`namespace`, `pod`,
  `container`) already names the series once `container` is set.
  Verified against every dashboard `charts/observability-dashboards`
  ships and every rule `charts/platform-alerts` ships: none of them
  groups, filters or joins on `id`. `pod`, `namespace`, `container` and
  `uid` are untouched; dashboards and rules use them.

  Every list is overridable and the whole default has a switch:
  `metrics.scrape.cadvisorDrop.metricNames` / `.keepBucketMetrics` are
  each replaced wholesale by a consumer who sets that key (ordinary Helm
  list semantics); `.extraMetricNames` / `.extraKeepBucketMetrics` add to
  the shipped default instead of replacing it; `.enabled: false` turns
  the whole thing off and stores cadvisor exactly as upstream sends it.
  The kubelet job is **not** touched by any of this.

## v0.9.0

"Consumer simplification" (D42): the estate repo (e.g. `truvity/gitops`)
becomes a plain consumer — values and estate data only. Mechanism moves
here. Nothing below is required before an existing values file still
renders; see docs/adoption.md's own "0.8.x → 0.9.0" for the ordered,
optional migration.

- **Behaviour change: `charts/observability-emitters`'s
  `victoria-logs-collector` (the container-log DaemonSet) now defaults
  to `priorityClassName: system-node-critical`, `tolerations:
  [{operator: Exists}]`, and `resources` of `{requests: {cpu: 15m,
  memory: 192Mi}, limits: {cpu: 100m, memory: 192Mi}}` — the shape
  every consumer of this chart was hand-writing already, measured
  across a live fleet's own node pools (a log agent uses a few
  millicores and well under 30Mi in practice; the old 250m/512Mi ask,
  and upstream's own whole-CPU default before that, were both wildly
  oversized for what the daemon spends, and upstream's own empty
  `priorityClassName` and `tolerations` left the DaemonSet exactly as
  evictable, and exactly as likely to land on only the untainted pool,
  as any ordinary workload). **This is a default-render change**: a
  consumer who did not already set all three will see `helm diff` move
  on the next bump. Set any of the three explicitly to keep the old
  shape.
- **Feature: `remote`, the single-destination sugar for
  `charts/observability-emitters`.** One `url`/`tokenSecret`/`caSecret`/
  `signals`, expanded into `metrics.destinations`, `otlp.destinations.
  metrics` and, per `signals`, `otlp.destinations.logs`/`.traces` — the
  shape a cluster with no store of its own writes everything to ONE
  central install through. Mutually exclusive with the low-level form
  for whichever signals it covers (refused together); the low-level
  form is unchanged and still fully supported for a multi-destination
  or per-signal-different install. `victoria-logs-collector.
  remoteWrite` (the log agent's OWN write path) is deliberately
  untouched by `remote` either way: it is a real Helm subchart's own
  values, and Helm coalesces a subchart's values before any template
  runs, so this chart cannot compute it the way it computes its own
  `metrics.destinations` / `otlp.destinations.*`. Golden-tested equal,
  byte-for-byte, to the hand-assembled low-level render it replaces
  (`tests/cases/observability-emitters/remote-single-destination` vs.
  `remote-tls-destination`).
- **Feature: `kubeStateMetrics.customResources.kargo.enabled` on
  `charts/observability-emitters`.** Turns on kube-state-metrics'
  `kargo_stage_condition` / `kargo_promotion_phase` — the exact shape
  `charts/platform-alerts`' `groups.kargo` already reads — without the
  ~40-line `customResourceState.config` block and its `rbac.
  extraRules` written by hand. Off by default. Mechanism: this chart
  cannot compute `kube-state-metrics.customResourceState.config` or
  `.rbac.extraRules` from the flag directly (the same subchart-values
  limitation `remote` works around differently above), so it instead
  points `customResourceState` at a ConfigMap this chart renders
  itself (`create: false`, content built from the preset — empty when
  no preset is on) and grants the two extra verbs through a ClusterRole
  of its own, bound to kube-state-metrics' ServiceAccount. Whenever
  `kubeStateMetrics.enabled` is true, `kube-state-metrics.
  customResourceState.enabled`/`.create` are now pinned by this chart
  (`true`/`false`) regardless of whether this preset is used — refused
  together with a consumer-authored `customResourceState.config`,
  which would otherwise never be read.
- **Feature: `charts/observability-stack`'s `mode: operator-only` turns
  four of its fifteen components off ITSELF.** `vmauth.enabled`,
  `vmalert.enabled`, `alertmanager.enabled` and `metricsSelfScrape.
  enabled` now default to `null` rather than `true`; `mode` resolves an
  unset value (`full` → `true`, `operator-only` → `false`) — an
  explicit `true`/`false` still pins it regardless of mode, and an
  explicit `true` beside `operator-only` is still refused, the same
  contradiction the mode always refused. `backup`/`selfAlerts`/
  `tenancy.{principals,writers,alertReaders}` needed no change: they
  already defaulted off/empty. The three stores' own `enabled`,
  `grafana.enabled` and `victoria-metrics-k8s-stack.syncJob.enabled`
  still need an explicit `false` in this mode, and the render still
  refuses if any is left on: each is a real Helm subchart's own value,
  which this chart cannot compute from `mode` for the same reason it
  cannot compute `victoria-logs-collector.remoteWrite` from `remote`
  above. `tests/cases/observability-stack/operator-only-implicit`
  proves the short-hand and long-hand shapes render byte-identical
  (`TestOperatorOnlySelfDisableIsByteIdentical`).
- **Feature: `pkg/statusbox.RenderGatus` and `pkg/statusbox.Catalogue`
  — a typed, estate-neutral Gatus page builder.** Ported from the
  first consumer's own `internal/components/status/gatus.go`: endpoint
  naming (`<host's first DNS label> · <env>`, with the collision
  fallback to the full hostname), per-endpoint probe URL/conditions (a
  `StatusPath` → the strict `[STATUS] == 200`, none → the lenient
  `[STATUS] < 500`, plus a certificate-expiry condition either way),
  the platform group, company groups, each company's alerts-read
  customer-facing signal and the deadman (both through the SAME pull
  path, `AlertsRead`), the OIDC `security` block, and storage. What
  stayed with the consumer, deliberately: deriving a `Catalogue` from
  that estate's OWN configuration files — a cfg-specific derivation,
  not this repository's to own. Golden-tested and fixture-tested with
  no network needed (`pkg/statusbox/gatus_internal_test.go`), plus a
  real `docker run twinproduction/gatus:v5.37.0` boot of a rendered
  page (`hack/gatus-boot-proof.sh`, `just gatus-boot-proof`).

## v0.8.5

- README gains `Consumers` and `Neighbours`; `docs/doctrine.md` points at the policy component contract; ci-workflows pins moved to v3.13.1.

## v0.8.4

- **Fix: `charts/observability-stack`'s metrics backup fails with
  `permission denied` on an SELinux-enforcing node (Bottlerocket's
  default on EKS Auto Mode), reading a snapshot it had already proven
  it could create.** The backup CronJob mounts the metrics store's
  ReadWriteOnce volume read-only, on the same node (a required pod
  affinity), and asks the store to snapshot itself before reading the
  snapshot straight off that shared volume. On an SELinux-enforcing
  node each pod gets its own random MCS categories at admission, and
  the volume is labelled for the STORE pod's categories — a second pod
  with different ones gets refused the read by the kernel, after auth,
  the bucket region lookup and the snapshot itself all already
  succeeded. `hack/backup-restore-proof.sh` cannot catch this: it runs
  in Docker, which enforces no SELinux at all.
  - New `backup.seLinuxLevel` (default `""`, byte-identical render):
    an SELinux MCS level (`"s0:c123,c456"`), validated against that
    shape and refused otherwise. Renders
    `securityContext.seLinuxOptions.level` on every enabled backup
    CronJob's pod — metrics, and logs/traces when their own backups
    are on.
  - It is a MIRROR, not a single computed value: Helm evaluates a
    subchart's values before any template in this chart runs, so this
    chart cannot also set the matching field on the metrics store's
    `VMSingle` CR or the log/trace stores' StatefulSet pod templates —
    those are vendored dependencies' objects. Write the identical
    string yourself at
    `victoria-metrics-k8s-stack.vmsingle.spec.securityContext.seLinuxOptions.level`
    (always, once `backup.seLinuxLevel` is set) and at
    `victoria-logs-single.server.podSecurityContext.seLinuxOptions.level`
    / `victoria-traces-single.server.podSecurityContext.seLinuxOptions.level`
    (only for a store whose own backup is enabled); the render refuses
    if one is missing or disagrees, the same discipline as `interval`
    and `storeCredentials` already document. Merges with whatever a
    store's own `securityContext`/`podSecurityContext` already sets —
    `runAsUser`, `fsGroup`, and so on — rather than replacing it.
  - Setting this restarts the affected store(s) once: the field lives
    on the pod template, so the CR/StatefulSet spec changes and its
    controller rolls the pod. See docs/reference.md,
    `backup.seLinuxLevel`, for the full symptom/cause/fix, and
    docs/safety.md, "SELinux MCS categories, and why one value cannot
    set both sides", for the incident and why a privileged SELinux
    type is not the default instead.

## v0.8.3

- **Feature: `cmd/alert-ingress` now ships a built, published image.**
  This repository previously shipped `charts/alert-ingress` with no image
  behind it (`.goreleaser.yaml`'s `builds: skip: true`) — every install
  had to build `cmd/alert-ingress` itself, and `image.repository` had no
  default and was refused empty. The release workflow now builds it
  multi-arch (linux/amd64, linux/arm64) and publishes it to
  `ghcr.io/truvity/observability/alert-ingress`, tagged at the release
  version and `latest`, following the same shape (ko, a distroless
  `nonroot` base, no SBOM) as `truvity/cloudflare`'s r2-broker image and
  `truvity/access-roster`'s own images.
  - `charts/alert-ingress`'s `image.repository` now defaults to that
    image, and `image.tag` (already optional) defaults to
    `.Chart.AppVersion` when left unset — the same pattern those two
    repositories' charts use. An install that sets neither now works out
    of the box; overriding either still works exactly as before, and an
    explicit empty `image.repository` is still refused.
  - New `hack/check-image-refs.py` (run from `just lint`) refuses a
    chart `image.repository` default naming an image `.goreleaser.yaml`'s
    `kos:` does not actually build, so the two cannot drift apart
    silently again.
- **Security: `github.com/go-git/go-git/v6` v6.0.0-alpha.4 →
  v6.0.0-alpha.5** (GHSA-hc8v-wwc9-vgxm / GO-2026-6214, path traversal
  via crafted reference names; GHSA-qgq7-7hm3-q39j / GO-2026-6213,
  worktree operations may follow symlinks), with `go-billy/v6`
  alpha.1 → alpha.2, which alpha.5 requires. It arrives only through the
  Pulumi SDK (`pkg/statusbox` → `pulumi/sdk/v3` →
  `go/common/workspace`), and govulncheck finds neither advisory
  reachable from this module's code. No chart render changes and no
  value moves; an importer of `pkg/statusbox` gets the fixed version.

## v0.8.2

- **Feature: `charts/observability-stack`'s backups authenticate WITHOUT
  a static key.** `backup.auth.mode` (default `secret`, unchanged
  behaviour) gains two new values:
  - `ambient`: no `credentialsSecret` anywhere. Every backup job's pod
    runs as a rendered ServiceAccount (`backup.auth.serviceAccount.name`/
    `.annotations`) instead, and each store's own AWS SDK resolves
    credentials from whatever ambient identity that carries — EKS Pod
    Identity (the association is made outside this chart) or IRSA (an
    `eks.amazonaws.com/role-arn` annotation). Confirmed against the
    pinned vmbackup v1.152.0's own source
    (`aws-sdk-go-v2/config v1.33.4`'s `resolve_credentials.go`): the
    default credential chain checks `AWS_WEB_IDENTITY_TOKEN_FILE` (IRSA)
    unconditionally, then `AWS_CONTAINER_CREDENTIALS_FULL_URI` +
    `AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE` (EKS Pod Identity, re-read
    on every refresh), before IMDS.
  - `credentialProcess`: for `backup.metrics` only (refused together
    with `backup.logs.enabled`/`backup.traces.enabled`). Renders an AWS
    config file (`credential_process = <auth.credentialProcess.command>`)
    and the env vmbackup's SDK reads it from, plus an initContainer that
    copies `auth.credentialProcess.toolsImage`'s own tools into
    vmbackup's minimal container (a fixed path,
    `/var/run/backup-tools`), and an optional projected ServiceAccount
    token for a broker CLI to present. The worked example is a
    credential-broker CLI that exchanges the pod's own token for
    temporary S3-compatible credentials — Cloudflare R2 is the store
    this was proven against, not something the mechanism names.
  - `backup.metrics.s3CustomEndpoint` / `.s3ForcePathStyle` — `vmbackup
    -customS3Endpoint`/`-s3ForcePathStyle`, for an S3-compatible store
    that is not AWS. Both default to rendering no flag.

  Both new modes are refused together with `backup.credentialsSecret`
  set, so a static key can never coexist with either.

- **Feature: `backup.nodeSelector`/`.tolerations`/`.affinity`/`.resources`,
  applied to every backup CronJob's pod.** No chart default (`{}`/`[]`):
  a tainted node pool where a store itself runs left every backup
  CronJob Pending before this, with no way to fix it from values alone.
  `affinity` MERGES with the hard pod affinity every job already
  carries onto its store's own node — that requirement is never
  dropped, even if this sets its own `podAffinity` key. `resources`
  feeds the same requests-equal-limits, integer-CPU check every other
  component's `resources` does, only when set.

- **Docs: restore, and the `backup.logs.enabled` default made
  explicit.** `docs/reference.md`'s `backup` section gains a "Restore"
  runbook — exact `vmrestore` flags for S3 and an S3-compatible
  endpoint, the "the target instance must be stopped" requirement, and
  how to prove a restore on a throwaway instance — which this chart had
  nowhere before. It also calls out, in the value table itself, that
  `backup.logs.enabled` defaults to `true`: turning `backup.enabled` on
  for metrics alone, with nothing else touched, silently backs up logs
  too.

  No values surface changes for a consumer who sets nothing new — every
  new value defaults to today's exact rendered output, proved by golden
  (`backup-ambient`, `backup-credential-process`, `backup-scheduling`
  cases added; every existing golden is byte-identical). The
  `credentialProcess` path (against a real MinIO-shaped S3-compatible
  endpoint, adobe/s3mock, since it is the harder one to trust from a
  reading of the template alone) plus a full restore is proved for real
  in Docker by `hack/backup-restore-proof.sh` (`just
  backup-restore-proof`).

## v0.8.1

- **Fix: `charts/platform-alerts`' `BackupJobFailed` now reads only the
  newest Job of each CronJob, and behaviour CHANGES for a standalone
  Job.** A CronJob's `failedJobsHistoryLimit` keeps a failed Job object
  around long after a later run succeeded, and the old expression —
  `max by (namespace, job_name) (kube_job_status_failed{...}) > 0`, with
  no `for:` — read every retained Job equally, so that old failure fired
  forever, even once every later run had succeeded. Live example: three
  week-old failed Jobs from two different CronJobs, whose newer runs had
  all succeeded, kept the alert firing.

  The rule now joins `kube_job_created` onto `kube_job_owner` to find
  the newest Job per `(namespace, CronJob)`, and fires only when THAT
  Job has failed — clearing the instant a later run of the same CronJob
  succeeds, or is merely running. This is a behaviour change: the rule
  is now scoped to `kube_job_owner{owner_kind="CronJob"}`-owned Jobs, so
  a standalone Job (no CronJob owner) no longer raises `BackupJobFailed`
  at all — "newest" presupposes a schedule a standalone Job does not
  have, and the rule's own name says its intent is backup CronJobs. See
  charts/platform-alerts/templates/vmrule.yaml's comment on the alert,
  and docs/safety.md, for the reasoning. `CronJobNotSucceeding`, the
  companion rule in the same group, was checked for the same class of
  bug and does not have it: it reads `kube_cronjob_status_last_successful_time`,
  a single gauge the CronJob controller itself keeps current on every
  run, not a per-Job value retained by history limits.

  No values surface change: `namespaceSelector` and `groups.backups.*`
  keep their existing meaning. Proved against a real victoria-metrics —
  not this repository's own hold-window model, which cannot evaluate a
  join — with `hack/platform-alerts-newest-job-proof.sh` (`just
  platform-alerts-newest-job-proof`).

## v0.8.0

- **Feature: `charts/observability-stack` accepts REMOTE writers, pinned
  by cluster.** `tenancy.writers[]` gains an optional `cluster` field.
  Set, it FORCES every series, log record and span that writer sends to
  belong to that cluster — overriding whatever `k8s_cluster_name` (or the
  log/trace equivalent) the writer's own collector config claims, not
  merely adding a second, ignored value beside it. This closes a real
  gap for a central install serving more than one remote cluster: a
  bearer token alone proves nothing about which cluster it actually ran
  on, so before this a writer could claim to be any cluster its own
  config stated, unchecked. Verified, per signal, against the pinned
  vmsingle and a live VictoriaLogs and VictoriaTraces:
  - metrics: `extra_label=<tenancy.clusterLabel>=<cluster>` on
    `/api/v1/write` and `/opentelemetry/v1/metrics`.
  - logs: `extra_fields=<tenancy.logsClusterField>=<cluster>` on every
    log write path.
  - traces: `extra_fields=resource_attr:<tenancy.logsClusterField>=<cluster>`
    on the OTLP trace write path — VictoriaTraces stores every OTLP
    resource attribute under a `resource_attr:` prefix, confirmed by
    reading a live ingest back rather than from any public doc.

  Unset (the default) renders exactly as before: the unscoped, local
  writer's `VMUser` carries no `query_args` at all. New refusals: a
  `cluster` that is not a plain name, one equal to the new
  `tenancy.ownCluster` (the unscoped writer's own job), and two writers
  sharing a `name` or a `cluster`.
- **Feature: `mode: operator-only`.** A new top-level switch for a
  cluster that holds no store of its own but still runs
  `charts/observability-emitters`: only the vendored VictoriaMetrics
  operator and its own webhook/CRD prerequisites render — no stores, no
  proxy, no vmalert, no Grafana, no backups, no dashboards or rules sync
  — one chart, so the operator version stays identical to every cluster
  running the full stack. It is a CONTRACT, not a silent override: every
  component the mode does not run must also be turned off explicitly, or
  the render refuses and says which one is still on.
- `tenancy.ownCluster`: the name this install's own cluster is known by.
  Optional; it exists only so a remote writer's `cluster` has something
  to be refused against, and does nothing left unset.

## v0.7.8

- **Feature: `charts/platform-alerts` gains an optional `kargo` rule
  group.** An estate that runs Kargo can now page on a stuck promotion
  from this chart directly, instead of carrying the rule as a bare
  `VMRule` in its own config. `groups.kargo.enabled` defaults to
  `false` — off, the render is byte-identical to 0.7.7 — and turning it
  on adds `KargoStagePromotionErrored` (a Stage's Ready condition
  reading `LastPromotionErrored` for `groups.kargo.for`, 15m by
  default), `KargoPromotionErrored` (the same failure corroborated by
  the Promotion object, joined on `(namespace, stage)` so a Promotion
  superseded by a later, successful retry cannot page on its own stale
  history) and `KargoStateMetricsAbsent` (the deadman for both: neither
  series exported for `groups.kargo.absentFor`, 30m by default). The
  series read `kargo_stage_condition` and `kargo_promotion_phase`,
  produced by kube-state-metrics' own `customResourceState` feature and
  configured by the estate, never by this chart — see
  docs/kube-state-metrics.md for a worked config, and docs/safety.md,
  "A Kargo promotion stuck", for the incident and the reasoning behind
  the join.

## v0.7.7

- **Feature: the status box can now trust a private root for one probe,
  without skipping TLS verification.** A private-service probe whose
  certificate is issued by the estate's own root (not a publicly-trusted
  one) used to fail Gatus's own TLS verification outright —
  `x509: certificate signed by unknown authority` — and that probe also
  carries a bearer token, so skipping verification was never an
  acceptable way around it. `statusbox.Args` gains an optional field,
  `TrustedCAs`, a PEM bundle of one or more extra CA certificates
  (validated at render time: each block must parse as an X.509
  certificate and must itself be a CA). Left empty — the ordinary case —
  nothing changes: no file is staged, no directory is mounted, no
  environment variable is set, and the rendered cloud-init is
  byte-for-byte what it was before this field existed. Set, `CloudInit`
  stages it the same way an instance's own Config already travels
  (gzipped, base64-encoded, inside the rendered script), `setup.sh`'s
  new `setup_trusted_cas` unpacks it to
  `/opt/statusbox/ca/extra-roots.pem`, and `write_compose` bind-mounts
  that directory read-only into every Gatus container and sets
  `SSL_CERT_DIR` in its environment — additive to the release image's
  own public trust bundle, never a replacement of it (see
  docs/statusbox.md, "Trusting a private root", for the `crypto/x509`
  evidence this relies on). Proven in Docker against the real release
  image, not just against `setup.sh`'s own logic: see
  `hack/statusbox-ca-proof.sh` / `just statusbox-ca-proof`.

## v0.7.6

- **Fix: the status box could not reach anything behind the tailnet's
  own subnet router.** `setup_tailscale` ran `tailscale up` with neither
  `--accept-routes` nor a working `--accept-dns`
  (`--accept-dns=false` was set outright) — a design left over from
  before this box pulled its own reads from an internal service by
  private name instead of having something pushed into it. Without
  `--accept-routes` a subnet-routed private IP had no path off the box
  at all; without the tailnet's own DNS a private name never resolved in
  the first place, so every internal probe failed fast, on both counts,
  regardless of which one an operator noticed first. `setup_tailscale`
  now runs with `--accept-routes --accept-dns=true`. This box is still
  never a router for anyone else — no `--advertise-routes`, no exit
  node — accepting routes only changes what it can itself reach.
  `write_compose` also now gives every Gatus container an explicit
  `dns:` entry naming the tailnet's own resolver directly, rather than
  depending on whichever way the host distribution's own DNS happens to
  be wired: a container's default resolver is not guaranteed to inherit
  that configuration otherwise. See docs/statusbox.md for what the
  estate's own tailnet policy has to grant this box's tag for both of
  these to actually resolve and route: the destination service, and the
  DNS resolver behind the same subnet router.

## v0.7.5

- **Fix: replacing the status box no longer fails attaching its disk.**
  A Config, Version or hostname change replaces the Lightsail instance
  (see docs/statusbox.md, "Immutable, by construction"), and Pulumi's
  default create-before-delete order tried to attach the disk to the new
  instance while the old `DiskAttachment` still held it — Lightsail
  refused with `AttachDisk ... the state of this disk is: in-use`, and
  the whole replacement failed. `pkg/statusbox/lightsail`'s
  `DiskAttachment` now carries `pulumi.DeleteBeforeReplace(true)`: the
  old attachment is deleted first (which itself briefly stops the old
  instance to detach the disk, then restarts it — the upstream
  provider's own delete behaviour, not something this package adds), so
  the disk is free by the time the new attachment is created. The new
  box only ever gets the disk after it has already booted.
  `InstancePublicPorts` needed no equivalent change: it shares its
  logical name with `Instance` rather than being addressed
  independently the way `Disk` is, so a replacement replaces it too, and
  the old and new firewalls are two independent rule sets scoped to
  their own instance — never the same underlying resource the two
  `DiskAttachment`s contend over.
- **Fix: the status box's data disk was never actually used.** `setup.sh`
  never formatted or mounted the disk `pkg/statusbox/lightsail` attaches
  — every instance's Gatus data went to `/data` on the ROOT filesystem
  instead, so history was lost on every replacement despite the docs'
  promise that the disk survives one. `setup.sh` now waits (up to 15
  minutes, then fails loudly — a box silently running on the root disk
  is exactly the failure nobody would otherwise notice) for the disk to
  appear, identifies it by shape rather than by device name (a
  current-generation Lightsail bundle surfaces the same disk as an NVMe
  device, not the configured `/dev/xvdf`), formats it ext4 with the
  label `statusbox-data` only if it carries no filesystem yet, adds an
  fstab entry keyed on that label, and mounts it before any instance
  directory is created under `/data`. The systemd unit now also carries
  `RequiresMountsFor=/data`, so a start or restart can never race ahead
  of the mount either.

## v0.7.4

- **Fix: the status box's user-data now actually boots on Lightsail.**
  `pkg/statusbox.CloudInit` rendered the whole bootstrap as
  `bash -c "$(echo <blob> | base64 -d | gunzip)"` with no leading `#!`
  line. cloud-init classifies a user-data payload by its first line
  alone — `#!` is what makes it run the payload as a shellscript at all,
  and anything else is stored as `text/plain` and never executed. Every
  box built from a version before this one silently never ran its
  bootstrap; nothing about the failure was visible anywhere. The
  rendered user-data now starts with `#!/bin/bash\n` followed by the
  same gzip+base64 wrapper as before. Lightsail's user-data field turns
  out to have no line-count constraint at all — only the existing 16 KB
  size cap — so this needed no change to that limit or to how the inner
  bootstrap is built, only to the one line cloud-init actually reads.
- **Fix: `setup.sh` no longer fails on Debian while installing the
  container runtime.** It asked apt for `docker-compose-v2`, a package
  that exists only on Ubuntu; on a real Debian 12 box (the blueprint
  this project targets) that failed with exit 100 under
  `set -euo pipefail`, before tailscale, cloudflared or any instance was
  ever set up. `install_container_runtime` now installs Docker from
  Docker's own apt repository for Debian — `docker-ce`, `docker-ce-cli`,
  `containerd.io` and `docker-compose-plugin`, following Docker's
  documented Debian install steps — instead of the Ubuntu-only package.
  A CI job now runs this function alone, unmodified, inside a plain
  `debian:12` container on every pull request (see
  `hack/statusbox-debian-ci.sh` and the `statusbox-debian` recipe): the
  existing `statusbox` job only ever ran the whole script on an Ubuntu
  runner, which could not have caught a package that installs on Ubuntu
  and nowhere else.

## v0.7.3

- **New: `pkg/statusbox.Secrets.Env`, a second, more general way to hand
  a secret to a Gatus `Config`.** `Secrets.AlertURLs` already let a
  Config reference `${ALERT_URL_<NAME>}` for a push-alert credential
  without carrying it as a literal; `Env` is the same mechanism for
  everything else a Config's own `${...}` substitution might need a
  secret for — Gatus's `security.oidc.client-secret`, most immediately.
  A key becomes the WHOLE variable name a Config writes (`${<NAME>}`, no
  added prefix), staged internally as `STATUSBOX_ENV_<NAME>` and
  collected by `setup.sh`'s `write_env` into the same `.env` file
  `AlertURLs` entries already land in. `Args.validate` refuses a name
  that is not a valid environment-variable identifier, that collides
  with `TS_AUTHKEY`/`TUNNEL_TOKEN`, or that collides with the
  `ALERT_URL_<NAME>` an `AlertURLs` entry already produces — two secrets
  under one `${...}` reference is a mistake worth refusing at deploy
  time rather than discovering in a container's environment. No default
  changes and no existing consumer is affected: an `Args` with no `Env`
  entries renders exactly as before.

## v0.7.2

- **Fix: the status box's Lightsail firewall now declares exactly one
  public port — 41641/udp, tailscaled's own WireGuard port — instead of
  an empty port list.** An empty list was always meant to read as "no
  port is public," but the AWS provider refuses that shape outright
  (Lightsail's `port_info` requires at least one entry), so a real
  `pulumi preview` against this package failed before it ever reached
  the cloud. "Nothing public" is now the narrowest port that still
  says that: 41641/udp only, open to both IPv4 and IPv6, so the box can
  take a direct, authenticated tailnet connection instead of always
  relaying through DERP. Nothing else changes — SSH and the status page
  itself were never reachable through this firewall and still are not;
  they answer only over the tailnet.
- A consumer pins `statusbox.Args.Version` to a release of this
  repository, so moving to this one to pick up the fix is itself a
  `Version` change — see docs/statusbox.md, "Immutable, by
  construction": the box is replaced on next deploy rather than
  updated in place, by design; about two minutes of status-page blip,
  the disk reattached, no history lost.

## v0.7.1

- **`setup.sh` now serves the status box's one private page on tailnet
  port 80, not the instance's own `Port`** — an operator reaches it at
  plain `http://<Hostname>/`, no port to remember or paste, the same
  way MagicDNS already lets them reach the box by name alone. Plain
  HTTP is intentional: the tailnet is WireGuard-encrypted end to end,
  so a second TLS termination in front of a page nothing outside the
  tailnet can even address buys nothing. Consumer-visible only if a
  bookmark or a runbook still names the old `:<Port>` URL — update it
  to drop the port.
- **`pkg/statusbox.Args.validate` now refuses more than one non-`Public`
  instance** — the box has always served one combined private page by
  design (see docs/statusbox.md, "The shape"), and port 80 above makes
  that a hard requirement rather than a preference: two private
  instances cannot both claim it on the same box. A caller with two
  today gets a refusal, not a box that silently forwards only one of
  them; run the extra private page on a second box.
- Bumping to this version replaces the box on next deploy, by design —
  see docs/statusbox.md, "Immutable, by construction": a `setup.sh`
  change is a `Version` change, and a `Version` change always replaces
  the instance rather than re-running the new script on the old one.

## v0.7.0

Two additions to `tenancy`, both opt-in on their OWN terms — a
`principals` entry that sets neither `audience` nor `routes` renders
byte-identically to 0.6.2, and an install that sets no `alertReaders`
gets no reader — but one consumer-visible change reaches every install
regardless, described in its own bullet below: a new default-deny
`NetworkPolicy` now selects vmalert's own pods whenever `vmalert.enabled`
and `networkPolicy.enabled` are both true (both defaults), whether or
not `alertReaders` is ever set.

- **New: `tenancy.alertReaders` / `tenancy.allowUnfilteredAlertReads`** —
  a bearer-token reader of vmalert's own `/api/v1/alerts`, the mechanism
  the status box's PULLED deadman and per-company signal are built on
  (docs/statusbox.md, "internal → status, pulled"): a caller outside the
  estate's own issuer reads the metrics alerter's active alerts —
  whether `Watchdog` is present, whether an alert with `customer_facing`
  and `company` labels is firing — with one bearer token, on one route
  this proxy did not forward anywhere before. Unscoped, the same way
  trace reads are: refused until `allowUnfilteredAlertReads: true` says
  every reader below sees every alert.
- **`networkPolicy` now also selects vmalert's own pods** — a new,
  default-deny `NetworkPolicy` renders whenever `vmalert.enabled` and
  `networkPolicy.enabled` are both true (both defaults), the same
  "pay nothing until you need it" choice the three store policies
  already made: admitting only the proxy (for `tenancy.alertReaders`)
  and the scrape peer (`networkPolicy.scrapeFrom`, for vmalert's own
  `/metrics` and its config-reloader sidecar's `reloader-http`, 8435) —
  closed now that something outside this namespace can read from it
  directly, and consumer-visible on upgrade whether or not
  `alertReaders` is ever set. The 8435 admission (**fix**, before this
  reached anyone): the first render of this policy admitted 8080 only,
  which drops every reloader-sidecar sample at the policy rather than
  the socket — the identical `up=0`/`TargetDown` shape the proxy's own
  policy was fixed for in 0.5.3, one object over. See docs/safety.md.
- **Fix: the `Watchdog` VMRule was trapped inside `alertmanager.enabled`**
  — an install with `notifications.mode: evaluate-only` (which refuses
  `alertmanager.enabled: true`) rendered no VMAlertmanager and, because
  the rule lived in that same template, no `Watchdog` alert either: the
  one thing an outside watcher waits for did not exist to wait for. The
  rule is now its own template (`templates/watchdog.yaml`), gated on the
  new `vmalert.watchdog.enabled` (default `true`) and nothing else — read
  PUSHED, through `alertmanager.watchdog`'s route, same as before, or
  PULLED, through `tenancy.alertReaders`, with no Alertmanager at all.
- **`pkg/statusbox`: `Args.Hostname`, required** — the box's own tailnet
  device name (`setup.sh` now runs `tailscale up --hostname=`), so an
  estate that wants a DNS record pointing at the box across a
  replacement (MagicDNS: `<Hostname>.<tailnet>.ts.net`) has a name it
  chose rather than whatever the provider's image happened to boot with.
  Every existing caller sets one: this is a required field, not a
  default that moved.
- **Fix: this chart's own `Watchdog` could duplicate the vendored one** —
  `victoria-metrics-k8s-stack` carries its own vendored default rule set
  (ON here, deliberately: it is where several rules with no
  `charts/platform-alerts` equivalent come from), applied directly to
  the cluster by a sync job — invisible to `helm template` — and its
  `general.rules` group is also where this install's `Watchdog` alert
  comes from by default. `templates/watchdog.yaml`, this chart's own
  Watchdog, now renders only when that vendored one is NOT presumed
  present (`defaultRules` turned off, or its `general.rules` group
  specifically) — never both. Turning both off at once is refused
  (`observability-stack.validate.watchdogSource`): the status box's
  deadman depends on exactly one existing to read, from either source.
  One thing this render-time check cannot confirm: the vendored rule
  set's own CONTENT is fetched over the network at apply time, so that
  its `general.rules` group carries `Watchdog` is this design leaning on
  an unchanged, years-old convention across this rule family rather than
  something checked against the fetched file itself — see docs/safety.md,
  "One Watchdog, from whichever source is not already there".
- **New per-principal `tenancy.principals[].audience` and
  `tenancy.principals[].routes` / `metricsQueryOnly`** — a machine reader
  in the shape a verification gate needs: its own token audience (the
  same escaping and anchoring `tenancy.audience` already gets, which
  stays required as the install-wide default), and a route restriction —
  `routes: [metrics]` with `metricsQueryOnly: true` renders exactly
  `/prometheus/api/v1/query` and `/prometheus/api/v1/query_range`, no
  series, labels, label values, tsdb status, vmui, logs or traces. Both
  fields are optional and additive: a `principals` entry that sets
  neither renders byte-identically to before this release.

## v0.6.2

- **Fix: `setup.sh` never made a private instance (`Public: false` —
  gatus-ops) reachable from the tailnet** — it joined the box to the
  tailnet and published every instance at `127.0.0.1:<Port>`, but nothing
  forwarded a tailnet peer's connection to that loopback port, so an
  estate wiring an internal Alertmanager's deadman push to gatus-ops's
  external-endpoint API (see docs/statusbox.md, "internal → status") had
  no path to it despite the box appearing joined and healthy. `setup.sh`
  now also runs `tailscale serve --tcp=<Port>` for every non-Public
  instance, forwarding the tailnet to its own loopback port; a Public
  instance is never registered this way — it stays reachable through
  cloudflared alone. Restricting who on the tailnet may dial a forwarded
  port is unchanged: it is the estate's own tailnet ACL to grant, not
  something this repository or this script decides.

## v0.6.1

- **Fix: `charts/observability-emitters`'s `kube-state-metrics`
  `ServiceMonitor` latches `OutOfSync` forever** — the five `replace`
  steps in the default `kube-state-metrics.prometheus.monitor.http.
  metricRelabelings` chain (the four `exported_<name>` restores plus the
  final `namespace`-to-`k8s_namespace_name` derivation) left `action`
  unset. That is also `replace`'s default, but only for whoever reads
  the YAML: the `monitoring.coreos.com` `ServiceMonitor` CRD's
  structural schema fills the field in on admission, so the object
  actually stored in the cluster always carries it, and a rendered
  manifest that omits it never matches that stored object byte-for-byte
  — the same class of latch as the earlier `record: ""` `VMRule` issue.
  `action: replace` is now written out on all five steps; no relabel
  behavior changes, and `metricRelabelings` overridden by a consumer is
  unaffected either way. A new test
  (`tests/relabel_action_defaults_test.go`) fails the build if any
  future `ServiceMonitor`/`PodMonitor` relabeling in any golden omits
  `action` again.

## v0.6.0

- **New value: `charts/observability-emitters`** — `kubeStateMetrics`,
  an optional fourth emitter wrapping upstream's own `kube-state-metrics`
  chart. **Off by default.** The gap it closes: nothing in this
  repository has ever collected `kube_*` metrics, so `platform-alerts`'
  `CronJobNotSucceeding` and `BackupJobFailed` rules, and
  `charts/observability-stack`'s own self-alert
  `*StoreSnapshotOlderThanWindow` (which reads
  `kube_cronjob_status_last_successful_time`), have always depended on
  an estate collecting it some other way — undocumented until now beyond
  a line in docs/adoption.md's "what must already exist" table.

  Lives in `charts/observability-emitters` rather than
  `charts/observability-stack`, because it needs cluster-wide read RBAC
  the same way the metrics agent's kubelet and cAdvisor scrapes already
  do, and because it has to exist on every cluster this chart is
  installed on — including one that runs no store of its own.

  A deliberately trimmed `kube-state-metrics.collectors` allow-list ships
  by default: eleven kinds (`cronjobs`, `jobs`, `pods`, `nodes`,
  `deployments`, `replicasets`, `statefulsets`, `daemonsets`,
  `namespaces`, `persistentvolumeclaims`, `horizontalpodautoscalers`)
  against upstream's own default of twenty-eight — every RBAC grant and
  every cardinality line item is one this repository, or a documented
  fleet rule, actually reads today; nothing else is collected by
  default. `metricLabelsAllowlist` and `metricAnnotationsAllowList` stay
  at upstream's own empty default.

  **The namespace stamp is the part worth reading before turning this
  on, and it is more involved than one relabel rule.** The VictoriaMetrics
  operator's own ServiceMonitor conversion stamps `namespace`, `pod`,
  `container` and `service` as TARGET labels — the kube-state-metrics
  pod's own identity, unconditionally, on every scrape — and this
  chart's `overrideHonorLabels: true` means a same-named label
  kube-state-metrics' OWN series carries (`kube_pod_container_status_
  restarts_total` carries all four; `kube_deployment_status_replicas`
  carries only `namespace`; `kube_node_status_condition` carries none)
  collides and survives only as `exported_<name>` — the target's value
  wins under the bare name. Left uncorrected, every `kube_*` series on
  the whole cluster would read as belonging to this release's own
  namespace (the kube-state-metrics pod's), not the namespace of the
  object it describes. This release ships an eight-step
  `metricRelabelings` chain on kube-state-metrics' `ServiceMonitor`:
  drop the four target-stamped names and `k8s_namespace_name`, restore
  each of the four from its `exported_` twin where the object had one,
  clean up the `exported_` twins, then derive `k8s_namespace_name` from
  the now-corrected `namespace` — and refuses to render if any step, or
  the ORDER between the steps that depend on one another, is wrong.
  Proved against a real relabel engine
  (`tests/kubestatemetrics_relabel_test.go`,
  `github.com/prometheus/prometheus/model/relabel`), not only read.
  See docs/safety.md, "The namespace stamp kube-state-metrics needs and
  no other scrape object does".

  A number of new refusals in all (docs/safety.md has the table):
  enabling this without the metrics agent; no `ServiceMonitor` rendered
  for it; a `namespaces` filter or a non-cluster Role, either of which
  makes it silently under-read; RBAC not created with no existing role
  named; more than one replica without upstream's own
  `autosharding.enabled`, which would double every series; a `[*]`
  label or annotation allow-list entry, the same cardinality trap this
  chart's node-label lesson already documents; and every step, and the
  relative order, of the namespace-stamp chain above.

## v0.5.3

- **Fix: `charts/observability-stack`** — the proxy's own `NetworkPolicy`
  now admits `networkPolicy.scrapeFrom` (the metrics agent, by default)
  on port 8435, the vm-operator's config-reloader sidecar for the VMAuth
  pod. That port was scraped and dropped at admission, not the socket:
  `up=0` for the job and `TargetDown`/`ServiceDown` firing permanently
  on a proxy that was otherwise perfectly healthy.

  **What starts working on upgrade, no action needed:** the reload
  endpoint's own scrape job reports `up=1`, and the `TargetDown`/
  `ServiceDown` pair that had been firing on it clears once its `for:`
  window passes.

## v0.5.2

Every `ServiceMonitor` this chart has ever rendered was inert, from the
first release: `disable_prometheus_converter: true` turned off the
VictoriaMetrics operator's conversion of Prometheus-Operator objects
entirely, so nothing ever turned this chart's own `ServiceMonitor`
objects into the native `VMServiceScrape` vmagent watches. `up=0` and
`vm_*` metric names stuck at the count from before any of it existed —
0.5.0 and 0.5.1 each believed docs/safety.md's own promise ("the
VictoriaMetrics operator converts the Prometheus kinds today") and
neither closed anything on a real install. See docs/safety.md, "The
doctrine's own promise was broken from this chart's first commit".

- **Fix: `charts/observability-stack`** —
  `victoria-metrics-k8s-stack.victoria-metrics-operator.operator.
  disable_prometheus_converter` is now `false`, matching the vendored
  chart's own default, and four `env` entries on the operator restate
  `VM_ENABLEDPROMETHEUSCONVERTER_PROBE`, `_SCRAPECONFIG`,
  `_PROMETHEUSRULE` and `_ALERTMANAGERCONFIG` back to `false` explicitly
  — this stack renders none of those four kinds, and converting one that
  belongs to an unrelated chart or team, on an estate that runs its own
  Prometheus Operator or a second VictoriaMetrics operator instance, is
  the two-controllers-fighting-over-one-scrape failure the original
  switch existed to prevent. Only `VM_ENABLEDPROMETHEUSCONVERTER_
  SERVICESCRAPE` and `_PODMONITOR` are left at the operator's own
  default of enabled.

  **What starts being scraped on upgrade, with no values change
  required:** the metrics store's own selfscrape ServiceMonitor
  (`metricsSelfScrape`, 0.5.1), and the log and trace stores' own
  `serviceMonitor` (0.5.0) — all three converted to a `VMServiceScrape`
  for the first time, so `vm_*`, `vl_*` and `vt_*` metric names start
  appearing in the metrics store where none of the three existed before,
  and every `selfAlerts` rule that names one of those metrics (still
  `""` by default, docs/notifications.md) has a real series to
  evaluate against once a name is confirmed and set.
  `charts/observability-emitters`' `PodMonitor` objects (vmagent, vlagent,
  the OpenTelemetry gateway) start being converted too, and scraped, the
  same way — that chart's own release notes have not needed a change,
  because the fix lives entirely in the operator this chart installs.

  A new refusal (`templates/_validate.tpl`) catches the same regression
  in either form: `disable_prometheus_converter` flipped back to `true`,
  or `VM_ENABLEDPROMETHEUSCONVERTER_SERVICESCRAPE` restated to `"false"`
  in the operator's `env`, while this chart still renders a
  `ServiceMonitor` of its own.

## v0.5.1

A follow-up to 0.5.0's own NetworkPolicy fix: opening the metrics agent's
path to the metrics store's port was necessary and not sufficient, and a
live install after upgrading to 0.5.0 found the rest of it.

- **Fix: `charts/observability-stack`** — the metrics store's own
  `/metrics` answered every scrape with 401. `networkPolicy.scrapeFrom`'s
  new default (0.5.0) let the metrics agent reach the store's port for
  the first time, and once it could, the scrape it found there was the
  operator's own — auto-created for every `VMSingle`, on by default, and
  carrying no `basicAuth` at all, while the store itself runs with
  `-httpAuth.*` from `storeCredentials`. `up=0` for that job, and the
  store's own `vm_*` series never landed, the same shape 0.5.0 already
  closed for the log and trace stores.

  `victoria-metrics-k8s-stack.vmsingle.spec.disableSelfServiceScrape` is
  now `true`, turning the operator's version off — it is always a
  `VMServiceScrape`, the one kind docs/safety.md rules out regardless of
  what credential is added to it — and a new `ServiceMonitor`
  (`templates/selfscrape.yaml`, gated on the new `metricsSelfScrape.
  enabled`, default `true`) reads `storeCredentials` directly in its
  place. Nothing for a consumer to change: both default on, and the
  render already carries the same credential the store itself demands.
  A new refusal keeps `disableSelfServiceScrape` from being flipped back
  without the new `ServiceMonitor` also being turned off, since the
  operator's object recreates the same 401 alongside it either way.

- **Fix: docs/safety.md** — the `observability-stack` refusal-count
  sentence said "Forty-six" against a table that already had
  forty-nine rows before this release, and one of those forty-nine — "A
  `-retention.max*` flag on the metrics store" — had never had a fixture
  under `tests/invalid/observability-stack/` proving it actually fires.
  Both are fixed: the fixture now exists, and the sentence says "Fifty",
  counting the new metrics-store refusal above alongside it.

- **CI**: `dashboard-lint` was in the local `just check` but missing from
  `.github/workflows/ci.yaml`'s recipe list, so a dashboard that failed
  the six-rule contract in docs/dashboards.md passed review as long as
  nobody ran `just check` themselves. It is a required CI job now, the
  same as every other `check` recipe.

## v0.5.0

The nineteen store self-alerts, and the NetworkPolicy gap that
had kept every store from ever being scraped in the first place — once a
live install was checked against both, mostly a value list waiting for
names, and one more peer a policy had never admitted.

- **New value: `charts/observability-stack`** — `networkPolicy.scrapeFrom`,
  beside the existing `proxyFrom` and `writersFrom`. Every store's own
  NetworkPolicy admitted the proxy, vmalert and the store's own pods, and
  nothing else — so the metrics agent `charts/observability-emitters`
  renders matched none of the three peers, and no store in this stack was
  ever scraped by anything but itself indirectly through the peers above.
  Measured on a live install: `up=0` for every store's own scrape job, no
  `scrape_samples_scraped` series for it, and every rule naming a store's
  own counter — including this chart's own `selfAlerts` — evaluating
  against a sample that never arrived. See docs/safety.md, "A store whose
  own policy hides it from the scraper".

  Empty (the default) now admits `app.kubernetes.io/name: vmagent` in the
  release's own namespace — the selector the sibling chart's agent
  carries — so a bare `helm install` self-monitors without a values
  change. A non-empty list REPLACES that default rather than adding to
  it, the same trap `proxyFrom` already carries: an estate renaming the
  agent or running it in another namespace has to re-state the default
  peer alongside whatever it adds, or the store goes dark again.

  A new refusal closes the value against a peer that would fail the same
  way silently: `scrapeFrom[]` naming an `ipBlock` with no `podSelector`
  or `namespaceSelector` is refused, because a pod IP is reassigned on
  every reschedule and the rule would work today and stop, unannounced,
  on the next one.

- **New value: `charts/observability-stack`** — `selfAlerts`, one more
  `VMRule` alongside this chart's other own objects, watching the stack's
  own components rather than a consumer's telemetry:
  `MetricStoreIgnoringRows`, `MetricStoreCardinalityNearLimit`,
  `MetricStoreDailyCardinalityNearLimit`, `LogStoreDroppingRows`,
  `TraceStoreDroppingRows`, `LogStoreStreamsChurning`,
  `TraceStoreStreamsChurning`, `WriterBufferGrowing`,
  `WriterDroppingPackets`, `GatewayQueueFilling`, `GatewayExportFailing`,
  `GatewayEnqueueFailing`, `ProxyAtConcurrencyLimit`,
  `MetricStoreDiskNearGuard`, `LogStoreDiskNearGuard`,
  `TraceStoreDiskNearGuard`, `MetricStoreSnapshotOlderThanWindow`,
  `LogStoreSnapshotOlderThanWindow`, `TraceStoreSnapshotOlderThanWindow`
  — see docs/notifications.md, "Store self-alerts", for what each one
  catches. Every rule that watches one specific store is named for that
  store — three stores exist, and a rule named just "Store..." does not
  say which one paged you; a rule stays generic
  (`WriterBufferGrowing`, `WriterDroppingPackets`,
  `ProxyAtConcurrencyLimit`) only where it genuinely is not about any one
  store.

  - **A separate value in the same release, unrelated to the rules
    above: `notifications.mode`**, `route` (default, today's behaviour)
    or `evaluate-only`. An install with no Slack or webhook credential
    yet used to have exactly one accepted shape — `alertmanager.enabled:
    false` **and** `vmalert.enabled: false`, so no rule was even
    evaluated. `evaluate-only` keeps vmalert evaluating every rule —
    visible in its own UI, its API, and (since vmalert already carries
    `-remoteWrite.url` unconditionally) the `ALERTS` / `ALERTS_FOR_STATE`
    series in the metrics store — and renders `-notifier.blackhole`
    instead of a notifier URL, the flag vmalert has carried since
    v1.93.0 for evaluating rules "without sending any notifications to
    external receivers". Alertmanager is not rendered in this mode, and
    is refused if `alertmanager.enabled` is left at its default or set
    explicitly, the same as a receiver, a route, a severity or
    `alertmanager.notifierUrl` configured beside it — each looks wired
    up and is never reached, because nothing this mode renders notifies
    anybody. See docs/notifications.md, "Evaluate, notify nobody yet".

  Two rules close incidents from this week: `MetricStoreIgnoringRows`
  watches for the counter that sat at 9,748,387 while a collector reported
  888k rows written with zero errors and the store held none of them;
  `GatewayQueueFilling` / `GatewayExportFailing` / `GatewayEnqueueFailing`
  watch the OpenTelemetry gateway's queue and its send and enqueue
  failures — two separate rules, because a destination refusing a batch
  already accepted and the gateway's own queue refusing at the door are
  different losses — after a network policy change cut the gateway off
  from its own proxy and "sending queue is full" ran for hours with every
  pod Running and nothing reporting it.

  **Every metric name in `selfAlerts` is a value with NO default**, the
  same shape `charts/platform-alerts` already uses for its own `stores`
  list. Measured against a live install: ten of the eleven counters this
  design originally named were simply absent from this chart's own
  metrics store — the log store, the trace store, and the sibling chart's
  OpenTelemetry gateway had NOTHING scraping them into it at all. Shipping
  the names as written would have produced exactly what this repository
  refuses: rules that render cleanly, look like coverage, and never fire.
  `selfAlerts.enabled` therefore **defaults to `false`**; each rule renders
  independently once its metric name is confirmed against your own
  component's `/metrics` and set, and the render refuses `enabled: true`
  with nothing that would actually render at all. See docs/safety.md,
  "The self-alerts: nineteen rules, and what a live install did to eleven
  of the original twelve", for the measurement, the two exceptions
  (`kube_cronjob_status_last_successful_time` for the `*SnapshotOlderThanWindow`
  rules, a standard field this session did not doubt;
  `vmauth_concurrent_requests_limit_reached_total`, confirmed present but
  still not written in), and a doctrine refinement on which store
  read-only flags are observable in principle.

- **`just lint`** — the negative-fixture check now requires the RIGHT
  refusal, not just a non-zero exit. 12 of the 38 fixtures under
  `tests/invalid/observability-stack/` were passing for the wrong
  reason: an early, unconditional refusal (`alertmanager.enabled` true
  with no `notifications` configured) fired first on any fixture that
  had not configured one, whatever that fixture actually meant to test.
  Every fixture under `tests/invalid/<chart>/` now starts with a `#
  expect: <substring>` line, and `hack/lint-fixtures.sh` fails the
  recipe if that substring is missing from the fixture's output — or if
  the line itself is missing. See `docs/safety.md`, *The negative-fixture
  suite counted refusals it was not guarding*.

  **Nothing to do on upgrade.** This changes the test suite only; no
  chart output moves.

- **New value: `victoria-logs-single.server.serviceMonitor.enabled` /
  `victoria-traces-single.server.serviceMonitor.enabled`**, both now
  `true`. The two components this chart itself renders that the
  measurement above found with zero scrape coverage; this closes that for
  them, using each upstream chart's own existing toggle rather than a new
  template. `basicAuth` MIRRORs `storeCredentials`, refused if it
  disagrees. Does **not** close the gap for `charts/observability-emitters`'
  vmagent, vlagent or OpenTelemetry gateway — that chart's own scrape
  coverage is its own decision.

- **New chart `charts/observability-dashboards`** — the generic
  dashboards, shipped as their own artifact into Grafana's namespace
  rather than left to the sidecar's own cluster, per docs/dashboards.md.
  One ConfigMap per dashboard, labelled `grafana_dashboard: "1"` for the
  sidecar and annotated `k8s-sidecar-target-directory` for its folder.

  The generic set is fetched from the same upstream URLs the store
  chart's own `defaultDashboards.sources` names — VictoriaMetrics
  (single-node, vmagent, vmalert, operator), VictoriaLogs (single-node,
  vlagent), VictoriaTraces (single-node), Alertmanager, node-exporter-full
  and kubelet — pinned by release and committed under
  `charts/observability-dashboards/dashboards/`, never resolved at render
  time. `just dashboards` re-fetches; `hack/dashboards.sh` and
  `hack/dashboards/sources.yaml` say where from and why pinned there.

  Every one of the ten is rewritten to the same contract: a `datasource`
  variable every panel uses instead of a literal UID, a `cluster`
  variable chained off it and populated by a label-values query, `$cluster`
  in the title, and — where a dashboard is namespace-scoped, which only
  Alertmanager's is — a `namespace` variable chained off `cluster`.

  New values: `datasources.{metrics,logs,traces}` (the provisioned
  Grafana datasource UIDs a dashboard is pointed at; only `metrics` is
  used by the shipped set), `folders.{infrastructure,stores}` (the two
  folders the shipped set files into), `dashboards.<name>.enabled` (one
  key per shipped dashboard), and `extraDashboards` (the estate's own,
  held to the same contract, filed into their own named folder).

- **New six-rule dashboard lint**, `pkg/dashboardlint` and the
  `dashboardlint` binary it ships as `cmd/dashboardlint`, run with `just
  dashboard-lint` — on this chart's own set by default, or against any
  dashboard JSON an estate names. The rule that matters most: a panel
  pinned to one datasource UID is how a fleet dashboard silently becomes
  a one-install dashboard, and the lint fails on any panel whose
  datasource is a literal. The other five: a `cluster` variable used in
  every query, a `namespace` variable chained off it where the dashboard
  is namespace-scoped, `$cluster` in the title, the environment tier
  never a selector, and upstream's own `namespace` label accepted beside
  `k8s_namespace_name`. `just check` now runs it in CI on the shipped
  set; every rule has its own fixture under
  `tests/dashboardlint/invalid/`.

## v0.4.1

One background task, off, and the empty panel it explains.

- **New value: `charts/observability-stack`** —
  `victoria-traces-single.server.extraArgs['servicegraph.enableTask']`,
  default `"false"`. It turns on the Jaeger dependency graph (Grafana's
  service map, over `api/dependencies`), which the trace store computes
  with a background task that upstream ships disabled — this chart now
  writes that default out explicitly instead of leaving it to upstream's
  own.

  Off looks like a bug and always has: the endpoint answers `200` with
  `{"data":[],"total":0}` rather than an error, so a consumer sees an
  empty panel with nothing naming the cause, even on an install where
  the trace reads and context propagation both work. That is the whole
  reason the flag is now spelled out where somebody tuning the store can
  find it, alongside its companions — `taskInterval`, `taskLookbehind`,
  `taskLimit`, `taskTimeout`, `databaseTaskLimit` — rather than left to
  a background task nobody knew existed.

  It is upstream-experimental and only supported on a single-node or
  vtstorage deployment, which is this chart's shape. Whether the graph
  it computes is written back into the store, and so costs retention
  headroom and write throughput, is inferred from the endpoint's
  existence, not measured.

  **Nothing to do on upgrade.** The default is upstream's own behaviour,
  now written down.

- **`docs/safety.md`** — *The trace store speaks two dialects, and
  neither one completely* corrected: an empty service map was said to be
  "an empty graph, not a broken route", which is true but was
  incomplete. By default nothing computes the graph at all, which is why
  it is empty.

- **`docs/reference.md`** gains the value.

## v0.4.0

The one router, and the retirement of the shape it replaces.

- **New value: `charts/observability-stack`** — `notifications`, the
  routing tree docs/notifications.md describes: receiver kinds (Slack,
  named webhooks), severity defaults, a project route list matched on
  cluster and namespace, and a bridge to a webhook beside the normal
  route. The chart renders group_by, the group/repeat intervals, an
  inhibit rule (a `critical` silences the matching `warning`), and the
  Slack message template — cluster and namespace in the title, the
  alert's summary, a runbook link, a Grafana link built from the alert's
  labels, and a silence link. Every receiver's secret is mounted and
  read with `*_url_file`, the same mechanism the deadman's webhook
  already used; none is ever interpolated into the rendered config.

  **`alertmanager.config` is gone.** It was a free-form object in
  Alertmanager's own syntax, and the chart could not tell an empty one
  from a working one — which is exactly how every install used to end
  up routing to a receiver named `blackhole`: every rule evaluated,
  Alertmanager accepted every alert, and the result reached nobody,
  with nothing anywhere reporting it. `alertmanager.enabled` now refuses
  to render until `notifications` configures at least one receiver kind
  and a default for both `critical` and `warning` — see docs/safety.md
  for the rest of what it refuses and why each one is silent otherwise.

  An existing `alertmanager.config` block does not carry over: replace
  it with `notifications` before upgrading, or the render will refuse.

  `notifications.externalUrl` defaults to `vmalert.externalUrl` — they
  are the same fact, the base URL a link leaving the cluster should
  point at, and the chart refuses if both are set and disagree. Set
  `vmalert.externalUrl` and the Slack template's Grafana link works too,
  with nothing else to set; `notifications.externalUrl` stays a
  separate value because `vmalert.externalUrl` still has to work on its
  own for an install with `alertmanager.enabled: false`, which renders
  no `notifications` block at all.

- **Two defaults that made an alerting path lie about itself, fixed
  everywhere they apply, not values:**

  - Every vmalert this chart renders now carries `-remoteWrite.url`
    **and** `-remoteRead.url` against the metrics store — it already
    wrote that state, but never read it back. vmalert keeps every
    `for:` timer's state there, and without the read half a restart
    resets every pending timer to zero: a rule with `for: 30m` that was
    25 minutes into firing has to start over, and in a cluster that
    rolls its pods more often than that it can never fire at all.
  - The logs alerter's `-rule.evalDelay` drops from vmalert's own 30s
    default to 5s. That default exists to match VictoriaMetrics'
    `-search.latencyOffset`, which withholds a metrics query's newest
    samples because they may still be incomplete; VictoriaLogs makes no
    such promise and needs no such offset, so inheriting it held every
    log-based alert back by half a minute for a latency the log store
    does not have.

- **`docs/reference.md`** and **`docs/safety.md`** gain the value list
  and the refusal table for `notifications`. docs/notifications.md is
  the design this release implements; its "Store self-alerts" section
  ships separately.

- **New chart `charts/alert-ingress`, and a new image, `cmd/alert-ingress`**
  — turns a cloud provider's own notification topic (a threat-detection
  finding, a root sign-in, a signing operation on a key that should
  never sign, a budget crossing its line) into an alert on the same
  Alertmanager `notifications` now routes everything else through,
  rather than a second router with its own silences to keep. Every
  message is signature-verified against a certificate fetched only from
  the provider's own signing domain, pinned by pattern in the binary; a
  subscription is confirmed only for an allow-listed topic; a message no
  mapping rule matches is never dropped — it becomes `CloudEventUnmapped`
  rather than vanishing. The chart renders its own deadman `VMRule`,
  because a notification service retries and then gives up quietly and
  nothing else would say so.

  **`image.repository` has no default and is a required value.** This
  repository has never built or published an image for a Go binary —
  every release to date has been chart-only, via goreleaser's
  `builds-skip` — and this one does not change that: no image-build
  workflow was invented for it. Point it at wherever your estate builds
  and pushes `cmd/alert-ingress` from this tag.

  **`networkPolicy.allowCloudHTTPS`** (default `true`) is a decision for
  the installing estate, not a footnote. Verifying a signature and
  confirming a subscription both need HTTPS egress to the cloud
  provider, and vanilla Kubernetes NetworkPolicy has no way to pin
  egress to a hostname — only to a peer selector or a CIDR block. The
  rendered policy says so rather than pretending otherwise: Alertmanager
  (peer-scoped, required), cluster DNS, and HTTPS to anywhere. Turn it
  off only if your cluster's CNI enforces FQDN-scoped egress and the
  real signing domain is pinned there instead.

- **New package: `pkg/statusbox`, and its first provider,
  `pkg/statusbox/lightsail`** — the watcher outside: a small virtual
  machine, provisioned by a Pulumi call, running several Gatus instances
  behind a tunnel and a private network with no inbound port open. It is
  what receives the deadman (the alert that fires when this chart's own
  Alertmanager has stopped) and what carries a public status page,
  because both have to live somewhere the estate's own failure cannot
  reach. docs/statusbox.md is the design; docs/target-state.md has the
  smallest worked example.

  `setup.sh` — the script the box actually runs — is a release asset of
  this repository, not something a consumer writes or copies. A box's
  Pulumi call pins only a `Version`; at deploy time it fetches that
  release's `checksums.txt` and bakes `setup.sh`'s sha256 into the box's
  own boot script, which refuses to run a `setup.sh` whose checksum does
  not match. Nobody hand-copies a hash, and nothing about the script is
  a moving target.

  **The box is immutable.** The provider applies the rendered boot
  script once, at creation, so changing an instance's Gatus
  configuration — or bumping `Version` — **replaces the box**: about two
  minutes of status-page blip while its data disk reattaches to the new
  instance, with the SQLite history on it intact. There is no
  in-place config update to ask for.

  Two operational facts worth knowing before they are a surprise rather
  than a line in this entry: the rendered boot script is capped at 16 KB
  (Lightsail's own user-data limit) and `pkg/statusbox` refuses to
  render past it rather than produce a box that silently fails to boot;
  and that boot script — tailnet key, tunnel token, alert-push URLs and
  all — is readable in plain text from the instance metadata service by
  any process running on the box. Accepted rather than worked around,
  because the box is single-purpose, the tailnet key is spent at first
  boot, and an alert URL is rotated the day the box is ever asked to be
  anything else.

## v0.3.10

Documentation and a probe, no render change.

- **`hack/trace-api.sh`** — probes a live trace store for the Jaeger and
  Tempo endpoints a Grafana datasource calls, and prints which of them it
  implements. The store answers **both** dialects on `/select/jaeger/*`
  and `/select/tempo/*` and implements neither completely; an endpoint it
  does not implement returns 400 `unsupported path requested`, which a
  datasource reports as a failed query rather than as a missing feature.

- **`docs/safety.md`** gains *The trace store speaks two dialects, and
  neither one completely*, with the measured table — including **why the
  chart's default trace datasource is `type: jaeger`**: Grafana's Jaeger
  datasource asks for operations by the nested path
  `api/services/{service}/operations`, which the store has, while
  Jaeger's own UI moved to the flat `api/operations?service=`, which it
  does not. A Tempo datasource is the shape to avoid, because
  `api/status/buildinfo` is how it decides what the backend supports.

  It also records the two 400s that look like outages and are not: an
  Explore search with no service selected is refused correctly by the
  store, and `api/dependencies` answers `{"data":[]}` rather than
  refusing, so an empty service map is an empty graph and not a broken
  route.

- **`docs/reference.md`** and the datasource comment in `values.yaml` say
  the same thing where somebody changing the type will read it.

## v0.3.9

One metrics endpoint becomes an estate's decision instead of an
unexplained 401.

- **New value: `charts/observability-stack`** —
  `tenancy.allowUnfilteredMetricMetadata`, default `false`, admits
  `/api/v1/metadata` on the metrics read route.

  The endpoint returns every metric **name** in the store with its type
  and help string, and no filter reaches it: measured against the store,
  an `extra_filters` naming a namespace that matches nothing returns the
  same body as no filter at all. Where every grant is `allNamespaces` it
  discloses nothing a principal could not already query; where grants are
  per-namespace it is an inventory of what another tenant runs. That is a
  judgement about an install, so it is a value.

  Leaving it off has always been the behaviour — the omission was
  deliberate and reasoned in `pkg/tenancy`. What was missing is that the
  cost of off is **visible and looked like a fault**: Grafana's
  Prometheus-family datasources ask for this endpoint to put descriptions
  on metric names, and write

      path=…/resources/api/v1/metadata status=401

  into their own logs. Nothing is broken — metric names come from
  `/label/__name__/values`, which IS filtered, so only descriptions are
  missing — but an operator reading that 401 should find a value rather
  than a mystery.

  **Nothing to do on upgrade.** The default is the old behaviour.

- **Unchanged, and now tested as such** — `/api/v1/status/active_queries`,
  `/api/v1/status/top_queries` and `/api/v1/status/metric_names_stats` are
  admitted by no value. Two return other principals' query *text* and one
  returns names with per-tenant counts, so unlike the metadata endpoint
  there is no install for which routing them is correct. A wildcard on
  this route once admitted all four together; `tests/metadata_test.go` and
  `TestMetricMetadataOptIn` now fail if any of them reappears, and both
  were shown failing on the shapes they exist to catch.

- **`pkg/tenancy`** gains `Config.AllowUnfilteredMetricMetadata` and the
  exported `MetricMetadataPath`, so the library and the chart still render
  the same routes.

- **Docs** — `docs/safety.md` gains *Four metrics endpoints, one of them a
  decision*; `docs/reference.md` gains the value.

## v0.3.8

Grafana here could be configured into two shapes that look right and are
not: more replicas than its database can serve, and a store nobody can
query.

- **New refusal: `charts/observability-stack`** — `grafana.replicas`
  above one is refused unless `grafana.ini`'s `[database] type` names
  something shared.

  Grafana's default is SQLite, a file on the pod. Two replicas on it are
  either two separate databases — a dashboard saved on one is missing
  from the other — or one ReadWriteOnce volume with two processes writing
  it, which answers `500 database is locked` on whichever request loses
  while the rest of the UI keeps working. The pods are Running and Ready
  throughout.

  The replica count and the database are one decision, and they were two
  values in different parts of the file set by different people at
  different times.

  **On upgrade**: an install running more than one replica on SQLite now
  fails to render. It was already losing data. Point `[database]` at
  Postgres or MySQL — the password belongs in
  `envValueFrom.GF_DATABASE_PASSWORD`, since `grafana.ini` renders into a
  ConfigMap — or drop to one replica.

- **New refusal: `charts/observability-stack`** — an enabled store that
  no Grafana datasource type reads is refused, when Grafana is enabled
  here.

  A store with no datasource ingests, retains and answers exactly as if
  it were being read. Nothing is unhealthy and no metric moves the wrong
  way; the only symptom is that nobody ever looks at it.

- **Fix: `charts/observability-stack`** — the default datasource list
  gains **VictoriaTraces**. It had the fault above: the trace store is
  enabled by default and only metrics and logs were readable.

  It is `type: jaeger` — the store serves the Jaeger select API and
  Grafana ships that datasource in core, so there is no plugin to
  install. Note that the three stores take three unlike URL shapes; see
  the table in `docs/safety.md`.

- **Docs** — `docs/safety.md` gains *Grafana's replica count and its
  database are one decision* and *A store nobody can query*;
  `docs/adoption.md` gains the database prerequisite; `docs/reference.md`
  gains the three values.

## v0.3.7

Every span a writer sent went to the **log** store and was rejected. The
trace store had never held anything.

- **Fix: `charts/observability-stack`** — the log store's write routes are
  now enumerated instead of `/insert/.*`.

  vmauth matches a VMUser's `src_paths` in the order its `targetRefs`
  render and stops at the first hit, and a writer's routes render metrics,
  logs, traces. `/insert/.*` matched `/insert/opentelemetry/v1/traces`
  before the trace store's own route was reached, so spans were posted to
  the log store, which answered:

  *Permanent error: rpc error: code = InvalidArgument desc = error
  exporting items, request to …/insert/opentelemetry/v1/traces responded
  with HTTP Status Code 400*

  The collector treats that as permanent, drops the batch and moves on.
  Nothing was unhealthy, every query answered, and the trace store stayed
  empty — which is indistinguishable from an estate that emits no spans.

  The catch-all also routed `/insert/multitenant/*`, the endpoint a writer
  uses to NAME the tenant it writes to. Deciding that is what this proxy is
  for, so the route around it is gone with it.

  **Nothing to do on upgrade**, unless a writer here ingests logs through
  an endpoint outside the store's own list — the routes are now
  `/insert/{datadog/api/v2/logs,elasticsearch/_bulk,journald/upload,jsonline,loki/api/v1/push,native,opentelemetry/v1/logs,splunk}`.
  Add an endpoint deliberately rather than widening one back to a pattern.

- **New refusal: `charts/observability-stack`** — the render now refuses
  any route declared before another store's that also matches it, naming
  both. `tests/routing_test.go` holds the same property against the routes
  as they are ORDERED in a rendered VMUser, which is what vmauth actually
  reads.

  How it was found: by sending one span and then asking the *store*
  whether it had arrived. The sender's 200 said nothing — it only means
  the collector accepted the batch for its queue.
## v0.3.6

The gateway's release never converged. `charts/observability-emitters`
rendered its `volumeClaimTemplates` entry without `apiVersion` or `kind`,
the API server defaults both in, and continuous delivery then compared
what it rendered against what the cluster holds and reported the release
**OutOfSync for ever** — with nothing to converge on, because each sync
writes the same manifest and the server adds them back.

- **Fix: `charts/observability-emitters`** — the queue's
  `volumeClaimTemplate` spells out `apiVersion: v1` and
  `kind: PersistentVolumeClaim`.

  Declaring the defaults is the fix rather than teaching a differ to
  ignore those fields: an ignore rule would hide a real change in the same
  field later, and this costs nothing. Both upstream stores already did
  it, which is how the shape was recognised.

  **Nothing to do on upgrade.** The rendered object is the same one the
  cluster already holds; the release simply stops reporting a difference.

- **Check: `tests/volumes_test.go`** — every `volumeClaimTemplate` in
  every golden must declare both. Stated about the shape rather than this
  chart, because every claimed volume has the same trap.

## v0.3.5

**Every kubelet and cadvisor series was being discarded by the store**, and
every counter on the writing side said success. Measured on a live
cluster, because nothing else can see this.

- **Fix: `charts/observability-emitters`** — the node scrapes no longer
  `labelmap` every node label onto every series.

  That snippet is conventional and it is unbounded by construction: the
  labels belong to the cloud provider, not to this chart. On EKS a node
  carries around forty (`eks_amazonaws_com_instance_*`, karpenter,
  topology), so each kubelet and cadvisor series arrived with **46 to 52
  labels** — past VictoriaMetrics' `-maxLabelsPerTimeseries=40`.

  What the store does then is the part worth knowing: it **ignores the
  series and answers 200**. The agent reported 888k rows written, zero
  errors, zero dropped. The store held none of them. The only record
  anywhere was a warning in the store's own log.

  So node identity is now one label, `node`, from the node's name — the
  conventional name, and the one dashboards and recording rules join on.

- **New: `metrics.scrape.nodeLabels`** — node labels to copy, **by name**,
  empty by default. Breadth is asked for where somebody can count it,
  since every entry lands on every node series.

  **On upgrade:** if your dashboards join on a node label other than
  `node`, name it here. If they never worked, this is why.

- **Check: `tests/cardinality_test.go`** — no scrape config this chart
  renders may copy labels it has not named.

## v0.3.4

The OTLP gateway could not start, and could not be placed. Both were found
by installing the chart on a real cluster for the first time, which is
where this pair of defects had to be found: neither is visible to a render,
a lint, a golden, an API server or the operator.

- **Fix: `charts/observability-emitters`** — the gateway pod now sets an
  `fsGroup`, so the queue volume it declares is one it can write to.
  Before, it exited at startup on **any** cluster with a default
  StorageClass:

  *failed to build extensions: failed to create extension "file_storage":
  mkdir /var/lib/otelcol/queue: permission denied*

  A dynamically provisioned volume arrives owned by root and the collector
  image does not run as root; `fsGroup` is the only thing that bridges the
  two. It is a default rather than a value to discover, because a
  StatefulSet that declares a volume it cannot write to is not a
  configuration choice. Replace `otlp.podSecurityContext` wholesale if
  your policy differs.

  Nothing upstream of the cluster could see it: the template renders, the
  chart lints, the golden is ordinary, the API server accepts the object
  and the operator has no opinion. The only thing that disagreed was the
  container, after the volume was attached.

- **Check: `tests/volumes_test.go`** — every StatefulSet in every golden
  that claims storage must say who may write to it. Stated about the shape
  rather than this chart: a pod that asks for storage intends to write to
  it. The upstream stores already passed; ours was the only one that did
  not.

  **Nothing to do on upgrade.**

- **`charts/observability-emitters`** — new `otlp.nodeSelector` and
  `otlp.tolerations`, both empty by default, so nothing changes for an
  install that does not set them.

  This is the third of three rather than a new idea: the other two
  emitters already had a way to say where they run, and the gap was only
  visible on a cluster that is fully tainted, where the schema correctly
  refused the values an operator would reach for first.

  Worth stating because it is not symmetric: the gateway is **not** given
  the log agent's blanket `operator: Exists`. A log agent is a node agent
  and has to run everywhere or the logs it skipped are missing — and
  missing logs look exactly like quiet ones. The gateway is one replica
  per queue volume and should be placed deliberately, on a pool that is
  not reclaimed underneath it.

  **Nothing to do on upgrade.**

## v0.3.3

Both alerters loaded every rule in the cluster. `charts/observability-stack`
runs two vmalerts that speak different query languages, and gave each of
them `selectAllByDefault: true` with no selector.

- **Fix: `charts/observability-stack`** — each alerter now selects its
  rules by label. Before, the logs alerter (which runs with
  `-rule.defaultRuleType=vlogs`) was handed the metrics subchart's PromQL
  and **crash-looped**, because vmalert parses every rule at startup and
  exits on the first one it cannot parse:

  *cannot parse configuration file: errors(23): invalid expression for rule
  "TargetDown": bad LogsQL expr … probably, the whole string must be put
  into quotes*

  So every log rule stopped being evaluated too, and the reverse pairing —
  LogsQL reaching the metrics alerter — is the same failure the other way
  round.

  **The label is now load-bearing.** A rule written in LogsQL must carry
  `observability.rule-type: vlogs` to be evaluated; in `platform-alerts`
  that is the `ruleLabels` value. A PromQL rule needs no label, because the
  metrics alerter selects everything *not* marked as LogsQL — which is what
  keeps the rules other charts ship working untouched.

  **Nothing to do on upgrade unless you already ship LogsQL rules.** If you
  do, label them: until you do they are selected by nobody, and a rule
  nobody selects is a file on the cluster rather than an alert.

  Why it was invisible: an install with no rules yet is perfectly healthy.
  It fires the moment the first rules exist — for the metrics subchart,
  when its own sync job runs.

- **Check: `tests/selection_test.go`** — the property is about the pair, so
  the test is too. No rule shape may be selected by two alerters at once,
  and none may be selected by none of them: a selector pair that overlaps
  nowhere is otherwise satisfied perfectly by two selectors that match
  nothing, which is an install where no rule is ever evaluated and every
  pod is green.

## v0.3.2

The metrics alerter was refused by the API server. `charts/observability-stack`
wrote `extraArgs` on its VMAlert unconditionally while everything under it
was conditional, so the alerter that had no flags to set rendered the key
with an empty body — and an empty body is null, where the definition types
the field as an object.

- **Fix: `charts/observability-stack`** — the VMAlert renders `extraArgs`
  only when it has something to put there. Before, the metrics alerter
  rendered a bare `extraArgs:` and the API server refused the **whole
  object**:

  *VMAlert.operator.victoriametrics.com "…-metrics" is invalid:
  [spec.extraArgs: Invalid value: "null": spec.extraArgs in body must be of
  type object]*

  The logs alerter hid it, because its one flag (`rule.defaultRuleType`) is
  unconditional and so its body was never empty.

  What this costs is not the field. The resource never exists, so **the
  metrics alerter does not run and no metrics rule is evaluated** — while
  every other object in the release applies and reports healthy. In
  continuous delivery the whole sync is marked failed on that one resource,
  and on a self-healing install the same revision is not retried, so the
  next unrelated change to that cluster waits behind it.

  **Nothing to do on upgrade.** No value changes. An install that carries
  `vmalert.externalUrl` renders the same flag as before, now serialized by
  `toYaml` and therefore unquoted — the same string either way.

- **Check: `tests/typing_test.go`** — every custom resource these charts
  render is now walked against the definition this repository installs for
  its kind, and a value whose type the definition contradicts fails the
  test. This is the sibling of the pruning check: that one catches a field
  the API server silently *drops*, this one a value it *refuses*. The
  reasoning that left types out was that a refusal is loud — it is, but it
  is loud at apply, which is not a place anyone is watching. Fixtures under
  `tests/rejected/` prove it can fail.

## v0.3.1

The proxy had no Deployment. `charts/observability-stack` rendered
`unauthorizedUserAccessSpec: {disabled: true}` on its VMAuth, and that
field does not exist — no release of the VictoriaMetrics operator has ever
had it, so the CustomResourceDefinition this repository ships has no room
for it.

- **Fix: `charts/observability-stack`** — the VMAuth no longer renders
  `unauthorizedUserAccessSpec`. The API server pruned the unknown key and
  stored an unauthorized section that routes nowhere, which the operator
  then refused — *cannot build unauthorized_user config section: at least
  one of `url_map`, `url_prefix` or `targetRefs` must be defined* — so the
  VMAuth reported `failed`, no Deployment was created, and **there was no
  read path at all**. Nothing upstream of the cluster showed it: the
  template rendered, the render was valid, the chart linted and the golden
  was byte-identical to one that works.

  **The intent is unchanged, and absence is how it is expressed.** There
  must be no unauthorized user, because from vmauth v1.147.0 a token that
  verifies but carries no `vm_access` claim falls THROUGH to it rather than
  being rejected. Omitting the field is what produces that: the operator
  writes the `unauthorized_user` section of vmauth's configuration only
  when the field is set, nothing defaults it, and with it absent the key is
  never written. There is no "off" setting to write instead — the operator
  rejects a section with no route, so every shape it accepts is a shape
  that serves. Confirmed on a cluster running the pinned operator: with no
  section, a verified token carrying no claim, a garbage token and a
  request with no `Authorization` header are all answered **401**; with the
  smallest section the schema accepts, all three are answered **200**.

  **Nothing to do on upgrade.** No value changes, and an object already
  holding the pruned `unauthorizedUserAccessSpec: {}` has the field removed
  by the upgrade itself, after which the operator reconciles it
  `operational`.

- **New: rendered objects are checked against the CustomResourceDefinitions
  this repository ships.** `TestRenderedObjectsSurviveTheCRDs` walks every
  custom resource in every golden against the schema for its kind and fails
  on any field the API server would prune, and `TestNoUnauthorizedUser`
  fails if a rendered VMAuth carries an unauthorized section by either
  spelling. `tests/pruned/` holds manifests that pass every other check and
  are destroyed on apply, so the checker has to prove it can fail. The
  general shape — a pruned field is indistinguishable, in a rendered
  manifest and in every golden, from a field that works — is in
  docs/safety.md.

## v0.3.0

The audience pin, and every `match_claims` value meaning only itself.
vmauth checks a token's expiry and its issuer; who the token was minted
FOR is the caller's to state, and now has to be.

- **Breaking: `pkg/tenancy` and `charts/observability-stack`** —
  `Config.Audience` and `tenancy.audience` are new and **required**
  wherever a proxy configuration is rendered. The migration is one line:
  set it to the client id this proxy's own tokens are minted under. It is
  rendered into every reader's `match_claims` beside the group, under
  `aud`.

  **This narrows who the proxy admits.** vmauth validates a token's
  expiry and, with OIDC discovery configured, its issuer — and nothing
  else. It has no audience option and never inspects `aud` on its own. So
  until now a reader was selected by its group alone, and every unexpired
  token the issuer minted was admitted whatever client it was minted for:
  an estate whose issuer serves several applications was admitting a
  token a person holds for a different one, which then read that person's
  namespaces. Nothing reported it, because the token verified, the claim
  matched and the filters applied.
  - **The claim name is fixed, not an input.** `aud` is OpenID Connect's
    own name for it, and a second spelling of a spec-defined claim is how
    a configuration comes to read as though something were pinned when
    nothing is. `tenancy.claimName` may therefore no longer be `aud`:
    both are entries in one `matchClaims` map, and one would overwrite
    the other.
  - **Every `match_claims` value is now escaped and anchored** — the
    audience and the **group** alike, on both the library and the chart
    side. vmauth compiles each value as a regular expression, and neither
    value belongs to this repository: an issuer assigns a client id, an
    identity provider names a population. Escaped, so a client id with a
    dot in it pins that client rather than every id of the same length;
    anchored, so a group written `.*` matches the literal `.*` and no
    other token. **The rendered values change shape**: `groups:
    "example:k8s:viewer"` becomes `groups: "^(example:k8s:viewer)$"`, and
    `helm diff` shows it on every reader. It matches the same tokens it
    was meant to match and fewer of the ones it was not, so no grant
    widens and no principal that was reachable stops being reachable.
  - **Escaped rather than refused, unlike a cluster or namespace name.**
    Those names are the estate's own and are still refused outside the
    plain-name shape. A client id and a group name are handed to the
    estate by an identity provider it does not control — issuers mint
    ids with dots in them — so refusing a shape we do not control would
    be an outage with no alternative available to the operator. The rule
    is: refuse what we name, escape what we are handed. What is still
    refused on the audience is a value no issuer mints: one carrying
    whitespace or a newline, which is how a value that arrived from the
    wrong place looks.
  - **The anchors are rendered although vmauth anchors too**
    (`^(?:…)$`, since v1.152.0, which is already this design's floor):
    a narrowing control that works only when the binary in front of it is
    patched is a control with a version number in it. Both sides are
    tested under both compilations.
  - **A list `aud` needs no special case.** vmauth tests a
    `match_claims` entry against an array claim element by element, so
    the pin works whether the issuer mints the claim as a string or as a
    list.
  - Three negative fixtures under `tests/invalid/observability-stack/`
    (no audience beside principals, an audience that is not an identifier,
    the groups claim named `aud`), the Go refusals beside them, and the
    rendered values walked in the OUTPUT on both sides — the chart's
    VMUsers and the library's users — because a pin both sides dropped
    would leave every comparison between them satisfied.

  Adopting: register a client for this proxy if there is not one already,
  and set `tenancy.audience` / `Config.Audience` to its id. A render
  refuses until you do. Readers whose tokens are minted for that client
  are unaffected; readers arriving with a token for some other client of
  the same issuer stop being admitted, which is the point.

## v0.2.0

The vocabulary rework. `tenant` × `env`, derived from namespace labels,
is retired; the scoping key is the **cluster and the namespace**, under
OpenTelemetry's names, and the environment tier rides along as a
descriptive dimension that is never a key.

- **Breaking: `pkg/tenancy`, `charts/observability-stack`,
  `charts/observability-emitters`** — one coordinated change, and the
  migration is two lines: a grant's `env` becomes `cluster` and its
  `tenants` become `namespaces` (`allTenants` → `allNamespaces`); the
  emitters' `tenancy` block is replaced by `tenancy.cluster` and
  `tenancy.environment`. What every writer stamps and every filter
  selects on, per signal:

  | dimension | metrics label | log field (both writers) | span attribute |
  |---|---|---|---|
  | cluster — key | `k8s_cluster_name` | `k8s.cluster.name` | `k8s.cluster.name` |
  | namespace — key | `k8s_namespace_name` (`namespace` stays too) | `kubernetes.pod_namespace` | `k8s.namespace.name` |
  | tier — descriptive | `deployment_environment_name` | `deployment.environment.name` | `deployment.environment.name` |

  A project is not a label on the telemetry any more: it is a
  derivation from a name to the namespaces it owns, held with whoever
  writes the grants. That is what removes `tenancy.namespaceLabels`,
  `tenancy.fallbackTenant`, `tenancy.tenantLabel` and `tenancy.envLabel`
  from the emitters chart — a namespace always has a name, so the
  unlabelled-namespace case, the fallback tenant, and the no-fallback gap
  on the log path all stop existing. The tier is never a key because two
  clusters can share one.
  - **`Grant{Env, Tenants, AllTenants}` is `Grant{Cluster, Namespaces,
    AllNamespaces}`**; the empty-list refusal, the hostile-name refusals
    and every message stay, rewritten to teach the new model.
  - **The keys are inputs with defaults on both paths.** `ClusterLabel`
    / `NamespaceLabel` (`tenancy.clusterLabel` / `tenancy.namespaceLabel`)
    default to `k8s_cluster_name` / `k8s_namespace_name`;
    `LogsClusterField` / `LogsNamespaceField` (`tenancy.logsClusterField`
    / `tenancy.logsNamespaceField`) default to `k8s.cluster.name` /
    `kubernetes.pod_namespace`. The log fields had no default in 0.1.0
    because the field was derived from an estate's own namespace label,
    which every estate spelled differently; the key is the namespace's
    name now, which every estate spells the same way, and the default is
    the field the container-log agent natively writes. A metrics key is
    held to the Prometheus label shape rather than the plain-name shape,
    because the defaults carry underscores.
  - **The two log writers name the namespace the same way**, which
    0.1.0's docs/safety.md listed as not fixed. The container-log agent
    cannot rename a field, so its native `kubernetes.pod_namespace` is
    the key and the gateway writes that spelling on its log pipeline
    beside `k8s.namespace.name`; cluster and tier reach the agent
    through `-kubernetesCollector.extraFields` under the exact
    conventional names, so the two agree for free. `extraFields` is a
    mirror of `tenancy` and is refused when it disagrees.
  - **OTLP-derived metrics carry the keys as labels.** The Prometheus
    remote-write exporter puts a resource attribute on `target_info`
    and nowhere else unless told to promote it, so a series would have
    reached the store with no cluster and no namespace and every scoped
    query would have missed it. The gateway's metrics exporters now
    promote exactly the three through `resource_constant_labels`
    (dots to underscores on the way out), verified against the collector
    binary the chart pins. The gateway's components are also declared
    under the pinned version's current type names —
    `prometheus_remote_write`, `otlp_http`, `delta_to_cumulative` — since
    the old aliases log a deprecation warning at 0.161.0; and the queue
    extension now creates its directory, because a fresh volume is empty
    and the extension refuses to start on a directory that does not
    exist, which would have crash-looped every first boot on a new
    PersistentVolume. Both found by running the rendered file.
  - **The gateway strips a namespace an SDK claims before resolving the
    pod**, in a `transform/disown` step, because `k8sattributes` writes
    an attribute only when it is absent — a resource that arrived
    carrying `k8s.namespace.name` would have kept the application's
    claim, and the namespace is now the key. The pod is resolved from
    the connection first; the pod-UID and pod-IP attributes an SDK
    supplies are fallbacks.
  - **The metrics agent** relabels `k8s_namespace_name` from service
    discovery on every scrape object, copies the container's own
    `namespace` into it on the two node-level jobs after the scrape,
    stamps the cluster and the tier statically, and passes the Helm
    release through as `app_kubernetes_io_instance`. `attachMetadata.namespace`
    on the scrape class, and its refusal, are gone: the namespace name
    needs no metadata. The `overrideHonorLabels` guard and its
    `exported_(…)` labeldrop, and the "default scrape class stamps
    nothing" refusal, are re-targeted to the three new labels.
  - **The Helm release passes through everywhere and is never a stream
    field**: pod labels are on for the container-log agent (upstream's
    default, reversed from 0.1.0), the gateway extracts
    `app.kubernetes.io/instance` as `k8s.pod.labels.app.kubernetes.io/instance`,
    and the stream-field allow-list refuses it.
  - **`tests/agreement_test.go` walks the rendered output of every
    writer for every dimension** — the metrics agent's two scrape
    shapes, the container-log agent's flags, and the gateway's three
    pipelines — against the names read back out of a rendered library
    filter. Negative fixtures: a blank `tenancy.cluster`, a blank
    `tenancy.environment`, a hostile value of either, a static-fields
    mirror that disagrees or is not JSON, either log key missing from
    either writer's stream fields, the release label as a stream field,
    and the two keys colliding on the stack chart.

  Adopting: rename the grant fields, replace the emitters' `tenancy`
  block, write the log agent's `extraFields` as the chart tells you to,
  and drop `logsTenantField` / `logsEnvField` unless your log agent is
  not this chart's. Existing data stamped `tenant`/`env` is not
  rewritten; it stops matching grants once the proxy is upgraded, which
  is the intended shape rather than a migration to run.

## v0.1.0

The first release. Everything below is new to a consumer, so the two
authorization fixes among these entries describe defects that **never
reached a published version** — they were found and fixed between the
repository being created and this tag. Nobody ran them.

They are written up anyway, at length, because the mechanisms are the
ones an operator has to understand to run this safely, and because each
one is a shape that could come back.

- **`pkg/tenancy` and `charts/observability-stack`** — the proxy now
  APPLIES the filters it renders. **This is an authorization fix: before
  it, every principal who passed JWT verification read every tenant's
  metrics and logs.** A `vm_access` claim does nothing on its own —
  vmauth applies it only by substituting a placeholder into the route it
  forwards on, and the routes carried none, so each principal's claim was
  verified, computed, written into the manifest and then discarded. Every
  read route now carries its filter argument
  (`extra_filters={{.MetricsExtraFilters}}` for metrics,
  `extra_stream_filters={{.LogsExtraStreamFilters}}` for logs), a route
  cannot be constructed without one, `Validate` refuses one that lost it,
  and tests on both sides walk every rendered route and fail on any that
  does not carry it. Four things follow:
  - **Traces cannot be scoped at all, and now say so.** VictoriaTraces'
    Jaeger and Tempo select APIs accept no query argument a proxy could
    put a filter in. With a trace store and `principals` both set, the
    chart refuses to render until `tenancy.allowUnfilteredTraceReads` is
    `true` and the library until `AllowUnfilteredTraceReads` is set —
    which records that every principal who can reach the proxy reads
    every tenant's spans. It admits the trace route and nothing else.
    **The trace store is enabled by default, so an existing values file
    with principals in it will refuse to render until this is answered.**
  - **The logs filter is now prefixed `_stream:`.** VictoriaLogs reads an
    `extra_stream_filters` value beginning with `{"` as its JSON object
    form, and every filter rendered here begins with `{"` because a log
    field name has to be quoted — so the unprefixed value would have
    failed to parse on every log query once it started being sent.
  - **Two metrics routes are gone.** `/api/v1/metadata` and everything
    under `/api/v1/status/` except `tsdb` and `buildinfo` take no filter,
    so no filter narrows them: they returned metric names, and other
    principals' query text, across every tenant.
  - **`vmauth.extraArgs.mergeQueryArgs` naming a filter argument is
    refused.** The clash between a client's query argument and the
    route's is the other half of the enforcement, and that flag exempts
    an argument from it; vmselect ORs `extra_filters` alternatives, so a
    caller adding an empty one would read every tenant.
  - **A claim can no longer be rendered empty.** An empty filter list
    does not deny anything — it removes the query argument, and with it
    the clash that stops a caller supplying its own.

  Needs no action beyond answering the traces question, and `helm diff`
  before the upgrade shows the new `query_args` on every reader's routes.
  See docs/safety.md, "A filter that is computed and never applied".

- **`pkg/tenancy` and `charts/observability-stack`** — the logs filter
  names the field the log store actually has. Until now both rendered the
  same string for both signals, so a reader querying logs through the
  proxy was filtered on `tenant` — a field the log path does not have and
  cannot have, because vlagent can rename no field and a namespace label
  arrives as `kubernetes.namespace_labels.<key>`. The query did not fail;
  it returned nothing, which reads as "my service logged nothing".
  **Breaking, and deliberately so:** `tenancy.logsTenantField` and
  `tenancy.logsEnvField` on the chart, `LogsTenantField` and
  `LogsEnvField` on `tenancy.Config`, are now required whenever there is a
  principal, and there is no default — every default anyone would write is
  right on one estate and silently wrong on the next. With
  `charts/observability-emitters` they are
  `kubernetes.namespace_labels.<tenancy.namespaceLabels.project>` and
  `tenancy.envLabel`, which that chart already refuses to render without.
  Two further changes follow from LogsQL rather than from taste: the field
  name is quoted and held to a field shape (`^[a-zA-Z0-9_][a-zA-Z0-9_./-]*$`)
  rather than to the plain-name shape a tenant is held to, since a real
  field name carries dots and a slash; and a principal now gets **one**
  stream filter with its grants as `or` alternatives instead of one entry
  per grant, because VictoriaLogs AND-s every `extra_stream_filters`
  argument it is given — two entries naming two environments intersected
  in nothing, so the principal with the most access got the emptiest
  screen. The metrics and traces paths are unchanged. See docs/safety.md.

- **`pkg/tenancy`** — the default `vm_access` claim is rendered inside
  the token block rather than beside the route map. vmauth's user object
  has no such field and its parser is strict, so the misplaced version
  did not merely lose the default: vmauth refused the whole
  configuration file and exited, and the proxy never started. Caught by
  running a rendered configuration against the binary; a test now asserts
  the nesting against the marshalled output rather than against our own
  structs.

- **`charts/observability-emitters`** — per-cluster collection: vmagent as
  a `VMAgent` the operator reconciles, vlagent from the vendor's own
  DaemonSet chart, and an OpenTelemetry gateway this chart renders itself.
  Each is optional, each replicates to every destination it is given with
  its own on-disk buffer, and each stamps `tenant` and `env` from the
  **namespace's** labels — an application that sets them itself has them
  overwritten. Install `charts/observability-crds` first: the chart
  renders `PodMonitor` objects, and on a cluster without those CRDs every
  other chart's monitor template renders nothing at all, silently, with a
  successful sync. Twenty-seven refusals, each with a fixture, and the three
  worth knowing before you write the values file: `overrideHonorLabels`
  cannot be turned off, because a target that exports its own `tenant`
  label would otherwise choose its own tenant; `remoteWrite.shardByURL` is
  refused outright, because it splits the series between a redundant pair
  instead of replicating to both and every query still answers with half
  of every result missing; and a buffer on an emptyDir is refused for all
  three emitters, including the log agent's, where the same volume holds
  the checkpoint that stops it re-reading every container log from the
  beginning on each rollout. Five values have no default and are asked for
  rather than guessed — `tenancy.env`, `tenancy.fallbackTenant`,
  `tenancy.namespaceLabels.project`, `writeCredentials.secretName` and a
  destination list per emitter — because each of them renders, runs and
  reports healthy when it is wrong. **One thing to carry out of the
  chart:** on the log path the tenancy stream field is
  `kubernetes.namespace_labels.<your project label key>` and **not**
  `tenant`, because vlagent cannot rename a field; a proxy filtering on
  `tenant` against those streams returns an empty result rather than an
  error. See docs/safety.md.
- **`charts/observability-stack`** — one install of the store: the
  VictoriaMetrics family from the vendor's own pinned charts, with the
  proxy, the two vmalerts, Alertmanager, the network policies and the
  backups this chart renders itself. Reads go through vmauth, which
  verifies the caller's token against an OIDC issuer and injects the
  filters that token is entitled to; `pkg/tenancy` renders the same
  principals into the claim an issuer mints, and a test compares the two
  so they cannot drift. Single-replica: `ha` is accepted and refuses fewer
  than two zones, and the zone-redundant behaviour lands in a later
  release. Install `charts/observability-crds` first and have cert-manager
  present — the operator's own `crds.enabled` is off here, and its webhook
  certificate comes from cert-manager rather than from a self-signed CA
  the chart would regenerate on every upgrade. Seventeen refusals, each
  with a fixture: a retention without a unit (a bare number is months), the
  two disk guards that are mutually exclusive at the binary, a fractional
  CPU (the store rounds it down and buys one thread), an `enterprise` image
  tag, a licence flag, a vmauth below v1.152.0, a Grafana datasource
  without `oauthPassThru`, and the rest in docs/safety.md. Three values are
  written twice because Helm cannot compute a subchart's values; the chart
  refuses to render when a pair disagrees.
- **`charts/observability-crds`** — the CustomResourceDefinitions this stack
  needs, as a release of their own: the VictoriaMetrics operator's, and the
  `PodMonitor`, `ServiceMonitor`, `ScrapeConfig` and `Probe` kinds every
  component authors its scrape objects in. Install it at a wave ahead of
  the stack with `prune: false` and `ServerSideApply=true`, and turn the
  operator chart's own `crds.enabled` off — Helm never upgrades a CRD it
  installed from a chart's `crds/` directory, so a set with two owners is
  a schema that drifts behind the controller reading it. Both upstreams are
  pinned; every render ends with the kinds it carries and the version each
  one stores, which is what a bump is reviewed against. Install it before
  any chart that offers a monitor template and before switching such a
  value on: a chart whose monitor is gated on
  `.Capabilities.APIVersions.Has "monitoring.coreos.com/v1"` renders
  nothing when the kind is absent and still reports a successful install.
- **`charts/platform-alerts`** — the rules that fire when something has
  stopped working while everything still looks green: a CronJob no longer
  being scheduled, a store whose write path has died, a volume that was
  never mounted, a store approaching its own read-only limit. There is no
  default store list: the chart refuses to render until the counters are
  named, because a guessed metric name renders cleanly and then never
  fires.
- **`pkg/tenancy`** — one input, two shapes: a vmauth configuration and
  the `vm_access` claim an issuer mints. A tenant or environment name
  outside the plain-name shape is refused rather than escaped, because a
  name carrying `|` or `.*` would widen the grant it appears in.

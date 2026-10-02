"""Descriptions for the panels of the adapted platform dashboards.

Keyed by (dashboard, panel title); a ("*", title) entry serves every
dashboard. The upstream dashboards describe only some of their panels;
docs/dashboards.md requires every panel to say what it shows and what to do
about it. Applied by hack/dashboards/platform_adapt.py to a panel that has
none; upstream's own wording, where it exists, is kept.
"""

_D = {}


def _add(dashboard, entries):
    for title, text in entries.items():
        _D[(dashboard, title)] = text


_add("argocd", {
    "Uptime": "How long the Argo CD API server has been running. A value that keeps resetting is a crash loop: read the server's pod events.",
    "Clusters": "Destination clusters Argo CD manages. A number that changes with no planned cluster change means a cluster was removed or its credentials expired.",
    "Applications": "Applications matching the health, sync and destination filters above. Compare with the total to see how much is in trouble.",
    "Repositories": "Distinct Git repositories the applications track. Use it as a scale figure; an unexpected drop is applications that lost their source.",
    "Operations": "Applications with a sync or refresh operation in flight now. A number that stays high for long is a stuck operation: open the application and read its operation state.",
    "Applications over time": "Applications by the chosen grouping over time. A step is applications added or removed; a slow slide is applications leaving the selected filters.",
    "Health Status": "Applications by health status over time. Anything other than Healthy for long, especially Degraded or Missing, needs a look: open the application and read the failing resource.",
    "Sync Status": "Applications by sync status over time. OutOfSync that persists means Git and the cluster disagree and nothing is reconciling it: check auto-sync and the last sync result.",
    "Sync Activity": "Syncs started in each interval, by the chosen grouping. A burst is a deploy or a sync loop; a flat zero on a busy estate is a controller that stopped.",
    "Sync Failures": "Syncs that ended in Error or Failed in each interval. Should be zero. Open the affected application and read the sync result message.",
    "Reconciliation Activity": "Application reconciliations in each interval, by the chosen grouping. A sudden rise is a resync storm; a fall to zero means the application controller is not reconciling.",
    "Reconciliation Performance": "Distribution of how long an application reconcile takes. A band that moves up means slow Git or a heavy manifest: compare with the repo server panels.",
    "K8s API Activity": "Kubernetes API requests the controller made in each interval, by verb and resource kind. A spike is a resync or a cluster watch restarting; sustained high volume can throttle the API server.",
    "Workqueue Depth": "Items waiting in the application controller's work queues. A depth that does not drain means reconciles are slower than changes arrive: check the controller's CPU and the repo server.",
    "Pending kubectl run": "kubectl commands the controller has queued and not started. A number that grows means the controller is short of exec capacity.",
    "Controller memory usage": "Go heap allocated by the application controller. A steady climb over days is a leak or a growing estate; a jump on restart is the cluster cache filling.",
    "Controller CPU usage": "CPU seconds per second used by the application controller. Sustained near its limit means reconciles queue behind it.",
    "Controller goroutines": "Goroutines in the application controller. A count that only grows is a leak; compare with memory.",
    "Resource Objects Count": "Kubernetes objects Argo CD caches per destination cluster. Growth is the estate growing; it also sizes the controller's memory.",
    "API Resources Count": "API resource kinds Argo CD watches per destination cluster. A change with no CRD change points at a cluster upgrade or a broken discovery.",
    "Cluster Events Count": "Cluster events the controller processed in each interval, per destination cluster. Sustained high volume is a noisy workload driving reconciles.",
    "Git Requests (ls-remote)": "Git ls-remote calls the repo server made in each window, to detect new commits. A rise means more applications or a shorter refresh interval; failures show in sync errors.",
    "Git Requests (checkout)": "Git fetches the repo server made in each window, to render manifests. A rise is new commits or cache misses; sustained high volume loads the Git host.",
    "Git Fetch Performance": "Distribution of Git fetch durations. A band that moves up is a slow Git host or network: sync latency follows.",
    "Git Ls-Remote Performance": "Distribution of Git ls-remote durations. A band that moves up delays the detection of new commits.",
    "API server memory used": "Go heap allocated by the Argo CD API server. A steady climb is a leak; a jump follows a restart.",
    "API server goroutines": "Goroutines in the Argo CD API server. A count that only grows is a leak; compare with memory.",
    "Repo server memory used": "Go heap allocated by the repo server. Large manifests and many parallel renders raise it; a steady climb is a leak.",
    "Repo server goroutines": "Goroutines in the repo server. A count that only grows is a leak; compare with memory.",
    "GC Time Quantiles": "Go garbage-collection pause of the API server, worst case. Long pauses mean memory pressure: compare with the memory panel.",
    "Requests by result": "Redis requests made by Argo CD components, split by whether they failed. The failed series should be zero; a rise means Redis is unavailable or slow and the UI and controller cache suffer.",
})
for _svc in ("Application", "Cluster", "Project", "Repository", "Session", "Version", "Account", "Settings"):
    _add("argocd", {
        "%sService Requests" % _svc: "gRPC calls to the API server's %s service in each interval, by method and result code. Non-OK codes are the ones to read: a rise in errors on a method is a failing feature or a client misbehaving." % _svc,
    })

_add("cert-manager", {
    "Soonest Cert Expiry": "Time left before the soonest certificate expires, across the selected cluster. Below two weeks and not renewing means an issuer or an ACME challenge is failing: open the Certificates table and read the Ready status.",
})

_add("cnpg-operator", {
    "Operator pods up": "CloudNativePG operator metrics endpoints answering. Zero means the operator is down or not scraped: no Postgres cluster is being reconciled, and the panels below are empty.",
}, )

_add("cnpg-cluster", {
    "Instance ready": "Whether every selected Postgres instance's container reports ready. Anything but ready means a pod is starting, failing its probes or restarting: open the pod and read its events and the instance log.",
    "Replication status": "Whether each instance is doing the replication work its role needs: a primary accepting standbys, a standby receiving WAL. A standby that reads as not receiving is falling behind or cut off: read the replication lag panels below.",
    "Streaming replicas": "Standbys connected to each primary's WAL sender. Fewer than the number of replicas the cluster is configured with means a standby is down or cannot reach the primary.",
    "Connections": "Sessions open on each instance, by pod. A steady climb toward the maximum is a pool that does not release connections; compare with Max connections.",
    "Connections used of the maximum": "Open sessions as a share of max_connections, per instance. Past 80% new clients will start being refused: add a pooler or raise the limit.",
    "Transaction ID age": "Age of the oldest unfrozen transaction ID in each instance's databases. Autovacuum keeps it low; one that approaches the wraparound limit (about two billion) forces the database read-only, so act long before.",
    "Started": "When each instance's Postgres process last started. A recent time on an instance nobody restarted is a crash or a failover.",
    "Postgres version": "The Postgres version each instance runs. Instances of one cluster should agree except during a rolling upgrade.",
    "Version": "The Postgres version each instance runs. Instances of one cluster should agree except during a rolling upgrade.",
    "Last failover": "When the current primary's Postgres process started, shown as time ago. A recent time with no planned change is a failover: read the cluster's events.",
    "TPS": "Transactions committed per second across the selected instances. A drop to zero on a busy cluster is an outage or an application that stopped; compare with the rollback rate in Transactions.",
    "Memory Utilisation": "Working-set memory of the selected instances as a share of their memory requests. Near or above 100% leaves no headroom: raise the request or look at work_mem and connection counts.",
    "Replication Lag": "The largest replica lag the instances report, in seconds. Near zero is healthy; a lag that grows is a standby that cannot keep up with the primary's writes or is cut off.",
    "Write Lag": "Time between the primary flushing WAL and a standby confirming it has written it. A rise means the network or the standby's disk is slow.",
    "Flush Lag": "Time between the primary flushing WAL and a standby confirming it flushed it to disk. A rise means the standby's storage cannot keep up.",
    "Replay Lag": "Time between a WAL record being flushed on the primary and applied on a standby. A rise with flat write lag means the standby is slow to replay: reads from it are stale.",
    "Volume Space Usage": "Fullest PVC (data or WAL) of the selected instances, in percent. Past 85% expand the claim; a full data volume stops Postgres.",
    "Last Base Backup": "How long ago the last base backup completed, from the backup's own timestamp. A value much older than the backup schedule means backups are failing: read the Backup resources and the barman logs.",
    "Last archived WAL": "Seconds since the primary last archived a WAL segment. It grows on a quiet database; one that keeps growing on a busy one means archiving is failing and WAL piles up on the volume.",
    "Database Size": "Size of each database on each instance. A steady climb is data growth; a jump is a bulk load or runaway table.",
    "First Recoverability Point": "The oldest point in time the backups can restore to. A point that does not move forward means retention is not pruning, one that moves too far forward means backups are being lost.",
    "Session States": "Sessions by state (active, idle, idle in transaction ...) summed over the instances. Many idle in transaction is an application holding transactions open, which blocks vacuum.",
    "Transactions [5m]": "Commits and rollbacks per second over five minutes. A rising rollback share means application errors or deadlocks.",
    "Longest Transaction": "Age of the oldest open transaction, per instance. A long one holds back vacuum and bloats tables: find its session and end it.",
    "Deadlocks [5m]": "Deadlocks per second detected over five minutes. Any steady count is two code paths locking in different orders: fix the application.",
    "Blocked Queries": "Sessions waiting on a lock, per instance. A number that stays above zero is a long transaction blocking others.",
    "Memory Usage (container memory working set)": "Working-set memory of each selected instance container. A steady climb is a leak or growing shared buffers; reaching the limit means an out-of-memory kill.",
    "Volume Space Usage: PGDATA and WAL": "Used share of each instance's data and WAL volumes. Past 85% expand the claim; a full WAL volume stops writes at once.",
    "Volume Inode Usage: PGDATA and WAL": "Used share of each instance's volume inodes. Many small files (temporary tables, WAL) can exhaust inodes before space.",
    "Volume Space Usage: Tablespaces": "Used share of each tablespace volume. Past 85% expand the claim.",
    "Tuple I/O [5m]": "Rows inserted, updated, deleted, fetched and returned per second over five minutes. Read the mix: a surge of updates or deletes precedes bloat.",
    "Block I/O [5m]": "Buffer-cache hits against blocks read from disk, per second. A falling hit share means the working set no longer fits in shared buffers.",
    "Temp Bytes [5m]": "Bytes per second written to temporary files. A steady rate means sorts and hashes spill to disk: raise work_mem for those queries or fix them.",
    "WAL Segment Archive Status": "WAL segments waiting to be archived. Should be near zero: a count that grows is archiving failing and WAL filling the volume.",
    "Archiver Status [5m]": "WAL segments archived and failed per second over five minutes. Failures should be zero; a failing archive blocks the volume from being recycled.",
    "Last Archive Age": "Seconds since each instance last archived a WAL segment. Grows on a quiet database; climbing on a busy one means archiving is failing.",
    "WAL Count": "WAL segment files on each instance's volume. A count that keeps rising is WAL being retained: a stuck archive, a stuck replication slot or a heavy write burst.",
    "Collection Duration": "How long the instance exporter's metric queries take. A rise means the database is slow to answer its own monitoring.",
    "Errors": "Collector queries that failed (1 is an error on the last collection). Any non-zero value leaves metrics stale or missing for that instance.",
    "Requested/Timed": "Checkpoints per second, split into requested (forced by WAL volume) and timed (on schedule). Mostly requested checkpoints mean max_wal_size is too small for the write rate.",
    "Write/Sync time": "Time spent writing and syncing checkpoint files, per second. A rise means slow storage under write load.",
    "Max connections": "The configured max_connections of each instance: the ceiling of open sessions.",
    "Shared buffers": "The configured shared_buffers of each instance, in bytes: Postgres's own cache.",
    "Effective cache size": "The planner's estimate of the cache available to one query (shared buffers plus the OS cache), in bytes. It guides plans and allocates nothing.",
    "Work mem": "The configured work_mem of each instance, per sort or hash node, in bytes. Large values multiply across concurrent sessions.",
    "Maintenance work mem": "The configured maintenance_work_mem of each instance, in bytes: memory for vacuum and index builds.",
    "Random page cost": "The planner's cost of a non-sequential page read. Low values on SSD favour index scans.",
    "Sequential page cost": "The planner's cost of a sequential page read: the unit the other planner costs are measured in.",
})

_add("nats-server", {
    "Server CPU": "CPU used by each NATS server, in percent of one core. Sustained high values mean the server is saturated: look at message rate and connection count.",
    "Server Memory": "Resident memory of each NATS server. A steady climb is a slow consumer holding messages or a leak; a JetStream server also holds memory-backed streams here.",
    "Bytes In": "Bytes per second received by each server from clients and routes. Read with Bytes Out: a large gap is fan-in or fan-out, not a fault.",
    "NATS Msgs In": "Messages per second received by each server. A drop to zero on a busy subject is a publisher that stopped.",
    "Bytes Out": "Bytes per second sent by each server to clients and routes. A rise with flat input is fan-out growing: more subscribers.",
    "NATS Msgs Out": "Messages per second sent by each server. A rise with flat input is more subscribers; a fall with flat input is subscribers that left or fell behind.",
    "Connections": "Client connections open on each server. A sudden drop is a server restart or a network split; a steady rise is a client leaking connections.",
    "Subscriptions": "Subscriptions registered on each server. Growth without more clients is a subscription leak.",
    "Slow Consumers": "Clients the server disconnected or dropped messages for because they could not keep up. Should be zero. Find the client behind it and raise its pending limit or speed it up.",
})

_add("nats-jetstream", {
    "Storage Used": "File-backed JetStream storage used, as a share of the configured maximum, across the selected servers. Past 80% means streams will start refusing writes: add storage or trim stream limits.",
    "Total Storage Used": "Bytes of file-backed JetStream storage in use across the selected servers.",
    "Memory Used": "JetStream memory storage in use, as a share of the configured maximum. Past 80% means memory-backed streams are close to their limit: raise the limit or move the stream to file storage.",
    "Connections": "Client connections across the selected servers, for scale.",
    "Max Storage": "Configured maximum of JetStream file storage across the selected servers. The denominator of Storage Used.",
    "Total Memory": "Configured maximum of JetStream memory storage across the selected servers. The denominator of the memory gauge.",
    "Total Consumers": "JetStream consumers across the selected servers. A sudden fall is consumers deleted or a server that lost its state.",
    "Stream data size": "Bytes held by each stream. A stream that only grows is missing a retention limit; a fall is retention or a purge working.",
    "Stream message count": "Messages held by each stream. Compare with the size panel: many small messages and a few large ones cost differently.",
    "Message Rate (per second)": "Messages per second appended to each stream, from its last sequence number. Zero on a stream that should be busy means its publishers stopped.",
    "Total delivered messages": "Messages delivered to each consumer since it was created. It should keep rising for an active consumer; a flat line is a consumer that stopped receiving.",
    "Pending messages": "Messages a consumer has not yet been sent. A number that only grows is a consumer that cannot keep up: scale it or read its acknowledgement errors.",
    "Message Acks Pending": "Messages delivered to a consumer and not yet acknowledged. A large or growing number is a slow or stuck handler; messages will be redelivered after the ack wait.",
})

_add("envoy-gateway", {
    "Duration Status": "Average time the selected runner takes to process a resource update, in milliseconds. A rise means the controller is slower to react to Gateway API changes.",
    "Statistics": "Updates the selected runner processed, by outcome. Failures should be zero: a rise means resources are being refused; read the controller's log.",
    "Avg Duration": "Average time to write a status back to Gateway API resources, by kind. A rise means the API server is slow or the controller is backed up.",
    "p99 Duration": "99th-percentile time to write a status back, by kind. Long tails delay the Accepted and Programmed conditions users wait on.",
    "p50 Duration": "Median time to write a status back, by kind. The baseline the p99 is read against.",
    "Status": "Status updates by outcome. Failures should be zero; a rise means the controller cannot write conditions back to resources.",
    "Snapshot Creation Status": "xDS snapshots the controller built, in total and by outcome. Failures mean a configuration it could not translate: read the controller's log and the resource's conditions.",
    "Finished Stream": "The highest bucket count of finished delta xDS streams. Context for how many proxy connections came and went; a steady climb means proxies reconnect often.",
    "Update Status": "xDS snapshot updates pushed to proxies, by outcome. Failures mean a proxy did not get its configuration: check the proxy's connection to the controller.",
    "Total Apply Duration Bucket": "Distribution of how long applying an infrastructure resource (proxy Deployment and Service) took, in cumulative buckets. Most of the mass should sit in the low buckets.",
    "Avg Apply Duration": "Average time to apply infrastructure resources, by kind. A rise means the Kubernetes API is slow or contended.",
    "p99 Apply Duration": "99th-percentile time to apply infrastructure resources, by kind. Long tails delay a new Gateway becoming ready.",
    "p50 Apply Duration": "Median time to apply infrastructure resources, by kind. The baseline the p99 is read against.",
    "Total Delete Duration Bucket": "Distribution of how long deleting an infrastructure resource took, in cumulative buckets.",
    "Avg Delete Duration": "Average time to delete infrastructure resources, by kind. A rise delays cleanup after a Gateway is removed.",
    "p99 Delete Duration": "99th-percentile time to delete infrastructure resources, by kind.",
    "p50 Delete Duration": "Median time to delete infrastructure resources, by kind. The baseline the p99 is read against.",
})

_add("*", {
    "Live servers": "Envoy proxies reporting live now. Zero means no data plane is serving; compare with the number of proxy pods you expect.",
    "Avg uptime per node": "Average time the proxies have been running. A value that keeps resetting is a crash loop or repeated restarts: read the proxy pods' events.",
    "Heap Size": "Envoy heap memory reserved from the operating system, per proxy. Compare with the pod's memory limit.",
    "Allocated Memory": "Envoy heap memory in use, per proxy. Compare with the pod's memory limit; a steady climb is a leak or a growing configuration.",
})

_add("envoy-proxy", {
    "Downstream RPS": "HTTP requests per second received by all proxies. The traffic level the rest of the dashboard is read against; a fall to zero with no cause is an outage upstream of the gateway.",
    "Downstream CPS": "New client connections per second, by proxy namespace. A spike is a client reconnecting in a loop or a connection-per-request client.",
    "Downstream Latency": "Client-facing request latency at p50, p90 and p99, in milliseconds. A rising p99 with a flat p50 is a slow backend or a saturated proxy.",
    "Downstream Total Connections": "Client connections open on the listeners, by proxy namespace. A steady climb is clients not closing connections.",
    "TCP Downstream CPS": "New TCP client connections per second on TCP routes, by proxy namespace.",
    "TCP Downstream Bytes Rx/second": "Bytes per second received from clients on TCP routes.",
    "TCP Downstream Bytes Tx/Second": "Bytes per second sent to clients on TCP routes.",
    "Upstream CPS": "New connections per second from the proxies to backends, by proxy namespace. High churn with low request rate means connections are not reused.",
    "Upstream Latency": "Backend request latency at p50, p90 and p99, in milliseconds. Compare with the downstream latency to see how much time the backend accounts for.",
    "Upstream Total Connections": "Connections open from the proxies to backends, by proxy namespace. A steady climb is a backend not closing idle connections.",
    "Upstream Bytes Rx/Second": "Bytes per second received from backends.",
    "Upstream Bytes Tx/Second": "Bytes per second sent to backends.",
    "Upstream 2xx Responses": "Backend responses per second with a 2xx status. The healthy baseline the error panels are read against.",
    "Upstream 3xx Responses": "Backend responses per second with a 3xx status (redirects).",
    "Upstream 4xx Responses": "Backend responses per second with a 4xx status. A rise is clients sending bad requests or a broken route or auth policy.",
    "Upstream 5xx Responses": "Backend responses per second with a 5xx status. Should be near zero. A rise is a failing backend: open the Envoy clusters dashboard to see which one.",
    "Endpoint Percentage Health": "Healthy endpoints as a share of all endpoints known to the proxies, by proxy namespace. Below 100% means backends are failing health checks or being removed.",
    "Total Endpoints": "Backend endpoints known to the proxies, by proxy namespace.",
    "Healthy Endpoints": "Backend endpoints currently healthy, by proxy namespace.",
    "Unhealthy Endpoints": "Backend endpoints known to the proxies but not healthy, by proxy namespace. Should be zero; find them in the Envoy clusters dashboard.",
    "TLS Certificate Expiry": "Days until each certificate served by the listeners expires. Under 14 days and not renewing means cert-manager or the issuer is failing: open the cert-manager dashboard.",
})

_add("envoy-clusters", {
    "Unhealthy Clusters": "Endpoints that are known to the selected Envoy cluster and not healthy. Should be zero; if not, the panels below name the cluster.",
    "Cluster State": "One when every endpoint of the selected clusters is healthy, zero otherwise.",
    "Total active connections": "Connections open from the proxies to each selected Envoy cluster. A steady climb is connections not being closed.",
    "Total requests": "Requests per second sent to each selected Envoy cluster. A cluster at zero that should carry traffic is unrouted.",
    "Upstream Network Traffic": "Bytes per second received from and sent to each selected Envoy cluster (received above, sent below, per series). A cluster with requests and no bytes returns empty answers.",
    "Downstream Network Traffic": "Bytes per second received from and sent to clients on HTTP listeners. The client side of the traffic in the panel beside it.",
    "Upstream Latency": "Request latency at p50, p90 and p99 for each selected Envoy cluster, in milliseconds. One cluster far above the others is a slow backend.",
    "Upstream 2xx Responses": "Responses per second with a 2xx status from each selected Envoy cluster. The healthy baseline.",
    "Upstream 3xx Responses": "Responses per second with a 3xx status from each selected Envoy cluster (redirects).",
    "Upstream 4xx Responses": "Responses per second with a 4xx status from each selected Envoy cluster. A rise is a broken route, auth policy or a client sending bad requests.",
    "Upstream 5xx Responses": "Responses per second with a 5xx status from each selected Envoy cluster. Should be near zero; the cluster with the rate is the failing backend.",
    "Downstream members": "Healthy endpoints in the selected Envoy clusters, in total and per cluster; the second panel adds the unhealthy count. A fall is backends leaving or failing health checks.",
    "Outlier ejected": "Endpoints currently ejected by outlier detection, per Envoy cluster. Ejection means the proxy saw repeated failures from them and stopped sending traffic.",
    "Degraded": "Endpoints marked degraded, per Envoy cluster: still receiving traffic at a reduced share.",
    "Excluded": "Endpoints excluded from load balancing, per Envoy cluster, for example while draining.",
})

_add("external-secrets", {
    "SecretStore error rate [15m]": "Share of SecretStore reconciles that ended in an error over 15 minutes. Empty while there are none; above zero means a store cannot validate its provider: read the store's Ready condition and the controller log.",
    "ClusterSecretStore error rate [15m]": "Share of ClusterSecretStore reconciles that ended in an error over 15 minutes. Empty while there are none; above zero means a cluster-wide store cannot validate its provider.",
    "ExternalSecret error rate [15m]": "Share of ExternalSecret reconciles that ended in an error over 15 minutes. Empty while there are none; above zero means secrets are not syncing: open the Not Ready ExternalSecrets table.",
    "ClusterExternalSecret error rate [15m]": "Share of ClusterExternalSecret reconciles that ended in an error over 15 minutes. Empty while there are none or the kind is unused.",
    "PushSecret error rate [15m]": "Share of PushSecret reconciles that ended in an error over 15 minutes. Empty while there are none or the kind is unused.",
    "Provider error rate [15m]": "Share of calls to the secret provider's API that failed over 15 minutes. Empty while there are none; above zero means the provider is unreachable, throttling or refusing the operator's credentials.",
    "Workqueue depth": "Items waiting in the operator's work queues, per queue. A depth that does not drain means reconciles are slower than changes arrive, or stuck.",
    "Webhook error rate [15m]": "Share of admission webhook requests answered with an HTTP 500 over 15 minutes. The webhooks fail closed, so errors here block ExternalSecret and SecretStore writes cluster-wide.",
    "Webhook latency [5m]": "The 99th-percentile time the admission webhook takes to answer. A rise slows every ExternalSecret and SecretStore write.",
    "Reconcile latency [p99]": "The 99th-percentile time a reconcile takes across all the operator's controllers. A rise is a slow provider or an overloaded operator.",
    "reconcile error rate [p99]": "Share of reconciles that ended in an error in the last minute, for the selected controllers. Empty while there are none.",
    "Provider errors [15m]": "Failed calls to the secret provider's API in the last 15 minutes, by provider and call. Should be empty; a call that keeps failing names the provider operation to check.",
    "Not Ready ExternalSecrets  [15m]": "ExternalSecrets whose Ready condition is False, by namespace and name. Should be empty. Open the ExternalSecret and read its status message: a missing remote key, a denied store or a bad template.",
    "ExternalSecret sync call errors [15m]": "Failed ExternalSecret syncs in the last 15 minutes, by namespace and name. A secret that keeps failing is stale in the cluster.",
    "requests by path per minute": "Admission webhook requests per minute by webhook (ExternalSecret and SecretStore validation). A flat zero while resources are being written means the API server is not reaching the webhook.",
    "requests in flight": "Admission webhook requests being handled now, by webhook. A number that stays high is a slow or stuck webhook.",
    "requests by code per minute": "Admission webhook requests per minute by HTTP code. Anything other than 200 is a failing webhook, which blocks writes because it fails closed.",
    "webhook latency": "Distribution of admission webhook response times. A band that moves up slows writes of ExternalSecrets and SecretStores.",
    "active workers by controller": "Reconcile workers busy now, per controller. Workers pinned at their maximum with a deep queue mean the controller is saturated.",
    "workqueue depth": "Items waiting in the operator's work queues, per queue. A depth that does not drain is a controller that cannot keep up.",
    "API calls by provider": "Calls to the secret provider's API per minute, by provider, operation and result. Failed results are the ones to read; a rise in volume is more or faster-refreshing ExternalSecrets.",
    "max concurrent: $controller": "The configured maximum concurrent reconciles of the controller. Compare with its active workers to see headroom.",
    "reconcile rate per minute: $controller": "Reconciles per minute for the controller, by result. An error series that grows is the controller failing; a flat zero is a controller that stopped.",
    "reconcile time latency: $controller": "Distribution of reconcile durations for the controller. A band that moves up is a slow provider or an overloaded operator.",
})

DESCRIPTIONS = _D

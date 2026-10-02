"""The Keycloak dashboard, authored (no upstream project ships one).

Reads the series a Keycloak install exposes on its management port once
its chart's ServiceMonitor is on (truvity/keycloak, `serviceMonitor`): the
optional source `keycloak-metrics`, so the dashboard is OFF by default and
its first panel says so where nothing is scraped.

Sources of the series (Keycloak's own guides and the Quarkus Micrometer
defaults):

  * `up`: the scrape.
  * `http_server_requests_seconds_{count,sum,bucket}`: Quarkus HTTP server
    metrics. The `_bucket` series needs the chart's `metrics.httpHistograms`.
  * `keycloak_user_events_total{realm,event,error}`: Keycloak user event
    metrics (the image is built with event-metrics-user-enabled).
  * `vendor_statistics_approximate_entries_unique{cache}`: the embedded
    Infinispan session caches.
  * `jvm_memory_used_bytes`, `jvm_memory_max_bytes`,
    `jvm_memory_usage_after_gc`: Micrometer JVM metrics.
  * `agroal_active_count`, `agroal_available_count`, `agroal_awaiting_count`:
    the database connection pool.
"""

import fleet_overview as fo

K = 'k8s_cluster_name=~"$cluster"'
S = '%s,namespace=~"$namespace",service=~"$service"' % K
NO_HIST = "no data: set the chart's metrics.httpHistograms"
SESSION_CACHES = "sessions|clientSessions|offlineSessions|offlineClientSessions"


def build():
    b = fo.Builder()
    ds = fo.DS

    b.row("Availability", 0)
    y = 1
    b.stat("Pods up",
           "Keycloak pods answering the metrics scrape. Zero means the server is down or no longer scraped: the first panel to read when sign-in fails. n/a where the Keycloak ServiceMonitor is off.",
           'count(up{%s} == 1) or vector(0)' % S,
           0, y, None, w=4, no_value="n/a", steps=[(None, fo.RED), (1, fo.GREEN)])
    b.stat("Login failure ratio (1h)",
           "Share of login attempts that failed in the last hour. Wrong passwords are user errors, so a few percent is normal; a ratio above half is a credential run, a broken identity provider or user store, or a client sending bad credentials. Compare with the failures by error below.",
           '(sum(increase(keycloak_user_events_total{%s,event="login",error!=""}[1h])) / sum(increase(keycloak_user_events_total{%s,event="login"}[1h]))) or vector(0)' % (S, S),
           4, y, None, w=4, unit="percentunit", no_value="n/a", steps=[(None, fo.GREEN), (0.2, fo.ORANGE), (0.5, fo.RED)], decimals=1)
    b.stat("5xx ratio (5m)",
           "Share of HTTP responses that were server errors over the last five minutes. Should be zero: read the server log and the database panels.",
           '(sum(rate(http_server_requests_seconds_count{%s,status=~"5.."}[5m])) / sum(rate(http_server_requests_seconds_count{%s}[5m]))) or vector(0)' % (S, S),
           8, y, None, w=4, unit="percentunit", no_value="n/a", steps=[(None, fo.GREEN), (0.01, fo.ORANGE), (0.05, fo.RED)], decimals=2)
    b.stat("Request p99 (5m)",
           "99th percentile of the request duration over five minutes. Password hashing is deliberately slow, so seconds are a warning, not an incident. " + NO_HIST + " to fill it.",
           'histogram_quantile(0.99, sum by (le) (rate(http_server_requests_seconds_bucket{%s}[5m])))' % S,
           12, y, None, w=4, unit="s", no_value=NO_HIST, steps=[(None, fo.GREEN), (1, fo.ORANGE), (2, fo.RED)], decimals=2)
    b.stat("Heap after GC (max)",
           "The fullest pod's heap still in use after its last garbage collection. A JVM rests near its maximum between collections by design; this is the value that shows real pressure. Above 90 percent an out-of-memory restart follows.",
           'max(jvm_memory_usage_after_gc{%s,area="heap"}) or vector(0)' % S,
           16, y, None, w=4, unit="percentunit", no_value="n/a", steps=[(None, fo.GREEN), (0.75, fo.ORANGE), (0.9, fo.RED)], decimals=0)
    b.stat("DB requests waiting",
           "Requests waiting for a database connection across the pods. Zero is healthy; a count that stays above zero means the pool is exhausted and requests queue behind the database.",
           'sum(agroal_awaiting_count{%s}) or vector(0)' % S,
           20, y, None, w=4, no_value="n/a")
    y += 4
    b.timeseries(
        "Pods up over time",
        "Scrape targets that answered, per pod. A gap is a restart or a pod that stopped answering; two pods dipping together is the whole install.",
        [('sum by (pod) (up{%s})' % S, "{{pod}}")],
        0, y, 24, 6, "short", None)
    y += 6

    b.row("Requests", y)
    y += 1
    b.timeseries(
        "Request rate by status",
        "HTTP requests per second by response status class. 5xx is a server fault; a wall of 4xx is clients sending bad requests or credentials.",
        [('sum by (status) (rate(http_server_requests_seconds_count{%s}[$__rate_interval]))' % S, "{{status}}")],
        0, y, 12, 8, "reqps", None, colors={"500": fo.RED, "503": fo.RED})
    b.timeseries(
        "Request duration p50, p95, p99",
        "Request duration percentiles over all endpoints. Compare the tail with the database pool and the JVM below. " + NO_HIST + " to fill it.",
        [('histogram_quantile(%s, sum by (le) (rate(http_server_requests_seconds_bucket{%s}[$__rate_interval])))' % (q, S), "p%d" % round(q * 100))
         for q in (0.5, 0.95, 0.99)],
        12, y, 12, 8, "s", None)
    y += 8
    b.timeseries(
        "Request rate by endpoint",
        "HTTP requests per second by URI template, the busiest ten. Shows what the traffic is: token, authentication, userinfo or the admin API.",
        [('topk(10, sum by (uri) (rate(http_server_requests_seconds_count{%s}[$__rate_interval])))' % S, "{{uri}}")],
        0, y, 12, 8, "reqps", None)
    b.timeseries(
        "Mean request duration by endpoint",
        "Average duration per URI template, the slowest ten (sum over count, so it needs no histogram). A slow token or login endpoint points at hashing or the database.",
        [('topk(10, sum by (uri) (rate(http_server_requests_seconds_sum{%s}[$__rate_interval])) / sum by (uri) (rate(http_server_requests_seconds_count{%s}[$__rate_interval])))' % (S, S), "{{uri}}")],
        12, y, 12, 8, "s", None)
    y += 8

    b.row("Logins", y)
    y += 1
    b.timeseries(
        "Logins by outcome",
        "Login attempts per second, successful and failed, per realm. A failure line that rises with no rise in successes is an attack or a broken integration.",
        [('sum by (realm) (rate(keycloak_user_events_total{%s,event="login",error=""}[$__rate_interval]))' % S, "{{realm}} success"),
         ('sum by (realm) (rate(keycloak_user_events_total{%s,event="login",error!=""}[$__rate_interval]))' % S, "{{realm}} failure")],
        0, y, 12, 8, "ops", None)
    b.timeseries(
        "Login failures by error",
        "Failed logins per second by Keycloak's error: invalid_user_credentials is a wrong password, user_not_found an unknown name, user_temporarily_disabled a brute-force lock. Anything else is the server or an identity provider.",
        [('sum by (error) (rate(keycloak_user_events_total{%s,event="login",error!=""}[$__rate_interval]))' % S, "{{error}}")],
        12, y, 12, 8, "ops", None)
    y += 8
    b.timeseries(
        "User events by type",
        "Every user event Keycloak counts, per second: login, logout, token exchange, refresh, registration, credential updates. Errors included.",
        [('sum by (event) (rate(keycloak_user_events_total{%s}[$__rate_interval]))' % S, "{{event}}")],
        0, y, 12, 8, "ops", None)
    b.timeseries(
        "Sessions",
        "Entries in the embedded session caches (approximate, without backup copies), summed over the pods: one per user or client session, online and offline. A climb that never falls is sessions that are not expiring.",
        [('sum by (cache) (vendor_statistics_approximate_entries_unique{%s,cache=~"%s"})' % (S, SESSION_CACHES), "{{cache}}")],
        12, y, 12, 8, "short", None)
    y += 8

    b.row("JVM", y)
    y += 1
    b.timeseries(
        "Heap used against its maximum",
        "Heap in use and the heap maximum, per pod. Used climbs to the maximum between collections by design; read the next panel for pressure.",
        [('sum by (pod) (jvm_memory_used_bytes{%s,area="heap"})' % S, "{{pod}} used"),
         ('sum by (pod) (jvm_memory_max_bytes{%s,area="heap"})' % S, "{{pod}} max")],
        0, y, 12, 8, "bytes", None)
    b.timeseries(
        "Heap after GC",
        "Share of the long-lived heap in use after the last collection, per pod. A floor that rises over days is a leak; above 90 percent the pod is about to run out.",
        [('max by (pod) (jvm_memory_usage_after_gc{%s,area="heap"})' % S, "{{pod}}")],
        12, y, 12, 8, "percentunit", None)
    y += 8

    b.row("Database pool", y)
    y += 1
    b.timeseries(
        "Connections in use and free",
        "Connections in use and available in the Agroal pool, per pod. Active at the pool's size with none available is an exhausted pool.",
        [('sum by (pod) (agroal_active_count{%s})' % S, "{{pod}} active"),
         ('sum by (pod) (agroal_available_count{%s})' % S, "{{pod}} available")],
        0, y, 12, 8, "short", None)
    b.timeseries(
        "Requests waiting for a connection",
        "Requests blocked on a database connection, per pod. Should sit at zero; a sustained value is the cause of slow logins, not a symptom.",
        [('sum by (pod) (agroal_awaiting_count{%s})' % S, "{{pod}}")],
        12, y, 12, 8, "short", None)

    def var_ds():
        return {
            "name": "datasource", "label": "datasource", "type": "datasource", "query": "prometheus",
            "current": {}, "hide": 0, "includeAll": False, "multi": False, "options": [],
            "refresh": 1, "regex": "", "skipUrlSync": False,
        }

    def query_var(name, label, q, include_all):
        v = {
            "name": name, "label": label, "type": "query",
            "datasource": ds, "definition": q, "query": {"query": q, "refId": "%s-Variable-Query" % name},
            "current": {}, "hide": 0, "includeAll": include_all, "multi": include_all, "options": [],
            "refresh": 2, "regex": "", "skipUrlSync": False, "sort": 1,
        }
        if include_all:
            v["allValue"] = ".*"
        return v

    return {
        "title": "Keycloak",
        "description": "Keycloak: availability, request rate and latency, logins, sessions, the JVM and the database pool, from the install's own metrics (truvity/keycloak serviceMonitor).",
        "editable": False,
        "graphTooltip": 1,
        "refresh": "1m",
        "schemaVersion": 39,
        "time": {"from": "now-6h", "to": "now"},
        "timezone": "",
        "annotations": {"list": []},
        "links": [],
        "tags": [],
        "templating": {"list": [
            var_ds(),
            query_var("cluster", "cluster", 'label_values(agroal_active_count, k8s_cluster_name)', False),
            query_var("namespace", "namespace", 'label_values(agroal_active_count{%s}, namespace)' % K, True),
            query_var("service", "service", 'label_values(agroal_active_count{%s,namespace=~"$namespace"}, service)' % K, True),
        ]},
        "panels": b.panels,
    }

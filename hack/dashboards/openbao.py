"""The OpenBAO dashboard, authored (no upstream project ships one for the
Prometheus names OpenBAO's server exposes).

One question per row, top to bottom: is it serving (seal and active state),
is the Raft cluster healthy, how fast are requests answered, how much is it
holding (tokens and leases), and is the audit trail being written.

Source: OpenBAO's own `/v1/sys/metrics?format=prometheus`, scraped from each
server pod by the PodMonitor charts/openbao-ops (truvity/openbao) renders
(`serverMetrics`). The server keeps the `vault_` prefix on the wire. An
OPTIONAL source (`openbao-server-metrics`): the dashboard `requires:` it and
is off by default, because only an install that runs OpenBAO has any of it.

Not read, on purpose:
  * the `namespace` label of `vault_token_count` and `vault_token_creation`:
    OpenBAO's own namespace collides with the scrape's `namespace`, so the
    series' value survives only as `exported_namespace`; the totals here sum
    over it.
  * anything that needs a token (sys/health details, policies).
"""

import fleet_overview as fo

K = 'k8s_cluster_name=~"$cluster"'
V = "vault_"


def _vm(name):
    """A series of one OpenBAO server pod, scoped to the selected cluster."""
    return "%s%s{%s}" % (V, name, K)


# A gauge the server stops updating is still exposed until its
# prometheus_retention_time passes. Autopilot, the token and lease counts are
# set only by the ACTIVE node (and a follower's applied-index delta only by a
# follower), so after a leadership change the former holder keeps exporting
# its last value for hours. Joined to vault_core_active, which every pod sets
# itself, only the pod that owns the gauge now is read.
ON_ACTIVE = " and on (namespace, pod) %s == 1" % ("%score_active{%s}" % (V, K))
ON_STANDBY = " and on (namespace, pod) %s == 0" % ("%score_active{%s}" % (V, K))


def _active(name):
    return "(%s%s)" % (_vm(name), ON_ACTIVE)


def build():
    b = fo.Builder()
    na = "n/a"

    # -- Availability ----------------------------------------------------
    b.row("Availability", 0)
    y = 1
    b.stat("Pods reporting",
           "OpenBAO server pods whose metrics are being scraped. Fewer than the StatefulSet's replicas means a pod is down or not scraped: the other panels then describe only the pods that answer.",
           "count(%s) or vector(0)" % _vm("core_unsealed"),
           0, y, None, w=4, no_value=na, steps=[(None, fo.RED), (1, fo.ORANGE), (3, fo.GREEN)])
    b.stat("Sealed pods",
           "Pods that report themselves sealed. A sealed pod serves nothing. With auto-unseal it should unseal on start: read its log for the seal's error (KMS permission, endpoint, plugin binary).",
           "count(%s == 0) or (0 * count(%s))" % (_vm("core_unsealed"), _vm("core_unsealed")),
           4, y, None, w=4, no_value=na)
    b.stat("Active nodes",
           "Pods that report themselves active. Exactly one is right: zero is no leader (every client fails), two is a split that Raft should not allow.",
           "sum(%s) or vector(0)" % _vm("core_active"),
           8, y, None, w=4, steps=[(None, fo.RED), (1, fo.GREEN), (2, fo.RED)])
    b.stat("Healthy voters",
           "Raft voters Autopilot reports healthy, read from the active node. Three of three is right; two leaves no failure to spare; fewer than two loses quorum.",
           "count(%s == 1%s) or vector(0)" % (_vm("autopilot_node_healthy"), ON_ACTIVE),
           12, y, None, w=4, steps=[(None, fo.RED), (2, fo.ORANGE), (3, fo.GREEN)])
    b.stat("Failure tolerance",
           "How many more voters can fail before quorum is lost, as Autopilot counts it. Zero means the next failure is an outage: find the unhealthy voter now.",
           "max(%s)" % _active("autopilot_failure_tolerance"),
           16, y, None, w=4, no_value=na, steps=[(None, fo.RED), (1, fo.GREEN)])
    b.stat("Audit failures (1h)",
           "Requests and responses the audit devices failed to log in the last hour. OpenBAO refuses a request it cannot audit, so a non-zero count is clients seeing errors. Read the pods' log and the audit device.",
           "(sum(increase(%s[1h])) or vector(0)) + (sum(increase(%s[1h])) or vector(0))"
           % (_vm("audit_log_request_failure"), _vm("audit_log_response_failure")),
           20, y, None, w=4)
    y += 4

    b.timeseries(
        "Unsealed, by pod",
        "1 while a pod is unsealed, 0 while it is sealed. A line at 0 is a pod serving nothing; a line that drops and returns is a restart that unsealed.",
        [(_vm("core_unsealed"), "{{pod}}")],
        0, y, 12, 7, "short", None)
    b.timeseries(
        "Active, by pod",
        "1 on the pod that is the active node, 0 on standbys. A change of line is a leadership change; a graph with no 1 anywhere is an outage.",
        [(_vm("core_active"), "{{pod}}")],
        12, y, 12, 7, "short", None)
    y += 7

    # -- Raft ------------------------------------------------------------
    b.row("Raft", y)
    y += 1
    b.timeseries(
        "Node health, by voter",
        "1 while Autopilot reports a voter healthy, read from the active node. A voter at 0 cannot be reached or has fallen behind: open its pod's log and its disk.",
        [(_active("autopilot_node_healthy"), "{{node_id}}")],
        0, y, 12, 8, "short", None)
    b.timeseries(
        "Commit index, by pod",
        "The index of the last Raft log entry each pod has committed to disk. The lines climb together; a pod whose line flattens while the others climb has stopped replicating.",
        [(_vm("raft_storage_stats_commit_index"), "{{pod}}")],
        12, y, 12, 8, "short", None)
    y += 8
    b.timeseries(
        "Applied index behind the leader, by follower",
        "How many Raft log entries each follower has applied fewer than the leader. Should hover near zero; a follower stuck high is a slow disk or network, and cannot take over cleanly. The OpenBAORaftCommitLag alert fires above 500 for ten minutes.",
        [(_vm("raft_storage_follower_applied_index_delta") + ON_STANDBY, "{{pod}}")],
        0, y, 12, 8, "short", None)
    b.timeseries(
        "Leader contact (p99), by pod",
        "Time since the leader last reached its followers when checking its lease, 99th percentile. A rise toward the election timeout is a network or load problem between pods, ahead of a leadership change.",
        [('%s%s{%s,quantile="0.99"}' % (V, "raft_leader_lastContact", K), "{{pod}}")],
        12, y, 12, 8, "ms", None)
    y += 8
    b.timeseries(
        "Leadership changes",
        "Times a pod became Raft leader, per hour. One at a roll is normal; a steady count is leadership flapping: look at leader contact and the pods' resource limits.",
        [("sum by (pod) (increase(%s[1h]))" % _vm("raft_state_leader"), "{{pod}}")],
        0, y, 12, 8, "short", None)
    b.timeseries(
        "Raft writes",
        "Raft log transactions per second: the write load on storage. A rise is more writes (leases, tokens, audit); a flat zero while clients write is a leader that is not committing.",
        [("sum(rate(%s[$__rate_interval]))" % _vm("raft_apply"), "writes/s")],
        12, y, 12, 8, "ops", None)
    y += 8

    # -- Requests --------------------------------------------------------
    b.row("Requests", y)
    y += 1
    b.timeseries(
        "Request rate, by pod",
        "Non-login requests handled per second by each pod. Standbys forward to the active node, so the active pod carries the load. A drop to zero with clients waiting is a leader that stopped answering.",
        [("sum by (pod) (rate(%s%s{%s}[$__rate_interval]))" % (V, "core_handle_request_count", K), "{{pod}}")],
        0, y, 12, 8, "reqps", None)
    b.timeseries(
        "Request latency, by pod",
        "Time to handle a non-login request: the median and the 99th percentile. A p99 above a second for long is the OpenBAOHighRequestLatency alert: suspect the Raft disk, a blocked audit device or CPU throttling.",
        [('%s%s{%s,quantile="0.5"}' % (V, "core_handle_request", K), "p50 {{pod}}"),
         ('%s%s{%s,quantile="0.99"}' % (V, "core_handle_request", K), "p99 {{pod}}")],
        12, y, 12, 8, "ms", None)
    y += 8
    b.timeseries(
        "Login rate, by pod",
        "Login requests handled per second. A step is a new workload or a retry loop; a burst of logins from one client is a credential that is not being cached.",
        [("sum by (pod) (rate(%s%s{%s}[$__rate_interval]))" % (V, "core_handle_login_request_count", K), "{{pod}}")],
        0, y, 12, 8, "reqps", None)
    b.timeseries(
        "Login latency (p99), by pod",
        "Time to handle a login request, 99th percentile. Logins that call out (a JWKS fetch, an IAM check) are slower than reads; a rise is usually the identity provider, not OpenBAO.",
        [('%s%s{%s,quantile="0.99"}' % (V, "core_handle_login_request", K), "{{pod}}")],
        12, y, 12, 8, "ms", None)
    y += 8

    # -- Tokens and leases ----------------------------------------------
    b.row("Tokens and leases", y)
    y += 1
    b.stat("Tokens",
           "Un-expired, un-revoked tokens, summed over every namespace; refreshed every ten minutes. A count that only grows is workloads that log in without revoking: check their token TTL.",
           "sum(%s) or vector(0)" % _active("token_count"),
           0, y, None, w=6, no_value=na, steps=[(None, "blue")])
    b.stat("Leases",
           "Leases eligible for eventual expiry. A steady climb is dynamic secrets that are issued and never revoked; compare with Raft writes.",
           "max(%s) or vector(0)" % _active("expire_num_leases"),
           6, y, None, w=6, no_value=na, steps=[(None, "blue")])
    b.stat("Irrevocable leases",
           "Leases OpenBAO cannot revoke by itself, because the revoke fails permanently. Should be zero; each one needs a manual look at the secrets engine that issued it.",
           "max(%s) or vector(0)" % _active("expire_num_irrevocable_leases"),
           12, y, None, w=6, no_value=na)
    b.stat("Tokens created (1h)",
           "Tokens created in the last hour. Context for the token count: a spike is a login storm or a runaway client.",
           "sum(increase(%s[1h])) or vector(0)" % _vm("token_creation"),
           18, y, None, w=6, no_value=na, steps=[(None, "blue")])
    y += 4
    b.timeseries(
        "Tokens and leases over time",
        "Tokens and leases the active node holds, over time. A sawtooth is expiry catching up; a staircase that never comes down is a leak in a client.",
        [("sum(%s)" % _active("token_count"), "tokens"),
         ("max(%s)" % _active("expire_num_leases"), "leases")],
        0, y, 12, 8, "short", None)
    b.timeseries(
        "Token creation rate",
        "Tokens created per second. A flat line is steady state; a rise with no deploy is a client logging in on every request.",
        [("sum(rate(%s[$__rate_interval]))" % _vm("token_creation"), "tokens/s")],
        12, y, 12, 8, "ops", None)
    y += 8

    # -- Audit -----------------------------------------------------------
    b.row("Audit", y)
    y += 1
    b.timeseries(
        "Audit failures, by pod",
        "Audit request and response failures per second. Should be zero. OpenBAO refuses a request it cannot audit: a non-zero line is clients seeing errors and no audit trail for them.",
        [("sum by (pod) (rate(%s[$__rate_interval]))" % _vm("audit_log_request_failure"), "request {{pod}}"),
         ("sum by (pod) (rate(%s[$__rate_interval]))" % _vm("audit_log_response_failure"), "response {{pod}}")],
        0, y, 12, 8, "ops", None, colors={})
    b.timeseries(
        "Audit latency (p99), by pod",
        "Time to write a request and a response to every audit device, 99th percentile. A device that blocks (a full log pipe) shows here before it fails.",
        [('%s%s{%s,quantile="0.99"}' % (V, "audit_log_request", K), "request {{pod}}"),
         ('%s%s{%s,quantile="0.99"}' % (V, "audit_log_response", K), "response {{pod}}")],
        12, y, 12, 8, "ms", None)

    ds = fo.DS

    def var_ds():
        return {
            "name": "datasource", "label": "datasource", "type": "datasource", "query": "prometheus",
            "current": {}, "hide": 0, "includeAll": False, "multi": False, "options": [],
            "refresh": 1, "regex": "", "skipUrlSync": False,
        }

    def var_cluster():
        q = "label_values(%score_unsealed, k8s_cluster_name)" % V
        return {
            "name": "cluster", "label": "cluster", "type": "query",
            "datasource": ds, "definition": q, "query": {"query": q, "refId": "cluster-Variable-Query"},
            "current": {}, "hide": 0, "includeAll": False, "multi": False, "options": [],
            "refresh": 2, "regex": "", "skipUrlSync": False, "sort": 1,
        }

    return {
        "title": "OpenBAO",
        "description": "OpenBAO server health: seal and active state, Raft and Autopilot, request rate and latency, tokens and leases, and the audit devices. Needs the server's metrics scraped (charts/openbao-ops serverMetrics).",
        "editable": False,
        "graphTooltip": 1,
        "refresh": "1m",
        "schemaVersion": 39,
        "time": {"from": "now-6h", "to": "now"},
        "timezone": "",
        "annotations": {"list": []},
        "links": [],
        "tags": [],
        "templating": {"list": [var_ds(), var_cluster()]},
        "panels": b.panels,
    }

"""The Fleet overview dashboard, generated (there is no upstream to fetch).

Built with hack/dashboards.py and committed as
charts/observability-dashboards/dashboards/fleet-overview.json. Written as
code rather than as hand-edited JSON so that thresholds, units, links and
descriptions are one decision made once, not forty made by hand.

Design, stated so a reviewer can check it:

  * The question is "is anything wrong, and where?", answered in ten
    seconds. The top section is fleet-wide (firing alerts by severity and
    the table that names them); below it comes ONE ROW PER CLUSTER
    (`repeat: cluster`), each a fixed grid of health tiles, so a red tile
    sits in the same place on every cluster.
  * Method: USE for resources (nodes, volumes, the store write path),
    RED-adjacent counts for workloads (pods not ready, crash loops, image
    pull failures, restarts). Every tile has ONE purpose.
  * Every tile drills down: a data link into the Kubernetes views or the
    store dashboard, carrying datasource, cluster, namespace and the time
    range across.
  * Only series a store holds today (hack/dashboards/available-metrics.yaml).
    Kargo tiles read series that exist only when the Kargo preset is on, so
    they show "n/a" when it is off and a count (possibly 0) when it is on.
  * Below the cluster rows sits one line of tiles per platform component
    (ArgoCD, cert-manager, NATS, Envoy, CloudNativePG; Kargo's are in the
    cluster row): "is it healthy?" and nothing else, each tile linking to the
    component's dashboard with datasource and cluster carried. A component a
    cluster does not run reads "n/a".
"""

DS = {"type": "prometheus", "uid": "${datasource}"}

CLUSTER = 'k8s_cluster_name=~"$cluster"'
NS = 'namespace=~"$namespace"'

# What a link carries across to the target dashboard. `:queryparam` renders
# every selected value as var-x=a&var-x=b, and an empty selection as
# nothing, so the same string serves a single value and a multi-select.
CARRY = "var-datasource=${datasource:queryparam}&${cluster:queryparam}&${namespace:queryparam}&${__url_time_range}"

# What a link to a platform component's dashboard carries: those dashboards
# are scoped by cluster, not by the Kubernetes namespace this page filters on.
CARRY_CLUSTER = "var-datasource=${datasource:queryparam}&${cluster:queryparam}&${__url_time_range}"

UID_NAMESPACES = "truvity-obs-k8s-views-namespaces"
UID_GLOBAL = "truvity-obs-k8s-views-global"
UID_VM = "truvity-obs-victoriametrics-single"
UID_ARGOCD = "truvity-obs-argocd"
UID_CERT_MANAGER = "truvity-obs-cert-manager"
UID_NATS = "truvity-obs-nats-jetstream"
UID_ENVOY = "truvity-obs-envoy-proxy"
UID_CNPG = "truvity-obs-cnpg-operator"

CNPG_JOB = "cnpg-system/cnpg-cloudnative-pg"

GREEN = "green"
RED = "red"
ORANGE = "orange"


def link(title, uid):
    return {
        "title": title,
        "url": "/d/%s?%s" % (uid, CARRY),
        "targetBlank": False,
    }


def link_cluster(title, uid):
    return {
        "title": title,
        "url": "/d/%s?%s" % (uid, CARRY_CLUSTER),
        "targetBlank": False,
    }


def _target(expr, legend="", ref="A", instant=False, fmt=None):
    t = {
        "datasource": DS,
        "editorMode": "code",
        "expr": expr,
        "legendFormat": legend,
        "range": not instant,
        "instant": instant,
        "refId": ref,
    }
    if fmt:
        t["format"] = fmt
    return t


class Builder:
    def __init__(self):
        self.panels = []
        self._id = 0

    def _next(self):
        self._id += 1
        return self._id

    def row(self, title, y, repeat=None):
        r = {
            "id": self._next(),
            "type": "row",
            "title": title,
            "collapsed": False,
            "gridPos": {"h": 1, "w": 24, "x": 0, "y": y},
            "panels": [],
        }
        if repeat:
            r["repeat"] = repeat
        self.panels.append(r)

    def stat(self, title, description, expr, x, y, link_to, unit="short", w=4, h=4,
             steps=None, no_value="0", instant=True, decimals=0):
        # `steps`: [(value|None, color)]; the default is green at 0 and red
        # from 1, right for every count of "things that should be zero".
        steps = steps or [(None, GREEN), (1, RED)]
        self.panels.append({
            "id": self._next(),
            "type": "stat",
            "title": title,
            "description": description,
            "datasource": DS,
            "gridPos": {"h": h, "w": w, "x": x, "y": y},
            "targets": [_target(expr, instant=instant)],
            "fieldConfig": {
                "defaults": {
                    "unit": unit,
                    "noValue": no_value,
                    "decimals": decimals,
                    "mappings": [],
                    "thresholds": {
                        "mode": "absolute",
                        "steps": [{"color": c, "value": v} for v, c in steps],
                    },
                    "links": [link_to] if link_to else [],
                },
                "overrides": [],
            },
            "options": {
                "colorMode": "background",
                "graphMode": "none",
                "justifyMode": "center",
                "textMode": "value",
                "orientation": "auto",
                "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
            },
        })

    def timeseries(self, title, description, targets, x, y, w, h, unit, link_to, colors=None):
        overrides = []
        for name, color in (colors or {}).items():
            overrides.append({
                "matcher": {"id": "byName", "options": name},
                "properties": [{"id": "color", "value": {"mode": "fixed", "fixedColor": color}}],
            })
        self.panels.append({
            "id": self._next(),
            "type": "timeseries",
            "title": title,
            "description": description,
            "datasource": DS,
            "gridPos": {"h": h, "w": w, "x": x, "y": y},
            "targets": [_target(e, legend=l, ref=chr(65 + i)) for i, (e, l) in enumerate(targets)],
            "fieldConfig": {
                "defaults": {
                    "unit": unit,
                    "noValue": "no data",
                    "custom": {
                        "drawStyle": "line",
                        "lineWidth": 1,
                        "fillOpacity": 10,
                        "showPoints": "never",
                        "spanNulls": False,
                    },
                    "thresholds": {"mode": "absolute", "steps": [{"color": GREEN, "value": None}]},
                    "links": [link_to] if link_to else [],
                },
                "overrides": overrides,
            },
            "options": {
                "legend": {"displayMode": "list", "placement": "bottom", "showLegend": True},
                "tooltip": {"mode": "multi", "sort": "desc"},
            },
        })

    def alerts_table(self, x, y, w, h):
        expr = 'ALERTS{alertstate="firing",%s,%s}' % (CLUSTER, NS)
        jump = {
            "title": "Open the namespace",
            "url": "/d/%s?var-datasource=${datasource:queryparam}&var-cluster=${__data.fields.Cluster}"
                   "&var-namespace=${__data.fields.Namespace}&${__url_time_range}" % UID_NAMESPACES,
            "targetBlank": False,
        }
        self.panels.append({
            "id": self._next(),
            "type": "table",
            "title": "Firing alerts",
            "description": "Every alert firing now in the selected clusters and namespaces, from the store's own ALERTS series. Start with the critical ones; the Alert cell opens the namespace's view. An alert with no namespace is about the cluster itself.",
            "datasource": DS,
            "gridPos": {"h": h, "w": w, "x": x, "y": y},
            "targets": [_target(expr, instant=True, fmt="table")],
            "transformations": [
                {"id": "filterFieldsByName",
                 "options": {"include": {"names": ["alertname", "k8s_cluster_name", "namespace", "severity"]}}},
                {"id": "organize",
                 "options": {
                     "renameByName": {"alertname": "Alert", "k8s_cluster_name": "Cluster",
                                      "namespace": "Namespace", "severity": "Severity"},
                     "indexByName": {"alertname": 0, "k8s_cluster_name": 1, "namespace": 2, "severity": 3},
                 }},
                {"id": "sortBy", "options": {"sort": [{"field": "Severity"}, {"field": "Alert"}]}},
            ],
            "fieldConfig": {
                "defaults": {
                    "noValue": "No alert is firing",
                    "custom": {"align": "auto", "cellOptions": {"type": "auto"}, "filterable": True},
                    "thresholds": {"mode": "absolute", "steps": [{"color": GREEN, "value": None}]},
                },
                "overrides": [
                    {"matcher": {"id": "byName", "options": "Alert"},
                     "properties": [{"id": "links", "value": [jump]}]},
                    {"matcher": {"id": "byName", "options": "Severity"},
                     "properties": [
                         {"id": "custom.cellOptions", "value": {"type": "color-text"}},
                         {"id": "mappings", "value": [{"type": "value", "options": {
                             "critical": {"color": RED, "index": 0},
                             "warning": {"color": ORANGE, "index": 1},
                         }}]},
                     ]},
                ],
            },
            "options": {"showHeader": True, "cellHeight": "sm"},
        })


def build():
    b = Builder()
    to_ns = link("Open the namespaces view", UID_NAMESPACES)
    to_global = link("Open the cluster view", UID_GLOBAL)
    to_store = link("Open the store dashboard", UID_VM)

    # -- Fleet-wide: what is firing, by severity, and which alerts.
    b.row("Firing alerts, all selected clusters", 0)

    def alerts(sel):
        return "count(ALERTS{alertstate=\"firing\",%s,%s,%s}) or vector(0)" % (CLUSTER, NS, sel)

    b.stat("Critical alerts",
           "Alerts firing with severity critical. Anything above zero needs a person now: open the table beside it, start with the oldest.",
           alerts('severity="critical"'), 0, 1, to_ns)
    b.stat("Warning alerts",
           "Alerts firing with severity warning. Not urgent, but a warning that stays is a critical in waiting: assign it before it escalates.",
           alerts('severity="warning"'), 4, 1, to_ns, steps=[(None, GREEN), (1, ORANGE)])
    b.stat("Other alerts",
           "Alerts firing with any other severity (info or unset). Read them for context; they never page anyone.",
           alerts('severity!~"critical|warning"'), 8, 1, to_ns, steps=[(None, GREEN), (1, "blue")])
    b.timeseries(
        "Firing alerts over time",
        "How many alerts fired, by severity, over the selected range. A step up is a new incident; a slow rise is an accumulating backlog.",
        [('sum by (severity) (ALERTS{alertstate="firing",%s,%s})' % (CLUSTER, NS), "{{severity}}")],
        0, 5, 12, 4, "short", to_ns, colors={"critical": RED, "warning": ORANGE})
    b.alerts_table(12, 1, 12, 8)

    # -- One row per cluster.
    y = 9
    b.row("$cluster", y, repeat="cluster")
    y += 1

    b.stat("Firing alerts",
           "Critical and warning alerts firing for this cluster. Above zero: read the table at the top, filtered to this cluster.",
           'count(ALERTS{alertstate="firing",%s,%s,severity=~"critical|warning"}) or vector(0)' % (CLUSTER, NS),
           0, y, to_ns)
    b.stat("Pods not Ready",
           "Pods that are Pending or Running but not Ready (Succeeded pods, such as finished Jobs, are excluded). Open the namespaces view and read Pods with unexpected status.",
           'count((kube_pod_status_ready{%s,%s,condition="false"} == 1) and on (k8s_cluster_name, namespace, pod) '
           '(kube_pod_status_phase{%s,%s,phase=~"Pending|Running"} == 1)) or vector(0)' % (CLUSTER, NS, CLUSTER, NS),
           4, y, to_ns)
    b.stat("CrashLoopBackOff",
           "Containers currently waiting in CrashLoopBackOff: the process starts and dies. Read the pod's logs and its last terminated reason in the pod view.",
           'sum(kube_pod_container_status_waiting_reason{%s,%s,reason="CrashLoopBackOff"}) or vector(0)' % (CLUSTER, NS),
           8, y, to_ns)
    b.stat("ImagePullBackOff",
           "Containers that cannot pull their image (ImagePullBackOff or ErrImagePull). Check the image name and tag, the registry, and the pull secret.",
           'sum(kube_pod_container_status_waiting_reason{%s,%s,reason=~"ImagePullBackOff|ErrImagePull"}) or vector(0)' % (CLUSTER, NS),
           12, y, to_ns)
    b.stat("Restarts (1h)",
           "Container restarts in the last hour. A handful is noise; a steady climb is a crash loop or OOM kills. Open the namespaces view and read Container Restarts.",
           'sum(increase(kube_pod_container_status_restarts_total{%s,%s}[1h])) or vector(0)' % (CLUSTER, NS),
           16, y, to_ns, steps=[(None, GREEN), (5, ORANGE), (20, RED)])
    b.stat("Nodes NotReady",
           "Nodes whose Ready condition is false or unknown. Pods on them are unreachable or about to be evicted: check the node's kubelet and the cloud instance.",
           'sum(kube_node_status_condition{%s,condition="Ready",status=~"false|unknown"}) or vector(0)' % CLUSTER,
           20, y, to_global)
    y += 4

    b.stat("Nodes under pressure",
           "Nodes reporting memory, disk or PID pressure. The kubelet starts evicting pods under pressure: find the noisy workload in the cluster view, or add capacity.",
           'sum(kube_node_status_condition{%s,condition=~"MemoryPressure|DiskPressure|PIDPressure",status="true"}) or vector(0)' % CLUSTER,
           0, y, to_global)
    b.stat("PVCs over 85% full",
           "Persistent volumes whose used bytes exceed 85% of capacity. Expand the claim or clean up before it fills; open the namespaces view for the Persistent Volumes panels.",
           'count((kubelet_volume_stats_used_bytes{%s,%s} / kubelet_volume_stats_capacity_bytes{%s,%s}) > 0.85) or vector(0)'
           % (CLUSTER, NS, CLUSTER, NS),
           4, y, to_ns)
    b.stat("Rows dropped or ignored (1h)",
           "Rows the metrics, logs and traces stores refused in the last hour (ignored or dropped for a reason, such as too many labels or a timestamp out of range). Above zero means data is being lost at the write path: open the store dashboard and read the reason.",
           '(sum(increase(vm_rows_ignored_total{%s}[1h])) or vector(0)) + (sum(increase(vl_rows_dropped_total{%s}[1h])) or vector(0)) '
           '+ (sum(increase(vt_rows_dropped_total{%s}[1h])) or vector(0))' % (CLUSTER, CLUSTER, CLUSTER),
           8, y, to_store)
    b.stat("Kargo stages not healthy",
           "Kargo Stages whose Ready or Healthy condition is false or unknown. Shows n/a where the Kargo state metrics are not enabled. Open the Stage in Kargo and read its conditions.",
           'count(count by (k8s_cluster_name, namespace, stage) (kargo_stage_condition{%s,type=~"Ready|Healthy"} == 0)) '
           'or (0 * count(kargo_stage_condition{%s}))' % (CLUSTER, CLUSTER),
           12, y, to_ns, no_value="n/a")
    b.stat("Kargo promotions errored",
           "Errored Promotions whose Stage has not recovered (the same condition the KargoPromotionErrored alert uses). Shows n/a where the Kargo state metrics are not enabled. Re-promote once the cause is fixed.",
           'count((kargo_promotion_phase{%s,phase="Errored"} == 1) and on (k8s_cluster_name, namespace, stage) '
           '(kargo_stage_condition{%s,type="Ready",reason="LastPromotionErrored"} == 0)) '
           'or (0 * count(kargo_promotion_phase{%s}))' % (CLUSTER, CLUSTER, CLUSTER),
           16, y, to_ns, no_value="n/a")
    b.stat("Pods running",
           "Pods in the Running phase, for scale. It is context, not an alarm: a sudden drop with no deploy is worth a look.",
           'sum(kube_pod_status_phase{%s,%s,phase="Running"}) or vector(0)' % (CLUSTER, NS),
           20, y, to_global, steps=[(None, "blue")])
    y += 4

    b.timeseries(
        "Rows ingested per second, by store",
        "The store write path: rows per second accepted by the metrics (vm_), logs (vl_) and traces (vt_) components of this cluster. A flat zero on a store that had traffic means the write path is dead; the WritePathDead alert watches the same counters.",
        [('sum by (job) (rate(vm_rows_inserted_total{%s}[$__rate_interval]))' % CLUSTER, "metrics {{job}}"),
         ('sum by (job) (rate(vl_rows_ingested_total{%s}[$__rate_interval]))' % CLUSTER, "logs {{job}}"),
         ('sum by (job) (rate(vt_rows_ingested_total{%s}[$__rate_interval]))' % CLUSTER, "traces {{job}}")],
        0, y, 12, 7, "ops", to_store)
    b.timeseries(
        "Rows dropped or ignored per second, by store",
        "Rows the stores refused, by reason. Should be flat at zero. A rise means data loss at ingest: read the reason label, then fix the sender (label count, timestamps) or the store limit.",
        [('sum by (job, reason) (rate(vm_rows_ignored_total{%s}[$__rate_interval]))' % CLUSTER, "metrics {{job}} {{reason}}"),
         ('sum by (job, reason) (rate(vl_rows_dropped_total{%s}[$__rate_interval]))' % CLUSTER, "logs {{job}} {{reason}}"),
         ('sum by (job, reason) (rate(vt_rows_dropped_total{%s}[$__rate_interval]))' % CLUSTER, "traces {{job}} {{reason}}")],
        12, y, 12, 7, "ops", to_store, colors={})

    y += 7

    # -- Platform components: is each one healthy? One line of tiles per
    # component, the same on every cluster. A component a cluster does not run
    # reads "n/a", never a green 0: the fallback multiplies an always-present
    # series of the component by zero, so it is absent exactly when the
    # component is.
    b.row("Platform components, $cluster", y, repeat="cluster")
    y += 1
    K = CLUSTER

    def line(components, y):
        # components: [(title, description, bad, anchor, kwargs)]
        w = 24 // len(components)
        for i, (title, desc, bad, anchor, kw) in enumerate(components):
            expr = "(%s) or (0 * count(%s))" % (bad, anchor)
            b.stat(title, desc, expr, i * w, y, kw.pop("link"), w=w, h=3, no_value="n/a", **kw)

    to_argocd = link_cluster("Open the ArgoCD dashboard", UID_ARGOCD)
    to_certs = link_cluster("Open the cert-manager dashboard", UID_CERT_MANAGER)
    to_nats = link_cluster("Open the NATS JetStream dashboard", UID_NATS)
    to_envoy = link_cluster("Open the Envoy proxy dashboard", UID_ENVOY)
    to_cnpg = link_cluster("Open the CloudNativePG operator dashboard", UID_CNPG)

    argo_up = 'up{%s,job=~"argocd-.*-metrics"}' % K
    line([
        ("ArgoCD apps not Synced",
         "Applications whose sync status is anything but Synced: Git and the cluster disagree. Open the ArgoCD dashboard, then the application, and read the sync result. n/a where ArgoCD is not scraped.",
         'count(argocd_app_info{%s,sync_status!="Synced"})' % K, 'argocd_app_info{%s}' % K, {"link": to_argocd}),
        ("ArgoCD apps not Healthy",
         "Applications whose health is anything but Healthy (Progressing, Degraded, Missing, Suspended, Unknown). A few Progressing during a rollout is normal; one that stays is not. n/a where ArgoCD is not scraped.",
         'count(argocd_app_info{%s,health_status!="Healthy"})' % K, 'argocd_app_info{%s}' % K, {"link": to_argocd}),
        ("ArgoCD targets down",
         "ArgoCD metrics endpoints (application controller, API server, repo server) that stopped answering. Any down leaves that part of ArgoCD unobserved, and the two tiles beside it stale.",
         'count(%s == 0)' % argo_up, argo_up, {"link": to_argocd}),
    ], y)
    y += 3

    cm_exp = 'certmanager_certificate_expiration_timestamp_seconds{%s}' % K
    cm_up = 'up{%s,job=~"cert-manager|cainjector"}' % K
    line([
        ("Certificates expiring in 14 days",
         "Certificates that expire within 14 days (certificates with no expiry yet, still being issued, are not counted). One that has not renewed means its issuer or challenge is failing: open the cert-manager dashboard and read the Ready condition. n/a where cert-manager is not scraped.",
         'count((%s > 0) and (%s - time() < 1209600))' % (cm_exp, cm_exp), cm_exp, {"link": to_certs}),
        ("Certificates not Ready",
         "Certificates whose Ready condition is false or unknown. A new certificate is not Ready until issued; one that stays so is stuck. Open the cert-manager dashboard's Certificates table.",
         'count(certmanager_certificate_ready_status{%s,condition!="True"} == 1)' % K,
         'certmanager_certificate_ready_status{%s}' % K, {"link": to_certs}),
        ("cert-manager targets down",
         "cert-manager metrics endpoints (controller, cainjector) that stopped answering. Certificates are not being renewed while the controller is down.",
         'count(%s == 0)' % cm_up, cm_up, {"link": to_certs}),
    ], y)
    y += 3

    nats_up = 'up{%s,job="nats/nats"}' % K
    line([
        ("NATS servers down",
         "NATS metrics endpoints that stopped answering: the server or its exporter is down. Publishers and consumers on that server are affected. n/a where NATS is not scraped.",
         'count(%s == 0)' % nats_up, nats_up, {"link": to_nats}),
        ("JetStream disabled",
         "NATS servers reporting JetStream as disabled. Streams and durable consumers need it: a server that should have it and does not is misconfigured or restarted without its store.",
         'count(jetstream_server_jetstream_disabled{%s} == 1)' % K, 'jetstream_server_jetstream_disabled{%s}' % K, {"link": to_nats}),
        ("NATS slow consumers (1h)",
         "Slow consumers the servers reported in the last hour: clients that could not keep up and were disconnected or lost messages. Find the client and raise its limit or speed it up.",
         'sum(increase(gnatsd_varz_slow_consumers{%s}[1h]))' % K, 'gnatsd_varz_slow_consumers{%s}' % K, {"link": to_nats}),
    ], y)
    y += 3

    envoy_total = 'envoy_http_downstream_rq_total{%s}' % K
    envoy_up = 'up{%s,job="envoy-gateway-system/envoy-proxy"}' % K
    envoy_5xx = 'sum(rate(envoy_http_downstream_rq_xx{%s,envoy_response_code_class="5"}[5m]))' % K
    line([
        ("Envoy 5xx per second",
         "Requests per second the proxies answered with a 5xx, measured at the client side. Near zero is healthy; open the Envoy proxy dashboard, then the Envoy clusters dashboard to find the failing backend. n/a where the proxies are not scraped.",
         envoy_5xx, envoy_total,
         {"link": to_envoy, "unit": "reqps", "decimals": 2, "steps": [(None, GREEN), (0.05, ORANGE), (1, RED)]}),
        ("Envoy 5xx share of requests",
         "Share of client requests the proxies answered with a 5xx, over five minutes. Above 1% is worth a look; above 5% is an outage for some route.",
         '100 * %s / (sum(rate(envoy_http_downstream_rq_total{%s}[5m])) > 0)' % (envoy_5xx, K), envoy_total,
         {"link": to_envoy, "unit": "percent", "decimals": 1, "steps": [(None, GREEN), (1, ORANGE), (5, RED)]}),
        ("Envoy proxies down",
         "Envoy proxy metrics endpoints that stopped answering. A proxy pod that is down serves no traffic; check the Gateway's pods.",
         'count(%s == 0)' % envoy_up, envoy_up, {"link": to_envoy}),
    ], y)
    y += 3

    cnpg_sel = '%s,job="%s"' % (K, CNPG_JOB)
    cnpg_up = 'up{%s}' % cnpg_sel
    line([
        ("CNPG operator reconcile errors (1h)",
         "Reconciles that ended in an error in the last hour, across the CloudNativePG operator's control loops. A few are retried conflicts; a steady count means a Postgres cluster, backup or pooler cannot converge. n/a where the operator is not scraped.",
         'sum(increase(controller_runtime_reconcile_total{%s,result="error"}[1h]))' % cnpg_sel,
         'controller_runtime_reconcile_total{%s}' % cnpg_sel,
         {"link": to_cnpg, "steps": [(None, GREEN), (1, ORANGE), (20, RED)]}),
        ("CNPG operator down",
         "CloudNativePG operator metrics endpoints that stopped answering. With the operator down no Postgres cluster is reconciled: failovers and backups are not driven.",
         'count(%s == 0)' % cnpg_up, cnpg_up, {"link": to_cnpg}),
    ], y)

    def var_ds():
        return {
            "name": "datasource", "label": "datasource", "type": "datasource", "query": "prometheus",
            "current": {}, "hide": 0, "includeAll": False, "multi": False, "options": [],
            "refresh": 1, "regex": "", "skipUrlSync": False,
        }

    def var_cluster():
        q = "label_values(up, k8s_cluster_name)"
        return {
            "name": "cluster", "label": "cluster", "type": "query",
            "datasource": DS, "definition": q, "query": {"query": q, "refId": "cluster-Variable-Query"},
            "current": {}, "hide": 0, "includeAll": True, "allValue": ".*", "multi": True, "options": [],
            "refresh": 2, "regex": "", "skipUrlSync": False, "sort": 1,
        }

    def var_namespace():
        q = 'label_values(kube_pod_info{%s}, namespace)' % CLUSTER
        return {
            "name": "namespace", "label": "namespace", "type": "query",
            "datasource": DS, "definition": q, "query": {"query": q, "refId": "namespace-Variable-Query"},
            "current": {}, "hide": 0, "includeAll": True, "allValue": ".*", "multi": True, "options": [],
            "refresh": 2, "regex": "", "skipUrlSync": False, "sort": 1,
        }

    return {
        "title": "Fleet overview",
        "description": "Is anything wrong, and where? Firing alerts, then one row of health tiles per cluster and one line of tiles per platform component. Every tile opens the drill-down view for that cluster and namespace, or the component's dashboard.",
        "editable": False,
        "graphTooltip": 1,
        "refresh": "1m",
        "schemaVersion": 39,
        "time": {"from": "now-6h", "to": "now"},
        "timezone": "",
        "annotations": {"list": []},
        "links": [],
        "tags": [],
        "templating": {"list": [var_ds(), var_cluster(), var_namespace()]},
        "panels": b.panels,
    }

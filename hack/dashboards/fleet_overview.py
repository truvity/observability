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
"""

DS = {"type": "prometheus", "uid": "${datasource}"}

CLUSTER = 'k8s_cluster_name=~"$cluster"'
NS = 'namespace=~"$namespace"'

# What a link carries across to the target dashboard. `:queryparam` renders
# every selected value as var-x=a&var-x=b, and an empty selection as
# nothing, so the same string serves a single value and a multi-select.
CARRY = "var-datasource=${datasource:queryparam}&${cluster:queryparam}&${namespace:queryparam}&${__url_time_range}"

UID_NAMESPACES = "truvity-obs-k8s-views-namespaces"
UID_GLOBAL = "truvity-obs-k8s-views-global"
UID_VM = "truvity-obs-victoriametrics-single"

GREEN = "green"
RED = "red"
ORANGE = "orange"


def link(title, uid):
    return {
        "title": title,
        "url": "/d/%s?%s" % (uid, CARRY),
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
             steps=None, no_value="0", instant=True):
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
                    "decimals": 0,
                    "mappings": [],
                    "thresholds": {
                        "mode": "absolute",
                        "steps": [{"color": c, "value": v} for v, c in steps],
                    },
                    "links": [link_to],
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
                    "links": [link_to],
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
        "description": "Is anything wrong, and where? Firing alerts, then one row of health tiles per cluster. Every tile opens the drill-down view for that cluster and namespace.",
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

"""The Kargo dashboard, authored (no upstream project ships one).

Two halves, kept apart because they come from two sources:

  * Stages and Promotions, from the kube-state-metrics custom-resource
    preset (`kargo_stage_condition`, `kargo_promotion_phase`). Only where
    that preset is on; the tiles read "n/a" where it is off.
  * The controller, from its own metrics endpoint: the controller-runtime
    reconcile and work-queue series every Kargo controller reports.

Kargo's own `kargo_promotions_*` and `kargo_stages_by_ready_reason` series
are deliberately not read: they exist from v1.12, and this dashboard is
written to work on the release before it too.
"""

import fleet_overview as fo

J = "kargo-controller-metrics"
K = 'k8s_cluster_name=~"$cluster"'
S = '%s,namespace=~"$namespace"' % K  # stage and promotion series carry the Project's namespace

UID_NAMESPACES = fo.UID_NAMESPACES
_CARRY = "var-datasource=${datasource:queryparam}&${cluster:queryparam}&${__url_time_range}"


def _nolink():
    return None


def build():
    b = fo.Builder()
    to_ns = fo.link("Open the namespaces view", UID_NAMESPACES)

    b.row("Stages and Promotions (needs the Kargo state-metrics preset)", 0)
    y = 1
    na = "n/a"
    b.stat("Stages Ready",
           "Stages whose Ready condition is true: healthy and verified. Reads n/a where the Kargo state metrics are not enabled.",
           'count(count by (namespace, stage) (kargo_stage_condition{%s,type="Ready"} == 1)) or (0 * count(kargo_stage_condition{%s}))' % (S, K),
           0, y, to_ns, w=4, no_value=na, steps=[(None, "blue")])
    b.stat("Stages not Ready",
           "Stages whose Ready condition is false or unknown. Open the table beside it: the reason says whether a Promotion failed, the Stage is unhealthy or its Freight is unverified.",
           'count(count by (namespace, stage) (kargo_stage_condition{%s,type="Ready"} == 0)) or (0 * count(kargo_stage_condition{%s}))' % (S, K),
           4, y, to_ns, w=4, no_value=na)
    b.stat("Stages not Healthy",
           "Stages whose Healthy condition is false or unknown: the last Promotion left something that fails its health check. Read the Stage's conditions in Kargo.",
           'count(count by (namespace, stage) (kargo_stage_condition{%s,type="Healthy"} == 0)) or (0 * count(kargo_stage_condition{%s}))' % (S, K),
           8, y, to_ns, w=4, no_value=na)
    b.stat("Promotions Running",
           "Promotions in the Running phase now. A number that stays above zero for long is a stuck Promotion: open it in Kargo and read its steps.",
           'count(kargo_promotion_phase{%s,phase="Running"} == 1) or (0 * count(kargo_promotion_phase{%s}))' % (S, K),
           12, y, to_ns, w=4, no_value=na, steps=[(None, "blue")])
    b.stat("Promotions errored",
           "Errored Promotions whose Stage has not recovered (the condition the KargoPromotionErrored alert uses). Re-promote once the cause is fixed.",
           'count((kargo_promotion_phase{%s,phase="Errored"} == 1) and on (k8s_cluster_name, namespace, stage) '
           '(kargo_stage_condition{%s,type="Ready",reason="LastPromotionErrored"} == 0)) '
           'or (0 * count(kargo_promotion_phase{%s}))' % (S, S, K),
           16, y, to_ns, w=4, no_value=na)
    b.stat("Promotions failed",
           "Promotions that finished in the Failed phase and are still in the cluster. Failed is terminal: read the failed step and fix the cause before promoting again.",
           'count(kargo_promotion_phase{%s,phase="Failed"} == 1) or (0 * count(kargo_promotion_phase{%s}))' % (S, K),
           20, y, to_ns, w=4, no_value=na, steps=[(None, "green"), (1, "orange")])
    y += 4

    b.timeseries(
        "Promotions by phase",
        "How many Promotions are in each phase, over time, counting the Promotion objects still in the cluster. A rise in Failed or Errored is a delivery that broke; Running that never drains is a stuck Promotion.",
        [('sum by (phase) (kargo_promotion_phase{%s})' % S, "{{phase}}")],
        0, y, 12, 8, "short", None, colors={"Errored": fo.RED, "Failed": fo.ORANGE, "Succeeded": fo.GREEN})
    b.timeseries(
        "Stages by Ready reason",
        "How many Stages sit in each Ready state, by the reason Kargo reports. Verified is the good state; a growing LastPromotionFailed, Unhealthy or VerificationFailed is a pipeline that stopped delivering.",
        [('count by (reason) (kargo_stage_condition{%s,type="Ready"})' % S, "{{reason}}")],
        12, y, 12, 8, "short", None)
    y += 8

    b.panels.append(_stages_table(b, 0, y, 24, 8))
    y += 8

    b.row("Controller", y)
    y += 1
    b.stat("Controller targets up",
           "Kargo controller metrics endpoints answering. Zero means the controller is down or not scraped: nothing is promoting, and the reconcile panels below are empty.",
           'count(up{%s,job="%s"} == 1) or vector(0)' % (K, J),
           0, y, None, w=4, steps=[(None, fo.RED), (1, fo.GREEN)])
    b.stat("Reconcile errors (1h)",
           "Reconciles that ended in an error in the last hour, summed over the controller's loops. A handful is retried noise; a steady count means a loop cannot make progress: read the controller's log for the same window.",
           'sum(increase(controller_runtime_reconcile_total{%s,job="%s",result="error"}[1h])) or vector(0)' % (K, J),
           4, y, None, w=4, steps=[(None, fo.GREEN), (1, fo.ORANGE), (20, fo.RED)])
    b.stat("Work queue depth",
           "Items waiting across the controller's work queues now. It should drain to a few; a depth that only grows means the controller cannot keep up.",
           'sum(workqueue_depth{%s,job="%s"}) or vector(0)' % (K, J),
           8, y, None, w=4, steps=[(None, fo.GREEN), (50, fo.ORANGE), (200, fo.RED)])
    b.stat("Controller memory",
           "Resident memory of the Kargo controller. A steady climb over days is a leak; a jump on deploy is a new cache.",
           'sum(process_resident_memory_bytes{%s,job="%s"}) or vector(0)' % (K, J),
           12, y, None, w=4, unit="bytes", steps=[(None, "blue")])
    b.stat("Controller goroutines",
           "Goroutines in the Kargo controller. Context for a memory climb: a count that only grows is a leak.",
           'sum(go_goroutines{%s,job="%s"}) or vector(0)' % (K, J),
           16, y, None, w=4, steps=[(None, "blue")])
    b.stat("Controller CPU",
           "CPU cores used by the Kargo controller. Sustained near its limit means reconciles queue behind it.",
           'sum(rate(process_cpu_seconds_total{%s,job="%s"}[$__rate_interval])) or vector(0)' % (K, J),
           20, y, None, w=4, unit="short", steps=[(None, "blue")], decimals=2, instant=False)
    y += 4
    b.timeseries(
        "Reconciles per second, by controller",
        "Reconcile rate per Kargo control loop (stage, promotion, warehouse and so on). A loop at zero that should be busy has stopped; a loop far above its neighbours is churning.",
        [('sum by (controller) (rate(controller_runtime_reconcile_total{%s,job="%s"}[$__rate_interval]))' % (K, J), "{{controller}}")],
        0, y, 12, 8, "ops", None)
    b.timeseries(
        "Reconcile errors per second, by controller",
        "Reconciles that returned an error, per control loop. Should sit at zero. A loop with a steady error rate cannot converge: read the controller's log for that loop.",
        [('sum by (controller) (rate(controller_runtime_reconcile_total{%s,job="%s",result="error"}[$__rate_interval]))' % (K, J), "{{controller}}")],
        12, y, 12, 8, "ops", None, colors={})
    y += 8
    b.timeseries(
        "Reconcile duration p95, by controller",
        "The 95th-percentile time a reconcile takes, per control loop. A loop that slows down delays every Promotion behind it; compare with the work queue depth.",
        [('histogram_quantile(0.95, sum by (le, controller) (rate(controller_runtime_reconcile_time_seconds_bucket{%s,job="%s"}[$__rate_interval])))' % (K, J), "{{controller}}")],
        0, y, 12, 8, "s", None)
    b.timeseries(
        "Work queue depth, by queue",
        "Items waiting in each work queue. A queue that does not drain is a loop that cannot keep up with what is asked of it.",
        [('sum by (name) (workqueue_depth{%s,job="%s"})' % (K, J), "{{name}}")],
        12, y, 12, 8, "short", None)
    y += 8
    b.timeseries(
        "Work queue wait p95, by queue",
        "How long items wait in a queue before a worker takes them, 95th percentile. Rising wait with flat reconcile duration means too few workers for the load.",
        [('histogram_quantile(0.95, sum by (le, name) (rate(workqueue_queue_duration_seconds_bucket{%s,job="%s"}[$__rate_interval])))' % (K, J), "{{name}}")],
        0, y, 12, 8, "s", None)
    b.timeseries(
        "Controller memory and goroutines",
        "Resident memory (bytes) and goroutines of the controller over time. A sawtooth is normal garbage collection; a staircase that never comes back down is a leak.",
        [('sum(process_resident_memory_bytes{%s,job="%s"})' % (K, J), "memory"),
         ('sum(go_goroutines{%s,job="%s"})' % (K, J), "goroutines")],
        12, y, 12, 8, "short", None)

    ds = fo.DS

    def var_ds():
        return {
            "name": "datasource", "label": "datasource", "type": "datasource", "query": "prometheus",
            "current": {}, "hide": 0, "includeAll": False, "multi": False, "options": [],
            "refresh": 1, "regex": "", "skipUrlSync": False,
        }

    def var_cluster():
        q = 'label_values(up{job="%s"}, k8s_cluster_name)' % J
        return {
            "name": "cluster", "label": "cluster", "type": "query",
            "datasource": ds, "definition": q, "query": {"query": q, "refId": "cluster-Variable-Query"},
            "current": {}, "hide": 0, "includeAll": False, "multi": False, "options": [],
            "refresh": 2, "regex": "", "skipUrlSync": False, "sort": 1,
        }

    def var_namespace():
        q = 'label_values(kargo_stage_condition{%s}, namespace)' % K
        return {
            "name": "namespace", "label": "project namespace", "type": "query",
            "datasource": ds, "definition": q, "query": {"query": q, "refId": "namespace-Variable-Query"},
            "current": {}, "hide": 0, "includeAll": True, "allValue": ".*", "multi": True, "options": [],
            "refresh": 2, "regex": "", "skipUrlSync": False, "sort": 1,
        }

    return {
        "title": "Kargo",
        "description": "Kargo delivery: Stages and Promotions from the state-metrics preset, and the controller's own reconcile and work-queue health.",
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


def _stages_table(b, x, y, w, h):
    expr = 'kargo_stage_condition{%s,type="Ready"} == 0' % S
    return {
        "id": b._next(),
        "type": "table",
        "title": "Stages not Ready",
        "description": "Every Stage whose Ready condition is false or unknown, with the reason Kargo gives. Start with LastPromotionFailed and LastPromotionErrored: the Promotion did not deliver.",
        "datasource": fo.DS,
        "gridPos": {"h": h, "w": w, "x": x, "y": y},
        "targets": [fo._target(expr, instant=True, fmt="table")],
        "transformations": [
            {"id": "filterFieldsByName", "options": {"include": {"names": ["namespace", "stage", "reason"]}}},
            {"id": "organize", "options": {
                "renameByName": {"namespace": "Project", "stage": "Stage", "reason": "Reason"},
                "indexByName": {"namespace": 0, "stage": 1, "reason": 2}}},
            {"id": "sortBy", "options": {"sort": [{"field": "Project"}, {"field": "Stage"}]}},
        ],
        "fieldConfig": {
            "defaults": {
                "noValue": "Every Stage is Ready",
                "custom": {"align": "auto", "cellOptions": {"type": "auto"}, "filterable": True},
                "thresholds": {"mode": "absolute", "steps": [{"color": fo.GREEN, "value": None}]},
            },
            "overrides": [],
        },
        "options": {"showHeader": True, "cellHeight": "sm"},
    }

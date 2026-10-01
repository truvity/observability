"""Panels authored onto an adapted dashboard where the upstream leaves a gap.

CloudNativePG's dashboard reads the operator through two cumulative error
counters; a rate, a duration and a queue depth say more about whether the
operator is keeping up. They read the controller-runtime series every
operator reports, on the operator's own scrape job.

The instance dashboard (an optional source, `cnpg-instance-metrics`) gets a
banner stat that says whether any instance reports at all: "no data" there
means the source is not scraped, which a blank panel would not say.
"""

import fleet_overview as fo

CNPG_JOB = "cnpg-system/cnpg-cloudnative-pg"
K = 'k8s_cluster_name=~"$cluster"'


def extras(name):
    if name == "cnpg-cluster":
        b = fo.Builder()
        sel = '%s,namespace=~"$namespace",cluster=~"$pgcluster"' % K
        b.stat(
            "Postgres instances reporting",
            "Postgres instances whose exporter answers, in the selected namespace and Postgres cluster. "
            "'no data: instance metrics not scraped' means the CloudNativePG instance source is off here: "
            "enable the Postgres cluster chart's PodMonitor, and every panel below stays empty until then. "
            "A number below the instance count means an instance is down or its exporter is failing.",
            'count(cnpg_collector_up{%s} == 1)' % sel,
            0, -1000, None, w=24, h=3, no_value="no data: instance metrics not scraped",
            steps=[(None, "red"), (1, "green")])
        return b.panels
    if name != "cnpg-operator":
        return []
    b = fo.Builder()
    sel = '%s,job="%s",namespace=~"$namespace"' % (K, CNPG_JOB)
    y = 200  # below everything upstream lays out; the adapter re-stacks the band
    b.timeseries(
        "Reconciles per second, by controller",
        "Reconcile rate per operator control loop (cluster, backup, pooler, scheduled backup), split by result. A loop that stops reconciling while clusters exist is a stuck operator.",
        [('sum by (controller, result) (rate(controller_runtime_reconcile_total{%s}[$__rate_interval]))' % sel, "{{controller}} {{result}}")],
        0, y, 8, 7, "ops", None)
    b.timeseries(
        "Reconcile duration p95, by controller",
        "The 95th-percentile time a reconcile takes, per control loop. A loop that slows down delays every change to a Postgres cluster behind it.",
        [('histogram_quantile(0.95, sum by (le, controller) (rate(controller_runtime_reconcile_time_seconds_bucket{%s}[$__rate_interval])))' % sel, "{{controller}}")],
        8, y, 8, 7, "s", None)
    b.timeseries(
        "Work queue depth, by queue",
        "Items waiting in the operator's work queues. A queue that does not drain is a control loop that cannot keep up.",
        [('sum by (name) (workqueue_depth{%s})' % sel, "{{name}}")],
        16, y, 8, 7, "short", None)
    return b.panels

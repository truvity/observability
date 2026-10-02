#!/usr/bin/env python3
"""The two Frontend dashboards, authored (no upstream project ships them).

  charts/observability-rum/dashboards/frontend-issues.json
  charts/observability-rum/dashboards/frontend-overview.json

Run `python3 hack/dashboards/frontend.py` after editing; the output is
committed, and `just dashboard-lint` holds it to the contract in
docs/dashboards.md. They read the LOG store, through Grafana's
VictoriaLogs datasource, from what charts/observability-rum's pipeline
writes: resource attributes `telemetry.source`, `app`, `k8s.cluster.name`;
record attributes `kind`, `type`, `session_id`, `error.fingerprint`,
`error.message`, `value_lcp`, `value_inp`, `value_cls`, `page_url`,
`app_release` and the structured `trace_id`.

Two placeholders are replaced when the chart renders (templates/dashboards.yaml):
__LOGS_DATASOURCE_UID__ and __TRACES_DATASOURCE_UID__ -- the UIDs the
consuming Grafana was provisioned with.

Query types of the datasource, and why each panel uses the one it does:
  stats / statsRange   numeric, Prometheus-shaped frames: stat and time series
  instant              a table of rows whose columns are all strings (a
                       `stats` pipe is fine); the dashboard expands them with
                       `extractFields` and converts the numeric ones
The dashboard's time range is the query's time range; no `_time:` filter is
written into an expression.
"""

import json
import pathlib
import urllib.parse

OUT = pathlib.Path(__file__).resolve().parent.parent.parent / "charts" / "observability-rum" / "dashboards"

DS_TYPE = "victoriametrics-logs-datasource"
DS = {"type": DS_TYPE, "uid": "$datasource"}

# Every query starts from this: RUM rows, of the chosen cluster and app.
SCOPE = 'telemetry.source:faro k8s.cluster.name:"$cluster" app:$app'
CARRY = "var-datasource=${datasource:queryparam}&var-cluster=${cluster:queryparam}&var-app=${app:queryparam}&${__url_time_range}"

TRACE_URL = (
    "/explore?schemaVersion=1&orgId=1&panes="
    + urllib.parse.quote(
        json.dumps(
            {
                "a": {
                    "datasource": "__TRACES_DATASOURCE_UID__",
                    "queries": [{"refId": "A", "datasource": {"uid": "__TRACES_DATASOURCE_UID__"}, "query": "__TRACE__"}],
                    "range": {"from": "now-7d", "to": "now"},
                }
            },
            separators=(",", ":"),
        ),
        safe="",
    ).replace("__TRACE__", "${__value.raw}")
)


def thresholds(steps):
    return {"mode": "absolute", "steps": [{"color": c, "value": v} for v, c in steps]}


class Dash:
    def __init__(self, uid, title, tags):
        self.uid, self.title, self.tags = uid, title, tags
        self.panels = []
        self.next_id = 1
        self.vars = []

    def _id(self):
        i = self.next_id
        self.next_id += 1
        return i

    def variables(self, extra=()):
        self.vars = [
            {
                "name": "datasource",
                "label": "datasource",
                "type": "datasource",
                "query": DS_TYPE,
                "current": {"selected": False, "text": "__LOGS_DATASOURCE_UID__", "value": "__LOGS_DATASOURCE_UID__"},
                "hide": 0,
                "includeAll": False,
                "multi": False,
                "options": [],
                "refresh": 1,
                "regex": "",
            },
            {
                "name": "cluster",
                "label": "cluster",
                "type": "query",
                "datasource": DS,
                # The logs datasource's own "field values" query: what the
                # chosen store holds for k8s.cluster.name, never a fixed list.
                "query": {"type": "fieldValue", "field": "k8s.cluster.name", "query": "telemetry.source:faro", "limit": 100},
                "definition": "field values of k8s.cluster.name",
                "current": {},
                "hide": 0,
                "includeAll": False,
                "multi": False,
                "options": [],
                "refresh": 2,
                "regex": "",
                "sort": 1,
            },
            {
                "name": "app",
                "label": "app",
                "type": "query",
                "datasource": DS,
                "query": {"type": "fieldValue", "field": "app", "query": 'telemetry.source:faro k8s.cluster.name:"$cluster"', "limit": 100},
                "definition": "field values of app",
                "current": {"selected": True, "text": "All", "value": "$__all"},
                "hide": 0,
                "includeAll": True,
                # LogsQL: any non-empty value.
                "allValue": "*",
                "multi": False,
                "options": [],
                "refresh": 2,
                "regex": "",
                "sort": 1,
            },
        ] + list(extra)

    def row(self, title, y):
        self.panels.append(
            {"id": self._id(), "type": "row", "title": title, "collapsed": False, "gridPos": {"h": 1, "w": 24, "x": 0, "y": y}, "panels": []}
        )

    def target(self, expr, query_type, ref="A", **kw):
        t = {"refId": ref, "datasource": DS, "expr": expr, "queryType": query_type, "editorMode": "code"}
        t.update(kw)
        return t

    def stat(self, title, desc, expr, x, y, w=6, unit="short", steps=((None, "green"),), decimals=None, link=None):
        d = {"unit": unit, "thresholds": thresholds(steps), "color": {"mode": "thresholds"}, "mappings": []}
        if decimals is not None:
            d["decimals"] = decimals
        if link:
            d["links"] = [link]
        self.panels.append(
            {
                "id": self._id(),
                "type": "stat",
                "title": title,
                "description": desc,
                "datasource": DS,
                "gridPos": {"h": 4, "w": w, "x": x, "y": y},
                "fieldConfig": {"defaults": d, "overrides": []},
                "options": {
                    "colorMode": "value",
                    "graphMode": "none",
                    "justifyMode": "auto",
                    "orientation": "auto",
                    "reduceOptions": {"calcs": ["lastNotNull"], "fields": "", "values": False},
                },
                "targets": [self.target(expr, "stats")],
            }
        )

    def timeseries(self, title, desc, expr, x, y, w=12, h=8, unit="short", steps=((None, "green"),), step="$__interval", legend="{{app}}"):
        self.panels.append(
            {
                "id": self._id(),
                "type": "timeseries",
                "title": title,
                "description": desc,
                "datasource": DS,
                "gridPos": {"h": h, "w": w, "x": x, "y": y},
                "fieldConfig": {
                    "defaults": {
                        "unit": unit,
                        "thresholds": thresholds(steps),
                        "color": {"mode": "palette-classic"},
                        "custom": {"drawStyle": "line", "lineWidth": 1, "fillOpacity": 10, "showPoints": "never", "spanNulls": False},
                    },
                    "overrides": [],
                },
                "options": {"legend": {"displayMode": "list", "placement": "bottom", "showLegend": True}, "tooltip": {"mode": "multi", "sort": "desc"}},
                "targets": [self.target(expr, "statsRange", step=step, legendFormat=legend)],
            }
        )

    def table(self, title, desc, expr, columns, x, y, w=24, h=12, overrides=(), sort=None):
        """columns: [(field, display name, numeric?, unit)]. Instant query,
        then extractFields over `labels` (the row), then the numeric
        conversions, then only the named columns in order."""
        convert = [{"targetField": f, "destinationType": "number"} for f, _, num, _ in columns if num == "number"]
        convert += [{"targetField": f, "destinationType": "time"} for f, _, num, _ in columns if num == "time"]
        transformations = [
            {"id": "extractFields", "options": {"source": "labels", "format": "json", "keepTime": False, "replace": True}},
            {"id": "convertFieldType", "options": {"conversions": convert, "fields": {}}},
            {
                "id": "organize",
                "options": {
                    "excludeByName": {"Time": True, "Line": True, "id": True, "labels": True, "streams": True, "streamId": True},
                    "indexByName": {f: i for i, (f, _, _, _) in enumerate(columns)},
                    "renameByName": {f: n for f, n, _, _ in columns},
                },
            },
        ]
        unit_overrides = [
            {"matcher": {"id": "byName", "options": n}, "properties": [{"id": "unit", "value": u}]} for f, n, _, u in columns if u
        ]
        self.panels.append(
            {
                "id": self._id(),
                "type": "table",
                "title": title,
                "description": desc,
                "datasource": DS,
                "gridPos": {"h": h, "w": w, "x": x, "y": y},
                "fieldConfig": {"defaults": {"custom": {"align": "auto", "filterable": True}, "thresholds": thresholds([(None, "green")])}, "overrides": unit_overrides + list(overrides)},
                "options": {"showHeader": True, "cellHeight": "sm", "sortBy": sort or []},
                "transformations": transformations,
                "targets": [self.target(expr, "instant")],
            }
        )

    def logs(self, title, desc, expr, x, y, w=24, h=10):
        self.panels.append(
            {
                "id": self._id(),
                "type": "logs",
                "title": title,
                "description": desc,
                "datasource": DS,
                "gridPos": {"h": h, "w": w, "x": x, "y": y},
                "fieldConfig": {"defaults": {"unit": "short", "thresholds": thresholds([(None, "green")])}, "overrides": []},
                "options": {
                    "showTime": True,
                    "wrapLogMessage": True,
                    "prettifyLogMessage": False,
                    "enableLogDetails": True,
                    "sortOrder": "Descending",
                    "dedupStrategy": "none",
                },
                "targets": [self.target(expr, "instant", maxLines=200)],
            }
        )

    def build(self, description):
        return {
            "uid": self.uid,
            "title": self.title,
            "description": description,
            "tags": self.tags,
            "timezone": "browser",
            "schemaVersion": 39,
            "version": 1,
            "editable": False,
            "graphTooltip": 1,
            "time": {"from": "now-24h", "to": "now"},
            "refresh": "1m",
            "links": [
                {
                    "title": "Frontend",
                    "type": "dashboards",
                    "tags": ["observability-frontend"],
                    "asDropdown": False,
                    "includeVars": True,
                    "keepTime": True,
                }
            ],
            "templating": {"list": self.vars},
            "annotations": {"list": []},
            "panels": self.panels,
        }


def issues():
    d = Dash("observability-rum-issues", "Frontend Issues ($cluster)", ["observability-frontend", "frontend-issues"])
    d.variables(
        extra=[
            {
                "name": "fingerprint",
                "label": "fingerprint",
                "type": "textbox",
                "query": "*",
                "current": {"selected": False, "text": "*", "value": "*"},
                "options": [{"selected": True, "text": "*", "value": "*"}],
                "hide": 0,
            }
        ]
    )
    base = SCOPE + " kind:exception"
    d.row("Overview", 0)
    d.stat("Exceptions", "Exceptions the browsers reported in the selected range, for the chosen app. A rise after a release is the first thing to read the table below for.", f"{base} | stats count() as n", 0, 1, w=6)
    d.stat(
        "Distinct issues",
        "Different error fingerprints seen in the range. One issue is one fingerprint: an exception type, a normalised message and the first frame of the app's own code. See docs/frontend.md.",
        f"{base} error.fingerprint:* | stats count_uniq(error.fingerprint) as n",
        6, 1, w=6,
    )
    d.stat(
        "Sessions with an exception",
        "Anonymous browser sessions that hit at least one exception. Compare it with Sessions on the Overview dashboard for the share of sessions affected.",
        f"{base} | stats count_uniq(session_id) as n",
        12, 1, w=6,
    )
    d.stat(
        "Exceptions without a fingerprint",
        "Exception rows carrying no error.fingerprint: the pipeline did not run on them. Anything above zero is a defect to report, not an issue to fix in an app.",
        f"{SCOPE} kind:exception -error.fingerprint:* | stats count() as n",
        18, 1, w=6, steps=((None, "green"), (1, "red")),
    )
    d.timeseries(
        "Exceptions over time",
        "Exceptions per bucket, one line per app. A step that lines up with a release is a regression; a slow climb is a leak or a degrading dependency.",
        f"{base} | stats by (app) count() as exceptions",
        0, 5, w=24, h=7,
    )
    d.row("Issues", 12)
    issues_expr = (
        f"{base} error.fingerprint:* "
        "| stats by (app, error.fingerprint) count() as events, count_uniq(session_id) as sessions, "
        "min(_time) as first_seen, max(_time) as last_seen, any(type) as type, any(error.message) as message, any(value) as sample, "
        "row_max(_time, app_release, trace_id) as latest "
        "| sort by (events desc) limit 200 "
        "| unpack_json from latest fields (app_release, trace_id) "
        "| fields app, error.fingerprint, type, message, sample, events, sessions, first_seen, last_seen, app_release, trace_id "
        "| rename error.fingerprint as fingerprint, app_release as release"
    )
    d.table(
        "Issues by fingerprint",
        "One row per fingerprint in the range, busiest first (200 at most): the app, the exception type, the normalised message (numbers, ids, strings and URLs removed, so the same bug groups) and one message as it was seen, how many events and sessions, when it first and last appeared WITHIN the selected range, the release of the latest event and its trace. Click a fingerprint to see its raw events below; click a trace to open it.",
        issues_expr,
        [
            ("app", "App", None, None),
            ("fingerprint", "Fingerprint", None, None),
            ("type", "Type", None, None),
            ("message", "Message", None, None),
            ("sample", "Sample", None, None),
            ("events", "Events", "number", "short"),
            ("sessions", "Sessions", "number", "short"),
            ("first_seen", "First seen", "time", None),
            ("last_seen", "Last seen", "time", None),
            ("release", "Release", None, None),
            ("trace_id", "Trace", None, None),
        ],
        0, 13, h=14,
        overrides=[
            {
                "matcher": {"id": "byName", "options": "Fingerprint"},
                "properties": [
                    {
                        "id": "links",
                        "value": [
                            {
                                "title": "Raw events of this fingerprint",
                                "url": "/d/observability-rum-issues?" + CARRY.replace("var-app=${app:queryparam}", "var-app=${__data.fields.App}") + "&var-fingerprint=${__value.raw}",
                            }
                        ],
                    },
                    {"id": "custom.width", "value": 170},
                ],
            },
            {
                "matcher": {"id": "byName", "options": "Trace"},
                "properties": [
                    {"id": "links", "value": [{"title": "Open the trace", "url": TRACE_URL, "targetBlank": True}]},
                    {"id": "custom.width", "value": 280},
                ],
            },
            {"matcher": {"id": "byName", "options": "Message"}, "properties": [{"id": "custom.width", "value": 420}]},
            {"matcher": {"id": "byName", "options": "Sample"}, "properties": [{"id": "custom.width", "value": 420}]},
        ],
        sort=[{"displayName": "Events", "desc": True}],
    )
    d.row("Raw events", 27)
    d.logs(
        "Raw events",
        "The exception rows of the fingerprint set in the variable above (`*` is every one), newest first, with every field: the page, the release, the session, the trace_id and the symbolicated stack. Click a fingerprint in the table to set it.",
        f"{base} error.fingerprint:$fingerprint | sort by (_time desc)",
        0, 28,
    )
    return d.build(
        "Browser exceptions grouped by error fingerprint (charts/observability-rum): what is new, how big, since when, in which release, and the raw events behind each."
    )


def overview():
    d = Dash("observability-rum-overview", "Frontend Overview ($cluster)", ["observability-frontend", "frontend-overview"])
    d.variables()
    vit = f"{SCOPE} kind:measurement type:web-vitals"
    d.row("Health", 0)
    d.stat("Sessions", "Distinct anonymous browser sessions with any event in the range, for the chosen app.", f"{SCOPE} | stats count_uniq(session_id) as n", 0, 1, w=4)
    d.stat("Exceptions", "Exceptions reported in the range. The Frontend Issues dashboard has them by fingerprint.", f"{SCOPE} kind:exception | stats count() as n", 4, 1, w=4)
    d.stat(
        "Exceptions per session",
        "Exceptions divided by sessions in the range: the number the FrontendErrorRateHigh alert watches. Zero is quiet, above one means a typical session raises an error.",
        f"{SCOPE} | stats count() if (kind:exception) as e, count_uniq(session_id) as s | math e / s as rate | fields rate",
        8, 1, w=4, unit="none", decimals=2, steps=((None, "green"), (0.5, "orange"), (1, "red")),
    )
    d.stat(
        "LCP p75",
        "Largest Contentful Paint, 75th percentile over the range (Core Web Vitals). Good is under 2.5 s, poor over 4 s.",
        f"{vit} | stats quantile(0.75, value_lcp) as lcp",
        12, 1, w=4, unit="ms", decimals=0, steps=((None, "green"), (2500, "orange"), (4000, "red")),
    )
    d.stat(
        "INP p75",
        "Interaction to Next Paint, 75th percentile over the range. Good is under 200 ms, poor over 500 ms. Reported only by browsers that support it.",
        f"{vit} | stats quantile(0.75, value_inp) as inp",
        16, 1, w=4, unit="ms", decimals=0, steps=((None, "green"), (200, "orange"), (500, "red")),
    )
    d.stat(
        "CLS p75",
        "Cumulative Layout Shift, 75th percentile over the range. Good is under 0.1, poor over 0.25.",
        f"{vit} | stats quantile(0.75, value_cls) as cls",
        20, 1, w=4, unit="none", decimals=3, steps=((None, "green"), (0.1, "orange"), (0.25, "red")),
    )
    d.row("Over time", 5)
    d.timeseries("Sessions", "Distinct sessions per bucket, per app.", f"{SCOPE} | stats by (app) count_uniq(session_id) as sessions", 0, 6, w=8)
    d.timeseries(
        "Exceptions per session",
        "Exceptions divided by sessions in each bucket, per app. The same ratio as the stat above, as a trend.",
        f"{SCOPE} | stats by (app) count() if (kind:exception) as e, count_uniq(session_id) as s | math e / s as rate | fields _time, app, rate",
        8, 6, w=8, unit="none", steps=((None, "green"), (0.5, "orange"), (1, "red")),
    )
    d.timeseries(
        "LCP p75",
        "75th percentile Largest Contentful Paint per bucket, per app, from the web-vitals measurements the Faro SDK sends. Computed at query time over the log rows: this chart ships no recording rule.",
        f"{vit} | stats by (app) quantile(0.75, value_lcp) as lcp",
        16, 6, w=8, unit="ms", steps=((None, "green"), (2500, "orange"), (4000, "red")),
    )
    d.timeseries(
        "INP p75",
        "75th percentile Interaction to Next Paint per bucket, per app.",
        f"{vit} | stats by (app) quantile(0.75, value_inp) as inp",
        0, 14, w=12, unit="ms", steps=((None, "green"), (200, "orange"), (500, "red")),
    )
    d.timeseries(
        "CLS p75",
        "75th percentile Cumulative Layout Shift per bucket, per app.",
        f"{vit} | stats by (app) quantile(0.75, value_cls) as cls",
        12, 14, w=12, unit="none", steps=((None, "green"), (0.1, "orange"), (0.25, "red")),
    )
    d.row("Pages", 22)
    path = '| extract_regexp "https?://[^/]+(?P<path>/[^?#]*)" from page_url | replace_regexp("[0-9a-fA-F-]{8,}|[0-9]+", ":id") at path'
    d.table(
        "Web vitals by page",
        "Web vitals measurements per page: the path of the page URL with query and fragment already removed and every number or id replaced by `:id` so pages group. LCP, INP and CLS are 75th percentiles over the range; Samples is how many measurements, so read a row with a handful as a hint, not a result.",
        f"{vit} {path} | stats by (app, path) quantile(0.75, value_lcp) as lcp, quantile(0.75, value_inp) as inp, quantile(0.75, value_cls) as cls, count() as samples | sort by (samples desc) limit 100",
        [
            ("app", "App", None, None),
            ("path", "Page", None, None),
            ("lcp", "LCP p75", "number", "ms"),
            ("inp", "INP p75", "number", "ms"),
            ("cls", "CLS p75", "number", "none"),
            ("samples", "Samples", "number", "short"),
        ],
        0, 23, w=14, h=12,
        overrides=[
            {"matcher": {"id": "byName", "options": "LCP p75"}, "properties": [{"id": "thresholds", "value": thresholds([(None, "green"), (2500, "orange"), (4000, "red")])}, {"id": "custom.cellOptions", "value": {"type": "color-text"}}]},
            {"matcher": {"id": "byName", "options": "INP p75"}, "properties": [{"id": "thresholds", "value": thresholds([(None, "green"), (200, "orange"), (500, "red")])}, {"id": "custom.cellOptions", "value": {"type": "color-text"}}]},
            {"matcher": {"id": "byName", "options": "CLS p75"}, "properties": [{"id": "thresholds", "value": thresholds([(None, "green"), (0.1, "orange"), (0.25, "red")])}, {"id": "custom.cellOptions", "value": {"type": "color-text"}}]},
        ],
        sort=[{"displayName": "Samples", "desc": True}],
    )
    d.table(
        "Top pages",
        "Pages by distinct sessions in the range (any event on the page), same path normalisation as the table beside it. Open it from the Exceptions count to see where the errors are.",
        f"{SCOPE} {path} | stats by (app, path) count_uniq(session_id) as sessions, count() if (kind:exception) as exceptions | sort by (sessions desc) limit 100",
        [
            ("app", "App", None, None),
            ("path", "Page", None, None),
            ("sessions", "Sessions", "number", "short"),
            ("exceptions", "Exceptions", "number", "short"),
        ],
        14, 23, w=10, h=12,
        sort=[{"displayName": "Sessions", "desc": True}],
    )
    return d.build(
        "Browser health per app (charts/observability-rum): sessions, exceptions per session, Core Web Vitals (LCP, INP, CLS) at the 75th percentile, and the pages behind them."
    )


def main():
    OUT.mkdir(parents=True, exist_ok=True)
    for name, dash in (("frontend-issues", issues()), ("frontend-overview", overview())):
        (OUT / f"{name}.json").write_text(json.dumps(dash, indent=2, sort_keys=False) + "\n")
        print("wrote", OUT / f"{name}.json")


if __name__ == "__main__":
    main()

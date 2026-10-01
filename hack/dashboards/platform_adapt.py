"""Adapt a platform component's upstream dashboard to the dashboard contract.

Used by hack/dashboards.py for every source flagged `adapt: platform` in
hack/dashboards/sources.yaml (ArgoCD, cert-manager, CloudNativePG, NATS,
Envoy Gateway). The rewrite is mechanical and driven by the source's
`platform:` block; what it does, in order:

  1. strips the export scaffolding (`__inputs`, `__requires`, ids);
  2. drops the rows and panels the block names (by title or type, or by a
     text their query mentions), each with a reason, and every text and
     alert-list panel;
  3. renames the variables that collide with the contract's names
     (upstream's own `cluster` is usually an application's destination or an
     Envoy cluster, not an install) and drops the ones that do not apply;
  4. points every panel, target and variable at the `datasource` variable;
  5. applies the block's literal query replacements (each must match, or the
     build fails: a replace that silently matches nothing is how a stale
     patch survives an upstream bump);
  6. drops every query that reads a metric outside
     hack/dashboards/available-metrics.yaml, and every panel left empty, and
     prints each drop;
  7. adds `k8s_cluster_name=~"$cluster"` to every selector and to every
     variable query, and the chained `cluster` variable;
  8. converts legacy `graph` panels to `timeseries`, closes the gaps dropped
     panels leave, numbers the panels, and requires a description and a unit
     on each.

It never invents a metric: a query it cannot keep is dropped, not rewritten
into something else, except where the block says so in a `replace` entry that
names the query.
"""

import copy
import re
import sys
from pathlib import Path

sys.path.insert(0, str(Path(__file__).resolve().parent))

import promql_scan as scan  # noqa: E402
from platform_descriptions import DESCRIPTIONS  # noqa: E402

CLUSTER_LABEL = "k8s_cluster_name"
CLUSTER_MATCHER = '%s=~"$cluster"' % CLUSTER_LABEL
DATASOURCE_TOKEN = "__DATASOURCE_UID__"
DS_REF = {"type": "prometheus", "uid": "${datasource}"}
GREEN = "green"

# Keys a legacy `graph` panel carries that a `timeseries` panel does not.
_GRAPH_KEYS = (
    "aliasColors", "bars", "dashLength", "dashes", "fill", "fillGradient", "hiddenSeries", "lines",
    "linewidth", "nullPointMode", "paceLength", "percentage", "pointradius", "points", "renderer",
    "seriesOverrides", "spaceLength", "stack", "steppedLine", "thresholds", "timeRegions", "xaxis",
    "yaxes", "yaxis", "tooltip", "legend",
)


class Report:
    """What the adapter dropped, printed for the review and returned for the
    PR description."""

    def __init__(self, name):
        self.name = name
        self.dropped = []  # (title, reason)

    def drop(self, title, reason):
        self.dropped.append((title, reason))
        print("  %s: drop %r (%s)" % (self.name, title, reason))


# ---------------------------------------------------------------------------
# Generic helpers
# ---------------------------------------------------------------------------
def walk(panels):
    for p in panels or []:
        yield p
        yield from walk(p.get("panels"))


def _map_strings(obj, fn):
    if isinstance(obj, dict):
        return {k: _map_strings(v, fn) for k, v in obj.items()}
    if isinstance(obj, list):
        return [_map_strings(v, fn) for v in obj]
    if isinstance(obj, str):
        return fn(obj)
    return obj


def sub_var(s: str, old: str, new: str) -> str:
    s = re.sub(r"\$\{%s(?=[:}])" % re.escape(old), "${%s" % new, s)
    s = re.sub(r"\$%s\b" % re.escape(old), "$" + new, s)
    return s.replace("[[%s]]" % old, "[[%s]]" % new)


def rename_var(dashboard, old: str, new: str, label=None) -> None:
    fixed = _map_strings(dashboard, lambda s: sub_var(s, old, new))
    dashboard.clear()
    dashboard.update(fixed)
    for p in walk(dashboard.get("panels")):
        if p.get("repeat") == old:
            p["repeat"] = new
    found = False
    for v in dashboard["templating"]["list"]:
        if v.get("name") == old:
            v["name"] = new
            v["label"] = label or new
            found = True
    if not found:
        raise SystemExit("renameVars: dashboard has no variable %r" % old)


def _selector_text(query: str):
    if isinstance(query, dict):
        return query.get("query", "")
    return query or ""


def _matches(p, d):
    if "title" not in d and "type" not in d:
        raise SystemExit("a drop/retitle entry needs a title or a type: %r" % (d,))
    if "title" in d and p.get("title") != d["title"]:
        return False
    if "type" in d and p.get("type") != d["type"]:
        return False
    return True


# ---------------------------------------------------------------------------
# Sections: the flat panel list as (row, children) pairs.
# ---------------------------------------------------------------------------
def to_sections(panels):
    sections = []
    cur = None
    for p in panels:
        if p.get("type") == "row":
            cur = {"row": p, "children": list(p.get("panels") or []) if p.get("collapsed") else []}
            sections.append(cur)
        else:
            if cur is None:
                cur = {"row": None, "children": []}
                sections.append(cur)
            cur["children"].append(p)
    return sections


def from_sections(sections):
    out = []
    for s in sections:
        row = s["row"]
        if row is not None:
            row = dict(row)
            if row.get("collapsed"):
                row["panels"] = s["children"]
                out.append(row)
            else:
                row["panels"] = []
                out.append(row)
                out.extend(s["children"])
        else:
            out.extend(s["children"])
    return out


def _shelf(children, y0, lost):
    """Lay children out in bands (panels sharing an original `y`), from y0.
    A band that lost a member is re-spread over the 24 columns when its
    panels share a height; the others keep their widths."""
    bands = {}
    for p in children:
        bands.setdefault(p["gridPos"]["y"], []).append(p)
    cursor = y0
    for y in sorted(bands):
        band = sorted(bands[y], key=lambda p: p["gridPos"]["x"])
        total = sum(p["gridPos"]["w"] for p in band)
        h = max(p["gridPos"]["h"] for p in band)
        if y in lost and total != 24 and len({p["gridPos"]["h"] for p in band}) == 1:
            x = 0
            for i, p in enumerate(band):
                w = 24 - x if i == len(band) - 1 else round(p["gridPos"]["w"] * 24 / total)
                p["gridPos"].update({"x": x, "w": w})
                x += w
        for p in band:
            p["gridPos"]["y"] = cursor
        cursor += h
    return cursor


def relayout(sections, lost_by_section):
    y = 0
    for i, s in enumerate(sections):
        row = s["row"]
        if row is not None:
            row["gridPos"]["y"] = y
            y += 1
        if not s["children"]:
            continue
        if lost_by_section.get(i):
            end = _shelf(s["children"], y, lost_by_section[i])
        else:
            # Nothing was dropped here: keep upstream's own arrangement (panels
            # of different heights side by side), moved up under its row.
            base = min(p["gridPos"]["y"] for p in s["children"])
            for p in s["children"]:
                p["gridPos"]["y"] += y - base
            end = max(p["gridPos"]["y"] + p["gridPos"]["h"] for p in s["children"])
        if row is None or not row.get("collapsed"):
            y = end
        s["children"].sort(key=lambda p: (p["gridPos"]["y"], p["gridPos"]["x"]))


def _query_var(v):
    q = v["query"]
    return {
        "name": v["name"], "label": v.get("label", v["name"]), "type": "query",
        "datasource": dict(DS_REF), "definition": q, "query": {"query": q, "refId": "%s-Variable-Query" % v["name"]},
        "current": {}, "hide": 0, "includeAll": v.get("includeAll", True), "allValue": ".*" if v.get("includeAll", True) else None,
        "multi": v.get("multi", True), "options": [], "refresh": 2, "regex": "", "skipUrlSync": False, "sort": 1,
    }


# ---------------------------------------------------------------------------
# The adapter
# ---------------------------------------------------------------------------
def adapt(dashboard, spec, metric_available, extras=None):
    name = spec["name"]
    cfg = spec.get("platform") or {}
    report = Report(name)
    anchor = cfg["anchor"]

    for k in ("__inputs", "__elements", "__requires", "gnetId", "iteration", "version", "links"):
        dashboard.pop(k, None)
    dashboard["id"] = None
    dashboard["editable"] = False
    dashboard["refresh"] = "1m"
    dashboard["annotations"] = {"list": [a for a in dashboard.get("annotations", {}).get("list", []) if a.get("builtIn")]}
    dashboard["tags"] = []
    dashboard.setdefault("templating", {}).setdefault("list", [])

    # -- 2b. a declared metric-name prefix rewrite (NATS: the chart runs the
    # exporter with a prefix the walkthrough dashboards do not use). The
    # rewrite must match something, or the build fails; one map serves both
    # NATS dashboards, so a single entry may match nothing in one of them.
    total = [0]
    for old, new in (cfg.get("metricPrefixRewrite") or {}).items():
        pat = re.compile(r"(?<![A-Za-z0-9_])%s" % re.escape(old))
        hits = [0]

        def _rw(text, pat=pat, new=new):
            out, n = pat.subn(new, text)
            hits[0] += n
            return out

        fixed = _map_strings(dashboard, _rw)
        dashboard.clear()
        dashboard.update(fixed)
        total[0] += hits[0]
    if cfg.get("metricPrefixRewrite") and not total[0]:
        raise SystemExit("%s: metricPrefixRewrite matched nothing (upstream moved?)" % name)

    # -- 3. variables -------------------------------------------------------
    tv = dashboard["templating"]["list"]
    for vname in cfg.get("dropVars", []):
        if not any(v.get("name") == vname for v in tv):
            raise SystemExit("%s: dropVars names a variable that is not there: %r" % (name, vname))
    tv[:] = [v for v in tv if v.get("name") not in cfg.get("dropVars", [])]
    for old, new in (cfg.get("renameVars") or {}).items():
        label = None
        if isinstance(new, dict):
            new, label = new["name"], new.get("label")
        rename_var(dashboard, old, new, label)
    tv = dashboard["templating"]["list"]
    for newvar in cfg.get("addVars", []):
        tv.append(_query_var(newvar))
    for v in tv:
        ov = (cfg.get("varOverrides") or {}).get(v.get("name"))
        if ov:
            v.update(copy.deepcopy(ov))
    for vname in (cfg.get("varOverrides") or {}):
        if not any(v.get("name") == vname for v in tv):
            raise SystemExit("%s: varOverrides names a variable that is not there: %r" % (name, vname))
    for v in tv:
        if v.get("name") in cfg.get("varOptionsDrop", {}):
            drop = set(cfg["varOptionsDrop"][v["name"]])
            opts = [o for o in v.get("query", "").split(",") if o not in drop]
            v["query"] = ",".join(opts)
            v["options"] = [o for o in v.get("options", []) if o.get("value") not in drop]
            if v.get("current", {}).get("value") in drop:
                v["current"] = {"selected": True, "text": opts[0], "value": opts[0]}

    # -- 4. the datasource variable and every reference to it ----------------
    ds_vars = [v for v in tv if v.get("type") == "datasource"]
    if len(ds_vars) > 1:
        raise SystemExit("%s: expected at most one datasource variable" % name)
    placeholder = cfg.get("datasourcePlaceholder")
    if ds_vars and ds_vars[0]["name"] != "datasource":
        rename_var(dashboard, ds_vars[0]["name"], "datasource")
        tv = dashboard["templating"]["list"]
    if placeholder:
        fixed = _map_strings(dashboard, lambda s: s.replace(placeholder, "${datasource}"))
        dashboard.clear()
        dashboard.update(fixed)
        tv = dashboard["templating"]["list"]
    if not ds_vars:
        tv.insert(0, {})
        ds_var = tv[0]
    else:
        ds_var = next(v for v in tv if v.get("type") == "datasource")
    ds_var.clear()
    ds_var.update({
        "name": "datasource", "label": "datasource", "type": "datasource", "query": "prometheus",
        "current": {"selected": True, "text": DATASOURCE_TOKEN, "value": DATASOURCE_TOKEN},
        "hide": 0, "includeAll": False, "multi": False, "options": [], "refresh": 1, "regex": "",
        "skipUrlSync": False,
    })
    tv.remove(ds_var)
    tv.insert(0, ds_var)

    # -- 2. drops ---------------------------------------------------------
    # Grafana lays panels out by gridPos, not by list order; upstream exports
    # are not always sorted (a panel can sit after the row it is above).
    dashboard["panels"] = sorted(dashboard["panels"], key=lambda p: (p["gridPos"]["y"], p["gridPos"]["x"]))
    sections = to_sections(dashboard["panels"])
    lost = {}  # section index -> set of original band ys that lost a member

    def mark_lost(i, p):
        lost.setdefault(i, set()).add(p["gridPos"]["y"])

    # Keep only the named rows' contents, when the block says so (CNPG).
    if cfg.get("keepOnlyRows"):
        keep_rows = set(cfg["keepOnlyRows"])
        new = []
        for s in sections:
            if s["row"] is not None and s["row"].get("title") in keep_rows:
                s["row"]["collapsed"] = False
                new.append(s)
            else:
                shown = [p.get("title") for p in walk(s["children"]) if p.get("type") not in ("row", "text", "alertlist") and p.get("targets")]
                report.drop(
                    "%s: %d panels with queries%s" % (
                        s["row"]["title"] if s["row"] is not None else "top of dashboard", len(shown),
                        (" (" + "; ".join(t or "untitled" for t in shown) + ")") if shown else ""),
                    cfg.get("keepOnlyReason", "outside the kept rows"),
                )
        sections = new
        lost = {}

    drop_rows = {r["title"]: r["reason"] for r in cfg.get("dropRows", [])}
    for title in drop_rows:
        if not any(s["row"] is not None and s["row"].get("title") == title for s in sections):
            raise SystemExit("%s: dropRows names a row that is not there: %r" % (name, title))
    kept = []
    for s in sections:
        if s["row"] is not None and s["row"].get("title") in drop_rows:
            report.drop("row %s (%d panels)" % (s["row"]["title"], len(s["children"])), drop_rows[s["row"]["title"]])
            continue
        kept.append(s)
    sections = kept

    for spec_drop in cfg.get("dropPanels", []):
        hit = 0
        for i, s in enumerate(sections):
            rowtitle = s["row"].get("title") if s["row"] is not None else None
            if spec_drop.get("row") and spec_drop["row"] != rowtitle:
                continue
            keep = []
            for p in s["children"]:
                if _matches(p, spec_drop):
                    hit += 1
                    report.drop(p.get("title") or "(untitled %s)" % p.get("type"), spec_drop["reason"])
                    mark_lost(i, p)
                else:
                    keep.append(p)
            s["children"] = keep
        if not hit:
            raise SystemExit("%s: dropPanels matched nothing: %r" % (name, spec_drop))

    # Panels whose query mentions a given text (a variable that is dropped
    # with the row it belongs to, say) leave with it.
    for spec_drop in cfg.get("dropPanelsReading", []):
        hit = 0
        for i, s in enumerate(sections):
            keep = []
            for p in s["children"]:
                if any(spec_drop["contains"] in t.get("expr", "") for t in p.get("targets") or []):
                    hit += 1
                    report.drop(p.get("title") or "(untitled %s)" % p.get("type"), spec_drop["reason"])
                    mark_lost(i, p)
                else:
                    keep.append(p)
            s["children"] = keep
        if not hit:
            raise SystemExit("%s: dropPanelsReading matched nothing: %r" % (name, spec_drop))

    # Untitled panels that sat under a text label upstream (the label is a
    # text panel, dropped below) are dropped by section, or titled by the
    # start of their query.
    for spec_drop in cfg.get("dropUntitled", []):
        hit = 0
        for i, s in enumerate(sections):
            rowtitle = s["row"].get("title") if s["row"] is not None else "top of dashboard"
            if rowtitle != spec_drop["row"]:
                continue
            keep = []
            for p in s["children"]:
                if not p.get("title") and p.get("type") not in ("text", "alertlist", "news", "dashlist", "row"):
                    hit += 1
                    report.drop("(untitled %s)" % p.get("type"), spec_drop["reason"])
                    mark_lost(i, p)
                else:
                    keep.append(p)
            s["children"] = keep
        if not hit:
            raise SystemExit("%s: dropUntitled matched nothing: %r" % (name, spec_drop))
    for rt in cfg.get("retitleByQuery", []):
        hit = 0
        for s in sections:
            for p in s["children"]:
                if not p.get("title") and any(t.get("expr", "").strip().startswith(rt["startswith"]) for t in p.get("targets") or []):
                    p["title"] = rt["to"]
                    hit += 1
        if not hit:
            raise SystemExit("%s: retitleByQuery matched nothing: %r" % (name, rt))

    # Text and alert-list panels answer no query and have nothing to describe.
    for i, s in enumerate(sections):
        keep = []
        for p in s["children"]:
            if p.get("type") in ("text", "alertlist", "news", "dashlist"):
                mark_lost(i, p)
                report.drop(p.get("title") or "(untitled %s panel)" % p.get("type"), "%s panel, no query" % p["type"])
            else:
                keep.append(p)
        s["children"] = keep

    for i, s in enumerate(sections):
        s["_i"] = i

    # -- retitle, replace, convert ------------------------------------------
    for rt in cfg.get("retitle", []):
        hit = 0
        for s in sections:
            rowtitle = s["row"].get("title") if s["row"] is not None else None
            if rt.get("row") and rt["row"] != rowtitle:
                continue
            for p in s["children"]:
                if _matches(p, {k: rt[k] for k in ("type",) if k in rt} | {"title": rt["from"]}):
                    p["title"] = rt["to"]
                    hit += 1
        if not hit:
            raise SystemExit("%s: retitle matched nothing: %r" % (name, rt))

    for s in sections:
        s["children"] = [convert_panel(p) for p in s["children"]]

    # -- 5. literal replacements ------------------------------------------------
    for rep in cfg.get("replace", []):
        hit = 0
        for s in sections:
            for p in s["children"]:
                for t in p.get("targets") or []:
                    if rep["from"] in t.get("expr", ""):
                        t["expr"] = t["expr"].replace(rep["from"], rep["to"])
                        hit += 1
        for v in tv:
            q = v.get("query")
            text = _selector_text(q)
            if rep["from"] in text:
                new = text.replace(rep["from"], rep["to"])
                v["query"] = {"query": new, "refId": "%s-Variable-Query" % v["name"]} if isinstance(q, dict) else new
                v["definition"] = new
                hit += 1
        if not hit:
            raise SystemExit("%s: replace matched nothing (upstream moved?): %r" % (name, rep["from"]))
    for var in cfg.get("regexMatchVars", []):
        # A single-select variable used with `=` becomes a regex match, so the
        # same query serves a multi-select and an All.
        pat = re.compile(r'([A-Za-z_][A-Za-z0-9_]*)="\$\{?%s\}?"' % re.escape(var))
        for s in sections:
            for p in s["children"]:
                for t in p.get("targets") or []:
                    t["expr"] = pat.sub(r'\1=~"$%s"' % var, t["expr"])
    for rep in cfg.get("setExpr", []):
        hit = 0
        for s in sections:
            for p in s["children"]:
                if p.get("title") == rep["title"] and (not rep.get("row") or (s["row"] and s["row"].get("title") == rep["row"])):
                    for t in p.get("targets") or []:
                        t["expr"] = rep["expr"]
                        if "legend" in rep:
                            t["legendFormat"] = rep["legend"]
                    p["targets"] = p["targets"][:1]
                    if rep.get("unit"):
                        p.setdefault("fieldConfig", {}).setdefault("defaults", {})["unit"] = rep["unit"]
                    hit += 1
        if not hit:
            raise SystemExit("%s: setExpr matched nothing: %r" % (name, rep["title"]))

    # -- 6. absent metrics --------------------------------------------------------
    for i, s in enumerate(sections):
        keep = []
        for p in s["children"]:
            if p.get("type") == "row" or not p.get("targets"):
                keep.append(p)
                continue
            targets = []
            for t in p["targets"]:
                e = t.get("expr", "")
                if not e.strip():
                    continue
                missing = sorted({m for m in scan.metrics_in(e) if not metric_available(m)})
                if missing:
                    report.drop("query of %s" % (p.get("title") or "(untitled)"), "reads %s, not in available-metrics.yaml" % ", ".join(missing))
                    continue
                targets.append(t)
            if not targets:
                report.drop(p.get("title") or "(untitled %s)" % p.get("type"), "no query left after the absent-metric check")
                mark_lost(i, p)
                continue
            p["targets"] = targets
            keep.append(p)
        s["children"] = keep

    # -- 7. the cluster filter and the cluster variable -----------------------------
    for s in sections:
        for p in s["children"]:
            for t in p.get("targets") or []:
                t["expr"] = scan.add_matcher(t["expr"], CLUSTER_MATCHER)
    cluster_q = "label_values(%s, %s)" % (anchor, CLUSTER_LABEL)
    for v in tv:
        if v.get("type") != "query":
            continue
        q = v.get("query")
        text = _selector_text(q)
        parts = scan.split_label_values(text)
        if parts is None:
            raise SystemExit("%s: variable %r has a query that is not label_values(): %s" % (name, v["name"], text))
        prefix, sel, label, suffix = parts
        if sel is None:
            sel = cfg.get("varAnchor") or anchor
        sel = scan.add_matcher(sel, CLUSTER_MATCHER)
        for m in scan.metrics_in(sel):
            if not metric_available(m):
                raise SystemExit("%s: variable %r reads %s, not in available-metrics.yaml" % (name, v["name"], m))
        new = "%s%s, %s%s" % (prefix, sel, label, suffix.strip())
        v["query"] = {"query": new, "refId": "%s-Variable-Query" % v["name"]}
        v["definition"] = new
        v["datasource"] = dict(DS_REF)
        v["refresh"] = 2
        v.pop("tagsQuery", None)
        v.pop("tagValuesQuery", None)
        v.pop("useTags", None)
    tv.insert(1, {
        "current": {"selected": False, "text": "", "value": ""},
        "datasource": dict(DS_REF),
        "definition": cluster_q,
        "hide": 0, "includeAll": False, "label": "cluster", "multi": False, "name": "cluster",
        "options": [],
        "query": {"query": cluster_q, "refId": "cluster-Variable-Query"},
        "refresh": 2, "regex": "", "skipUrlSync": False, "sort": 1, "type": "query",
    })

    # Every variable still referenced must exist.
    have = {v["name"] for v in tv} | {"__rate_interval", "__interval", "__range", "__interval_ms", "__from", "__to",
                                        "__all", "__name", "__field", "__value", "__series", "__data", "__url_time_range",
                                        "__auto_interval_resolution"}
    for s in sections:
        for p in s["children"]:
            for t in p.get("targets") or []:
                for ref in re.findall(r"\$\{?([A-Za-z_][A-Za-z0-9_]*)", t["expr"]):
                    if ref not in have:
                        raise SystemExit("%s: panel %r uses $%s, which is not a variable (dropVars too much?)" % (name, p.get("title"), ref))

    # -- extras (authored panels added to the adapted dashboard) ---------------------
    if extras:
        # `guardPanel`: the block authors a banner that must sit first (the
        # "is the source scraped at all" stat), not at the foot.
        target = sections[0] if cfg.get("guardPanel") else sections[-1]
        for p in extras(name):
            target["children"].append(p)

    # -- 8. panels ---------------------------------------------------------------------
    missing_desc = []
    for s in sections:
        for p in s["children"]:
            if p.get("type") == "row":
                continue
            p["datasource"] = dict(DS_REF)
            for t in p.get("targets") or []:
                t["datasource"] = dict(DS_REF)
            title = p.get("title", "")
            if not p.get("description"):
                d = DESCRIPTIONS.get((name, title)) or DESCRIPTIONS.get(("*", title))
                if not d:
                    missing_desc.append(title)
                else:
                    p["description"] = d
            defaults = p.setdefault("fieldConfig", {}).setdefault("defaults", {})
            if p.get("type") in ("stat", "gauge", "bargauge", "timeseries") and not defaults.get("unit"):
                defaults["unit"] = cfg.get("units", {}).get(title, "short")
            elif cfg.get("units", {}).get(title):
                defaults["unit"] = cfg["units"][title]
            p.pop("pluginVersion", None)
    if missing_desc:
        raise SystemExit("%s: panels with no description in hack/dashboards/platform_descriptions.py: %s" % (name, sorted(set(missing_desc))))

    # Sections the block lays out by hand: dropping the text labels and the
    # status lights leaves holes no re-spread can close sensibly. Each grid
    # row is {h, titles}; the titles share the 24 columns equally, and every
    # panel of the section must be placed.
    for sec_title, rows in (cfg.get("grids") or {}).items():
        sec = next((s for s in sections if (s["row"].get("title") if s["row"] is not None else "top of dashboard") == sec_title), None)
        if sec is None:
            raise SystemExit("%s: grids names a section that is not there: %r" % (name, sec_title))
        by_title = {}
        for p in sec["children"]:
            if p.get("title") in by_title:
                raise SystemExit("%s: grids %r: two panels titled %r" % (name, sec_title, p.get("title")))
            by_title[p.get("title")] = p
        placed = set()
        y = 0
        for gr in rows:
            titles = gr["titles"]
            w, x = 24 // len(titles), 0
            for i, t in enumerate(titles):
                if t not in by_title:
                    raise SystemExit("%s: grids %r names a panel that is not there: %r" % (name, sec_title, t))
                if t in placed:
                    raise SystemExit("%s: grids %r places %r twice" % (name, sec_title, t))
                placed.add(t)
                ww = 24 - x if i == len(titles) - 1 else w
                by_title[t]["gridPos"].update({"x": x, "y": y, "w": ww, "h": gr["h"]})
                x += ww
            y += gr["h"]
        left = sorted(set(by_title) - placed)
        if left:
            raise SystemExit("%s: grids %r leaves panels unplaced: %s" % (name, sec_title, left))
        lost.pop(sections.index(sec), None)

    relayout(sections, {s["_i"]: lost.get(s["_i"], set()) for s in sections})
    dashboard["panels"] = from_sections(sections)
    n = 0
    for p in walk(dashboard["panels"]):
        n += 1
        p["id"] = n
    return report


def convert_panel(p):
    if p.get("type") != "graph":
        return p
    y0 = (p.get("yaxes") or [{}])[0]
    lg = p.get("legend") or {}
    calcs = [c for c, k in (("lastNotNull", "current"), ("max", "max"), ("min", "min"), ("mean", "avg"), ("sum", "total")) if lg.get(k)]
    fill = p.get("fill", 1)
    defaults = {
        "unit": y0.get("format") or "short",
        "custom": {
            "drawStyle": "line", "lineWidth": p.get("linewidth", 1), "fillOpacity": min(fill * 10, 100),
            "showPoints": "never", "spanNulls": p.get("nullPointMode") == "connected",
            "stacking": {"mode": "normal" if p.get("stack") else "none", "group": "A"},
        },
        "thresholds": {"mode": "absolute", "steps": [{"color": GREEN, "value": None}]},
        "links": [],
    }
    if y0.get("decimals") is not None:
        defaults["decimals"] = y0["decimals"]
    if y0.get("min") not in (None, ""):
        defaults["min"] = float(y0["min"])
    out = {k: v for k, v in p.items() if k not in _GRAPH_KEYS and k != "fieldConfig" and k != "options"}
    out["type"] = "timeseries"
    out["fieldConfig"] = {"defaults": defaults, "overrides": []}
    out["options"] = {
        "legend": {
            "displayMode": "table" if lg.get("alignAsTable") else "list",
            "placement": "right" if lg.get("rightSide") else "bottom",
            "showLegend": lg.get("show", True),
            "calcs": calcs,
        },
        "tooltip": {"mode": "multi" if (p.get("tooltip") or {}).get("shared") else "single", "sort": "desc"},
    }
    return out


# ---------------------------------------------------------------------------
# Mixins rendered with their own defaults (cert-manager)
# ---------------------------------------------------------------------------
def render_mixin_defaults(text: str, selectors: dict, default: str = "") -> str:
    """The cert-manager mixin's dashboard is a jsonnet file that is one
    substitution away from JSON: `"expr": "...{%(xSelector)s}..." % $._config`.
    Substitute each selector with its single-cluster default from the mixin's
    own config.libsonnet (`selectors`, keyed by the name, `default` for the
    rest) and drop the trailing `% $._config...`."""

    def sel(m):
        return selectors.get(m.group(1), default).replace('"', '\\"')

    text = re.sub(r"%\((\w+)\)s", sel, text)
    text = re.sub(r'"\s*%\s*\$\._config(\.dashboards)?', '"', text)
    text = re.sub(r"if \$\._config\.\w+ then 0 else 2", "2", text)
    return text

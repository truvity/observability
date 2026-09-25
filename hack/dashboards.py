"""Fetch and rewrite the generic dashboard set from hack/dashboards/sources.yaml.

Every dashboard here is upstream's own, pinned by release, rewritten to
the contract docs/dashboards.md defines and committed — a render needs no
network, the way `hack/crds.sh` vendors CRDs instead of resolving them at
render time.

The rewrite is mechanical, not a rebuild: it renames the existing
datasource variable to `datasource`, adds (or, for kubelet, relabels) a
`cluster` variable chained off it, injects a `k8s_cluster_name=~"$cluster"`
filter into every query that names a metric, and appends `($cluster)` to
the title. It does NOT invent panels, queries or thresholds — a dashboard
that needed more than that to pass the lint would be a rewrite, not a
fetch, and this script fails loudly instead of guessing.
"""

import json
import re
import sys
import urllib.request
from pathlib import Path

import yaml

ROOT = Path(__file__).resolve().parent.parent
SOURCES = ROOT / "hack" / "dashboards" / "sources.yaml"
OUT_DIR = ROOT / "charts" / "observability-dashboards" / "dashboards"

CLUSTER_LABEL = "k8s_cluster_name"
DATASOURCE_TOKEN = "__DATASOURCE_UID__"

# Metric selectors that carry no `[range]` and no `{labels}` at all, so
# neither brace-injection rule below has anywhere to land. Each is an
# exact literal in the fetched JSON text, found once, by hand — not a
# pattern, because a pattern loose enough to catch a bare metric name
# also catches an aggregation's `by (job)` clause, which is not one.
BARE_METRIC_PATCHES = {
    "victoriametrics-operator": [
        (
            "sum(operator_prometheus_converter_active_watchers)",
            'sum(operator_prometheus_converter_active_watchers{%s=~"$cluster"})' % CLUSTER_LABEL,
        ),
    ],
}

# label_values(identifier, label) with no braces at all -> the most
# common bare shape, from a dashboard's own job/instance variable
# queries; matched first so the patterns below never have to.
_SEL_LABELVALUES_BARE = re.compile(r"(label_values\()([a-zA-Z_:][a-zA-Z0-9_:]*)\s*,")
# identifier{...}  -> inject the filter just inside the opening brace.
_SEL_NAMED = re.compile(r"([a-zA-Z_:][a-zA-Z0-9_:]*)\{")
# {__name__=...}   -> a selector with no leading metric name.
_SEL_BARE = re.compile(r"(?<![a-zA-Z0-9_:}])\{")
# identifier[range] with no braces at all -> inject a selector before the range.
_SEL_RANGE = re.compile(r"([a-zA-Z_:][a-zA-Z0-9_:]*)\[")


def inject_cluster_filter(expr: str) -> str:
    """Add a k8s_cluster_name=~"$cluster" filter to every metric selector
    in a PromQL/MetricsQL expression string. Three shapes, in order:
    `metric{...}`, a bare `{__name__=...}` selector, and `metric[range]`
    with no braces at all. Left untouched: function/aggregation names,
    `by (...)`/`without (...)` grouping labels, and anything already
    filtered (idempotent re-runs do not double-inject).
    """

    out = []
    i = 0
    for m in _SEL_LABELVALUES_BARE.finditer(expr):
        out.append(expr[i : m.start()])
        out.append('%s%s{%s=~"$cluster"},' % (m.group(1), m.group(2), CLUSTER_LABEL))
        i = m.end()
    out.append(expr[i:])
    expr = "".join(out)

    out = []
    i = 0
    for m in _SEL_NAMED.finditer(expr):
        out.append(expr[i : m.end()])
        rest = expr[m.end() :]
        if rest.startswith(CLUSTER_LABEL + "="):
            pass  # this brace was just filled by the pass above; do not double-inject.
        elif rest.startswith("}"):
            out.append('%s=~"$cluster"' % CLUSTER_LABEL)
        else:
            out.append('%s=~"$cluster",' % CLUSTER_LABEL)
        i = m.end()
    out.append(expr[i:])
    expr = "".join(out)

    out = []
    i = 0
    for m in _SEL_BARE.finditer(expr):
        out.append(expr[i : m.end()])
        rest = expr[m.end() :]
        if rest.startswith(CLUSTER_LABEL + "="):
            pass
        elif rest.startswith("}"):
            out.append('%s=~"$cluster"' % CLUSTER_LABEL)
        else:
            out.append('%s=~"$cluster",' % CLUSTER_LABEL)
        i = m.end()
    out.append(expr[i:])
    expr = "".join(out)

    out = []
    i = 0
    for m in _SEL_RANGE.finditer(expr):
        # Skip if this identifier is immediately preceded by one we just
        # gave a brace to (i.e. it is the label name, not a metric) — not
        # reachable here since named/bare selectors already consumed
        # their braces; a metric followed by `[` with no braces is always
        # a bare range selector.
        out.append(expr[i : m.start()])
        out.append('%s{%s=~"$cluster"}[' % (m.group(1), CLUSTER_LABEL))
        i = m.end()
    out.append(expr[i:])
    return "".join(out)


def already_filtered(expr: str) -> bool:
    return ("%s=" % CLUSTER_LABEL) in expr or ("%s=~" % CLUSTER_LABEL) in expr


def names_a_metric(expr: str) -> bool:
    return bool(
        _SEL_NAMED.search(expr)
        or _SEL_BARE.search(expr)
        or _SEL_RANGE.search(expr)
        or _SEL_LABELVALUES_BARE.search(expr)
    )


def walk_panels(panels):
    for p in panels or []:
        yield p
        yield from walk_panels(p.get("panels"))


def rewrite_exprs(dashboard, skip_var: str) -> None:
    """Inject the cluster filter into every panel target and every other
    template variable's query — except the one that defines `cluster`
    itself, which must stay unfiltered to list every cluster there is.
    """
    for panel in walk_panels(dashboard.get("panels")):
        for target in panel.get("targets") or []:
            expr = target.get("expr")
            if isinstance(expr, str) and expr and not already_filtered(expr):
                target["expr"] = inject_cluster_filter(expr)

    for var in dashboard.get("templating", {}).get("list", []):
        if var.get("name") in (skip_var, "datasource") or var.get("type") not in ("query",):
            continue
        q = var.get("query")
        if isinstance(q, str):
            if not already_filtered(q):
                var["query"] = inject_cluster_filter(q)
        elif isinstance(q, dict) and isinstance(q.get("query"), str):
            if not already_filtered(q["query"]):
                q["query"] = inject_cluster_filter(q["query"])
        if isinstance(var.get("definition"), str) and not already_filtered(var["definition"]):
            var["definition"] = inject_cluster_filter(var["definition"])


def verify_every_query_filtered(dashboard, name: str) -> None:
    """The build-time half of lint rule 2: fail loudly, naming the panel
    and the expression, rather than commit a dashboard the lint would
    only fail later with less context.
    """
    for panel in walk_panels(dashboard.get("panels")):
        for target in panel.get("targets") or []:
            expr = target.get("expr")
            if isinstance(expr, str) and names_a_metric(expr) and not already_filtered(expr):
                raise SystemExit(
                    "%s: panel %r has an unfiltered query after rewrite: %s\n"
                    "Add a literal patch to BARE_METRIC_PATCHES in hack/dashboards.py, "
                    "or drop this dashboard from hack/dashboards/sources.yaml."
                    % (name, panel.get("title"), expr)
                )


def rename_datasource_var(dashboard) -> str:
    tvars = dashboard.get("templating", {}).get("list", [])
    ds = [v for v in tvars if v.get("type") == "datasource"]
    if len(ds) != 1:
        raise SystemExit("expected exactly one datasource variable, found %d" % len(ds))
    old_name = ds[0]["name"]
    ds[0]["name"] = "datasource"
    ds[0]["current"] = {"selected": True, "text": DATASOURCE_TOKEN, "value": DATASOURCE_TOKEN}
    ds[0]["options"] = []

    old_dollar = "$" + old_name
    old_brace = "${" + old_name + "}"

    def rename_in(obj):
        if isinstance(obj, dict):
            for k, v in obj.items():
                obj[k] = rename_in(v)
            return obj
        if isinstance(obj, list):
            return [rename_in(v) for v in obj]
        if isinstance(obj, str) and old_name != "datasource":
            obj = obj.replace(old_brace, "${datasource}")
            obj = re.sub(r"\$%s\b" % re.escape(old_name), "$datasource", obj)
            return obj
        return obj

    rename_in(dashboard)
    return old_name


def insert_cluster_var(dashboard, seed_query: str) -> None:
    tvars = dashboard.setdefault("templating", {}).setdefault("list", [])
    idx = next(i for i, v in enumerate(tvars) if v["name"] == "datasource")
    cluster_query = "label_values(%s, %s)" % (seed_query, CLUSTER_LABEL)
    cluster_var = {
        "current": {"selected": False, "text": "", "value": ""},
        "datasource": {"type": "prometheus", "uid": "$datasource"},
        "definition": cluster_query,
        "hide": 0,
        "includeAll": False,
        "label": "cluster",
        "multi": False,
        "name": "cluster",
        "options": [],
        "query": {"query": cluster_query, "refId": "cluster-Variable-Query"},
        "refresh": 2,
        "regex": "",
        "skipUrlSync": False,
        "sort": 1,
        "type": "query",
    }
    tvars.insert(idx + 1, cluster_var)


def relabel_bare_cluster(dashboard) -> None:
    """kubelet.json only: rename the bare `cluster` label upstream already
    filters every query by, to `k8s_cluster_name` — see sources.yaml's
    comment on the `kubelet` entry for why the label, not the shape, is
    wrong.
    """

    def rename_in(obj):
        if isinstance(obj, dict):
            for k, v in obj.items():
                obj[k] = rename_in(v)
            return obj
        if isinstance(obj, list):
            return [rename_in(v) for v in obj]
        if isinstance(obj, str):
            obj = obj.replace('cluster="$cluster"', '%s="$cluster"' % CLUSTER_LABEL)
            obj = obj.replace(", cluster)", ", %s)" % CLUSTER_LABEL)
            return obj
        return obj

    rename_in(dashboard)
    for var in dashboard.get("templating", {}).get("list", []):
        if var.get("name") == "cluster":
            var["label"] = "cluster"


def set_title(dashboard) -> None:
    title = dashboard.get("title", "")
    if "$cluster" not in title:
        dashboard["title"] = "%s ($cluster)" % title


def stable_uid(name: str) -> str:
    return ("truvity-obs-" + name)[:40]


def fetch(url: str) -> str:
    with urllib.request.urlopen(url, timeout=30) as resp:  # noqa: S310 - pinned, https, build-time only
        return resp.read().decode("utf-8")


def load_bundle_dashboard(bundle_url: str, key: str) -> dict:
    raw = fetch(bundle_url)
    doc = yaml.safe_load(raw)
    for item in doc.get("items", []):
        data = item.get("data", {})
        if key in data:
            return json.loads(data[key])
    raise SystemExit("bundle %s has no dashboard key %r" % (bundle_url, key))


def build_one(spec: dict, bundles: dict) -> None:
    name = spec["name"]
    if spec.get("bundle"):
        bundle = bundles[spec["bundle"]]
        url = bundle["url"].format(ref=bundle["ref"])
        dashboard = load_bundle_dashboard(url, spec["bundleKey"])
    else:
        url = spec["url"].format(ref=spec["ref"])
        dashboard = json.loads(fetch(url))

    dashboard["uid"] = stable_uid(name)

    if spec.get("preRenamed"):
        # kubelet: already has `datasource` and `cluster` variables in the
        # right shape; only the label under `cluster` needs to change.
        relabel_bare_cluster(dashboard)
    else:
        rename_datasource_var(dashboard)
        existing_cluster = any(
            v.get("name") == "cluster" for v in dashboard.get("templating", {}).get("list", [])
        )
        if not existing_cluster:
            insert_cluster_var(dashboard, spec["seedQuery"])
        rewrite_exprs(dashboard, skip_var="cluster")

    for literal, patched in BARE_METRIC_PATCHES.get(name, []):
        found = False
        for panel in walk_panels(dashboard.get("panels")):
            for target in panel.get("targets") or []:
                if target.get("expr") == literal:
                    target["expr"] = patched
                    found = True
        if not found:
            raise SystemExit(
                "%s: BARE_METRIC_PATCHES literal not found — upstream moved, "
                "update or remove the patch: %r" % (name, literal)
            )

    set_title(dashboard)
    verify_every_query_filtered(dashboard, name)

    out_path = OUT_DIR / ("%s.json" % name)
    out_path.write_text(json.dumps(dashboard, indent=2, ensure_ascii=False) + "\n")
    print("wrote %s (folder=%s)" % (out_path.relative_to(ROOT), spec["folder"]))


def write_catalog(manifest: dict) -> None:
    """The chart's own map of name -> {folder, file}, read by
    templates/dashboards.yaml via .Files.Get so the template never
    hand-maintains a second copy of what this script already knows.
    """
    catalog = {
        spec["name"]: {"folder": spec["folder"], "file": "%s.json" % spec["name"]}
        for spec in manifest["dashboards"]
    }
    path = OUT_DIR / "catalog.yaml"
    lines = [
        "# Generated by hack/dashboards.sh. Do not edit.",
        "#",
        "# name -> {folder, file}, read by templates/dashboards.yaml. The",
        "# source of truth for what belongs to which folder is",
        "# hack/dashboards/sources.yaml; this is its render-time shadow.",
        "",
    ]
    lines.append(yaml.safe_dump(catalog, sort_keys=True, default_flow_style=False).rstrip())
    path.write_text("\n".join(lines) + "\n")
    print("wrote %s" % path.relative_to(ROOT))


def main() -> None:
    manifest = yaml.safe_load(SOURCES.read_text())
    OUT_DIR.mkdir(parents=True, exist_ok=True)
    failures = []
    for spec in manifest["dashboards"]:
        try:
            build_one(spec, manifest.get("bundles", {}))
        except SystemExit as e:
            failures.append("%s: %s" % (spec["name"], e))
    if failures:
        print("\n".join(failures), file=sys.stderr)
        sys.exit(1)
    write_catalog(manifest)


if __name__ == "__main__":
    main()

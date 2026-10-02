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
sys.path.insert(0, str(ROOT / "hack" / "dashboards"))

import fleet_overview  # noqa: E402  (hack/dashboards/fleet_overview.py)
import kargo as kargo_dashboard  # noqa: E402  (hack/dashboards/kargo.py)
import keycloak as keycloak_dashboard  # noqa: E402  (hack/dashboards/keycloak.py)
import platform_extras  # noqa: E402  (hack/dashboards/platform_extras.py)
import platform_adapt  # noqa: E402  (hack/dashboards/platform_adapt.py)
from descriptions import DESCRIPTIONS, NODE_DESCRIPTIONS  # noqa: E402

AVAILABLE = ROOT / "hack" / "dashboards" / "available-metrics.yaml"
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


# ---------------------------------------------------------------------------
# Navigation: every shipped dashboard carries the same links row (tag-based,
# so a dashboard added later appears in it without editing the others) and
# the tags that feed it. `includeVars` carries datasource/cluster/namespace
# across, which works because every dashboard names them identically.
# ---------------------------------------------------------------------------
NAV_LINKS = [
    ("Fleet overview", "observability-fleet", "The home page: is anything wrong, and where?"),
    ("Kubernetes views", "observability-kubernetes", "Cluster, namespace and pod drill-down views"),
    ("Store dashboards", "observability-stores", "The observability stack's own health"),
]

# Carried by the platform-component dashboards only, so the dashboards that
# shipped before them are byte-for-byte what they were.
PLATFORM_NAV_LINK = ("Platform dashboards", "observability-platform", "ArgoCD, cert-manager, CloudNativePG, NATS, Envoy Gateway and Kargo")


def add_navigation(dashboard, spec) -> None:
    tags = list(dashboard.get("tags") or [])
    for t in spec.get("tags", []):
        if t not in tags:
            tags.append(t)
    dashboard["tags"] = tags
    links = list(dashboard.get("links") or [])
    have = {l.get("title") for l in links}
    nav = list(NAV_LINKS)
    if "observability-platform" in spec.get("tags", []):
        nav.append(PLATFORM_NAV_LINK)
    for title, tag, tip in nav:
        if title in have:
            continue
        links.append({
            "type": "dashboards",
            "title": title,
            "tags": [tag],
            "asDropdown": True,
            "includeVars": True,
            "keepTime": True,
            "icon": "external link",
            "tooltip": tip,
            "targetBlank": False,
            "url": "",
        })
    dashboard["links"] = links


# ---------------------------------------------------------------------------
# The metrics a store holds (hack/dashboards/available-metrics.yaml).
# ---------------------------------------------------------------------------
def _load_available():
    doc = yaml.safe_load(AVAILABLE.read_text())
    return doc["sources"]


_SOURCES = None

# Prefixes of the families a dashboard imported here might read. A name
# outside this pattern (a label, a function) is never mistaken for a metric.
_METRIC_RE = re.compile(
    r"(?<![A-Za-z0-9_:\"$.])"
    r"((?:kube|kubelet|container|machine|node|kargo|argocd|certmanager|cnpg|nats|envoy|karpenter|barman)_[A-Za-z0-9_:]*)"
)


def metric_available(name: str, optional=()) -> bool:
    """Whether a store holds `name`. A source marked `onlyWith: <x>` in
    available-metrics.yaml (node-exporter, which a cluster runs only when
    `nodeExporter.enabled`) counts only when `<x>` is in `optional`: a
    dashboard that `requires:` it in sources.yaml passes that, and every
    other dashboard is held to the default install, where it is absent.
    """
    global _SOURCES
    if _SOURCES is None:
        _SOURCES = _load_available()
    for src in _SOURCES:
        if src.get("onlyWith") and src["onlyWith"] not in optional:
            continue
        if name in src.get("deny", []):
            continue
        if name.endswith(tuple(src.get("denySuffixes", []))) and name not in src.get("keep", []):
            continue
        if name in src.get("names", []) or name in src.get("keep", []):
            return True
        if any(name.startswith(p) for p in src.get("prefixes", [])):
            return True
    return False


def metrics_in(expr: str):
    return _METRIC_RE.findall(expr)


def expr_available(expr: str, optional=()) -> bool:
    return all(metric_available(m, optional) for m in metrics_in(expr))


def _strip_absent(dashboard, name: str, optional=()) -> None:
    """Drop every target that reads a metric the store does not hold, then
    every panel left with no target. A panel that can only ever be empty is
    not shipped; each drop is printed so the review sees it.
    """
    panels = []
    for p in dashboard.get("panels", []):
        if p.get("type") == "row":
            panels.append(p)
            continue
        kept = []
        for t in p.get("targets") or []:
            e = t.get("expr", "")
            if e and not expr_available(e, optional):
                missing = sorted({m for m in metrics_in(e) if not metric_available(m, optional)})
                print("  %s: drop query of %r (absent: %s)" % (name, p.get("title"), ", ".join(missing)))
                continue
            kept.append(t)
        if p.get("targets") and not kept:
            print("  %s: drop panel %r" % (name, p.get("title")))
            continue
        p["targets"] = kept
        panels.append(p)
    dashboard["panels"] = panels


def _relayout(dashboard) -> None:
    """Close the holes dropped panels leave: bands (panels sharing a `y`)
    that lost a member are re-spread across the 24 columns, and every band
    is restacked with no gap under the one above it.
    """
    panels = dashboard["panels"]
    bands = {}
    for p in panels:
        bands.setdefault(p["gridPos"]["y"], []).append(p)
    cursor = 0
    for y in sorted(bands):
        band = sorted(bands[y], key=lambda p: p["gridPos"]["x"])
        if band[0].get("type") == "row":
            for p in band:
                p["gridPos"]["y"] = cursor
            cursor += 1
            continue
        total = sum(p["gridPos"]["w"] for p in band)
        h = max(p["gridPos"]["h"] for p in band)
        if total != 24 and len({p["gridPos"]["h"] for p in band}) == 1:
            x = 0
            for i, p in enumerate(band):
                w = 24 - x if i == len(band) - 1 else round(p["gridPos"]["w"] * 24 / total)
                p["gridPos"].update({"x": x, "w": w})
                x += w
        for p in band:
            p["gridPos"]["y"] = cursor
        cursor += h
    panels.sort(key=lambda p: (p["gridPos"]["y"], p["gridPos"]["x"]))


# Panels the node-exporter-based originals answered with node_* series a
# store does not hold. cadvisor's root cgroup (`id="/"`, container empty)
# is the node's own total, so utilisation is answered from it, against
# machine_cpu_cores / machine_memory_bytes.
_C = 'k8s_cluster_name="$cluster"'
_CPU = 'sum(rate(container_cpu_usage_seconds_total{id="/", %s}[$__rate_interval]))' % _C
_MEM = 'sum(container_memory_working_set_bytes{id="/", %s})' % _C
_CPU_N = 'sum by (instance) (rate(container_cpu_usage_seconds_total{id="/", %s}[$__rate_interval]))' % _C
_MEM_N = 'sum by (instance) (container_memory_working_set_bytes{id="/", %s})' % _C
REPLACE_QUERIES = {
    "Global CPU  Usage": (_CPU + ' / sum(machine_cpu_cores{%s})' % _C, ""),
    "Global RAM Usage": (_MEM + ' / sum(machine_memory_bytes{%s})' % _C, ""),
    "CPU Usage": (_CPU, ""),
    "RAM Usage": (_MEM, ""),
    "Cluster CPU Utilization": (_CPU + ' / sum(machine_cpu_cores{%s})' % _C, "cluster"),
    "Cluster Memory Utilization": (_MEM + ' / sum(machine_memory_bytes{%s})' % _C, "cluster"),
    "CPU Utilization by instance": (_CPU_N + ' / sum by (instance) (machine_cpu_cores{%s})' % _C, "{{instance}}"),
    "Memory Utilization by instance": (_MEM_N, "{{instance}}"),
}

# Drill-down: a series click opens the next level down with its label carried.
_KEEP = "var-datasource=${datasource:queryparam}&var-cluster=${cluster}&${__url_time_range}"
DRILL = {
    "CPU Utilization by namespace": ("truvity-obs-k8s-views-namespaces", "namespace"),
    "Memory Utilization by namespace": ("truvity-obs-k8s-views-namespaces", "namespace"),
    "CPU usage by Pod": ("truvity-obs-k8s-views-pods", "pod"),
    "Memory usage by Pod": ("truvity-obs-k8s-views-pods", "pod"),
}
_DRILL_TITLE = {"namespace": "Open this namespace", "pod": "Open this pod"}


def _sub(obj, fn):
    if isinstance(obj, dict):
        return {k: _sub(v, fn) for k, v in obj.items()}
    if isinstance(obj, list):
        return [_sub(v, fn) for v in obj]
    if isinstance(obj, str):
        return fn(obj)
    return obj


def _bare_cluster_to_label(s: str) -> str:
    s = re.sub(r'(?<![A-Za-z0-9_])cluster(=~?"\$\{?cluster\}?")', CLUSTER_LABEL + r"\1", s)
    s = s.replace("label_values(kube_node_info,cluster)", "label_values(kube_node_info, %s)" % CLUSTER_LABEL)
    return s


def _strip_job(s: str) -> str:
    # `job` was upstream's node-exporter / kube-state-metrics job picker.
    # The store has one job per scrape and the cluster variable already
    # scopes, so the picker is dropped rather than left empty.
    s = re.sub(r',\s*job=~?"\$job"', "", s)
    s = re.sub(r'job=~?"\$job",\s*', "", s)
    s = re.sub(r'job=~?"\$job"', "", s)
    return s


def adapt_k8s_views(dashboard, name: str, optional=()) -> None:
    """Adapt one dotdc/grafana-dashboards-kubernetes view to this store:
    its bare `cluster` label becomes `k8s_cluster_name`; queries and
    panels that read a metric the store does not hold are replaced (from
    cadvisor's root cgroup) or dropped; the `job` picker goes; every panel
    gets a description, a unit and, where a series names the next level
    down, a data link to it.
    """
    for k in ("__inputs", "__elements", "__requires"):
        dashboard.pop(k, None)
    dashboard["annotations"]["list"] = [a for a in dashboard["annotations"]["list"] if a.get("builtIn")]
    dashboard["editable"] = False

    fixed = _sub(dashboard, lambda s: _strip_job(_bare_cluster_to_label(s)))
    dashboard.clear()
    dashboard.update(fixed)

    dashboard["templating"]["list"] = [v for v in dashboard["templating"]["list"] if v["name"] != "job"]

    for p in dashboard["panels"]:
        title = p.get("title")
        # The cluster-level replacements answer node_* panels from cadvisor
        # for a store with no node-exporter; a dashboard that requires
        # node-exporter keeps its own queries.
        if title in REPLACE_QUERIES and p.get("targets") and not optional:
            expr, legend = REPLACE_QUERIES[title]
            p["targets"] = p["targets"][:1]
            p["targets"][0]["expr"] = expr
            if legend == "cluster":
                legend = ""
            p["targets"][0]["legendFormat"] = legend

    # Upstream leaves a few panels (the pod view's two issue tables)
    # unscoped by cluster: on a shared store they list every cluster's pods.
    for p in dashboard["panels"]:
        for t in p.get("targets") or []:
            e = t.get("expr")
            if isinstance(e, str) and e and names_a_metric(e) and not already_filtered(e):
                t["expr"] = inject_cluster_filter(e)

    _strip_absent(dashboard, name, optional)
    _relayout(dashboard)

    for p in dashboard["panels"]:
        if p.get("type") == "row":
            continue
        title = p.get("title", "")
        if not p.get("description") and optional and title in NODE_DESCRIPTIONS:
            p["description"] = NODE_DESCRIPTIONS[title]
        if not p.get("description"):
            if title not in DESCRIPTIONS:
                raise SystemExit("%s: panel %r has no description and none in hack/dashboards/descriptions.py" % (name, title))
            p["description"] = DESCRIPTIONS[title]
        defaults = p.setdefault("fieldConfig", {}).setdefault("defaults", {})
        if p.get("type") in ("stat", "gauge", "bargauge", "timeseries") and not defaults.get("unit"):
            defaults["unit"] = "short"
        if title in DRILL:
            uid, label = DRILL[title]
            defaults["links"] = [{
                "title": _DRILL_TITLE[label],
                "url": "/d/%s?%s&var-%s=${__field.labels.%s}" % (uid, _KEEP, label, label),
                "targetBlank": False,
            }]



def build_one(spec: dict, bundles: dict) -> None:
    name = spec["name"]
    if spec.get("generator"):
        dashboard = GENERATORS[spec["generator"]].build()
        url = spec["generator"]
    elif spec.get("bundle"):
        bundle = bundles[spec["bundle"]]
        url = bundle["url"].format(ref=bundle["ref"])
        dashboard = load_bundle_dashboard(url, spec["bundleKey"])
    else:
        url = spec["url"].format(ref=spec["ref"])
        raw = fetch(url)
        if spec.get("mixinDefaults"):
            raw = platform_adapt.render_mixin_defaults(raw, spec["mixinDefaults"])
        dashboard = json.loads(raw)

    dashboard["uid"] = stable_uid(name)

    if spec.get("generator"):
        # Authored to the contract already: only the datasource variable's
        # default (the placeholder the chart substitutes) is stamped.
        for v in dashboard["templating"]["list"]:
            if v["type"] == "datasource":
                v["current"] = {"selected": True, "text": DATASOURCE_TOKEN, "value": DATASOURCE_TOKEN}
    elif spec.get("adapt") == "k8s-views":
        rename_datasource_var(dashboard)
        adapt_k8s_views(dashboard, name, tuple(spec.get("requires", [])))
    elif spec.get("adapt") == "platform":
        REPORTS[name] = platform_adapt.adapt(dashboard, spec, lambda m, _o=tuple(spec.get("requires", [])): metric_available(m, _o), platform_extras.extras)
    elif spec.get("preRenamed"):
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
    add_navigation(dashboard, spec)
    if spec.get("upstream"):
        up = MANIFEST["upstreams"][spec["upstream"]]
        note = ("Adapted from %s (%s, %s); modified. See THIRD_PARTY_NOTICES.md."
                % (up["project"], up["license"], up["url"]))
        desc = (dashboard.get("description") or "").strip()
        # Upstream's own description may already carry an older notice.
        dashboard["description"] = (desc + " " + note).strip() if note not in desc else desc
    verify_every_query_filtered(dashboard, name)

    out_path = OUT_DIR / ("%s.json" % name)
    out_path.write_text(json.dumps(dashboard, indent=2, ensure_ascii=False) + "\n")
    print("wrote %s (folder=%s)" % (out_path.relative_to(ROOT), spec["folder"]))


def write_catalog(manifest: dict) -> None:
    """The chart's own map of name -> {folder, file}, read by
    templates/dashboards.yaml via .Files.Get so the template never
    hand-maintains a second copy of what this script already knows.
    """
    catalog = {}
    for spec in manifest["dashboards"]:
        entry = {"folder": spec["folder"], "file": "%s.json" % spec["name"]}
        # Provenance of a dashboard adopted from a third party, and whether
        # tests/dashboard_queries_test.go holds it to the available-metrics
        # allow-list.
        for key in ("upstream", "authored", "ref", "queryCheck", "requires", "deferred"):
            if key in spec:
                entry[key] = spec[key]
        catalog[spec["name"]] = entry
    path = OUT_DIR / "catalog.yaml"
    lines = [
        "# Generated by hack/dashboards.sh. Do not edit.",
        "#",
        "# name -> {folder, file, ...}, read by templates/dashboards.yaml. The",
        "# source of truth for what belongs to which folder is",
        "# hack/dashboards/sources.yaml; this is its render-time shadow.",
        "",
    ]
    lines.append(yaml.safe_dump(catalog, sort_keys=True, default_flow_style=False).rstrip())
    path.write_text("\n".join(lines) + "\n")
    print("wrote %s" % path.relative_to(ROOT))


MANIFEST: dict = {}
REPORTS: dict = {}
GENERATORS = {
    "hack/dashboards/fleet_overview.py": fleet_overview,
    "hack/dashboards/kargo.py": kargo_dashboard,
    "hack/dashboards/keycloak.py": keycloak_dashboard,
}


def write_notices(manifest: dict) -> None:
    """THIRD_PARTY_NOTICES.md, rendered from sources.yaml so it cannot
    drift from what is vendored. Written to the repository root and, byte
    for byte, into the chart directory (which is what `helm package`
    ships); LICENSES/ is copied the same way.
    """
    lines = [
        "# Third-party notices",
        "",
        "<!-- Generated by hack/dashboards.py from hack/dashboards/sources.yaml. Do not edit. -->",
        "",
        "This repository redistributes dashboards from the projects below, in",
        "`charts/observability-dashboards/dashboards/`. Each is used under the",
        "licence named, whose full text is in `LICENSES/`. Every one is",
        "**modified**: rewritten to this repository's dashboard contract",
        "(datasource/cluster/namespace variables, titles, links), and where",
        "stated, with panels replaced or removed. None of the upstream projects",
        "ships a NOTICE file at the pinned ref.",
        "",
    ]
    for spec in manifest["dashboards"]:
        if not spec.get("upstream"):
            continue
        up = manifest["upstreams"][spec["upstream"]]
        if spec.get("bundle"):
            b = manifest["bundles"][spec["bundle"]]
            ref, where = b["ref"], b["url"].format(ref=b["ref"]) + " (key `%s`)" % spec["bundleKey"]
        else:
            ref, where = spec["ref"], spec["url"].format(ref=spec["ref"])
        lines += [
            "## %s" % spec["name"],
            "",
            "- Upstream project: %s" % up["project"],
            "- URL: %s" % up["url"],
            "- Fetched from: %s" % where,
            "- Pinned ref: `%s`" % ref,
            "- SPDX licence: %s (`LICENSES/%s.txt`)" % (up["license"], up["license"]),
            "- Copyright: %s" % up["copyright"],
        ]
        if up.get("notice"):
            lines.append("- Upstream NOTICE: %s" % up["notice"])
        lines += [
            "- Modified: rewritten to this repository's dashboard contract "
            "(datasource/cluster/namespace variables, titles, links)"
            + ("; panels on metrics a store does not hold replaced or removed" if spec.get("adapt") else ""),
            "",
        ]
    text = "\n".join(lines)
    for dest in (ROOT, ROOT / "charts" / "observability-dashboards"):
        (dest / "THIRD_PARTY_NOTICES.md").write_text(text)
        (dest / "LICENSES").mkdir(exist_ok=True)
        for f in (ROOT / "LICENSES").glob("*.txt"):
            (dest / "LICENSES" / f.name).write_bytes(f.read_bytes())
    print("wrote THIRD_PARTY_NOTICES.md (root and chart)")


def main() -> None:
    manifest = yaml.safe_load(SOURCES.read_text())
    MANIFEST.update(manifest)
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
    write_notices(manifest)


if __name__ == "__main__":
    main()

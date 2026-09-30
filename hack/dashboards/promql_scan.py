"""A small PromQL/MetricsQL scanner for the import tooling.

hack/dashboards.py rewrites other projects' dashboards, and the rewrite has
to know which identifiers in a query are METRICS (to add the cluster filter
and to check the allow-list) and which are functions, keywords, grouping
labels or Grafana variables. A regular expression cannot tell `sum by (le)`
from a selector; this scanner walks the text once and can.

It is deliberately not a parser: it never evaluates anything. The Go test
(tests/dashboard_queries_test.go) parses every query with the real
Prometheus parser and then on a real VictoriaMetrics; this only has to be
right enough to rewrite what the parsers then confirm.
"""

import re

_IDENT_START = re.compile(r"[A-Za-z_:]")
_IDENT = re.compile(r"[A-Za-z0-9_:]*")

# Words that are never a metric. Grouping modifiers take a parenthesised
# label list, which is skipped whole.
_GROUPING = {"by", "without", "on", "ignoring", "group_left", "group_right"}
_KEYWORDS = _GROUPING | {"and", "or", "unless", "bool", "offset", "inf", "nan", "atan2", "start", "end"}


class Selector:
    """One metric selector in an expression: where its name is, whether it
    has a `{...}` block, and where that block's contents start and end."""

    def __init__(self, name, start, end, brace_open=None, brace_close=None):
        self.name = name  # "" for an anonymous `{__name__=~"..."}` selector
        self.start = start
        self.end = end  # end of the name (start of the brace, if any)
        self.brace_open = brace_open  # index of `{`
        self.brace_close = brace_close  # index of `}`


def _skip_string(expr, i):
    q = expr[i]
    i += 1
    while i < len(expr):
        if expr[i] == "\\" and q != "`":
            i += 2
            continue
        if expr[i] == q:
            return i + 1
        i += 1
    return i


def _skip_block(expr, i, open_c, close_c):
    """expr[i] == open_c; return the index just past the matching close_c,
    stepping over strings."""
    depth = 0
    while i < len(expr):
        c = expr[i]
        if c in "\"'`":
            i = _skip_string(expr, i)
            continue
        if c == open_c:
            depth += 1
        elif c == close_c:
            depth -= 1
            if depth == 0:
                return i + 1
        i += 1
    return i


def _next_nonspace(expr, i):
    while i < len(expr) and expr[i].isspace():
        i += 1
    return i


def selectors(expr: str):
    out = []
    i, n = 0, len(expr)
    while i < n:
        c = expr[i]
        if c in "\"'`":
            i = _skip_string(expr, i)
        elif c == "$":
            i += 1
            if i < n and expr[i] == "{":
                i = _skip_block(expr, i, "{", "}")
            else:
                while i < n and (expr[i].isalnum() or expr[i] == "_"):
                    i += 1
        elif c == "[":
            i = _skip_block(expr, i, "[", "]")
        elif c.isdigit():
            while i < n and (expr[i].isalnum() or expr[i] in "._"):
                i += 1
        elif c == "{":
            close = _skip_block(expr, i, "{", "}") - 1
            out.append(Selector("", i, i, i, close))
            i = close + 1
        elif _IDENT_START.match(c):
            m = _IDENT.match(expr, i)
            ident = m.group(0)
            end = m.end()
            j = _next_nonspace(expr, end)
            nxt = expr[j] if j < n else ""
            if ident.lower() in _GROUPING and nxt == "(":
                i = _skip_block(expr, j, "(", ")")
                continue
            if ident.lower() in _KEYWORDS:
                i = end
                continue
            if nxt == "(":  # a function
                i = end
                continue
            # `sum by (x) (...)`: an aggregation written with its grouping
            # before the operand.
            if re.match(r"(by|without)\b\s*\(", expr[j:], re.I):
                i = end
                continue
            if nxt == "{" and j == end:
                close = _skip_block(expr, j, "{", "}") - 1
                out.append(Selector(ident, i, end, j, close))
                i = close + 1
            else:
                out.append(Selector(ident, i, end))
                i = end
        else:
            i += 1
    return out


_NAME_RE = re.compile(r'__name__\s*=~?\s*"([^"]*)"')


def metrics_in(expr: str):
    """Every metric name an expression reads. An anonymous selector that
    matches `__name__` by regex yields the pattern prefixed `~`, which no
    allow-list names, so it is reported as absent rather than guessed."""
    names = []
    for s in selectors(expr):
        if s.name:
            names.append(s.name)
        else:
            body = expr[s.brace_open : s.brace_close + 1]
            m = _NAME_RE.search(body)
            names.append("~" + m.group(1) if m else "~")
    return names


def add_matcher(expr: str, matcher: str, only=None) -> str:
    """Add `matcher` (e.g. `k8s_cluster_name=~"$cluster"`) to every metric
    selector of `expr`. Idempotent: a selector that already names the label
    is left alone. `only`, if given, is a predicate on the metric name."""
    label = matcher.split("=", 1)[0].rstrip("!~")
    edits = []  # (index, text)
    for s in selectors(expr):
        if only is not None and s.name and not only(s.name):
            continue
        if s.brace_open is None:
            edits.append((s.end, "{%s}" % matcher))
            continue
        body = expr[s.brace_open + 1 : s.brace_close]
        if re.search(r"(?<![A-Za-z0-9_])%s\s*(=|!=|=~|!~)" % re.escape(label), body):
            continue
        sep = "," if body.strip() else ""
        edits.append((s.brace_open + 1, matcher + sep))
    for idx, text in sorted(edits, reverse=True):
        expr = expr[:idx] + text + expr[idx:]
    return expr


_LABEL_VALUES = re.compile(r"^(\s*label_values\()(.*?)(\s*,\s*)?([A-Za-z_][A-Za-z0-9_]*)(\s*\)\s*)$", re.S)


def split_label_values(q: str):
    """`label_values(<selector>, label)` -> (prefix, selector, label, suffix);
    `label_values(label)` -> selector None. None if `q` is another shape."""
    m = _LABEL_VALUES.match(q)
    if not m:
        return None
    prefix, sel, comma, label, suffix = m.groups()
    if comma is None:
        return (prefix, None, label, suffix)
    return (prefix, sel, label, suffix)

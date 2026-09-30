"""Driver for hack/mcp-paths-proof.sh (see its header)."""

import http.server
import json
import os
import re
import signal
import socketserver
import subprocess
import sys
import threading
import time
import urllib.request

import yaml

ROOT, WORK = sys.argv[1], sys.argv[2]

SERVERS = {
    # upstream key in values.yaml: (git repository, binary, main package)
    "victoriametrics": ("VictoriaMetrics-Community/mcp-victoriametrics", "mcp-victoriametrics"),
    "victorialogs": ("VictoriaMetrics-Community/mcp-victorialogs", "mcp-victorialogs"),
    "victoriatraces": ("VictoriaMetrics-Community/mcp-victoriatraces", "mcp-victoriatraces"),
}
# Per-tool arguments the tool's schema cannot say (a query language).
ARGS = {
    "query": {"victoriametrics": {"query": "up"}, "victorialogs": {"query": "*", "limit": 1}},
    "query_range": {"query": "up", "start": "2026-01-01T00:00:00Z", "end": "2026-01-01T01:00:00Z", "step": "1m"},
    "explain_query": {"query": "sum(rate(up[5m]))"},
    "series": {"match": "up"},
    "label_values": {"label": "job", "label_name": "job"},
    "hits": {"query": "*", "start": "2026-01-01T00:00:00Z", "end": "2026-01-01T01:00:00Z"},
    "facets": {"query": "*", "start": "2026-01-01T00:00:00Z", "end": "2026-01-01T01:00:00Z"},
    "stats_query": {"query": "* | stats count()", "time": "2026-01-01T00:00:00Z"},
    "stats_query_range": {"query": "* | stats count()", "start": "2026-01-01T00:00:00Z", "end": "2026-01-01T01:00:00Z"},
    "field_values": {"query": "*", "field": "level"},
    "stream_field_values": {"query": "*", "field": "app"},
    "trace": {"trace_id": "0123456789abcdef"},
    "service_operations": {"service_name": "frontend"},
    "traces": {"service_name": "frontend"},
}
EXPECTED_PATH = {
    "metrics": {
        "query": "/prometheus/api/v1/query", "query_range": "/prometheus/api/v1/query_range",
        "metrics": "/prometheus/api/v1/label/__name__/values", "labels": "/prometheus/api/v1/labels",
        "label_values": "/prometheus/api/v1/label/job/values", "series": "/prometheus/api/v1/series",
        "rules": "/prometheus/vmalert/api/v1/rules", "alerts": "/prometheus/vmalert/api/v1/alerts",
        "tsdb_status": "/prometheus/api/v1/status/tsdb", "explain_query": None,
    },
    "logs": {t: f"/select/logsql/{t}" for t in (
        "hits", "facets", "stats_query", "stats_query_range", "field_names", "field_values",
        "stream_field_names", "stream_field_values", "stream_ids", "streams", "query")},
    "traces": {
        "traces": "/select/jaeger/api/traces", "trace": "/select/jaeger/api/traces/0123456789abcdef",
        "services": "/select/jaeger/api/services",
        "service_operations": "/select/jaeger/api/services/frontend/operations",
        "dependencies": "/select/jaeger/api/dependencies",
    },
}


def run(*cmd, cwd=None, env=None):
    return subprocess.run(cmd, cwd=cwd, env=env, check=True, capture_output=True, text=True).stdout


def routes():
    """The path regexes vmauth would serve the reader principal."""
    out = []
    with open(os.path.join(ROOT, "tests/golden/observability-stack/tenancy-mcp-reader.yaml")) as f:
        for d in yaml.safe_load_all(f):
            if d and d.get("kind") == "VMUser" and d["spec"]["name"] == "example:mcp:reader":
                for ref in d["spec"]["targetRefs"]:
                    out += [re.compile("^(?:" + p + ")$") for p in ref["paths"]]
    assert out, "reader principal not found in the golden"
    return out


class Store(http.server.BaseHTTPRequestHandler):
    seen = []
    routes = []

    def log_message(self, *a):
        pass

    def do_GET(self):
        path = self.path.split("?")[0]
        Store.seen.append(path)
        if not any(r.match(path) for r in Store.routes):
            self.send_response(401)
            self.end_headers()
            self.wfile.write(b"unauthorized: no route")
            return
        if path.startswith("/select/logsql/"):
            body = b""
        elif path.startswith("/select/jaeger/"):
            body = b'{"data":[],"total":0,"limit":0,"offset":0,"errors":null}'
        elif "vmalert" in path:
            body = b'{"status":"success","data":{"groups":[],"alerts":[]}}'
        elif path.endswith("/values") or path.endswith("/labels"):
            body = b'{"status":"success","data":[]}'
        else:
            body = b'{"status":"success","data":{"resultType":"vector","result":[]}}'
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(body)


class Threaded(socketserver.ThreadingMixIn, http.server.HTTPServer):
    daemon_threads = True
    allow_reuse_address = True


def mcp(url, method, params=None, version="2025-06-18", session=None):
    body = {"jsonrpc": "2.0", "id": 1, "method": method}
    if params is not None:
        body["params"] = params
    headers = {"Content-Type": "application/json", "Accept": "application/json, text/event-stream",
               "Mcp-Protocol-Version": version}
    if session:
        headers["Mcp-Session-Id"] = session
    req = urllib.request.Request(url, data=json.dumps(body).encode(), headers=headers)
    with urllib.request.urlopen(req, timeout=60) as r:
        text = r.read().decode()
    data = [l[5:].strip() for l in text.splitlines() if l.startswith("data:")]
    return json.loads(data[-1] if data else text)


def arguments(tool, schema, server):
    given = ARGS.get(tool)
    if isinstance(given, dict) and server in given:
        given = given[server]
    if given is not None and not any(isinstance(v, dict) for v in given.values()):
        out = dict(given)
    else:
        out = {}
    for name in schema.get("required", []):
        if name in out:
            continue
        typ = schema["properties"][name].get("type")
        out[name] = {"number": 1, "integer": 1, "boolean": True, "array": ["up"]}.get(typ, "up")
    return out


def main():
    with open(os.path.join(ROOT, "charts/observability-mcp/values.yaml")) as f:
        values = yaml.safe_load(f)
    rendered = list(yaml.safe_load_all(run("helm", "template", "x", os.path.join(ROOT, "charts/observability-mcp"),
                                           "-f", os.path.join(ROOT, "tests/cases/observability-mcp/single-store/values.yaml"))))
    deploy = next(d for d in rendered if d and d["kind"] == "Deployment" and d["metadata"]["name"].endswith("-primary"))
    config = next(d for d in rendered if d and d["kind"] == "ConfigMap")["data"]["config.yaml"]
    containers = {c["name"]: c for c in deploy["spec"]["template"]["spec"]["containers"]}

    procs = []

    def stop(*_):
        for p in procs:
            p.terminate()

    signal.signal(signal.SIGTERM, stop)
    failures = []
    try:
        # Build the three stock servers from their pinned tags.
        bins = {}
        for key, (repo, binary) in SERVERS.items():
            tag = values["upstreams"][key]["image"]["tag"]
            src = os.path.join(WORK, f"{binary}-{tag}")
            exe = os.path.join(src, binary + ".bin")
            if not os.path.exists(exe):
                if not os.path.isdir(src):
                    run("git", "clone", "-q", "--depth", "1", "--branch", tag, f"https://github.com/{repo}", src)
                print(f"building {binary} {tag}")
                run("go", "build", "-mod=vendor", "-o", exe, f"./cmd/{binary}", cwd=src)
            bins[key] = exe
        agg = os.path.join(WORK, "mcp-aggregator.bin")
        run("go", "build", "-o", agg, "./cmd/mcp-aggregator", cwd=ROOT)

        # The stand-in for the proxy's outbound listener and the store's vmauth.
        Store.routes = routes()
        httpd = Threaded(("127.0.0.1", 8429), Store)
        threading.Thread(target=httpd.serve_forever, daemon=True).start()

        # The stock servers, with the environment the chart rendered.
        for key, cname in (("victoriametrics", "upstream-metrics"), ("victorialogs", "upstream-logs"), ("victoriatraces", "upstream-traces")):
            env = {e["name"]: e["value"] for e in containers[cname]["env"]}
            procs.append(subprocess.Popen([bins[key]], env={**os.environ, **env}, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL))
        cfg_path = os.path.join(WORK, "aggregator.yaml")
        with open(cfg_path, "w") as f:
            f.write(config)
        procs.append(subprocess.Popen([agg, "-config", cfg_path], stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL))

        url = "http://127.0.0.1:8081/mcp"
        for _ in range(120):
            try:
                urllib.request.urlopen("http://127.0.0.1:9090/readyz", timeout=2)
                break
            except Exception:
                time.sleep(0.5)
        else:
            raise SystemExit("the aggregator never became ready: a stock server did not start, or an allowlisted tool is missing")
        print("aggregator ready")

        listed = mcp(url, "tools/list")["result"]["tools"]
        print(f"{len(listed)} tools exposed")
        rows = []
        for tool in listed:
            prefix, _, name = tool["name"].partition("_")
            server = {"metrics": "victoriametrics", "logs": "victorialogs", "traces": "victoriatraces"}[prefix]
            Store.seen.clear()
            res = mcp(url, "tools/call", {"name": tool["name"], "arguments": arguments(name, tool["inputSchema"], server)})
            result = res.get("result", {})
            want = EXPECTED_PATH[prefix].get(name, "MISSING")
            got = list(dict.fromkeys(Store.seen))
            text = (result.get("content") or [{}])[0].get("text", "")[:80]
            ok = "error" not in res and not result.get("isError") and (want is None or want in got)
            if want == "MISSING":
                ok = False
            rows.append((tool["name"], want or "(local)", ",".join(got) or "-", "ok" if ok else "FAIL: " + text))
            if not ok:
                failures.append(rows[-1])
        w = max(len(r[0]) for r in rows)
        for r in rows:
            print(f"  {r[0]:<{w}}  {r[3]:<6} wants {r[1]:<55} saw {r[2]}")

        # The dropped tools, against the stock servers directly: each reaches a
        # path no route serves.
        print("dropped tools (stock server called directly):")
        for key, tool, port, args, path in (
            ("victoriametrics", "prettify_query", 8082, {"query": "sum(rate(up[5m]))"}, "/prometheus/prettify-query"),
            ("victorialogs", "flags", 8083, {}, "/flags"),
        ):
            # Run a second instance with the tool enabled (the chart disables it on the server too).
            env = {e["name"]: e["value"] for e in containers[{"victoriametrics": "upstream-metrics", "victorialogs": "upstream-logs"}[key]]["env"]}
            env["MCP_LISTEN_ADDR"] = f"127.0.0.1:{port + 100}"
            env["MCP_DISABLED_TOOLS"] = "documentation"
            p = subprocess.Popen([bins[key]], env={**os.environ, **env}, stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL)
            procs.append(p)
            time.sleep(1.5)
            Store.seen.clear()
            direct = f"http://127.0.0.1:{port + 100}/mcp"
            # The stock servers are stateful: open a session first.
            init = urllib.request.Request(direct, data=json.dumps({
                "jsonrpc": "2.0", "id": 0, "method": "initialize",
                "params": {"protocolVersion": "2025-03-26", "capabilities": {}, "clientInfo": {"name": "proof", "version": "1"}},
            }).encode(), headers={"Content-Type": "application/json", "Accept": "application/json, text/event-stream"})
            with urllib.request.urlopen(init, timeout=30) as r:
                sid = r.headers.get("Mcp-Session-Id")
            res = mcp(direct, "tools/call", {"name": tool, "arguments": args}, version="2025-03-26", session=sid)
            refused = any(not any(r.match(s) for r in Store.routes) for s in Store.seen)
            print(f"  {key}/{tool}: asked for {list(dict.fromkeys(Store.seen))}, refused by the routes: {refused}")
            if not refused or path not in Store.seen:
                failures.append((tool, path, str(Store.seen), "not dropped for the stated reason"))
    finally:
        stop()
    if failures:
        print("FAILURES:")
        for f in failures:
            print("  ", f)
        sys.exit(1)
    print("every exposed tool reached a served path; the dropped tools did not")


main()

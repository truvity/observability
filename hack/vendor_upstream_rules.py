"""Implementation of hack/vendor-upstream-rules.sh (see its header)."""
import hashlib
import http.server
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
import threading
import urllib.request

import yaml

ROOT, MODE = sys.argv[1], sys.argv[2]
CHART = os.path.join(ROOT, "charts", "platform-alerts")
UP = os.path.join(CHART, "upstream")
PIN = os.path.join(UP, "PIN.yaml")
STACK = os.path.join(ROOT, "charts", "observability-stack")
CASE = os.path.join(ROOT, "tests", "cases", "observability-stack", "minimal", "values.yaml")

CLUSTER = "__CLUSTER_LABEL__"
AM_NS = "__ALERTMANAGER_NAMESPACE__"
# Upstream's own expression of the one rule the stack overrides by default.
# The override moves to charts/platform-alerts/values.yaml; this guards it.
NODATA = "sum(vmalert_recording_rules_last_evaluation_samples) without(id) < 1"

RAW = re.compile(r"^https://raw\.githubusercontent\.com/([^/]+/[^/]+)/([^/]+)/(.+)$")


def sha(b):
    return hashlib.sha256(b).hexdigest()


def fetch(url):
    with urllib.request.urlopen(url, timeout=60) as r:
        return r.read()


def run(*a, **kw):
    return subprocess.run(a, check=True, text=True, capture_output=True, **kw).stdout


def head(repo, branch):
    out = run("git", "ls-remote", f"https://github.com/{repo}.git", f"refs/heads/{branch}")
    return out.split()[0]


def render_config():
    out = run("helm", "template", "x", STACK, "--values", CASE)
    cfg = image = None
    for d in yaml.safe_load_all(out):
        if not d:
            continue
        if d["kind"] == "ConfigMap" and d["metadata"]["name"].endswith("sync-job-config"):
            cfg = yaml.safe_load(d["data"]["config.yaml"])
        if d["kind"] == "Job" and "sync-job" in d["metadata"]["name"]:
            image = d["spec"]["template"]["spec"]["containers"][0]["image"]
    if cfg is None or image is None:
        sys.exit("the stack renders no sync job config or Job")
    return cfg, image


def stack_dep_version():
    c = yaml.safe_load(open(os.path.join(STACK, "Chart.yaml")))
    return next(d["version"] for d in c["dependencies"] if d["name"] == "victoria-metrics-k8s-stack")


def old_pins():
    if not os.path.exists(PIN):
        return {}
    p = yaml.safe_load(open(PIN))
    return {s["url"]: s for s in p["sources"]}


FAKE = r'''
import http.server, json, sys
class H(http.server.BaseHTTPRequestHandler):
    def _r(self, code, body):
        b = json.dumps(body).encode()
        self.send_response(code); self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def do_GET(self):
        if self.path.split("?")[0].endswith("s"):
            return self._r(200, {"kind": "List", "apiVersion": "v1", "items": [], "metadata": {}})
        self._r(404, {"kind": "Status", "apiVersion": "v1", "status": "Failure", "reason": "NotFound", "code": 404, "message": "nf"})
    def _w(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0)))
        open(sys.argv[2], "ab").write(json.dumps({"path": self.path, "body": body.decode()}).encode() + b"\n")
        try: self._r(200, json.loads(body))
        except Exception: self._r(200, {})
    do_POST = do_PUT = do_PATCH = _w
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
'''


def vendor(out):
    cfg, image = render_config()
    rules = cfg["rules"]
    # The stack's override of RecordingRulesNoData is not upstream: it moves to
    # platform-alerts' values.
    (rules.get("rules") or {}).pop("RecordingRulesNoData", None)
    if not rules.get("rules"):
        rules.pop("rules", None)

    # Groups the stack switches off by default stay vendored, default-off.
    default_off = sorted(g for g, v in rules["groups"].items() if v.get("enabled") is False)
    for g in rules["groups"].values():
        g.pop("enabled", None)
    for g, v in list(rules["groups"].items()):
        rw = v.get("labelRewrites") or {}
        if "namespace" in rw and rw["namespace"]["value"] not in (".*",):
            rw["namespace"]["value"] = AM_NS
        if not v:
            del rules["groups"][g]
    cfg["common"]["clusterLabel"] = CLUSTER

    # Only sources the stack renders enabled; pinned to a commit.
    old = old_pins()
    sources, pinned = [], []
    for s in rules["sources"]:
        if s.get("enabled") is False:
            continue
        m = RAW.match(s["url"])
        repo, branch, path = m.groups()
        prev = old.get(s["url"])
        if MODE == "update" or not prev:
            commit = head(repo, branch)
        else:
            commit = prev["commit"]
        url = f"https://raw.githubusercontent.com/{repo}/{commit}/{path}"
        body = fetch(url)
        sources.append({"url": url})
        doc = yaml.safe_load(body)
        groups = (doc.get("spec") or doc)["groups"]
        pinned.append({
            "url": s["url"], "repo": repo, "branch": branch, "commit": commit,
            "path": path, "sha256": sha(body), "groups": sorted(g["name"] for g in groups),
        })
    rules["sources"] = sources
    pinned.sort(key=lambda p: p["url"])
    cfg["dashboards"]["sources"] = []

    work = tempfile.mkdtemp(prefix="vendor-upstream-rules.")
    try:
        os.makedirs(os.path.join(work, "etc"))
        yaml.safe_dump(cfg, open(os.path.join(work, "etc", "config.yaml"), "w"))
        open(os.path.join(work, "fakeapi.py"), "w").write(FAKE)
        with open(os.path.join(work, "kubeconfig"), "w") as f:
            f.write('apiVersion: v1\nkind: Config\nclusters: [{name: c, cluster: {server: "http://127.0.0.1:18084"}}]\n'
                    'users: [{name: u, user: {token: t}}]\ncontexts: [{name: c, context: {cluster: c, user: u}}]\ncurrent-context: c\n')
        writes = os.path.join(work, "writes.jsonl")
        open(writes, "w").close()
        api = subprocess.Popen([sys.executable, "-I", os.path.join(work, "fakeapi.py"), "18084", writes])
        try:
            import time
            time.sleep(1)
            r = subprocess.run(
                ["docker", "run", "--rm", "--network", "host",
                 "-v", f"{work}/etc:/etc/config:ro", "-v", f"{work}/kubeconfig:/kc:ro",
                 "-e", "KUBECONFIG=/kc", image],
                text=True, capture_output=True)
            if r.returncode != 0:
                sys.exit("sync job failed:\n" + r.stdout + r.stderr)
        finally:
            api.terminate()
        groups = {}
        for line in open(writes):
            body = json.loads(json.loads(line)["body"])
            if body.get("kind") != "VMRule":
                continue
            for g in body["spec"]["groups"]:
                groups[g["name"]] = g
    finally:
        shutil.rmtree(work, ignore_errors=True)

    if not groups:
        sys.exit("the sync job applied no rule group")
    nodata = [r for r in groups.get("vmalert", {}).get("rules", []) if r.get("alert") == "RecordingRulesNoData"]
    if not nodata or " ".join(nodata[0]["expr"].split()) != NODATA:
        sys.exit("upstream's RecordingRulesNoData is no longer '%s': update the default override in "
                 "charts/platform-alerts/values.yaml and NODATA here" % NODATA)

    gdir = os.path.join(out, "groups")
    os.makedirs(gdir, exist_ok=True)
    src_of = {n: p["url"] for p in pinned for n in p["groups"]}
    entries = []
    for name in sorted(groups):
        g = groups[name]
        text = yaml.safe_dump(g, sort_keys=True, width=100000, default_flow_style=False, allow_unicode=True)
        fn = name + ".yaml"
        open(os.path.join(gdir, fn), "w").write(text)
        kinds = {("alert" if "alert" in r else "recording") for r in g["rules"]}
        entries.append({
            "name": name, "file": "groups/" + fn, "sha256": sha(text.encode()),
            "ruleType": "recording" if kinds == {"recording"} else "alert",
            "defaultEnabled": name not in default_off,
            "source": src_of[name],
            "rules": [r.get("alert") or r.get("record") for r in g["rules"]],
        })
    pin = {
        "generator": "hack/vendor-upstream-rules.sh",
        "stack": {"chart": "victoria-metrics-k8s-stack", "version": stack_dep_version(), "syncJobImage": image},
        "placeholders": {"clusterLabel": CLUSTER, "alertmanagerNamespace": AM_NS},
        "sources": pinned,
        "groups": entries,
    }
    open(os.path.join(out, "PIN.yaml"), "w").write(
        "# Written by hack/vendor-upstream-rules.sh; do not edit by hand.\n"
        + yaml.safe_dump(pin, sort_keys=False, width=100000))


def main():
    if MODE == "check":
        tmp = tempfile.mkdtemp(prefix="vendor-check.")
        try:
            vendor(tmp)
            r = subprocess.run(["diff", "-r", UP, tmp])
            sys.exit(r.returncode)
        finally:
            shutil.rmtree(tmp, ignore_errors=True)
    shutil.rmtree(os.path.join(UP, "groups"), ignore_errors=True)
    os.makedirs(UP, exist_ok=True)
    vendor(UP)


main()

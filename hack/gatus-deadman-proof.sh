#!/usr/bin/env bash
# hack/gatus-deadman-proof.sh: REAL proof, in Docker, that the deadman
# group pkg/statusbox renders (Catalogue.Deadman) behaves on the real
# twinproduction/gatus:v5.37.0 image:
#   1. all three checks are green while a mock of the read API answers the
#      Watchdog in vmalert, the Watchdog active in Alertmanager, and no
#      SlackNotificationsFailing;
#   2. when the mock stops answering the Alertmanager Watchdog AND starts
#      firing SlackNotificationsFailing, each of those two checks posts ONE
#      message to the mock chat API, with the bot token in the
#      Authorization header and the channel in the JSON body, after two
#      failures (about four minutes at the two-minute interval);
#   3. restoring the mock posts a RESOLVED message for each;
#   4. the vmalert check, untouched, never posts.
#
# No real chat call: the mock stands in for the chat API. The checks'
# `https://` is rewritten to `http://` in the rendered config, since the
# mock has no certificate (the render itself requires https).
#
# Takes about ten minutes. Needs Docker, curl, python3 and go.
# Deliberately not part of `check` or CI.
set -euo pipefail

root="$(cd "$(dirname "$0")/.." && pwd)"
work="$(mktemp -d -t gatus-deadman-proof.XXXXXX)"
container=gatus-deadman-proof
mock_port=18099
gatus_port=8080

cleanup() {
  docker rm -f "$container" >/dev/null 2>&1 || true
  [ -n "${mock_pid:-}" ] && kill "$mock_pid" >/dev/null 2>&1 || true
  rm -rf "$work"
}
trap cleanup EXIT

for tool in docker curl python3 go; do
  command -v "$tool" >/dev/null 2>&1 || { echo "needs $tool" >&2; exit 1; }
done

cat > "$work/render.go" <<'EOF'
package main

import (
	"fmt"
	"os"

	"github.com/truvity/observability/pkg/statusbox"
)

func main() {
	out, err := statusbox.RenderGatus(statusbox.Catalogue{
		PlatformHosts: []string{"example.invalid"},
		AlertsRead:    statusbox.AlertsRead{Host: "127.0.0.1:18099", TokenEnvKey: "read_token"},
		Deadman: statusbox.DeadmanChecks{
			Post:                 &statusbox.ChatPost{URL: "https://127.0.0.1:18099/chat.postMessage", TokenEnvKey: "deadman_token", Channel: "#deadman"},
			AlertmanagerWatchdog: true,
			NotFiring:            []statusbox.NotFiringCheck{{Name: "slack-notifications", AlertName: "SlackNotificationsFailing"}},
		},
		StoragePath: "/data/ops.db",
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Print(out)
}
EOF
( cd "$root" && go run "$work/render.go" > "$work/config.yaml" )
sed -i 's#https://127.0.0.1:18099#http://127.0.0.1:18099#' "$work/config.yaml"
# example.invalid is a plain platform probe Gatus needs for the config to
# boot; it is never reachable and its check is not part of this proof.
echo "--- rendered config ---"; cat "$work/config.yaml"; echo "-----------------------"

mkdir "$work/flags"
touch "$work/flags/am_ok"
cat > "$work/mock.py" <<'EOF'
import http.server, json, os, sys, urllib.parse
flags, log, port = sys.argv[1], sys.argv[2], int(sys.argv[3])
def has(f): return os.path.exists(os.path.join(flags, f))
class H(http.server.BaseHTTPRequestHandler):
    def _json(self, obj):
        b = json.dumps(obj).encode()
        self.send_response(200); self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(b))); self.end_headers(); self.wfile.write(b)
    def do_GET(self):
        u = urllib.parse.urlparse(self.path); q = urllib.parse.parse_qs(u.query)
        if self.headers.get("Authorization") != "Bearer read-secret":
            self.send_response(401); self.end_headers(); return
        if u.path == "/api/v2/alerts":
            ok = has("am_ok")
            return self._json([{"labels": {"alertname": "Watchdog"}, "status": {"state": "active"}}] if ok else [])
        if u.path == "/api/v1/alerts":
            m = q.get("match[]", [""])[0]
            if "Watchdog" in m: return self._json({"status": "success", "data": {"alerts": [{"labels": {"alertname": "Watchdog"}}]}})
            if "SlackNotificationsFailing" in m:
                return self._json({"status": "success", "data": {"alerts": [{"labels": {"alertname": "SlackNotificationsFailing"}}] if has("slack_firing") else []}})
        self.send_response(404); self.end_headers()
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0))).decode()
        with open(log, "a") as f:
            f.write(json.dumps({"auth": self.headers.get("Authorization"), "ctype": self.headers.get("Content-Type"), "body": json.loads(body)}) + "\n")
        self._json({"ok": True})
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", port), H).serve_forever()
EOF
: > "$work/posts.log"
python3 "$work/mock.py" "$work/flags" "$work/posts.log" "$mock_port" & mock_pid=$!

mkdir -p "$work/data"
docker run -d --name "$container" --network host \
  -e GATUS_CONFIG_PATH=/config/config.yaml \
  -e ALERT_URL_READ_TOKEN=read-secret \
  -e ALERT_URL_DEADMAN_TOKEN=xoxb-bot-secret \
  -v "$work/config.yaml:/config/config.yaml:ro" -v "$work/data:/data" \
  twinproduction/gatus:v5.37.0 >/dev/null

base="http://127.0.0.1:$gatus_port"
for _ in $(seq 1 30); do curl -fsS "$base/health" >/dev/null 2>&1 && break; sleep 1; done

echo "green phase: three checks must read healthy within a minute or so"
healthy() { # name -> "true"/"false"/""
  curl -fsS "$base/api/v1/endpoints/statuses" | python3 -c "
import json,sys
for e in json.load(sys.stdin):
    if e['name']=='$1':
        r=e.get('results') or []
        print(str(r[-1]['success']).lower() if r else '')"
}
for name in deadman alertmanager-watchdog slack-notifications; do
  ok=""
  for _ in $(seq 1 40); do ok="$(healthy "$name")"; [ "$ok" = true ] && break; sleep 3; done
  [ "$ok" = true ] || { echo "FAIL: $name is not healthy in the green phase (got '$ok')" >&2; docker logs "$container" >&2; exit 1; }
  echo "  $name healthy"
done

echo "red phase: Alertmanager Watchdog gone, SlackNotificationsFailing firing; waiting up to 8m"
rm "$work/flags/am_ok"; touch "$work/flags/slack_firing"
for _ in $(seq 1 96); do
  [ "$(grep -c 'TRIGGERED' "$work/posts.log" || true)" -ge 2 ] && break
  sleep 5
done
trig="$(grep -c 'TRIGGERED' "$work/posts.log" || true)"
[ "$trig" = 2 ] || { echo "FAIL: want exactly 2 TRIGGERED posts, got $trig" >&2; cat "$work/posts.log" >&2; exit 1; }
grep -q 'platform/alertmanager-watchdog' "$work/posts.log" || { echo "FAIL: no alertmanager-watchdog post" >&2; exit 1; }
grep -q 'platform/slack-notifications' "$work/posts.log" || { echo "FAIL: no slack-notifications post" >&2; exit 1; }
if grep -q 'platform/deadman' "$work/posts.log"; then echo "FAIL: the healthy vmalert check posted" >&2; exit 1; fi
python3 - "$work/posts.log" <<'EOF'
import json,sys
for line in open(sys.argv[1]):
    p=json.loads(line)
    assert p["auth"]=="Bearer xoxb-bot-secret", p
    assert p["body"]["channel"]=="#deadman", p
    assert p["ctype"].startswith("application/json"), p
print("  both TRIGGERED posts carry the bot token and the channel")
EOF

echo "recovery: restoring the mock; waiting up to 8m for the RESOLVED messages"
touch "$work/flags/am_ok"; rm "$work/flags/slack_firing"
for _ in $(seq 1 96); do
  [ "$(grep -c 'RESOLVED' "$work/posts.log" || true)" -ge 2 ] && break
  sleep 5
done
res="$(grep -c 'RESOLVED' "$work/posts.log" || true)"
[ "$res" = 2 ] || { echo "FAIL: want 2 RESOLVED posts, got $res" >&2; cat "$work/posts.log" >&2; exit 1; }
echo "hack/gatus-deadman-proof.sh: three checks, two failures to page, resolved on recovery, healthy vmalert check silent: OK"

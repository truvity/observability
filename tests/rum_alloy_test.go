package tests

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The generated Alloy configuration is only as good as the Alloy that reads
// it, so these two tests run the real binary at the version the chart pins
// (the vendored subchart's appVersion). Like the dashboard-query tests they
// need a Docker daemon: without one they skip, and RUM_ALLOY=require makes a
// missing daemon a failure.

func rumAlloyImage(t testing.TB) string {
	t.Helper()
	out, err := exec.Command("bash", "-c",
		`f=$(ls ../charts/observability-rum/charts/alloy-*.tgz | head -1); tar -xzOf "$f" alloy/Chart.yaml | awk '/^appVersion:/ {print $2}'`).Output()
	require.NoError(t, err, "cannot read the pinned Alloy version from the vendored subchart")
	v := strings.TrimSpace(string(out))
	require.Regexp(t, `^v\d+\.\d+\.\d+$`, v)
	return "grafana/alloy:" + v
}

func rumDocker(t *testing.T) {
	t.Helper()
	skip := func(msg string) {
		if os.Getenv("RUM_ALLOY") == "require" {
			t.Fatal(msg)
		}
		t.Skip(msg + " (set RUM_ALLOY=require to make this a failure)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		skip("docker is not on PATH")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		skip("the docker daemon is not reachable")
	}
}

func TestRumGeneratedConfigValidatesOnTheRealAlloy(t *testing.T) {
	rumDocker(t)
	image := rumAlloyImage(t)
	dir := t.TempDir()
	for name, cfg := range rumConfigs(t) {
		path := filepath.Join(dir, strings.TrimSuffix(name, ".yaml")+".alloy")
		require.NoError(t, os.WriteFile(path, []byte(cfg), 0o644)) // #nosec G306 -- read by a container user.
		out, err := exec.Command("docker", "run", "--rm", "-v", path+":/c.alloy:ro", image, "validate", "/c.alloy").CombinedOutput()
		require.NoErrorf(t, err, "%s does not validate on %s:\n%s", name, image, out)
		out, err = exec.Command("docker", "run", "--rm", "-v", path+":/c.alloy:ro", image, "fmt", "/c.alloy").CombinedOutput()
		require.NoErrorf(t, err, "%s does not format on %s:\n%s", name, image, out)
	}
}

type otlpAttr struct {
	Key   string         `json:"key"`
	Value map[string]any `json:"value"`
}

type otlpLogs struct {
	ResourceLogs []struct {
		Resource struct {
			Attributes []otlpAttr `json:"attributes"`
		} `json:"resource"`
		ScopeLogs []struct {
			LogRecords []struct {
				Body       map[string]any `json:"body"`
				TraceID    string         `json:"traceId"`
				Attributes []otlpAttr     `json:"attributes"`
			} `json:"logRecords"`
		} `json:"scopeLogs"`
	} `json:"resourceLogs"`
}

func attrMap(attrs []otlpAttr) map[string]string {
	m := map[string]string{}
	for _, a := range attrs {
		for _, v := range a.Value {
			m[a.Key] = fmt.Sprint(v)
		}
	}
	return m
}

// The fingerprint, the app stamp and the privacy rules, end to end: a Faro
// payload goes into a receiver built from the generated configuration and the
// OTLP request that comes out the other side is read.
func TestRumPipelineStampsFingerprintsAndScrubs(t *testing.T) {
	rumDocker(t)
	image := rumAlloyImage(t)
	cfg := rumConfigs(t)["minimal.yaml"]

	// The test's own wiring: a literal key instead of the Secret, and an
	// exporter that talks JSON to a local sink.
	sinkPort, recvPort, httpPort := freePort(t), freePort(t), freePort(t)
	cfg = regexp.MustCompile(`(?s)remote\.kubernetes\.secret "shop_key" \{.*?\n\}\n`).ReplaceAllString(cfg, "")
	cfg = strings.Replace(cfg, `remote.kubernetes.secret.shop_key.data["key"]`, `"test-key"`, 1)
	cfg = strings.Replace(cfg, "listen_port              = 12347", fmt.Sprintf("listen_port              = %d", recvPort), 1)
	cfg = strings.Replace(cfg, `endpoint = "http://otlp-gateway.example.svc:4318"`,
		fmt.Sprintf("endpoint = \"http://127.0.0.1:%d\"\n  }\n  encoding = \"json\"\n  retry_on_failure {\n    enabled = false", sinkPort), 1)
	require.Contains(t, cfg, "encoding", "the test's exporter rewrite did not apply")

	var mu sync.Mutex
	var logs []otlpLogs
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rd io.Reader = r.Body
		if r.Header.Get("Content-Encoding") == "gzip" {
			if gz, err := gzip.NewReader(r.Body); err == nil {
				rd = gz
			}
		}
		body, _ := io.ReadAll(rd)
		if strings.HasSuffix(r.URL.Path, "/v1/logs") {
			var l otlpLogs
			if json.Unmarshal(body, &l) == nil {
				mu.Lock()
				logs = append(logs, l)
				mu.Unlock()
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("{}"))
	}))
	l, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", sinkPort))
	require.NoError(t, err)
	srv.Listener = l
	srv.Start()
	defer srv.Close()

	path := filepath.Join(t.TempDir(), "e2e.alloy")
	require.NoError(t, os.WriteFile(path, []byte(cfg), 0o644)) // #nosec G306 -- read by a container user.
	name := fmt.Sprintf("rum-pipeline-%d", os.Getpid())
	out, err := exec.Command("docker", "run", "-d", "--name", name, "--network", "host", "-v", path+":/c.alloy:ro", image,
		"run", "--server.http.listen-addr=127.0.0.1:"+fmt.Sprint(httpPort), "--storage.path=/tmp/a", "--disable-reporting", "/c.alloy").CombinedOutput()
	require.NoErrorf(t, err, "starting alloy: %s", out)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", name).Run() })
	require.Eventually(t, func() bool {
		r, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/-/ready", httpPort))
		if err != nil {
			return false
		}
		defer func() { _ = r.Body.Close() }()
		return r.StatusCode == 200
	}, 60*time.Second, 500*time.Millisecond, "alloy did not become ready")

	post := func(key, payload string) int {
		req, _ := http.NewRequest(http.MethodPost, fmt.Sprintf("http://127.0.0.1:%d/collect", recvPort), strings.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-api-key", key)
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	exception := func(msg, file string, line int) string {
		return fmt.Sprintf(`{"meta":{"app":{"name":"somebody-else","environment":"x"},"user":{"id":"u-1","email":"a@b.example"},`+
			`"session":{"id":"s-1"},"page":{"url":"https://shop.example/cart?token=abc#frag"}},`+
			`"exceptions":[{"type":"TypeError","value":%q,"timestamp":"2026-10-02T10:00:00.000Z","stacktrace":{"frames":[`+
			`{"function":"vendor","filename":"https://cdn.other/x.js","lineno":1,"colno":1},`+
			`{"function":"react","filename":"../node_modules/react/index.js","lineno":5,"colno":2},`+
			`{"function":"onClick","filename":%q,"lineno":%d,"colno":7}]},`+
			`"trace":{"trace_id":"0af7651916cd43dd8448eb211c80319c","span_id":"b7ad6b7169203331"}}]}`, msg, file, line)
	}

	assert.Equal(t, http.StatusUnauthorized, post("wrong", exception("x", "https://shop.example/assets/a.js", 1)), "a wrong key")
	// Same bug: the message differs only in a number, an id, a quoted string and a
	// URL's query; the frame differs only in the content hash, the cache-buster
	// and the line. A different bug: another frame.
	require.Equal(t, http.StatusAccepted, post("test-key", exception(
		`Cannot read properties of undefined (reading 'id') for order 12345 at https://api.example/o/7?x=1 id 123e4567-e89b-12d3-a456-42661417400a`,
		"https://shop.example/assets/index-AbC123xy.js?v=9", 10)))
	require.Equal(t, http.StatusAccepted, post("test-key", exception(
		`Cannot read properties of undefined (reading 'name') for order 99 at https://api.example/o/8?y=2 id 223e4567-e89b-12d3-a456-42661417499b`,
		"https://shop.example/assets/index-ZzZ999qq.js?v=10", 99)))
	require.Equal(t, http.StatusAccepted, post("test-key", exception(
		`Cannot read properties of undefined (reading 'id') for order 12345 at https://api.example/o/7?x=1 id 123e4567-e89b-12d3-a456-42661417400a`,
		"https://shop.example/assets/checkout-AbC123xy.js", 10)))

	t.Cleanup(func() {
		if t.Failed() {
			logsOut, _ := exec.Command("docker", "logs", name).CombinedOutput()
			t.Logf("alloy output:\n%s", logsOut)
		}
	})
	var recs []map[string]string
	var res map[string]string
	var body, trace []string
	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		recs, body, trace = nil, nil, nil
		for _, l := range logs {
			for _, rl := range l.ResourceLogs {
				res = attrMap(rl.Resource.Attributes)
				for _, sl := range rl.ScopeLogs {
					for _, r := range sl.LogRecords {
						recs = append(recs, attrMap(r.Attributes))
						body = append(body, fmt.Sprint(r.Body["stringValue"]))
						trace = append(trace, r.TraceID)
					}
				}
			}
		}
		return len(recs) == 3
	}, 30*time.Second, 300*time.Millisecond, "the three exceptions did not reach the sink")

	// The app is the receiver's, whatever the payload claimed.
	assert.Equal(t, "shop-browser", res["service.name"])
	assert.Equal(t, "shop", res["app"])
	assert.Equal(t, "faro", res["telemetry.source"])
	for i, r := range recs {
		assert.NotContains(t, r, "app_name", "the client's claim about its app survived")
		assert.NotContains(t, r, "user_id")
		assert.NotContains(t, r, "user_email")
		assert.Equal(t, "https://shop.example/cart", r["page_url"], "query and fragment are scrubbed")
		assert.NotContains(t, r["value"], "?x=1")
		assert.NotContains(t, body[i], "token=abc", "the body is the human line, not the raw payload")
		assert.Equal(t, "s-1", r["session_id"], "the anonymous session id stays")
		assert.Equal(t, "0af7651916cd43dd8448eb211c80319c", trace[i], "the trace id is the structured field")
		assert.Regexp(t, `^[0-9a-f]{16}$`, r["error.fingerprint"])
		assert.Contains(t, r["error.message"], "<n>")
		assert.Contains(t, r["error.message"], "<url>")
		assert.Contains(t, r["error.message"], "<uuid>")
		assert.Contains(t, r["error.message"], "'<str>'")
		assert.NotContains(t, r, "hash", "alloy's own hash is not a grouping key")
	}
	// Sorted by arrival, which is the order posted: same bug twice, then another.
	assert.Equal(t, "assets/index.js:onClick", recs[0]["error.frame"], "first own-code frame, hash and query stripped")
	assert.Equal(t, recs[0]["error.fingerprint"], recs[1]["error.fingerprint"], "the same bug must group")
	assert.NotEqual(t, recs[0]["error.fingerprint"], recs[2]["error.fingerprint"], "a different frame is a different issue")
}

// External OTLP ingest (`otlp.external`): identity comes from the request
// headers a trusting gateway route set, never from the payload.
//
// Two layers. The config-shape tests read the rendered golden and need
// nothing. The behaviour test takes the REAL rendered external pipelines
// (the processors, unchanged), runs them in the pinned collector image with
// a file exporter in place of the stores, posts OTLP that carries forged
// identity and tenancy attributes together with the trusted headers, and
// reads back what would have been written. It needs Docker; without it the
// test skips, unless EXTERNAL_INGEST=require makes that a failure.
package tests

import (
	"bytes"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

const (
	externalGolden = "golden/observability-emitters/external-ingest.yaml"
	externalOff    = "golden/observability-emitters/minimal.yaml"
)

func TestExternalIngestIsAbsentUnlessEnabled(t *testing.T) {
	cfg := gatewayConfig(t, renderedDocs(t, externalOff))
	assert.Nil(t, dig(cfg, "receivers", "otlp/external"))
	for _, p := range []string{"metrics/external", "logs/external", "traces/external"} {
		assert.Nil(t, dig(cfg, "service", "pipelines", p), p)
	}
	for _, d := range renderedDocs(t, externalOff) {
		assert.NotEqual(t, "NetworkPolicy", d["kind"], "the gateway gets no policy unless external ingest is on")
	}
}

func TestExternalIngestHasItsOwnReceiverAndPipelines(t *testing.T) {
	cfg := gatewayConfig(t, renderedDocs(t, externalGolden))

	http := dig(cfg, "receivers", "otlp/external", "protocols", "http")
	require.NotNil(t, http)
	assert.Equal(t, true, dig(http, "include_metadata"))
	assert.Equal(t, 4194304, dig(http, "max_request_body_size"))
	assert.Equal(t, "0.0.0.0:4319", dig(http, "endpoint"))
	assert.Nil(t, dig(cfg, "receivers", "otlp/external", "protocols", "grpc"))
	// the in-cluster receiver does not read metadata
	assert.Nil(t, dig(cfg, "receivers", "otlp", "protocols", "http", "include_metadata"))

	for _, signal := range []string{"metrics", "logs", "traces"} {
		// the receivers never mix
		assert.Equal(t, []string{"otlp/external"}, stringList(t, dig(cfg, "service", "pipelines", signal+"/external", "receivers")))
		assert.NotContains(t, stringList(t, dig(cfg, "service", "pipelines", signal, "receivers")), "otlp/external")
		// the exporters are the existing ones, so writes replicate and go
		// through the same writer
		assert.Equal(t, pipelineExporters(t, cfg, signal), pipelineExporters(t, cfg, signal+"/external"))
		// the order is the security property, and all of it is before batch
		procs := stringList(t, dig(cfg, "service", "pipelines", signal+"/external", "processors"))
		order := []string{"transform/external-disown", "resource/external-identity", "filter/external-unidentified", "transform/external-stamp"}
		last := -1
		for _, p := range order {
			i := indexOf(procs, p)
			require.Greater(t, i, last, "%s: %s out of order in %v", signal, p, procs)
			last = i
		}
		assert.Equal(t, "batch", procs[len(procs)-1])
		assert.Equal(t, -1, indexOf(procs, "k8sattributes"), "there is no pod behind an external caller")
	}
}

func TestExternalIngestPolicyNarrowsOnlyItsOwnPort(t *testing.T) {
	var np networkPolicyDoc
	var found bool
	for _, d := range renderedDocs(t, externalGolden) {
		if d["kind"] != "NetworkPolicy" {
			continue
		}
		b, err := yaml.Marshal(d)
		require.NoError(t, err)
		require.NoError(t, yaml.Unmarshal(b, &np))
		found = true
	}
	require.True(t, found)
	require.Len(t, np.Spec.Ingress, 2)
	// the in-cluster ports keep admitting everyone: no `from`
	assert.Empty(t, np.Spec.Ingress[0].From)
	var open []int
	for _, p := range np.Spec.Ingress[0].Ports {
		open = append(open, p.Port)
	}
	assert.ElementsMatch(t, []int{4317, 4318, 8888}, open)
	// the external port: only the named peer, and only that port
	require.Len(t, np.Spec.Ingress[1].From, 1)
	require.Len(t, np.Spec.Ingress[1].Ports, 1)
	assert.Equal(t, 4319, np.Spec.Ingress[1].Ports[0].Port)
}

// The behaviour that matters, on the real collector.
func TestExternalIngestStampsTheHeadersAndNotThePayload(t *testing.T) {
	skip := func(msg string) {
		if os.Getenv("EXTERNAL_INGEST") == "require" {
			t.Fatal(msg)
		}
		t.Skip(msg + " (set EXTERNAL_INGEST=require to make this a failure)")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		skip("docker is not on PATH")
	}
	if err := exec.Command("docker", "info").Run(); err != nil {
		skip("the docker daemon is not reachable")
	}

	full := gatewayConfig(t, renderedDocs(t, externalGolden))
	image := pinnedGatewayImage(t)

	dir := t.TempDir()
	require.NoError(t, os.Chmod(dir, 0o777))
	cfg := map[string]any{
		"receivers":  map[string]any{"otlp/external": dig(full, "receivers", "otlp/external")},
		"processors": map[string]any{"batch": map[string]any{"timeout": "100ms"}},
		"exporters":  map[string]any{},
		"service":    map[string]any{"pipelines": map[string]any{}, "telemetry": map[string]any{"metrics": map[string]any{"level": "none"}}},
	}
	exporters := cfg["exporters"].(map[string]any)
	pipelines := dig(cfg, "service", "pipelines").(map[string]any)
	for _, signal := range []string{"metrics", "logs", "traces"} {
		for _, p := range stringList(t, dig(full, "service", "pipelines", signal+"/external", "processors")) {
			// the processors under test, byte for byte
			cfg["processors"].(map[string]any)[p] = dig(full, "processors", p)
		}
		exporters["file/"+signal] = map[string]any{"path": "/out/" + signal + ".json", "flush_interval": "100ms"}
		pipelines[signal+"/external"] = map[string]any{
			"receivers":  []string{"otlp/external"},
			"processors": dig(full, "service", "pipelines", signal+"/external", "processors"),
			"exporters":  []string{"file/" + signal},
		}
	}
	cfg["processors"].(map[string]any)["batch"] = map[string]any{"timeout": "100ms"}
	b, err := yaml.Marshal(cfg)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, "config.yaml"), b, 0o644))

	port := freePort(t)
	name := fmt.Sprintf("external-ingest-%d", os.Getpid())
	out, err := exec.Command("docker", "run", "-d", "--rm", "--name", name,
		"-p", fmt.Sprintf("127.0.0.1:%d:4319", port),
		"-v", dir+"/config.yaml:/etc/otelcol/config.yaml:ro",
		"-v", dir+":/out",
		image, "--config=/etc/otelcol/config.yaml").CombinedOutput()
	if err != nil && strings.Contains(string(out), "Unable to find image") {
		skip("cannot pull " + image)
	}
	require.NoErrorf(t, err, "starting the collector: %s", out)
	t.Cleanup(func() {
		if t.Failed() {
			logs, _ := exec.Command("docker", "logs", name).CombinedOutput()
			t.Logf("collector log:\n%s", logs)
		}
		_ = exec.Command("docker", "rm", "-f", name).Run()
	})
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	post := func(path, body string, headers map[string]string) int {
		req, err := http.NewRequest("POST", base+path, strings.NewReader(body))
		require.NoError(t, err)
		req.Header.Set("Content-Type", "application/json")
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		resp, err := http.DefaultClient.Do(req)
		require.NoError(t, err)
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	require.Eventually(t, func() bool {
		resp, err := http.Post(base+"/v1/traces", "application/json", bytes.NewReader([]byte("{}")))
		if err != nil {
			return false
		}
		_ = resp.Body.Close()
		return true
	}, 60*time.Second, 300*time.Millisecond, "the collector did not start")

	// Everything a Lambda layer's payload could say about who it is, and a
	// few things it must not be able to say about where it is filed.
	forged := func(attrs string) string {
		return `[
		  {"key":"owner","value":{"stringValue":"forged-owner"}},
		  {"key":"project","value":{"stringValue":"forged-project"}},
		  {"key":"enduser.id","value":{"stringValue":"forged-subject"}},
		  {"key":"cloud.account.id","value":{"stringValue":"forged-account"}},
		  {"key":"k8s.namespace.name","value":{"stringValue":"forged-namespace"}},
		  {"key":"k8s.cluster.name","value":{"stringValue":"forged-cluster"}},
		  {"key":"kubernetes.pod_namespace","value":{"stringValue":"forged-namespace"}},
		  {"key":"deployment.environment.name","value":{"stringValue":"forged-env"}},
		  {"key":"telemetry.source","value":{"stringValue":"forged-source"}},
		  {"key":"faas.name","value":{"stringValue":"fn-one"}}` + attrs + `]`
	}
	logBody := `{"resourceLogs":[{"resource":{"attributes":` + forged("") + `},
	  "scopeLogs":[{"scope":{"attributes":[{"key":"owner","value":{"stringValue":"forged-scope-owner"}}]},
	  "logRecords":[{"timeUnixNano":"1700000000000000000","body":{"stringValue":"hello"},
	    "attributes":[{"key":"owner","value":{"stringValue":"forged-record-owner"}},
	      {"key":"k8s.namespace.name","value":{"stringValue":"forged-record-ns"}}]}]}]}]}`
	traceBody := `{"resourceSpans":[{"resource":{"attributes":` + forged("") + `},
	  "scopeSpans":[{"spans":[{"traceId":"5b8efff798038103d269b633813fc60c","spanId":"eee19b7ec3c1b174","name":"op",
	    "startTimeUnixNano":"1700000000000000000","endTimeUnixNano":"1700000001000000000",
	    "attributes":[{"key":"owner","value":{"stringValue":"forged-span-owner"}}]}]}]}]}`
	metricBody := `{"resourceMetrics":[{"resource":{"attributes":` + forged("") + `},
	  "scopeMetrics":[{"metrics":[{"name":"m","gauge":{"dataPoints":[{"asInt":"1","timeUnixNano":"1700000000000000000",
	    "attributes":[{"key":"owner","value":{"stringValue":"forged-point-owner"}}]}]}}]}]}]}`

	trusted := map[string]string{
		"X-Roster-Subject": "aws:acct:role/fn-role",
		"X-Roster-Account": "acct",
		"X-Roster-Owner":   "trusted-owner",
	}
	for path, body := range map[string]string{"/v1/logs": logBody, "/v1/traces": traceBody, "/v1/metrics": metricBody} {
		require.Equal(t, 200, post(path, body, trusted), path)
	}
	// Missing one of the required headers: refused by omission. The
	// request is accepted (the gateway already decided) and the data is
	// dropped, not filed anonymously.
	partial := map[string]string{"X-Roster-Subject": "aws:acct:role/fn-role", "X-Roster-Account": "acct"}
	require.Equal(t, 200, post("/v1/logs", strings.ReplaceAll(logBody, "hello", "no-owner-header"), partial))

	read := func(signal string) string {
		var got string
		require.Eventually(t, func() bool {
			raw, _ := os.ReadFile(filepath.Join(dir, signal+".json"))
			got = string(raw)
			return strings.Contains(got, "trusted-owner")
		}, 30*time.Second, 200*time.Millisecond, "nothing was written for %s", signal)
		return got
	}
	for _, signal := range []string{"logs", "traces", "metrics"} {
		got := read(signal)
		for _, want := range []string{
			`"aws:acct:role/fn-role"`, `"trusted-owner"`, `"external"`, `"example-cluster"`, `"development"`,
			`"serverless"`, // the static `project`
			`"fn-one"`,     // descriptive attributes survive
		} {
			assert.Contains(t, got, want, signal)
		}
		assert.NotContains(t, got, "forged", "%s: a client-supplied identity or tenancy value reached the output", signal)
		assert.NotContains(t, got, "no-owner-header", "%s: data without the required header was kept", signal)
	}
}

func pinnedGatewayImage(t *testing.T) string {
	t.Helper()
	raw, err := os.ReadFile("../charts/observability-emitters/values.yaml")
	require.NoError(t, err)
	var v struct {
		Otlp struct {
			Image struct{ Repository, Tag string } `yaml:"image"`
		} `yaml:"otlp"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &v))
	return v.Otlp.Image.Repository + ":" + v.Otlp.Image.Tag
}

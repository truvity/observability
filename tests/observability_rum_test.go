package tests

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// charts/observability-rum's security claims are shapes in the rendered
// Alloy configuration, so they are checked on the text of every golden's
// configuration rather than on the values that produced it: whatever route
// a value takes, an edit that lets a receiver trust its payload, open a
// wildcard origin, enable the receiver's own source-map download, or hand
// a per-error value to a stream fails here first.

func rumConfigs(t *testing.T) map[string]string {
	t.Helper()
	goldens, err := filepath.Glob("golden/observability-rum/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, goldens, "no observability-rum goldens; this test would pass vacuously")
	out := map[string]string{}
	for _, g := range goldens {
		docs := renderedDocs(t, g)
		cm := findDoc(t, docs, "ConfigMap", func(d map[string]any) bool {
			data, _ := d["data"].(map[string]any)
			_, ok := data["config.alloy"]
			return ok
		})
		out[filepath.Base(g)] = cm["data"].(map[string]any)["config.alloy"].(string)
	}
	return out
}

// blocks returns each `<kind> "<label>" {` block's text, by brace matching.
func rumBlocks(cfg, kind string) []string {
	var out []string
	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(kind) + ` "[^"]+" \{`)
	for _, loc := range re.FindAllStringIndex(cfg, -1) {
		depth, i := 0, loc[1]-1
		for ; i < len(cfg); i++ {
			switch cfg[i] {
			case '{':
				depth++
			case '}':
				depth--
			}
			if depth == 0 {
				break
			}
		}
		out = append(out, cfg[loc[0]:i+1])
	}
	return out
}

func TestRumEveryReceiverIsClosedByDefault(t *testing.T) {
	for name, cfg := range rumConfigs(t) {
		recv := rumBlocks(cfg, "faro.receiver")
		require.NotEmptyf(t, recv, "%s: no faro.receiver rendered", name)
		for _, r := range recv {
			assert.Contains(t, r, "download = false", "%s: a receiver must never fetch a source map from a URL the browser names", name)
			assert.Contains(t, r, `strategy   = "global"`, "%s: per_app rate limiting is keyed on the payload's own app name", name)
			assert.Regexp(t, `api_key\s+= remote\.kubernetes\.secret\.[a-z0-9_]+_key\.data\["[^"]+"\]`, r,
				"%s: the key comes from the app's Secret, never a literal", name)
			assert.NotContains(t, r, `"*"`, "%s: a wildcard origin", name)
			assert.Regexp(t, `cors_allowed_origins\s+= \["https?://[^"*]+"(, "https?://[^"*]+")*\]`, r, "%s: origins must be exact and listed", name)
			m := regexp.MustCompile(`max_allowed_payload_size\s+= "([0-9]+)(KiB|MiB)"`).FindStringSubmatch(r)
			require.NotNilf(t, m, "%s: no payload cap", name)
			n, _ := strconv.Atoi(m[1])
			if m[2] == "MiB" {
				n *= 1024
			}
			assert.LessOrEqualf(t, n, 1024, "%s: payload cap above 1MiB", name)
			assert.Contains(t, r, `log_format = "json"`, name)
		}
	}
}

// The app is stamped from the receiver, and a client's own claim about it is
// deleted, in the logs path and in the traces path.
func TestRumIdentityComesFromTheReceiverNotThePayload(t *testing.T) {
	for name, cfg := range rumConfigs(t) {
		for _, p := range rumBlocks(cfg, "otelcol.processor.transform") {
			if strings.Contains(p, "_logs\"") {
				for _, want := range []string{
					`delete_key(log.attributes, "app_name")`,
					`delete_key(log.attributes, "app_namespace")`,
					`set(resource.attributes["service.name"], "`,
					`set(resource.attributes["app"], "`,
					`set(resource.attributes["telemetry.source"], "faro")`,
				} {
					assert.Containsf(t, p, want, "%s: the logs transform must contain %s", name, want)
				}
				// The fingerprint is a record attribute, never a resource one.
				assert.NotRegexp(t, `resource\.attributes\["error\.`, p, "%s: error.* as a resource attribute would become a stream field", name)
				assert.Contains(t, p, `set(log.attributes["error.fingerprint"]`, name)
			}
			if strings.Contains(p, "_traces\"") {
				assert.Contains(t, p, `set(resource.attributes["service.name"], "`, name)
				assert.Contains(t, p, `set(resource.attributes["app"], "`, name)
			}
			// No stamp is ever read from the payload: a stamp's value is a literal.
			assert.NotRegexp(t, `set\(resource\.attributes\["(service\.name|app)"\], (log|span)\.`, p, "%s: an identity read from the payload", name)
		}
	}
}

func TestRumRoleReadsOnlyTheNamedSecrets(t *testing.T) {
	goldens, _ := filepath.Glob("golden/observability-rum/*.yaml")
	for _, g := range goldens {
		docs := renderedDocs(t, g)
		role := findDoc(t, docs, "Role", nil)
		rules := role["rules"].([]any)
		require.Len(t, rules, 1, g)
		rule := rules[0].(map[string]any)
		assert.Equal(t, []any{"get"}, rule["verbs"], g)
		assert.Equal(t, []any{"secrets"}, rule["resources"], g)
		assert.NotEmpty(t, rule["resourceNames"], "%s: a Role on every Secret in the namespace", g)
		for _, d := range docs {
			assert.NotEqual(t, "ClusterRole", d["kind"], "%s: no ClusterRole", g)
		}
	}
}

func TestRumRulesAreLogsQLForTheLogsAlerter(t *testing.T) {
	goldens, _ := filepath.Glob("golden/observability-rum/*.yaml")
	var sawRule bool
	for _, g := range goldens {
		for _, d := range renderedDocs(t, g) {
			if d["kind"] != "VMRule" {
				continue
			}
			sawRule = true
			assert.Equal(t, "vlogs", dig(d, "metadata", "labels", "observability.rule-type"), g)
			for _, grp := range dig(d, "spec", "groups").([]any) {
				assert.Equal(t, "vlogs", grp.(map[string]any)["type"], "%s: a group without type: vlogs is parsed as MetricsQL and silently absent", g)
				for _, r := range grp.(map[string]any)["rules"].([]any) {
					expr := r.(map[string]any)["expr"].(string)
					// Every query is scoped to RUM rows and carries an explicit window.
					assert.Contains(t, expr, "telemetry.source:faro", g)
					assert.Regexp(t, `^_time:[0-9]+[mhdw] `, expr, "%s: vmalert adds no time filter to a LogsQL rule", g)
					alert := fmt.Sprint(r.(map[string]any)["alert"])
					if alert == "FrontendNewIssue" || alert == "FrontendIssueRegressed" {
						assert.Contains(t, expr, " limit ", "%s: an issue rule without a cap on how many fingerprints fire", g)
					}
				}
			}
		}
	}
	assert.True(t, sawRule, "no VMRule in any golden")
}

// dashboards.namespace puts the ConfigMaps where Grafana's sidecar looks. It
// cannot be a golden: hack/apply.sh applies every golden into one namespace.
func TestRumDashboardsFollowTheirNamespaceValue(t *testing.T) {
	out, err := exec.Command("helm", "template", "x", "../charts/observability-rum", "-n", "rum",
		"-f", "cases/observability-rum/minimal/values.yaml",
		"--set", "dashboards.namespace=monitoring",
		"--set", "dashboards.datasources.logs=logs-main",
		"--set", "dashboards.datasources.traces=traces-main").CombinedOutput()
	require.NoError(t, err, string(out))
	text := string(out)
	cms := regexp.MustCompile(`(?s)kind: ConfigMap\nmetadata:\n  name: observability-rum-frontend-[a-z-]+\n  namespace: monitoring\n`).FindAllString(text, -1)
	assert.Len(t, cms, 2, "both dashboards go to the namespace value")
	assert.Contains(t, text, `"value": "logs-main"`)
	assert.NotContains(t, text, "__LOGS_DATASOURCE_UID__")
	assert.NotContains(t, text, "__TRACES_DATASOURCE_UID__")
	assert.Contains(t, text, "traces-main")
}

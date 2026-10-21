// The notification convention (docs/notifications.md, "Annotations"), from
// the Slack side: each alert reads as its bold summary, its description, and
// its own link, the Grafana link appears only for an alert that has a
// cluster, and the message stays bounded however many alerts or however long
// a description arrives.
//
// The alerts are the ones alert-ingress produces from AWS's sample payloads
// (cmd/alert-ingress/testdata/alerts/*.json, written by that package's
// TestAWSSamples), executed through the shipped Slack text; the result is
// golden under golden/slack-text/<case>.txt. `go test ./tests -run
// TestSlackTextFromAWSSamples -update-slack-text` rewrites them.
package tests

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var updateSlackText = flag.Bool("update-slack-text", false, "rewrite golden/slack-text/*.txt")

// slackTextOf is the first Slack `text` of a golden render.
func slackTextOf(t *testing.T, golden string) string {
	t.Helper()
	return slackConfigsOf(t, golden)[0]["text"].(string)
}

func TestSlackTextFromAWSSamples(t *testing.T) {
	files, err := filepath.Glob("../cmd/alert-ingress/testdata/alerts/*.json")
	require.NoError(t, err)
	require.NotEmpty(t, files)

	text := slackTextOf(t, "golden/observability-stack/notifications-silence-link.yaml")

	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".json")
		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(f)
			require.NoError(t, err)

			var a struct {
				Labels      amKV `json:"labels"`
				Annotations amKV `json:"annotations"`
			}
			require.NoError(t, json.Unmarshal(raw, &a))

			out := executeSlackText(t, text, amData{Status: "firing", CommonLabels: a.Labels,
				Alerts: []amAlert{{Labels: a.Labels, Annotations: a.Annotations}}})

			path := filepath.Join("golden", "slack-text", name+".txt")
			if *updateSlackText {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte(out), 0o644))
			}

			want, err := os.ReadFile(path)
			require.NoError(t, err, "run with -update-slack-text to create it")
			assert.Equal(t, string(want), out)

			set := slackLinks(t, out)
			assert.True(t, strings.HasPrefix(out, "*"+a.Annotations["summary"]), "the summary leads, bold: %q", out)
			if a.Labels["k8s_cluster_name"] == "" {
				assert.Empty(t, set.Grafana, "no cluster, no Grafana link")
			} else {
				assert.Len(t, set.Grafana, 1)
			}
			if a.Annotations["link"] != "" {
				assert.Contains(t, set.Alert, a.Annotations["link_name"])
			}
			assert.Equal(t, []string{"Silence"}, set.Group)
		})
	}
}

// A long description is cut at 1500 characters (characters, not bytes), a
// group of many alerts lists the first 20, and the message stays bounded.
func TestSlackTextIsBounded(t *testing.T) {
	text := slackTextOf(t, "golden/observability-stack/notifications-silence-link.yaml")
	long := strings.Repeat("é", 4000)

	var alerts []amAlert
	for i := 0; i < 50; i++ {
		alerts = append(alerts, amAlert{
			Labels:      amKV{"alertname": "A", "source": "s"},
			Annotations: amKV{"summary": "s", "description": long, "link": "https://example.com/x", "link_name": "X"},
		})
	}

	out := executeSlackText(t, text, amData{Status: "firing", CommonLabels: amKV{"alertname": "A"}, Alerts: alerts})
	assert.True(t, utf8.ValidString(out))
	assert.Equal(t, 5, strings.Count(out, strings.Repeat("é", 1500)+"…"), "five full alerts, each description cut at 1500 characters")
	assert.NotContains(t, out, strings.Repeat("é", 1501))
	assert.Equal(t, 15, strings.Count(out, "• s\n"), "the next 15 by summary only")
	assert.Contains(t, out, "… 50 alerts in this notification; the first 20 are listed.")
	assert.Less(t, utf8.RuneCountInString(out), 12000)
}

// Annotation text cannot open a link, a mention or a formatting escape, and
// only an http(s) `link` becomes a link.
func TestSlackTextEscapesAnnotations(t *testing.T) {
	text := slackTextOf(t, "golden/observability-stack/notifications-silence-link.yaml")
	a := amAlert{
		Labels: amKV{"alertname": "A", "source": "s"},
		Annotations: amKV{
			"summary":     "x <!channel> & y",
			"description": "see <https://evil.example|here> & >",
			"link":        "https://example.com/a?b=1&c=<2>|3",
			"link_name":   "Do <it>",
			"runbook":     "javascript:alert(1)",
		},
	}

	out := executeSlackText(t, text, amData{Status: "firing", CommonLabels: a.Labels, Alerts: []amAlert{a}})
	assert.Contains(t, out, "*x &lt;!channel&gt; &amp; y*")
	assert.Contains(t, out, "see &lt;https://evil.example|here&gt; &amp; &gt;")
	assert.Contains(t, out, "<https://example.com/a?b=1&amp;c=%3C2%3E%7C3|Do &lt;it&gt;>")
	assert.NotContains(t, out, "javascript:")
	assert.NotContains(t, out, "|Runbook>")

	a.Annotations = amKV{"summary": "s", "link": "ftp://example.com"}
	out = executeSlackText(t, text, amData{Status: "firing", CommonLabels: a.Labels, Alerts: []amAlert{a}})
	assert.NotContains(t, out, "ftp://")
}

// An alert with no summary is named by its alertname; a runbook annotation
// that is a URL is a link even without runbookBaseUrl, and a path is joined
// to runbookBaseUrl when that is set.
func TestSlackTextRunbookAndFallbackSummary(t *testing.T) {
	text := slackTextOf(t, "golden/observability-stack/notifications-silence-link.yaml")
	a := amAlert{Labels: amKV{"alertname": "NoSummary"}, Annotations: amKV{"runbook": "https://runbooks.example/x"}}
	out := executeSlackText(t, text, amData{Status: "firing", CommonLabels: a.Labels, Alerts: []amAlert{a}})
	assert.True(t, strings.HasPrefix(out, "*NoSummary*\n<https://runbooks.example/x|Runbook>\n"), out)

	a.Annotations = amKV{"summary": "s", "runbook": "relative/path", "link": "https://example.com", "link_name": "L"}
	out = executeSlackText(t, text, amData{Status: "firing", CommonLabels: a.Labels, Alerts: []amAlert{a}})
	assert.NotContains(t, out, "relative/path", "no base URL: a path is not a link")

	withBase := slackTextOf(t, "golden/observability-stack/everything.yaml")
	if !strings.Contains(withBase, "$a.Annotations.runbook }}{{ else if") {
		t.Skip("the everything case sets no runbookBaseUrl")
	}
	out = executeSlackText(t, withBase, amData{Status: "firing", CommonLabels: a.Labels, Alerts: []amAlert{a}})
	assert.Contains(t, out, "<https://example.com|L> · <")
	assert.Contains(t, out, "relative/path|Runbook>")
}

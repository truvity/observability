// The links a notification carries must work, or not be there.
//
// A Slack message used to carry `Silence: {{ .ExternalURL }}/...`, and
// Alertmanager's own default for that is the pod address, which nobody
// outside the cluster can open; the chart then rendered `externalURL` from
// `vmalert.externalUrl`, which is the Grafana base, a different service.
// `notifications.alertmanagerUrl` is now the one input: set, it is the
// VMAlertmanager's `externalURL` and the base of the silence link; unset,
// the message has no silence line at all.
//
// Beside it, the title and the Grafana link must not end in a dangling
// `/` or `&var-namespace=` for an alert with no namespace, and an `also`
// entry that delivers to Slack must reuse the receiver a primary route
// already renders for the same destination.
package tests

import (
	"bytes"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"text/template"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

type linksAMConfig struct {
	Route struct {
		Routes []map[string]any `yaml:"routes"`
	} `yaml:"route"`
	Receivers []struct {
		Name         string           `yaml:"name"`
		SlackConfigs []map[string]any `yaml:"slack_configs"`
		TelegramCfgs []map[string]any `yaml:"telegram_configs"`
	} `yaml:"receivers"`
}

func renderedLinksAM(t *testing.T, golden string) (string, linksAMConfig) {
	t.Helper()
	var doc map[string]any
	for _, d := range renderedDocs(t, golden) {
		if d["kind"] == "VMAlertmanager" {
			doc = d
		}
	}
	if doc == nil {
		return "", linksAMConfig{}
	}
	ext, _ := dig(doc, "spec", "externalURL").(string)
	raw, _ := dig(doc, "spec", "configRawYaml").(string)
	var cfg linksAMConfig
	require.NoError(t, yaml.Unmarshal([]byte(raw), &cfg))
	return ext, cfg
}

func TestSilenceLinkIsAReachableURLOrAbsent(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)

	var withURL, without int
	for _, g := range goldens {
		ext, cfg := renderedLinksAM(t, g)
		for _, r := range cfg.Receivers {
			var messages []string
			for _, c := range r.SlackConfigs {
				messages = append(messages, c["text"].(string))
			}
			for _, c := range r.TelegramCfgs {
				if m, ok := c["message"].(string); ok {
					messages = append(messages, m)
				}
			}
			for _, m := range messages {
				assert.NotContains(t, m, ".ExternalURL", "%s/%s: the pod address is never a link", g, r.Name)
				if ext == "" {
					without++
					assert.NotContains(t, m, "Silence", "%s/%s: no alertmanagerUrl, so no silence link", g, r.Name)
					continue
				}
				withURL++
				if strings.Contains(m, "<https://karma.") {
					// `notifications.console: karma`: karma's own link, held
					// to karma's type in karma_test.go.
					assert.Contains(t, m, "/?m=", "%s/%s", g, r.Name)
					continue
				}
				assert.Contains(t, m, "<"+ext+"/#/silences/new?filter=%7B", "%s/%s", g, r.Name)
				assert.NotContains(t, m, "%2C%7D", "%s/%s: no trailing separator before the closing brace", g, r.Name)
			}
		}
	}
	assert.NotZero(t, withURL, "no golden proves the silence line")
	assert.NotZero(t, without, "no golden proves its absence")
}

func TestAlertmanagerExternalURLComesOnlyFromAlertmanagerURL(t *testing.T) {
	// `single` sets vmalert.externalUrl (the Grafana base) and no
	// alertmanagerUrl: the VMAlertmanager must not inherit it.
	ext, _ := renderedLinksAM(t, "golden/observability-stack/single.yaml")
	assert.Empty(t, ext)

	ext, _ = renderedLinksAM(t, "golden/observability-stack/notifications-silence-link.yaml")
	assert.Equal(t, "https://alertmanager.example", ext)
}

func TestTitleAndGrafanaLinkSurviveAnEmptyNamespace(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)

	var seen int
	for _, g := range goldens {
		_, cfg := renderedLinksAM(t, g)
		for _, r := range cfg.Receivers {
			for _, c := range r.SlackConfigs {
				seen++
				title := c["title"].(string)
				wantTitle := "on {{ .CommonLabels.k8s_cluster_name }}" +
					"{{ if .CommonLabels.k8s_namespace_name }}/{{ .CommonLabels.k8s_namespace_name }}{{ end }}"
				assert.Contains(t, title, wantTitle, "%s/%s", g, r.Name)
				assert.NotContains(t, title, "}}/{{ .CommonLabels.k8s_namespace_name }}'", "%s/%s: unguarded namespace", g, r.Name)
				text := c["text"].(string)
				assert.Contains(t, text, "{{ if .Labels.k8s_namespace_name }}&amp;var-namespace={{ .Labels.k8s_namespace_name | urlquery }}{{ end }}", "%s/%s", g, r.Name)
				if tl := c["title_link"].(string); !strings.HasPrefix(tl, "https://karma.") {
					assert.Contains(t, tl,
						"{{ if .CommonLabels.k8s_namespace_name }}&var-namespace={{ .CommonLabels.k8s_namespace_name | urlquery }}{{ end }}",
						"%s/%s", g, r.Name)
				}
			}
		}
	}
	assert.NotZero(t, seen)
}

// slackConfigs are the rendered Slack configs of a golden.
func slackConfigsOf(t *testing.T, golden string) []map[string]any {
	t.Helper()
	_, cfg := renderedLinksAM(t, golden)
	var out []map[string]any
	for _, r := range cfg.Receivers {
		out = append(out, r.SlackConfigs...)
	}
	require.NotEmpty(t, out, golden)
	return out
}

// Slack gets named mrkdwn links and a title that is itself a link: Grafana
// on each alert's own line (it is about that alert), and ONE line of group
// links, Silence and View, after the alerts, only the ones the mode has.
func TestSlackLinksAreNamedAndGroupLinksAppearOnce(t *testing.T) {
	labels := amKV{"alertname": "A", "k8s_cluster_name": "c1", "k8s_namespace_name": "ns"}
	other := amKV{"alertname": "A", "k8s_cluster_name": "c1", "k8s_namespace_name": "ns", "pod": "p2"}
	const grafana = "https://grafana.example/?var-cluster=c1&var-namespace=ns"
	cases := []struct {
		golden    string
		group     []string
		titleLink string
	}{
		{"golden/observability-stack/notifications-karma-console.yaml", []string{"Silence", "View"}, "https://karma.example.com/?q="},
		{"golden/observability-stack/notifications-silence-link.yaml", []string{"Silence"}, grafana},
		{"golden/observability-stack/notifications-slack-one-workspace.yaml", nil, grafana},
	}
	for _, tc := range cases {
		for _, c := range slackConfigsOf(t, tc.golden) {
			data := amData{Status: "firing", CommonLabels: labels, Alerts: []amAlert{
				{Labels: labels, Annotations: amKV{"summary": "s1"}},
				{Labels: other, Annotations: amKV{"summary": "s2"}},
			}}
			out := executeSlackText(t, c["text"].(string), data)
			set := slackLinks(t, out)
			assert.Equal(t, tc.group, set.Group, tc.golden)
			assert.Equal(t, []string{grafana, grafana}, set.Grafana, "%s: one Grafana link per alert", tc.golden)
			assert.Equal(t, len(tc.group), strings.Count(out, "|Silence>")+strings.Count(out, "|View>"),
				"%s: group links appear once per message, not once per alert", tc.golden)
			assert.Equal(t, 2, strings.Count(out, "|Grafana>"), tc.golden)
			assert.NotContains(t, out, "Grafana: ", tc.golden)
			assert.NotContains(t, out, "Silence: ", tc.golden)
			assert.Contains(t, out, "?var-cluster=c1&amp;var-namespace=ns|Grafana>", "& is written &amp; inside the link")
			assert.Contains(t, out, "s1 · <https://grafana.example/", "Grafana is on the alert's own line")

			tl, ok := c["title_link"].(string)
			require.True(t, ok, "%s: title_link is set", tc.golden)
			tpl, err := template.New("tl").Funcs(amFuncs).Parse(tl)
			require.NoError(t, err)
			var buf bytes.Buffer
			require.NoError(t, tpl.Execute(&buf, data))
			assert.True(t, strings.HasPrefix(buf.String(), tc.titleLink), "%s: %s", tc.golden, buf.String())
			assert.NotContains(t, buf.String(), "&amp;", "title_link is a plain URL field, not mrkdwn")
			// The View URL when karma, else the Grafana one.
			if slices.Contains(tc.group, "View") {
				assert.Equal(t, set.GroupURLs["View"], buf.String())
			} else {
				assert.Equal(t, grafana, buf.String())
			}

			// Alertmanager's default `mrkdwn_in` includes `text`; the chart
			// must not override it away.
			if in, ok := c["mrkdwn_in"]; ok {
				assert.Contains(t, in, "text")
			}
		}
	}
}

// A label value is user data: a `|` or `>` in one must not end the link.
func TestSlackLinksSurviveHostileLabelValues(t *testing.T) {
	labels := amKV{"alertname": "A|B>C", "k8s_cluster_name": "c|1>", "k8s_namespace_name": "n s&p>q|r", "pod": "x\"y+z w"}
	for _, golden := range []string{
		"golden/observability-stack/notifications-karma-console.yaml",
		"golden/observability-stack/notifications-silence-link.yaml",
	} {
		for _, c := range slackConfigsOf(t, golden) {
			out := executeSlackText(t, c["text"].(string), amData{Status: "firing", CommonLabels: labels,
				Alerts: []amAlert{{Labels: labels, Annotations: amKV{"summary": "s"}}}})
			set := slackLinks(t, out)
			// Exactly the separators of the links themselves: one `|` and
			// one closing `>` per link.
			assert.Equal(t, strings.Count(out, "<"), strings.Count(out, "|"), out)
			assert.Equal(t, strings.Count(out, "<"), strings.Count(out, ">"), out)
			require.Len(t, set.Grafana, 1)
			assert.Equal(t, "https://grafana.example/?var-cluster=c%7C1%3E&var-namespace=n+s%26p%3Eq%7Cr", set.Grafana[0], golden)
			if sil := set.GroupURLs["Silence"]; strings.Contains(sil, "#/silences/") {
				assert.NotContains(t, sil, "+", "a space in a silence filter is %20, not +")
				assert.Contains(t, sil, "n%20s%26p%3Eq%7Cr")
				assert.Contains(t, sil, "x%22y%2Bz%20w")
			}
		}
	}
}

func linksReceiverNames(cfg linksAMConfig) []string {
	var names []string
	for _, r := range cfg.Receivers {
		names = append(names, r.Name)
	}
	sort.Strings(names)
	return names
}

func TestAlsoSlackReusesAPrimaryReceiver(t *testing.T) {
	_, cfg := renderedLinksAM(t, "golden/observability-stack/notifications-slack-also-one-workspace.yaml")
	// `#alerts-critical` is both the critical tier and the second `also`
	// entry: one receiver. `#mirror` is the first entry's alone.
	assert.Equal(t, []string{
		"notifications-none", "slack-acme--alerts", "slack-acme--alerts-critical", "slack-acme--mirror",
	}, linksReceiverNames(cfg))

	_, cfg = renderedLinksAM(t, "golden/observability-stack/notifications-slack-also-two-workspaces.yaml")
	assert.Equal(t, []string{
		"notifications-none", "slack-acme--alerts", "slack-acme--alerts-critical--here", "slack-partner--partner-alerts",
	}, linksReceiverNames(cfg))

	// The `also` routes follow the wrapped primary tree, each to its receiver.
	var also []string
	for _, r := range cfg.Route.Routes {
		if rcv, _ := r["receiver"].(string); strings.HasPrefix(rcv, "slack-") {
			also = append(also, rcv)
		}
	}
	assert.Equal(t, []string{"slack-partner--partner-alerts", "slack-acme--alerts-critical--here"}, also)
}

func TestEveryAlsoRouteContinues(t *testing.T) {
	// An alert matching two `also` entries must reach both receivers, and
	// the primary tree: only `continue: true` on every one of them does it.
	_, cfg := renderedLinksAM(t, "golden/observability-stack/notifications-slack-also-two-workspaces.yaml")
	var also int
	for _, r := range cfg.Route.Routes {
		if rcv, _ := r["receiver"].(string); strings.HasPrefix(rcv, "slack-") {
			also++
			assert.Equal(t, true, r["continue"], "also route to %s", rcv)
		}
	}
	assert.Equal(t, 2, also)

	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)
	for _, g := range goldens {
		_, c := renderedLinksAM(t, g)
		routes := c.Route.Routes
		for i, r := range routes {
			if after := wrapperIndex(routes); after >= 0 && i > after {
				assert.Equal(t, true, r["continue"], "%s: also route %d", g, i)
			}
		}
	}
}

func wrapperIndex(routes []map[string]any) int {
	for i, r := range routes {
		if m, ok := r["match"].(map[string]any); ok && len(m) == 0 && r["routes"] != nil {
			return i
		}
	}
	return -1
}

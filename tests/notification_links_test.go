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
	"path/filepath"
	"sort"
	"strings"
	"testing"

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
					assert.NotContains(t, m, "Silence:", "%s/%s: no alertmanagerUrl, so no silence line", g, r.Name)
					continue
				}
				withURL++
				assert.Contains(t, m, "Silence: "+ext+"/#/silences/new?filter=%7B", "%s/%s", g, r.Name)
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
				assert.Contains(t, text, "{{ if .Labels.k8s_namespace_name }}&var-namespace={{ .Labels.k8s_namespace_name }}{{ end }}", "%s/%s", g, r.Name)
			}
		}
	}
	assert.NotZero(t, seen)
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

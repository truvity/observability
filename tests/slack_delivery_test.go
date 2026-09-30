// Slack posts with a bot token, and the alert that says Slack is failing
// must never be sent through Slack.
//
// A Slack incoming webhook ignores the `channel` a message asks for, so
// this chart posts with a Slack app's bot token instead
// (`app_token_file`). Two things follow that a golden diff shows but
// nobody reads for, and this checks both against every rendered
// VMAlertmanager in every golden:
//
//   - a Slack receiver names its channel and reads its credential from
//     `app_token_file` — never `api_url_file`, which would silently bring
//     the webhook's one-channel behaviour back;
//   - when any Slack receiver exists, the FIRST route of the tree matches
//     `SlackNotificationsFailing`, stops there (`continue: false`) and
//     goes to a receiver that is not a Slack one, and the VMRule that
//     fires it is rendered.
package tests

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestSlackReceiversUseABotTokenAndTheFailureAlertNeverReachesSlack(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, goldens)

	var withSlack int

	for _, g := range goldens {
		docs := renderedDocs(t, g)

		var am map[string]any
		for _, d := range docs {
			if d["kind"] == "VMAlertmanager" {
				am = d
			}
		}
		if am == nil {
			continue
		}

		raw, _ := dig(am, "spec", "configRawYaml").(string)

		var cfg struct {
			Route struct {
				Routes []amRoute `yaml:"routes"`
			} `yaml:"route"`
			Receivers []struct {
				Name         string           `yaml:"name"`
				SlackConfigs []map[string]any `yaml:"slack_configs"`
			} `yaml:"receivers"`
		}
		require.NoErrorf(t, yaml.Unmarshal([]byte(raw), &cfg), "%s: configRawYaml is not YAML", g)

		slackReceivers := map[string]bool{}

		for _, r := range cfg.Receivers {
			for _, c := range r.SlackConfigs {
				slackReceivers[r.Name] = true

				assert.NotEmptyf(t, c["channel"], "%s: Slack receiver %s names no channel", g, r.Name)
				assert.NotEmptyf(t, c["app_token_file"], "%s: Slack receiver %s has no app_token_file", g, r.Name)
				assert.NotContainsf(t, c, "api_url_file", "%s: Slack receiver %s reads a webhook", g, r.Name)
				assert.NotContainsf(t, c, "update_message",
					"%s: Slack receiver %s sets update_message, which crashes Alertmanager v0.34.0 at config load", g, r.Name)
			}
		}

		if len(slackReceivers) == 0 {
			continue
		}
		withSlack++

		require.NotEmptyf(t, cfg.Route.Routes, "%s: no routes", g)
		first := cfg.Route.Routes[0]
		assert.Equalf(t, []string{`alertname = "SlackNotificationsFailing"`}, first.Matchers,
			"%s: the first route is not the Slack delivery alert's", g)
		assert.Falsef(t, first.Continue, "%s: the Slack delivery alert's route must stop the walk", g)
		assert.Falsef(t, slackReceivers[first.Receiver],
			"%s: the Slack delivery alert is routed to Slack (%s) — it would fail to deliver itself", g, first.Receiver)

		// The alert itself is rendered, and watches the Slack integration.
		var found bool
		for _, d := range docs {
			if d["kind"] != "VMRule" {
				continue
			}
			groups, _ := dig(d, "spec", "groups").([]any)
			for _, grp := range groups {
				rules, _ := dig(grp, "rules").([]any)
				for _, r := range rules {
					if dig(r, "alert") == "SlackNotificationsFailing" {
						found = true
						expr, _ := dig(r, "expr").(string)
						assert.Containsf(t, expr, `alertmanager_notifications_failed_total{integration="slack"}`, "%s: wrong expression", g)
						assert.Truef(t, strings.Contains(expr, "increase("), "%s: the rule must watch an increase, not a total", g)
					}
				}
			}
		}
		assert.Truef(t, found, "%s: Slack receivers exist but SlackNotificationsFailing is not rendered", g)
	}

	require.Positive(t, withSlack, "no golden with a Slack receiver; this test would pass vacuously")
}

// A `mention` is text in one receiver, for FIRING notifications only, and
// only where a destination asked for it. The golden
// `notifications-slack-mention` has critical (`here`) and warning (none)
// in one channel, a `channel` catch-all and a route override that keeps
// the tier's mention.
func TestSlackMentionAppearsOnlyWhereConfiguredAndOnlyWhenFiring(t *testing.T) {
	docs := renderedDocs(t, "golden/observability-stack/notifications-slack-mention.yaml")

	var raw string
	for _, d := range docs {
		if d["kind"] == "VMAlertmanager" {
			raw, _ = dig(d, "spec", "configRawYaml").(string)
		}
	}
	require.NotEmpty(t, raw)

	var cfg struct {
		Receivers []struct {
			Name         string           `yaml:"name"`
			SlackConfigs []map[string]any `yaml:"slack_configs"`
		} `yaml:"receivers"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(raw), &cfg))

	want := map[string]string{
		"slack-acme--alerts--here":                    "<!here>",
		"slack-acme--alerts":                          "",
		"slack-acme--alerts-everything-else--channel": "<!channel>",
		"slack-acme--example-app-critical--here":      "<!here>",
		"slack-acme--example-app--channel":            "<!channel>",
	}

	seen := map[string]bool{}
	for _, r := range cfg.Receivers {
		for _, c := range r.SlackConfigs {
			mention, ok := want[r.Name]
			require.Truef(t, ok, "unexpected Slack receiver %s", r.Name)
			seen[r.Name] = true

			text, _ := c["text"].(string)
			if mention == "" {
				assert.NotContainsf(t, text, "<!", "%s: no mention was configured", r.Name)
				continue
			}
			// At the very start, and inside a firing-only branch.
			prefix := `{{ if eq .Status "firing" }}` + mention + ` {{ end }}`
			assert.Truef(t, strings.HasPrefix(text, prefix), "%s: text must start with the firing-only %s, got %q", r.Name, mention, text)
			assert.Equalf(t, 1, strings.Count(text, "<!"), "%s: exactly one mention", r.Name)
		}
	}
	for name := range want {
		assert.Truef(t, seen[name], "Slack receiver %s is not rendered", name)
	}
}

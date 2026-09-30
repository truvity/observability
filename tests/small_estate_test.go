// The single-operator switches (docs/reference.md, "The single-operator
// estate"), held against the rendered goldens rather than against the
// values that produce them.
//
// Each property here is one whose failure is SILENT: a drop route
// rendered after the primary tree's `continue: true` node drops nothing,
// a catch-all that is only the receiver of the last node misses every
// `info` alert inside a project route, and a Grafana admitted to the
// stores while the proxy is still in front of them is a way around the
// proxy. None of those fails a render, an apply or an amtool check.
package tests

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// amRoute is the part of an Alertmanager route these tests read.
type amRoute struct {
	Receiver string            `yaml:"receiver"`
	Match    map[string]string `yaml:"match"`
	Matchers []string          `yaml:"matchers"`
	Continue bool              `yaml:"continue"`
	Routes   []amRoute         `yaml:"routes"`
}

type amConfig struct {
	Route        amRoute `yaml:"route"`
	InhibitRules []struct {
		SourceMatchers []string `yaml:"source_matchers"`
		Equal          []string `yaml:"equal"`
	} `yaml:"inhibit_rules"`
	Receivers []struct {
		Name            string           `yaml:"name"`
		TelegramConfigs []map[string]any `yaml:"telegram_configs"`
	} `yaml:"receivers"`
}

func alertmanagerConfig(t *testing.T, golden string) amConfig {
	t.Helper()
	am := findDoc(t, renderedDocs(t, golden), "VMAlertmanager", nil)
	raw, ok := dig(am, "spec", "configRawYaml").(string)
	require.True(t, ok, "%s: VMAlertmanager has no configRawYaml", golden)

	var cfg amConfig
	require.NoError(t, yaml.Unmarshal([]byte(raw), &cfg))
	return cfg
}

// primaryTree is the one `match: {}` node with `continue: true`.
func primaryTree(t *testing.T, cfg amConfig) (int, amRoute) {
	t.Helper()
	for i, r := range cfg.Route.Routes {
		if r.Continue && len(r.Match) == 0 && len(r.Matchers) == 0 {
			return i, r
		}
	}
	t.Fatal("no primary tree (a match-everything node with continue: true)")
	return -1, amRoute{}
}

// TestDropAndSilencedWatchdogPrecedeThePrimaryTree: the primary tree
// `continue`s past itself, so a null route AFTER it would receive the
// alert only once the primary tree had ALREADY delivered it.
func TestDropAndSilencedWatchdogPrecedeThePrimaryTree(t *testing.T) {
	cfg := alertmanagerConfig(t, "golden/observability-stack/small-estate.yaml")
	primary, _ := primaryTree(t, cfg)

	var dropped, watchdog bool
	for i, r := range cfg.Route.Routes {
		if r.Receiver != "notifications-none" {
			continue
		}
		assert.Less(t, i, primary, "a null route at position %d sits after the primary tree (%d) and drops nothing", i, primary)
		assert.False(t, r.Continue, "a null route with continue: true drops nothing")
		if r.Match["alertname"] == "InfoInhibitor" {
			dropped = true
		}
		for _, m := range r.Matchers {
			if m == `alertname = "Watchdog"` {
				watchdog = true
			}
		}
	}
	assert.True(t, dropped, "notifications.drop's InfoInhibitor route is not rendered")
	assert.True(t, watchdog, "no deadman receiver and a catch-all set, but Watchdog is not routed to nobody: the heartbeat would reach the catch-all "+
		"every repeat_interval")
}

// TestCatchAllIsTheFallbackOfEveryPrimaryNode: Alertmanager delivers to
// the DEEPEST matching node's receiver. An `info` alert in a project
// namespace matches the project's node and none of its tier children, so
// the catch-all has to be that node's receiver too — not only the root's.
func TestCatchAllIsTheFallbackOfEveryPrimaryNode(t *testing.T) {
	for golden, want := range map[string]string{
		"golden/observability-stack/notifications-catchall.yaml": "slack-acme--alerts-everything-else",
		"golden/observability-stack/small-estate.yaml":           "telegram-chatfile",
		"golden/observability-stack/single.yaml":                 "notifications-none",
	} {
		cfg := alertmanagerConfig(t, golden)
		_, tree := primaryTree(t, cfg)
		assert.Equal(t, want, tree.Receiver, "%s: primary tree", golden)
		for _, r := range tree.Routes {
			if _, isTier := r.Match["severity"]; isTier {
				continue
			}
			assert.Equal(t, want, r.Receiver, "%s: project route %v", golden, r.Match)
		}
	}
}

// TestChatIDFromASecretIsAFileNeverAValue: with `chatIdSecret` the chat id
// must never appear in the config, only the mounted file it is read from.
func TestChatIDFromASecretIsAFileNeverAValue(t *testing.T) {
	cfg := alertmanagerConfig(t, "golden/observability-stack/small-estate.yaml")
	var seen int
	for _, r := range cfg.Receivers {
		for _, tc := range r.TelegramConfigs {
			seen++
			assert.NotContains(t, tc, "chat_id", "receiver %s carries a literal chat_id beside chatIdSecret", r.Name)
			assert.Equal(t, "/etc/alertmanager/notifications-telegram-chat/chat_id", tc["chat_id_file"], "receiver %s", r.Name)
		}
	}
	require.NotZero(t, seen, "no telegram receiver rendered at all")
}

// storePeers returns every podSelector's labels admitted by the store
// NetworkPolicies of one golden.
func storePeers(t *testing.T, golden string) map[string][]map[string]any {
	t.Helper()
	out := map[string][]map[string]any{}
	for _, d := range renderedDocs(t, golden) {
		if d["kind"] != "NetworkPolicy" {
			continue
		}
		name, _ := dig(d, "metadata", "name").(string)
		if !strings.HasSuffix(name, "-store") {
			continue
		}
		rules, _ := dig(d, "spec", "ingress").([]any)
		for _, rule := range rules {
			from, _ := dig(rule, "from").([]any)
			for _, peer := range from {
				if p, ok := peer.(map[string]any); ok {
					out[name] = append(out[name], p)
				}
			}
		}
	}
	require.NotEmpty(t, out, "%s: no store NetworkPolicy", golden)
	return out
}

func admitsName(peers []map[string]any, name string) bool {
	for _, p := range peers {
		if dig(p, "podSelector", "matchLabels", "app.kubernetes.io/name") == name {
			return true
		}
	}
	return false
}

// TestGrafanaReachesTheStoresOnlyWithoutTheProxy: with vmauth in front,
// Grafana admitted to a store directly is a way around every grant the
// proxy enforces.
func TestGrafanaReachesTheStoresOnlyWithoutTheProxy(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)

	var direct int
	for _, g := range goldens {
		docs := renderedDocs(t, g)
		hasProxy := false
		for _, d := range docs {
			if d["kind"] == "VMAuth" {
				hasProxy = true
			}
		}
		hasPolicy := false
		for _, d := range docs {
			if d["kind"] == "NetworkPolicy" {
				hasPolicy = true
			}
		}
		if !hasPolicy {
			continue
		}
		for name, peers := range storePeers(t, g) {
			if admitsName(peers, "grafana") {
				assert.False(t, hasProxy, "%s: %s admits Grafana to a store while vmauth is rendered", g, name)
				direct++
			}
		}
	}
	require.NotZero(t, direct, "no golden exercises Grafana without the proxy")
}

// TestClientsFromReachOnlyTheStoresTheyName: the prober in small-estate
// names the metrics store only.
func TestClientsFromReachOnlyTheStoresTheyName(t *testing.T) {
	peers := storePeers(t, "golden/observability-stack/small-estate.yaml")
	for name, p := range peers {
		isMetrics := name == "observability-stack-metrics-store"
		assert.Equal(t, isMetrics, admitsName(p, "example-prober"), "%s: prober admission", name)
		assert.True(t, admitsName(p, "example-collector"), "%s: the collector names every store", name)
	}
}

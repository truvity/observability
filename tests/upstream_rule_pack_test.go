// The vendored upstream rule pack (restructure step 4).
//
// charts/platform-alerts/upstream/ holds the rule groups that
// victoria-metrics-k8s-stack's sync job fetches and applies at run time, at
// a pinned commit, so that the same rules can come from this repository's own
// chart with every rule individually switchable. hack/vendor-upstream-rules.sh
// produces the files and upstream/PIN.yaml; it needs Docker and the network
// and is not run in CI. These tests are what CI can hold still: the files
// are the ones the pin record describes, the chart's keys are the pack's
// groups, the pack covers exactly what the stack's sync job renders by
// default, and the rendered objects carry the ownership labels the contract
// asks for.
package tests

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

const packDir = "../charts/platform-alerts/upstream"

type packPin struct {
	Stack struct {
		Chart        string `yaml:"chart"`
		Version      string `yaml:"version"`
		SyncJobImage string `yaml:"syncJobImage"`
	} `yaml:"stack"`
	Placeholders struct {
		ClusterLabel          string `yaml:"clusterLabel"`
		AlertmanagerNamespace string `yaml:"alertmanagerNamespace"`
	} `yaml:"placeholders"`
	Sources []struct {
		URL    string   `yaml:"url"`
		Repo   string   `yaml:"repo"`
		Branch string   `yaml:"branch"`
		Commit string   `yaml:"commit"`
		Path   string   `yaml:"path"`
		SHA256 string   `yaml:"sha256"`
		Groups []string `yaml:"groups"`
	} `yaml:"sources"`
	Groups []struct {
		Name           string   `yaml:"name"`
		File           string   `yaml:"file"`
		SHA256         string   `yaml:"sha256"`
		RuleType       string   `yaml:"ruleType"`
		DefaultEnabled bool     `yaml:"defaultEnabled"`
		Source         string   `yaml:"source"`
		Rules          []string `yaml:"rules"`
	} `yaml:"groups"`
}

func loadPin(t *testing.T) packPin {
	t.Helper()

	b, err := os.ReadFile(filepath.Join(packDir, "PIN.yaml"))
	require.NoError(t, err)

	var p packPin
	require.NoError(t, yaml.Unmarshal(b, &p))
	require.NotEmpty(t, p.Groups)

	return p
}

type packGroup struct {
	Name  string           `yaml:"name"`
	Rules []map[string]any `yaml:"rules"`
}

// TestVendoredFilesMatchThePinRecord: the committed files are exactly the
// ones the pin record lists, byte for byte, and the record is complete: every
// source is pinned to a full commit, every group names its source, and the
// rule names recorded are the ones in the file.
func TestVendoredFilesMatchThePinRecord(t *testing.T) {
	p := loadPin(t)

	assert.Equal(t, "victoria-metrics-k8s-stack", p.Stack.Chart)
	assert.Regexp(t, `^ghcr\.io/victoriametrics/sync-job:v\d+\.\d+\.\d+$`, p.Stack.SyncJobImage)

	// The pin names the k8s-stack version the files were produced for: when
	// the dependency moves, the pack has to be re-vendored with it.
	chart, err := os.ReadFile("../charts/observability-stack/Chart.yaml")
	require.NoError(t, err)
	assert.Containsf(t, string(chart), "name: victoria-metrics-k8s-stack\n    version: "+p.Stack.Version+"\n",
		"upstream/PIN.yaml was vendored for victoria-metrics-k8s-stack %s, but the stack depends on another version: "+
			"run hack/vendor-upstream-rules.sh", p.Stack.Version)

	sources := map[string]bool{}
	for _, s := range p.Sources {
		sources[s.URL] = true

		assert.Regexpf(t, `^[0-9a-f]{40}$`, s.Commit, "source %s is not pinned to a full commit", s.URL)
		assert.Regexp(t, `^[0-9a-f]{64}$`, s.SHA256)
		assert.NotEmpty(t, s.Groups)
		assert.Truef(t, strings.HasSuffix(s.URL, "/"+s.Branch+"/"+s.Path), "source %s: url does not match branch and path", s.URL)
	}

	listed := map[string]bool{}

	for _, g := range p.Groups {
		listed[g.File] = true

		b, err := os.ReadFile(filepath.Join(packDir, g.File))
		require.NoErrorf(t, err, "%s: listed in PIN.yaml but missing", g.File)

		sum := sha256.Sum256(b)
		assert.Equalf(t, g.SHA256, hex.EncodeToString(sum[:]),
			"%s differs from its pin record: re-vendor with hack/vendor-upstream-rules.sh, do not edit by hand", g.File)
		assert.Truef(t, sources[g.Source], "group %s names source %s, which is not pinned", g.Name, g.Source)

		var pg packGroup
		require.NoError(t, yaml.Unmarshal(b, &pg))
		assert.Equal(t, g.Name, pg.Name)

		var names []string

		kinds := map[string]bool{}

		for _, r := range pg.Rules {
			if a, ok := r["alert"].(string); ok {
				names = append(names, a)
				kinds["alert"] = true
			} else {
				names = append(names, r["record"].(string))
				kinds["recording"] = true
			}
		}

		assert.Equalf(t, g.Rules, names, "%s: the rule names recorded in PIN.yaml are not the file's", g.File)

		want := "alert"
		if !kinds["alert"] {
			want = "recording"
		}

		assert.Equalf(t, want, g.RuleType, "%s: ruleType", g.File)
	}

	files, err := filepath.Glob(filepath.Join(packDir, "groups", "*"))
	require.NoError(t, err)

	for _, f := range files {
		rel, err := filepath.Rel(packDir, f)
		require.NoError(t, err)
		assert.Truef(t, listed[rel], "%s is not in PIN.yaml: a vendored file nobody recorded", rel)
	}

	// The two install-specific values the sync job substitutes are
	// placeholders, never a real value; and nothing else looks like one.
	for _, g := range p.Groups {
		b, _ := os.ReadFile(filepath.Join(packDir, g.File))
		assert.NotContainsf(t, string(b), "k8s_cluster_name", "%s: the cluster label must be the placeholder", g.File)
	}
}

// TestPackKeysAreThePinnedGroups: values.yaml and the schema list exactly the
// pinned groups, and each group's default is the one the pin recorded from the
// stack's sync-job config (the groups it leaves off by default stay off).
func TestPackKeysAreThePinnedGroups(t *testing.T) {
	p := loadPin(t)

	b, err := os.ReadFile("../charts/platform-alerts/values.yaml")
	require.NoError(t, err)

	var v struct {
		Groups struct {
			Upstream map[string]any `yaml:"upstream"`
		} `yaml:"groups"`
	}
	require.NoError(t, yaml.Unmarshal(b, &v))

	var want []string

	for _, g := range p.Groups {
		want = append(want, g.Name)

		cfg, ok := v.Groups.Upstream[g.Name].(map[string]any)
		require.Truef(t, ok, "values.yaml groups.upstream has no %q", g.Name)
		assert.Equalf(t, g.DefaultEnabled, cfg["enabled"], "groups.upstream.%s.enabled default", g.Name)
		assert.Equalf(t, []any{}, cfg["exclude"], "groups.upstream.%s.exclude default", g.Name)
	}

	var have []string

	for k := range v.Groups.Upstream {
		if k != "enabled" && k != "alertmanagerNamespace" {
			have = append(have, k)
		}
	}

	sort.Strings(want)
	sort.Strings(have)
	assert.Equal(t, want, have, "values.yaml groups.upstream keys are not the pinned groups")
	assert.Equal(t, false, v.Groups.Upstream["enabled"], "the pack is OFF by default")

	// The schema has a property for each.
	sb, err := os.ReadFile("../charts/platform-alerts/values.schema.json")
	require.NoError(t, err)

	for _, g := range p.Groups {
		assert.Containsf(t, string(sb), `"`+g.Name+`": { "$ref": "#/definitions/upstreamGroup" }`, "schema: groups.upstream.%s", g.Name)
	}
}

// TestPackCoversWhatTheStacksSyncJobRenders ties the pack to the stack: the
// rule sources the stack's sync job has ENABLED on a stock install are the
// pack's sources, and the groups it switches off by default are the pack's
// default-off groups. A change to the stack's defaults (or a k8s-stack bump
// that adds a source) that the pack does not follow fails here.
func TestPackCoversWhatTheStacksSyncJobRenders(t *testing.T) {
	p := loadPin(t)

	var cfg syncJobFullConfig

	for _, doc := range splitDocs(t, "golden/observability-stack/minimal.yaml") {
		var cm struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Data struct {
				ConfigYAML string `yaml:"config.yaml"`
			} `yaml:"data"`
		}
		if yaml.Unmarshal(doc, &cm) != nil || cm.Kind != "ConfigMap" || !strings.HasSuffix(cm.Metadata.Name, "-sync-job-config") {
			continue
		}

		require.NoError(t, yaml.Unmarshal([]byte(cm.Data.ConfigYAML), &cfg))
	}

	require.NotEmpty(t, cfg.Rules.Sources)

	enabled := map[string]bool{}

	for _, s := range cfg.Rules.Sources {
		if s.Enabled == nil || *s.Enabled {
			enabled[s.URL] = true
		}
	}

	pinned := map[string]bool{}
	for _, s := range p.Sources {
		pinned[s.URL] = true
	}

	assert.Equal(t, enabled, pinned, "the sources the stack's sync job renders enabled by default are not the pack's pinned sources: re-vendor")

	// Groups the sync job config turns off by default.
	var cfgFull struct {
		Rules struct {
			Groups map[string]struct {
				Enabled *bool `yaml:"enabled"`
			} `yaml:"groups"`
		} `yaml:"rules"`
	}

	for _, doc := range splitDocs(t, "golden/observability-stack/minimal.yaml") {
		var cm struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Data struct {
				ConfigYAML string `yaml:"config.yaml"`
			} `yaml:"data"`
		}
		if yaml.Unmarshal(doc, &cm) == nil && cm.Kind == "ConfigMap" && strings.HasSuffix(cm.Metadata.Name, "-sync-job-config") {
			require.NoError(t, yaml.Unmarshal([]byte(cm.Data.ConfigYAML), &cfgFull))
		}
	}

	off := map[string]bool{}
	for n, g := range cfgFull.Rules.Groups {
		if g.Enabled != nil && !*g.Enabled {
			off[n] = true
		}
	}

	pinOff := map[string]bool{}

	for _, g := range p.Groups {
		if !g.DefaultEnabled {
			pinOff[g.Name] = true
		}
	}

	assert.Equal(t, off, pinOff, "the groups the stack's sync job switches off by default are not the pack's default-off groups")
}

type renderedPack struct {
	byName map[string]vmRuleDoc // VMRule object name -> object
	groups map[string][]string  // group name -> rule names (alert or record)
	raw    string
}

func loadRenderedPack(t *testing.T, golden string) renderedPack {
	t.Helper()

	raw, err := os.ReadFile(golden)
	require.NoError(t, err)

	r := renderedPack{byName: map[string]vmRuleDoc{}, groups: map[string][]string{}, raw: string(raw)}

	for _, doc := range splitDocs(t, golden) {
		var v vmRuleDoc
		if yaml.Unmarshal(doc, &v) != nil || v.Kind != "VMRule" {
			continue
		}

		r.byName[v.Metadata.Name] = v

		for _, g := range v.Spec.Groups {
			for _, rule := range g.Rules {
				n := rule.Alert
				if n == "" {
					n = rule.Record
				}

				r.groups[g.Name] = append(r.groups[g.Name], n)
			}
		}
	}

	return r
}

// TestRenderedPackKeepsUpstreamIdentities: the groups render under upstream's
// own group names with upstream's rule names (silences, routes and the
// `alertgroup` label depend on them), each in its own object carrying the
// ownership labels with the right rule-type, and `exclude`/`override` do what
// they say.
func TestRenderedPackKeepsUpstreamIdentities(t *testing.T) {
	p := loadPin(t)
	r := loadRenderedPack(t, "golden/platform-alerts/upstream-pack.yaml")

	pinRules := map[string][]string{}
	for _, g := range p.Groups {
		pinRules[g.Name] = g.Rules
	}

	for name, obj := range r.byName {
		assert.Equal(t, "metrics", obj.Metadata.Labels["observability.truvity.io/evaluator"], name)
		require.Len(t, obj.Spec.Groups, 1, "%s: one group per object", name)

		g := obj.Spec.Groups[0]
		assert.Containsf(t, pinRules, g.Name, "%s: not a vendored group", name)

		want := "recording"

		for _, rule := range g.Rules {
			if rule.Alert != "" {
				want = "alert"
			}
		}

		assert.Equalf(t, want, obj.Metadata.Labels["observability.truvity.io/rule-type"], "%s: rule-type", name)

		// Nothing the sync job did not add: a rule carries upstream's own
		// labels only, never this chart's commonLabels.
		for _, rule := range g.Rules {
			assert.NotContainsf(t, rule.Labels, "k8s_cluster_name", "%s / %s: commonLabels leaked into an upstream rule", g.Name, rule.Alert)
		}
	}

	// Enabled by default and untouched: every pinned rule is there, in order.
	for _, g := range []string{"general.rules", "kube-prometheus-general.rules", "k8s.rules.pod_owner", "vm-health", "node.rules"} {
		assert.Equalf(t, pinRules[g], r.groups[g], "group %s", g)
	}

	// Default-off groups stay off unless switched on.
	assert.NotContains(t, r.groups, "kube-apiserver-slos")
	assert.NotContains(t, r.groups, "kubelet.rules")

	// exclude: by name, the rest of the group intact.
	assert.NotContains(t, r.groups["kubernetes-apps"], "KubeJobFailed")
	assert.NotContains(t, r.groups["kubernetes-apps"], "KubeDaemonSetMisScheduled")
	assert.NotContains(t, r.groups["kubernetes-apps"], "KubeDaemonSetRolloutStuck")
	assert.Contains(t, r.groups["kubernetes-apps"], "KubePodCrashLooping")
	assert.Len(t, r.groups["kubernetes-apps"], len(pinRules["kubernetes-apps"])-3)

	// A default-off group switched on, trimmed to what is left (both
	// KubeletClientCertificateExpiration alerts go with one name).
	assert.Equal(t, []string{"KubeNodeNotReady", "KubeNodeUnreachable"}, r.groups["kubernetes-system-kubelet"])

	// A group excluded down to nothing renders no object.
	assert.NotContains(t, r.groups, "node-network")

	// override: only the named field moves.
	var churn *struct {
		Alert, Record, Expr, For string
		Labels, Annotations      map[string]string
	}

	for _, o := range r.byName {
		for _, g := range o.Spec.Groups {
			if g.Name != "vmsingle" {
				continue
			}

			for _, rule := range g.Rules {
				if rule.Alert == "TooHighChurnRate24h" {
					churn = &struct {
						Alert, Record, Expr, For string
						Labels, Annotations      map[string]string
					}{rule.Alert, rule.Record, rule.Expr, rule.For, rule.Labels, rule.Annotations}
				}
			}
		}
	}

	require.NotNil(t, churn)
	assert.Contains(t, churn.Expr, "* 6)")
	assert.Equal(t, "warning", churn.Labels["severity"], "an override of expr keeps upstream's labels")

	// The placeholders are substituted.
	assert.NotContains(t, r.raw, p.Placeholders.ClusterLabel)
	assert.NotContains(t, r.raw, p.Placeholders.AlertmanagerNamespace)
	assert.Contains(t, r.raw, `namespace=~"example-observability"`)
	assert.Regexp(t, regexp.MustCompile(`cluster \{\{ \$labels\.k8s_cluster_name \}\}`), r.raw)

	// The Watchdog the stack no longer renders under the preset is here.
	assert.Contains(t, r.groups["general.rules"], "Watchdog")

	// The stack's long-standing replacement for RecordingRulesNoData's
	// expression is the pack's default override.
	assert.Contains(t, r.raw, `recording!~"count:up0"`)
}

// TestStackKeepsItsDefaultsAndStepsAsideOnOptIn: the stack's default render
// still applies the upstream rules through the sync job and renders no
// second source; the preset's render turns the rule sync off and does not
// add its own Watchdog next to the pack's.
func TestStackKeepsItsDefaultsAndStepsAsideOnOptIn(t *testing.T) {
	ruleSources := func(golden string) (n int, watchdog bool) {
		for _, doc := range splitDocs(t, golden) {
			var cm struct {
				Kind     string `yaml:"kind"`
				Metadata struct {
					Name string `yaml:"name"`
				} `yaml:"metadata"`
				Data struct {
					ConfigYAML string `yaml:"config.yaml"`
				} `yaml:"data"`
			}
			if yaml.Unmarshal(doc, &cm) != nil {
				continue
			}

			switch {
			case cm.Kind == "VMRule" && strings.HasSuffix(cm.Metadata.Name, "-watchdog"):
				watchdog = true
			case cm.Kind == "ConfigMap" && strings.HasSuffix(cm.Metadata.Name, "-sync-job-config"):
				var cfg syncJobFullConfig
				require.NoError(t, yaml.Unmarshal([]byte(cm.Data.ConfigYAML), &cfg))
				n = len(cfg.Rules.Sources)
			}
		}

		return n, watchdog
	}

	n, wd := ruleSources("golden/observability-stack/minimal.yaml")
	assert.Positive(t, n, "default: the sync job applies the upstream rules")
	assert.False(t, wd, "default: the sync job's general.rules carries the Watchdog")

	n, wd = ruleSources("golden/observability-stack/upstream-rules-platform-alerts.yaml")
	assert.Zero(t, n, "upstreamRules.source platform-alerts: the sync job has no rule source")
	assert.False(t, wd, "the pack's general.rules carries the Watchdog; the stack renders no second one")
}

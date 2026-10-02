// The VMRule CRD defaults `alert` and `record` to "" on every rule, and the
// API server applies the default on admission. ArgoCD compares the rendered
// manifest with the STORED object field for field, so a rule that leaves
// either out is OutOfSync forever on a field nothing ever changed (observed
// on a live cluster: `record: ""` was the whole diff of observability-rum's
// VMRule). Same class as relabel_action_defaults_test.go, which holds the
// ServiceMonitors to the same standard.
//
// This holds observability-rum's golden renders to it: every rule names both
// keys, one of them empty.
package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestRumVMRuleWritesOutTheDefaultedRuleKeys(t *testing.T) {
	matches, err := filepath.Glob("golden/observability-rum/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, matches)

	rules := 0
	for _, path := range matches {
		raw, err := os.ReadFile(path)
		require.NoError(t, err)
		dec := yaml.NewDecoder(strings.NewReader(string(raw)))
		for {
			var doc map[string]any
			if dec.Decode(&doc) != nil {
				break
			}
			if doc["kind"] != "VMRule" {
				continue
			}
			spec, _ := doc["spec"].(map[string]any)
			groups, _ := spec["groups"].([]any)
			for _, g := range groups {
				for _, r := range g.(map[string]any)["rules"].([]any) {
					rule := r.(map[string]any)
					alert, hasAlert := rule["alert"]
					record, hasRecord := rule["record"]
					require.Truef(t, hasAlert && hasRecord,
						"%s: a rule must write both `alert` and `record` (the CRD defaults the unused one to \"\", ArgoCD diffs forever): %v", path, rule)
					require.Truef(t, alert == "" || record == "",
						"%s: a rule is either an alert or a recording rule: %v", path, rule)
					rules++
				}
			}
		}
	}
	require.Positive(t, rules, "no VMRule rules found in the observability-rum goldens: this guard checks nothing")
}

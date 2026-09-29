// The inhibit rule's label guard (notifications.inhibit.requireLabels,
// 0.11.0; docs/notifications.md, "Inhibition").
//
// Alertmanager compares a label missing on BOTH alerts as equal. So an
// inhibit rule `equal: [k8s_cluster_name, k8s_namespace_name]` whose
// source carries neither label matches every target that carries
// neither — one critical mutes every such warning in the install, and
// nothing anywhere says so. It happened on a default install:
// platform-alerts' CronJobNotSucceeding (critical) and BackupJobFailed
// (warning) aggregate `by (namespace, ...)`, which drops
// k8s_namespace_name, so one CronJob not succeeding muted every failed
// backup Job's warning on the cluster.
//
// The guard is a `<label> =~ ".+"` source matcher per `equal` label.
// These tests hold it against every rendered Alertmanager config, and
// hold the escape hatch (`requireLabels: false`) to the rule 0.10.0
// rendered, byte for byte.
package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEveryInhibitEqualLabelIsGuarded: in every golden but the one that
// opts out, each `equal` label has a non-empty source matcher.
func TestEveryInhibitEqualLabelIsGuarded(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)

	var checked int
	for _, g := range goldens {
		if strings.HasSuffix(g, "/single-inhibit-unguarded.yaml") {
			continue
		}
		var hasAM bool
		for _, d := range renderedDocs(t, g) {
			if d["kind"] == "VMAlertmanager" {
				hasAM = true
			}
		}
		if !hasAM {
			continue
		}
		for _, rule := range alertmanagerConfig(t, g).InhibitRules {
			require.NotEmpty(t, rule.Equal, "%s: an inhibit rule with no `equal` mutes every warning", g)
			for _, label := range rule.Equal {
				assert.Contains(t, rule.SourceMatchers, label+` =~ ".+"`,
					"%s: `equal` label %q has no guard, so a critical without it mutes every warning without it", g, label)
			}
			checked++
		}
	}
	require.NotZero(t, checked, "no golden renders an inhibit rule at all")
}

// TestUnguardedInhibitIsTheOldRule: `requireLabels: false` is `single`
// with exactly the guard lines removed.
func TestUnguardedInhibitIsTheOldRule(t *testing.T) {
	guarded, err := os.ReadFile("golden/observability-stack/single.yaml")
	require.NoError(t, err)
	unguarded, err := os.ReadFile("golden/observability-stack/single-inhibit-unguarded.yaml")
	require.NoError(t, err)

	var stripped []string
	var removed int
	for _, line := range strings.Split(string(guarded), "\n") {
		if strings.HasSuffix(line, `=~ ".+"`) && strings.HasPrefix(strings.TrimSpace(line), "- k8s_") {
			removed++
			continue
		}
		stripped = append(stripped, line)
	}
	require.Equal(t, 2, removed, "single.yaml should carry exactly two guard matchers")
	assert.Equal(t, strings.Join(stripped, "\n"), string(unguarded))
}

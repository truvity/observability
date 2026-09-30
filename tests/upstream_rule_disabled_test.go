// An estate whose nodes are provisioned on demand turns upstream's
// KubeCPUOvercommit and KubeMemoryOvercommit off (requests above allocatable
// is that estate's normal state). The interface is the vendored sync job's
// per-rule `enabled: false`; this pins that it reaches the sync job's
// rendered config, for those two names only.
package tests

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestUpstreamRuleCanBeDisabledByName(t *testing.T) {
	found := false
	for _, doc := range splitDocs(t, "golden/observability-stack/upstream-rule-disabled.yaml") {
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
		var cfg struct {
			Rules struct {
				Rules map[string]struct {
					Enabled *bool `yaml:"enabled"`
				} `yaml:"rules"`
			} `yaml:"rules"`
		}
		require.NoError(t, yaml.Unmarshal([]byte(cm.Data.ConfigYAML), &cfg))
		found = true
		for _, name := range []string{"KubeCPUOvercommit", "KubeMemoryOvercommit"} {
			r, ok := cfg.Rules.Rules[name]
			require.Truef(t, ok, "%s is not in the sync job's per-rule config", name)
			require.NotNil(t, r.Enabled, name)
			assert.Falsef(t, *r.Enabled, "%s must be disabled", name)
		}
		for _, name := range []string{"KubeCPUQuotaOvercommit", "KubeMemoryQuotaOvercommit"} {
			_, ok := cfg.Rules.Rules[name]
			assert.Falsef(t, ok, "%s is a different alert (namespace quotas) and must stay", name)
		}
	}
	require.True(t, found, "no sync-job config in the golden")
}

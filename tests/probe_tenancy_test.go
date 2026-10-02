// Every VMProbe the emitters chart renders must stamp the cluster's
// tenancy labels itself. The operator applies the agent's default scrape
// class to no VMProbe, so a probe that relied on it produced series with no
// `k8s_cluster_name`: stored, scraped healthy, and invisible to every
// reader scoped by that label.
package tests

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

type vmProbeDoc struct {
	Kind string `yaml:"kind"`
	Spec struct {
		Targets struct {
			StaticConfig struct {
				RelabelingConfigs []struct {
					TargetLabel string `yaml:"target_label"`
				} `yaml:"relabelingConfigs"`
			} `yaml:"staticConfig"`
		} `yaml:"targets"`
	} `yaml:"spec"`
}

func TestVMProbesStampTenancyLabels(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-emitters/*.yaml")
	require.NoError(t, err)

	seen := 0
	for _, g := range goldens {
		for _, doc := range splitDocs(t, g) {
			var p vmProbeDoc
			if err := yaml.Unmarshal(doc, &p); err != nil || p.Kind != "VMProbe" {
				continue
			}
			seen++
			got := map[string]bool{}
			for _, r := range p.Spec.Targets.StaticConfig.RelabelingConfigs {
				got[r.TargetLabel] = true
			}
			for _, want := range []string{"k8s_cluster_name", "deployment_environment_name", "k8s_namespace_name"} {
				require.True(t, got[want], "%s: a VMProbe does not stamp %s", g, want)
			}
		}
	}
	require.Positive(t, seen, "no golden renders a VMProbe; this test would pass vacuously")
}

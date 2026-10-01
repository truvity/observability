// kube-state-metrics reloads its custom-resource-state config in place
// (v2.8.0+), but only if the kubelet updates the file in the pod, and the
// kubelet never updates a `subPath` mount. This pins the directory mount so
// a subchart bump cannot silently break preset changes taking effect live.
package tests

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestKubeStateMetricsConfigMountHasNoSubPath(t *testing.T) {
	found := false
	for _, doc := range splitDocs(t, "golden/observability-emitters/nodeclaims-preset.yaml") {
		var o struct {
			Kind string `yaml:"kind"`
			Spec struct {
				Template struct {
					Spec struct {
						Containers []struct {
							Args         []string `yaml:"args"`
							VolumeMounts []struct {
								Name      string `yaml:"name"`
								MountPath string `yaml:"mountPath"`
								SubPath   string `yaml:"subPath"`
							} `yaml:"volumeMounts"`
						} `yaml:"containers"`
						Volumes []struct {
							Name      string `yaml:"name"`
							ConfigMap *struct {
								Name string `yaml:"name"`
							} `yaml:"configMap"`
						} `yaml:"volumes"`
					} `yaml:"spec"`
				} `yaml:"template"`
			} `yaml:"spec"`
		}
		if yaml.Unmarshal(doc, &o) != nil || o.Kind != "Deployment" {
			continue
		}
		for _, c := range o.Spec.Template.Spec.Containers {
			for _, m := range c.VolumeMounts {
				if m.Name != "customresourcestate-config" {
					continue
				}
				found = true
				assert.Empty(t, m.SubPath, "a subPath mount is never updated by the kubelet, so config reload would never fire")
				assert.Contains(t, c.Args, "--custom-resource-state-config-file="+m.MountPath+"/config.yaml")
			}
		}
		for _, v := range o.Spec.Template.Spec.Volumes {
			if v.Name == "customresourcestate-config" {
				require.NotNil(t, v.ConfigMap, "the config volume must be a ConfigMap volume")
			}
		}
	}
	require.True(t, found, "no customresourcestate-config mount in the golden")
}

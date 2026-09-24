// A StatefulSet that declares a volume it cannot write to.
//
// A dynamically provisioned volume arrives owned by root. An image that
// does not run as root cannot create anything in it, and `fsGroup` is the
// only thing that bridges the two — so a pod with a volumeClaimTemplate
// and no fsGroup fails at startup, every time, on every cluster with a
// default StorageClass:
//
//	failed to build extensions: failed to create extension "file_storage":
//	mkdir /var/lib/otelcol/queue: permission denied
//
// It renders, it lints, its golden is ordinary, the API server accepts it
// and the operator has no opinion. The only thing that disagrees is the
// container, after the volume is attached — which is why this was found by
// installing the chart rather than by any check in this repository.
//
// The rule is stated about the SHAPE rather than about this one chart: a
// pod that asks for storage is a pod that intends to write to it.
package tests

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type statefulSetDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		VolumeClaimTemplates []struct {
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
		} `yaml:"volumeClaimTemplates"`
		Template struct {
			Spec struct {
				SecurityContext struct {
					FSGroup *int `yaml:"fsGroup"`
				} `yaml:"securityContext"`
			} `yaml:"spec"`
		} `yaml:"template"`
	} `yaml:"spec"`
}

// TestEveryClaimedVolumeIsWritable walks every golden this repository
// renders and requires each StatefulSet that claims storage to say who may
// write to it.
func TestEveryClaimedVolumeIsWritable(t *testing.T) {
	t.Parallel()

	goldens, err := filepath.Glob("golden/*/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, goldens)

	var checked int

	for _, g := range goldens {
		for _, doc := range splitDocs(t, g) {
			var set statefulSetDoc
			if err := yaml.Unmarshal(doc, &set); err != nil || set.Kind != "StatefulSet" {
				continue
			}

			if len(set.Spec.VolumeClaimTemplates) == 0 {
				continue
			}

			checked++

			assert.NotNilf(t, set.Spec.Template.Spec.SecurityContext.FSGroup,
				"%s: StatefulSet %s claims a volume but its pod sets no fsGroup. "+
					"A provisioned volume arrives owned by root and the image does not run as root, "+
					"so the container cannot create anything in it and exits at startup — on every "+
					"cluster, with a message about the directory rather than about the pod",
				g, set.Metadata.Name)
		}
	}

	// Upstream subcharts render StatefulSets of their own into these
	// goldens, so this finding nothing means the goldens moved.
	assert.Positive(t, checked, "no StatefulSet with a volumeClaimTemplate was found in any golden")
	t.Logf("%d StatefulSets claiming storage checked", checked)
}

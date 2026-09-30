package tests

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// `tenancy.principals[].vmalertAPI` is opt-in and unfiltered. Two things
// must hold in the render: the principal that set it carries the two
// vmalert routes, exact, to the metrics vmalert, dropping the `/prometheus`
// prefix part so vmalert receives `/vmalert/api/v1/...`; and a principal
// that did not set it carries no vmalert route at all.
type vmalertUserDoc struct {
	Kind string `yaml:"kind"`
	Spec struct {
		Name       string `yaml:"name"`
		TargetRefs []struct {
			Static struct {
				URL string `yaml:"url"`
			} `yaml:"static"`
			Paths                  []string `yaml:"paths"`
			DropSrcPathPrefixParts *int     `yaml:"drop_src_path_prefix_parts"`
		} `yaml:"targetRefs"`
	} `yaml:"spec"`
}

func TestVmalertAPIIsOptInAndExact(t *testing.T) {
	var seen int
	for _, raw := range splitDocs(t, filepath.Join("golden", "observability-stack", "tenancy-vmalert-api.yaml")) {
		var d vmalertUserDoc
		if err := yaml.Unmarshal(raw, &d); err != nil || d.Kind != "VMUser" {
			continue
		}
		switch d.Spec.Name {
		case "example:observability-mcp:reader":
			seen++
			var found bool
			for _, r := range d.Spec.TargetRefs {
				if r.DropSrcPathPrefixParts == nil {
					continue
				}
				found = true
				assert.Equal(t, 1, *r.DropSrcPathPrefixParts)
				assert.Equal(t, []string{"/prometheus/vmalert/api/v1/alerts", "/prometheus/vmalert/api/v1/rules"}, r.Paths,
					"exactly the two reads, never a prefix")
				assert.Contains(t, r.Static.URL, "-metrics.", "the metrics vmalert, not the logs one")
			}
			assert.True(t, found, "the vmalertAPI principal has no vmalert route")
		case "example:k8s:viewer":
			seen++
			for _, r := range d.Spec.TargetRefs {
				assert.Nil(t, r.DropSrcPathPrefixParts, "a principal without vmalertAPI carries a vmalert route")
				for _, p := range r.Paths {
					assert.NotContains(t, p, "vmalert")
				}
			}
		}
	}
	require.Equal(t, 2, seen, "both principals must be in the golden; this check went blind rather than passing")
}

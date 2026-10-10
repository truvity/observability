// The grafana-operator CRD set of charts/observability-crds
// (sets.grafanaOperator), and the half of the contract that lives in
// charts/observability-cluster: that chart ships the operator and none of
// its CRDs. docs/architecture/observability-cluster.md, "CRDs".
//
// What has to hold: the set is the operator's own CRDs at the very version
// the cluster chart vendors (not "the CRDs of some release"), it is off by
// default, and no release but observability-crds can apply them.
package tests

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

const grafanaOperatorArchive = "../charts/observability-cluster/charts/grafana-operator-5.25.0.tgz"

// archiveFiles returns every regular file of a .tgz by path.
func archiveFiles(t *testing.T, path string) map[string][]byte {
	t.Helper()
	f, err := os.Open(path)
	require.NoError(t, err)
	defer func() { _ = f.Close() }()
	gz, err := gzip.NewReader(f)
	require.NoError(t, err)
	tr := tar.NewReader(gz)
	out := map[string][]byte{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		require.NoError(t, err)
		if h.Typeflag != tar.TypeReg {
			continue
		}
		b, err := io.ReadAll(tr)
		require.NoError(t, err)
		out[h.Name] = b
	}
	return out
}

type crdShape struct {
	Kind     string
	Plural   string
	Versions []string
	Storage  string
}

func shapesOf(t *testing.T, docs [][]byte) map[string]crdShape {
	t.Helper()
	out := map[string]crdShape{}
	for _, doc := range docs {
		var crd struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Spec struct {
				Group string `yaml:"group"`
				Names struct {
					Kind   string `yaml:"kind"`
					Plural string `yaml:"plural"`
				} `yaml:"names"`
				Versions []struct {
					Name    string `yaml:"name"`
					Storage bool   `yaml:"storage"`
				} `yaml:"versions"`
			} `yaml:"spec"`
		}
		if err := yaml.Unmarshal(doc, &crd); err != nil || crd.Kind != "CustomResourceDefinition" {
			continue
		}
		s := crdShape{Kind: crd.Spec.Names.Kind, Plural: crd.Spec.Names.Plural}
		for _, v := range crd.Spec.Versions {
			s.Versions = append(s.Versions, v.Name)
			if v.Storage {
				s.Storage = v.Name
			}
		}
		out[crd.Metadata.Name] = s
	}
	return out
}

func TestGrafanaOperatorSetIsTheVendoredOperatorsCRDs(t *testing.T) {
	// What the set renders.
	var rendered [][]byte
	rendered = append(rendered, splitDocs(t, "golden/observability-crds/grafana-operator-only.yaml")...)
	got := shapesOf(t, rendered)
	require.Len(t, got, 13, "the grafana-operator has thirteen kinds")

	// What the operator the cluster chart vendors ships itself (files/crds
	// is kept in the archive on purpose, so this comparison needs no network).
	var upstream [][]byte
	for name, b := range archiveFiles(t, grafanaOperatorArchive) {
		if strings.HasPrefix(name, "grafana-operator/files/crds/") && strings.HasSuffix(name, ".yaml") {
			upstream = append(upstream, b)
		}
	}
	want := shapesOf(t, upstream)
	require.NotEmpty(t, want, "the vendored archive carries no files/crds to compare against")

	assert.Equal(t, want, got,
		"sets.grafanaOperator must be the CRDs of the operator version charts/observability-cluster vendors; run hack/crds.sh")
}

func TestGrafanaOperatorSetPinMatchesTheVendoredOperator(t *testing.T) {
	cfg, err := os.ReadFile("../hack/crds/grafana-operator/crdctl.yaml")
	require.NoError(t, err)
	var c struct {
		Repo          string `yaml:"repo"`
		PinnedVersion string `yaml:"pinned_version"`
	}
	require.NoError(t, yaml.Unmarshal(cfg, &c))
	assert.Equal(t, "grafana/grafana-operator", c.Repo)

	chart, err := os.ReadFile("../charts/observability-cluster/Chart.yaml")
	require.NoError(t, err)
	var ch struct {
		Dependencies []struct {
			Name    string `yaml:"name"`
			Version string `yaml:"version"`
		} `yaml:"dependencies"`
	}
	require.NoError(t, yaml.Unmarshal(chart, &ch))
	require.Len(t, ch.Dependencies, 1)
	assert.Equal(t, "grafana-operator", ch.Dependencies[0].Name)
	assert.Equal(t, ch.Dependencies[0].Version, strings.TrimPrefix(c.PinnedVersion, "v"),
		"hack/crds/grafana-operator/crdctl.yaml and charts/observability-cluster/Chart.yaml move together: the operator and its CRDs are one release")
	assert.Contains(t, filepath.Base(grafanaOperatorArchive), ch.Dependencies[0].Version,
		"the archive this test reads is not the pinned one: update grafanaOperatorArchive")
}

func TestGrafanaOperatorSetIsOffByDefault(t *testing.T) {
	for _, g := range []string{"minimal", "prometheus-operator-only"} {
		b, err := os.ReadFile("golden/observability-crds/" + g + ".yaml")
		require.NoError(t, err)
		assert.NotContains(t, string(b), "grafana.integreatly.org",
			"%s: a default install of observability-crds must change nothing on an existing cluster", g)
	}
}

func TestGrafanaOperatorSetIsTheOnlyOwner(t *testing.T) {
	// The vendored archive has no CRDs subchart: Helm would install its
	// crds/ directory on `helm install` and Argo CD's render includes it.
	for name := range archiveFiles(t, grafanaOperatorArchive) {
		assert.False(t, regexp.MustCompile(`^grafana-operator/charts/crds/`).MatchString(name),
			"%s: the vendored operator must not ship its CRDs subchart; run hack/vendor.sh observability-cluster", name)
	}

	// And the render, with CRDs included the way `helm install` and Argo CD
	// include them, holds none.
	out, stderr, err := helmTemplate(t, "observability-cluster", "observability-cluster", "default",
		"--include-crds", "-f", filepath.Join("cases", "observability-cluster", "everything", "values.yaml"))
	require.NoError(t, err, stderr)
	assert.NotContains(t, kindsOf(objectsByKey(t, out)), "CustomResourceDefinition")
}

// The dashboards this repository redistributes are other projects' works.
// Apache-2.0 section 4 requires a copy of the licence, a notice that the
// files were modified, and the upstream attribution to travel with them.
// hack/dashboards.py renders THIRD_PARTY_NOTICES.md and LICENSES/ from
// hack/dashboards/sources.yaml; these tests hold the result to the catalog,
// and the chart's own copy (what `helm package` ships) to the root copy.
package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

const (
	chartDir = "../charts/observability-dashboards"
)

func TestEveryVendoredDashboardHasANotice(t *testing.T) {
	raw, err := os.ReadFile(dashboardsDir + "/catalog.yaml")
	require.NoError(t, err)
	var cat map[string]struct {
		Upstream string `yaml:"upstream"`
		Authored bool   `yaml:"authored"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &cat))
	require.NotEmpty(t, cat)

	srcRaw, err := os.ReadFile("../hack/dashboards/sources.yaml")
	require.NoError(t, err)
	var src struct {
		Upstreams map[string]struct {
			License   string `yaml:"license"`
			Copyright string `yaml:"copyright"`
			URL       string `yaml:"url"`
		} `yaml:"upstreams"`
	}
	require.NoError(t, yaml.Unmarshal(srcRaw, &src))

	notices, err := os.ReadFile("../THIRD_PARTY_NOTICES.md")
	require.NoError(t, err)
	text := string(notices)

	for name, e := range cat {
		if e.Authored {
			continue
		}
		require.NotEmptyf(t, e.Upstream, "catalog entry %q is neither authored here nor names an upstream: "+
			"add `upstream:` (or `authored: true`) in hack/dashboards/sources.yaml", name)
		up, ok := src.Upstreams[e.Upstream]
		require.Truef(t, ok, "%s: upstream %q is not defined in sources.yaml", name, e.Upstream)
		section := "## " + name + "\n"
		i := strings.Index(text, section)
		require.GreaterOrEqualf(t, i, 0, "THIRD_PARTY_NOTICES.md has no entry for %q: run `just dashboards`", name)
		body := text[i:]
		if j := strings.Index(body[len(section):], "\n## "); j >= 0 {
			body = body[:len(section)+j]
		}
		assert.Contains(t, body, "SPDX licence: "+up.License)
		assert.Contains(t, body, "Copyright: "+up.Copyright)
		assert.Contains(t, body, up.URL)
		assert.Contains(t, body, "Modified: rewritten to this repository's dashboard contract")
		assert.FileExistsf(t, "../LICENSES/"+up.License+".txt", "the full text of %s must be in LICENSES/", up.License)
	}
}

func TestTheChartShipsTheNoticesAndLicences(t *testing.T) {
	rootN, err := os.ReadFile("../THIRD_PARTY_NOTICES.md")
	require.NoError(t, err)
	chartN, err := os.ReadFile(chartDir + "/THIRD_PARTY_NOTICES.md")
	require.NoError(t, err, "the chart directory must carry a copy of THIRD_PARTY_NOTICES.md: run `just dashboards`")
	assert.Equal(t, string(rootN), string(chartN), "the chart's copy of THIRD_PARTY_NOTICES.md is out of sync: run `just dashboards`")

	files, err := filepath.Glob("../LICENSES/*.txt")
	require.NoError(t, err)
	require.NotEmpty(t, files)
	for _, f := range files {
		want, err := os.ReadFile(f)
		require.NoError(t, err)
		got, err := os.ReadFile(chartDir + "/LICENSES/" + filepath.Base(f))
		require.NoErrorf(t, err, "the chart directory must carry LICENSES/%s: run `just dashboards`", filepath.Base(f))
		assert.Equalf(t, string(want), string(got), "chart copy of %s is out of sync", filepath.Base(f))
	}
}

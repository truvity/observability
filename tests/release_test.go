package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The third boundary: what exists, what is checked, and what is published.
//
// A chart is named in three places — the directory it lives in, the
// Justfile list that lints and goldens it, and the release workflow's
// list that pushes it to the registry. Nothing joins them, and the way
// they come apart is not an error: `observability-emitters` was added to
// the first two and not the third, so the next tag would have published
// three charts, reported success, and left the fourth unpublished until
// somebody went looking for a version that was never there.
//
// That is the expensive direction, because a tag is spent. The version
// cannot be re-cut; the fix is a new one, and anybody who pinned the
// missing chart at the old number finds nothing.
func TestEveryChartIsLintedAndPublished(t *testing.T) {
	entries, err := os.ReadDir(filepath.Join("..", "charts"))
	require.NoError(t, err)

	var onDisk []string
	for _, e := range entries {
		if e.IsDir() {
			onDisk = append(onDisk, e.Name())
		}
	}
	require.NotEmpty(t, onDisk, "no charts found; this test would pass vacuously")
	sort.Strings(onDisk)

	assert.Equal(t, onDisk, justfileCharts(t),
		"the Justfile's chart list and charts/ disagree: a chart missing here is never linted, and its goldens never render")
	assert.Equal(t, onDisk, releaseCharts(t),
		"the release workflow's chart list and charts/ disagree: a chart missing here is never published, and the tag that should have carried it is spent")
}

// justfileCharts reads the `charts := "..."` assignment rather than
// running just, so the test says which list is wrong rather than that
// something failed.
func justfileCharts(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "Justfile"))
	require.NoError(t, err)

	m := regexp.MustCompile(`(?m)^charts\s*:=\s*"([^"]*)"`).FindSubmatch(b)
	require.NotNil(t, m, "no `charts := \"...\"` assignment in the Justfile")

	out := strings.Fields(string(m[1]))
	sort.Strings(out)
	return out
}

// releaseCharts reads the JSON array the release workflow passes to the
// shared publisher.
func releaseCharts(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", ".github", "workflows", "release.yaml"))
	require.NoError(t, err)

	m := regexp.MustCompile(`(?m)^\s*charts:\s*'(\[[^']*\])'`).FindSubmatch(b)
	require.NotNil(t, m, "no `charts: '[...]'` input in the release workflow")

	var out []string
	require.NoError(t, json.Unmarshal(m[1], &out))
	sort.Strings(out)
	return out
}

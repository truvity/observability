package tests

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// `operatorFolders` (opt-in): a reconcile Job that ensures each cluster's
// folder, its grafana-operator user (role None) and Edit on that folder only.
// The behaviour was exercised against Grafana 13.1.1 (see the PR); here the
// render must hold the properties that keep it safe.

type opDoc struct {
	Kind     string                `yaml:"kind"`
	Metadata struct{ Name string } `yaml:"metadata"`
	Data     map[string]string     `yaml:"data"`
}

func opFolderDocs(t *testing.T, golden string) []string {
	t.Helper()
	var out []string
	for _, raw := range splitDocs(t, filepath.Join("golden", "observability-grafana", golden+".yaml")) {
		out = append(out, string(raw))
	}
	return out
}

func TestOperatorFoldersAreAbsentUnlessEnabled(t *testing.T) {
	goldens, err := filepath.Glob(filepath.Join("golden", "observability-grafana", "*.yaml"))
	require.NoError(t, err)
	for _, g := range goldens {
		name := strings.TrimSuffix(filepath.Base(g), ".yaml")
		if name == "operator-folders" {
			continue
		}
		for _, d := range opFolderDocs(t, name) {
			assert.NotContains(t, d, "operator-folders", "%s: the reconcile Job is opt-in", name)
		}
	}
}

func TestOperatorFoldersRender(t *testing.T) {
	var kinds = map[string]int{}
	var script, all string
	for _, d := range opFolderDocs(t, "operator-folders") {
		if !strings.Contains(d, "operator-folders") {
			continue
		}
		all += d
		var o opDoc
		require.NoError(t, yaml.Unmarshal([]byte(d), &o))
		kinds[o.Kind]++
		if o.Kind == "ConfigMap" {
			script = o.Data["reconcile.sh"]
			assert.Equal(t, "cluster-a cluster-b", o.Data["CLUSTERS"])
			assert.Equal(t, "true", o.Data["PRUNE"])
			assert.Equal(t, ":k8s:observability:observability-cluster-grafana-operator", o.Data["SUBJECT_SUFFIX"])
			for k := range o.Data {
				assert.NotRegexp(t, `(?i)password|token`, k, "no credential in the ConfigMap")
			}
		}
	}
	assert.Equal(t, map[string]int{"ConfigMap": 1, "ServiceAccount": 1, "Job": 1, "CronJob": 1}, kinds)

	// Credentials come from the existing admin Secret only.
	assert.Contains(t, all, "secretKeyRef")
	assert.Contains(t, all, `name: "grafana-admin"`)
	assert.Contains(t, all, "argocd.argoproj.io/hook: PostSync")
	assert.Contains(t, all, `schedule: "*/30 * * * *"`)
	assert.Contains(t, all, "readOnlyRootFilesystem: true")

	// The script never prints a credential or a response body, and writes the
	// credentials only to the in-memory curl config.
	require.NotEmpty(t, script)
	for _, bad := range []string{"set -x", "echo ", "cat "} {
		assert.NotContains(t, script, bad)
	}
	for _, line := range strings.Split(script, "\n") {
		if strings.Contains(line, "log \"") {
			assert.NotRegexp(t, `\$\{?(body|out|pw|GRAFANA_ADMIN_PASSWORD)\b`, line)
		}
	}
	// Only the intended mutations: folder, user, role None, Edit, and (behind
	// PRUNE) a user delete. Never a folder delete.
	assert.Contains(t, script, `'{"role":"None"}'`)
	assert.Contains(t, script, `'{"permission":"Edit"}'`)
	assert.Contains(t, script, "/api/access-control/folders/$c/users/$uid")
	assert.NotContains(t, script, "DELETE /api/folders")
	assert.NotContains(t, script, "/api/folders/$c/permissions", "the replace-all permissions endpoint would clobber other grants")
}

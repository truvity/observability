// charts/observability-cluster, first slice (restructure R2.1;
// docs/architecture/observability-cluster.md).
//
// The chart is installed on every cluster and is off by default, so what
// has to hold is small and has to hold always: an empty values file renders
// nothing (installing the chart changes no cluster), the operator renders
// only when asked, no CustomResourceDefinition comes out of a render (the
// CRDs are observability-crds' `sets.grafanaOperator`, docs/adoption.md;
// tests/crds_grafana_operator_test.go also renders with --include-crds), and the Grafana resource
// carries a reference to its credential and never the credential.
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

func clusterChartObjects(t *testing.T, valuesCase string) map[string]string {
	t.Helper()
	args := []string{}
	if valuesCase != "" {
		// A case may list chart presets, as hack/golden.sh does.
		if b, err := os.ReadFile(filepath.Join("cases", "observability-cluster", valuesCase, "presets")); err == nil {
			for _, name := range strings.Fields(string(b)) {
				args = append(args, "-f", filepath.Join("..", "charts", "observability-cluster", "presets", name+".yaml"))
			}
		}
		args = append(args, "-f", filepath.Join("cases", "observability-cluster", valuesCase, "values.yaml"))
	}
	out, stderr, err := helmTemplate(t, "observability-cluster", "observability-cluster", "default", args...)
	require.NoError(t, err, stderr)
	return objectsByKey(t, out)
}

func kindsOf(objs map[string]string) map[string]int {
	kinds := map[string]int{}
	for k := range objs {
		kinds[strings.SplitN(k, "/", 2)[0]]++
	}
	return kinds
}

func TestClusterChartDefaultsRenderNothing(t *testing.T) {
	assert.Empty(t, clusterChartObjects(t, ""),
		"installing observability-cluster with no values must change nothing on the cluster")
}

func TestClusterChartRendersWhatItWasAskedFor(t *testing.T) {
	grafana := clusterChartObjects(t, "minimal")
	assert.Equal(t, map[string]int{"Grafana": 1, "GrafanaFolder": 1}, kindsOf(grafana),
		"the external Grafana and the folder, and no operator")

	operator := clusterChartObjects(t, "operator")
	assert.NotContains(t, kindsOf(operator), "Grafana", "the operator alone names no Grafana")
	assert.Equal(t, 1, kindsOf(operator)["Deployment"])

	bare := clusterChartObjects(t, "folder-less")
	assert.Equal(t, map[string]int{"Grafana": 1}, kindsOf(bare))

	cases := map[string]map[string]string{
		"minimal": grafana, "operator": operator, "folder-less": bare,
		"everything": clusterChartObjects(t, "everything"),
	}
	for name, objs := range cases {
		assert.NotContains(t, kindsOf(objs), "CustomResourceDefinition",
			"%s: a CRD in the render would make this release a second owner of the Grafana CRDs", name)
	}
}

func TestClusterChartGrafanaHoldsAReferenceNotACredential(t *testing.T) {
	objs := clusterChartObjects(t, "everything")
	var g, f string
	for k, v := range objs {
		switch {
		case strings.HasPrefix(k, "Grafana/"):
			g = v
		case strings.HasPrefix(k, "GrafanaFolder/"):
			f = v
		}
	}
	require.NotEmpty(t, g)
	require.NotEmpty(t, f)

	assert.Contains(t, g, "apiKey:\n      name: \"grafana-credentials\"\n      key: \"api-token\"")
	for _, forbidden := range []string{"adminUser", "adminPassword", "insecureSkipVerify"} {
		assert.NotContains(t, g, forbidden)
	}
	// The label a component's GrafanaDashboard selects on is on the Grafana
	// resource, and the folder selects the same instance.
	assert.Contains(t, g, "observability.truvity.io/grafana: shared")
	assert.Contains(t, f, "observability.truvity.io/grafana: shared")
	assert.Contains(t, f, `uid: "example_cluster"`)
	// A Kubernetes name cannot hold an underscore; the folder uid can.
	assert.Contains(t, f, "name: example-cluster")
	assert.Contains(t, g, "namespace: \"cert-store\"")
}

// The operator authenticating with an exchanged token
// (externalGrafana.auth.sluisExchange + presets/operator-sluis-exchange.yaml).
func TestClusterChartSluisExchange(t *testing.T) {
	objs := clusterChartObjects(t, "sluis-exchange-preset")

	var dep, g, script string
	for k, v := range objs {
		switch {
		case strings.HasPrefix(k, "Deployment/"):
			dep = v
		case strings.HasPrefix(k, "Grafana/"):
			g = v
		}
	}
	require.NotEmpty(t, dep)
	require.NotEmpty(t, g)

	t.Run("the Grafana resource uses the token file, not a Secret", func(t *testing.T) {
		assert.Contains(t, g, "useKubeAuth: true")
		assert.NotContains(t, g, "apiKey")
	})

	t.Run("the operator Deployment has exactly one volume at the token path", func(t *testing.T) {
		var d struct {
			Spec struct {
				Template struct {
					Spec struct {
						Containers []struct {
							Name         string `yaml:"name"`
							Command      []string
							VolumeMounts []struct {
								Name      string `yaml:"name"`
								MountPath string `yaml:"mountPath"`
								ReadOnly  bool   `yaml:"readOnly"`
							} `yaml:"volumeMounts"`
						}
						Volumes []struct {
							Name     string         `yaml:"name"`
							EmptyDir map[string]any `yaml:"emptyDir"`
							Source   map[string]any `yaml:"projected"`
						}
					}
				}
			}
		}
		require.NoError(t, yaml.Unmarshal([]byte(dep), &d))
		const path = "/var/run/secrets/grafana.com/serviceaccount"
		pod := d.Spec.Template.Spec

		names := map[string]int{}
		for _, v := range pod.Volumes {
			names[v.Name]++
		}
		for n, c := range names {
			assert.Equal(t, 1, c, "volume %s is declared %d times", n, c)
		}

		var atPath []string
		var operatorRO bool
		for _, c := range pod.Containers {
			for _, m := range c.VolumeMounts {
				if m.MountPath == path {
					atPath = append(atPath, c.Name+":"+m.Name)
					if c.Name == "grafana-operator" {
						operatorRO = m.ReadOnly
					}
				}
			}
			if c.Name == "token-exchange" {
				script = strings.Join(c.Command, "\n")
			}
		}
		assert.ElementsMatch(t, []string{"grafana-operator:kubeauth-token-volume", "token-exchange:kubeauth-token-volume"}, atPath,
			"the operator and the sidecar share the one volume at the path")
		assert.True(t, operatorRO, "the operator only reads the token")

		var token, subject map[string]any
		for _, v := range pod.Volumes {
			switch v.Name {
			case "kubeauth-token-volume":
				token = v.EmptyDir
				assert.Nil(t, v.Source, "the projected upstream volume must be replaced, not kept at the path")
			case "kubeauth-subject-token":
				subject = v.Source
			}
		}
		assert.Equal(t, "Memory", token["medium"], "the exchanged token stays in memory")
		assert.NotNil(t, subject, "the ServiceAccount token the sidecar exchanges")
	})

	t.Run("the sidecar never prints the token", func(t *testing.T) {
		require.NotEmpty(t, script)
		for _, bad := range []string{"set -x", "echo ", "cat "} {
			assert.NotContains(t, script, bad)
		}
		// no log line mentions the token or the response body
		for _, line := range strings.Split(script, "\n") {
			if strings.Contains(line, "log ") && !strings.Contains(line, "log() {") {
				assert.NotRegexp(t, `\$\{?(token|out|body)\b`, line)
			}
		}
		// the token is only ever written to the file, through an atomic rename
		assert.Contains(t, script, "mv ")
		assert.Contains(t, script, "subject_token@")
		assert.Contains(t, script, "urn:ietf:params:oauth:grant-type:token-exchange")
	})

	t.Run("the parameters are a ConfigMap, the sidecar is ready only after the first token", func(t *testing.T) {
		cm := objs["ConfigMap/default/observability-cluster-operator-token-exchange"]
		require.NotEmpty(t, cm)
		assert.Contains(t, cm, `ISSUER_URL: "https://issuer.example.org"`)
		assert.Contains(t, cm, `CLIENT_ID: "grafana-workload"`)
		assert.Contains(t, cm, `REFRESH_SECONDS: "1200"`)
		assert.Contains(t, dep, "test -s /var/run/secrets/grafana.com/serviceaccount/token")
	})
}

func TestClusterChartDeploymentIsUpstreamsWithoutTheExtensionPoints(t *testing.T) {
	// Neither extension point set: the operator Deployment keeps its single
	// projected token volume and has no sidecar.
	dep := ""
	for k, v := range clusterChartObjects(t, "operator") {
		if strings.HasPrefix(k, "Deployment/") {
			dep = v
		}
	}
	require.NotEmpty(t, dep)
	assert.Equal(t, 1, strings.Count(dep, "name: kubeauth-token-volume\n          projected:"))
	assert.NotContains(t, dep, "kubeauth-subject-token")
	assert.NotContains(t, dep, "token-exchange")
	assert.Equal(t, 1, strings.Count(dep, "\n          image: "), "only the operator container")
}

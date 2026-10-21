package tests

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// haDoc is the part of a rendered object the HA checks read.
type haDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec map[string]any `yaml:"spec"`
}

func haDocs(t *testing.T, golden string) []haDoc {
	t.Helper()

	var out []haDoc

	for _, raw := range splitDocs(t, golden) {
		var d haDoc
		require.NoError(t, yaml.Unmarshal(raw, &d))

		if d.Kind != "" {
			out = append(out, d)
		}
	}

	return out
}

// TestReplicaRendersTheStoresAndNothingElse pins `mode: replica`: the three
// stores, their policies and scrape objects, and none of what the primary
// release owns. A kind appearing here is a second proxy, a second alerter or
// a second backup, which is the failure the mode exists to rule out.
func TestReplicaRendersTheStoresAndNothingElse(t *testing.T) {
	allowed := map[string]bool{
		"NetworkPolicy": true, "Service": true, "StatefulSet": true,
		"ServiceMonitor": true, "VMSingle": true, "VMRule": true,
	}

	kinds := map[string]int{}

	for _, d := range haDocs(t, "golden/observability-stack/replica.yaml") {
		kinds[d.Kind]++

		assert.Truef(t, allowed[d.Kind], "replica renders a %s (%s)", d.Kind, d.Metadata.Name)
	}

	assert.Equal(t, 1, kinds["VMSingle"])
	assert.Equal(t, 2, kinds["StatefulSet"], "the log and trace stores")
	assert.Equal(t, 3, kinds["NetworkPolicy"], "one per store, none for the proxy, vmalert or karma")
}

// TestHAPrimaryReadsBothStoresPrimaryFirst pins the read routes: every
// reader route of every store carries this release's address first and the
// peer's second, which is what `first_available` needs to mean "primary
// unless it fails".
func TestHAPrimaryReadsBothStoresPrimaryFirst(t *testing.T) {
	var readers int

	for _, d := range haDocs(t, "golden/observability-proxy/ha.yaml") {
		if d.Kind != "VMUser" {
			continue
		}

		assert.Equal(t, "first_available", d.Spec["load_balancing_policy"], d.Metadata.Name)

		refs, _ := d.Spec["targetRefs"].([]any)
		for _, r := range refs {
			static, _ := r.(map[string]any)["static"].(map[string]any)
			urls, ok := static["urls"].([]any)

			if d.Metadata.Name == "observability-stack-writer-metrics-agent" {
				// A write is never fanned out by the proxy: the writers send to
				// both stores.
				assert.Falsef(t, ok, "%s: a writer route carries a list of backends", d.Metadata.Name)

				continue
			}

			require.Truef(t, ok, "%s: a read route has one backend", d.Metadata.Name)
			require.Len(t, urls, 2)
			first, _ := urls[0].(string)
			second, _ := urls[1].(string)
			assert.NotContains(t, first, "replica-", "the peer is not first")
			assert.Truef(t, strings.Contains(second, "replica-") || strings.Contains(second, "-peer."),
				"%s: the peer is second, got %s", d.Metadata.Name, second)

			readers++
		}
	}

	assert.Positive(t, readers)
}

// TestHAAlertersAreTwins pins the pair of vmalerts: each peer twin reads and
// writes ITS store, and carries the same rules, interval and labels as its
// original, so Alertmanager sees one alert from two evaluators.
func TestHAAlertersAreTwins(t *testing.T) {
	byName := map[string]haDoc{}

	for _, d := range haDocs(t, "golden/observability-stack/ha.yaml") {
		if d.Kind == "VMAlert" {
			byName[d.Metadata.Name] = d
		}
	}

	require.Len(t, byName, 4, "metrics and logs, each with a peer twin")

	for _, kind := range []string{"metrics", "logs"} {
		a := byName["observability-stack-"+kind]
		b := byName["observability-stack-"+kind+"-peer"]

		for _, key := range []string{"ruleSelector", "ruleNamespaceSelector", "evaluationInterval", "externalLabels", "notifiers", "extraArgs"} {
			assert.Equalf(t, a.Spec[key], b.Spec[key], "%s twin differs on %s", kind, key)
		}

		assert.NotEqual(t, a.Spec["remoteWrite"], b.Spec["remoteWrite"], "%s: the twin must keep its state in its own store", kind)
		assert.NotEqual(t, a.Spec["remoteRead"], b.Spec["remoteRead"], "%s: the twin must restore its state from its own store", kind)
		assert.NotContains(t, a.Spec, "externalLabels", "a replica label here would make Alertmanager page twice")
	}
}

// vmauthSpec renders the proxy chart with the given extra args and returns the
// VMAuth's spec.
func vmauthSpec(t *testing.T, args ...string) map[string]any {
	t.Helper()

	out, stderr, err := helmTemplate(t, "x", "observability-proxy", "obs",
		append([]string{"-f", "cases/observability-proxy/single/values.yaml"}, args...)...)
	require.NoErrorf(t, err, "%s", stderr)

	for _, raw := range strings.Split(out, "\n---\n") {
		var d haDoc
		require.NoError(t, yaml.Unmarshal([]byte(raw), &d))

		if d.Kind == "VMAuth" {
			return d.Spec
		}
	}

	require.Fail(t, "no VMAuth rendered")

	return nil
}

// TestVMAuthSpreadAndBudgetFollowTheReplicaCount pins the condition: one
// replica renders neither the zone spread nor the PodDisruptionBudget, two or
// more render both, with `ha` or without.
func TestVMAuthSpreadAndBudgetFollowTheReplicaCount(t *testing.T) {
	one := vmauthSpec(t)
	assert.NotContains(t, one, "topologySpreadConstraints")
	assert.NotContains(t, one, "podDisruptionBudget")

	for _, n := range []string{"2", "3"} {
		spec := vmauthSpec(t, "--set", "vmauth.replicaCount="+n)

		spread, _ := spec["topologySpreadConstraints"].([]any)
		require.Lenf(t, spread, 1, "replicaCount %s", n)
		c, _ := spread[0].(map[string]any)
		assert.Equal(t, "topology.kubernetes.io/zone", c["topologyKey"])
		assert.Equal(t, "ScheduleAnyway", c["whenUnsatisfiable"], "soft: a zone loss must stay schedulable")
		assert.Equal(t, map[string]any{"maxUnavailable": 1}, spec["podDisruptionBudget"])
	}
}

// TestVMAuthHARefusesFewerThanTwoReplicas keeps ha's own refusal.
func TestVMAuthHARefusesFewerThanTwoReplicas(t *testing.T) {
	_, stderr, err := helmTemplate(t, "x", "observability-proxy", "obs",
		"-f", "cases/observability-proxy/ha/values.yaml", "--set", "vmauth.replicaCount=1")
	require.Error(t, err)
	assert.Contains(t, stderr, "`vmauth.replicaCount` is 1")
}

// TestVMAuthTrafficDistributionPatchesTheDefaultService pins that the value is
// carried through serviceSpec with useAsDefault (patch, not a second Service),
// and that it renders nothing when empty.
func TestVMAuthTrafficDistributionPatchesTheDefaultService(t *testing.T) {
	assert.NotContains(t, vmauthSpec(t), "serviceSpec")

	spec := vmauthSpec(t, "--set", "vmauth.service.trafficDistribution=PreferClose")
	assert.Equal(t, map[string]any{
		"useAsDefault": true,
		"spec":         map[string]any{"trafficDistribution": "PreferClose"},
	}, spec["serviceSpec"])
}

// TestVMAuthSpreadCanBeHard pins `vmauth.topologySpread.whenUnsatisfiable`:
// DoNotSchedule reaches the zone spread, and the schema refuses anything else
// than the two Kubernetes values.
func TestVMAuthSpreadCanBeHard(t *testing.T) {
	spec := vmauthSpec(t, "--set", "vmauth.replicaCount=3",
		"--set", "vmauth.topologySpread.whenUnsatisfiable=DoNotSchedule")

	spread, _ := spec["topologySpreadConstraints"].([]any)
	require.Len(t, spread, 1)
	c, _ := spread[0].(map[string]any)
	assert.Equal(t, "DoNotSchedule", c["whenUnsatisfiable"])

	_, _, err := helmTemplate(t, "x", "observability-proxy", "obs",
		"--set", "vmauth.replicaCount=3", "--set", "vmauth.topologySpread.whenUnsatisfiable=Sometimes")
	require.Error(t, err)
}

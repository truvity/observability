// The way back from a store's non-root default is an override of the user,
// not a null.
//
// The metrics store runs as 65534 by default and relies on `fsGroup` to make
// its volume writable. The kubelet never applies `fsGroup` to a hostPath
// volume, so a store whose data root wrote, on a hostPath, has to run as root.
// The CHANGELOG once named `vmsingle.spec.securityContext: null` as the way to
// do that. It is not one: an Argo CD `valuesObject` drops the null, and Helm 4
// carries it into the subchart, where it renders a VMSingle the API server
// refuses. These tests pin both halves, so the documented override keeps
// rendering a valid object, and the null keeps being the trap the docs say it
// is (docs/reference.md, "Running a store as root").
//
// The Argo CD half cannot be expressed here: Argo CD drops the null before
// Helm runs, so what Helm renders then is the default, which every golden
// already covers.
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

const stackMinimalValues = "cases/observability-stack/minimal/values.yaml"

// rootOverride is the opt-out docs/reference.md gives, for all three stores.
const rootOverride = `victoria-metrics-k8s-stack:
  vmsingle:
    spec:
      securityContext:
        runAsNonRoot: false
        runAsUser: 0
        runAsGroup: 0
victoria-logs-single:
  server:
    podSecurityContext:
      runAsNonRoot: false
      runAsUser: 0
      runAsGroup: 0
victoria-traces-single:
  server:
    podSecurityContext:
      runAsNonRoot: false
      runAsUser: 0
      runAsGroup: 0
`

// writeValues puts a values file where helm can read it.
func writeValues(t *testing.T, body string) string {
	t.Helper()

	p := filepath.Join(t.TempDir(), "values.yaml")
	require.NoError(t, os.WriteFile(p, []byte(body), 0o600))

	return p
}

// renderStack renders observability-stack on the minimal case plus args and
// returns each document, raw.
func renderStack(t *testing.T, args ...string) [][]byte {
	t.Helper()

	out, stderr, err := helmTemplate(t, stackRelease, "observability-stack", "observability",
		append([]string{"-f", stackMinimalValues}, args...)...)
	require.NoErrorf(t, err, "%s", stderr)

	var docs [][]byte

	dec := yaml.NewDecoder(strings.NewReader(out))
	for {
		var n yaml.Node
		if err := dec.Decode(&n); err != nil {
			break
		}

		b, err := yaml.Marshal(&n)
		require.NoError(t, err)

		docs = append(docs, b)
	}

	require.NotEmpty(t, docs)

	return docs
}

// onlyDoc returns the one document of the given kind, decoded, and its raw
// bytes.
func onlyDoc(t *testing.T, docs [][]byte, kind string, match func(map[string]any) bool) (map[string]any, []byte) {
	t.Helper()

	var (
		found map[string]any
		raw   []byte
	)

	for _, b := range docs {
		var d map[string]any
		require.NoError(t, yaml.Unmarshal(b, &d))

		if d["kind"] != kind || (match != nil && !match(d)) {
			continue
		}

		require.Nilf(t, found, "more than one %s rendered", kind)

		found, raw = d, b
	}

	require.NotNilf(t, found, "no %s rendered", kind)

	return found, raw
}

// TestStoreRootOverrideRendersValidObjects pins the documented opt-out: the
// three keys reach each store, everything else of the hardening stays, and
// the VMSingle still matches the CRD this repository installs.
func TestStoreRootOverrideRendersValidObjects(t *testing.T) {
	schemas := loadSchemas(t)
	docs := renderStack(t, "-f", writeValues(t, rootOverride))

	vmsingle, raw := onlyDoc(t, docs, "VMSingle", nil)
	sc, ok := dig(vmsingle, "spec", "securityContext").(map[string]any)
	require.Truef(t, ok, "spec.securityContext is not a mapping: %#v", dig(vmsingle, "spec", "securityContext"))

	assert.Equal(t, 0, sc["runAsUser"])
	assert.Equal(t, 0, sc["runAsGroup"])
	assert.Equal(t, false, sc["runAsNonRoot"])
	// Maps merge: the rest of the default is still there.
	assert.Equal(t, "RuntimeDefault", dig(sc, "seccompProfile", "type"))
	assert.Equal(t, false, sc["allowPrivilegeEscalation"])
	assert.Equal(t, []any{"ALL"}, dig(sc, "capabilities", "drop"))
	assert.Equal(t, 65534, sc["fsGroup"], "fsGroup stays; it is inert on a hostPath and harmless elsewhere")

	key, bad, checked := checkDocTypes(raw, schemas)
	require.True(t, checked, "the VMSingle was not matched to a shipped CustomResourceDefinition")
	assert.Emptyf(t, bad, "%s does not match its CustomResourceDefinition: %s", key, strings.Join(bad, "; "))

	key, pruned, checked := checkDoc(raw, schemas)
	require.True(t, checked)
	assert.Emptyf(t, pruned, "%s: the CustomResourceDefinition has no room for %v", key, pruned)

	for _, name := range []string{"victoria-logs-single-server", "vt-single-server"} {
		sts, _ := onlyDoc(t, docs, "StatefulSet", func(d map[string]any) bool {
			return strings.HasSuffix(asString(dig(d, "metadata", "name")), name)
		})
		psc := dig(sts, "spec", "template", "spec", "securityContext")

		assert.Equalf(t, 0, dig(psc, "runAsUser"), "%s", name)
		assert.Equalf(t, 0, dig(psc, "runAsGroup"), "%s", name)
		assert.Equalf(t, false, dig(psc, "runAsNonRoot"), "%s", name)
		assert.Equalf(t, "RuntimeDefault", dig(psc, "seccompProfile", "type"), "%s", name)
	}
}

// TestStoreNullOptOutRendersARefusedVMSingle pins why the docs say "not
// null": through Helm 4 the null reaches the VMSingle, in a values file or as
// a `--set`, and the CRD types the field as an object.
func TestStoreNullOptOutRendersARefusedVMSingle(t *testing.T) {
	schemas := loadSchemas(t)

	for name, args := range map[string][]string{
		"values file": {"-f", writeValues(t, "victoria-metrics-k8s-stack:\n  vmsingle:\n    spec:\n      securityContext: null\n")},
		"--set":       {"--set", "victoria-metrics-k8s-stack.vmsingle.spec.securityContext=null"},
	} {
		t.Run(name, func(t *testing.T) {
			vmsingle, raw := onlyDoc(t, renderStack(t, args...), "VMSingle", nil)

			spec, ok := vmsingle["spec"].(map[string]any)
			require.True(t, ok)

			sc, present := spec["securityContext"]
			require.True(t, present, "the null was dropped before the render; if Helm now does that, the docs' "+
				"\"not null\" explanation is out of date")
			assert.Nil(t, sc, "spec.securityContext rendered a value; if Helm now does that, the docs' "+
				"\"not null\" explanation is out of date")

			_, bad, checked := checkDocTypes(raw, schemas)
			require.True(t, checked)
			assert.NotEmpty(t, bad, "the type check let `securityContext: null` through; the API server refuses it")
		})
	}
}

// TestStoreNullPodSecurityContextFailsTheRender pins the subcharts' half:
// they read `podSecurityContext.enabled`, so a null fails the render outright.
func TestStoreNullPodSecurityContextFailsTheRender(t *testing.T) {
	for _, store := range []string{"victoria-logs-single", "victoria-traces-single"} {
		_, stderr, err := helmTemplate(t, stackRelease, "observability-stack", "observability",
			"-f", stackMinimalValues, "--set", store+".server.podSecurityContext=null")
		require.Errorf(t, err, "%s: a null podSecurityContext rendered", store)
		assert.Containsf(t, stderr, "podSecurityContext.enabled", "%s: failed for another reason", store)
	}
}

package tests

import (
	"net/url"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func oncallGoldens(t *testing.T) []string {
	t.Helper()
	g, err := filepath.Glob("golden/oncall/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, g)
	return g
}

// Every pod the oncall chart renders meets Pod Security "restricted", and
// runs as its own ServiceAccount with no API token mounted.
func TestOncallPodsAreRestrictedWithTheirOwnServiceAccount(t *testing.T) {
	var pods int
	for _, g := range oncallGoldens(t) {
		docs := renderedDocs(t, g)
		accounts := map[string]bool{}
		for _, d := range docs {
			if d["kind"] == "ServiceAccount" {
				accounts[asString(dig(d, "metadata", "name"))] = true
				assert.Equalf(t, false, dig(d, "automountServiceAccountToken"), "%s: ServiceAccount mounts a token", g)
			}
		}
		used := map[string]string{}
		for _, d := range docs {
			if d["kind"] != "Deployment" {
				continue
			}
			pods++
			name := asString(dig(d, "metadata", "name"))
			spec := dig(d, "spec", "template", "spec")
			assertRestrictedPod(t, g+": "+name, spec)
			sa := asString(dig(spec, "serviceAccountName"))
			assert.Truef(t, accounts[sa], "%s: %s runs as %q, which the chart does not render", g, name, sa)
			assert.Equalf(t, "", used[sa], "%s: %s and %s share ServiceAccount %q", g, name, used[sa], sa)
			used[sa] = name
			assert.Equalf(t, false, dig(spec, "automountServiceAccountToken"), "%s: %s mounts a token", g, name)
		}
	}
	require.Positive(t, pods)
}

// No secret value is ever in the render: the database URL carries no
// password, and every credential is a secretKeyRef or a Secret volume.
func TestOncallRendersNoSecretValue(t *testing.T) {
	for _, g := range oncallGoldens(t) {
		for _, d := range renderedDocs(t, g) {
			assert.NotEqualf(t, "Secret", d["kind"], "%s renders a Secret", g)
			if d["kind"] != "Deployment" {
				continue
			}
			for _, c := range containersOf(dig(d, "spec", "template", "spec")) {
				env, _ := c["env"].([]any)
				for _, e := range env {
					m := e.(map[string]any)
					name := asString(m["name"])
					switch name {
					case "PGPASSWORD", "GOALERT_DATA_ENCRYPTION_KEY", "GOALERT_DATA":
						assert.NotNilf(t, dig(m, "valueFrom", "secretKeyRef"), "%s: %s is not from a Secret", g, name)
						assert.Nilf(t, m["value"], "%s: %s has a literal value", g, name)
					case "GOALERT_DB_URL":
						u, err := url.Parse(asString(m["value"]))
						require.NoError(t, err)
						_, hasPassword := u.User.Password()
						assert.Falsef(t, hasPassword, "%s: the database URL carries a password", g)
						assert.Equalf(t, "goalert", u.User.Username(), "%s", g)
					}
				}
			}
		}
	}
}

// The default-deny selects every pod of the release, and the relay admits
// only GoAlert on its webhook port.
func TestOncallNetworkPolicies(t *testing.T) {
	docs := renderedDocs(t, "golden/oncall/minimal.yaml")
	deny := findDoc(t, docs, "NetworkPolicy", func(d map[string]any) bool {
		return strings.HasSuffix(asString(dig(d, "metadata", "name")), "-default-deny")
	})
	assert.Nil(t, dig(deny, "spec", "ingress"))
	assert.Nil(t, dig(deny, "spec", "egress"))
	assert.ElementsMatch(t, []any{"Ingress", "Egress"}, dig(deny, "spec", "policyTypes"))
	sel := dig(deny, "spec", "podSelector", "matchLabels").(map[string]any)
	assert.NotContains(t, sel, "app.kubernetes.io/component", "the default-deny must select both workloads")

	relay := findDoc(t, docs, "NetworkPolicy", func(d map[string]any) bool {
		return asString(dig(d, "metadata", "name")) == "oncall-telegram-relay"
	})
	ingress := dig(relay, "spec", "ingress").([]any)
	from := dig(ingress[0], "from").([]any)
	require.Len(t, from, 1)
	assert.Equal(t, "goalert", dig(from[0], "podSelector", "matchLabels", "app.kubernetes.io/component"))
	assert.EqualValues(t, 8080, dig(ingress[0], "ports").([]any)[0].(map[string]any)["port"])

	// Without the relay, GoAlert has no egress rule naming it.
	for _, d := range renderedDocs(t, "golden/oncall/no-relay.yaml") {
		assert.NotContains(t, asString(dig(d, "metadata", "name")), "telegram-relay", "no-relay renders a relay object")
	}
}

// The metrics ports are never in a Service, so nothing routed to one can
// read /metrics.
func TestOncallServicesExposeNoMetricsPort(t *testing.T) {
	for _, g := range oncallGoldens(t) {
		for _, d := range renderedDocs(t, g) {
			if d["kind"] != "Service" {
				continue
			}
			for _, p := range dig(d, "spec", "ports").([]any) {
				assert.Equalf(t, "http", dig(p, "name"), "%s: Service %s exposes %v", g, dig(d, "metadata", "name"), dig(p, "name"))
			}
		}
	}
}

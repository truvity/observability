package tests

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// `workloadAuth` is opt-in and cannot be used to escalate. Three things must
// hold in the render: off, nothing about it appears anywhere; on, the
// identity it creates has a role that is a constant, never read from the
// token, with no way to become a server admin; and the gate is an `aud` the
// human sign-in client does not have.

type grafanaConfigMap struct {
	Kind string            `yaml:"kind"`
	Data map[string]string `yaml:"data"`
}

func grafanaIni(t *testing.T, golden string) string {
	t.Helper()
	for _, raw := range splitDocs(t, filepath.Join("golden", "observability-grafana", golden+".yaml")) {
		var d grafanaConfigMap
		if err := yaml.Unmarshal(raw, &d); err != nil || d.Kind != "ConfigMap" {
			continue
		}
		if ini, ok := d.Data["grafana.ini"]; ok {
			return ini
		}
	}
	require.FailNow(t, "no grafana.ini in golden "+golden)
	return ""
}

// section returns the keys of every `[name]` block, merged, as Grafana's ini
// reader merges a repeated section.
func iniSection(ini, name string) (map[string]string, int) {
	out := map[string]string{}
	var in bool
	var blocks int
	for _, line := range strings.Split(ini, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			in = line == "["+name+"]"
			if in {
				blocks++
			}
			continue
		}
		if in {
			if k, v, ok := strings.Cut(line, " = "); ok {
				out[k] = v
			}
		}
	}
	return out, blocks
}

func TestGrafanaWorkloadAuthIsAbsentUnlessEnabled(t *testing.T) {
	goldens, err := filepath.Glob(filepath.Join("golden", "observability-grafana", "*.yaml"))
	require.NoError(t, err)
	var off int
	for _, g := range goldens {
		name := strings.TrimSuffix(filepath.Base(g), ".yaml")
		if strings.HasPrefix(name, "workload-auth") || name == "operator-folders" {
			continue
		}
		off++
		assert.NotContains(t, grafanaIni(t, name), "auth.jwt", "%s: the workload path is off by default and must leave no trace", name)
	}
	assert.Positive(t, off)
}

// The role expression names no claim but `sub`, yields only Viewer or None,
// and is strict: nothing in a token can ask for more.
func assertWorkloadRoleCannotEscalate(t *testing.T, expr string) {
	t.Helper()
	rest := strings.NewReplacer("'Viewer'", "", "'None'", "").Replace(expr)
	assert.NotRegexp(t, regexp.MustCompile(`(?i)groups|role|admin|editor`), rest,
		"the role expression must not read a claim that could ask for more")
	for _, lit := range regexp.MustCompile(`'([^']*)'`).FindAllStringSubmatch(expr, -1) {
		if strings.HasPrefix(lit[1], ":") {
			continue // an ends_with suffix, not a role
		}
		assert.Contains(t, []string{"Viewer", "None"}, lit[1], "the only roles a workload can get")
	}
}

// By default the per-cluster grafana-operator's subject is role None (it may
// write only its own folder: a folder permission Edit); everyone else stays a
// Viewer.
func TestGrafanaWorkloadAuthLowersTheOperatorToNone(t *testing.T) {
	jwt, _ := iniSection(grafanaIni(t, "workload-auth"), "auth.jwt")
	assert.Equal(t,
		"ends_with(sub, ':k8s:observability:observability-cluster-grafana-operator') && 'None' || 'Viewer'",
		jwt["role_attribute_path"])
	assertWorkloadRoleCannotEscalate(t, jwt["role_attribute_path"])
	assert.Equal(t, "true", jwt["role_attribute_strict"])

	two, _ := iniSection(grafanaIni(t, "workload-auth-two-suffixes"), "auth.jwt")
	assert.Equal(t,
		"ends_with(sub, ':k8s:team-a:writer') && 'None' || ends_with(sub, ':k8s:team-b:writer') && 'None' || 'Viewer'",
		two["role_attribute_path"])
	assertWorkloadRoleCannotEscalate(t, two["role_attribute_path"])
}

func TestGrafanaWorkloadAuthViewersOnlyWhenTheListIsEmpty(t *testing.T) {
	jwt, _ := iniSection(grafanaIni(t, "workload-auth-viewers-only"), "auth.jwt")
	assert.Equal(t, "sub && 'Viewer'", jwt["role_attribute_path"], "the constant of earlier releases")
}

func TestGrafanaWorkloadAuthIsAFixedViewer(t *testing.T) {
	ini := grafanaIni(t, "workload-auth-viewers-only")

	jwt, blocks := iniSection(ini, "auth.jwt")
	assert.Equal(t, 1, blocks, "exactly one [auth.jwt] block")
	assert.Equal(t, "true", jwt["enabled"])
	assert.Equal(t, "Authorization", jwt["header_name"], "the standard header: Grafana strips `Bearer `")
	assert.Equal(t, "https://issuer.example.org/keys", jwt["jwk_set_url"])
	assert.JSONEq(t, `{"iss":"https://issuer.example.org","aud":"grafana-workload"}`, jwt["expect_claims"])

	// The role is a constant expression: it names no claim but `sub`'s
	// presence, yields exactly Viewer, and is strict.
	assert.Equal(t, "sub && 'Viewer'", jwt["role_attribute_path"])
	assertWorkloadRoleCannotEscalate(t, jwt["role_attribute_path"])
	assert.Equal(t, "true", jwt["role_attribute_strict"])
	assert.Equal(t, "false", jwt["allow_assign_grafana_admin"], "the server-admin flag is never granted")
	assert.Equal(t, "false", jwt["skip_org_role_sync"], "the role is asserted on every request, not once")
	assert.Equal(t, "true", jwt["auto_sign_up"])
	assert.Equal(t, "false", jwt["url_login"], "no ?auth_token= login")
	assert.Equal(t, "false", jwt["enable_login_token"], "no session cookie: every request carries its own token")
	assert.Equal(t, "join(':', ['workload', sub])", jwt["username_attribute_path"], "a workload login is prefixed, so it cannot be linked to a person's")
	assert.NotContains(t, jwt, "email_claim")
	assert.NotContains(t, jwt, "email_attribute_path")
	assert.NotContains(t, jwt, "groups_attribute_path")
	assert.NotContains(t, jwt, "org_mapping")

	// The human sign-in is untouched, and the `[auth]` keys that sort after
	// the injected block are still in `[auth]`.
	auth, blocks := iniSection(ini, "auth")
	assert.Equal(t, 2, blocks, "[auth] is re-opened after [auth.jwt]")
	assert.Equal(t, "true", auth["disable_login_form"])
	assert.Equal(t, "true", auth["oauth_auto_login"])
	assert.Equal(t, "24h", auth["login_maximum_lifetime_duration"])
	oauth, _ := iniSection(ini, "auth.generic_oauth")
	assert.Equal(t, "true", oauth["role_attribute_strict"])
	assert.Equal(t, "grafana", oauth["client_id"])
	assert.NotEqual(t, oauth["client_id"], "grafana-workload", "the human client's audience must differ from the workload's")
}

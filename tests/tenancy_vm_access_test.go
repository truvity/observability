package tests

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// `tenancy.vmAccessReader` (opt-in): one VMUser selected by the token's own
// `vm_access` claim. What has to hold in the render:
//
//   - off, nothing about it exists in any golden;
//   - on, it is selected by FOUR match claims (aud, iss and the two vm_access
//     lists), which is more than a principal's two, so vmauth (which tries the
//     user with more match_claims first) prefers it for a token that carries
//     the claim and leaves every other token to the group readers;
//   - it has NO default claim, so a token without the claim can never reach
//     it and an empty placeholder can never mean "no filter";
//   - its routes carry the same placeholders a principal's do.
//
// hack/vm-access-reader-proof.sh shows the behaviour on the real vmauth.

type vmUserDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Name string `yaml:"name"`
		JWT  struct {
			MatchClaims map[string]string `yaml:"matchClaims"`
			Default     map[string]any    `yaml:"defaultVMAccessClaim"`
		} `yaml:"jwt"`
		TargetRefs []struct {
			Paths     []string `yaml:"paths"`
			QueryArgs []struct {
				Name   string   `yaml:"name"`
				Values []string `yaml:"values"`
			} `yaml:"query_args"`
		} `yaml:"targetRefs"`
	} `yaml:"spec"`
}

func vmUsersOf(t *testing.T, golden string) []vmUserDoc {
	t.Helper()
	var out []vmUserDoc
	for _, raw := range splitDocs(t, filepath.Join("golden", "observability-stack", golden+".yaml")) {
		var d vmUserDoc
		if err := yaml.Unmarshal(raw, &d); err != nil || d.Kind != "VMUser" || len(d.Spec.JWT.MatchClaims) == 0 {
			continue
		}
		out = append(out, d)
	}
	return out
}

func TestVMAccessReaderIsAbsentUnlessEnabled(t *testing.T) {
	goldens, err := filepath.Glob(filepath.Join("golden", "observability-stack", "*.yaml"))
	require.NoError(t, err)
	var seen int
	for _, g := range goldens {
		name := strings.TrimSuffix(filepath.Base(g), ".yaml")
		// The cases that turn the reader on: itself, and tenancy.authz, which
		// requires it.
		if name == "tenancy-vm-access" || strings.HasPrefix(name, "tenancy-authz") {
			continue
		}
		seen++
		for _, u := range vmUsersOf(t, name) {
			assert.NotEqual(t, "vm-access", u.Spec.Name, "%s: the vm_access reader is opt-in", name)
		}
	}
	assert.Positive(t, seen)
}

func TestVMAccessReaderIsSelectedByTheClaimAndHasNoDefault(t *testing.T) {
	users := vmUsersOf(t, "tenancy-vm-access")
	var reader *vmUserDoc
	maxOther := 0
	for i, u := range users {
		if u.Spec.Name == "vm-access" {
			reader = &users[i]
			continue
		}
		if n := len(u.Spec.JWT.MatchClaims); n > maxOther {
			maxOther = n
		}
		assert.NotEmpty(t, u.Spec.JWT.Default, "the group readers keep their default claim: that is how a token without the claim is read")
	}
	require.NotNil(t, reader)

	claims := reader.Spec.JWT.MatchClaims
	assert.Equal(t, "^(example-observability-client)$", claims["aud"])
	assert.Equal(t, `^(https://issuer\.example)$`, claims["iss"])
	assert.Equal(t, ".+", claims["vm_access.metrics_extra_filters"])
	assert.Equal(t, ".+", claims["vm_access.logs_extra_stream_filters"])
	assert.Len(t, claims, 4)
	assert.Greater(t, len(claims), maxOther,
		"vmauth tries the user with MORE match_claims first: the claim reader must outrank every group reader")

	assert.Empty(t, reader.Spec.JWT.Default,
		"no default claim: a token without vm_access never reaches this user, and an empty placeholder can never mean 'no filter'")

	args := map[string]string{}
	for _, ref := range reader.Spec.TargetRefs {
		for _, a := range ref.QueryArgs {
			require.Len(t, a.Values, 1)
			args[a.Name] = a.Values[0]
		}
	}
	assert.Equal(t, "{{.MetricsExtraFilters}}", args["extra_filters"])
	assert.Equal(t, "{{.LogsExtraStreamFilters}}", args["extra_stream_filters"])
	assert.Len(t, reader.Spec.TargetRefs, 2, "metrics and logs; no trace store in this case")
}

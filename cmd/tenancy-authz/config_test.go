package main

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const validConfig = `
claimName: groups
principals:
  - group: team-a
    grants:
      - cluster: cluster-a
        namespaces: [app-a]
authz:
  upstreamIssuer: https://idp.example
  personAudiences: [grafana]
  issuerURL: http://tenancy-authz.example.svc:8080
  mintedAudience: vmauth
  signingKey:
    file: /etc/tenancy-authz/signing/privateKey
`

func TestParseConfigDefaultsAndValidates(t *testing.T) {
	cfg, err := ParseConfig([]byte(validConfig))
	require.NoError(t, err)
	assert.Equal(t, 5*time.Minute, cfg.Authz.TokenTTL)
	assert.Equal(t, 10*time.Second, cfg.Authz.Leeway)
	assert.Equal(t, "groups", cfg.ClaimName)
	require.Len(t, cfg.Principals, 1)
}

func TestParseConfigReadsDurationsAndFileKey(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
claimName: groups
principals: [{group: g, grants: [{cluster: c, allNamespaces: true}]}]
authz:
  upstreamIssuer: https://idp.example
  personAudiences: [grafana]
  issuerURL: http://a.b.svc:8080
  mintedAudience: vmauth
  tokenTTL: 90s
  leeway: 2s
  signingKey: {file: /keys/privateKey}
`))
	require.NoError(t, err)
	assert.Equal(t, 90*time.Second, cfg.Authz.TokenTTL)
	assert.Equal(t, 2*time.Second, cfg.Authz.Leeway)
}

func TestParseConfigRefusals(t *testing.T) {
	mutate := func(from, to string) string {
		out := ""
		for i := 0; i+len(from) <= len(validConfig); i++ {
			if validConfig[i:i+len(from)] == from {
				out = validConfig[:i] + to + validConfig[i+len(from):]
				break
			}
		}
		require.NotEmpty(t, out, "fixture drifted: %q", from)
		return out
	}
	cases := map[string]struct{ config, want string }{
		"unknown key":       {validConfig + "bogus: 1\n", "bogus"},
		"unknown authz key": {mutate("mintedAudience: vmauth", "mintedAudience: vmauth\n  bogus: 1"), "bogus"},
		"no principals": {
			mutate("principals:\n  - group: team-a\n    grants:\n      - cluster: cluster-a\n        namespaces: [app-a]", "principals: []"),
			"no principals",
		},
		"hostile namespace":         {mutate("[app-a]", "[\"a|.*\"]"), "namespace"},
		"no person audience":        {mutate("[grafana]", "[]"), "personAudiences"},
		"blank person audience":     {mutate("[grafana]", "[\"a b\"]"), "identifier"},
		"minted audience is person": {mutate("mintedAudience: vmauth", "mintedAudience: grafana"), "also a person audience"},
		"own issuer with path":      {mutate("svc:8080", "svc:8080/"), "trailing slash"},
		"own issuer equals idp":     {mutate("http://tenancy-authz.example.svc:8080", "https://idp.example"), "equals"},
		"idp not a URL":             {mutate("https://idp.example", "idp.example"), "not an http(s) URL"},
		"ttl too short":             {mutate("mintedAudience: vmauth", "mintedAudience: vmauth\n  tokenTTL: 1s"), "tokenTTL"},
		"ttl too long":              {mutate("mintedAudience: vmauth", "mintedAudience: vmauth\n  tokenTTL: 24h"), "tokenTTL"},
		"no key file":               {mutate("file: /etc/tenancy-authz/signing/privateKey", "file: \"\""), "signingKey.file"},
		"a key to create":           {mutate("file: /etc/tenancy-authz/signing/privateKey", "file: /k\n    create: true"), "create"},
		"groups claim is aud":       {mutate("claimName: groups", "claimName: aud"), "claimName"},
	}
	for name, c := range cases {
		_, err := ParseConfig([]byte(c.config))
		require.Error(t, err, name)
		assert.Contains(t, err.Error(), c.want, name)
	}
}

func TestParseConfigReadsTraceClustersAndRefusesABadName(t *testing.T) {
	cfg, err := ParseConfig([]byte(validConfig + "  traceClusters: [cluster-a, cluster-b]\n"))
	require.NoError(t, err)
	assert.Equal(t, []string{"cluster-a", "cluster-b"}, cfg.Authz.TraceClusters)

	cfg, err = ParseConfig([]byte(validConfig))
	require.NoError(t, err)
	assert.Empty(t, cfg.Authz.TraceClusters, "unset means nobody reads traces")

	_, err = ParseConfig([]byte(validConfig + "  traceClusters: ['cluster a']\n"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "traceClusters")
}

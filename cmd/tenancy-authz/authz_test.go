package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// verifyMinted checks a minted token the way vmauth would: against the
// service's own published key set, and reads its claims.
func verifyMinted(t *testing.T, h *harness, authorization string) (jwt.Claims, map[string]any) {
	t.Helper()
	raw := strings.TrimPrefix(authorization, "Bearer ")
	require.NotEqual(t, authorization, raw, "the header must be a Bearer token")
	tok, err := jwt.ParseSigned(raw, []jose.SignatureAlgorithm{jose.ES256})
	require.NoError(t, err)
	var set jose.JSONWebKeySet
	require.NoError(t, json.Unmarshal(h.signer.JWKS(), &set))
	require.Len(t, set.Keys, 1)
	assert.Equal(t, set.Keys[0].KeyID, tok.Headers[0].KeyID, "kid names the published key")
	var std jwt.Claims
	var all map[string]any
	require.NoError(t, tok.Claims(set.Keys[0], &std, &all))
	return std, all
}

func outcomes(h *harness) map[string]float64 {
	out := map[string]float64{}
	for _, o := range []string{outcomePassThrough, outcomeMinted, outcomeUnauthenticated, outcomeForbidden, outcomeError} {
		out[o] = counterValue(h.metrics.Checks.WithLabelValues(o))
	}
	return out
}

func TestPersonWithTwoGroupsGetsTheUnionInOneStreamFilter(t *testing.T) {
	h := newHarness(t)
	person := h.issuer.token(map[string]any{"groups": []string{"team-a", "team-b", "unrelated"}})

	rec := h.check("Bearer " + person)
	require.Equal(t, http.StatusOK, rec.Code)

	std, all := verifyMinted(t, h, rec.Header().Get("Authorization"))
	assert.Equal(t, "http://tenancy-authz.example.svc:8080", std.Issuer)
	assert.Equal(t, jwt.Audience{"vmauth"}, std.Audience)
	assert.Equal(t, "person-1", std.Subject)
	assert.NotContains(t, all, "groups", "a minted token carries the grant, not the groups")

	va, ok := all["vm_access"].(map[string]any)
	require.True(t, ok)
	assert.Equal(t, []any{`{k8s_cluster_name="cluster-a",k8s_namespace_name=~"^(app-a|app-b)$"}`}, va["metrics_extra_filters"])
	assert.Equal(t, []any{`_stream:{"k8s.cluster.name"="cluster-a","kubernetes.pod_namespace"=~"^(app-a|app-b)$"}`}, va["logs_extra_stream_filters"])
	assert.Len(t, va, 2, "only the two filter lists")

	assert.Equal(t, 1.0, outcomes(h)[outcomeMinted])
}

func TestTwoClustersJoinIntoOneLogsStringAndOneSelectorEach(t *testing.T) {
	h := newHarness(t)
	rec := h.check("Bearer " + h.issuer.token(map[string]any{"groups": []string{"team-a", "ops"}}))
	require.Equal(t, http.StatusOK, rec.Code)
	_, all := verifyMinted(t, h, rec.Header().Get("Authorization"))
	va := all["vm_access"].(map[string]any)
	assert.Equal(t, []any{
		`{k8s_cluster_name="cluster-a",k8s_namespace_name=~"^(app-a)$"}`,
		`{k8s_cluster_name="cluster-b"}`,
	}, va["metrics_extra_filters"])
	logs := va["logs_extra_stream_filters"].([]any)
	require.Len(t, logs, 1)
	assert.Equal(t, `_stream:{"k8s.cluster.name"="cluster-a","kubernetes.pod_namespace"=~"^(app-a)$" or "k8s.cluster.name"="cluster-b"}`, logs[0])
}

func TestExpiryIsTheEarlierOfUpstreamAndTTL(t *testing.T) {
	h := newHarness(t)

	long := h.issuer.token(map[string]any{"exp": time.Now().Add(2 * time.Hour).Unix()})
	rec := h.check("Bearer " + long)
	require.Equal(t, http.StatusOK, rec.Code)
	std, _ := verifyMinted(t, h, rec.Header().Get("Authorization"))
	assert.WithinDuration(t, time.Now().Add(5*time.Minute), std.Expiry.Time(), 5*time.Second, "capped by the TTL")

	upstreamExp := time.Now().Add(2 * time.Minute).Unix()
	short := h.issuer.token(map[string]any{"exp": upstreamExp})
	rec = h.check("Bearer " + short)
	require.Equal(t, http.StatusOK, rec.Code)
	std, _ = verifyMinted(t, h, rec.Header().Get("Authorization"))
	assert.Equal(t, upstreamExp, std.Expiry.Time().Unix(), "never outlives the token it came from")
}

func TestNoMatchingGroupIsForbidden(t *testing.T) {
	h := newHarness(t)
	for _, groups := range []any{[]string{"nobody"}, []string{}, nil} {
		rec := h.check("Bearer " + h.issuer.token(map[string]any{"groups": groups}))
		assert.Equal(t, http.StatusForbidden, rec.Code, "groups %v", groups)
		assert.Empty(t, rec.Header().Get("Authorization"))
	}
	assert.Equal(t, 3.0, outcomes(h)[outcomeForbidden])
}

func TestAGroupsClaimThatIsAStringIsRead(t *testing.T) {
	h := newHarness(t)
	rec := h.check("Bearer " + h.issuer.token(map[string]any{"groups": "team-a"}))
	assert.Equal(t, http.StatusOK, rec.Code)
}

func TestInvalidPersonTokensAreUnauthenticated(t *testing.T) {
	h := newHarness(t)
	forged := signWith(t, newTestKey(t), "kid-1", map[string]any{
		"iss": h.issuer.URL(), "sub": "x", "aud": "grafana", "exp": time.Now().Add(time.Hour).Unix(), "groups": []string{"team-a"},
	})
	otherKid := signWith(t, newTestKey(t), "no-such-kid", map[string]any{
		"iss": h.issuer.URL(), "sub": "x", "aud": "grafana", "exp": time.Now().Add(time.Hour).Unix(), "groups": []string{"team-a"},
	})

	cases := map[string]string{
		"wrong key, known kid":       forged,
		"unknown kid":                otherKid,
		"expired":                    h.issuer.token(map[string]any{"exp": time.Now().Add(-time.Hour).Unix()}),
		"wrong issuer":               h.issuer.token(map[string]any{"iss": "https://elsewhere.example"}),
		"no expiry":                  h.issuer.token(map[string]any{"exp": nil}),
		"no subject":                 h.issuer.token(map[string]any{"sub": nil}),
		"not yet valid":              h.issuer.token(map[string]any{"nbf": time.Now().Add(time.Hour).Unix()}),
		"person audience, mangled":   h.issuer.token(nil)[:len(h.issuer.token(nil))-4] + "AAAA",
		"audience list incl. person": signWith(t, newTestKey(t), "kid-1", map[string]any{"iss": h.issuer.URL(), "sub": "x", "aud": []string{"other", "grafana"}, "exp": time.Now().Add(time.Hour).Unix(), "groups": []string{"team-a"}}),
	}
	for name, tok := range cases {
		rec := h.check("Bearer " + tok)
		assert.Equal(t, http.StatusUnauthorized, rec.Code, name)
		assert.Empty(t, rec.Header().Get("Authorization"), name)
	}
	assert.Equal(t, float64(len(cases)), outcomes(h)[outcomeUnauthenticated])
}

func TestTokensThatAreNotAPersonsPassThroughUntouched(t *testing.T) {
	h := newHarness(t)
	workload := h.issuer.token(map[string]any{"aud": "reader", "groups": []string{"c:ns:reader"}})
	// Even a token that would not verify is not this service's to judge when
	// it is not a person's: vmauth's readers verify it for themselves.
	forgedWorkload := signWith(t, newTestKey(t), "kid-1", map[string]any{"iss": h.issuer.URL(), "sub": "w", "aud": "reader", "exp": time.Now().Add(time.Hour).Unix()})

	for name, header := range map[string]string{
		"workload audience":         "Bearer " + workload,
		"forged workload":           "Bearer " + forgedWorkload,
		"opaque static token":       "Bearer s3cr3t-static-token",
		"no header":                 "",
		"basic auth":                "Basic dXNlcjpwYXNz",
		"bearer without a token":    "Bearer ",
		"a scheme alone":            "Bearer",
		"minted-audience lookalike": "Bearer " + h.issuer.token(map[string]any{"aud": "vmauth"}),
	} {
		rec := h.check(header)
		assert.Equal(t, http.StatusOK, rec.Code, name)
		assert.Empty(t, rec.Header().Get("Authorization"), "%s: no header may be set, so Envoy keeps the client's own", name)
		assert.Empty(t, rec.Body.String(), name)
	}
	assert.Equal(t, 8.0, outcomes(h)[outcomePassThrough])
}

func TestBearerSchemeIsReadTheWayVMAuthReadsIt(t *testing.T) {
	h := newHarness(t)
	person := h.issuer.token(nil)
	for _, scheme := range []string{"Bearer", "bearer", "BEARER", "Token"} {
		rec := h.check(scheme + " " + person)
		assert.Equal(t, http.StatusOK, rec.Code, scheme)
		assert.True(t, strings.HasPrefix(rec.Header().Get("Authorization"), "Bearer "), scheme)
	}
}

func TestEveryMethodAndPathUnderTheCheckPrefixIsAnswered(t *testing.T) {
	h := newHarness(t)
	person := h.issuer.token(nil)
	mux := newMux(h.cfg, h.signer.JWKS, h.authz)
	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodPut, http.MethodDelete, http.MethodHead} {
		for _, path := range []string{"/check", "/check/", "/check/api/v1/query_range", "/check/select/logsql/query"} {
			req := httptestRequest(method, path, "Bearer "+person)
			rec := serve(mux, req)
			assert.Equal(t, http.StatusOK, rec.Code, "%s %s", method, path)
			assert.NotEmpty(t, rec.Header().Get("Authorization"), "%s %s", method, path)
		}
	}
}

func TestMintedTokensAreCachedPerSubjectGroupsAndUpstreamToken(t *testing.T) {
	h := newHarness(t)
	person := h.issuer.token(map[string]any{"groups": []string{"team-b", "team-a"}})

	first := h.check("Bearer " + person).Header().Get("Authorization")
	for i := 0; i < 20; i++ {
		assert.Equal(t, first, h.check("Bearer "+person).Header().Get("Authorization"))
	}
	assert.Equal(t, 20.0, counterValue(h.metrics.CacheHits))
	assert.Equal(t, 21.0, outcomes(h)[outcomeMinted], "a cache hit is still a minted outcome")

	// A different person, different groups, or a refreshed upstream token
	// is a different entry.
	other := h.check("Bearer " + h.issuer.token(map[string]any{"sub": "person-2", "groups": []string{"team-a", "team-b"}})).Header().Get("Authorization")
	assert.NotEqual(t, first, other)
	fewer := h.check("Bearer " + h.issuer.token(map[string]any{"groups": []string{"team-a"}})).Header().Get("Authorization")
	assert.NotEqual(t, first, fewer)
	assert.Equal(t, 20.0, counterValue(h.metrics.CacheHits))
}

func TestACachedTokenIsNotServedNearItsExpiry(t *testing.T) {
	h := newHarness(t)
	now := time.Now()
	h.authz.now = func() time.Time { return now }
	person := h.issuer.token(map[string]any{"exp": now.Add(time.Hour).Unix()})

	first := h.check("Bearer " + person).Header().Get("Authorization")
	now = now.Add(5*time.Minute - 10*time.Second) // inside the last margin
	second := h.check("Bearer " + person).Header().Get("Authorization")
	assert.NotEqual(t, first, second, "re-signed rather than served with seconds left")
}

func TestAnUnreadyVerifierIsAServiceFailureNeverAPassThrough(t *testing.T) {
	h := newHarness(t)
	fresh := NewVerifier(h.cfg.Authz.UpstreamIssuer, h.cfg.ClaimName, time.Second, nil)
	h.authz.verifier = fresh

	rec := h.check("Bearer " + h.issuer.token(nil))
	assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
	assert.Empty(t, rec.Header().Get("Authorization"))
	assert.Equal(t, 1.0, outcomes(h)[outcomeError])
}

func TestNothingLoggedCarriesAToken(t *testing.T) {
	h := newHarness(t)
	person := h.issuer.token(nil)
	bad := h.issuer.token(map[string]any{"exp": time.Now().Add(-time.Hour).Unix()})
	nobody := h.issuer.token(map[string]any{"groups": []string{"nobody"}})
	minted := h.check("Bearer " + person).Header().Get("Authorization")
	h.check("Bearer " + bad)
	h.check("Bearer " + nobody)

	logged := h.logs.String()
	assert.NotEmpty(t, logged)
	for _, secret := range []string{person, bad, nobody, strings.TrimPrefix(minted, "Bearer ")} {
		assert.NotContains(t, logged, secret)
	}
	assert.Contains(t, logged, "person-1", "the subject and the outcome are what is logged")
}

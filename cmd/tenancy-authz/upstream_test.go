package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVerifierReadsGroupsSubjectAndExpiry(t *testing.T) {
	iss := newFakeIssuer(t)
	v := NewVerifier(iss.URL(), "groups", 10*time.Second, nil)
	require.NoError(t, v.Refresh(context.Background()))
	require.True(t, v.Ready())

	exp := time.Now().Add(time.Hour).Truncate(time.Second)
	id, err := v.Verify(context.Background(), iss.token(map[string]any{"exp": exp.Unix(), "groups": []string{"a", "b"}}), []string{"grafana"})
	require.NoError(t, err)
	assert.Equal(t, "person-1", id.Subject)
	assert.Equal(t, []string{"a", "b"}, id.Groups)
	assert.True(t, exp.Equal(id.Expiry))
}

func TestVerifierGroupClaimNameIsConfigurable(t *testing.T) {
	iss := newFakeIssuer(t)
	v := NewVerifier(iss.URL(), "roles", 0, nil)
	require.NoError(t, v.Refresh(context.Background()))
	id, err := v.Verify(context.Background(), iss.token(map[string]any{"roles": []string{"x"}}), []string{"grafana"})
	require.NoError(t, err)
	assert.Equal(t, []string{"x"}, id.Groups, "groups is not read when the claim is named roles")
}

func TestVerifierRefusesWhatIsNotAnAsymmetricSignature(t *testing.T) {
	iss := newFakeIssuer(t)
	v := NewVerifier(iss.URL(), "groups", 0, nil)
	require.NoError(t, v.Refresh(context.Background()))

	b64 := func(v any) string {
		j, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(j)
	}
	body := map[string]any{"iss": iss.URL(), "sub": "x", "aud": "grafana", "exp": time.Now().Add(time.Hour).Unix()}
	for _, alg := range []string{"none", "HS256"} {
		tok := b64(map[string]any{"alg": alg, "typ": "JWT", "kid": "kid-1"}) + "." + b64(body) + "." + base64.RawURLEncoding.EncodeToString([]byte("sig"))
		_, err := v.Verify(context.Background(), tok, []string{"grafana"})
		assert.ErrorIs(t, err, errInvalidToken, alg)
	}
}

func TestVerifierPicksUpARotatedKeyOnAnUnknownKid(t *testing.T) {
	iss := newFakeIssuer(t)
	v := NewVerifier(iss.URL(), "groups", 0, nil)
	require.NoError(t, v.Refresh(context.Background()))
	now := time.Now()
	v.now = func() time.Time { return now }

	iss.rotate("kid-2")
	tok := iss.token(nil)

	// Inside the cooldown after the last fetch: no refetch, so refused.
	_, err := v.Verify(context.Background(), tok, []string{"grafana"})
	assert.ErrorIs(t, err, errInvalidToken)

	// After it: one refetch, and the new key is found.
	now = now.Add(unknownKidCooldown + time.Second)
	_, err = v.Verify(context.Background(), tok, []string{"grafana"})
	assert.NoError(t, err)
}

func TestVerifierUnknownKidsCannotHammerTheIssuer(t *testing.T) {
	iss := newFakeIssuer(t)
	v := NewVerifier(iss.URL(), "groups", 0, nil)
	require.NoError(t, v.Refresh(context.Background()))
	before := iss.hits.Load()

	other := newTestKey(t)
	for i := 0; i < 50; i++ {
		tok := signWith(t, other, "random-"+rand.Text(), map[string]any{
			"iss": iss.URL(), "sub": "x", "aud": "grafana", "exp": time.Now().Add(time.Hour).Unix(),
		})
		_, err := v.Verify(context.Background(), tok, []string{"grafana"})
		assert.ErrorIs(t, err, errInvalidToken)
	}
	assert.LessOrEqual(t, iss.hits.Load()-before, int64(1), "at most one refetch for fifty unknown kids")
}

func TestVerifierRefusesADiscoveryDocumentForAnotherIssuer(t *testing.T) {
	iss := newFakeIssuer(t)
	iss.mu.Lock()
	iss.issuerName = "https://someone-else.example"
	iss.mu.Unlock()

	v := NewVerifier(iss.URL(), "groups", 0, nil)
	err := v.Refresh(context.Background())
	assert.ErrorContains(t, err, "names issuer")
	assert.False(t, v.Ready())

	_, err = v.Verify(context.Background(), iss.token(nil), []string{"grafana"})
	assert.ErrorIs(t, err, errNoKeys, "no keys is the service's failure, not the caller's")
}

func TestVerifierSurvivesAnIssuerThatIsDownAfterItHasKeys(t *testing.T) {
	iss := newFakeIssuer(t)
	v := NewVerifier(iss.URL(), "groups", 0, nil)
	require.NoError(t, v.Refresh(context.Background()))
	tok := iss.token(nil)

	iss.server.Close()
	assert.Error(t, v.Refresh(context.Background()))
	_, err := v.Verify(context.Background(), tok, []string{"grafana"})
	assert.NoError(t, err, "cached keys keep working through an outage of the issuer")
	assert.True(t, v.Ready())
}

func TestVerifierIgnoresPrivateAndUnknownKeysInTheSet(t *testing.T) {
	mux := http.NewServeMux()
	srv := httptest.NewServer(mux)
	defer srv.Close()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": srv.URL, "jwks_uri": srv.URL + "/k"})
	})
	mux.HandleFunc("/k", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"keys":[{"kty":"oct","k":"c2VjcmV0","kid":"sym"},{"kty":"nonsense"}]}`))
	})
	v := NewVerifier(srv.URL, "groups", 0, nil)
	err := v.Refresh(context.Background())
	assert.ErrorContains(t, err, "no usable signing key")
}

func TestMuxServesDiscoveryAndKeysForVMAuth(t *testing.T) {
	h := newHarness(t)
	mux := newMux(h.cfg, h.signer.JWKS, h.authz)

	rec := serve(mux, httptest.NewRequest(http.MethodGet, "/.well-known/openid-configuration", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	var doc map[string]any
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &doc))
	// vmauth compares the document's issuer to the one it was configured
	// with, exactly, and fetches keys from jwks_uri.
	assert.Equal(t, h.cfg.Authz.IssuerURL, doc["issuer"])
	assert.Equal(t, h.cfg.Authz.IssuerURL+"/jwks", doc["jwks_uri"])

	rec = serve(mux, httptest.NewRequest(http.MethodGet, "/jwks", nil))
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, string(h.signer.JWKS()), rec.Body.String())
	assert.NotContains(t, rec.Body.String(), `"d"`, "no private material")
}

func TestReadyHandler(t *testing.T) {
	ready := false
	h := readyHandler(func() bool { return ready })
	assert.Equal(t, http.StatusServiceUnavailable, serve(h, httptest.NewRequest(http.MethodGet, "/readyz", nil)).Code)
	ready = true
	assert.Equal(t, http.StatusOK, serve(h, httptest.NewRequest(http.MethodGet, "/readyz", nil)).Code)
}

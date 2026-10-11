package main

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
	"github.com/stretchr/testify/require"

	"github.com/truvity/observability/pkg/tenancy"
)

// fakeIssuer is an identity provider: discovery, a key set, and tokens.
type fakeIssuer struct {
	t      *testing.T
	server *httptest.Server

	mu         sync.Mutex
	key        *ecdsa.PrivateKey
	kid        string
	issuerName string // what discovery claims; defaults to the server URL
	hits       atomic.Int64
}

func newFakeIssuer(t *testing.T) *fakeIssuer {
	t.Helper()
	f := &fakeIssuer{t: t}
	f.key, f.kid = newTestKey(t), "kid-1"
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		f.hits.Add(1)
		f.mu.Lock()
		defer f.mu.Unlock()
		name := f.issuerName
		if name == "" {
			name = f.server.URL
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": name, "jwks_uri": f.server.URL + "/keys"})
	})
	mux.HandleFunc("/keys", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		pub := jose.JSONWebKey{Key: &f.key.PublicKey, KeyID: f.kid, Algorithm: "ES256", Use: "sig"}
		_ = json.NewEncoder(w).Encode(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{pub}})
	})
	f.server = httptest.NewServer(mux)
	t.Cleanup(f.server.Close)
	return f
}

func newTestKey(t *testing.T) *ecdsa.PrivateKey {
	t.Helper()
	k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	return k
}

func (f *fakeIssuer) URL() string { return f.server.URL }

// rotate replaces the signing key and its kid.
func (f *fakeIssuer) rotate(kid string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.key, f.kid = newTestKey(f.t), kid
}

// sign signs claims with key under kid.
func signWith(t *testing.T, key *ecdsa.PrivateKey, kid string, claims map[string]any) string {
	t.Helper()
	s, err := jose.NewSigner(jose.SigningKey{Algorithm: jose.ES256, Key: jose.JSONWebKey{Key: key, KeyID: kid}},
		(&jose.SignerOptions{}).WithType("JWT"))
	require.NoError(t, err)
	tok, err := jwt.Signed(s).Claims(claims).Serialize()
	require.NoError(t, err)
	return tok
}

// token mints a person's token with sensible defaults, overridden by over.
func (f *fakeIssuer) token(over map[string]any) string {
	f.t.Helper()
	f.mu.Lock()
	key, kid := f.key, f.kid
	f.mu.Unlock()
	claims := map[string]any{
		"iss":    f.server.URL,
		"sub":    "person-1",
		"aud":    "grafana",
		"exp":    time.Now().Add(time.Hour).Unix(),
		"iat":    time.Now().Unix(),
		"groups": []string{"team-a"},
	}
	for k, v := range over {
		if v == nil {
			delete(claims, k)
			continue
		}
		claims[k] = v
	}
	return signWith(f.t, key, kid, claims)
}

func testConfig(upstream, own string) Config {
	return Config{
		Config: tenancy.Config{
			ClaimName: "groups",
			Principals: []tenancy.Principal{
				{Group: "team-a", Grants: []tenancy.Grant{{Cluster: "cluster-a", Namespaces: []string{"app-a"}}}},
				{Group: "team-b", Grants: []tenancy.Grant{{Cluster: "cluster-a", Namespaces: []string{"app-b"}}}},
				{Group: "ops", Grants: []tenancy.Grant{{Cluster: "cluster-b", AllNamespaces: true}}},
				{Group: "fleet", Grants: []tenancy.Grant{{Cluster: "cluster-a", AllNamespaces: true}, {Cluster: "cluster-b", AllNamespaces: true}}},
			},
		},
		Authz: AuthzConfig{
			UpstreamIssuer:  upstream,
			PersonAudiences: []string{"grafana"},
			IssuerURL:       own,
			MintedAudience:  "vmauth",
			TokenTTL:        5 * time.Minute,
			Leeway:          10 * time.Second,
			SigningKey:      SigningKeyConfig{File: "/keys/privateKey"},
			TraceClusters:   []string{"cluster-a", "cluster-b"},
		},
	}
}

// harness is an Authorizer wired to a fake issuer.
type harness struct {
	issuer   *fakeIssuer
	cfg      Config
	authz    *Authorizer
	verifier *Verifier
	signer   *Signer
	metrics  *Metrics
	logs     *bytes.Buffer
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	iss := newFakeIssuer(t)
	cfg := testConfig(iss.URL(), "http://tenancy-authz.example.svc:8080")
	signer, err := NewSigner(newTestKey(t))
	require.NoError(t, err)

	verifier := NewVerifier(cfg.Authz.UpstreamIssuer, cfg.ClaimName, cfg.Authz.Leeway, nil)
	require.NoError(t, verifier.Refresh(context.Background()))

	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewJSONHandler(logs, nil))
	metrics := NewMetrics(prometheus.NewRegistry())
	a := NewAuthorizer(cfg, verifier, signer, metrics, logger)
	return &harness{issuer: iss, cfg: cfg, authz: a, verifier: verifier, signer: signer, metrics: metrics, logs: logs}
}

// check sends an ext_authz style request carrying the bearer token.
func (h *harness) check(authorization string) *httptest.ResponseRecorder {
	return h.checkPath("/check/api/v1/query?query=up", authorization)
}

// checkPath is check for a chosen path (the original path follows /check).
func (h *harness) checkPath(target, authorization string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, target, nil)
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	rec := httptest.NewRecorder()
	h.authz.ServeHTTP(rec, req)
	return rec
}

func httptestRequest(method, path, authorization string) *http.Request {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("Authorization", authorization)
	return req
}

func serve(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// counterValue reads a counter without a test-only dependency.
func counterValue(c prometheus.Counter) float64 {
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		panic(err)
	}
	return m.GetCounter().GetValue()
}

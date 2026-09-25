// A public endpoint that posts to Alertmanager on request is a way to
// page a whole organisation from a single curl unless the signature on
// every message is checked, against a certificate fetched from nowhere
// but the provider's own signing domain. These three tests are the proof
// docs/alert-ingress.md asks for by name: a real signed message verifies,
// a tampered one does not, and a message whose certificate URL is outside
// the signing domain is refused before anything is ever fetched.
package main

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1" //nolint:gosec // test fixture signs with SignatureVersion 1, the envelope's own default.
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// signingFixture is a throwaway key pair and a self-signed certificate
// serving it over TLS, standing in for the provider's own signing
// infrastructure. It is deliberately NOT reachable under a name matching
// signingHost: httptest hands out an address like 127.0.0.1:PORT, and the
// domain pin is proven separately, against the real production predicate,
// in TestIsSigningHost.
type signingFixture struct {
	key    *rsa.PrivateKey
	server *httptest.Server
}

func newSigningFixture(t *testing.T) *signingFixture {
	t.Helper()

	key, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "alert-ingress test fixture"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
	}

	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	require.NoError(t, err)

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(certPEM)
	}))
	t.Cleanup(server.Close)

	return &signingFixture{key: key, server: server}
}

// sign fills in SignatureVersion, SigningCertURL and Signature for e, in
// that order, so the canonical string it signs is exactly what Verify
// will reconstruct.
func (f *signingFixture) sign(t *testing.T, e Envelope) Envelope {
	t.Helper()

	e.SignatureVersion = "1"
	e.SigningCertURL = f.server.URL + "/cert.pem"

	plaintext, err := canonicalString(e)
	require.NoError(t, err)

	sum := sha1.Sum([]byte(plaintext))

	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA1, sum[:])
	require.NoError(t, err)

	e.Signature = base64.StdEncoding.EncodeToString(sig)

	return e
}

// verifier returns a Verifier that trusts the fixture's own throwaway
// server as the signing domain — the one place in these tests that the
// real, pinned isSigningHost is deliberately not used, because the
// fixture's host is a random localhost port, not sns.*.amazonaws.com.
func (f *signingFixture) verifier() *Verifier {
	v := NewVerifier(f.server.Client())

	u, err := url.Parse(f.server.URL)
	if err != nil {
		panic(err) // httptest's own URL; a parse failure here is a bug in the test itself.
	}

	want := u.Hostname()
	v.allowedHost = func(h string) bool { return h == want }

	return v
}

func TestSignedMessageVerifies(t *testing.T) {
	fixture := newSigningFixture(t)
	env := fixture.sign(t, Envelope{
		Type:      "Notification",
		MessageID: "example-message-id",
		TopicArn:  "<the security-alerts topic ARN>",
		Message:   `{"detail-type":"GuardDuty Finding","detail":{"severity":8.0,"title":"example finding"}}`,
		Timestamp: "2026-01-01T00:00:00.000Z",
	})

	err := fixture.verifier().Verify(env)
	assert.NoErrorf(t, err, "a message signed with its own certificate's key must verify, not %v", err)
}

// TestTamperedMessageRejected is the check a signature scheme without a
// test is not a signature scheme at all: something has to prove that
// CHANGING the signed content is caught, or Verify could be returning nil
// unconditionally and nothing here would know.
func TestTamperedMessageRejected(t *testing.T) {
	fixture := newSigningFixture(t)
	env := fixture.sign(t, Envelope{
		Type:      "Notification",
		MessageID: "example-message-id",
		TopicArn:  "<the security-alerts topic ARN>",
		Message:   `{"detail-type":"GuardDuty Finding"}`,
		Timestamp: "2026-01-01T00:00:00.000Z",
	})

	// Signed over one Message; the request now carries another. This is
	// exactly what an attacker who can reach the public route, but not
	// the provider's private key, would have to try.
	env.Message = `{"detail-type":"RootConsoleLogin"}`

	err := fixture.verifier().Verify(env)
	assert.Errorf(t, err, "a message whose content changed after it was signed must be rejected, and was not")
}

// TestSigningCertURLOutsideDomainRejected is the third fixture the design
// page asks for, and it needs no network at all: Verify's very first
// question about the certificate is whether its host is one this binary
// was compiled to trust, and a hostile host fails that question before
// any request is made. https://attacker.example/sns.us-east-1.amazonaws.com/cert.pem
// looks, read left to right by a person, like it might be on the
// provider's domain; it is not, because what receives the TCP connection
// is attacker.example.
func TestSigningCertURLOutsideDomainRejected(t *testing.T) {
	fixture := newSigningFixture(t)
	env := fixture.sign(t, Envelope{
		Type:      "Notification",
		MessageID: "example-message-id",
		TopicArn:  "<the security-alerts topic ARN>",
		Message:   `{"detail-type":"GuardDuty Finding"}`,
		Timestamp: "2026-01-01T00:00:00.000Z",
	})
	env.SigningCertURL = "https://attacker.example/sns.us-east-1.amazonaws.com/cert.pem"

	// The PRODUCTION verifier, pinned to the real domain — not the
	// fixture's own permissive one — because this is the check that
	// matters in a running cluster.
	err := NewVerifier(nil).Verify(env)
	require.Errorf(t, err, "a certificate URL outside the signing domain must be refused, and was not")
	assert.Contains(t, err.Error(), "signing domain")
}

// TestIsSigningHost is the domain pin itself, exercised against every
// trick a hostname can play on a naive substring check: embedding the
// real domain as a subdomain of a hostile one, embedding it as a PATH on
// a hostile host, and the bare hostile host with nothing borrowed at all.
func TestIsSigningHost(t *testing.T) {
	cases := []struct {
		name string
		host string
		want bool
	}{
		{"the real signing domain", "sns.us-east-1.amazonaws.com", true},
		{"another real region", "sns.eu-central-1.amazonaws.com", true},
		{"real domain embedded as a SUBDOMAIN of a hostile one", "sns.us-east-1.amazonaws.com.attacker.example", false},
		{"a bare hostile host", "attacker.example", false},
		{"amazonaws.com without the sns label", "s3.us-east-1.amazonaws.com", false},
		{"a hostile TLD appended", "sns.us-east-1.amazonaws.com.evil", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equalf(t, c.want, isSigningHost(c.host), "isSigningHost(%q)", c.host)
		})
	}
}

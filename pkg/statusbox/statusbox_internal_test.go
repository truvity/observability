package statusbox

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	cryptorand "crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// generateTestCAPEM returns a freshly generated, self-signed CA certificate in
// PEM, so a test of parseTrustedCAs exercises the real x509.ParseCertificate
// path. ECDSA P-256 keeps generation fast; key strength is not the property
// under test.
func generateTestCAPEM(t *testing.T, commonName string) string {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(cryptorand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	require.NoError(t, err)

	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

// generateTestLeafPEM is the same as generateTestCAPEM except
// BasicConstraints.IsCA is false: a certificate parseTrustedCAs must refuse.
func generateTestLeafPEM(t *testing.T, commonName string) string {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), cryptorand.Reader)
	require.NoError(t, err)

	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(cryptorand.Reader, tmpl, tmpl, &priv.PublicKey, priv)
	require.NoError(t, err)

	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
}

func TestParseTrustedCAs(t *testing.T) {
	t.Run("a single CA certificate is accepted", func(t *testing.T) {
		require.NoError(t, parseTrustedCAs(generateTestCAPEM(t, "root-one")))
	})

	t.Run("two concatenated CA certificates are accepted", func(t *testing.T) {
		bundle := generateTestCAPEM(t, "root-one") + generateTestCAPEM(t, "root-two")
		require.NoError(t, parseTrustedCAs(bundle))
	})

	t.Run("not PEM at all is refused", func(t *testing.T) {
		err := parseTrustedCAs("not a pem bundle at all\n")
		require.ErrorContains(t, err, "no PEM CERTIFICATE block")
	})

	t.Run("a PEM block of the wrong type is refused", func(t *testing.T) {
		block := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: []byte("not really a key either")})
		require.ErrorContains(t, parseTrustedCAs(string(block)), `not CERTIFICATE`)
	})

	t.Run("a leaf certificate without the CA bit is refused", func(t *testing.T) {
		require.ErrorContains(t, parseTrustedCAs(generateTestLeafPEM(t, "leaf.example.test")), "is not a CA certificate")
	})

	t.Run("trailing data after the last PEM block is refused", func(t *testing.T) {
		err := parseTrustedCAs(generateTestCAPEM(t, "root-one") + "not part of any PEM block\n")
		require.ErrorContains(t, err, "are not themselves a PEM block")
	})
}

func TestValidateInstancesAcceptsTheFixture(t *testing.T) {
	require.NoError(t, ValidateInstances(validInstances(), validHostnames()))
}

func validInstances() []Instance {
	return []Instance{
		{Name: "example-co", Port: 8081, Public: true, Config: "endpoints: []\n"},
		{Name: "ops", Port: 8084, Public: false, Config: "external-endpoints: []\n"},
	}
}

func validHostnames() map[string]string {
	return map[string]string{"example-co": "status.example.test"}
}

// TestValidateInstancesRefusals mutates exactly one field of the accepted
// fixture per case, so a refusal that fired on the fixture unmodified would be
// a false positive in every case.
func TestValidateInstancesRefusals(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*[]Instance, map[string]string)
		wantErr string
	}{
		{"instance with no config", func(i *[]Instance, _ map[string]string) { (*i)[0].Config = "" }, `instance "example-co": Config is empty`},
		{"two instances on one port", func(i *[]Instance, _ map[string]string) { (*i)[1].Port = (*i)[0].Port }, "is also used by instance"},
		{
			"public instance with no hostname", func(_ *[]Instance, h map[string]string) { delete(h, "example-co") },
			`Public is true but Hostnames["example-co"] is empty`,
		},
		{"two private instances", func(i *[]Instance, _ map[string]string) { (*i)[0].Public = false }, "at most one private instance"},
		{"no instances", func(i *[]Instance, _ map[string]string) { *i = nil }, "no instances"},
		{"instance name is not a valid shape", func(i *[]Instance, _ map[string]string) { (*i)[0].Name = "Example.Co" }, "is not a valid name"},
		{"duplicate instance name", func(i *[]Instance, _ map[string]string) { (*i)[1].Name = (*i)[0].Name }, "appears twice"},
		{"hostname names no instance", func(_ *[]Instance, h map[string]string) { h["typo-co"] = "status.example.test" }, `Hostnames["typo-co"] names no instance`},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			instances, hostnames := validInstances(), validHostnames()
			c.mutate(&instances, hostnames)
			require.ErrorContains(t, ValidateInstances(instances, hostnames), c.wantErr)
		})
	}
}

func TestValidateSecretNames(t *testing.T) {
	require.NoError(t, ValidateSecretNames([]string{"slack"}, []string{"OIDC_CLIENT_SECRET"}))
	require.ErrorContains(t, ValidateSecretNames([]string{"push-service"}, nil), "is not a valid name")
	require.ErrorContains(t, ValidateSecretNames(nil, []string{"push-service"}), "is not a valid environment-variable name")
	require.ErrorContains(t, ValidateSecretNames(nil, []string{"TUNNEL_TOKEN"}), "is reserved by statusbox itself")
	require.ErrorContains(t, ValidateSecretNames([]string{"slack"}, []string{"ALERT_URL_SLACK"}), "collides with alert key")
}

func TestChecksumFor(t *testing.T) {
	sum := "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

	got, err := ChecksumFor("deadbeef  other\n"+sum+"  gatus_v1\n", "gatus_v1", "v1.0.0")
	require.NoError(t, err)
	require.Equal(t, sum, got)

	_, err = ChecksumFor("deadbeef  other\n", "gatus_v1", "v1.0.0")
	require.ErrorContains(t, err, "carries no entry for gatus_v1")
}

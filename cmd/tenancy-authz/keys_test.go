package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
)

func pkcs8PEM(t *testing.T, key *ecdsa.PrivateKey) []byte {
	t.Helper()
	der, err := x509.MarshalPKCS8PrivateKey(key)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})
}

// opensshPEM is the format External Secrets' SSHKey generator writes.
func opensshPEM(t *testing.T, key any) []byte {
	t.Helper()
	b, err := ssh.MarshalPrivateKey(key, "")
	require.NoError(t, err)
	return pem.EncodeToMemory(b)
}

func TestParseKeyReadsEveryFormatASecretHolds(t *testing.T) {
	key := newTestKey(t)
	sec1, err := x509.MarshalECPrivateKey(key)
	require.NoError(t, err)
	for name, in := range map[string][]byte{
		"openssh": opensshPEM(t, key),
		"pkcs8":   pkcs8PEM(t, key),
		"sec1":    pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: sec1}),
	} {
		got, err := ParseKey(in)
		require.NoError(t, err, name)
		assert.True(t, key.Equal(got), name)
	}
}

func TestParseKeyRefusals(t *testing.T) {
	_, err := ParseKey([]byte("not pem"))
	assert.Error(t, err)

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	_, err = ParseKey(opensshPEM(t, rsaKey))
	assert.ErrorContains(t, err, "not an ECDSA key")

	p384, err := ecdsa.GenerateKey(elliptic.P384(), rand.Reader)
	require.NoError(t, err)
	_, err = ParseKey(opensshPEM(t, p384))
	assert.ErrorContains(t, err, "not P-256")
}

func TestSignerKidIsTheThumbprintAndTheSetIsPublicOnly(t *testing.T) {
	key := newTestKey(t)
	s, err := NewSigner(key)
	require.NoError(t, err)
	var set jose.JSONWebKeySet
	require.NoError(t, json.Unmarshal(s.JWKS(), &set))
	require.Len(t, set.Keys, 1)
	k := set.Keys[0]
	assert.Equal(t, "ES256", k.Algorithm)
	assert.Equal(t, "sig", k.Use)
	assert.True(t, k.IsPublic())

	thumb, err := k.Thumbprint(0x05) // crypto.SHA256
	require.NoError(t, err)
	assert.Equal(t, base64.RawURLEncoding.EncodeToString(thumb), s.KeyID(), "kid is the RFC 7638 thumbprint")

	s2, err := NewSigner(key)
	require.NoError(t, err)
	assert.Equal(t, s.KeyID(), s2.KeyID(), "every replica names the same key the same way")
}

// ringHarness is a Keyring over a file with a clock the test moves.
type ringHarness struct {
	t    *testing.T
	path string
	ring *Keyring
	now  time.Time
}

func newRing(t *testing.T, first *ecdsa.PrivateKey) *ringHarness {
	t.Helper()
	h := &ringHarness{t: t, path: filepath.Join(t.TempDir(), "privateKey"), now: time.Now()}
	require.NoError(t, os.WriteFile(h.path, opensshPEM(t, first), 0o600))
	ring, err := NewKeyring(h.path, 15*time.Minute)
	require.NoError(t, err)
	ring.now = func() time.Time { return h.now }
	h.ring = ring
	return h
}

func (h *ringHarness) write(key *ecdsa.PrivateKey) {
	h.t.Helper()
	require.NoError(h.t, os.WriteFile(h.path, opensshPEM(h.t, key), 0o600))
}

func (h *ringHarness) kids() []string {
	var set jose.JSONWebKeySet
	require.NoError(h.t, json.Unmarshal(h.ring.JWKS(), &set))
	var out []string
	for _, k := range set.Keys {
		out = append(out, k.KeyID)
	}
	return out
}

func (h *ringHarness) signedBy() string {
	tok, err := h.ring.Sign(map[string]any{"sub": "x"})
	require.NoError(h.t, err)
	parsed, err := jwt.ParseSigned(tok, []jose.SignatureAlgorithm{jose.ES256})
	require.NoError(h.t, err)
	return parsed.Headers[0].KeyID
}

func TestKeyringRotationPublishesBeforeItSignsAndRetainsAfter(t *testing.T) {
	oldKey, newKey := newTestKey(t), newTestKey(t)
	oldS, _ := NewSigner(oldKey)
	newS, _ := NewSigner(newKey)
	h := newRing(t, oldKey)

	assert.Equal(t, oldS.KeyID(), h.signedBy())
	assert.Equal(t, []string{oldS.KeyID()}, h.kids())

	// The Secret changes. The new key is PUBLISHED at once so vmauth can
	// fetch it, but the old key still signs: vmauth has not fetched yet.
	h.write(newKey)
	changed, err := h.ring.Reload()
	require.NoError(t, err)
	assert.True(t, changed)
	assert.Equal(t, []string{oldS.KeyID(), newS.KeyID()}, h.kids())
	assert.Equal(t, oldS.KeyID(), h.signedBy(), "not yet: vmauth refetches on its own timer")

	changed, err = h.ring.Reload()
	require.NoError(t, err)
	assert.False(t, changed, "the same key again is not a rotation")

	// After the switch delay the new key signs, and the old one stays
	// published for the life of the tokens it signed.
	h.now = h.now.Add(h.ring.switchDelay + time.Second)
	assert.Equal(t, newS.KeyID(), h.signedBy())
	assert.Equal(t, []string{oldS.KeyID(), newS.KeyID()}, h.kids(), "the old key outlives its last token")

	h.now = h.now.Add(16 * time.Minute)
	assert.Equal(t, []string{newS.KeyID()}, h.kids(), "and is dropped once retention has passed")
	assert.Equal(t, newS.KeyID(), h.signedBy())
}

func TestKeyringKeepsItsKeysWhenTheFileGoesBad(t *testing.T) {
	key := newTestKey(t)
	h := newRing(t, key)
	want := h.signedBy()

	require.NoError(t, os.WriteFile(h.path, []byte("garbage"), 0o600))
	_, err := h.ring.Reload()
	assert.Error(t, err)
	assert.Equal(t, want, h.signedBy(), "a Secret mid-update must not stop the service signing")

	require.NoError(t, os.Remove(h.path))
	_, err = h.ring.Reload()
	assert.Error(t, err)
	assert.Equal(t, want, h.signedBy())
}

func TestKeyringRefusesToStartWithoutAKey(t *testing.T) {
	_, err := NewKeyring(filepath.Join(t.TempDir(), "missing"), time.Minute)
	assert.Error(t, err)
}

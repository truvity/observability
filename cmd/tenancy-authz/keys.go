package main

import (
	"context"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
	"golang.org/x/crypto/ssh"
)

// Signer mints tokens with the service's ES256 key.
type Signer struct {
	signer jose.Signer
	kid    string
	jwks   []byte
}

// ParseKey reads a P-256 private key in any form a Secret is likely to hold
// it in: the OpenSSH format External Secrets' SSHKey generator writes, or
// PKCS#8 / SEC1 PEM. Anything but a P-256 ECDSA key is refused: the tokens
// are ES256.
func ParseKey(b []byte) (*ecdsa.PrivateKey, error) {
	raw, err := ssh.ParseRawPrivateKey(b)
	if err != nil {
		return nil, fmt.Errorf("parsing the signing key: %w", err)
	}
	var key *ecdsa.PrivateKey
	switch k := raw.(type) {
	case *ecdsa.PrivateKey:
		key = k
	case ecdsa.PrivateKey:
		key = &k
	default:
		return nil, fmt.Errorf("the signing key is %T, not an ECDSA key", raw)
	}
	if key.Curve != elliptic.P256() {
		return nil, fmt.Errorf("the signing key is on curve %s, not P-256 (ES256); set the generator's keySize to 256", key.Curve.Params().Name)
	}
	return key, nil
}

// NewSigner builds the signer and the key set it publishes. The kid is the
// RFC 7638 thumbprint of the public key, so every replica holding the same
// key names it the same way without agreeing on anything.
func NewSigner(key *ecdsa.PrivateKey) (*Signer, error) {
	pub := jose.JSONWebKey{Key: &key.PublicKey, Algorithm: string(jose.ES256), Use: "sig"}
	thumb, err := pub.Thumbprint(crypto.SHA256)
	if err != nil {
		return nil, err
	}
	kid := base64.RawURLEncoding.EncodeToString(thumb)
	pub.KeyID = kid

	signer, err := jose.NewSigner(
		jose.SigningKey{Algorithm: jose.ES256, Key: jose.JSONWebKey{Key: key, KeyID: kid}},
		(&jose.SignerOptions{}).WithType("JWT"),
	)
	if err != nil {
		return nil, err
	}
	jwks, err := json.Marshal(jose.JSONWebKeySet{Keys: []jose.JSONWebKey{pub}})
	if err != nil {
		return nil, err
	}
	return &Signer{signer: signer, kid: kid, jwks: jwks}, nil
}

// KeyID is the kid of the signing key.
func (s *Signer) KeyID() string { return s.kid }

// JWKS is the public key set, as JSON.
func (s *Signer) JWKS() []byte { return s.jwks }

// Sign serialises the claims objects, merged, as one signed token.
func (s *Signer) Sign(claims ...any) (string, error) {
	b := jwt.Signed(s.signer)
	for _, c := range claims {
		b = b.Claims(c)
	}
	return b.Serialize()
}

// Keyring holds the signing keys and rotates them without failing a token.
//
// The key is a file (a mounted Secret). The keyring re-reads it on a timer;
// when the key in it has changed, the new key is PUBLISHED in the key set at
// once but not yet SIGNED with: vmauth fetches the set only on its own
// five-minute timer and never on an unknown `kid`, so a token signed with a
// key it has not seen yet would fail. After switchDelay the new key signs.
// The old key stays published for retention after that, so a token minted
// just before the switch still verifies for its whole life.
type Keyring struct {
	path        string
	now         func() time.Time
	switchDelay time.Duration
	retention   time.Duration

	mu      sync.Mutex
	entries []*ringEntry // oldest first
}

type ringEntry struct {
	signer   *Signer
	activeAt time.Time
}

// Rotation timing. switchDelay exceeds vmauth's refresh interval (5 minutes
// plus jitter) and the time a mounted Secret takes to reach every replica;
// retention is the token lifetime bound plus a margin, set by the caller.
const (
	defaultSwitchDelay = 7 * time.Minute
	reloadInterval     = 15 * time.Second
)

// NewKeyring reads the key at path. retention is how long a superseded key
// stays published after its successor starts signing.
func NewKeyring(path string, retention time.Duration) (*Keyring, error) {
	k := &Keyring{path: path, now: time.Now, switchDelay: defaultSwitchDelay, retention: retention}
	if _, err := k.Reload(); err != nil {
		return nil, err
	}
	return k, nil
}

// Reload re-reads the file and reports whether the key changed.
func (k *Keyring) Reload() (bool, error) {
	b, err := os.ReadFile(k.path)
	if err != nil {
		return false, fmt.Errorf("reading the signing key: %w", err)
	}
	key, err := ParseKey(b)
	if err != nil {
		return false, err
	}
	s, err := NewSigner(key)
	if err != nil {
		return false, err
	}

	k.mu.Lock()
	defer k.mu.Unlock()
	now := k.now()
	if n := len(k.entries); n > 0 && k.entries[n-1].signer.kid == s.kid {
		k.pruneLocked(now)
		return false, nil
	}
	activeAt := now.Add(k.switchDelay)
	if len(k.entries) == 0 {
		activeAt = now // the first key signs at once: there is nothing to wait for
	}
	k.entries = append(k.entries, &ringEntry{signer: s, activeAt: activeAt})
	k.pruneLocked(now)
	return true, nil
}

// Run reloads on a timer until ctx ends, calling onChange with the new key
// id when the key changed and onError when it could not be read (the keyring
// then keeps the keys it has).
func (k *Keyring) Run(ctx context.Context, onChange func(kid string), onError func(error)) {
	t := time.NewTicker(reloadInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			changed, err := k.Reload()
			switch {
			case err != nil:
				onError(err)
			case changed:
				onChange(k.newest())
			}
		}
	}
}

func (k *Keyring) newest() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.entries[len(k.entries)-1].signer.kid
}

// activeLocked is the index of the entry that signs now: the newest whose
// activation time has come.
func (k *Keyring) activeLocked(now time.Time) int {
	for i := len(k.entries) - 1; i >= 0; i-- {
		if !now.Before(k.entries[i].activeAt) {
			return i
		}
	}
	return 0
}

func (k *Keyring) pruneLocked(now time.Time) {
	active := k.activeLocked(now)
	keep := 0
	for i := range k.entries {
		// Superseded: dropped once its successor has signed for `retention`.
		if i < active && !now.Before(k.entries[i+1].activeAt.Add(k.retention)) {
			keep = i + 1
		}
	}
	k.entries = k.entries[keep:]
}

// Sign signs with the key that is active now.
func (k *Keyring) Sign(claims ...any) (string, error) {
	k.mu.Lock()
	now := k.now()
	k.pruneLocked(now)
	s := k.entries[k.activeLocked(now)].signer
	k.mu.Unlock()
	return s.Sign(claims...)
}

// JWKS is the key set: every key that signs, will sign, or signed recently
// enough that its tokens are still alive.
func (k *Keyring) JWKS() []byte {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.pruneLocked(k.now())
	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	for _, e := range k.entries {
		var one struct {
			Keys []json.RawMessage `json:"keys"`
		}
		_ = json.Unmarshal(e.signer.jwks, &one)
		set.Keys = append(set.Keys, one.Keys...)
	}
	b, _ := json.Marshal(set)
	return b
}

// KeyID is the kid of the key that signs now.
func (k *Keyring) KeyID() string {
	k.mu.Lock()
	defer k.mu.Unlock()
	return k.entries[k.activeLocked(k.now())].signer.kid
}

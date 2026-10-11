package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-jose/go-jose/v4"
	"github.com/go-jose/go-jose/v4/jwt"
)

// The asymmetric algorithms a person's token may be signed with. HMAC is
// absent on purpose: a symmetric algorithm checked against a public key is
// the classic way to forge a token, and "none" is not a signature at all.
var personAlgorithms = []jose.SignatureAlgorithm{
	jose.RS256, jose.RS384, jose.RS512,
	jose.PS256, jose.PS384, jose.PS512,
	jose.ES256, jose.ES384, jose.ES512,
	jose.EdDSA,
}

const (
	maxDocumentBytes   = 1 << 20
	refreshInterval    = 5 * time.Minute
	retryInterval      = 5 * time.Second
	unknownKidCooldown = 10 * time.Second
)

// errNoKeys means the upstream key set has never been fetched, so nothing
// can be said about any token. It is the service's failure, not the
// caller's.
var errNoKeys = errors.New("the upstream key set has not been fetched yet")

// errInvalidToken wraps every reason a token is refused: unlike errNoKeys,
// these are the caller's.
var errInvalidToken = errors.New("invalid token")

// Identity is what a verified person's token says.
type Identity struct {
	Subject string
	Groups  []string
	Expiry  time.Time
}

// Verifier checks a person's token against the upstream issuer: OIDC
// discovery, then the key set it points to, cached.
//
// The key set is refreshed on a timer, and once on a `kid` it does not
// know — that is how a rotation at the issuer shows up between ticks. The
// refresh on a miss is rate limited, so a client sending tokens with random
// kids cannot turn this service into a source of load on the issuer.
type Verifier struct {
	issuer     string
	groupClaim string
	leeway     time.Duration
	client     *http.Client
	now        func() time.Time
	onRefresh  func(ok bool)

	mu          sync.RWMutex
	keys        []jose.JSONWebKey
	lastAttempt time.Time

	refreshMu sync.Mutex
	ready     atomic.Bool
}

// NewVerifier builds a Verifier. client may be nil.
func NewVerifier(issuer, groupClaim string, leeway time.Duration, client *http.Client) *Verifier {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return &Verifier{
		issuer:     issuer,
		groupClaim: groupClaim,
		leeway:     leeway,
		client:     client,
		now:        time.Now,
		onRefresh:  func(bool) {},
	}
}

// Ready reports whether the key set has been fetched at least once.
func (v *Verifier) Ready() bool { return v.ready.Load() }

// Run fetches the key set, retrying until it succeeds, and then keeps it
// fresh until ctx ends.
func (v *Verifier) Run(ctx context.Context, logger interface {
	Warn(msg string, args ...any)
	Info(msg string, args ...any)
}) {
	wait := time.Duration(0)
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if err := v.Refresh(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			logger.Warn("refreshing the upstream key set", "error", err.Error())
			wait = retryInterval
			continue
		}
		wait = refreshInterval
	}
}

// Refresh fetches the discovery document and the key set it points to.
func (v *Verifier) Refresh(ctx context.Context) (err error) {
	v.refreshMu.Lock()
	defer v.refreshMu.Unlock()
	defer func() { v.onRefresh(err == nil) }()

	v.mu.Lock()
	v.lastAttempt = v.now()
	v.mu.Unlock()

	var doc struct {
		Issuer  string `json:"issuer"`
		JWKSURI string `json:"jwks_uri"`
	}
	if err := v.getJSON(ctx, strings.TrimSuffix(v.issuer, "/")+"/.well-known/openid-configuration", &doc); err != nil {
		return fmt.Errorf("discovery: %w", err)
	}
	// The document must say it belongs to the issuer it was fetched for. A
	// mismatch is how a misdirected or hijacked discovery URL shows up, and
	// it is the one check the spec requires of a client.
	if doc.Issuer != v.issuer {
		return fmt.Errorf("discovery document names issuer %q, expected %q", doc.Issuer, v.issuer)
	}
	if doc.JWKSURI == "" {
		return errors.New("discovery document has no jwks_uri")
	}

	var set struct {
		Keys []json.RawMessage `json:"keys"`
	}
	if err := v.getJSON(ctx, doc.JWKSURI, &set); err != nil {
		return fmt.Errorf("key set: %w", err)
	}
	// Keys are parsed one by one: an issuer that publishes a key type this
	// library does not know must not take down the keys it does.
	var keys []jose.JSONWebKey
	for _, raw := range set.Keys {
		var k jose.JSONWebKey
		// A private key in a published key set is somebody's mistake; it is
		// never used as verification material.
		if err := k.UnmarshalJSON(raw); err != nil || !k.Valid() || !k.IsPublic() || k.Use == "enc" {
			continue
		}
		keys = append(keys, k)
	}
	if len(keys) == 0 {
		return errors.New("key set holds no usable signing key")
	}

	v.mu.Lock()
	v.keys = keys
	v.mu.Unlock()
	v.ready.Store(true)
	return nil
}

func (v *Verifier) getJSON(ctx context.Context, target string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := v.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d from %s", resp.StatusCode, target)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, maxDocumentBytes)).Decode(out)
}

// candidates are the keys a token's header allows.
func (v *Verifier) candidates(kid string) []jose.JSONWebKey {
	v.mu.RLock()
	defer v.mu.RUnlock()
	var out []jose.JSONWebKey
	for _, k := range v.keys {
		if kid == "" || k.KeyID == kid {
			out = append(out, k)
		}
	}
	return out
}

// Verify checks the token's signature, issuer, audience and times against
// the upstream issuer, and returns who it says the caller is.
func (v *Verifier) Verify(ctx context.Context, raw string, audiences []string) (*Identity, error) {
	if !v.Ready() {
		return nil, errNoKeys
	}
	tok, err := jwt.ParseSigned(raw, personAlgorithms)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidToken, err)
	}
	if len(tok.Headers) != 1 {
		return nil, fmt.Errorf("%w: expected exactly one signature", errInvalidToken)
	}
	kid := tok.Headers[0].KeyID

	keys := v.candidates(kid)
	if len(keys) == 0 && kid != "" {
		// An unknown kid: maybe the issuer rotated. Look once, at most once
		// per cooldown, and then decide.
		v.mu.RLock()
		last := v.lastAttempt
		v.mu.RUnlock()
		if v.now().Sub(last) >= unknownKidCooldown {
			_ = v.Refresh(ctx)
			keys = v.candidates(kid)
		}
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("%w: no key for kid %q", errInvalidToken, kid)
	}

	var std jwt.Claims
	var all map[string]any
	var verifyErr error
	for _, k := range keys {
		std, all = jwt.Claims{}, nil
		if verifyErr = tok.Claims(k, &std, &all); verifyErr == nil {
			break
		}
	}
	if verifyErr != nil {
		return nil, fmt.Errorf("%w: signature: %v", errInvalidToken, verifyErr)
	}

	now := v.now()
	if err := std.ValidateWithLeeway(jwt.Expected{
		Issuer:      v.issuer,
		AnyAudience: audiences,
		Time:        now,
	}, v.leeway); err != nil {
		return nil, fmt.Errorf("%w: %v", errInvalidToken, err)
	}
	// ValidateWithLeeway checks an expiry only when there is one. A person's
	// token without `exp` would be valid forever, and a minted token takes
	// its expiry from this one.
	if std.Expiry == nil {
		return nil, fmt.Errorf("%w: no exp", errInvalidToken)
	}
	if std.Subject == "" {
		return nil, fmt.Errorf("%w: no sub", errInvalidToken)
	}

	return &Identity{
		Subject: std.Subject,
		Groups:  stringsClaim(all[v.groupClaim]),
		Expiry:  std.Expiry.Time(),
	}, nil
}

// stringsClaim reads a claim that is a string or a list of strings.
func stringsClaim(v any) []string {
	switch t := v.(type) {
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	case []any:
		var out []string
		for _, e := range t {
			if s, ok := e.(string); ok && s != "" {
				out = append(out, s)
			}
		}
		return out
	}
	return nil
}

// unverifiedAudience reads a token's `aud` WITHOUT checking anything. It is
// used to decide which path a token takes and for nothing else; a token
// that takes the person path is then verified in full.
func unverifiedAudience(raw string) ([]string, bool) {
	tok, err := jwt.ParseSigned(raw, personAlgorithms)
	if err != nil {
		return nil, false
	}
	var std jwt.Claims
	if err := tok.UnsafeClaimsWithoutVerification(&std); err != nil {
		return nil, false
	}
	return std.Audience, true
}

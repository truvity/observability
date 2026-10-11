package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/truvity/observability/pkg/tenancy"
)

// checkPrefix is where Envoy sends its checks. The gateway's `path` setting
// is a PREFIX: the check for a request to /api/v1/query arrives at
// /check/api/v1/query, with the original method, so the handler matches the
// prefix and ignores the rest.
const checkPrefix = "/check"

// Authorizer is the Envoy external authorisation service.
type Authorizer struct {
	cfg      Config
	person   []string
	verifier tokenVerifier
	signer   tokenSigner
	metrics  *Metrics
	logger   *slog.Logger
	now      func() time.Time
	cache    *mintCache
}

// tokenVerifier is what Authorizer needs of the upstream verification.
type tokenVerifier interface {
	Verify(ctx context.Context, raw string, audiences []string) (*Identity, error)
}

// tokenSigner signs a minted token with whichever key is active.
type tokenSigner interface {
	Sign(claims ...any) (string, error)
}

// NewAuthorizer builds the handler.
func NewAuthorizer(cfg Config, verifier tokenVerifier, signer tokenSigner, metrics *Metrics, logger *slog.Logger) *Authorizer {
	return &Authorizer{
		cfg:      cfg,
		person:   slices.Clone(cfg.Authz.PersonAudiences),
		verifier: verifier,
		signer:   signer,
		metrics:  metrics,
		logger:   logger,
		now:      time.Now,
		cache:    newMintCache(),
	}
}

// ServeHTTP answers one ext_authz check.
//
//   - Not a person's token (no bearer, not a JWT, or an audience that is not
//     a person audience): 200, nothing changed. vmauth's own readers decide.
//   - A person's token that does not verify: 401.
//   - A verified person whose groups select no principal: 403.
//   - Otherwise: 200 with the Authorization header replaced by a token
//     carrying the union of the selected principals' grants.
//
// Anything the service cannot decide is a 5xx, never a 200: a failure that
// let the person's own token through would be a failure that let a token
// vmauth cannot scope reach the readers meant for something else.
func (a *Authorizer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := a.now()
	outcome := outcomeError
	defer func() {
		a.metrics.Checks.WithLabelValues(outcome).Inc()
		a.metrics.Duration.Observe(a.now().Sub(start).Seconds())
	}()

	raw, ok := bearerToken(r.Header.Get("Authorization"))
	if !ok {
		outcome = outcomePassThrough
		w.WriteHeader(http.StatusOK)
		return
	}
	aud, ok := unverifiedAudience(raw)
	if !ok || !a.isPerson(aud) {
		outcome = outcomePassThrough
		w.WriteHeader(http.StatusOK)
		return
	}

	id, err := a.verifier.Verify(r.Context(), raw, a.person)
	switch {
	case errors.Is(err, errInvalidToken):
		outcome = outcomeUnauthenticated
		a.logger.Info("refused", "outcome", outcome)
		w.Header().Set("WWW-Authenticate", `Bearer error="invalid_token"`)
		http.Error(w, "unauthenticated", http.StatusUnauthorized)
		return
	case err != nil:
		a.logger.Error("verifying a token", "error", err.Error())
		http.Error(w, "authorisation unavailable", http.StatusServiceUnavailable)
		return
	}

	claim, selected, err := a.cfg.ClaimForGroups(id.Groups)
	switch {
	case err != nil:
		a.logger.Error("computing a claim", "sub", id.Subject, "error", err.Error())
		http.Error(w, "authorisation unavailable", http.StatusInternalServerError)
		return
	case !selected:
		outcome = outcomeForbidden
		a.logger.Info("refused", "sub", id.Subject, "outcome", outcome)
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	minted, hit, err := a.mint(id, claim)
	if err != nil {
		a.logger.Error("minting a token", "sub", id.Subject, "error", err.Error())
		http.Error(w, "authorisation unavailable", http.StatusInternalServerError)
		return
	}
	if hit {
		a.metrics.CacheHits.Inc()
	} else {
		a.logger.Info("minted", "sub", id.Subject, "outcome", outcomeMinted)
	}
	outcome = outcomeMinted
	w.Header().Set("Authorization", "Bearer "+minted)
	w.WriteHeader(http.StatusOK)
}

func (a *Authorizer) isPerson(aud []string) bool {
	for _, x := range aud {
		if slices.Contains(a.person, x) {
			return true
		}
	}
	return false
}

// mintedClaims is the body of a minted token. It carries no `groups`: the
// grant is already in `vm_access`, and a groups claim would let a reader
// downstream select a principal by it and apply a second, different answer.
type mintedClaims struct {
	Issuer   string        `json:"iss"`
	Subject  string        `json:"sub"`
	Audience string        `json:"aud"`
	IssuedAt int64         `json:"iat"`
	Expiry   int64         `json:"exp"`
	VMAccess tenancy.Claim `json:"vm_access"`
}

// mint returns a signed token for the identity and claim, from the cache
// when one made earlier is still good. The bool reports a cache hit.
//
// The cache key is the subject, the SORTED groups and the upstream expiry:
// the same person, holding the same groups, on the same upstream token. A
// dashboard fires dozens of queries with one token and costs one signature.
// A new upstream token (a refresh) has a new expiry, so a change of groups
// is picked up at the next token and never served from a stale entry.
func (a *Authorizer) mint(id *Identity, claim tenancy.Claim) (string, bool, error) {
	now := a.now()
	groups := slices.Clone(id.Groups)
	slices.Sort(groups)
	key := id.Subject + "\x00" + strings.Join(groups, "\x00") + "\x00" + id.Expiry.UTC().Format(time.RFC3339)

	if tok, ok := a.cache.get(key, now); ok {
		return tok, true, nil
	}

	exp := now.Add(a.cfg.Authz.TokenTTL)
	if id.Expiry.Before(exp) {
		exp = id.Expiry
	}
	tok, err := a.signer.Sign(mintedClaims{
		Issuer:   a.cfg.Authz.IssuerURL,
		Subject:  id.Subject,
		Audience: a.cfg.Authz.MintedAudience,
		IssuedAt: now.Unix(),
		Expiry:   exp.Unix(),
		VMAccess: claim,
	})
	if err != nil {
		return "", false, err
	}
	a.cache.put(key, tok, now, exp)
	return tok, false, nil
}

// bearerToken extracts the token from an Authorization header. vmauth reads
// the `Bearer` scheme case-insensitively and also `Token`, so this does too:
// a scheme vmauth would accept and this service ignored would be a way
// around the exchange.
func bearerToken(h string) (string, bool) {
	scheme, rest, ok := strings.Cut(strings.TrimSpace(h), " ")
	if !ok {
		return "", false
	}
	if !strings.EqualFold(scheme, "Bearer") && !strings.EqualFold(scheme, "Token") {
		return "", false
	}
	rest = strings.TrimSpace(rest)
	if rest == "" {
		return "", false
	}
	return rest, true
}

// mintCache holds minted tokens until shortly before they expire.
type mintCache struct {
	mu      sync.Mutex
	entries map[string]cacheEntry
	puts    int
}

type cacheEntry struct {
	token string
	until time.Time // do not serve at or after this instant
}

const (
	cacheMaxEntries = 10000
	cacheSweepEvery = 256
	cacheMaxMargin  = 30 * time.Second
)

func newMintCache() *mintCache { return &mintCache{entries: map[string]cacheEntry{}} }

func (c *mintCache) get(key string, now time.Time) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[key]
	if !ok || !now.Before(e.until) {
		return "", false
	}
	return e.token, true
}

// put stores a token minted at `now` that expires at exp. It is served
// until a margin before exp — a quarter of its life, at most 30 seconds —
// so a request that picks it up still has time to reach vmauth.
func (c *mintCache) put(key, token string, now, exp time.Time) {
	margin := exp.Sub(now) / 4
	if margin > cacheMaxMargin {
		margin = cacheMaxMargin
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.puts++
	if c.puts%cacheSweepEvery == 0 || len(c.entries) >= cacheMaxEntries {
		for k, e := range c.entries {
			if !now.Before(e.until) {
				delete(c.entries, k)
			}
		}
	}
	if len(c.entries) >= cacheMaxEntries {
		return // full of live entries: skip caching rather than grow
	}
	c.entries[key] = cacheEntry{token: token, until: exp.Add(-margin)}
}

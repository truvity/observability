// Package lambdaext is the AWS Lambda extension that sends a function's
// OpenTelemetry data to an OTLP endpoint with the function role's identity.
//
// The function exports to a proxy on 127.0.0.1 and holds no credential. The
// proxy asks STS for an identity token for the role the function already
// runs as, trades it at the sluis issuer for a short-lived access
// token, and forwards each export with that token as a bearer. Everything is
// fail-open: nothing here may block, slow or crash an invocation.
package lambdaext

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Environment variable names.
const (
	EnvIssuer       = "OTLP_LAMBDA_ISSUER"
	EnvAudience     = "OTLP_LAMBDA_AUDIENCE"
	EnvOTLPAudience = "OTLP_LAMBDA_OTLP_AUDIENCE"
	EnvOTLPEndpoint = "OTLP_LAMBDA_ENDPOINT"
	EnvListen       = "OTLP_LAMBDA_LISTEN"
	EnvSTSDuration  = "OTLP_LAMBDA_STS_DURATION_SECONDS"
	EnvSTSAlgorithm = "OTLP_LAMBDA_STS_ALGORITHM"
	EnvTokenFile    = "OTLP_LAMBDA_TOKEN_FILE"
	// EnvRefreshWait bounds the refresh finished on INVOKE before the next
	// event is asked for; EnvRefreshMargin is how long before expiry a
	// token is replaced; EnvExchangeTimeout bounds one attempt of a refresh.
	EnvRefreshWait     = "OTLP_LAMBDA_REFRESH_WAIT_MS"
	EnvRefreshMargin   = "OTLP_LAMBDA_REFRESH_MARGIN_SECONDS"
	EnvExchangeTimeout = "OTLP_LAMBDA_EXCHANGE_TIMEOUT_MS"
	defaultListen      = "127.0.0.1:4318"
	defaultAlgorithm   = "ES384"
	defaultOTLPAud     = "otlp"
	defaultDuration    = 300
)

// Config is the extension's settings, all from the environment.
type Config struct {
	// Issuer is the sluis issuer's base URL.
	Issuer string
	// Audience is the audience asked of STS for the identity token. The
	// issuer's AWS verifier decides what it must be; the function role's
	// policy pins it with sts:IdentityTokenAudience.
	Audience string
	// OTLPAudience is the audience (and exchange client id) of the access
	// token the OTLP endpoint accepts.
	OTLPAudience string
	// Endpoint is the upstream OTLP/HTTP base URL, https.
	Endpoint string
	// Listen is the proxy's address.
	Listen string
	// Duration and Algorithm are asked of STS.
	Duration  int32
	Algorithm string
	// TokenFile, when set, receives each access token (0600, atomically).
	TokenFile string
	// RefreshWait, RefreshMargin and ExchangeTimeout are the Source's
	// fields of the same meaning (ExchangeTimeout is its RefreshTimeout).
	RefreshWait, RefreshMargin, ExchangeTimeout time.Duration
	// Deprecated is the old (SLUIS_, ACCESS_ROSTER_) names that were read because their
	// OTLP_LAMBDA_ name was not set. Names only, never values.
	Deprecated []string
}

// LoadConfig reads the environment through getenv. The error names every
// missing or invalid setting at once.
func LoadConfig(getenv func(string) string) (Config, error) {
	env := newEnvReader(getenv)
	get := func(name, fallback string) string {
		if v := strings.TrimSpace(env.lookup(name)); v != "" {
			return v
		}
		return fallback
	}
	c := Config{
		Issuer:       strings.TrimRight(get(EnvIssuer, ""), "/"),
		Audience:     get(EnvAudience, ""),
		OTLPAudience: get(EnvOTLPAudience, defaultOTLPAud),
		Endpoint:     strings.TrimRight(get(EnvOTLPEndpoint, ""), "/"),
		Listen:       get(EnvListen, defaultListen),
		Algorithm:    get(EnvSTSAlgorithm, defaultAlgorithm),
		TokenFile:    get(EnvTokenFile, ""),
		Duration:     defaultDuration,

		RefreshWait:     DefaultRefreshWait,
		RefreshMargin:   DefaultRefreshMargin,
		ExchangeTimeout: DefaultRefreshTimeout,
	}
	var problems []error
	for _, required := range []struct{ name, value string }{
		{EnvIssuer, c.Issuer}, {EnvAudience, c.Audience}, {EnvOTLPEndpoint, c.Endpoint},
	} {
		if required.value == "" {
			problems = append(problems, fmt.Errorf("%s is not set", required.name))
		}
	}
	if raw := get(EnvSTSDuration, ""); raw != "" {
		n, convErr := strconv.Atoi(raw)
		if convErr != nil || n < 60 || n > 3600 {
			problems = append(problems, fmt.Errorf("%s must be 60..3600, got %q", EnvSTSDuration, raw))
		} else {
			c.Duration = int32(n) //nolint:gosec // bounded above
		}
	}
	for _, d := range []struct {
		name   string
		dst    *time.Duration
		unit   time.Duration
		lo, hi int
	}{
		{EnvRefreshWait, &c.RefreshWait, time.Millisecond, 100, 60000},
		{EnvRefreshMargin, &c.RefreshMargin, time.Second, 10, 3600},
		{EnvExchangeTimeout, &c.ExchangeTimeout, time.Millisecond, 100, 60000},
	} {
		if raw := get(d.name, ""); raw != "" {
			n, convErr := strconv.Atoi(raw)
			if convErr != nil || n < d.lo || n > d.hi {
				problems = append(problems, fmt.Errorf("%s must be %d..%d, got %q", d.name, d.lo, d.hi, raw))
			} else {
				*d.dst = time.Duration(n) * d.unit
			}
		}
	}
	if c.Endpoint != "" {
		if upErr := checkUpstream(c.Endpoint); upErr != nil {
			problems = append(problems, upErr)
		}
	}
	c.Deprecated = env.deprecated()
	return c, errors.Join(problems...)
}

// checkUpstream refuses a plain-http upstream that is not loopback: the
// bearer token would cross the network in the clear.
func checkUpstream(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%s is not a URL", EnvOTLPEndpoint)
	}
	switch u.Scheme {
	case "https":
		return nil
	case "http":
		if ip := net.ParseIP(u.Hostname()); (ip != nil && ip.IsLoopback()) || u.Hostname() == "localhost" {
			return nil
		}
	}
	// Only scheme and host are printed: a URL may carry credentials, and the
	// error is logged (and forwarded as a function log).
	return fmt.Errorf("%s must be https, got %s://%s", EnvOTLPEndpoint, u.Scheme, u.Host)
}

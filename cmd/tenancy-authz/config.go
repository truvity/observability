package main

import (
	"bytes"
	"errors"
	"fmt"
	"net/url"
	"os"
	"regexp"
	"slices"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/truvity/observability/pkg/tenancy"
)

// Config is the whole configuration file: the tenancy.Config the chart
// renders the VMUsers from, so the grants this service unions are the very
// ones vmauth would have applied one at a time, plus the `authz` section
// that says who to trust and what to mint.
//
// Flags carry addresses only. Everything that changes what a token is
// allowed to read is in this file, and the file is validated whole at
// start: a service that cannot say what it would enforce does not start.
type Config struct {
	tenancy.Config `yaml:",inline"`

	Authz AuthzConfig `yaml:"authz"`
}

// AuthzConfig is the part of the configuration that is about tokens.
type AuthzConfig struct {
	// UpstreamIssuer is the identity provider people sign in with. Its OIDC
	// discovery document and keys are fetched from here.
	UpstreamIssuer string `yaml:"upstreamIssuer"`

	// PersonAudiences are the `aud` values that mark a token as a person's.
	// A token carrying any of them is verified in full and exchanged. A token
	// carrying none is passed on unchanged, for vmauth's own readers.
	PersonAudiences []string `yaml:"personAudiences"`

	// IssuerURL is THIS service's own issuer: what minted tokens carry as
	// `iss`, and the address vmauth fetches discovery and keys from. It has
	// no path and no trailing slash, because vmauth compares it as a string.
	IssuerURL string `yaml:"issuerURL"`

	// MintedAudience is the `aud` of a minted token.
	MintedAudience string `yaml:"mintedAudience"`

	// TokenTTL bounds a minted token's life. Defaults to 5 minutes.
	TokenTTL time.Duration `yaml:"tokenTTL"`

	// Leeway is the clock skew tolerated when checking a person's token.
	// Defaults to 10 seconds.
	Leeway time.Duration `yaml:"leeway"`

	// SigningKey says where the ES256 key is.
	SigningKey SigningKeyConfig `yaml:"signingKey"`
}

// SigningKeyConfig says where the ES256 key is: a file, which the chart
// mounts from a Secret (one External Secrets generated, or one the operator
// owns). The service only READS it. It re-reads the file as it changes, so a
// rotation is the Secret changing and nothing else.
type SigningKeyConfig struct {
	// File holds the private key: the OpenSSH format External Secrets'
	// SSHKey generator writes, or PKCS#8 / SEC1 PEM. P-256 only.
	File string `yaml:"file"`
}

const (
	defaultTokenTTL = 5 * time.Minute
	defaultLeeway   = 10 * time.Second
	minTokenTTL     = 30 * time.Second
	maxTokenTTL     = time.Hour
	maxLeeway       = time.Minute
)

var audienceShape = regexp.MustCompile(`^\S+$`)

// LoadConfig reads, defaults and validates the file at path. Unknown keys
// are refused: a misspelled key is a setting that silently does not apply.
func LoadConfig(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, err
	}
	return ParseConfig(b)
}

// ParseConfig is LoadConfig on bytes.
func ParseConfig(b []byte) (Config, error) {
	var cfg Config
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&cfg); err != nil {
		return Config{}, fmt.Errorf("parsing configuration: %w", err)
	}
	if cfg.Authz.TokenTTL == 0 {
		cfg.Authz.TokenTTL = defaultTokenTTL
	}
	if cfg.Authz.Leeway == 0 {
		cfg.Authz.Leeway = defaultLeeway
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// Validate reports every problem it can find.
func (c Config) Validate() error {
	var errs []error

	if err := c.Config.Validate(); err != nil {
		errs = append(errs, fmt.Errorf("tenancy: %w", err))
	}

	a := c.Authz
	errs = append(errs, validateIssuerURL("authz.upstreamIssuer", a.UpstreamIssuer, false)...)
	errs = append(errs, validateIssuerURL("authz.issuerURL", a.IssuerURL, true)...)
	if a.UpstreamIssuer != "" && a.UpstreamIssuer == a.IssuerURL {
		errs = append(errs, errors.New("authz.issuerURL equals authz.upstreamIssuer: the service would vouch for its own keys as the identity provider's"))
	}

	if len(a.PersonAudiences) == 0 {
		errs = append(errs, errors.New("authz.personAudiences is empty: no token would ever be exchanged, and every person would be refused by vmauth"))
	}
	for _, aud := range a.PersonAudiences {
		if !audienceShape.MatchString(aud) {
			errs = append(errs, fmt.Errorf("authz.personAudiences has %q, which is not an identifier (blank or carrying whitespace)", aud))
		}
	}
	if !audienceShape.MatchString(a.MintedAudience) {
		errs = append(errs, fmt.Errorf("authz.mintedAudience %q is not an identifier (blank or carrying whitespace)", a.MintedAudience))
	}
	if slices.Contains(a.PersonAudiences, a.MintedAudience) {
		errs = append(errs, fmt.Errorf("authz.mintedAudience %q is also a person audience: a minted token would be treated as a person's token if it ever came back", a.MintedAudience))
	}

	if a.TokenTTL < minTokenTTL || a.TokenTTL > maxTokenTTL {
		errs = append(errs, fmt.Errorf("authz.tokenTTL %s is outside %s..%s", a.TokenTTL, minTokenTTL, maxTokenTTL))
	}
	if a.Leeway < 0 || a.Leeway > maxLeeway {
		errs = append(errs, fmt.Errorf("authz.leeway %s is outside 0..%s", a.Leeway, maxLeeway))
	}

	if a.SigningKey.File == "" {
		errs = append(errs, errors.New("authz.signingKey.file is empty: the service reads its key from a mounted file and makes none"))
	}

	return errors.Join(errs...)
}

func validateIssuerURL(field, raw string, own bool) []error {
	if raw == "" {
		return []error{fmt.Errorf("%s is empty", field)}
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return []error{fmt.Errorf("%s %q is not an http(s) URL", field, raw)}
	}
	var errs []error
	if u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		errs = append(errs, fmt.Errorf("%s %q carries a query, fragment or credentials", field, raw))
	}
	if own && (u.Path != "" || u.RawPath != "") {
		errs = append(errs, fmt.Errorf("%s %q has a path or a trailing slash: the discovery document is served at the root, and vmauth compares the issuer as a string", field, raw))
	}
	return errs
}

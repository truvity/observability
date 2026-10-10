package lambdaprobe

import (
	"errors"
	"fmt"
	"net/url"
	"slices"
)

const (
	// Version is the only config shape this package understands.
	Version = 1

	// DefaultOTLPAudience is the layer's own default (SLUIS_OTLP_AUDIENCE).
	DefaultOTLPAudience = "otlp"
)

type (
	// Config is the whole file.
	Config struct {
		Version int `yaml:"version"`
		// Account is the one account this stack may run in.
		Account string `yaml:"account"`
		Layer   Layer  `yaml:"layer"`
		Probe   Probe  `yaml:"probe"`
	}

	// Layer describes the published layer versions.
	Layer struct {
		// Version is the truvity/observability release, without the "v".
		Version            string   `yaml:"version"`
		Name               string   `yaml:"name"`
		Architectures      []string `yaml:"architectures"`
		CompatibleRuntimes []string `yaml:"compatible_runtimes"`
	}

	// Probe describes the probe function.
	Probe struct {
		FunctionName string `yaml:"function_name"`
		Architecture string `yaml:"architecture"`
		Runtime      string `yaml:"runtime"`
		Schedule     string `yaml:"schedule"`
		IssuerURL    string `yaml:"issuer_url"`
		// Audience overrides the STS token audience; empty means IssuerURL.
		Audience     string `yaml:"audience,omitempty"`
		OTLPEndpoint string `yaml:"otlp_endpoint"`
		// OTLPAudience overrides the exchange audience; empty means the
		// layer's default.
		OTLPAudience string `yaml:"otlp_audience,omitempty"`
	}
)

// STSAudience is the audience asked of STS, pinned by the role policy, and
// expected by the issuer's AWS verifier: Probe.Audience, else the issuer URL.
func (p Probe) STSAudience() string {
	if p.Audience != "" {
		return p.Audience
	}

	return p.IssuerURL
}

// EffectiveOTLPAudience is the exchange audience the layer is configured with.
func (p Probe) EffectiveOTLPAudience() string {
	if p.OTLPAudience != "" {
		return p.OTLPAudience
	}

	return DefaultOTLPAudience
}

// Validate reports every problem it can find.
func (c *Config) Validate() error {
	var errs []error

	bad := func(format string, args ...any) {
		errs = append(errs, fmt.Errorf("lambdaprobe config: "+format, args...))
	}

	if c.Version != Version {
		bad("version %d is not %d", c.Version, Version)
	}

	if c.Account == "" {
		bad("account is required")
	}

	if c.Layer.Version == "" || c.Layer.Version[0] == 'v' {
		bad("layer.version %q must be a release number without the leading v", c.Layer.Version)
	}

	if c.Layer.Name == "" {
		bad("layer.name is required")
	}

	if len(c.Layer.Architectures) == 0 {
		bad("layer.architectures is empty")
	}

	for _, a := range c.Layer.Architectures {
		if a != "arm64" && a != "amd64" {
			bad("layer.architectures: %q is not arm64 or amd64", a)
		}
	}

	if len(c.Layer.CompatibleRuntimes) == 0 {
		bad("layer.compatible_runtimes is empty")
	}

	if c.Probe.FunctionName == "" {
		bad("probe.function_name is required")
	}

	if !slices.Contains(c.Layer.Architectures, c.Probe.Architecture) {
		bad("probe.architecture %q is not one of layer.architectures %v", c.Probe.Architecture, c.Layer.Architectures)
	}

	if !slices.Contains(c.Layer.CompatibleRuntimes, c.Probe.Runtime) {
		bad("probe.runtime %q is not one of layer.compatible_runtimes", c.Probe.Runtime)
	}

	if c.Probe.Schedule == "" {
		bad("probe.schedule is required")
	}

	for name, v := range map[string]string{"probe.issuer_url": c.Probe.IssuerURL, "probe.otlp_endpoint": c.Probe.OTLPEndpoint} {
		if u, err := url.Parse(v); err != nil || u.Scheme != "https" || u.Host == "" {
			bad("%s %q must be an https URL", name, v)
		}
	}

	return errors.Join(errs...)
}

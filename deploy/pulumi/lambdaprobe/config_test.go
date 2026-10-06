package lambdaprobe

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSampleConfigValidates(t *testing.T) {
	c := sampleConfig()
	require.NoError(t, c.Validate())

	// One value drives the role policy, the layer's STS audience and the issuer.
	assert.Equal(t, c.Probe.IssuerURL, c.Probe.STSAudience())
	assert.Equal(t, DefaultOTLPAudience, c.Probe.EffectiveOTLPAudience())
}

func TestValidateRejects(t *testing.T) {
	c := sampleConfig()
	c.Layer.Version = "v0.47.0"
	c.Probe.Architecture = "riscv"
	c.Probe.OTLPEndpoint = "http://otlp.example"

	err := c.Validate()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "without the leading v")
	assert.Contains(t, err.Error(), "probe.architecture")
	assert.Contains(t, err.Error(), "must be an https URL")
}

func TestAudienceOverride(t *testing.T) {
	p := Probe{IssuerURL: "https://i", Audience: "aud"}
	assert.Equal(t, "aud", p.STSAudience())
}

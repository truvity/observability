package tests

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// alert-ingress's DNS egress rule must not default to kube-system: on a
// cluster whose resolver is not a kube-system pod that blocks all DNS, and the
// pod can then resolve neither Alertmanager nor the provider's signing domain.
// A consumer narrows it with networkPolicy.egress.dns.
func TestAlertIngressDNSEgressIsNotKubeSystemByDefault(t *testing.T) {
	goldens, err := filepath.Glob("golden/alert-ingress/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, goldens)
	seen := 0
	for _, g := range goldens {
		for _, raw := range splitDocs(t, g) {
			var d mcpDoc
			if err := yaml.Unmarshal(raw, &d); err != nil || d.Kind != "NetworkPolicy" {
				continue
			}
			seen++
			t.Run(filepath.Base(g), func(t *testing.T) {
				var dns []int
				for i, r := range d.Spec.Egress {
					for _, p := range r.Ports {
						if p.Port == 53 {
							dns = append(dns, i)
							break
						}
					}
				}
				require.Len(t, dns, 1, "exactly one DNS egress rule")
				r := d.Spec.Egress[dns[0]]
				require.Len(t, r.Ports, 2)
				if filepath.Base(g) == "dns-narrowed.yaml" {
					assert.Equal(t, []map[string]any{{"ipBlock": map[string]any{"cidr": "10.96.0.0/16"}}}, r.To)
				} else {
					assert.Empty(t, r.To, "default DNS egress must not be restricted to a destination")
				}
				assert.NotContains(t, r.To, map[string]any{
					"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": "kube-system"}},
				})
			})
		}
	}
	assert.Positive(t, seen)
}

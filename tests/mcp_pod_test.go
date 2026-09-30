package tests

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// charts/observability-mcp's security claim is a shape, not a setting: the
// stock upstream server has no credential and is unreachable from outside
// its pod, the resource-proxy next to it is the ONLY thing with a port, and
// the pod can connect to exactly the issuer, the store's proxy and cluster
// DNS. Each of these is checked on every golden that renders a server, so
// an edit that opens a second port, binds the upstream to all interfaces
// or widens the egress fails here first.

type mcpContainer struct {
	Name  string `yaml:"name"`
	Image string `yaml:"image"`
	Env   []struct {
		Name      string         `yaml:"name"`
		Value     string         `yaml:"value"`
		ValueFrom map[string]any `yaml:"valueFrom"`
	} `yaml:"env"`
	Ports []struct {
		Name          string `yaml:"name"`
		ContainerPort int    `yaml:"containerPort"`
	} `yaml:"ports"`
}

type mcpDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Template struct {
			Spec struct {
				Containers []mcpContainer `yaml:"containers"`
			} `yaml:"spec"`
		} `yaml:"template"`
		PolicyTypes []string `yaml:"policyTypes"`
		Ingress     []struct {
			From  []map[string]any `yaml:"from"`
			Ports []struct {
				Protocol string `yaml:"protocol"`
				Port     int    `yaml:"port"`
			} `yaml:"ports"`
		} `yaml:"ingress"`
		Egress []struct {
			To    []map[string]any `yaml:"to"`
			Ports []struct {
				Protocol string `yaml:"protocol"`
				Port     int    `yaml:"port"`
			} `yaml:"ports"`
		} `yaml:"egress"`
	} `yaml:"spec"`
}

func (c mcpContainer) env(name string) (string, bool) {
	for _, e := range c.Env {
		if e.Name == name {
			return e.Value, true
		}
	}
	return "", false
}

func mcpGoldens(t *testing.T) []string {
	t.Helper()
	goldens, err := filepath.Glob("golden/observability-mcp/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, goldens)
	return goldens
}

func TestMCPUpstreamIsLoopbackOnlyAndProxyIsTheOnlyPort(t *testing.T) {
	var seen int
	for _, g := range mcpGoldens(t) {
		for _, raw := range splitDocs(t, g) {
			var d mcpDoc
			if err := yaml.Unmarshal(raw, &d); err != nil || d.Kind != "Deployment" {
				continue
			}
			seen++
			t.Run(filepath.Base(g)+"/"+d.Metadata.Name, func(t *testing.T) {
				var upstream, proxy *mcpContainer
				cs := d.Spec.Template.Spec.Containers
				for i := range cs {
					switch cs[i].Name {
					case "upstream":
						upstream = &cs[i]
					case "proxy":
						proxy = &cs[i]
					}
				}
				require.NotNil(t, upstream, "no upstream container")
				require.NotNil(t, proxy, "no proxy container")
				require.Len(t, cs, 2, "exactly the upstream and the proxy")

				listen, ok := upstream.env("MCP_LISTEN_ADDR")
				require.True(t, ok, "upstream has no MCP_LISTEN_ADDR: it would listen on the image's default")
				assert.True(t, strings.HasPrefix(listen, "127.0.0.1:"),
					"upstream listens on %q: anything but loopback makes it reachable without the proxy's token check", listen)
				assert.Empty(t, upstream.Ports, "the upstream must declare no container port")

				var total int
				for _, c := range cs {
					total += len(c.Ports)
				}
				assert.Equal(t, 1, total, "the proxy's inbound port must be the pod's only container port")
				require.Len(t, proxy.Ports, 1)

				// The proxy's inbound port is where its own listener is.
				pl, _ := proxy.env("LISTEN")
				assert.Equal(t, fmt.Sprintf(":%d", proxy.Ports[0].ContainerPort), pl)
				// Its outbound side is loopback too.
				ol, _ := proxy.env("OUTBOUND_LISTEN")
				assert.True(t, strings.HasPrefix(ol, "127.0.0.1:"), "outbound listener %q is not loopback", ol)
				// And the upstream reaches the store only through it.
				ep, _ := upstream.env("VM_INSTANCE_ENTRYPOINT")
				assert.True(t, strings.HasPrefix(ep, "http://"+ol), "upstream entrypoint %q is not the proxy's outbound listener %q", ep, ol)

				// The upstream holds no credential of any kind.
				for _, e := range upstream.Env {
					assert.Nil(t, e.ValueFrom, "upstream env %s comes from a Secret or ConfigMap", e.Name)
					assert.NotContains(t, strings.ToUpper(e.Name), "TOKEN", "upstream env %s looks like a credential", e.Name)
					assert.NotContains(t, strings.ToUpper(e.Name), "HEADERS", "upstream env %s injects headers", e.Name)
				}
			})
		}
	}
	assert.Positive(t, seen, "no Deployment in any observability-mcp golden; this check went blind rather than passing")
}

func TestMCPNetworkPolicyReachesOnlyIssuerStoreAndDNS(t *testing.T) {
	var seen int
	for _, g := range mcpGoldens(t) {
		for _, raw := range splitDocs(t, g) {
			var d mcpDoc
			if err := yaml.Unmarshal(raw, &d); err != nil || d.Kind != "NetworkPolicy" {
				continue
			}
			seen++
			t.Run(filepath.Base(g)+"/"+d.Metadata.Name, func(t *testing.T) {
				assert.ElementsMatch(t, []string{"Ingress", "Egress"}, d.Spec.PolicyTypes)

				require.Len(t, d.Spec.Egress, 3, "egress is the issuer, the store's proxy and DNS, and nothing else")
				for i, r := range d.Spec.Egress {
					require.NotEmpty(t, r.To, "egress rule %d has no peer (an empty `to` admits everything)", i)
					require.NotEmpty(t, r.Ports, "egress rule %d has no port (an empty `ports` admits every port)", i)
					for _, p := range r.To {
						if ib, ok := p["ipBlock"].(map[string]any); ok {
							assert.NotContains(t, []any{"0.0.0.0/0", "::/0"}, ib["cidr"], "egress rule %d opens the world", i)
						}
					}
				}

				// DNS is the last rule: kube-system on 53.
				dns := d.Spec.Egress[2]
				require.Len(t, dns.To, 1)
				assert.Equal(t, map[string]any{
					"namespaceSelector": map[string]any{"matchLabels": map[string]any{"kubernetes.io/metadata.name": "kube-system"}},
				}, dns.To[0])
				for _, p := range dns.Ports {
					assert.Equal(t, 53, p.Port)
				}

				// Ingress: one rule, to the proxy's one port.
				require.Len(t, d.Spec.Ingress, 1)
				require.Len(t, d.Spec.Ingress[0].Ports, 1)
				assert.Equal(t, 8080, d.Spec.Ingress[0].Ports[0].Port)
				assert.NotEmpty(t, d.Spec.Ingress[0].From)
			})
		}
	}
	assert.Positive(t, seen, "no NetworkPolicy in any observability-mcp golden; this check went blind rather than passing")
}

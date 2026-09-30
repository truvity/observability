package tests

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// charts/observability-mcp's security claim is a shape, not a setting: every
// stock server is unreachable from outside its pod and holds no credential,
// the resource-proxy next to them holds the pod's only MCP port, and the pod
// can connect to exactly the issuer, the store's proxy (or Grafana) and
// cluster DNS. Each of these is checked on every golden that renders a
// connector, so an edit that opens a second port, binds a stock server to all
// interfaces or widens the egress fails here first.

type mcpContainer struct {
	Name  string   `yaml:"name"`
	Image string   `yaml:"image"`
	Args  []string `yaml:"args"`
	Env   []struct {
		Name      string         `yaml:"name"`
		Value     string         `yaml:"value"`
		ValueFrom map[string]any `yaml:"valueFrom"`
	} `yaml:"env"`
	Ports []struct {
		Name          string `yaml:"name"`
		ContainerPort int    `yaml:"containerPort"`
	} `yaml:"ports"`
	Resources struct {
		Limits map[string]string `yaml:"limits"`
	} `yaml:"resources"`
}

type mcpDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Data map[string]string `yaml:"data"`
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

func (c mcpContainer) arg(prefix string) (string, bool) {
	for _, a := range c.Args {
		if strings.HasPrefix(a, prefix) {
			return strings.TrimPrefix(a, prefix), true
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

func mcpDocs(t *testing.T, kind string) map[string]mcpDoc {
	t.Helper()
	out := map[string]mcpDoc{}
	for _, g := range mcpGoldens(t) {
		for _, raw := range splitDocs(t, g) {
			var d mcpDoc
			if err := yaml.Unmarshal(raw, &d); err != nil || d.Kind != kind {
				continue
			}
			out[filepath.Base(g)+"/"+d.Metadata.Name] = d
		}
	}
	return out
}

var credentialish = regexp.MustCompile(`(?i)TOKEN|SECRET|PASSWORD|API_?KEY|HEADERS|AUTH`)

func TestMCPStockServersAreLoopbackOnlyAndProxyIsTheOnlyMCPPort(t *testing.T) {
	deployments := mcpDocs(t, "Deployment")
	require.NotEmpty(t, deployments, "no Deployment in any observability-mcp golden; this check went blind rather than passing")
	for name, d := range deployments {
		t.Run(name, func(t *testing.T) {
			cs := map[string]mcpContainer{}
			for _, c := range d.Spec.Template.Spec.Containers {
				cs[c.Name] = c
			}
			proxy, ok := cs["proxy"]
			require.True(t, ok, "no proxy container")
			require.Len(t, proxy.Ports, 1)
			assert.Equal(t, "http", proxy.Ports[0].Name)
			pl, _ := proxy.env("LISTEN")
			assert.Equal(t, fmt.Sprintf(":%d", proxy.Ports[0].ContainerPort), pl)
			ol, _ := proxy.env("OUTBOUND_LISTEN")
			require.True(t, strings.HasPrefix(ol, "127.0.0.1:"), "outbound listener %q is not loopback", ol)
			up, _ := proxy.env("UPSTREAM")
			mcpListen := strings.TrimSuffix(strings.TrimPrefix(up, "http://"), "/mcp")
			require.True(t, strings.HasPrefix(mcpListen, "127.0.0.1:"), "the proxy forwards to %q, which is not loopback", up)

			var stock []mcpContainer
			var aggregator *mcpContainer
			for _, c := range d.Spec.Template.Spec.Containers {
				switch {
				case c.Name == "proxy":
				case c.Name == "aggregator":
					c := c
					aggregator = &c
				case strings.HasPrefix(c.Name, "upstream"):
					stock = append(stock, c)
				default:
					t.Errorf("unexpected container %q", c.Name)
				}
			}
			require.NotEmpty(t, stock, "no stock server")

			// Only two ports exist in the pod: the proxy's MCP port, and (in a
			// store pod) the aggregator's admin port, which serves health and
			// metrics and nothing of the MCP surface.
			var total int
			for _, c := range d.Spec.Template.Spec.Containers {
				total += len(c.Ports)
			}
			if aggregator != nil {
				assert.Equal(t, 2, total)
				require.Len(t, aggregator.Ports, 1)
				assert.Equal(t, "admin", aggregator.Ports[0].Name)
				assert.Equal(t, 9090, aggregator.Ports[0].ContainerPort)
				assert.Len(t, stock, strings.Count(aggregatorBackends(t, d.Metadata.Name, name), "prefix:"),
					"one stock server per aggregator backend")
			} else {
				assert.Equal(t, 1, total, "the proxy's inbound port must be the pod's only container port")
			}

			seenListen := map[string]bool{}
			for _, c := range stock {
				assert.Empty(t, c.Ports, "%s must declare no container port", c.Name)
				assert.Contains(t, c.Resources.Limits, "memory", "%s has no memory limit", c.Name)
				assert.NotContains(t, c.Resources.Limits, "cpu", "%s has a CPU limit", c.Name)

				listen, ok := c.env("MCP_LISTEN_ADDR")
				if !ok {
					// mcp-grafana takes flags, not environment.
					listen, ok = c.arg("--address=")
				}
				require.True(t, ok, "%s has no listen address: it would listen on the image's default", c.Name)
				assert.True(t, strings.HasPrefix(listen, "127.0.0.1:"),
					"%s listens on %q: anything but loopback makes it reachable without the proxy's token check", c.Name, listen)
				assert.False(t, seenListen[listen], "%s shares %s with another container", c.Name, listen)
				seenListen[listen] = true

				// Every stock server reaches the store only through the proxy's
				// outbound listener, and holds no credential of any kind.
				for _, key := range []string{"VM_INSTANCE_ENTRYPOINT", "VL_INSTANCE_ENTRYPOINT", "VT_INSTANCE_ENTRYPOINT", "GRAFANA_URL"} {
					if v, ok := c.env(key); ok {
						assert.True(t, strings.HasPrefix(v, "http://"+ol), "%s %s=%q is not the proxy's outbound listener %q", c.Name, key, v, ol)
					}
				}
				for _, e := range c.Env {
					assert.Nil(t, e.ValueFrom, "%s env %s comes from a Secret or ConfigMap", c.Name, e.Name)
					assert.False(t, credentialish.MatchString(e.Name), "%s env %s looks like a credential", c.Name, e.Name)
				}
				for _, a := range c.Args {
					assert.False(t, credentialish.MatchString(a), "%s arg %s looks like a credential", c.Name, a)
				}
			}
			if aggregator != nil {
				assert.True(t, seenListen[mcpListen] == false, "the aggregator's MCP address %s is also a stock server's", mcpListen)
			}
		})
	}
}

// aggregatorBackends returns the aggregator ConfigMap's rendered config of
// the connector the Deployment belongs to.
func aggregatorBackends(t *testing.T, deployment, goldenKey string) string {
	t.Helper()
	golden := strings.SplitN(goldenKey, "/", 2)[0]
	cm, ok := mcpDocs(t, "ConfigMap")[golden+"/"+deployment+"-aggregator"]
	require.True(t, ok, "no aggregator ConfigMap for %s", goldenKey)
	return cm.Data["config.yaml"]
}

// The Grafana connector is read-only by construction, not by the upstream's
// defaults: the default mcp-grafana enables 81 tools, writes among them.
func TestMCPGrafanaIsReadOnlyAndHoldsNoToken(t *testing.T) {
	var seen int
	for name, d := range mcpDocs(t, "Deployment") {
		if !strings.HasSuffix(d.Metadata.Name, "-grafana") {
			continue
		}
		seen++
		t.Run(name, func(t *testing.T) {
			var up *mcpContainer
			for _, c := range d.Spec.Template.Spec.Containers {
				if c.Name == "upstream" {
					c := c
					up = &c
				}
			}
			require.NotNil(t, up)
			assert.Contains(t, up.Args, "--disable-write")
			tools, ok := up.arg("--enabled-tools=")
			require.True(t, ok, "no --enabled-tools: the server would enable its whole default set")
			assert.NotEmpty(t, tools)
			for _, cat := range strings.Split(tools, ",") {
				assert.Contains(t, []string{"search", "dashboard", "folder"}, cat, "%q is not a read-only category", cat)
			}
			assert.NotContains(t, up.Args, "--enable-write-tools")
			host, _ := up.arg("--allowed-hosts=")
			assert.NotContains(t, host, "*", "host validation must not be disabled")
		})
	}
	assert.Positive(t, seen, "no Grafana connector in any golden")
}

func TestMCPNetworkPolicyReachesOnlyIssuerStoreAndDNS(t *testing.T) {
	policies := mcpDocs(t, "NetworkPolicy")
	require.NotEmpty(t, policies, "no NetworkPolicy in any observability-mcp golden; this check went blind rather than passing")
	for name, d := range policies {
		t.Run(name, func(t *testing.T) {
			assert.ElementsMatch(t, []string{"Ingress", "Egress"}, d.Spec.PolicyTypes)

			require.Len(t, d.Spec.Egress, 3, "egress is the issuer, the store's proxy (or Grafana) and DNS, and nothing else")
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

			// Ingress: the gateway on the proxy's one MCP port and, for a store
			// whose consumer named a scraper, that scraper on the admin port
			// only.
			require.NotEmpty(t, d.Spec.Ingress)
			require.LessOrEqual(t, len(d.Spec.Ingress), 2)
			require.Len(t, d.Spec.Ingress[0].Ports, 1)
			assert.Equal(t, 8080, d.Spec.Ingress[0].Ports[0].Port)
			assert.NotEmpty(t, d.Spec.Ingress[0].From)
			if len(d.Spec.Ingress) == 2 {
				assert.False(t, strings.HasSuffix(d.Metadata.Name, "-grafana"), "the Grafana connector has no admin port")
				require.Len(t, d.Spec.Ingress[1].Ports, 1)
				assert.Equal(t, 9090, d.Spec.Ingress[1].Ports[0].Port)
				assert.NotEmpty(t, d.Spec.Ingress[1].From)
			}
		})
	}
}

// Every object of a connector is named for it, and two connectors in one
// install share no name.
func TestMCPObjectsAreNamedPerConnector(t *testing.T) {
	for _, g := range mcpGoldens(t) {
		seen := map[string]bool{}
		for _, raw := range splitDocs(t, g) {
			var d mcpDoc
			if err := yaml.Unmarshal(raw, &d); err != nil || d.Kind == "" {
				continue
			}
			key := d.Kind + "/" + d.Metadata.Name
			assert.False(t, seen[key], "%s renders %s twice", g, key)
			seen[key] = true
			assert.True(t, strings.HasPrefix(d.Metadata.Name, "observability-mcp-"), "%s: %s is not named observability-mcp-<connector>", g, key)
		}
	}
}

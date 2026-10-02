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
	VolumeMounts []struct {
		Name      string `yaml:"name"`
		MountPath string `yaml:"mountPath"`
		ReadOnly  bool   `yaml:"readOnly"`
	} `yaml:"volumeMounts"`
}

type mcpDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Data map[string]string `yaml:"data"`
	Spec struct {
		Replicas int `yaml:"replicas"`
		Strategy *struct {
			Type          string `yaml:"type"`
			RollingUpdate struct {
				MaxUnavailable int `yaml:"maxUnavailable"`
				MaxSurge       int `yaml:"maxSurge"`
			} `yaml:"rollingUpdate"`
		} `yaml:"strategy"`
		Template struct {
			Spec struct {
				TopologySpreadConstraints []struct {
					TopologyKey string `yaml:"topologyKey"`
				} `yaml:"topologySpreadConstraints"`
				Containers []mcpContainer `yaml:"containers"`
				Volumes    []struct {
					Name      string `yaml:"name"`
					ConfigMap *struct {
						Name  string                       `yaml:"name"`
						Items []struct{ Key, Path string } `yaml:"items"`
					} `yaml:"configMap"`
					Secret *struct {
						SecretName string                       `yaml:"secretName"`
						Items      []struct{ Key, Path string } `yaml:"items"`
					} `yaml:"secret"`
				} `yaml:"volumes"`
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
				require.NotEmpty(t, r.Ports, "egress rule %d has no port (an empty `ports` admits every port)", i)
				if i == 2 {
					continue // DNS: checked below, it may legitimately have no `to`
				}
				require.NotEmpty(t, r.To, "egress rule %d has no peer (an empty `to` admits everything)", i)
				for _, p := range r.To {
					if ib, ok := p["ipBlock"].(map[string]any); ok {
						assert.NotContains(t, []any{"0.0.0.0/0", "::/0"}, ib["cidr"], "egress rule %d opens the world", i)
					}
				}
			}

			// DNS is the last rule, on 53 only. By default it has NO `to`
			// (any destination): the resolver is not a kube-system pod on every
			// cluster, so a namespace-scoped default blocks all DNS there. A
			// consumer narrows it with networkPolicy.egress.dns, passed through.
			dns := d.Spec.Egress[2]
			if strings.HasPrefix(name, "dns-narrowed.yaml/") {
				assert.Equal(t, []map[string]any{{"ipBlock": map[string]any{"cidr": "10.96.0.0/16"}}}, dns.To)
			} else {
				assert.Empty(t, dns.To, "default DNS egress must not be restricted to a destination (kube-system or otherwise)")
			}
			assert.NotContains(t, fmt.Sprint(dns.To), "kube-system", "DNS egress must never default to kube-system")
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

// Every connector rolls without a gap (a pod is never removed before its
// replacement is ready), and a connector with more than one replica spreads
// them over zones and nodes; a single replica carries no spread.
func TestMCPRolloutIsGaplessAndReplicasSpread(t *testing.T) {
	sawSpread := false
	for key, d := range mcpDocs(t, "Deployment") {
		require.NotNil(t, d.Spec.Strategy, "%s: no strategy", key)
		assert.Equal(t, "RollingUpdate", d.Spec.Strategy.Type, key)
		assert.Equal(t, 0, d.Spec.Strategy.RollingUpdate.MaxUnavailable, key)
		assert.Equal(t, 1, d.Spec.Strategy.RollingUpdate.MaxSurge, key)
		var keys []string
		for _, c := range d.Spec.Template.Spec.TopologySpreadConstraints {
			keys = append(keys, c.TopologyKey)
		}
		if d.Spec.Replicas > 1 {
			sawSpread = true
			assert.ElementsMatch(t, []string{"topology.kubernetes.io/zone", "kubernetes.io/hostname"}, keys, key)
		} else {
			assert.Empty(t, keys, key)
		}
	}
	assert.True(t, sawSpread, "no golden renders a multi-replica connector")
}

// A private CA for the outbound target is mounted, read-only, into the proxy
// and named by OUTBOUND_CA_FILE, only for a connector that sets `caBundle`;
// every other connector's pod carries neither the volume nor the variable.
func TestMCPProxyMountsCABundleOnlyWhenSet(t *testing.T) {
	// connector -> {volume source kind, source name, key}
	want := map[string][3]string{
		"private-ca.yaml/observability-mcp-private": {"configMap", "private-ca", "ca.crt"},
		"private-ca.yaml/observability-mcp-grafana": {"secret", "grafana-ca", "tls.crt"},
	}
	var withBundle int
	for name, d := range mcpDocs(t, "Deployment") {
		var proxy mcpContainer
		for _, c := range d.Spec.Template.Spec.Containers {
			if c.Name == "proxy" {
				proxy = c
			}
		}
		var vol *int
		for i, v := range d.Spec.Template.Spec.Volumes {
			if v.Name == "outbound-ca" {
				i := i
				vol = &i
			}
		}
		file, hasEnv := proxy.env("OUTBOUND_CA_FILE")
		var mount string
		for _, m := range proxy.VolumeMounts {
			if m.Name == "outbound-ca" {
				mount = m.MountPath
				assert.True(t, m.ReadOnly, "%s: the CA bundle is mounted writable", name)
			}
		}
		w, set := want[name]
		if !set {
			assert.False(t, hasEnv, "%s: OUTBOUND_CA_FILE without a caBundle", name)
			assert.Nil(t, vol, "%s: a CA volume without a caBundle", name)
			assert.Empty(t, mount, "%s: a CA mount without a caBundle", name)
			continue
		}
		withBundle++
		require.True(t, hasEnv, "%s: caBundle set but no OUTBOUND_CA_FILE", name)
		require.NotNil(t, vol, "%s: caBundle set but no volume", name)
		require.NotEmpty(t, mount, "%s: caBundle set but the proxy does not mount it", name)
		assert.Equal(t, mount+"/ca.pem", file, "%s: OUTBOUND_CA_FILE is not the mounted file", name)
		v := d.Spec.Template.Spec.Volumes[*vol]
		switch w[0] {
		case "configMap":
			require.NotNil(t, v.ConfigMap)
			assert.Nil(t, v.Secret)
			assert.Equal(t, w[1], v.ConfigMap.Name)
			require.Len(t, v.ConfigMap.Items, 1)
			assert.Equal(t, w[2], v.ConfigMap.Items[0].Key)
			assert.Equal(t, "ca.pem", v.ConfigMap.Items[0].Path)
		case "secret":
			require.NotNil(t, v.Secret)
			assert.Nil(t, v.ConfigMap)
			assert.Equal(t, w[1], v.Secret.SecretName)
			require.Len(t, v.Secret.Items, 1)
			assert.Equal(t, w[2], v.Secret.Items[0].Key)
			assert.Equal(t, "ca.pem", v.Secret.Items[0].Path)
		}
	}
	assert.Equal(t, len(want), withBundle, "a connector with a caBundle is missing from the goldens")
}

// The egress rule to a connector's target uses `podPort` when set (the Service
// port in the URL is not the port a NetworkPolicy matches), and the URL's port
// when not.
func TestMCPNetworkPolicyUsesPodPortWhenSet(t *testing.T) {
	want := map[string]int{
		"private-ca.yaml/observability-mcp-private": 8428, // vmauth.podPort; the URL says 8427
		"private-ca.yaml/observability-mcp-plain":   8427, // no podPort: the URL's
		"private-ca.yaml/observability-mcp-grafana": 3000, // grafana.podPort; the URL says 80
		"everything.yaml/observability-mcp-grafana": 3000, // no podPort: the URL's
	}
	policies := mcpDocs(t, "NetworkPolicy")
	for name, port := range want {
		d, ok := policies[name]
		require.True(t, ok, "no NetworkPolicy %s", name)
		require.Len(t, d.Spec.Egress, 3)
		require.Len(t, d.Spec.Egress[1].Ports, 1)
		assert.Equal(t, port, d.Spec.Egress[1].Ports[0].Port, name)
	}
}

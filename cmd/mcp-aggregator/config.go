package main

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"regexp"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"
)

// Config is the whole of what the aggregator knows about the world. It
// names no product: a backend is a prefix, a loopback MCP endpoint and the
// upstream tool names that may be exposed. Everything else about the
// surface -- what the tools do, how they are described, what their
// arguments are -- comes from the backends themselves at startup and is
// passed through verbatim.
type Config struct {
	// Listen is where the MCP endpoint is served. It is loopback by
	// default: the resource-proxy next to it is what faces the network.
	Listen string `yaml:"listen"`
	// Path is the MCP endpoint's path on Listen.
	Path string `yaml:"path"`
	// AdminListen serves /healthz, /readyz and /metrics, and nothing of
	// the MCP surface. It is what the kubelet probes and a scraper reads.
	AdminListen string `yaml:"adminListen"`
	// ServerName is the implementation name advertised to clients.
	ServerName string `yaml:"serverName"`
	// Instructions is advertised verbatim as the server's instructions.
	Instructions string `yaml:"instructions"`
	// CallTimeout bounds one tools/call, end to end.
	CallTimeout Duration `yaml:"callTimeout"`
	// ListTimeout bounds one attempt to list one backend's tools at start.
	ListTimeout Duration `yaml:"listTimeout"`
	// AllowNonLoopback permits a backend URL that is not on loopback. Off
	// by default: a backend is a stock server in the same pod, and one
	// reached over the network is a different trust decision.
	AllowNonLoopback bool      `yaml:"allowNonLoopback"`
	Backends         []Backend `yaml:"backends"`
}

// Backend is one upstream MCP server.
type Backend struct {
	// Prefix groups this backend's tools: the exposed name is
	// <prefix>_<tool>.
	Prefix string `yaml:"prefix"`
	// URL is the backend's streamable-HTTP MCP endpoint.
	URL string `yaml:"url"`
	// Tools is the allowlist of upstream tool names. A tool the backend
	// offers that is not listed is never exposed; a listed tool the
	// backend does not offer keeps the aggregator from becoming ready.
	Tools []string `yaml:"tools"`
}

// Duration is a time.Duration that reads "30s" in YAML.
type Duration time.Duration

// UnmarshalYAML implements yaml.Unmarshaler.
func (d *Duration) UnmarshalYAML(n *yaml.Node) error {
	var s string
	if err := n.Decode(&s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("%q is not a duration such as 30s: %w", s, err)
	}
	*d = Duration(v)
	return nil
}

// Std returns the value as a time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// exposedNameRE is the tool-name alphabet and length Claude accepts. A name
// outside it is refused at load, not discovered when a client drops the
// tool.
var exposedNameRE = regexp.MustCompile(`^[a-zA-Z0-9_-]{1,64}$`)

var prefixRE = regexp.MustCompile(`^[a-zA-Z0-9-]{1,32}$`)

// LoadConfig reads and validates the file at path. Unknown keys are refused:
// a misspelt allowlist key would otherwise be an empty allowlist.
func LoadConfig(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseConfig(raw)
}

// ParseConfig decodes and validates a configuration.
func ParseConfig(raw []byte) (*Config, error) {
	cfg := &Config{}
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if err := dec.Decode(cfg); err != nil {
		return nil, fmt.Errorf("decoding configuration: %w", err)
	}
	if err := cfg.applyDefaultsAndValidate(); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) applyDefaultsAndValidate() error {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8081"
	}
	if c.Path == "" {
		c.Path = "/mcp"
	}
	if !strings.HasPrefix(c.Path, "/") {
		return fmt.Errorf("path %q must start with /", c.Path)
	}
	if c.AdminListen == "" {
		c.AdminListen = ":9090"
	}
	if c.ServerName == "" {
		c.ServerName = "mcp-aggregator"
	}
	if c.CallTimeout == 0 {
		c.CallTimeout = Duration(60 * time.Second)
	}
	if c.ListTimeout == 0 {
		c.ListTimeout = Duration(15 * time.Second)
	}
	if c.CallTimeout < 0 || c.ListTimeout < 0 {
		return errors.New("callTimeout and listTimeout must be positive")
	}
	if len(c.Backends) == 0 {
		return errors.New("no backends: an aggregator with nothing to aggregate serves no tools")
	}

	prefixes := map[string]bool{}
	exposed := map[string]string{}
	for i := range c.Backends {
		b := &c.Backends[i]
		if !prefixRE.MatchString(b.Prefix) {
			return fmt.Errorf("backends[%d].prefix %q must match %s", i, b.Prefix, prefixRE)
		}
		if prefixes[b.Prefix] {
			return fmt.Errorf("backends[%d].prefix %q is used twice", i, b.Prefix)
		}
		prefixes[b.Prefix] = true
		if err := checkBackendURL(b.URL, c.AllowNonLoopback); err != nil {
			return fmt.Errorf("backends[%d] (%s).url: %w", i, b.Prefix, err)
		}
		if len(b.Tools) == 0 {
			return fmt.Errorf("backends[%d] (%s).tools is empty: an empty allowlist exposes nothing", i, b.Prefix)
		}
		seen := map[string]bool{}
		for _, t := range b.Tools {
			if t == "" || seen[t] {
				return fmt.Errorf("backends[%d] (%s).tools lists %q twice or empty", i, b.Prefix, t)
			}
			seen[t] = true
			name := b.Prefix + "_" + t
			if !exposedNameRE.MatchString(name) {
				return fmt.Errorf("backends[%d] (%s): exposed tool name %q must match %s", i, b.Prefix, name, exposedNameRE)
			}
			if other, dup := exposed[name]; dup {
				return fmt.Errorf("backends[%d] (%s): exposed tool name %q is also produced by backend %s", i, b.Prefix, name, other)
			}
			exposed[name] = b.Prefix
		}
	}
	return nil
}

func checkBackendURL(raw string, allowNonLoopback bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return fmt.Errorf("%q is not an http(s) URL", raw)
	}
	if allowNonLoopback {
		return nil
	}
	host := u.Hostname()
	if host == "localhost" {
		return nil
	}
	if ip := net.ParseIP(host); ip != nil && ip.IsLoopback() {
		return nil
	}
	return fmt.Errorf("%q is not on loopback; a backend is a stock server in the same pod (set allowNonLoopback to reach one elsewhere)", raw)
}

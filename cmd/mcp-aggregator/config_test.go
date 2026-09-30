package main

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

const goodConfig = `
instructions: hello
backends:
  - prefix: metrics
    url: http://127.0.0.1:8082/mcp
    tools: [query, labels]
  - prefix: logs
    url: http://localhost:8083/mcp
    tools: [query]
`

func TestConfigLoads(t *testing.T) {
	c, err := ParseConfig([]byte(goodConfig))
	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:8081", c.Listen, "MCP is on loopback unless said otherwise")
	assert.Equal(t, "/mcp", c.Path)
	assert.Len(t, c.Backends, 2)
}

func TestConfigRefusals(t *testing.T) {
	long := strings.Repeat("t", 62) // metrics_ + 62 = 70 > 64
	for name, tc := range map[string]struct{ yaml, want string }{
		"unknown key":   {"bakends: []\n", "bakends"},
		"no backends":   {"instructions: x\n", "no backends"},
		"bad prefix":    {"backends:\n - {prefix: 'a b', url: 'http://127.0.0.1:1', tools: [x]}\n", "prefix"},
		"dot in prefix": {"backends:\n - {prefix: 'a.b', url: 'http://127.0.0.1:1', tools: [x]}\n", "prefix"},
		"empty prefix":  {"backends:\n - {url: 'http://127.0.0.1:1', tools: [x]}\n", "prefix"},
		"duplicate prefix": {"backends:\n - {prefix: a, url: 'http://127.0.0.1:1', tools: [x]}\n" +
			" - {prefix: a, url: 'http://127.0.0.1:2', tools: [y]}\n", "used twice"},
		"name too long":   {"backends:\n - {prefix: metrics, url: 'http://127.0.0.1:1', tools: [" + long + "]}\n", "must match"},
		"name with a dot": {"backends:\n - {prefix: m, url: 'http://127.0.0.1:1', tools: ['a.b']}\n", "must match"},
		"empty allowlist": {"backends:\n - {prefix: m, url: 'http://127.0.0.1:1', tools: []}\n", "allowlist"},
		"duplicate tool":  {"backends:\n - {prefix: m, url: 'http://127.0.0.1:1', tools: [x, x]}\n", "twice"},
		"remote backend":  {"backends:\n - {prefix: m, url: 'http://example.com/mcp', tools: [x]}\n", "loopback"},
		"not a url":       {"backends:\n - {prefix: m, url: 'nope', tools: [x]}\n", "http(s) URL"},
		"bad duration":    {"callTimeout: soon\nbackends:\n - {prefix: m, url: 'http://127.0.0.1:1', tools: [x]}\n", "duration"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseConfig([]byte(tc.yaml))
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.want)
		})
	}
}

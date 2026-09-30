package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// charts/observability-mcp generates this binary's configuration. The two are
// kept honest by this test: every aggregator ConfigMap in every golden render
// must be accepted by the real parser, with no backend off loopback, no
// exposed name the validator would refuse, and instructions short enough for
// a client to read whole.
func TestChartRendersAConfigTheBinaryAccepts(t *testing.T) {
	goldens, err := filepath.Glob(filepath.Join("..", "..", "tests", "golden", "observability-mcp", "*.yaml"))
	require.NoError(t, err)
	require.NotEmpty(t, goldens)

	var seen int
	for _, g := range goldens {
		raw, err := os.ReadFile(g)
		require.NoError(t, err)
		dec := yaml.NewDecoder(strings.NewReader(string(raw)))
		for {
			var doc struct {
				Kind     string                `yaml:"kind"`
				Metadata struct{ Name string } `yaml:"metadata"`
				Data     map[string]string     `yaml:"data"`
			}
			if err := dec.Decode(&doc); err != nil {
				break
			}
			if doc.Kind != "ConfigMap" {
				continue
			}
			seen++
			t.Run(filepath.Base(g)+"/"+doc.Metadata.Name, func(t *testing.T) {
				cfg, err := ParseConfig([]byte(doc.Data["config.yaml"]))
				require.NoError(t, err, "the chart renders a configuration the aggregator refuses")
				assert.Equal(t, "127.0.0.1:8081", cfg.Listen)
				assert.Equal(t, "/mcp", cfg.Path, "the proxy forwards to /mcp")
				assert.False(t, cfg.AllowNonLoopback)
				assert.Equal(t, strings.TrimSuffix(doc.Metadata.Name, "-aggregator"), cfg.ServerName)
				assert.LessOrEqual(t, len(cfg.Instructions), 2048, "a client truncates instructions past 2048 characters")
				assert.NotEmpty(t, cfg.Instructions)
				for _, b := range cfg.Backends {
					for _, tool := range b.Tools {
						assert.NotContains(t, []string{"documentation", "flags", "export", "test_rules", "prettify_query"}, tool,
							"%s exposes %s", b.Prefix, tool)
					}
				}
			})
		}
	}
	assert.Positive(t, seen, "no aggregator ConfigMap in any golden; this check went blind rather than passing")
}

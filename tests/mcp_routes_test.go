package tests

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// Every tool the observability-mcp connector exposes must have its HTTP path
// served by the store's vmauth for the principal the connector runs as, or it
// fails with a 401 by design. This is the test that says so.
//
// The path of each tool is what the stock server asks for, read from its
// source at the pinned tag (mcp-victoriametrics v1.20.2, mcp-victorialogs
// v1.9.0, mcp-victoriatraces v1.5.0), for the entrypoint the chart gives it:
// the proxy's outbound listener plus the prefix in `upstreams.*.entrypointPath`
// (`/prometheus` for metrics; nothing for logs and traces, which add their own
// `/select/...`). hack/mcp-paths-proof.sh observes the same paths from the
// running binaries. The routes are the ones tests/golden/observability-stack/
// tenancy-mcp-reader.yaml renders for a principal with all three routes and
// `vmalertAPI`.
var mcpToolPaths = map[string]map[string]string{
	"victoriametrics": {
		"query":        "/prometheus/api/v1/query",
		"query_range":  "/prometheus/api/v1/query_range",
		"metrics":      "/prometheus/api/v1/label/__name__/values",
		"labels":       "/prometheus/api/v1/labels",
		"label_values": "/prometheus/api/v1/label/job/values",
		"series":       "/prometheus/api/v1/series",
		"rules":        "/prometheus/vmalert/api/v1/rules",
		"alerts":       "/prometheus/vmalert/api/v1/alerts",
		"tsdb_status":  "/prometheus/api/v1/status/tsdb",
		// Parses locally; its metadata lookups are best-effort and their
		// failure is swallowed, so it has no path it depends on.
		"explain_query": "",
	},
	"victorialogs": {
		"hits":                "/select/logsql/hits",
		"facets":              "/select/logsql/facets",
		"stats_query":         "/select/logsql/stats_query",
		"stats_query_range":   "/select/logsql/stats_query_range",
		"field_names":         "/select/logsql/field_names",
		"field_values":        "/select/logsql/field_values",
		"stream_field_names":  "/select/logsql/stream_field_names",
		"stream_field_values": "/select/logsql/stream_field_values",
		"stream_ids":          "/select/logsql/stream_ids",
		"streams":             "/select/logsql/streams",
		"query":               "/select/logsql/query",
	},
	"victoriatraces": {
		"traces":             "/select/jaeger/api/traces",
		"trace":              "/select/jaeger/api/traces/0123456789abcdef",
		"services":           "/select/jaeger/api/services",
		"service_operations": "/select/jaeger/api/services/frontend/operations",
		"dependencies":       "/select/jaeger/api/dependencies",
	},
}

// Tools the connector does NOT expose, and the path each asks for. None may
// be served by a route in the render: if the stack ever serves one, that is a
// decision to make (and to add to the allowlist), not a thing to learn from
// a green test.
var mcpDroppedToolPaths = map[string]string{
	// Its upstream code calls <entrypoint>/prettify-query when the local
	// parse SUCCEEDS, a path no read route admits.
	"victoriametrics/prettify_query": "/prometheus/prettify-query",
	// Admin endpoints at the store's root.
	"victorialogs/flags": "/flags",
}

type mcpAllowlists struct {
	Upstreams map[string]struct {
		Tools []string `yaml:"tools"`
	} `yaml:"upstreams"`
}

func readerRoutes(t *testing.T) []*regexp.Regexp {
	t.Helper()
	var out []*regexp.Regexp
	var found bool
	for _, raw := range splitDocs(t, filepath.Join("golden", "observability-stack", "tenancy-mcp-reader.yaml")) {
		var d vmalertUserDoc
		if err := yaml.Unmarshal(raw, &d); err != nil || d.Kind != "VMUser" || d.Spec.Name != "example:mcp:reader" {
			continue
		}
		found = true
		for _, r := range d.Spec.TargetRefs {
			for _, p := range r.Paths {
				// vmauth anchors a route's path regular expression.
				out = append(out, regexp.MustCompile("^(?:"+p+")$"))
			}
		}
	}
	require.True(t, found, "the reader principal is not in the golden; this check went blind rather than passing")
	return out
}

func routed(routes []*regexp.Regexp, path string) bool {
	for _, r := range routes {
		if r.MatchString(path) {
			return true
		}
	}
	return false
}

func TestMCPEveryExposedToolIsServedByTheStoreRoutes(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "charts", "observability-mcp", "values.yaml"))
	require.NoError(t, err)
	var v mcpAllowlists
	require.NoError(t, yaml.Unmarshal(raw, &v))

	routes := readerRoutes(t)
	for upstream, paths := range mcpToolPaths {
		exposed := v.Upstreams[upstream].Tools
		require.NotEmpty(t, exposed, "%s has no allowlist", upstream)
		for _, tool := range exposed {
			path, known := paths[tool]
			require.True(t, known, "%s/%s is allowlisted but has no verified path in mcpToolPaths: read the stock server's source for what it calls, add it, and check a route serves it", upstream, tool)
			if path == "" {
				continue
			}
			assert.True(t, routed(routes, path), "%s/%s calls %s, which no route in the store's vmauth serves for the connector's principal: the tool would fail by design", upstream, tool, path)
		}
	}
}

func TestMCPDroppedToolsAreNotServed(t *testing.T) {
	routes := readerRoutes(t)
	for tool, path := range mcpDroppedToolPaths {
		assert.False(t, routed(routes, path), "%s calls %s, which a route now serves: it was dropped because it could not work; decide whether to expose it", tool, path)
	}
}

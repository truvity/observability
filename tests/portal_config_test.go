package tests

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// The portal's extra catalog has ONE contract: apps/portal/schema/
// portal.schema.json. The page validates what it loads against it, and these
// tests hold the other two producers to it — the chart's render and the
// fixtures the app's own tests use — so a field added to one and not the
// others fails here rather than as a banner on somebody's entrypoint.

const portalSchemaPath = "../apps/portal/schema/portal.schema.json"

func compilePortalSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()

	compiler := jsonschema.NewCompiler()
	compiler.Draft = jsonschema.Draft7

	raw, err := os.ReadFile(portalSchemaPath)
	require.NoError(t, err)
	require.NoError(t, compiler.AddResource("portal.schema.json", bytes.NewReader(raw)))

	schema, err := compiler.Compile("portal.schema.json")
	require.NoError(t, err)

	return schema
}

func validatePortal(schema *jsonschema.Schema, doc []byte) error {
	var v any
	if err := json.Unmarshal(doc, &v); err != nil {
		return err
	}

	return schema.Validate(v)
}

// portalConfigs returns the portal.json of every ConfigMap in a golden render.
func portalConfigs(t *testing.T, golden string) []string {
	t.Helper()

	raw, err := os.ReadFile(golden)
	require.NoError(t, err)

	var out []string

	dec := yaml.NewDecoder(bytes.NewReader(raw))
	for {
		var doc struct {
			Kind string            `yaml:"kind"`
			Data map[string]string `yaml:"data"`
		}

		if err := dec.Decode(&doc); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			require.NoError(t, err)
		}

		if doc.Kind == "ConfigMap" && doc.Data["portal.json"] != "" {
			out = append(out, doc.Data["portal.json"])
		}
	}

	return out
}

func TestPortalChartConfigMatchesTheSchema(t *testing.T) {
	schema := compilePortalSchema(t)

	goldens, err := filepath.Glob("golden/observability-portal/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, goldens)

	for _, golden := range goldens {
		t.Run(filepath.Base(golden), func(t *testing.T) {
			configs := portalConfigs(t, golden)
			require.Len(t, configs, 1, "one portal.json per release")
			assert.NoError(t, validatePortal(schema, []byte(configs[0])))
		})
	}
}

// The chart renders only what was set: a default install is {"version": 1}.
func TestPortalChartMinimalRendersNoEstate(t *testing.T) {
	configs := portalConfigs(t, "golden/observability-portal/minimal.yaml")
	require.Len(t, configs, 1)

	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(configs[0]), &doc))
	assert.Equal(t, map[string]any{"version": float64(1)}, doc)
}

// The chart's `extraEntries` is the file's `entries`, untouched.
func TestPortalChartEverythingCarriesEveryKey(t *testing.T) {
	configs := portalConfigs(t, "golden/observability-portal/everything.yaml")
	require.Len(t, configs, 1)

	var doc map[string]any
	require.NoError(t, json.Unmarshal([]byte(configs[0]), &doc))

	for _, key := range []string{"title", "lede", "issuer", "replaceDefaults", "tierOrder", "entries", "orientation", "commandLine", "guides"} {
		assert.Contains(t, doc, key)
	}

	assert.Len(t, doc["entries"], 3)
}

// The app's own fixtures, judged by the Go validator: the same schema must
// say the same thing in both languages.
func TestPortalSchemaFixtures(t *testing.T) {
	schema := compilePortalSchema(t)

	files, err := filepath.Glob("../apps/portal/testdata/*.json")
	require.NoError(t, err)
	require.NotEmpty(t, files)

	for _, file := range files {
		name := filepath.Base(file)

		t.Run(name, func(t *testing.T) {
			raw, err := os.ReadFile(file)
			require.NoError(t, err)

			err = validatePortal(schema, raw)
			if strings.HasPrefix(name, "valid-") {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

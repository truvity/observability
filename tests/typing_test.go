// A field of the wrong type is not pruned. It is refused, and the whole
// object goes with it.
//
// That is the other half of the defect the pruning check covers, and the
// reasoning that left it out was wrong in one respect worth writing down.
// The pruning check says a value the schema rejects "produces a loud error
// on apply" and so needs no test. It does produce one — but apply is not a
// place anybody is watching. The error this check exists for surfaced as a
// continuous-delivery sync that failed on a single resource while every
// other object in the release reported healthy, hours after the merge that
// caused it, in a system whose next unrelated change was blocked behind it.
// Loud is not the same as seen.
//
// The shape that produces it is ordinary: a key written unconditionally
// with contents written conditionally. When every condition is false the
// key renders with an empty body, and an empty body is null. It renders,
// it lints, and it reads as deliberate in a diff.
package tests

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// typeViolations walks an object against its schema and returns a
// description of every value whose type the definition contradicts.
//
// It reports only the contradictions a structural schema is certain about:
// a null where a type is declared, and a scalar, mapping or sequence where
// one of the other two is required. It stays out of scalar-against-scalar
// judgements, where YAML's own coercions make a disagreement between, say,
// `"true"` and `true` a question about the serializer rather than about
// the object.
func typeViolations(v any, schema *jsonSchema, path string) []string {
	if schema == nil || schema.IntOrString {
		return nil
	}

	where := strings.TrimPrefix(path, ".")
	if where == "" {
		where = "(root)"
	}

	if v == nil {
		// `nullable: true` is how a definition says a null belongs here.
		if schema.Type == "" || schema.Nullable {
			return nil
		}

		return []string{fmt.Sprintf("%s is null, but the definition types it as %s. "+
			"The API server refuses the whole object, not just this field", where, schema.Type)}
	}

	switch schema.Type {
	case "object":
		body, ok := v.(map[string]any)
		if !ok {
			return []string{fmt.Sprintf("%s is %s, but the definition types it as object", where, kindOf(v))}
		}

		return typeViolationsInObject(body, schema, path)
	case "array":
		items, ok := v.([]any)
		if !ok {
			return []string{fmt.Sprintf("%s is %s, but the definition types it as array", where, kindOf(v))}
		}

		if schema.Items == nil {
			return nil
		}

		var found []string
		for i, e := range items {
			found = append(found, typeViolations(e, schema.Items, fmt.Sprintf("%s[%d]", path, i))...)
		}

		return found
	case "string", "boolean", "integer", "number":
		if kindOf(v) != "scalar" {
			return []string{fmt.Sprintf("%s is %s, but the definition types it as %s", where, kindOf(v), schema.Type)}
		}
	}

	return nil
}

// typeViolationsInObject descends into a mapping's declared properties.
// A key the definition does not declare is the pruning check's business,
// not this one's.
func typeViolationsInObject(obj map[string]any, schema *jsonSchema, path string) []string {
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}

	sort.Strings(keys) // stable output, so a failure reads the same twice

	var found []string

	for _, k := range keys {
		child := path + "." + k

		sub, declared := schema.Properties[k]
		if !declared {
			if ap := schema.AdditionalProperties; ap != nil && ap.schema != nil {
				found = append(found, typeViolations(obj[k], ap.schema, child)...)
			}

			continue
		}

		found = append(found, typeViolations(obj[k], sub, child)...)
	}

	return found
}

func kindOf(v any) string {
	switch v.(type) {
	case map[string]any:
		return "a mapping"
	case []any:
		return "a sequence"
	default:
		return "scalar"
	}
}

// checkDocTypes validates one rendered document, returning false for a
// document that is not a custom resource of a kind this repository ships.
func checkDocTypes(doc []byte, schemas map[schemaKey]*jsonSchema) (key schemaKey, bad []string, checked bool) {
	var head object
	if err := yaml.Unmarshal(doc, &head); err != nil {
		return key, nil, false
	}

	if head.APIVersion == "" || head.Kind == "" {
		return key, nil, false
	}

	key = schemaKey{apiVersion: head.APIVersion, kind: head.Kind}

	schema, ok := schemas[key]
	if !ok {
		return key, nil, false
	}

	var body map[string]any
	if err := yaml.Unmarshal(doc, &body); err != nil {
		return key, nil, false
	}

	// Same exclusion the pruning check makes: metadata is validated
	// against ObjectMeta by the API server, not against this schema.
	delete(body, "metadata")

	return key, typeViolationsInObject(body, schema, ""), true
}

// TestRenderedObjectsMatchTheCRDTypes is the check the null `extraArgs`
// defect needed. Every custom resource these charts render is walked
// against the definition this repository installs for its kind, and a
// value whose type the definition contradicts fails the test here rather
// than in a cluster.
func TestRenderedObjectsMatchTheCRDTypes(t *testing.T) {
	schemas := loadSchemas(t)
	require.NotEmpty(t, schemas, "no CustomResourceDefinition schemas found in the observability-crds golden")

	goldens, err := filepath.Glob("golden/*/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, goldens)

	var checked int

	for _, g := range goldens {
		if strings.Contains(g, "observability-crds") {
			continue // the definitions themselves, not objects against them
		}

		t.Run(strings.TrimSuffix(strings.TrimPrefix(g, "golden/"), ".yaml"), func(t *testing.T) {
			for _, doc := range splitDocs(t, g) {
				key, bad, ok := checkDocTypes(doc, schemas)
				if !ok {
					continue
				}

				checked++

				assert.Emptyf(t, bad, "%s: %s does not match the CustomResourceDefinition this "+
					"repository installs for it: %s", g, key, strings.Join(bad, "; "))
			}
		})
	}

	// A check that silently matched nothing is worse than no check: it
	// passes forever while proving nothing.
	assert.Positive(t, checked, "no rendered custom resource was matched to a shipped CustomResourceDefinition")
	t.Logf("%d rendered custom resources type-checked against %d CustomResourceDefinition versions", checked, len(schemas))
}

// TestRejectedFixturesAreCaught proves the check above can fail. Each
// fixture under tests/rejected/ is an object the API server refuses
// outright, and each must be reported.
//
// Without this, TestRenderedObjectsMatchTheCRDTypes is a test whose only
// evidence is that it has never failed.
func TestRejectedFixturesAreCaught(t *testing.T) {
	schemas := loadSchemas(t)

	fixtures, err := filepath.Glob("rejected/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, fixtures, "tests/rejected/ is empty")

	for _, f := range fixtures {
		t.Run(filepath.Base(f), func(t *testing.T) {
			docs := splitDocs(t, f)
			require.NotEmpty(t, docs)

			var total []string

			for _, doc := range docs {
				key, bad, ok := checkDocTypes(doc, schemas)
				require.Truef(t, ok, "fixture %s names no kind this repository ships a definition for — "+
					"it would be skipped rather than caught", f)
				t.Logf("%s: %v", key, bad)
				total = append(total, bad...)
			}

			assert.NotEmptyf(t, total, "fixture %s was NOT reported, so the check would have let "+
				"the defect it stands for through", f)
		})
	}
}

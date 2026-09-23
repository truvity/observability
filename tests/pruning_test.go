// A field the API server prunes is indistinguishable, in a rendered
// manifest and in every golden, from a field that works.
//
// That is the whole defect class. A template writes a key the
// CustomResourceDefinition does not have; the render is valid YAML, the
// schema check passes because the key is in the MANIFEST rather than in the
// values, `helm lint` is happy, and the golden is byte-identical to one
// that would have worked. The API server then drops the key on the way in
// and stores what is left. Nothing between the template and the cluster can
// tell the difference, so the only thing that can is a check that reads the
// CustomResourceDefinitions themselves.
//
// This repository is in the unusual position of shipping both halves:
// charts/observability-crds vendors the definitions, and the other charts
// render objects against them. So the check needs no cluster and no
// network. It reads the definitions out of the observability-crds goldens,
// walks every custom resource in every other golden, and reports any field
// the definition has no room for.
//
// What pruning costs is never the pruned field. It is whatever the field
// was holding off: a VMAuth left with an unauthorized section that routes
// nowhere is refused by the operator and gets no Deployment at all, and a
// VMUser left without its default claim refuses every token for that
// principal. Both read, from outside, as the cluster being broken
// somewhere else.
package tests

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// crdsGoldenGlob is where the definitions come from: the rendered output of
// the chart that installs them, so the check is against what this
// repository would actually put in a cluster rather than against whatever
// an upstream tag happens to hold today.
const crdsGoldenGlob = "golden/observability-crds/*.yaml"

// schemaKey identifies one CustomResourceDefinition version.
type schemaKey struct {
	apiVersion string // group/version, as an object spells it
	kind       string
}

func (k schemaKey) String() string { return k.apiVersion + "/" + k.kind }

// jsonSchema is the part of an OpenAPI v3 schema that decides whether a
// field survives. Everything else about the schema — formats, bounds,
// required lists — is the API server's business and not this check's: a
// value the schema rejects produces a loud error on apply, while a field it
// has never heard of produces silence, and silence is what this is for.
type jsonSchema struct {
	Type                  string                 `yaml:"type"`
	Properties            map[string]*jsonSchema `yaml:"properties"`
	Items                 *jsonSchema            `yaml:"items"`
	AdditionalProperties  *additionalProperties  `yaml:"additionalProperties"`
	PreserveUnknownFields *bool                  `yaml:"x-kubernetes-preserve-unknown-fields"`
	IntOrString           bool                   `yaml:"x-kubernetes-int-or-string"`
}

// additionalProperties is `true`/`false` or a schema, and YAML gives no
// warning about which, so both are decoded and the caller asks.
type additionalProperties struct {
	allowAny bool
	schema   *jsonSchema
}

func (a *additionalProperties) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind == yaml.ScalarNode {
		var b bool
		if err := n.Decode(&b); err != nil {
			return err
		}
		a.allowAny = b
		return nil
	}
	a.schema = &jsonSchema{}
	return n.Decode(a.schema)
}

// crdDoc is the shape of a CustomResourceDefinition this check reads.
type crdDoc struct {
	Kind string `yaml:"kind"`
	Spec struct {
		Group string `yaml:"group"`
		Names struct {
			Kind string `yaml:"kind"`
		} `yaml:"names"`
		Versions []struct {
			Name   string `yaml:"name"`
			Schema struct {
				OpenAPIV3Schema *jsonSchema `yaml:"openAPIV3Schema"`
			} `yaml:"schema"`
		} `yaml:"versions"`
	} `yaml:"spec"`
}

// loadSchemas reads every CustomResourceDefinition out of the
// observability-crds goldens.
func loadSchemas(t *testing.T) map[schemaKey]*jsonSchema {
	t.Helper()

	files, err := filepath.Glob(crdsGoldenGlob)
	require.NoError(t, err)
	require.NotEmpty(t, files, "no observability-crds golden — run 'just golden'")

	out := map[schemaKey]*jsonSchema{}
	for _, f := range files {
		for _, doc := range splitDocs(t, f) {
			var crd crdDoc
			if err := yaml.Unmarshal(doc, &crd); err != nil {
				continue // not every document in a CRDs render is a CRD
			}
			if crd.Kind != "CustomResourceDefinition" || crd.Spec.Group == "" {
				continue
			}
			for _, v := range crd.Spec.Versions {
				if v.Schema.OpenAPIV3Schema == nil {
					continue
				}
				key := schemaKey{
					apiVersion: crd.Spec.Group + "/" + v.Name,
					kind:       crd.Spec.Names.Kind,
				}
				out[key] = v.Schema.OpenAPIV3Schema
			}
		}
	}
	return out
}

// splitDocs reads a multi-document YAML file into its documents.
func splitDocs(t *testing.T, path string) [][]byte {
	t.Helper()

	raw, err := os.ReadFile(path)
	require.NoError(t, err)

	var docs [][]byte
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	for {
		var n yaml.Node
		err := dec.Decode(&n)
		if err != nil {
			break
		}
		b, err := yaml.Marshal(&n)
		require.NoError(t, err)
		docs = append(docs, b)
	}
	return docs
}

// object is the minimum needed to route a document to its schema.
type object struct {
	APIVersion string         `yaml:"apiVersion"`
	Kind       string         `yaml:"kind"`
	Metadata   map[string]any `yaml:"metadata"`
	Body       map[string]any `yaml:",inline"`
}

// prunedFields walks an object against its schema and returns the dotted
// path of every field the API server would drop.
//
// It deliberately does NOT descend into `metadata`: a CustomResourceDefinition
// declares that as a bare object and the API server validates it against
// ObjectMeta instead, so every label and annotation would otherwise be
// reported as unknown.
func prunedFields(obj map[string]any, schema *jsonSchema, path string) []string {
	if schema == nil {
		// No schema for this position at all: the API server keeps nothing
		// it cannot place, but a structural schema always has one, so this
		// is a gap in the definition rather than in the object.
		return nil
	}
	if schema.IntOrString {
		return nil
	}
	// `x-kubernetes-preserve-unknown-fields` holds at the node that sets it
	// and no further. An unknown key directly here survives; a child that
	// carries `properties` of its own still prunes inside itself. That is
	// the shape this defect hid in — `VMAuth.spec` preserves unknown
	// fields, so `spec.anythingAtAll` is kept, while
	// `spec.unauthorizedUserAccessSpec` does not, so `.disabled` under it
	// is dropped. Verified against a live API server, not inferred.
	preserve := schema.PreserveUnknownFields != nil && *schema.PreserveUnknownFields

	var found []string
	keys := make([]string, 0, len(obj))
	for k := range obj {
		keys = append(keys, k)
	}
	sort.Strings(keys) // stable output, so a failure reads the same twice

	for _, k := range keys {
		child := path + "." + k
		sub, known := schema.Properties[k]
		if !known {
			if ap := schema.AdditionalProperties; ap != nil {
				if ap.allowAny {
					continue
				}
				if ap.schema != nil {
					found = append(found, descend(obj[k], ap.schema, child)...)
					continue
				}
			}
			if preserve {
				continue // kept, and nothing below it has a schema either
			}
			found = append(found, strings.TrimPrefix(child, "."))
			continue
		}
		found = append(found, descend(obj[k], sub, child)...)
	}
	return found
}

// descend follows one value into maps and slices.
func descend(v any, schema *jsonSchema, path string) []string {
	switch tv := v.(type) {
	case map[string]any:
		return prunedFields(tv, schema, path)
	case []any:
		if schema == nil || schema.Items == nil {
			return nil
		}
		var found []string
		for i, e := range tv {
			found = append(found, descend(e, schema.Items, fmt.Sprintf("%s[%d]", path, i))...)
		}
		return found
	default:
		return nil
	}
}

// checkDoc validates one rendered document. It returns nil for a document
// that is not a custom resource of a kind this repository ships.
func checkDoc(doc []byte, schemas map[schemaKey]*jsonSchema) (key schemaKey, pruned []string, checked bool) {
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
	delete(body, "metadata")

	return key, prunedFields(body, schema, ""), true
}

// TestRenderedObjectsSurviveTheCRDs is the check the `disabled: true` defect
// needed. Every custom resource these charts render is walked against the
// definition this repository installs for its kind, and a field the
// definition has no room for fails the test — because in a cluster it does
// not fail anything at all.
func TestRenderedObjectsSurviveTheCRDs(t *testing.T) {
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
				key, pruned, ok := checkDoc(doc, schemas)
				if !ok {
					continue
				}
				checked++
				assert.Emptyf(t, pruned, "%s: the CustomResourceDefinition for %s has no room for %v — "+
					"the API server would PRUNE it, the render and the golden would not change, "+
					"and only the cluster would disagree", g, key, pruned)
			}
		})
	}

	// A check that silently matched nothing is worse than no check: it
	// passes forever while proving nothing.
	assert.Positive(t, checked, "no rendered custom resource was matched to a shipped CustomResourceDefinition")
	t.Logf("%d rendered custom resources checked against %d CustomResourceDefinition versions", checked, len(schemas))
}

// TestPrunedFixturesAreCaught proves the check above can fail. Each fixture
// under tests/pruned/ is a manifest that renders, lints and diffs like any
// other and is destroyed by the API server, and each must be reported.
//
// Without this, TestRenderedObjectsSurviveTheCRDs is a test whose only
// evidence is that it has never failed.
func TestPrunedFixturesAreCaught(t *testing.T) {
	schemas := loadSchemas(t)

	fixtures, err := filepath.Glob("pruned/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, fixtures, "tests/pruned/ is empty")

	for _, f := range fixtures {
		t.Run(filepath.Base(f), func(t *testing.T) {
			docs := splitDocs(t, f)
			require.NotEmpty(t, docs)

			var total []string
			for _, doc := range docs {
				key, pruned, ok := checkDoc(doc, schemas)
				require.Truef(t, ok, "fixture %s names no kind this repository ships a definition for — "+
					"it would be skipped rather than caught", f)
				t.Logf("%s: %v", key, pruned)
				total = append(total, pruned...)
			}
			assert.NotEmptyf(t, total, "fixture %s was NOT reported as pruned, so the check would "+
				"have let the defect it stands for through", f)
		})
	}
}

// TestNoUnauthorizedUser is the narrow one, stated in the object rather
// than in the operator's behaviour.
//
// No VMAuth these charts render may carry `unauthorizedUserAccessSpec` at
// all. The field has no shape that refuses: the operator rejects a section
// with no route (`at least one of url_map, url_prefix or targetRefs must be
// defined`), so every shape it accepts is a shape that SERVES — and a token
// that verifies but carries no `vm_access` claim falls through to it and is
// answered instead of refused. Absence is the only setting that means no.
func TestNoUnauthorizedUser(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, goldens)

	var seen int
	for _, g := range goldens {
		for _, doc := range splitDocs(t, g) {
			var head object
			if err := yaml.Unmarshal(doc, &head); err != nil || head.Kind != "VMAuth" {
				continue
			}
			seen++
			var body struct {
				Spec map[string]any `yaml:"spec"`
			}
			require.NoError(t, yaml.Unmarshal(doc, &body))
			for _, field := range []string{"unauthorizedUserAccessSpec", "unauthorizedAccessConfig"} {
				_, present := body.Spec[field]
				assert.Falsef(t, present, "%s: VMAuth carries spec.%s. Every shape the operator "+
					"accepts there routes somewhere, and a token that verifies with no `vm_access` "+
					"claim is then SERVED through it rather than refused with 401", g, field)
			}
		}
	}
	assert.Positive(t, seen, "no VMAuth found in any observability-stack golden")
}

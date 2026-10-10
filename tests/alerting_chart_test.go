package tests

// The safety proof for adopting charts/observability-alerting
// (docs/adoption.md, 0.71.0).
//
// An Argo CD Application switch can only adopt the stack's alerting objects
// in place if the new chart renders them under exactly the kind, name and
// namespace the stack does, with the same content. "In place" is the whole
// point: a delete and recreate of a VMAlertmanager is a gap in alert
// delivery, and of a VMAlert a gap in evaluation. The alerting chart carries
// copies of the stack's templates and helpers (a library chart would force a
// dependency build and rename every helper in the stack), so a copy that
// drifts is exactly the failure this file exists to catch.
//
// For EVERY case under tests/cases/observability-stack (presets merged), the
// stack is rendered twice, with the default `alerting.source: stack` and with
// `alerting.source: chart`, and the alerting chart once, from the same values
// kept to the keys it declares and with `stackReleaseName` set to the
// stack's release name. The proof is three equalities:
//
//	stack(default) == stack(chart) U alerting
//
// as sets of (kind, name), with the two halves disjoint, and the documents of
// each member of the union byte-identical to the stack's default render of
// that object (the `# Source:` comment Helm prints is the only line ignored:
// it names the template file, not the object).

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// movedKinds is what the alerting plane consists of; a moved object of any
// other kind is a surprise somebody should read.
var movedKinds = map[string]bool{
	"VMAlert": true, "VMAlertmanager": true, "VMRule": true, "ServiceMonitor": true,
	"ServiceAccount": true, "ConfigMap": true, "Service": true, "Deployment": true,
}

const stackRelease = "observability-stack"

func helmTemplate(t *testing.T, release, chart, namespace string, args ...string) (string, string, error) {
	t.Helper()
	full := append([]string{"template", release, filepath.Join("..", "charts", chart), "-n", namespace}, args...)
	cmd := exec.Command("helm", full...)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	return stdout.String(), stderr.String(), err
}

// objectsByKey splits a `helm template` output into documents keyed by
// "Kind/name", each normalised the way the proof compares them.
func objectsByKey(t *testing.T, out string) map[string]string {
	t.Helper()
	res := map[string]string{}
	for _, doc := range regexp.MustCompile(`(?m)^---\n`).Split(out, -1) {
		var kept []string
		for _, l := range strings.Split(doc, "\n") {
			if strings.HasPrefix(l, "# Source:") {
				continue
			}
			kept = append(kept, l)
		}
		text := strings.TrimSpace(strings.Join(kept, "\n"))
		if text == "" {
			continue
		}
		var meta struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name      string `yaml:"name"`
				Namespace string `yaml:"namespace"`
			} `yaml:"metadata"`
		}
		require.NoError(t, yaml.Unmarshal([]byte(text), &meta), text)
		key := meta.Kind + "/" + meta.Metadata.Namespace + "/" + meta.Metadata.Name
		require.NotContains(t, res, key, "the same object rendered twice by one chart")
		res[key] = text
	}
	return res
}

func readYAMLMap(t *testing.T, path string) map[string]any {
	t.Helper()
	b, err := os.ReadFile(path)
	require.NoError(t, err)
	m := map[string]any{}
	require.NoError(t, yaml.Unmarshal(b, &m))
	return m
}

func deepMerge(dst, src map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range dst {
		out[k] = v
	}
	for k, v := range src {
		if sm, ok := v.(map[string]any); ok {
			if dm, ok := out[k].(map[string]any); ok {
				out[k] = deepMerge(dm, sm)
				continue
			}
		}
		out[k] = v
	}
	return out
}

type schemaDoc struct {
	Definitions map[string]map[string]any `json:"definitions"`
	Properties  map[string]any            `json:"properties"`
}

// keepDeclared drops, recursively, every key a strict schema node does not
// declare. A node that is not strict (an open object, a list, a scalar) is
// kept whole: only the sections the alerting chart declares in a smaller
// form (tenancy, backup) differ in shape from the stack's.
func (s schemaDoc) keepDeclared(v any, node map[string]any) any {
	for {
		ref, ok := node["$ref"].(string)
		if !ok {
			break
		}
		node = s.Definitions[strings.TrimPrefix(ref, "#/definitions/")]
	}
	m, isMap := v.(map[string]any)
	props, hasProps := node["properties"].(map[string]any)
	if !isMap || !hasProps || node["additionalProperties"] != false {
		return v
	}
	out := map[string]any{}
	for k, x := range m {
		if p, ok := props[k].(map[string]any); ok {
			out[k] = s.keepDeclared(x, p)
		}
	}
	return out
}

func loadAlertingSchema(t *testing.T) schemaDoc {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "charts", "observability-alerting", "values.schema.json"))
	require.NoError(t, err)
	var s schemaDoc
	require.NoError(t, json.Unmarshal(b, &s))
	return s
}

func TestAlertingChartRendersExactlyWhatTheStackHandsOver(t *testing.T) {
	schema := loadAlertingSchema(t)
	cases, err := filepath.Glob("cases/observability-stack/*/values.yaml")
	require.NoError(t, err)
	require.GreaterOrEqual(t, len(cases), 40, "the stack cases moved; this proof would pass on too few")

	var mu sync.Mutex
	movedSeen := map[string]bool{}
	for _, valuesPath := range cases {
		caseDir := filepath.Dir(valuesPath)
		name := filepath.Base(caseDir)
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			namespace := "default"
			if b, err := os.ReadFile(filepath.Join(caseDir, "namespace")); err == nil {
				namespace = strings.TrimSpace(string(b))
			}
			var files []string
			merged := map[string]any{}
			if b, err := os.ReadFile(filepath.Join(caseDir, "presets")); err == nil {
				for _, p := range strings.Fields(string(b)) {
					pf := filepath.Join("..", "charts", "observability-stack", "presets", p+".yaml")
					files = append(files, "-f", pf)
					merged = deepMerge(merged, readYAMLMap(t, pf))
				}
			}
			files = append(files, "-f", valuesPath)
			merged = deepMerge(merged, readYAMLMap(t, valuesPath))

			defOut, defErr, err := helmTemplate(t, stackRelease, "observability-stack", namespace, files...)
			require.NoError(t, err, defErr)
			def := objectsByKey(t, defOut)

			if a, ok := merged["alerting"].(map[string]any); ok && a["source"] == "chart" {
				// The case that already sits on the handed-over side (the
				// `alerting-chart` golden): the default render IS the off
				// render, so there is no "before" to compare with. The
				// golden test below covers it.
				return
			}

			mode, _ := merged["mode"].(string)
			if mode != "" && mode != "full" {
				// Nothing to hand over: both the switch and the chart refuse.
				_, _, err := helmTemplate(t, stackRelease, "observability-stack", namespace,
					append(append([]string{}, files...), "--set", "alerting.source=chart")...)
				assert.Error(t, err, "alerting.source=chart with mode %s must be refused", mode)
				declared := schema.keepDeclared(merged, map[string]any{"properties": schema.Properties, "additionalProperties": false}).(map[string]any)
				declared["stackReleaseName"] = stackRelease
				f := writeTempYAML(t, declared)
				_, _, err = helmTemplate(t, "observability-alerting", "observability-alerting", namespace, "-f", f)
				assert.Error(t, err, "the alerting chart must refuse mode %s", mode)
				return
			}

			offOut, offErr, err := helmTemplate(t, stackRelease, "observability-stack", namespace,
				append(append([]string{}, files...), "--set", "alerting.source=chart")...)
			require.NoError(t, err, offErr)
			off := objectsByKey(t, offOut)

			declared := schema.keepDeclared(merged, map[string]any{"properties": schema.Properties, "additionalProperties": false}).(map[string]any)
			declared["stackReleaseName"] = stackRelease
			alertOut, alertErr, err := helmTemplate(t, "observability-alerting", "observability-alerting", namespace,
				"-f", writeTempYAML(t, declared))
			require.NoError(t, err, alertErr)
			alert := objectsByKey(t, alertOut)

			// The stack with the switch on keeps everything it had that the
			// plane does not own, unchanged.
			for k, doc := range off {
				assert.Equal(t, def[k], doc, "%s: the switch changed an object that stays in the stack", k)
			}
			// The two halves are disjoint.
			for k := range alert {
				assert.NotContains(t, off, k, "%s is rendered by both the stack (switch on) and the alerting chart", k)
			}
			// Everything that left the stack is in the alerting chart,
			// byte for byte, and nothing else is.
			moved := map[string]string{}
			for k, doc := range def {
				if _, stays := off[k]; !stays {
					moved[k] = doc
				}
			}
			var movedKeys, alertKeys []string
			for k := range moved {
				movedKeys = append(movedKeys, k)
			}
			for k := range alert {
				alertKeys = append(alertKeys, k)
			}
			sort.Strings(movedKeys)
			sort.Strings(alertKeys)
			require.Equal(t, movedKeys, alertKeys, "the alerting chart renders a different set of objects than the stack hands over")
			for _, k := range alertKeys {
				assert.Equal(t, moved[k], alert[k], "%s differs between the stack and the alerting chart", k)
				kind, _, _ := strings.Cut(k, "/")
				assert.True(t, movedKinds[kind], "%s: a kind the alerting plane is not expected to contain", k)
			}
			for _, k := range alertKeys {
				kind, _, _ := strings.Cut(k, "/")
				mu.Lock()
				movedSeen[kind] = true
				mu.Unlock()
			}
		})
	}
	t.Cleanup(func() {
		// Not vacuous: across the cases, every kind of the plane was
		// exercised at least once (a VMAlert, the VMAlertmanager, karma's
		// four objects, a rule, the ServiceMonitor).
		for kind := range movedKinds {
			assert.True(t, movedSeen[kind], "no stack case hands over a %s; the proof does not cover it", kind)
		}
	})
}

func writeTempYAML(t *testing.T, v any) string {
	t.Helper()
	b, err := yaml.Marshal(v)
	require.NoError(t, err)
	p := filepath.Join(t.TempDir(), "values.yaml")
	require.NoError(t, os.WriteFile(p, b, 0o600))
	return p
}

// The committed goldens carry the same proof where a reviewer reads it: the
// stack's `alertmanager-pair` render minus its `alerting-chart` render (the
// same values, switch on) is the alerting chart's `alertmanager-pair` render.
func TestAlertingGoldensAreTheStacksGoldenMinusTheHandedOverPart(t *testing.T) {
	read := func(p string) map[string]string {
		b, err := os.ReadFile(p)
		require.NoError(t, err, "regenerate the golden renders with `just golden`")
		return objectsByKey(t, string(b))
	}
	full := read("golden/observability-stack/alertmanager-pair.yaml")
	off := read("golden/observability-stack/alerting-chart.yaml")
	al := read("golden/observability-alerting/alertmanager-pair.yaml")
	require.NotEmpty(t, al)
	for k, doc := range off {
		assert.Equal(t, full[k], doc, "%s: the switch changed an object that stays in the stack", k)
	}
	want := map[string]string{}
	for k, doc := range full {
		if _, stays := off[k]; !stays {
			want[k] = doc
		}
	}
	assert.Equal(t, want, al)
}

// A value the alerting chart declares must be one the stack declares, with
// the same schema: "copy and paste" migration is the contract for moving a
// section between the two values files. `tenancy`, `backup` and the
// subchart mirrors are reduced on purpose and are compared on the keys they
// keep.
func TestAlertingChartValuesAreTheStacksValues(t *testing.T) {
	read := func(chart string) map[string]any {
		b, err := os.ReadFile(filepath.Join("..", "charts", chart, "values.schema.json"))
		require.NoError(t, err)
		m := map[string]any{}
		require.NoError(t, json.Unmarshal(b, &m))
		return m["properties"].(map[string]any)
	}
	stack, alerting := read("observability-stack"), read("observability-alerting")
	own := map[string]bool{"stackReleaseName": true} // the one key the stack has no use for
	reduced := map[string]bool{"tenancy": true, "backup": true,
		"victoria-metrics-k8s-stack": true, "victoria-logs-single": true, "victoria-traces-single": true}
	for k, v := range alerting {
		if own[k] {
			continue
		}
		require.Contains(t, stack, k, "%s is declared by the alerting chart and not by the stack", k)
		if reduced[k] {
			continue
		}
		assert.Equal(t, stack[k], v, "%s has a different schema in the two charts: a copy-paste migration would change meaning", k)
	}
	assert.Contains(t, stack, "alerting", "the stack's switch")
}

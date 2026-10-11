package tests

// The safety proof for adopting charts/observability-proxy
// (docs/adoption.md, 0.81.0).
//
// An Argo CD Application switch can only adopt the stack's Door objects in
// place if the new chart renders them under exactly the kind, name and
// namespace the stack does, with the same content. "In place" is the whole
// point: a delete and recreate of the VMAuth or a VMUser is a gap in every
// read and every cross-cluster write. The proxy chart carries copies of the
// stack's templates and helpers (the same choice charts/observability-alerting
// made), so a copy that drifts is exactly the failure this file exists to
// catch.
//
// For EVERY case under tests/cases/observability-stack (presets merged), the
// stack is rendered twice, with the default `door.source: stack` and with
// `door.source: chart`, and the proxy chart once, from the same values kept
// to the keys it declares and with `stackReleaseName` set to the stack's
// release name. The proof:
//
//	stack(default) == stack(chart) U proxy
//
// as sets of (kind, namespace, name), the two halves disjoint, and the
// documents of each member of the union byte-identical to the stack's
// default render of that object (the `# Source:` comment Helm prints is the
// only line ignored: it names the template file, not the object).

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// doorKinds is what the Door plane consists of; a moved object of any other
// kind is a surprise somebody should read.
var doorKinds = map[string]bool{
	"VMAuth": true, "VMUser": true, "ServiceAccount": true, "ConfigMap": true,
	"Service": true, "Deployment": true, "PodDisruptionBudget": true,
	"ServiceMonitor": true, "SecurityPolicy": true, "NetworkPolicy": true,
	"SSHKey": true, "ExternalSecret": true,
}

func loadChartSchema(t *testing.T, chart string) schemaDoc {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "charts", chart, "values.schema.json"))
	require.NoError(t, err)
	var s schemaDoc
	require.NoError(t, json.Unmarshal(b, &s))
	return s
}

func TestProxyChartRendersExactlyWhatTheStackHandsOver(t *testing.T) {
	schema := loadChartSchema(t, "observability-proxy")
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
			declared := schema.keepDeclared(merged, map[string]any{"properties": schema.Properties, "additionalProperties": false}).(map[string]any)
			declared["stackReleaseName"] = stackRelease

			if d, ok := merged["door"].(map[string]any); ok && d["source"] == "chart" {
				// A case already on the handed-over side (`door-chart`): the
				// default render IS the off render, so there is no "before"
				// to compare with. The golden test below covers it.
				return
			}

			mode, _ := merged["mode"].(string)
			if mode != "" && mode != "full" {
				// Nothing to hand over: both the switch and the chart refuse.
				_, _, err := helmTemplate(t, stackRelease, "observability-stack", namespace,
					append(append([]string{}, files...), "--set", "door.source=chart")...)
				assert.Error(t, err, "door.source=chart with mode %s must be refused", mode)
				_, _, err = helmTemplate(t, "observability-proxy", "observability-proxy", namespace, "-f", writeTempYAML(t, declared))
				assert.Error(t, err, "the proxy chart must refuse mode %s", mode)
				return
			}

			defOut, defErr, err := helmTemplate(t, stackRelease, "observability-stack", namespace, files...)
			require.NoError(t, err, defErr)
			def := objectsByKey(t, defOut)

			offOut, offErr, err := helmTemplate(t, stackRelease, "observability-stack", namespace,
				append(append([]string{}, files...), "--set", "door.source=chart")...)
			require.NoError(t, err, offErr)
			off := objectsByKey(t, offOut)

			proxyOut, proxyErr, err := helmTemplate(t, "observability-proxy", "observability-proxy", namespace,
				"-f", writeTempYAML(t, declared))
			require.NoError(t, err, proxyErr)
			proxy := objectsByKey(t, proxyOut)

			// The stack with the switch on keeps everything the Door does
			// not own, unchanged.
			for k, doc := range off {
				assert.Equal(t, def[k], doc, "%s: the switch changed an object that stays in the stack", k)
			}
			// The two halves are disjoint.
			for k := range proxy {
				assert.NotContains(t, off, k, "%s is rendered by both the stack (switch on) and the proxy chart", k)
			}
			// Everything that left the stack is in the proxy chart, byte for
			// byte, and nothing else is.
			moved := map[string]string{}
			for k, doc := range def {
				if _, stays := off[k]; !stays {
					moved[k] = doc
				}
			}
			var movedKeys, proxyKeys []string
			for k := range moved {
				movedKeys = append(movedKeys, k)
			}
			for k := range proxy {
				proxyKeys = append(proxyKeys, k)
			}
			sort.Strings(movedKeys)
			sort.Strings(proxyKeys)
			require.Equal(t, movedKeys, proxyKeys, "the proxy chart renders a different set of objects than the stack hands over")
			for _, k := range proxyKeys {
				assert.Equal(t, moved[k], proxy[k], "%s differs between the stack and the proxy chart", k)
				kind, _, _ := strings.Cut(k, "/")
				assert.True(t, doorKinds[kind], "%s: a kind the Door plane is not expected to contain", k)
				mu.Lock()
				movedSeen[kind] = true
				mu.Unlock()
			}
		})
	}
	t.Cleanup(func() {
		// Not vacuous: across the cases, every kind of the plane was
		// exercised at least once.
		for kind := range doorKinds {
			assert.True(t, movedSeen[kind], "no stack case hands over a %s; the proof does not cover it", kind)
		}
	})
}

// The committed goldens carry the same proof where a reviewer reads it: the
// stack's `tenancy-authz` render minus its `door-chart` render (the same
// values, switch on) is the proxy chart's `tenancy-authz` render.
func TestProxyGoldensAreTheStacksGoldenMinusTheHandedOverPart(t *testing.T) {
	read := func(p string) map[string]string {
		b, err := os.ReadFile(p)
		require.NoError(t, err, "regenerate the golden renders with `just golden`")
		return objectsByKey(t, string(b))
	}
	full := read("golden/observability-stack/tenancy-authz.yaml")
	off := read("golden/observability-stack/door-chart.yaml")
	px := read("golden/observability-proxy/tenancy-authz.yaml")
	require.NotEmpty(t, px)
	for k, doc := range off {
		assert.Equal(t, full[k], doc, "%s: the switch changed an object that stays in the stack", k)
	}
	want := map[string]string{}
	for k, doc := range full {
		if _, stays := off[k]; !stays {
			want[k] = doc
		}
	}
	assert.Equal(t, want, px)
}

// A value the proxy chart declares must be one the stack declares, with the
// same schema: "copy and paste" migration is the contract for moving a
// section between the two values files. The mirrors are reduced on purpose
// and are compared on the keys they keep.
func TestProxyChartValuesAreTheStacksValues(t *testing.T) {
	read := func(chart string) map[string]any {
		b, err := os.ReadFile(filepath.Join("..", "charts", chart, "values.schema.json"))
		require.NoError(t, err)
		m := map[string]any{}
		require.NoError(t, json.Unmarshal(b, &m))
		return m["properties"].(map[string]any)
	}
	stack, proxy := read("observability-stack"), read("observability-proxy")
	own := map[string]bool{"stackReleaseName": true}
	reduced := map[string]bool{"networkPolicy": true, "resources": true, "vmalert": true, "alertmanager": true,
		"victoria-metrics-k8s-stack": true, "victoria-logs-single": true, "victoria-traces-single": true}
	for k, v := range proxy {
		if own[k] {
			continue
		}
		require.Contains(t, stack, k, "%s is declared by the proxy chart and not by the stack", k)
		if reduced[k] {
			continue
		}
		assert.Equal(t, stack[k], v, "%s has a different schema in the two charts: a copy-paste migration would change meaning", k)
	}
	assert.Contains(t, stack, "door", "the stack's switch")
}

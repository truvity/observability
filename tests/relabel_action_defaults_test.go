// This file guards against the exact class of latch fixed in 0.6.1: a
// relabeling step that omits `action` because `replace` is its default.
//
// It IS the default — for whoever reads the YAML. It is not the default
// for what actually ships. The monitoring.coreos.com ServiceMonitor and
// PodMonitor CRDs default `action` to `replace` in their structural
// schema, which the API server applies ON ADMISSION: the object the
// cluster actually stores always carries `action: replace`, whether the
// manifest that created it wrote the field out or not. ArgoCD (and any
// other GitOps diff) compares the RENDERED manifest against that STORED
// object, field for field, not against what a human reading the chart
// would consider equivalent — so a chart that omits `action` renders a
// manifest that can never match what the cluster holds, and the
// Application it belongs to is OutOfSync forever on a field nothing
// ever changed. Same class as this chart's earlier `record: ""` VMRule
// issue: write out whatever the server would otherwise fill in.
//
// So this walks every golden render, finds every ServiceMonitor and
// PodMonitor, and fails if any relabeling step anywhere in it — endpoint
// or pod-metrics-endpoint, `metricRelabelings` or `relabelings` — omits
// `action`. It does not matter whether this repository's own default
// values produced the step or a case file overrode them: nothing
// checked into golden/ may ship an implicit `action` again.
package tests

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// relabelingListKeys are the field names, across both the Prometheus
// Operator's and the VictoriaMetrics operator's spellings, that hold a
// list of relabeling steps on a ServiceMonitor/PodMonitor endpoint.
var relabelingListKeys = []string{"metricRelabelings", "relabelings"}

func TestGoldenServiceMonitorsAndPodMonitorsWriteOutRelabelAction(t *testing.T) {
	matches, err := filepath.Glob("golden/*/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, matches, "no golden renders found — run `just golden` first")

	checked := 0
	for _, path := range matches {
		raw, err := os.ReadFile(path)
		require.NoErrorf(t, err, "reading %s", path)

		dec := yaml.NewDecoder(strings.NewReader(string(raw)))
		for {
			var doc map[string]any
			derr := dec.Decode(&doc)
			if derr != nil {
				break
			}
			kind, _ := doc["kind"].(string)
			if kind != "ServiceMonitor" && kind != "PodMonitor" {
				continue
			}
			name := "<unnamed>"
			if meta, ok := doc["metadata"].(map[string]any); ok {
				if n, ok := meta["name"].(string); ok {
					name = n
				}
			}
			n := checkRelabelingsCarryAction(t, path, kind, name, doc)
			checked += n
		}
	}
	require.Positive(t, checked,
		"no relabeling steps were found on any ServiceMonitor/PodMonitor in golden/ — "+
			"this guard has nothing to check, which likely means the goldens are stale or this walk no longer matches their shape")
}

// checkRelabelingsCarryAction walks every endpoints-shaped list on a
// ServiceMonitor/PodMonitor doc (spec.endpoints, spec.podMetricsEndpoints
// — whichever the kind carries) and asserts every relabeling step under
// it names its own action. Returns the number of steps it checked.
func checkRelabelingsCarryAction(t *testing.T, path, kind, name string, doc map[string]any) int {
	t.Helper()
	spec, ok := doc["spec"].(map[string]any)
	if !ok {
		return 0
	}
	checked := 0
	for _, epKey := range []string{"endpoints", "podMetricsEndpoints"} {
		eps, ok := spec[epKey].([]any)
		if !ok {
			continue
		}
		for i, rawEP := range eps {
			ep, ok := rawEP.(map[string]any)
			if !ok {
				continue
			}
			for _, listKey := range relabelingListKeys {
				steps, ok := ep[listKey].([]any)
				if !ok {
					continue
				}
				for j, rawStep := range steps {
					step, ok := rawStep.(map[string]any)
					if !ok {
						continue
					}
					checked++
					_, hasAction := step["action"]
					require.Truef(t, hasAction,
						"%s: %s %q spec.%s[%d].%s[%d] omits `action` (step: %v) — "+
							"the ServiceMonitor/PodMonitor CRD defaults this to `replace` ON ADMISSION, so the "+
							"stored object always carries it and a rendered manifest that omits it can never "+
							"match, latching the owning Application OutOfSync forever; write `action: replace` "+
							"(or whichever action this step means) explicitly",
						path, kind, name, epKey, i, listKey, j, step)
				}
			}
		}
	}
	return checked
}

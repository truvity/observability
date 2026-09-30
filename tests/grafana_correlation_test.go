// Logs and traces link to each other, inside one store.
//
// The chart gives every store a logs and a traces datasource with uids
// `<store>-logs` and `<store>-traces`. A link that named another store's
// uid would open a trace from the wrong estate (or a dead button), so the
// property proved here is: in a multi-store render, each store's logs
// derived field targets THAT store's traces uid and each store's
// tracesToLogsV2 targets THAT store's logs uid.
package tests

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

type grafanaDatasource struct {
	UID      string         `yaml:"uid"`
	Type     string         `yaml:"type"`
	JSONData map[string]any `yaml:"jsonData"`
}

// readGrafanaDatasources pulls the provisioning file out of a golden render
// of the observability-grafana chart.
func readGrafanaDatasources(t *testing.T, path string) map[string]grafanaDatasource {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "regenerate the golden with `just golden`")
	for _, doc := range strings.Split(string(raw), "\n---") {
		var cm struct {
			Kind string            `yaml:"kind"`
			Data map[string]string `yaml:"data"`
		}
		if yaml.Unmarshal([]byte(doc), &cm) != nil || cm.Kind != "ConfigMap" {
			continue
		}
		body, ok := cm.Data["datasources.yaml"]
		if !ok {
			continue
		}
		var file struct {
			Datasources []grafanaDatasource `yaml:"datasources"`
		}
		require.NoError(t, yaml.Unmarshal([]byte(body), &file))
		out := map[string]grafanaDatasource{}
		for _, d := range file.Datasources {
			out[d.UID] = d
		}
		return out
	}
	require.Fail(t, "no datasources ConfigMap in "+path)
	return nil
}

func TestEachStoresLogsAndTracesLinkToEachOther(t *testing.T) {
	ds := readGrafanaDatasources(t, "golden/observability-grafana/minimal.yaml")
	for _, store := range []string{"store-a", "store-b"} {
		logs, ok := ds[store+"-logs"]
		require.True(t, ok, "%s has no logs datasource", store)
		traces, ok := ds[store+"-traces"]
		require.True(t, ok, "%s has no traces datasource", store)

		derived, ok := logs.JSONData["derivedFields"].([]any)
		require.True(t, ok, "%s logs carry no derivedFields", store)
		require.Len(t, derived, 1)
		f := derived[0].(map[string]any)
		assert.Equal(t, "TraceID", f["name"])
		assert.Equal(t, "trace_id", f["matcherRegex"], "the field OpenTelemetry logs carry the trace id in")
		assert.Equal(t, store+"-traces", f["datasourceUid"], "a log must open ITS OWN store's trace")
		assert.Equal(t, "$${__value.raw}", f["url"], "`$$` escapes a literal `$`; a bare one is expanded as an env var")

		link, ok := traces.JSONData["tracesToLogsV2"].(map[string]any)
		require.True(t, ok, "%s traces carry no tracesToLogsV2", store)
		assert.Equal(t, store+"-logs", link["datasourceUid"], "a trace must open ITS OWN store's logs")
		assert.Equal(t, `trace_id:"$${__trace.traceId}"`, link["query"])
		assert.Equal(t, "-5m", link["spanStartTimeShift"])
		assert.Equal(t, "5m", link["spanEndTimeShift"])
	}
}

func TestNoLinkWithoutBothEndsOrWhenSwitchedOff(t *testing.T) {
	// everything: store-b has no logs, store-c no traces, store-a both.
	ds := readGrafanaDatasources(t, "golden/observability-grafana/everything.yaml")
	assert.Contains(t, ds["store-a-logs"].JSONData, "derivedFields")
	assert.Contains(t, ds["store-a-traces"].JSONData, "tracesToLogsV2")
	assert.NotContains(t, ds["store-b-traces"].JSONData, "tracesToLogsV2", "store-b has no logs datasource to link to")
	assert.NotContains(t, ds["store-c-logs"].JSONData, "derivedFields", "store-c has no traces datasource to link to")

	off := readGrafanaDatasources(t, "golden/observability-grafana/no-correlation.yaml")
	for uid, d := range off {
		assert.NotContains(t, d.JSONData, "derivedFields", uid)
		assert.NotContains(t, d.JSONData, "tracesToLogsV2", uid)
	}
}

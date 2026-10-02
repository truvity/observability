// The dashboard truth pass: panels that could never hold data are gone, the
// queries that matched nothing now match, and a tile no longer invents a zero.
// Each assertion is one defect found by reading every shipped panel's query
// against a real store; a regeneration that brought one back fails here.
package tests

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type truthDashboard struct {
	Title  string      `json:"title"`
	Panels []dashPanel `json:"panels"`
}

func loadTruthDashboard(t *testing.T, name string) truthDashboard {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(dashboardsDir, name+".json"))
	require.NoError(t, err)
	var d truthDashboard
	require.NoError(t, json.Unmarshal(raw, &d), name)
	return d
}

// walkPanels yields every panel, rows' collapsed children included.
func walkPanels(ps []dashPanel, fn func(dashPanel)) {
	for _, p := range ps {
		fn(p)
		walkPanels(p.Panels, fn)
	}
}

func panelTitles(d truthDashboard) map[string]int {
	out := map[string]int{}
	walkPanels(d.Panels, func(p dashPanel) { out[p.Title]++ })
	return out
}

func exprs(d truthDashboard) []string {
	var out []string
	walkPanels(d.Panels, func(p dashPanel) {
		for _, tg := range p.Targets {
			out = append(out, tg.Expr)
		}
	})
	return out
}

func TestDashboardsCarryNoPanelThatCanNeverHoldData(t *testing.T) {
	gone := map[string][]string{
		// kubelet_node_config_error went with dynamic kubelet configuration.
		"kubelet": {"Config Error Count"},
		// Removed from the pinned operator; the watchers stat reads the surviving series.
		"victoriametrics-operator": {"Prometheus Converter Watch events"},
		"victoriametrics-vmagent": {
			// the agent's own series limit, stream aggregation and Kafka are not in use
			"Hourly series limit", "Daily series limit",
			"Matched samples ($instance)", "Dropped samples ($instance)", "Produced samples ($instance)",
			"Flush timeouts ($instance)", "Samples lag 0.99 quantile ($instance)", "Dedup flush duration 0.99 quantile ($instance)",
			"Traffic (bytes)", "Messages in / out", "Producer errors", "Consumer errors",
		},
		// Collectors a virtual machine's node-exporter does not run, and hardware it does not have.
		"node-exporter-full": {
			"Hardware Temperature Monitor", "Hardware Fan Speed", "Cooling Device Utilization", "Power Supply",
			"CPU Frequency Scaling", "Systemd Units State", "Systemd Sockets Current", "Systemd Sockets Accepted",
			"Systemd Sockets Refused", "Processes Detailed States", "PIDs Number and Limit",
			"Threads Number and Limit", "IRQ Detail", "Network Saturation", "Speed",
		},
	}
	for name, titles := range gone {
		have := panelTitles(loadTruthDashboard(t, name))
		for _, title := range titles {
			assert.Zerof(t, have[title], "%s still has the panel %q", name, title)
		}
	}
	// What stays: the neighbours the drops must not take with them.
	assert.NotZero(t, panelTitles(loadTruthDashboard(t, "victoriametrics-vmagent"))["Labels compressor ($instance)"])
	assert.NotZero(t, panelTitles(loadTruthDashboard(t, "node-exporter-full"))["Processes Status"])
	assert.NotZero(t, panelTitles(loadTruthDashboard(t, "node-exporter-full"))["Network Traffic"])
}

func TestDroppedPanelsLeaveNoOverlapOrEmptyRow(t *testing.T) {
	for _, name := range []string{"kubelet", "victoriametrics-operator", "victoriametrics-vmagent", "node-exporter-full"} {
		raw, err := os.ReadFile(filepath.Join(dashboardsDir, name+".json"))
		require.NoError(t, err)
		var d struct {
			Panels []struct {
				Type      string                   `json:"type"`
				Title     string                   `json:"title"`
				Collapsed bool                     `json:"collapsed"`
				GridPos   struct{ X, Y, W, H int } `json:"gridPos"`
				Panels    []struct {
					Title   string                   `json:"title"`
					GridPos struct{ X, Y, W, H int } `json:"gridPos"`
				} `json:"panels"`
			} `json:"panels"`
		}
		require.NoError(t, json.Unmarshal(raw, &d), name)
		type box struct{ X, Y, W, H int }
		overlap := func(a, b box) bool {
			return a.X < b.X+b.W && b.X < a.X+a.W && a.Y < b.Y+b.H && b.Y < a.Y+a.H
		}
		var top []box
		for _, p := range d.Panels {
			if p.Type == "row" && p.Collapsed {
				assert.NotEmptyf(t, p.Panels, "%s: the collapsed row %q has no panel left", name, p.Title)
			}
			b := box(p.GridPos)
			for _, o := range top {
				assert.Falsef(t, overlap(b, o), "%s: %q overlaps another panel", name, p.Title)
			}
			top = append(top, b)
			var kids []box
			for _, c := range p.Panels {
				cb := box(c.GridPos)
				for _, o := range kids {
					assert.Falsef(t, overlap(cb, o), "%s: %q overlaps another panel in row %q", name, c.Title, p.Title)
				}
				kids = append(kids, cb)
			}
		}
	}
}

func TestDashboardQueriesThatMatchedNothingNowMatch(t *testing.T) {
	// The HTTP listener's connection-manager prefix is http or https plus the
	// port; `http-.*` never matched `https-<port>`.
	for _, e := range exprs(loadTruthDashboard(t, "envoy-clusters")) {
		assert.NotContains(t, e, `envoy_http_conn_manager_prefix=~"http-.*"`)
	}
	found := false
	for _, e := range exprs(loadTruthDashboard(t, "envoy-clusters")) {
		found = found || strings.Contains(e, `envoy_http_conn_manager_prefix=~"https?-.*"`)
	}
	assert.True(t, found, "Downstream Network Traffic must select https? prefixes")

	// kube-state-metrics writes no kube_*_labels series while its label
	// allow-list is empty, so the resource counts read the *_created series.
	for _, name := range []string{"k8s-views-global", "k8s-views-namespaces"} {
		for _, e := range exprs(loadTruthDashboard(t, name)) {
			for _, kind := range []string{"namespace", "deployment", "statefulset", "daemonset"} {
				assert.NotContainsf(t, e, "kube_"+kind+"_labels", "%s: %s", name, e)
			}
		}
	}
	for _, e := range exprs(loadTruthDashboard(t, "k8s-views-global")) {
		if strings.Contains(e, "kube_deployment_created") {
			assert.True(t, strings.HasPrefix(e, "count(kube_deployment_created{"), e)
		}
	}

	// VictoriaLogs exports no vm_cache_size_bytes: a sum of nothing emptied the panel.
	for _, e := range exprs(loadTruthDashboard(t, "victorialogs-single")) {
		assert.NotContains(t, e, "vm_cache_size_bytes")
	}
}

func TestFleetOverviewTilesNeverInventAZero(t *testing.T) {
	d := loadTruthDashboard(t, "fleet-overview")
	for _, e := range exprs(d) {
		assert.NotContains(t, e, "vector(0)", "a tile that falls back to vector(0) shows 0 when its source is gone")
	}
	// The two tiles on a series kube-state-metrics writes only while a
	// container waits read their zero off the always-present waiting gauge,
	// so an unscraped source reads No data and a healthy cluster reads 0.
	var seen int
	walkPanels(d.Panels, func(p dashPanel) {
		if p.Title != "CrashLoopBackOff" && p.Title != "ImagePullBackOff" {
			return
		}
		seen++
		require.Len(t, p.Targets, 1)
		e := p.Targets[0].Expr
		assert.Contains(t, e, "kube_pod_container_status_waiting_reason{")
		assert.Contains(t, e, "0 * count(kube_pod_container_status_waiting{")
	})
	assert.Equal(t, 2, seen)
}

func TestCloudNativePGDashboardsHaveDistinctTitles(t *testing.T) {
	assert.Equal(t, "CloudNativePG / Clusters ($cluster)", loadTruthDashboard(t, "cnpg-cluster").Title)
	assert.Equal(t, "CloudNativePG / Operator ($cluster)", loadTruthDashboard(t, "cnpg-operator").Title)
}

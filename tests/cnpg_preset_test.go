// The `cnpg` kube-state-metrics preset and the platform-alerts group that
// reads it: what the preset renders (the config, and a grant of
// get/list/watch on clusters.postgresql.cnpg.io and nothing else), the
// literal MetricsQL of the two alerts read back from their goldens, and
// the `for` behaviour of CNPGClusterNotReady against a Cluster that sits at
// Ready=False for hours, as one did after an automatic failover while every
// instance was up.
package tests

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestCNPGPresetRendersConfigAndNarrowGrant(t *testing.T) {
	var cfg string
	var rules []struct {
		APIGroups []string `yaml:"apiGroups"`
		Resources []string `yaml:"resources"`
		Verbs     []string `yaml:"verbs"`
	}
	for _, doc := range splitDocs(t, "golden/observability-emitters/cnpg-preset.yaml") {
		var o struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Name string `yaml:"name"`
			} `yaml:"metadata"`
			Data  map[string]string `yaml:"data"`
			Rules []struct {
				APIGroups []string `yaml:"apiGroups"`
				Resources []string `yaml:"resources"`
				Verbs     []string `yaml:"verbs"`
			} `yaml:"rules"`
		}
		if yaml.Unmarshal(doc, &o) != nil {
			continue
		}
		if o.Kind == "ConfigMap" && strings.HasSuffix(o.Metadata.Name, "-customresourcestate-config") {
			cfg = o.Data["config.yaml"]
		}
		if o.Kind == "ClusterRole" && strings.HasSuffix(o.Metadata.Name, "-cnpg") {
			rules = o.Rules
		}
	}
	require.NotEmpty(t, cfg, "no customresourcestate ConfigMap in the golden")

	var c struct {
		Spec struct {
			Resources []struct {
				GVK struct {
					Group, Version, Kind string
				} `yaml:"groupVersionKind"`
				MetricNamePrefix *string             `yaml:"metricNamePrefix"`
				Labels           map[string][]string `yaml:"labelsFromPath"`
				Metrics          []struct {
					Name string `yaml:"name"`
					Each struct {
						Type  string `yaml:"type"`
						Gauge struct {
							Path      []string            `yaml:"path"`
							Labels    map[string][]string `yaml:"labelsFromPath"`
							ValueFrom []string            `yaml:"valueFrom"`
						} `yaml:"gauge"`
					} `yaml:"each"`
				} `yaml:"metrics"`
			} `yaml:"resources"`
		} `yaml:"spec"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(cfg), &c))
	require.Len(t, c.Spec.Resources, 2, "only the cnpg preset is on: Cluster and Backup")
	r := c.Spec.Resources[0]
	assert.Equal(t, "postgresql.cnpg.io", r.GVK.Group)
	assert.Equal(t, "v1", r.GVK.Version)
	assert.Equal(t, "Cluster", r.GVK.Kind)
	require.NotNil(t, r.MetricNamePrefix)
	assert.Equal(t, "", *r.MetricNamePrefix)
	assert.Equal(t, []string{"metadata", "namespace"}, r.Labels["namespace"])
	assert.Equal(t, []string{"metadata", "name"}, r.Labels["cluster"])
	assert.Equal(t, []string{"status", "phase"}, r.Labels["phase"])
	require.Len(t, r.Metrics, 3)
	assert.Equal(t, "cnpg_cluster_status_condition", r.Metrics[0].Name)
	assert.Equal(t, "Gauge", r.Metrics[0].Each.Type)
	assert.Equal(t, []string{"status", "conditions"}, r.Metrics[0].Each.Gauge.Path)
	assert.Equal(t, []string{"type"}, r.Metrics[0].Each.Gauge.Labels["type"])
	assert.Equal(t, []string{"status"}, r.Metrics[0].Each.Gauge.ValueFrom)

	// The two scalar gauges the stuck-phase rule compares.
	assert.Equal(t, "cnpg_cluster_instances", r.Metrics[1].Name)
	assert.Equal(t, "Gauge", r.Metrics[1].Each.Type)
	assert.Equal(t, []string{"spec", "instances"}, r.Metrics[1].Each.Gauge.Path)
	assert.Equal(t, "cnpg_cluster_ready_instances", r.Metrics[2].Name)
	assert.Equal(t, "Gauge", r.Metrics[2].Each.Type)
	assert.Equal(t, []string{"status", "readyInstances"}, r.Metrics[2].Each.Gauge.Path)

	// The Backups: one Gauge, the creation time, labelled by cluster and
	// phase and deliberately NOT by Backup name (Backups accumulate).
	bk := c.Spec.Resources[1]
	assert.Equal(t, "postgresql.cnpg.io", bk.GVK.Group)
	assert.Equal(t, "v1", bk.GVK.Version)
	assert.Equal(t, "Backup", bk.GVK.Kind)
	require.NotNil(t, bk.MetricNamePrefix)
	assert.Equal(t, "", *bk.MetricNamePrefix)
	assert.Equal(t, map[string][]string{
		"namespace": {"metadata", "namespace"},
		"cluster":   {"spec", "cluster", "name"},
		"phase":     {"status", "phase"},
	}, bk.Labels, "no name label: one series per (namespace, cluster, phase)")
	require.Len(t, bk.Metrics, 1)
	assert.Equal(t, "cnpg_backup_created_timestamp_seconds", bk.Metrics[0].Name)
	assert.Equal(t, "Gauge", bk.Metrics[0].Each.Type)
	assert.Equal(t, []string{"metadata", "creationTimestamp"}, bk.Metrics[0].Each.Gauge.Path)
	assert.Empty(t, bk.Metrics[0].Each.Gauge.Labels)

	require.Len(t, rules, 1, "the grant is one rule")
	assert.Equal(t, []string{"postgresql.cnpg.io"}, rules[0].APIGroups)
	assert.Equal(t, []string{"clusters", "backups"}, rules[0].Resources)
	assert.Equal(t, []string{"get", "list", "watch"}, rules[0].Verbs)
}

func cnpgRules(t *testing.T, golden string) map[string]struct{ expr, hold string } {
	t.Helper()
	got := map[string]struct{ expr, hold string }{}
	for _, doc := range splitDocs(t, golden) {
		var rule vmRuleDoc
		if yaml.Unmarshal(doc, &rule) != nil || rule.Kind != "VMRule" {
			continue
		}
		for _, g := range rule.Spec.Groups {
			for _, r := range g.Rules {
				got[r.Alert] = struct{ expr, hold string }{strings.Join(strings.Fields(r.Expr), " "), r.For}
			}
		}
	}
	return got
}

func TestCNPGAlertsRenderTheirExpressions(t *testing.T) {
	const notReady = `max by (k8s_cluster_name, namespace, cluster, phase) (` +
		` cnpg_cluster_status_condition{type="Ready"} == 0 )`
	got := cnpgRules(t, "golden/platform-alerts/cnpg-shared-store.yaml")
	require.Contains(t, got, "CNPGClusterNotReady")
	require.Contains(t, got, "CNPGMetricsAbsent")
	assert.Equal(t, notReady, got["CNPGClusterNotReady"].expr)
	assert.Equal(t, "15m", got["CNPGClusterNotReady"].hold)
	assert.Equal(t, absentGuard(`cnpg_cluster_status_condition{type="Ready"}`, ""), got["CNPGMetricsAbsent"].expr)
	assert.Equal(t, "30m", got["CNPGMetricsAbsent"].hold)

	// At the group's own defaults the rule is the same, severity warning.
	d := cnpgRules(t, "golden/platform-alerts/cnpg.yaml")
	assert.Equal(t, "15m", d["CNPGClusterNotReady"].hold)
}

// A Cluster Ready=False in one phase for ten hours (every instance up, so
// no per-instance metric moves) fires once, 15 minutes in and stays firing;
// a failover that settles inside the hold never fires.
func TestCNPGClusterNotReadyHold(t *testing.T) {
	hold, err := time.ParseDuration(cnpgRules(t, "golden/platform-alerts/cnpg.yaml")["CNPGClusterNotReady"].hold)
	require.NoError(t, err)
	const step = time.Minute

	stuck := vmalertHoldWindow(allTrue(600), hold, step)
	requireFiresAfter(t, stuck, int(hold/step))

	brief := vmalertHoldWindow(untilThen(60, 5), hold, step)
	requireNeverFires(t, brief, "a failover that settled within five minutes must not alert")

	justShort := vmalertHoldWindow(untilThen(60, 14), hold, step)
	requireNeverFires(t, justShort, "Ready back before the hold elapsed must not alert")
}

// CNPGClusterPhaseStuck: the phase is not healthy while ready instances equal
// the desired ones (the lost-config-reload signature), held 10m. Read back
// from the golden, then its hold: a phase stuck for hours fires once, ten
// minutes in; a switchover that settles inside the hold never does.
func TestCNPGClusterPhaseStuck(t *testing.T) {
	const stuck = `count by (k8s_cluster_name, namespace, cluster) (` +
		` cnpg_cluster_status_condition{type="Ready", phase!="Cluster in healthy state"} )` +
		` and on (k8s_cluster_name, namespace, cluster) (` +
		` max by (k8s_cluster_name, namespace, cluster) (cnpg_cluster_ready_instances)` +
		` == max by (k8s_cluster_name, namespace, cluster) (cnpg_cluster_instances) )` +
		` and on (k8s_cluster_name, namespace, cluster) (` +
		` max by (k8s_cluster_name, namespace, cluster) (cnpg_cluster_instances) > 0 )`
	got := cnpgRules(t, "golden/platform-alerts/cnpg-shared-store.yaml")
	require.Contains(t, got, "CNPGClusterPhaseStuck")
	assert.Equal(t, stuck, got["CNPGClusterPhaseStuck"].expr)
	assert.Equal(t, "10m", got["CNPGClusterPhaseStuck"].hold)

	d := cnpgRules(t, "golden/platform-alerts/cnpg.yaml")
	require.Contains(t, d, "CNPGClusterPhaseStuck")
	assert.Equal(t, "10m", d["CNPGClusterPhaseStuck"].hold)

	hold, err := time.ParseDuration(d["CNPGClusterPhaseStuck"].hold)
	require.NoError(t, err)
	const step = time.Minute

	requireFiresAfter(t, vmalertHoldWindow(allTrue(600), hold, step), int(hold/step))
	requireNeverFires(t, vmalertHoldWindow(untilThen(60, 5), hold, step),
		"a switchover that settled within five minutes must not alert")
	requireNeverFires(t, vmalertHoldWindow(untilThen(60, 9), hold, step),
		"a phase back to healthy before the hold elapsed must not alert")
}

// The three rules that read Backups and node taints, read back from the
// shared-store golden: the exact MetricsQL, the hold and the runbook anchor
// in the CloudNativePG runbooks. hack/ has no proof for them; the joins were
// evaluated against victoria-metrics v1.152.0 with -dedup.minScrapeInterval
// (duplicate Backup series of one cluster and phase keep the largest value)
// when they were written.
func TestCNPGBackupAndDisruptionRules(t *testing.T) {
	const failed = `( max by (k8s_cluster_name, namespace, cluster) (cnpg_backup_created_timestamp_seconds{phase=~"failed|walArchivingFailing"})` +
		` > on (k8s_cluster_name, namespace, cluster)` +
		` max by (k8s_cluster_name, namespace, cluster) (cnpg_backup_created_timestamp_seconds{phase="completed"}) )` +
		` or` +
		` ( max by (k8s_cluster_name, namespace, cluster) (cnpg_backup_created_timestamp_seconds{phase=~"failed|walArchivingFailing"})` +
		` unless on (k8s_cluster_name, namespace, cluster)` +
		` cnpg_backup_created_timestamp_seconds{phase="completed"} )`
	const stuck = `time() - max by (k8s_cluster_name, namespace, cluster) (` +
		` cnpg_backup_created_timestamp_seconds{phase=~"|pending|started|running|finalizing"} ) > 7200`
	const disrupted = `label_replace( ( max by (k8s_cluster_name, namespace, created_by_name, pod, node) (` +
		` kube_pod_info{created_by_kind="Cluster", node!=""} )` +
		` * on (k8s_cluster_name, node) group_left()` +
		` max by (k8s_cluster_name, node) (kube_node_spec_taint{key="karpenter.sh/disrupted"}) )` +
		` and on (k8s_cluster_name, namespace, created_by_name)` +
		` label_replace( max by (k8s_cluster_name, namespace, cluster) (cnpg_cluster_instances),` +
		` "created_by_name", "$1", "cluster", "(.+)" ), "cluster", "$1", "created_by_name", "(.+)" )`

	got := cnpgRules(t, "golden/platform-alerts/cnpg-shared-store.yaml")
	for name, want := range map[string]struct{ expr, hold string }{
		"CNPGBackupFailed":            {failed, "10m"},
		"CNPGBackupStuck":             {stuck, "5m"},
		"CNPGInstanceOnDisruptedNode": {disrupted, "30m"},
	} {
		require.Contains(t, got, name)
		assert.Equal(t, want.expr, got[name].expr, name)
		assert.Equal(t, want.hold, got[name].hold, name)
	}

	// Each links to its section of the public CloudNativePG runbooks.
	links := map[string]string{}
	for _, doc := range splitDocs(t, "golden/platform-alerts/cnpg.yaml") {
		var rule vmRuleDoc
		if yaml.Unmarshal(doc, &rule) != nil || rule.Kind != "VMRule" {
			continue
		}
		var raw struct {
			Spec struct {
				Groups []struct {
					Rules []struct {
						Alert       string            `yaml:"alert"`
						Annotations map[string]string `yaml:"annotations"`
					} `yaml:"rules"`
				} `yaml:"groups"`
			} `yaml:"spec"`
		}
		require.NoError(t, yaml.Unmarshal(doc, &raw))
		for _, g := range raw.Spec.Groups {
			for _, r := range g.Rules {
				links[r.Alert] = r.Annotations["runbook_url"]
			}
		}
	}
	for _, a := range []string{"CNPGBackupFailed", "CNPGBackupStuck", "CNPGInstanceOnDisruptedNode"} {
		assert.Equal(t, "https://github.com/truvity/cnpg/blob/master/docs/runbooks.md#"+strings.ToLower(a), links[a], a)
	}

	// The node rule's hold: a node drained within half an hour (the
	// instance moved, or Karpenter gave up) never fires; one stuck for hours
	// fires once, thirty minutes in.
	hold, err := time.ParseDuration(got["CNPGInstanceOnDisruptedNode"].hold)
	require.NoError(t, err)
	const step = time.Minute
	requireFiresAfter(t, vmalertHoldWindow(allTrue(300), hold, step), int(hold/step))
	requireNeverFires(t, vmalertHoldWindow(untilThen(120, 25), hold, step),
		"an instance moved off within 25 minutes must not alert")
}

// The restore-drill rules, read back from the shared-store golden: the exact
// MetricsQL, the holds and the runbook anchors (the two sections truvity/cnpg
// has; NeverRun links to the Overdue one), then the behaviour: a failed drill
// fires once its hold elapses and stays firing (the gauge persists until the
// next run); a passing one never does; a success older than 35 days is
// overdue (the 35-day threshold is in the asserted expression).
func TestCNPGRestoreDrillRules(t *testing.T) {
	const failed = `cnpg_restore_drill_last_result == 0`
	const overdue = `( time() - cnpg_restore_drill_last_success_timestamp_seconds > 3024000 )` +
		` and on (k8s_cluster_name, namespace, cluster) cnpg_restore_drill_last_run_timestamp_seconds`
	const neverRun = `( cnpg_restore_drill_configured unless on (k8s_cluster_name, namespace, cluster)` +
		` cnpg_restore_drill_last_run_timestamp_seconds )` +
		` and on (k8s_cluster_name, namespace, cluster)` +
		` ( lifetime(cnpg_restore_drill_configured[36d]) > 3024000 )`
	got := cnpgRules(t, "golden/platform-alerts/cnpg-shared-store.yaml")
	for name, want := range map[string]struct{ expr, hold string }{
		"CNPGRestoreDrillFailed":   {failed, "10m"},
		"CNPGRestoreDrillOverdue":  {overdue, "1h"},
		"CNPGRestoreDrillNeverRun": {neverRun, "1h"},
	} {
		require.Contains(t, got, name)
		assert.Equal(t, want.expr, got[name].expr, name)
		assert.Equal(t, want.hold, got[name].hold, name)
	}

	links := map[string]string{}
	sev := map[string]string{}
	for _, doc := range splitDocs(t, "golden/platform-alerts/cnpg.yaml") {
		var raw struct {
			Spec struct {
				Groups []struct {
					Rules []struct {
						Alert       string            `yaml:"alert"`
						Labels      map[string]string `yaml:"labels"`
						Annotations map[string]string `yaml:"annotations"`
					} `yaml:"rules"`
				} `yaml:"groups"`
			} `yaml:"spec"`
		}
		if yaml.Unmarshal(doc, &raw) != nil {
			continue
		}
		for _, g := range raw.Spec.Groups {
			for _, r := range g.Rules {
				links[r.Alert] = r.Annotations["runbook_url"]
				sev[r.Alert] = r.Labels["severity"]
			}
		}
	}
	const base = "https://github.com/truvity/cnpg/blob/master/docs/runbooks.md#"
	assert.Equal(t, base+"cnpgrestoredrillfailed", links["CNPGRestoreDrillFailed"])
	assert.Equal(t, base+"cnpgrestoredrilloverdue", links["CNPGRestoreDrillOverdue"])
	assert.Equal(t, base+"cnpgrestoredrilloverdue", links["CNPGRestoreDrillNeverRun"])
	for _, a := range []string{"CNPGRestoreDrillFailed", "CNPGRestoreDrillOverdue", "CNPGRestoreDrillNeverRun"} {
		assert.Equal(t, "warning", sev[a], a)
	}

	const step = time.Minute
	hold, err := time.ParseDuration(got["CNPGRestoreDrillFailed"].hold)
	require.NoError(t, err)
	// last_result 0 for a month: fires after the hold, once.
	requireFiresAfter(t, vmalertHoldWindow(allTrue(600), hold, step), int(hold/step))
	// last_result 1: the expression is never true.
	requireNeverFires(t, vmalertHoldWindow(make([]bool, 600), hold, step), "a passing drill must not alert")
	// A scrape blip shorter than the hold.
	requireNeverFires(t, vmalertHoldWindow(untilThen(60, 5), hold, step), "a blip must not alert")
}

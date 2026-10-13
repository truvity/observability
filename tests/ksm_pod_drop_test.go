// `metrics.scrape.kubeStateMetricsDrop` — the default drop of the pod-level
// kube-state-metrics series of CI namespaces — run through a REAL relabel
// implementation. The rules are read off the rendered VMAgent's
// `globalScrapeMetricRelabelConfigs` in the goldens, never copied into the
// test. Series are shaped as they arrive at the global stage: under
// `honor_labels: false` the object's namespace is `exported_namespace` and
// `namespace` is the kube-state-metrics pod's own.
package tests

import (
	"testing"

	"github.com/prometheus/common/model"
	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/model/relabel"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func globalScrapeRelabelConfigs(t *testing.T, goldenPath string) []*relabel.Config {
	t.Helper()
	agent := findDoc(t, renderedDocs(t, goldenPath), "VMAgent", nil)
	spec, ok := agent["spec"].(map[string]any)
	require.True(t, ok, "VMAgent has no spec")
	raw, ok := spec["globalScrapeMetricRelabelConfigs"].([]any)
	require.True(t, ok, "VMAgent has no globalScrapeMetricRelabelConfigs")
	var cfgs []*relabel.Config
	for i, item := range raw {
		m, ok := item.(map[string]any)
		require.Truef(t, ok, "globalScrapeMetricRelabelConfigs[%d] is not a mapping", i)
		b, err := yaml.Marshal(k8sRelabelFieldsToPrometheusFields(m))
		require.NoError(t, err)
		var cfg relabel.Config
		require.NoErrorf(t, yaml.Unmarshal(b, &cfg), "decoding globalScrapeMetricRelabelConfigs[%d]: %s", i, string(b))
		cfg.NameValidationScheme = model.LegacyValidation
		cfgs = append(cfgs, &cfg)
	}
	return cfgs
}

func ksmSeries(cfgs []*relabel.Config, name, objectNS string) (map[string]string, bool) {
	lb := labels.NewBuilder(labels.FromMap(map[string]string{
		"__name__": name, "namespace": "observability", "exported_namespace": objectNS, "pod": "ksm-0", "exported_pod": "p",
	}))
	keep := relabel.ProcessBuilder(lb, cfgs...)
	return lb.Labels().Map(), keep
}

func TestKubeStateMetricsPodDropDefault(t *testing.T) {
	cfgs := globalScrapeRelabelConfigs(t, "golden/observability-emitters/minimal.yaml")
	for _, ns := range []string{"arc-runners-org", "ci-build-1"} {
		for _, dropped := range []string{
			"kube_pod_status_reason", "kube_pod_status_phase", "kube_pod_info", "kube_pod_labels", "kube_pod_owner",
			"kube_pod_container_info", "kube_pod_container_resource_requests", "kube_pod_tolerations", "kube_pod_scheduler",
		} {
			_, keep := ksmSeries(cfgs, dropped, ns)
			assert.Falsef(t, keep, "%s in %s is dropped", dropped, ns)
		}
		for _, kept := range []string{
			"kube_pod_status_unschedulable", "kube_pod_container_status_last_terminated_reason",
			"kube_pod_container_status_restarts_total",
		} {
			got, keep := ksmSeries(cfgs, kept, ns)
			require.Truef(t, keep, "%s in %s is on the allowlist", kept, ns)
			_, scratch := got["__ksm_keep_ephemeral__"]
			assert.False(t, scratch, "scratch label must not leak")
		}
		// Not a pod-level family: untouched even in a CI namespace.
		_, keep := ksmSeries(cfgs, "kube_job_status_failed", ns)
		assert.True(t, keep)
	}
	// Other namespaces, and the near-miss names, are untouched.
	for _, ns := range []string{"team-a", "observability", "my-ci-x", "arc-runners", "xci-1"} {
		for _, name := range []string{"kube_pod_status_reason", "kube_pod_info", "kube_pod_status_phase"} {
			_, keep := ksmSeries(cfgs, name, ns)
			assert.Truef(t, keep, "%s in %s is untouched", name, ns)
		}
	}
	// Series with no object namespace (cluster scoped) are untouched.
	_, keep := ksmSeries(cfgs, "kube_pod_info", "")
	assert.True(t, keep)
}

func TestKubeStateMetricsPodDropEmptyRestoresOldRender(t *testing.T) {
	cfgs := globalScrapeRelabelConfigs(t, "golden/observability-emitters/churn-drops-empty.yaml")
	_, keep := ksmSeries(cfgs, "kube_pod_status_reason", "ci-build-1")
	assert.True(t, keep, "ephemeralNamespaces: \"\" stores the CI pod series again")
	for _, c := range cfgs {
		assert.NotContains(t, c.TargetLabel, "__ksm_keep", "no rule of the drop is rendered")
	}
}

func ksmSeriesLabels(cfgs []*relabel.Config, labelsIn map[string]string) (map[string]string, bool) {
	lb := labels.NewBuilder(labels.FromMap(labelsIn))
	keep := relabel.ProcessBuilder(lb, cfgs...)
	return lb.Labels().Map(), keep
}

// kube_pod_tolerations is dropped in every namespace by default (nothing
// reads it); `extraMetricNames`/`metricNames: []` give it back.
func TestKubeStateMetricsTolerationsDroppedByDefault(t *testing.T) {
	cfgs := globalScrapeRelabelConfigs(t, "golden/observability-emitters/minimal.yaml")
	for _, ns := range []string{"team-a", "observability"} {
		_, keep := ksmSeries(cfgs, "kube_pod_tolerations", ns)
		assert.Falsef(t, keep, "kube_pod_tolerations in %s is dropped", ns)
		_, keep = ksmSeries(cfgs, "kube_pod_info", ns)
		assert.Truef(t, keep, "kube_pod_info in %s is kept", ns)
	}
}

// The opt-in drop of short-lived Jobs' series.
func TestKubeStateMetricsEphemeralJobs(t *testing.T) {
	cfgs := globalScrapeRelabelConfigs(t, "golden/observability-emitters/ksm-ephemeral-jobs.yaml")
	pod := func(name, pod string) map[string]string {
		return map[string]string{"__name__": name, "namespace": "observability", "pod": "ksm-0", "exported_namespace": "team-a", "exported_pod": pod}
	}
	job := func(name, jobName string) map[string]string {
		return map[string]string{"__name__": name, "namespace": "observability", "pod": "ksm-0", "exported_namespace": "team-a", "job_name": jobName}
	}
	// Pods of a matching Job or CronJob: every kube_pod_* series goes.
	for _, p := range []string{"nightly-backup-29000-abcde", "queue-janitor-xyz12", "nightly-backup-x"} {
		for _, m := range []string{"kube_pod_info", "kube_pod_status_phase", "kube_pod_container_info", "kube_pod_owner"} {
			_, keep := ksmSeriesLabels(cfgs, pod(m, p))
			assert.Falsef(t, keep, "%s of %s is dropped", m, p)
		}
	}
	// Other pods, and near-miss names, are untouched.
	for _, p := range []string{"web-0", "nightly-backups-1", "my-nightly-backup-1", "queue-janitorx"} {
		_, keep := ksmSeriesLabels(cfgs, pod("kube_pod_info", p))
		assert.Truef(t, keep, "kube_pod_info of %s is kept", p)
	}
	// Jobs: only the three the BackupJobFailed alert reads survive.
	for _, j := range []string{"nightly-backup", "nightly-backup-29000", "queue-janitor-29001"} {
		for _, m := range []string{"kube_job_created", "kube_job_owner", "kube_job_status_failed"} {
			got, keep := ksmSeriesLabels(cfgs, job(m, j))
			require.Truef(t, keep, "%s of %s is kept", m, j)
			_, scratch := got["__ksm_keep_job__"]
			assert.False(t, scratch, "scratch label must not leak")
		}
		for _, m := range []string{"kube_job_labels", "kube_job_status_start_time", "kube_job_status_active", "kube_job_failed", "kube_job_info"} {
			_, keep := ksmSeriesLabels(cfgs, job(m, j))
			assert.Falsef(t, keep, "%s of %s is dropped", m, j)
		}
	}
	for _, j := range []string{"other-job", "nightly-backups", "x-nightly-backup"} {
		_, keep := ksmSeriesLabels(cfgs, job("kube_job_labels", j))
		assert.Truef(t, keep, "kube_job_labels of %s is kept", j)
	}
}

// Off by default: the minimal render carries no ephemeral-Job rule.
func TestKubeStateMetricsEphemeralJobsOffByDefault(t *testing.T) {
	cfgs := globalScrapeRelabelConfigs(t, "golden/observability-emitters/minimal.yaml")
	_, keep := ksmSeriesLabels(cfgs, map[string]string{"__name__": "kube_job_labels", "job_name": "nightly-backup", "exported_pod": "nightly-backup-1"})
	assert.True(t, keep)
	for _, c := range cfgs {
		assert.NotContains(t, c.TargetLabel, "__ksm_keep_job__")
	}
}

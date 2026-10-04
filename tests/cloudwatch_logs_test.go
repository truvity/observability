// The CloudWatch reader (`cloudwatchLogs`): an EKS control plane's audit
// log, narrowed to the Pod Security audit violations.
//
// Config-shape tests over the golden render. The processors were also run
// against the pinned collector image (a file receiver in place of the
// CloudWatch one) with one violating and one ordinary audit event: the
// ordinary one was dropped, the violating one came out with the lifted
// `audit.*` fields and the static tenancy stamp.
package tests

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

const (
	cloudwatchGolden = "golden/observability-emitters/cloudwatch-audit.yaml"
	cloudwatchOff    = "golden/observability-emitters/minimal.yaml"
)

func cloudwatchConfig(t *testing.T, docs []map[string]any) map[string]any {
	t.Helper()

	cm := findDoc(t, docs, "ConfigMap", func(d map[string]any) bool {
		_, ok := dig(d, "data", "config.yaml").(string)

		return ok && strings.HasSuffix(dig(d, "metadata", "name").(string), "-cloudwatch")
	})

	var cfg map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(dig(cm, "data", "config.yaml").(string)), &cfg))

	return cfg
}

func TestCloudwatchReaderIsAbsentUnlessEnabled(t *testing.T) {
	for _, d := range renderedDocs(t, cloudwatchOff) {
		name, _ := dig(d, "metadata", "name").(string)
		assert.False(t, strings.HasSuffix(name, "-cloudwatch"), "%v %s", d["kind"], name)
	}
}

func TestCloudwatchReaderIsOneReplicaWithItsOwnServiceAccount(t *testing.T) {
	docs := renderedDocs(t, cloudwatchGolden)

	sts := findDoc(t, docs, "StatefulSet", func(d map[string]any) bool {
		return strings.HasSuffix(dig(d, "metadata", "name").(string), "-cloudwatch")
	})
	// The receiver has no leader election: a second replica reads and
	// writes every event twice.
	assert.Equal(t, 1, dig(sts, "spec", "replicas"))
	assert.Equal(t, "observability-cloudwatch", dig(sts, "spec", "template", "spec", "serviceAccountName"))

	findDoc(t, docs, "ServiceAccount", func(d map[string]any) bool {
		return dig(d, "metadata", "name") == "observability-cloudwatch"
	})
	// It reads AWS, not the Kubernetes API: no ClusterRole is named for it.
	for _, d := range docs {
		if d["kind"] == "ClusterRole" {
			assert.False(t, strings.HasSuffix(dig(d, "metadata", "name").(string), "-cloudwatch"))
		}
	}
}

func TestCloudwatchReaderFiltersFirstAndStampsAStaticNamespace(t *testing.T) {
	cfg := cloudwatchConfig(t, renderedDocs(t, cloudwatchGolden))

	procs := stringList(t, dig(cfg, "service", "pipelines", "logs", "processors"))
	assert.Equal(t, []string{"filter/match", "transform/audit", "transform/tenancy", "batch"}, procs,
		"the filter runs first, so nothing it drops is parsed or queued")
	assert.Equal(t, []string{"awscloudwatch"}, stringList(t, dig(cfg, "service", "pipelines", "logs", "receivers")))
	assert.Equal(t, []string{"otlp_http/logs-central"}, stringList(t, dig(cfg, "service", "pipelines", "logs", "exporters")))

	assert.Equal(t, "file_storage", dig(cfg, "receivers", "awscloudwatch", "storage"))
	assert.Equal(t, []any{"kube-apiserver-audit-"},
		dig(cfg, "receivers", "awscloudwatch", "logs", "groups", "autodiscover", "streams", "prefixes"))

	filter := stringList(t, dig(cfg, "processors", "filter/match", "logs", "log_record"))
	require.Len(t, filter, 1)
	assert.Contains(t, filter[0], `pod-security\\.kubernetes\\.io/audit-violations`)

	var tenancy []string
	for _, c := range dig(cfg, "processors", "transform/tenancy", "log_statements").([]any) {
		tenancy = append(tenancy, stringList(t, dig(c, "statements"))...)
	}
	assert.Contains(t, tenancy, `set(attributes["k8s.cluster.name"], "example-cluster")`)
	assert.Contains(t, tenancy, `set(attributes["k8s.namespace.name"], "kube-audit")`)
	assert.Contains(t, tenancy, `set(attributes["kubernetes.pod_namespace"], "kube-audit")`)
	// never from the event: the namespace an audit event is about is a
	// field, not the scoping key.
	for _, s := range tenancy {
		assert.NotContains(t, s, "objectRef")
	}
}

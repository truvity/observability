// The CloudWatch reader (`cloudwatchLogs`): an EKS control plane's audit
// log, narrowed to the Pod Security audit violations, and (with `matchers`)
// its failed authentications.
//
// Config-shape tests over the golden render. The processors were also run
// against the pinned collector image (a file receiver in place of the
// CloudWatch one) with one violating and one ordinary audit event: the
// ordinary one was dropped, the violating one came out with the lifted
// `audit.*` fields and the static tenancy stamp. The `matchers` render was
// run the same way over 3500 audit events (3000 answered 403, taken from a
// real control plane's log, and 500 that were not, among them a 404 whose
// request body contains the text `"code":403`, a code 4031, and an event
// with no responseStatus) and 20000 authenticator lines: every 403 and
// every `access denied` was kept, everything else dropped, and
// `audit.code` came out as the integer, `authn.*` as the lifted keys.
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
	assert.Equal(t, []string{"filter/match", "transform/audit", "transform/authenticator", "transform/tenancy", "batch"}, procs,
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

const cloudwatchMatchersGolden = "golden/observability-emitters/cloudwatch-auth-failures.yaml"

// `matchers` are more ways in beside `match`: the filter drops an event
// unless it contains the phrase OR satisfies one matcher, and a matcher is
// the AND of its keys.
func TestCloudwatchMatchersAreOredWithMatchAndAndedWithinOne(t *testing.T) {
	cfg := cloudwatchConfig(t, renderedDocs(t, cloudwatchMatchersGolden))

	assert.Equal(t, []any{"kube-apiserver-audit-", "authenticator-"},
		dig(cfg, "receivers", "awscloudwatch", "logs", "groups", "autodiscover", "streams", "prefixes"))

	filter := stringList(t, dig(cfg, "processors", "filter/match", "logs", "log_record"))
	require.Len(t, filter, 1, "one drop condition: the negation of every way in")

	cond := filter[0]
	assert.True(t, strings.HasPrefix(cond, "not ("), cond)
	// `match` still keeps the Pod Security violations, whatever the stream.
	assert.Contains(t, cond, `IsMatch(body.string, "pod-security\\.kubernetes\\.io/audit-violations")`)
	// The authenticator matcher: its stream AND a literal (the literals are
	// alternatives, quoted so `=` and `"` are not regex syntax).
	const stream = `IsMatch(resource.attributes["cloudwatch.log.stream"], `

	assert.Contains(t, cond, stream+`"^authenticator-") and IsMatch(body.string, " level=warning |msg=\"access denied\"")`)
	// The audit matcher: its stream AND a cheap text test on the number,
	// then the parse, so only events that look like a match are parsed.
	assert.Contains(t, cond, stream+`"^kube-apiserver-audit-") and (IsMatch(body.string, "\"code\":(?:401|403)[,}]") and (`+
		`ParseJSON(body.string)["responseStatus"]["code"] == 401 or ParseJSON(body.string)["responseStatus"]["code"] == 403))`)
	assert.Less(t,
		strings.Index(cond, `(?:401|403)`),
		strings.Index(cond, `ParseJSON`),
		"the text test comes before the parse")
}

// The stream a matcher names is the one a transform recognises it by, and
// the fields an alert filters on are lifted from the right stream only.
func TestCloudwatchTransformsLiftTheFieldsTheAuthAlertsReadFromTheirOwnStream(t *testing.T) {
	cfg := cloudwatchConfig(t, renderedDocs(t, cloudwatchMatchersGolden))

	for proc, want := range map[string]struct {
		stream string
		fields []string
	}{
		"transform/audit": {"^kube-apiserver-audit-", []string{
			`set(attributes["eks.log.type"], "audit")`,
			`set(attributes["audit.code"], Int(cache["e"]["responseStatus"]["code"])) where cache["e"]["responseStatus"]["code"] != nil`,
			`set(attributes["audit.user"], cache["e"]["user"]["username"])`,
			`set(attributes["audit.namespace"], cache["e"]["objectRef"]["namespace"])`,
			`set(attributes["audit.source_ip"], cache["e"]["sourceIPs"][0])`,
		}},
		"transform/authenticator": {"^authenticator-", []string{
			`set(attributes["eks.log.type"], "authenticator")`,
			`set(attributes["authn.level"], cache["level"]) where cache["level"] != nil`,
			`set(attributes["authn.msg"], cache["msg"]) where cache["msg"] != nil`,
			`set(attributes["authn.error"], cache["error"]) where cache["error"] != nil`,
		}},
	} {
		stmts := dig(cfg, "processors", proc, "log_statements").([]any)
		require.Len(t, stmts, 1, proc)

		conds := stringList(t, dig(stmts[0], "conditions"))
		require.Len(t, conds, 1, proc)
		assert.Contains(t, conds[0], want.stream, proc)

		got := stringList(t, dig(stmts[0], "statements"))
		for _, f := range want.fields {
			assert.Contains(t, got, f, proc)
		}
	}
}

// With no `matchers` the filter is the one phrase, as before.
func TestCloudwatchWithoutMatchersKeepsTheSinglePhraseCondition(t *testing.T) {
	cfg := cloudwatchConfig(t, renderedDocs(t, cloudwatchGolden))

	filter := stringList(t, dig(cfg, "processors", "filter/match", "logs", "log_record"))
	require.Len(t, filter, 1)
	assert.True(t, strings.HasPrefix(filter[0], "not IsMatch(body.string,"), filter[0])
}

const cloudwatchInsightsGolden = "golden/observability-emitters/cloudwatch-insights.yaml"

// `readMode: insights` reads a log group of the Infrequent Access class with
// Logs Insights: a second container posts what the queries return into the
// collector, whose processors are unchanged, and the `awscloudwatch` receiver
// is not configured. The default mode renders as it always did (the other
// goldens do not move).
func TestCloudwatchInsightsModeReplacesTheReceiverAndKeepsTheProcessors(t *testing.T) {
	docs := renderedDocs(t, cloudwatchInsightsGolden)
	cfg := cloudwatchConfig(t, docs)

	assert.Nil(t, dig(cfg, "receivers", "awscloudwatch"))
	assert.Equal(t, "127.0.0.1:4318", dig(cfg, "receivers", "otlp", "protocols", "http", "endpoint"),
		"loopback only: nothing but the sidecar may write into the pipeline")
	assert.Equal(t, []string{"otlp"}, stringList(t, dig(cfg, "service", "pipelines", "logs", "receivers")))

	// The same matching and lifting as the filter mode's render.
	filterCfg := cloudwatchConfig(t, renderedDocs(t, cloudwatchMatchersGolden))
	assert.Equal(t, dig(filterCfg, "processors"), dig(cfg, "processors"))
	assert.Equal(t, dig(filterCfg, "service", "pipelines", "logs", "processors"), dig(cfg, "service", "pipelines", "logs", "processors"))

	sts := findDoc(t, docs, "StatefulSet", func(d map[string]any) bool {
		return strings.HasSuffix(dig(d, "metadata", "name").(string), "-cloudwatch")
	})
	assert.Equal(t, 1, dig(sts, "spec", "replicas"))

	containers, ok := dig(sts, "spec", "template", "spec", "containers").([]any)
	require.True(t, ok)
	require.Len(t, containers, 2)

	reader := containers[1].(map[string]any)
	assert.Equal(t, "insights-reader", reader["name"])

	args := stringList(t, reader["args"])
	assert.Contains(t, args, "--stream-prefix=authenticator-")
	assert.Contains(t, args, "--stream-prefix=kube-apiserver-audit-")
	assert.Contains(t, args, "--overlap=3m")
	assert.Contains(t, args, "--poll-interval=2m")

	// The coarse prefilter is the cheap text half of every way in.
	var filter string

	for _, a := range args {
		if strings.HasPrefix(a, "--query-filter=") {
			filter = a
		}
	}

	assert.Contains(t, filter, `@logStream like /^authenticator-/ and @message like / level=warning |msg="access denied"/`)
	assert.Contains(t, filter, `@logStream like /^kube-apiserver-audit-/ and @message like /"code":(?:401|403)[,}]/`)
	assert.Contains(t, filter, `@message like /pod-security\.kubernetes\.io\/audit-violations/`)
	assert.NotContains(t, filter, "ParseJSON", "the parse stays in the collector")

	// The read position shares the collector's volume, so a restart keeps both.
	mounts, _ := reader["volumeMounts"].([]any)
	require.Len(t, mounts, 1)
	assert.Equal(t, "queue", mounts[0].(map[string]any)["name"])
}

func TestCloudwatchFilterModeHasOneContainer(t *testing.T) {
	sts := findDoc(t, renderedDocs(t, cloudwatchMatchersGolden), "StatefulSet", func(d map[string]any) bool {
		return strings.HasSuffix(dig(d, "metadata", "name").(string), "-cloudwatch")
	})

	containers, _ := dig(sts, "spec", "template", "spec", "containers").([]any)
	assert.Len(t, containers, 1)
}

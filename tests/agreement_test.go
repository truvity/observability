// Package tests holds the checks that span an artifact boundary: what the
// Go library renders, and what the charts render, for the same input.
//
// There are two of those boundaries, and both matter for the same reason.
//
// The first is the proxy's configuration. An estate can hold the grant
// mapping in the proxy's configuration or in the token its issuer mints,
// and pkg/tenancy renders both from one input so they cannot disagree.
// charts/observability-stack renders the same mapping a third time, in
// the operator's spelling — and a third shape is a third place to drift.
// A difference between them is a difference between what a token says a
// person may read and what the proxy lets them read, which nobody notices
// until someone sees data they should not.
//
// The second is the vocabulary. charts/observability-emitters STAMPS the
// cluster, the namespace and the tier on every series, log stream and
// span; pkg/tenancy renders the filters that SELECT on the cluster and
// the namespace. One name per dimension per signal, held in two
// artifacts, and a difference between them is not an error anywhere — it
// is an empty result. So the invariant is proved against the rendered
// output of every writer, for every dimension, and not against a value
// file: a writer is only stamping where it is emitted.
package tests

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/truvity/observability/pkg/tenancy"
)

// The addresses the chart derives from the upstream charts' own naming
// rules, for a release named after the chart in namespace `observability`
// — which is what hack/golden.sh renders. They are written out here
// rather than read back out of the golden, so that a chart which starts
// pointing somewhere else fails this test as well as producing a diff.
const (
	goldenIssuer     = "https://issuer.example"
	goldenAudience   = "example-observability-client"
	goldenMetricsURL = "http://vmsingle-observability-stack-victoria-metrics-k8s-stack.observability.svc:8428"
	goldenLogsURL    = "http://observability-stack-victoria-logs-single-server.observability.svc:9428"
)

// vmUser is the part of the operator's VMUser that carries tenancy.
// Everything else about the object is the chart's business.
type vmUser struct {
	Kind string `yaml:"kind"`
	Spec struct {
		Name string `yaml:"name"`
		JWT  struct {
			OIDC struct {
				Issuer string `yaml:"issuer"`
			} `yaml:"oidc"`
			MatchClaims          map[string]string `yaml:"matchClaims"`
			DefaultVMAccessClaim struct {
				MetricsExtraFilters    []string `yaml:"metricsExtraFilters"`
				LogsExtraStreamFilters []string `yaml:"logsExtraStreamFilters"`
			} `yaml:"defaultVMAccessClaim"`
		} `yaml:"jwt"`
		LoadBalancingPolicy string        `yaml:"load_balancing_policy"`
		RetryStatusCodes    []int         `yaml:"retry_status_codes"`
		TargetRefs          []vmTargetRef `yaml:"targetRefs"`
	} `yaml:"spec"`
}

// vmTargetRef is one route. `query_args` is the part that enforces
// anything: vmauth applies a principal's `vm_access` claim ONLY by
// substituting a placeholder into the route it forwards on, so a route
// without one carries the grant in the manifest and none of it on the
// wire.
type vmTargetRef struct {
	Static struct {
		URL string `yaml:"url"`
	} `yaml:"static"`
	Paths     []string `yaml:"paths"`
	QueryArgs []struct {
		Name   string   `yaml:"name"`
		Values []string `yaml:"values"`
	} `yaml:"query_args"`
}

// urlPrefix rebuilds what the operator writes into vmauth's `url_prefix`
// for this route, which is the shape pkg/tenancy renders directly.
//
// The two artifacts spell the same thing differently — the library
// writes one string, the CRD splits it into an address and a typed
// argument list — so the comparison happens on the reassembled value
// rather than on the raw text of either.
func (r vmTargetRef) urlPrefix() string {
	if len(r.QueryArgs) == 0 {
		return r.Static.URL
	}
	parts := make([]string, 0, len(r.QueryArgs))
	for _, a := range r.QueryArgs {
		for _, v := range a.Values {
			parts = append(parts, a.Name+"="+v)
		}
	}
	return r.Static.URL + "?" + strings.Join(parts, "&")
}

func TestChartAndLibraryRenderTheSameTenancy(t *testing.T) {
	// The principals from the repository's README, and from
	// tests/cases/observability-stack/tenancy/values.yaml. The two are
	// the same list on purpose. No key is set: the chart's defaults and
	// the library's must be the same names, and this is where that is
	// proved.
	cfg := tenancy.Config{
		ClaimName:      "groups",
		Audience:       goldenAudience,
		MetricsBackend: goldenMetricsURL,
		LogsBackend:    goldenLogsURL,
		Principals: []tenancy.Principal{
			{Group: "example:k8s:viewer", Grants: []tenancy.Grant{
				{Cluster: "example-cluster", AllNamespaces: true},
			}},
			{Group: "example:example-app:deployer", Grants: []tenancy.Grant{
				{Cluster: "example-cluster", Namespaces: []string{"example-app"}},
			}},
		},
	}

	want, err := cfg.RenderVMAuth(goldenIssuer)
	require.NoError(t, err)

	got := readVMUsers(t, "golden/observability-stack/tenancy.yaml")
	require.Len(t, got, len(want.Users),
		"the chart renders one VMUser per principal; a count that differs is a principal the proxy will not admit")

	byName := map[string]vmUser{}
	for _, u := range got {
		byName[u.Spec.Name] = u
	}

	for _, user := range want.Users {
		user := user
		t.Run(user.Name, func(t *testing.T) {
			chart, ok := byName[user.Name]
			require.True(t, ok, "the chart renders no VMUser for %q", user.Name)

			assert.Equal(t, user.JWT.OIDC, chart.Spec.JWT.OIDC.Issuer,
				"the proxy would verify tokens against a different issuer than the claim was minted by")
			assert.Equal(t, user.JWT.MatchClaims, chart.Spec.JWT.MatchClaims,
				"a user selected by different claims is a user a token reaches, or does not, for reasons neither side states")

			// And the audience pin, asserted on the chart's own output
			// rather than on the comparison above: both sides dropping
			// it would satisfy that equality and leave a proxy that
			// admits every client of the issuer, because vmauth
			// validates expiry and issuer and checks `aud` nowhere.
			assert.Equal(t, "^("+goldenAudience+")$", chart.Spec.JWT.MatchClaims[tenancy.AudienceClaim],
				"this VMUser carries no audience pin, so any unexpired token from the issuer is admitted whatever client it was minted for")

			// The filters. This is the whole of it: these strings are
			// what the store applies to every query the principal makes.
			assert.Equal(t, user.JWT.DefaultVMAccess.MetricsExtraFilters,
				chart.Spec.JWT.DefaultVMAccessClaim.MetricsExtraFilters,
				"the chart and the library disagree about what this principal may read")
			assert.Equal(t, user.JWT.DefaultVMAccess.LogsExtraStreamFilters,
				chart.Spec.JWT.DefaultVMAccessClaim.LogsExtraStreamFilters,
				"the chart and the library disagree about what this principal may read")

			assert.Equal(t, user.LoadBalancingPol, chart.Spec.LoadBalancingPolicy)
			assert.Equal(t, user.RetryStatusCodes, chart.Spec.RetryStatusCodes)

			require.Len(t, chart.Spec.TargetRefs, len(user.URLMap),
				"one route per backend, in the same order")
			for i, row := range user.URLMap {
				assert.Equal(t, row.URLPrefix[0], chart.Spec.TargetRefs[i].urlPrefix(),
					"the chart and the library send this route somewhere else, or apply a different filter on the way")
				assert.Equal(t, row.SrcPaths, chart.Spec.TargetRefs[i].Paths,
					"a route the chart admits and the library does not is a route nobody reviewed")
			}
		})
	}
}

// The test that would have caught it, on the chart's side.
//
// It walks every route in the rendered VMUser objects and fails on any
// that does not carry the placeholder vmauth substitutes the
// principal's filter into. That substitution is the whole of the
// enforcement: vmauth verifies the token, selects the user, computes the
// `vm_access` claim — and then, on a route with no placeholder, throws
// it away and forwards the request unfiltered.
//
// What makes that defect survive review is that the two cases are
// identical everywhere except in the answer to a question nobody asks.
// The manifest shows the grant either way. The install succeeds either
// way. The proxy is healthy either way. And a query for the ONE
// namespace the reviewer has data for returns the same rows either way,
// because the filter that was not applied would not have removed
// anything. It takes a second namespace, or this assertion, to tell them
// apart.
func TestEveryRenderedReadRouteCarriesItsFilter(t *testing.T) {
	for _, user := range readVMUsers(t, "golden/observability-stack/tenancy.yaml") {
		require.NotEmpty(t, user.Spec.TargetRefs)
		for i, ref := range user.Spec.TargetRefs {
			assert.True(t, carriesAFilter(ref),
				"route %d of %q (%s) forwards with no filter argument: every query it carries reaches the store unscoped, while `defaultVMAccessClaim` on the same object still states the grant",
				i, user.Spec.Name, strings.Join(ref.Paths, " "))
		}
	}
}

// And the one signal that cannot carry one, held in its own render so
// that the exception is a file somebody has to change rather than a
// case this test quietly tolerates.
//
// tests/cases/observability-stack/tenancy-traces sets
// `allowUnfilteredTraceReads`, which is the only way the trace route
// renders at all — see
// tests/invalid/observability-stack/traces-without-unfiltered-optin.yaml
// for the refusal when it is not set.
func TestOnlyTheTraceRouteIsUnfiltered(t *testing.T) {
	for _, user := range readVMUsers(t, "golden/observability-stack/tenancy-traces.yaml") {
		require.Len(t, user.Spec.TargetRefs, 3, "metrics, logs and traces")

		var unfiltered []string
		for _, ref := range user.Spec.TargetRefs {
			if !carriesAFilter(ref) {
				unfiltered = append(unfiltered, strings.Join(ref.Paths, " "))
			}
		}

		require.Len(t, unfiltered, 1,
			"exactly one route in this render may be unfiltered, and it is the trace route; these are: %v", unfiltered)
		assert.Contains(t, unfiltered[0], "/select/jaeger/",
			"the unfiltered route is not the trace route, so a route that could be scoped is not being scoped")
	}
}

// The filter arguments and placeholders the chart renders are the ones
// pkg/tenancy declares.
//
// A chart naming `extra_label` where the library names `extra_filters`,
// or `{{.MetricsExtraLabels}}` where it names `{{.MetricsExtraFilters}}`,
// would render, install, and enforce something other than the grant — or
// nothing at all, since vmauth forwards an unknown placeholder to the
// store as a literal string.
func TestChartUsesTheLibrarysFilterArguments(t *testing.T) {
	want := map[string]string{
		tenancy.MetricsFilterArg: tenancy.MetricsFilterPlaceholder,
		tenancy.LogsFilterArg:    tenancy.LogsFilterPlaceholder,
	}

	seen := map[string]string{}
	for _, user := range readVMUsers(t, "golden/observability-stack/tenancy.yaml") {
		for _, ref := range user.Spec.TargetRefs {
			for _, a := range ref.QueryArgs {
				require.Len(t, a.Values, 1,
					"a route carries one filter argument with one value; vmauth substitutes only a value that IS the placeholder")
				seen[a.Name] = a.Values[0]
			}
		}
	}
	assert.Equal(t, want, seen,
		"the chart enforces with different arguments than the library renders")
}

// carriesAFilter reports whether this route would actually apply a
// principal's grant: one of the library's filter arguments, carrying its
// placeholder as the WHOLE value.
//
// The whole-value part is not pedantry. vmauth substitutes a query
// argument by looking its complete value up in a map, not by replacing a
// substring, so `extra_filters=x{{.MetricsExtraFilters}}` is forwarded
// to the store exactly as written — and the check is the library's own,
// so the chart cannot be judged by a looser rule than the library is.
func carriesAFilter(ref vmTargetRef) bool {
	prefix := ref.urlPrefix()
	for arg, placeholder := range map[string]string{
		tenancy.MetricsFilterArg: tenancy.MetricsFilterPlaceholder,
		tenancy.LogsFilterArg:    tenancy.LogsFilterPlaceholder,
	} {
		if tenancy.CarriesFilter(prefix, arg, placeholder) {
			return true
		}
	}
	return false
}

func readVMUsers(t *testing.T, path string) []vmUser {
	t.Helper()

	raw, err := os.ReadFile(path)
	require.NoError(t, err, "the golden render is the chart's side of this comparison; regenerate it with `just golden`")

	var users []vmUser
	for _, doc := range strings.Split(string(raw), "\n---") {
		var u vmUser
		require.NoError(t, yaml.Unmarshal([]byte(doc), &u))
		if u.Kind == "VMUser" {
			users = append(users, u)
		}
	}
	require.NotEmpty(t, users, "no VMUser in %s", path)
	return users
}

// The second boundary: what the collectors STAMP and what the proxy
// FILTERS ON.
//
// One filter key per dimension per signal; every writer on that signal
// stamps it under that exact name. The names below come from the
// library — the metrics and log keys read back out of a rendered filter
// rather than from a constant, because the filter is what the store
// applies and so the only statement of them that can be wrong; the trace
// attributes and the tier from the constants the library exports, since
// no filter names them.
//
// What is checked is the RENDERED manifest of every writer: the metrics
// agent's relabel rules for both kinds of scrape, the container-log
// agent's flags, and the gateway's three pipelines. Two writer collisions
// exist on the way — a Prometheus label cannot carry a dot, and the
// container-log agent cannot rename a field — and each is closed by the
// flexible writer matching the inflexible one, so the assertion for a
// gateway pipeline is not "it emits the conventional name" but "it emits
// the name the OTHER writer on this signal is stuck with".
type vocabulary struct {
	metricsCluster, metricsNamespace, metricsTier string
	logsCluster, logsNamespace, logsTier          string
	tracesCluster, tracesNamespace, tracesTier    string
}

func libraryVocabulary(t *testing.T) vocabulary {
	t.Helper()
	claim, err := tenancy.Config{
		ClaimName: "groups",
		Principals: []tenancy.Principal{
			{Group: "example:reader", Grants: []tenancy.Grant{{Cluster: "example-cluster", Namespaces: []string{"example-app"}}}},
		},
	}.RenderClaim(tenancy.Principal{
		Group:  "example:reader",
		Grants: []tenancy.Grant{{Cluster: "example-cluster", Namespaces: []string{"example-app"}}},
	})
	require.NoError(t, err)
	require.Len(t, claim.MetricsExtraFilters, 1)
	require.Len(t, claim.LogsExtraStreamFilters, 1)

	// {k8s_cluster_name="example-cluster",k8s_namespace_name=~"^(example-app)$"}
	m := strings.Split(strings.Trim(claim.MetricsExtraFilters[0], "{}"), ",")
	require.Len(t, m, 2, "one cluster matcher and one namespace matcher: %s", claim.MetricsExtraFilters[0])
	// _stream:{"k8s.cluster.name"="example-cluster","kubernetes.pod_namespace"=~"^(example-app)$"}
	l := strings.Split(strings.Trim(strings.TrimPrefix(claim.LogsExtraStreamFilters[0], "_stream:"), "{}"), ",")
	require.Len(t, l, 2, "one cluster matcher and one namespace matcher: %s", claim.LogsExtraStreamFilters[0])

	key := func(matcher string) string {
		name, _, ok := strings.Cut(matcher, "=")
		require.True(t, ok, "not a matcher: %s", matcher)
		return strings.Trim(name, `"`)
	}
	return vocabulary{
		metricsCluster: key(m[0]), metricsNamespace: key(m[1]), metricsTier: tenancy.EnvironmentLabel,
		logsCluster: key(l[0]), logsNamespace: key(l[1]), logsTier: tenancy.EnvironmentAttribute,
		tracesCluster: tenancy.TracesClusterAttribute, tracesNamespace: tenancy.TracesNamespaceAttribute, tracesTier: tenancy.EnvironmentAttribute,
	}
}

// The values in tests/cases/observability-emitters/minimal, which the
// stamps below must carry. Written out rather than read from the values
// file so that a chart which stops plumbing a value fails here and not
// only in the golden diff.
const (
	minimalCluster = "example-cluster"
	minimalTier    = "development"
)

func TestEveryWriterStampsEveryDimensionUnderTheLibrarysName(t *testing.T) {
	v := libraryVocabulary(t)
	docs := renderedDocs(t, "golden/observability-emitters/minimal.yaml")

	t.Run("metrics/the metrics agent", func(t *testing.T) {
		agent := vmAgent(t, docs)

		// Every scrape object this chart does not own — which is all of
		// them — goes through the default scrape class.
		class := agent.defaultScrapeClass(t)
		assert.Equal(t, minimalCluster, class.replacementFor(v.metricsCluster),
			"the default scrape class does not stamp the cluster under the name the filters select on")
		assert.Equal(t, minimalTier, class.replacementFor(v.metricsTier))
		assert.Equal(t, []string{"__meta_kubernetes_namespace"}, class.sourceFor(v.metricsNamespace),
			"the namespace key has to come from service discovery — the namespace the pod is IN — not from anything the target exported")

		// And the two node-level jobs, which have no namespace in service
		// discovery, stamp the cluster and the tier on the target and copy
		// the container's own namespace into the key after the scrape.
		jobs := agent.inlineJobs(t)
		require.NotEmpty(t, jobs)
		for _, job := range jobs {
			assert.Equal(t, minimalCluster, job.relabel.replacementFor(v.metricsCluster), "job %s", job.name)
			assert.Equal(t, minimalTier, job.relabel.replacementFor(v.metricsTier), "job %s", job.name)
			assert.Equal(t, []string{"namespace"}, job.metricRelabel.sourceFor(v.metricsNamespace),
				"job %s: a container series from the node agent carries `namespace`, and a grant on that namespace should reach it", job.name)
		}
	})

	t.Run("metrics/the gateway", func(t *testing.T) {
		cfg := gatewayConfig(t, docs)
		exporters := pipelineExporters(t, cfg, "metrics")
		require.NotEmpty(t, exporters)
		for _, name := range exporters {
			// A resource attribute becomes a label only when the exporter
			// is told to promote it, and on the way out the exporter
			// spells it with underscores. So the check is: the dotted
			// attribute is in the promoted list, and its underscore
			// spelling is the label the filter selects on.
			promoted := stringList(t, dig(cfg, "exporters", name, "resource_constant_labels", "included"))
			for _, want := range []struct{ label string }{{v.metricsCluster}, {v.metricsNamespace}, {v.metricsTier}} {
				var found bool
				for _, attr := range promoted {
					if strings.ReplaceAll(attr, ".", "_") == want.label {
						found = true
					}
				}
				assert.True(t, found,
					"exporter %s promotes %v, none of which the remote-write exporter would spell %q — so OTLP-derived series would reach the store without it, and every scoped query would miss them", name, promoted, want.label)
			}
			// And nothing more: the rest of the resource carries the pod
			// UID, and a label that changes per restart is a series that
			// changes per restart.
			assert.Len(t, promoted, 3, "exporter %s promotes more than the three keys: %v", name, promoted)
		}
		// The cluster and tier values come from the values file — the
		// processor that resolves the pod cannot know either.
		statements := transformStatements(t, cfg, "metric")
		assert.Contains(t, statements, `set(attributes["k8s.cluster.name"], "`+minimalCluster+`")`)
		assert.Contains(t, statements, `set(attributes["deployment.environment.name"], "`+minimalTier+`")`)
	})

	t.Run("logs/the container-log agent", func(t *testing.T) {
		args := vlagentArgs(t, docs)
		stream := flagList(t, args, "--kubernetesCollector.streamFields=")
		assert.Contains(t, stream, v.logsCluster,
			"the cluster key is not a stream field, so a stream filter on it selects nothing")
		assert.Contains(t, stream, v.logsNamespace,
			"the namespace key is not a stream field; it is the agent's own default, so somebody removed it")

		extra := flagJSON(t, args, "--kubernetesCollector.extraFields=")
		assert.Equal(t, minimalCluster, extra[v.logsCluster],
			"the cluster reaches the log store as a static field, and this is where it is given")
		assert.Equal(t, minimalTier, extra[v.logsTier])
	})

	t.Run("logs/the gateway", func(t *testing.T) {
		cfg := gatewayConfig(t, docs)
		exporters := pipelineExporters(t, cfg, "logs")
		require.NotEmpty(t, exporters)
		for _, name := range exporters {
			header, _ := dig(cfg, "exporters", name, "headers", "VL-Stream-Fields").(string)
			fields := strings.Split(header, ",")
			assert.Contains(t, fields, v.logsCluster, "exporter %s", name)
			assert.Contains(t, fields, v.logsNamespace,
				"exporter %s: the gateway's half of the log store has to be keyed the way the agent's half is, or one scoped query returns one writer's logs and silently omits the other's", name)
		}
		statements := transformStatements(t, cfg, "log")
		assert.Contains(t, statements, `set(attributes["`+v.logsCluster+`"], "`+minimalCluster+`")`)
		assert.Contains(t, statements, `set(attributes["`+v.logsTier+`"], "`+minimalTier+`")`)
		// The one place a writer yields: the gateway writes the agent's
		// spelling beside its own, because the agent cannot.
		assert.Contains(t, statements,
			`set(attributes["`+v.logsNamespace+`"], attributes["k8s.namespace.name"]) where attributes["k8s.namespace.name"] != nil`,
			"the gateway does not write the namespace under the container-log agent's spelling, and the agent cannot write it under the gateway's")
	})

	t.Run("traces/the gateway", func(t *testing.T) {
		cfg := gatewayConfig(t, docs)
		require.NotEmpty(t, pipelineExporters(t, cfg, "traces"))
		statements := transformStatements(t, cfg, "trace")
		assert.Contains(t, statements, `set(attributes["`+v.tracesCluster+`"], "`+minimalCluster+`")`)
		assert.Contains(t, statements, `set(attributes["`+v.tracesTier+`"], "`+minimalTier+`")`)
		// The namespace on spans is the processor's own extraction, under
		// the conventional name, from the pod object.
		assert.Contains(t, stringList(t, dig(cfg, "processors", "k8sattributes", "extract", "metadata")), v.tracesNamespace)
		assert.Contains(t, stringList(t, dig(cfg, "service", "pipelines", "traces", "processors")), "k8sattributes")
	})
}

// A target that exports its own key must not win. Two mechanisms, both
// asserted on the render: `overrideHonorLabels` so the agent's stamp
// replaces the target's, and a labeldrop for the `exported_` copy the
// agent keeps of a conflicting label — because a series carrying
// `exported_k8s_namespace_name` is a series somebody will eventually
// query by.
func TestATargetCannotExportItsOwnKey(t *testing.T) {
	v := libraryVocabulary(t)
	agent := vmAgent(t, renderedDocs(t, "golden/observability-emitters/minimal.yaml"))

	assert.Equal(t, true, agent.Spec["overrideHonorLabels"])

	var drops []string
	for _, r := range agent.GlobalScrapeMetricRelabelConfigs {
		if r.Action == "labeldrop" {
			drops = append(drops, r.Regex)
		}
	}
	require.NotEmpty(t, drops, "no labeldrop at all")
	for _, label := range []string{v.metricsCluster, v.metricsNamespace, v.metricsTier} {
		var covered bool
		for _, re := range drops {
			if matchesWhole(t, re, "exported_"+label) {
				covered = true
			}
		}
		assert.True(t, covered, "a target exporting its own %q keeps it as exported_%s; no labeldrop covers that", label, label)
	}
}

// The gateway strips the namespace an SDK stated about itself BEFORE the
// processor that resolves the pod runs, because that processor writes an
// attribute only when it is absent. Without the strip, a resource that
// arrived carrying `k8s.namespace.name` would keep the application's
// claim, and the namespace is the key.
func TestTheGatewayDisownsTheNamespaceAnSDKClaims(t *testing.T) {
	v := libraryVocabulary(t)
	cfg := gatewayConfig(t, renderedDocs(t, "golden/observability-emitters/minimal.yaml"))

	for _, signal := range []string{"metrics", "logs", "traces"} {
		processors := stringList(t, dig(cfg, "service", "pipelines", signal, "processors"))
		disown, k8s := indexOf(processors, "transform/disown"), indexOf(processors, "k8sattributes")
		require.NotEqual(t, -1, disown, "%s pipeline has no transform/disown", signal)
		require.NotEqual(t, -1, k8s, "%s pipeline has no k8sattributes", signal)
		assert.Less(t, disown, k8s, "%s pipeline strips the claimed namespace AFTER the processor that only writes an absent one", signal)
	}
	for _, context := range []string{"metric", "log", "trace"} {
		statements := statementsOf(t, cfg, "transform/disown", context)
		assert.Contains(t, statements, `delete_key(attributes, "`+v.tracesNamespace+`")`)
		assert.Contains(t, statements, `delete_key(attributes, "`+v.logsNamespace+`")`)
	}

	// And the pod is resolved from the socket first. The other sources read
	// the pod's identity from attributes the sender supplied.
	sources := dig(cfg, "processors", "k8sattributes", "pod_association").([]any)
	require.NotEmpty(t, sources)
	firstSource := dig(sources[0], "sources").([]any)[0]
	assert.Equal(t, "connection", dig(firstSource, "from"))
}

// The Helm release is navigation: it passes through on every path and is
// never a key and never a stream field.
func TestTheHelmReleasePassesThroughAndIsNeverAStreamField(t *testing.T) {
	docs := renderedDocs(t, "golden/observability-emitters/minimal.yaml")
	const label = "app.kubernetes.io/instance"

	agent := vmAgent(t, docs)
	assert.Equal(t, []string{"__meta_kubernetes_pod_label_app_kubernetes_io_instance"},
		agent.defaultScrapeClass(t).sourceFor("app_kubernetes_io_instance"))

	args := vlagentArgs(t, docs)
	assert.Contains(t, args, "--kubernetesCollector.includePodLabels",
		"pod labels are how the release reaches the log store from the container-log agent")
	assert.NotContains(t, args, "--kubernetesCollector.includePodLabels=false")
	for _, f := range flagList(t, args, "--kubernetesCollector.streamFields=") {
		assert.NotContains(t, f, label, "the release is a stream field on the agent")
	}

	cfg := gatewayConfig(t, docs)
	var extracted bool
	for _, l := range dig(cfg, "processors", "k8sattributes", "extract", "labels").([]any) {
		if dig(l, "key") == label {
			extracted = true
		}
	}
	assert.True(t, extracted, "the gateway does not extract the release label")
	for _, name := range pipelineExporters(t, cfg, "logs") {
		header, _ := dig(cfg, "exporters", name, "headers", "VL-Stream-Fields").(string)
		assert.NotContains(t, header, label, "the release is a stream field on the gateway")
	}
}

// The tier is never a key: no filter the library renders names it, and
// that is a property of the library. What the chart has to hold is the
// other half — that it is stamped everywhere anyway, so a dashboard can
// pin it. Checked above; this pins the name against the constant.
func TestTheTierIsStampedUnderTheConventionalNameAndNeverFiltered(t *testing.T) {
	assert.Equal(t, strings.ReplaceAll(tenancy.EnvironmentAttribute, ".", "_"), tenancy.EnvironmentLabel,
		"the metrics spelling of the tier is the attribute with underscores, which is what the remote-write exporter produces")
	v := libraryVocabulary(t)
	for _, key := range []string{v.metricsCluster, v.metricsNamespace, v.logsCluster, v.logsNamespace} {
		assert.NotEqual(t, tenancy.EnvironmentLabel, key)
		assert.NotEqual(t, tenancy.EnvironmentAttribute, key)
	}
}

// And the values are plumbed, not decorative.
//
// tests/cases/observability-emitters/everything sets a different cluster
// and tier from the minimal case for exactly this: every place the chart
// stamps has to carry those, and none may carry the minimal case's. A
// site that hardcoded one renders identically for everyone whose cluster
// happens to be called that, which is how it survives review.
func TestEmittersValuesReachEverySite(t *testing.T) {
	raw, err := os.ReadFile("golden/observability-emitters/everything.yaml")
	require.NoError(t, err, "regenerate the golden renders with `just golden`")
	golden := string(raw)

	for _, site := range []struct{ what, needle string }{
		{"the metrics agent's cluster stamp", `replacement: "other-cluster"`},
		{"the metrics agent's tier stamp", `replacement: "staging"`},
		{"the gateway's cluster statement", `set(attributes["k8s.cluster.name"], "other-cluster")`},
		{"the gateway's tier statement", `set(attributes["deployment.environment.name"], "staging")`},
		{"the log agent's static fields", `--kubernetesCollector.extraFields={"k8s.cluster.name":"other-cluster","deployment.environment.name":"staging"}`},
	} {
		assert.Contains(t, golden, site.needle,
			"%s does not carry the configured value, so that value is a constant somewhere it should be plumbed", site.what)
	}
	for _, leak := range []string{`"` + minimalCluster + `"`, `"` + minimalTier + `"`} {
		assert.NotContains(t, golden, leak,
			"a stamping site renders another case's value while the values set this one — it would look correct for everyone whose cluster is called that")
	}
}

// ---- reading the rendered manifests

func renderedDocs(t *testing.T, path string) []map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	require.NoError(t, err, "regenerate the golden renders with `just golden`")

	var docs []map[string]any
	dec := yaml.NewDecoder(strings.NewReader(string(raw)))
	for {
		var d map[string]any
		err := dec.Decode(&d)
		if err != nil {
			break
		}
		if d != nil {
			docs = append(docs, d)
		}
	}
	require.NotEmpty(t, docs)
	return docs
}

func findDoc(t *testing.T, docs []map[string]any, kind string, match func(map[string]any) bool) map[string]any {
	t.Helper()
	for _, d := range docs {
		if d["kind"] == kind && (match == nil || match(d)) {
			return d
		}
	}
	t.Fatalf("no %s in the rendered manifest", kind)
	return nil
}

// dig walks nested maps and returns nil where the path does not exist.
func dig(v any, path ...string) any {
	for _, p := range path {
		m, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = m[p]
	}
	return v
}

func stringList(t *testing.T, v any) []string {
	t.Helper()
	list, ok := v.([]any)
	require.True(t, ok, "not a list: %#v", v)
	out := make([]string, 0, len(list))
	for _, e := range list {
		out = append(out, e.(string))
	}
	return out
}

func indexOf(list []string, s string) int {
	for i, e := range list {
		if e == s {
			return i
		}
	}
	return -1
}

func matchesWhole(t *testing.T, re, s string) bool {
	t.Helper()
	// vmagent anchors a relabel regex itself, so the golden carries it
	// unanchored; anchor it here the same way.
	ok, err := regexp.MatchString("^(?:"+re+")$", s)
	require.NoError(t, err)
	return ok
}

// ---- the metrics agent

type relabelRule struct {
	Action       string   `yaml:"action"`
	SourceLabels []string `yaml:"source_labels"`
	TargetLabel  string   `yaml:"target_label"`
	Regex        string   `yaml:"regex"`
	Replacement  string   `yaml:"replacement"`
}

type relabelRules []relabelRule

func (rs relabelRules) replacementFor(target string) string {
	for _, r := range rs {
		if r.TargetLabel == target && len(r.SourceLabels) == 0 {
			return r.Replacement
		}
	}
	return ""
}

func (rs relabelRules) sourceFor(target string) []string {
	for _, r := range rs {
		if r.TargetLabel == target && len(r.SourceLabels) > 0 {
			return r.SourceLabels
		}
	}
	return nil
}

type scrapeClass struct {
	Name           string       `yaml:"name"`
	Default        bool         `yaml:"default"`
	RelabelConfigs relabelRules `yaml:"relabelConfigs"`
}

type inlineJob struct {
	name          string
	relabel       relabelRules
	metricRelabel relabelRules
}

type vmAgentSpec struct {
	Spec                             map[string]any
	ScrapeClasses                    []scrapeClass
	GlobalScrapeMetricRelabelConfigs relabelRules
	InlineScrapeConfig               string
}

func vmAgent(t *testing.T, docs []map[string]any) vmAgentSpec {
	t.Helper()
	doc := findDoc(t, docs, "VMAgent", nil)
	spec, ok := doc["spec"].(map[string]any)
	require.True(t, ok)

	// Round-trip through YAML into typed structs: the golden is untyped
	// and the rules are easier to assert on typed.
	raw, err := yaml.Marshal(spec)
	require.NoError(t, err)
	var typed struct {
		ScrapeClasses                    []scrapeClass `yaml:"scrapeClasses"`
		GlobalScrapeMetricRelabelConfigs relabelRules  `yaml:"globalScrapeMetricRelabelConfigs"`
		InlineScrapeConfig               string        `yaml:"inlineScrapeConfig"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &typed))
	return vmAgentSpec{
		Spec:                             spec,
		ScrapeClasses:                    typed.ScrapeClasses,
		GlobalScrapeMetricRelabelConfigs: typed.GlobalScrapeMetricRelabelConfigs,
		InlineScrapeConfig:               typed.InlineScrapeConfig,
	}
}

func (a vmAgentSpec) defaultScrapeClass(t *testing.T) relabelRules {
	t.Helper()
	for _, c := range a.ScrapeClasses {
		if c.Default {
			return c.RelabelConfigs
		}
	}
	t.Fatal("the metrics agent has no default scrape class")
	return nil
}

func (a vmAgentSpec) inlineJobs(t *testing.T) []inlineJob {
	t.Helper()
	var jobs []struct {
		JobName              string       `yaml:"job_name"`
		RelabelConfigs       relabelRules `yaml:"relabel_configs"`
		MetricRelabelConfigs relabelRules `yaml:"metric_relabel_configs"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(a.InlineScrapeConfig), &jobs))
	out := make([]inlineJob, 0, len(jobs))
	for _, j := range jobs {
		out = append(out, inlineJob{name: j.JobName, relabel: j.RelabelConfigs, metricRelabel: j.MetricRelabelConfigs})
	}
	return out
}

// ---- the container-log agent

func vlagentArgs(t *testing.T, docs []map[string]any) []string {
	t.Helper()
	ds := findDoc(t, docs, "DaemonSet", nil)
	containers := dig(ds, "spec", "template", "spec", "containers").([]any)
	require.NotEmpty(t, containers)
	return stringList(t, dig(containers[0], "args"))
}

// flagList returns the comma-separated value of the first argument that
// starts with prefix.
func flagList(t *testing.T, args []string, prefix string) []string {
	t.Helper()
	for _, a := range args {
		if after, ok := strings.CutPrefix(a, prefix); ok {
			return strings.Split(after, ",")
		}
	}
	t.Fatalf("no %s argument", prefix)
	return nil
}

// flagJSON returns the JSON object that is the value of the first
// argument that starts with prefix.
func flagJSON(t *testing.T, args []string, prefix string) map[string]string {
	t.Helper()
	for _, a := range args {
		if after, ok := strings.CutPrefix(a, prefix); ok {
			var out map[string]string
			require.NoError(t, json.Unmarshal([]byte(after), &out), "%s is not a JSON object: %s", prefix, after)
			return out
		}
	}
	t.Fatalf("no %s argument", prefix)
	return nil
}

// ---- the gateway

func gatewayConfig(t *testing.T, docs []map[string]any) map[string]any {
	t.Helper()
	cm := findDoc(t, docs, "ConfigMap", func(d map[string]any) bool {
		_, ok := dig(d, "data", "config.yaml").(string)
		return ok
	})
	var cfg map[string]any
	require.NoError(t, yaml.Unmarshal([]byte(dig(cm, "data", "config.yaml").(string)), &cfg))
	return cfg
}

func pipelineExporters(t *testing.T, cfg map[string]any, signal string) []string {
	t.Helper()
	v := dig(cfg, "service", "pipelines", signal, "exporters")
	if v == nil {
		return nil
	}
	return stringList(t, v)
}

// transformStatements flattens every statement of transform/tenancy for
// one signal, across contexts.
func transformStatements(t *testing.T, cfg map[string]any, context string) []string {
	t.Helper()
	return statementsOf(t, cfg, "transform/tenancy", context)
}

func statementsOf(t *testing.T, cfg map[string]any, processor, context string) []string {
	t.Helper()
	groups, ok := dig(cfg, "processors", processor, context+"_statements").([]any)
	require.True(t, ok, "%s has no %s_statements", processor, context)
	var out []string
	for _, g := range groups {
		out = append(out, stringList(t, dig(g, "statements"))...)
	}
	return out
}

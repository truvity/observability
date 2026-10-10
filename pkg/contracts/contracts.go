// Package contracts is the single source for the names and values that
// cross a plane boundary: what a component chart may rely on in any
// cluster, and what the platform may rely on from a component chart.
//
// docs/contracts.md is the human version of this package and carries the
// contract version. The two are changed together.
//
// The package is a leaf: it imports nothing from this module, so that
// pkg/tenancy, pkg/rulecheck and pkg/dashboardlint can re-export from it
// without a cycle. Those packages keep their own exported names as aliases
// of the constants here; the value lives in exactly one place.
//
// Stability: everything exported here is stable unless its comment says
// "Planned". A planned name is reserved and documented but nothing renders
// or enforces it yet; it may not be relied on until the contract version
// in docs/contracts.md marks it stable.
package contracts

// Version is the contract version, kept equal to the version in
// docs/contracts.md (tests/contracts_test.go checks it). It moves only
// when a stable name changes meaning or a planned one becomes stable:
// minor for additions, major for a removal or a change of meaning.
const Version = "1.0"

// Metric label keys. A Prometheus label name cannot carry a dot, so the
// OpenTelemetry names are spelled with underscores on metrics.
const (
	// ClusterLabel names the cluster a series came from. Stamped by the
	// cluster's own collectors and, from the planned per-cluster
	// datasource (R2.2), forced on every read.
	ClusterLabel = "k8s_cluster_name"
	// NamespaceLabel names the namespace a series came from.
	NamespaceLabel = "k8s_namespace_name"
	// EnvironmentLabel carries the environment tier. Descriptive only,
	// never a scoping key.
	EnvironmentLabel = "deployment_environment_name"
	// OwnerLabel is the optional owner derived from the namespace
	// (observability-emitters `tenancy.owners`).
	OwnerLabel = "owner"
)

// Log stream fields and span resource attributes: the other two columns
// of the same vocabulary.
const (
	LogsClusterField   = "k8s.cluster.name"
	LogsNamespaceField = "kubernetes.pod_namespace"

	TracesClusterAttribute   = "k8s.cluster.name"
	TracesNamespaceAttribute = "k8s.namespace.name"

	EnvironmentAttribute = "deployment.environment.name"
)

// Annotation and label keys under the platform's own prefix.
const (
	// KeyPrefix prefixes every label or annotation the platform defines
	// on a Kubernetes object.
	KeyPrefix = "observability.truvity.io/"

	// EvaluatorLabel on a VMRule names the vmalert that evaluates it
	// (observability-stack `vmalert.remoteEvaluators`). Stable.
	EvaluatorLabel = KeyPrefix + "evaluator"
	// RuleTypeLabel on a VMRule says what kind of rule it holds, one of
	// the RuleType values. Planned (restructure step 2).
	RuleTypeLabel = KeyPrefix + "rule-type"
)

// RuleType values for RuleTypeLabel. Planned (restructure step 2).
const (
	// RuleTypeAlert marks a VMRule that holds alerting rules.
	RuleTypeAlert = "alert"
	// RuleTypeRecording marks a VMRule that holds recording rules only.
	RuleTypeRecording = "recording"
)

// Ownership: who writes a rule or a dashboard, and so who answers for it.
// The value is written by the owner and read by the platform.
const (
	// OwnerPlatform: shipped by this repository's charts (the stack's
	// self-alerts, platform-alerts, observability-dashboards).
	OwnerPlatform = "platform"
	// OwnerComponent: shipped by a component chart (truvity/<component>)
	// for the component's own behaviour.
	OwnerComponent = "component"
)

// Logical datasource names. A dashboard refers to these and to no store:
// no store name, no UID, and no cluster filter, because the datasource of
// each cluster forces the cluster label (planned, R2.2).
const (
	DatasourceMetrics = "metrics"
	DatasourceLogs    = "logs"
	DatasourceTraces  = "traces"

	// DatasourceVariable is the name of the dashboard template variable of
	// type `datasource` that every panel must use (pkg/dashboardlint).
	DatasourceVariable = "datasource"
)

// Datasources lists the logical datasource names.
func Datasources() []string {
	return []string{DatasourceMetrics, DatasourceLogs, DatasourceTraces}
}

// IdentityKind is what kind of caller a principal is.
type IdentityKind string

const (
	// IdentityHuman is a person, selected by a group claim on an OIDC
	// token (tenancy.Principal).
	IdentityHuman IdentityKind = "human"
	// IdentityMachine is a workload reading one namespace of one cluster,
	// named by a group of the form <cluster>:<namespace>:<role>
	// (tenancy.MachineReader).
	IdentityMachine IdentityKind = "machine"
	// IdentityStoreConnector is an install's outbound connector reading
	// every namespace of every cluster it serves (tenancy.StoreReader).
	IdentityStoreConnector IdentityKind = "store-connector"
)

// IdentityKinds lists the identity kinds.
func IdentityKinds() []IdentityKind {
	return []IdentityKind{IdentityHuman, IdentityMachine, IdentityStoreConnector}
}

// GroupSeparator separates the three segments of a machine identity's
// group: <cluster>:<namespace>:<role>.
const GroupSeparator = ":"

// AudienceClaim is the token claim an audience pin is rendered under.
const AudienceClaim = "aud"

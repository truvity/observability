// Package tenancy turns "who may read which telemetry" into the two shapes
// that can enforce it, from one input, so the two can never disagree.
//
// The two shapes are:
//
//   - a vmauth configuration, where each principal is a user selected by a
//     claim on its token and given the filters it is entitled to;
//   - the vm_access claim body an OIDC issuer mints into a token, which
//     vmauth then templates into those same filters.
//
// An estate uses one or the other depending on where it would rather hold
// the mapping — in the proxy's configuration, or in the issuer. Rendering
// both from one input is the point: a difference between them is a
// difference between what a token says a person may read and what the
// proxy lets them read, and that is not a difference anyone notices until
// it matters.
//
// The scoping key is the CLUSTER and the NAMESPACE, under OpenTelemetry's
// names. A grant says "these namespaces on this cluster"; a project, a
// team or a product is a derivation from a name to a list of namespaces
// that lives with whoever writes the grants, never a label on the
// telemetry. The environment tier (`deployment.environment.name`) rides
// along on every signal and is deliberately not a key: two clusters can
// share a tier, so a filter on it would select both.
//
// This package knows nothing about any estate. It takes principals and
// grants, validates them, and returns data structures. It reads no files
// and contacts nothing.
package tenancy

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// The vocabulary: what every writer stamps and every filter selects on,
// per signal. These are OpenTelemetry's semantic-convention names, spelled
// the way each store can carry them.
//
// The metrics spellings have underscores because a Prometheus label name
// cannot carry a dot. The log-path namespace field is the container-log
// agent's own native name rather than the convention's, because that
// agent cannot rename a field and the OTLP gateway can — so the gateway
// yields, and emits that spelling on its log pipeline beside the
// conventional one. charts/observability-emitters stamps exactly these;
// tests/agreement_test.go walks its rendered output and fails on any
// writer that spells one differently.
const (
	// DefaultClusterLabel and DefaultNamespaceLabel are the METRICS label
	// keys, and what Config.ClusterLabel and Config.NamespaceLabel mean
	// when empty.
	DefaultClusterLabel   = "k8s_cluster_name"
	DefaultNamespaceLabel = "k8s_namespace_name"

	// DefaultLogsClusterField and DefaultLogsNamespaceField are the LOG
	// stream fields, and what Config.LogsClusterField and
	// Config.LogsNamespaceField mean when empty.
	DefaultLogsClusterField   = "k8s.cluster.name"
	DefaultLogsNamespaceField = "kubernetes.pod_namespace"

	// TracesClusterAttribute and TracesNamespaceAttribute are the span
	// resource attributes. Nothing in this package filters on them — the
	// trace store cannot be scoped, see AllowUnfilteredTraceReads — but
	// they are the third column of the same table, and a writer that
	// spells them differently is caught by the same test.
	TracesClusterAttribute   = "k8s.cluster.name"
	TracesNamespaceAttribute = "k8s.namespace.name"

	// EnvironmentLabel and EnvironmentAttribute carry the environment
	// tier — `production`, `staging`, `development`, `test`, or whatever
	// the estate calls one. Descriptive only: it is never a key, because
	// two clusters can share a tier and a filter on it would select both.
	EnvironmentLabel     = "deployment_environment_name"
	EnvironmentAttribute = "deployment.environment.name"
)

// nameRE is what a cluster or namespace name may look like: the
// Kubernetes DNS-label shape, which is what a namespace name already is.
//
// This is a security boundary, not a style rule. Both names are
// interpolated into a regular expression in the rendered filter, so a name
// containing `|`, `)` or `.*` would not merely look odd — it would widen
// the filter to data the principal is not entitled to. Everything outside
// this shape is refused rather than escaped, because a name that needs
// escaping is a name nobody should have chosen.
var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// labelRE is what a METRICS LABEL KEY may look like: the Prometheus label
// name shape. The defaults carry underscores, which nameRE would refuse,
// and a key is not a value — it is never inside the alternation, so the
// regular-expression metacharacters nameRE exists to keep out are not the
// concern here. What it still refuses is anything a MetricsQL selector
// would read as syntax.
var labelRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// fieldRE is what a LOG FIELD NAME may look like, which is a different
// question from what a namespace may be called and has a different answer.
//
// A namespace name is held to the narrowest shape that can express one. A
// log field name is whatever the log agent produces, and the ones here
// carry dots: `kubernetes.pod_namespace`, `k8s.cluster.name`. Holding them
// to nameRE would refuse every real one, so this shape admits the
// characters a Kubernetes label key and its prefix can contain, and
// nothing else.
//
// What it still refuses is the point. The name is interpolated into a
// stream filter, where `"`, `\`, `{`, `}`, `,`, `=`, `~`, `|`, `(`, `)`,
// `:` and whitespace are all syntax — so a name carrying one of them could
// end the filter early, or open a second alternative beside it, and either
// way the grant would be wider than the one somebody wrote. Those are
// refused here rather than escaped at the point of use, for the same
// reason a namespace name is: a name that needs escaping is a name nobody
// should have chosen. The first character excludes `.`, `/` and `-`
// because LogsQL will not start an unquoted token with one.
var fieldRE = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_./-]*$`)

// Grant is what one principal may read on one cluster.
//
// Namespaces lists them by name. AllNamespaces means every namespace on
// that cluster, present or future.
//
// The two are mutually exclusive and one is required. An empty Namespaces
// list does NOT mean "everything": a list that is empty because a
// derivation produced nothing is the most likely way to widen a grant by
// accident, so it is refused and breadth has to be asked for by name.
//
// There is no project, team or product here on purpose. Those are
// derivations — a name that expands to a list of namespaces — and the
// expansion belongs to whoever writes the grants, where adding a namespace
// to a project is a visible act rather than a label somebody forgets.
type Grant struct {
	Cluster       string   `json:"cluster" yaml:"cluster"`
	Namespaces    []string `json:"namespaces,omitempty" yaml:"namespaces,omitempty"`
	AllNamespaces bool     `json:"allNamespaces,omitempty" yaml:"allNamespaces,omitempty"`
}

// Principal is a named population — a group in the issuer's vocabulary —
// and what it may read.
//
// Group is matched against the token's claim; it is not a display name and
// not an address.
type Principal struct {
	Group  string  `json:"group" yaml:"group"`
	Grants []Grant `json:"grants" yaml:"grants"`
}

// Config is the whole input.
//
// ClaimName is the token claim carrying the principal's groups, e.g.
// "groups". MetricsBackend and LogsBackend are the addresses vmauth
// forwards to; they are the caller's, and this package neither defaults
// nor validates them beyond requiring them to be present when a vmauth
// configuration is rendered.
type Config struct {
	ClaimName      string      `json:"claimName" yaml:"claimName"`
	MetricsBackend string      `json:"metricsBackend,omitempty" yaml:"metricsBackend,omitempty"`
	LogsBackend    string      `json:"logsBackend,omitempty" yaml:"logsBackend,omitempty"`
	TracesBackend  string      `json:"tracesBackend,omitempty" yaml:"tracesBackend,omitempty"`
	Principals     []Principal `json:"principals" yaml:"principals"`

	// ClusterLabel and NamespaceLabel are the label keys the collectors
	// stamp on METRICS. Empty means DefaultClusterLabel and
	// DefaultNamespaceLabel, which is what charts/observability-emitters
	// stamps; they are inputs so that an estate whose collectors were not
	// built here can state what its series actually carry.
	ClusterLabel   string `json:"clusterLabel,omitempty" yaml:"clusterLabel,omitempty"`
	NamespaceLabel string `json:"namespaceLabel,omitempty" yaml:"namespaceLabel,omitempty"`

	// LogsClusterField and LogsNamespaceField are the stream FIELD names
	// the log store carries the same two dimensions under. Empty means
	// DefaultLogsClusterField and DefaultLogsNamespaceField.
	//
	// They are separate inputs from the label keys, and defaulted
	// separately, because the log path cannot carry the metrics names: a
	// Prometheus label has no dots, and the container-log agent has no
	// flag, header or ingest pipeline that renames a field. So the default
	// on this path is what that agent natively writes — the namespace
	// under `kubernetes.pod_namespace`, and the cluster under the
	// conventional name it is given as a static extra field — and the
	// OTLP gateway is configured to write the same spelling on its log
	// pipeline. An earlier version of this package had no default here,
	// because every default was right on one estate and wrong on the
	// next; that was true while the field was derived from an estate's
	// own namespace label, and it stopped being true when the key became
	// the namespace's name, which every estate spells the same way.
	//
	// A filter naming a field the streams do not have matches nothing,
	// and a stream filter that matches nothing is an empty result rather
	// than an error. That is why these still exist as inputs: an estate
	// whose log agent writes something else has to say so here.
	LogsClusterField   string `json:"logsClusterField,omitempty" yaml:"logsClusterField,omitempty"`
	LogsNamespaceField string `json:"logsNamespaceField,omitempty" yaml:"logsNamespaceField,omitempty"`

	// AllowUnfilteredTraceReads admits the trace store's read route even
	// though nothing can scope it, and it has this name because that is
	// what it does.
	//
	// vmauth applies a principal's filters by substituting them into the
	// route, and VictoriaTraces' Jaeger and Tempo select APIs accept no
	// query argument to substitute them into — see TracesRead. So a
	// reader given those paths reads every namespace's spans on every
	// cluster, whatever the claim beside the route says.
	//
	// The option exists because for some estates that is the right
	// trade: one cluster, one team, traces that carry nothing a
	// colleague may not see. It is off by default and has to be written
	// down because the alternative — rendering the route anyway, since it
	// looks like the other two — is the defect this package was fixed to
	// remove, one level down.
	//
	// It admits exactly the trace route. It is not a general override,
	// and no metrics or logs route can be rendered unfiltered by any
	// value of it.
	AllowUnfilteredTraceReads bool `json:"allowUnfilteredTraceReads,omitempty" yaml:"allowUnfilteredTraceReads,omitempty"`
}

func (c Config) clusterLabel() string   { return orDefault(c.ClusterLabel, DefaultClusterLabel) }
func (c Config) namespaceLabel() string { return orDefault(c.NamespaceLabel, DefaultNamespaceLabel) }
func (c Config) logsClusterField() string {
	return orDefault(c.LogsClusterField, DefaultLogsClusterField)
}
func (c Config) logsNamespaceField() string {
	return orDefault(c.LogsNamespaceField, DefaultLogsNamespaceField)
}

// Validate reports every problem it can find, not just the first: a
// derivation that produced three bad names should not have to be run four
// times to learn that.
func (c Config) Validate() error {
	var errs []error

	if c.ClaimName == "" {
		errs = append(errs, errors.New("claimName is empty: nothing would select a principal, so every token would match none of them"))
	}
	if len(c.Principals) == 0 {
		errs = append(errs, errors.New("no principals: the rendered configuration would admit nobody, which is indistinguishable from a broken derivation"))
	}

	errs = append(errs, c.validateKeys()...)

	// The routes are this package's own, so this check does not guard
	// against a caller. It guards against an edit to route.go: a read
	// route that reaches a store without carrying the principal's filter
	// renders cleanly, installs, answers every query and scopes none of
	// them, and there is no later moment at which anything notices.
	for _, binding := range c.readRoutes() {
		if err := binding.route.validate(); err != nil {
			errs = append(errs, err)
		}
	}

	seen := map[string]bool{}
	for i, p := range c.Principals {
		where := fmt.Sprintf("principals[%d]", i)
		if p.Group == "" {
			errs = append(errs, fmt.Errorf("%s: group is empty", where))
		} else {
			if seen[p.Group] {
				errs = append(errs, fmt.Errorf("%s: group %q appears twice; the second entry would be unreachable, so a grant somebody wrote would silently not apply", where, p.Group))
			}
			seen[p.Group] = true
			where = fmt.Sprintf("principal %q", p.Group)
		}

		if len(p.Grants) == 0 {
			errs = append(errs, fmt.Errorf("%s: no grants. A principal that may read nothing is written by leaving it out, not by granting it nothing", where))
		}

		clusters := map[string]bool{}
		for j, g := range p.Grants {
			gw := fmt.Sprintf("%s grant[%d]", where, j)

			switch {
			case g.Cluster == "":
				errs = append(errs, fmt.Errorf("%s: cluster is empty. The cluster is half of the scoping key — a grant is namespaces ON a cluster — and a filter on cluster=\"\" selects the telemetry of no cluster at all, which is an empty result rather than an error", gw))
			case !nameRE.MatchString(g.Cluster):
				errs = append(errs, fmt.Errorf("%s: cluster %q is not a valid name (%s). Names are interpolated into a filter expression, so one outside this shape could widen the grant", gw, g.Cluster, nameRE))
			default:
				if clusters[g.Cluster] {
					errs = append(errs, fmt.Errorf("%s: cluster %q is granted twice to the same principal; merge them, or one grant is silently ignored", gw, g.Cluster))
				}
				clusters[g.Cluster] = true
			}

			switch {
			case g.AllNamespaces && len(g.Namespaces) > 0:
				errs = append(errs, fmt.Errorf("%s: allNamespaces is set and namespaces are listed. One of them is wrong, and guessing which would be how a grant quietly widens", gw))
			case !g.AllNamespaces && len(g.Namespaces) == 0:
				errs = append(errs, fmt.Errorf("%s: no namespaces and allNamespaces is not set. An empty list is refused rather than read as \"everything\": a project that expands to no namespaces is the likeliest way a derivation widens a grant by accident", gw))
			}

			for _, ns := range g.Namespaces {
				if !nameRE.MatchString(ns) {
					errs = append(errs, fmt.Errorf("%s: namespace %q is not a valid name (%s). Names are interpolated into a filter expression, so one outside this shape could widen the grant", gw, ns, nameRE))
				}
			}
			if dup := firstDuplicate(g.Namespaces); dup != "" {
				errs = append(errs, fmt.Errorf("%s: namespace %q is listed twice", gw, dup))
			}
		}
	}

	return errors.Join(errs...)
}

// validateKeys refuses a key the filters would be rendered with that the
// stores could not have.
//
// The messages say why at length on purpose: the failure each one
// prevents produces no error anywhere — a filter selecting on a name the
// telemetry does not carry is an empty result — so somebody reading this
// refusal is the last person who gets to be told.
func (c Config) validateKeys() []error {
	var errs []error

	for key, value := range map[string]string{
		"clusterLabel":   c.ClusterLabel,
		"namespaceLabel": c.NamespaceLabel,
	} {
		if value != "" && !labelRE.MatchString(value) {
			errs = append(errs, fmt.Errorf("%s %q is not a Prometheus label name (%s). This is the key every metrics filter selects on, so it has to be one a series can carry: a Prometheus label name has no dots, no dashes and no slashes. Leave it empty for the OpenTelemetry-derived default, which is what charts/observability-emitters stamps", key, value, labelRE))
		}
	}
	if c.clusterLabel() == c.namespaceLabel() {
		errs = append(errs, fmt.Errorf("clusterLabel and namespaceLabel are both %q. The two are the scoping key, and a filter with one name for both dimensions selects on one of them and ignores the other", c.clusterLabel()))
	}

	for key, value := range map[string]string{
		"logsClusterField":   c.LogsClusterField,
		"logsNamespaceField": c.LogsNamespaceField,
	} {
		if value != "" && !fieldRE.MatchString(value) {
			errs = append(errs, fmt.Errorf("%s %q is not a valid log field name (%s). The name is interpolated into a stream filter, where a quote, a brace, a comma or a `|` is syntax rather than a character — so a name carrying one could end the filter early or open a second alternative beside it, and widen the grant. Such a name is refused rather than escaped. Leave it empty for the default, which is the field the container-log agent natively writes and the gateway is configured to match", key, value, fieldRE))
		}
	}
	if c.logsClusterField() == c.logsNamespaceField() {
		errs = append(errs, fmt.Errorf("logsClusterField and logsNamespaceField are both %q; one stream filter would carry one dimension twice and the other not at all", c.logsClusterField()))
	}

	return errs
}

func firstDuplicate(names []string) string {
	seen := map[string]bool{}
	for _, n := range names {
		if seen[n] {
			return n
		}
		seen[n] = true
	}
	return ""
}

// sortedGrants returns a principal's grants ordered by cluster, so that
// rendering is deterministic and a diff shows a real change rather than a
// map iteration.
func sortedGrants(p Principal) []Grant {
	out := append([]Grant(nil), p.Grants...)
	sort.Slice(out, func(i, j int) bool { return out[i].Cluster < out[j].Cluster })
	for i := range out {
		out[i].Namespaces = append([]string(nil), out[i].Namespaces...)
		sort.Strings(out[i].Namespaces)
	}
	return out
}

// alternation renders names as an anchored regular-expression alternation.
// Every name has already been validated against nameRE, so nothing here
// needs escaping — and if that ever stops being true this is the line that
// makes it a vulnerability, which is why the check lives in Validate and
// not here.
func alternation(names []string) string {
	return "^(" + strings.Join(names, "|") + ")$"
}

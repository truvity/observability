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

// nameRE is what a tenant or environment name may look like: the
// Kubernetes label-value shape, lowercase.
//
// This is a security boundary, not a style rule. Both names are
// interpolated into a regular expression in the rendered filter, so a name
// containing `|`, `)` or `.*` would not merely look odd — it would widen
// the filter to data the principal is not entitled to. Everything outside
// this shape is refused rather than escaped, because a name that needs
// escaping is a name nobody should have chosen.
var nameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// fieldRE is what a LOG FIELD NAME may look like, which is a different
// question from what a tenant may be called and has a different answer.
//
// A tenant name is chosen by whoever writes the grant, so it is held to
// the narrowest shape that can express one. A log field name is not
// chosen at all: it is whatever the log agent produces, and vlagent
// produces `kubernetes.namespace_labels.<kubernetes label key>` — a name
// carrying dots, and a slash whenever the label key has a prefix. Holding
// it to nameRE would refuse every real one, so this shape admits the
// characters a Kubernetes label key and its prefix can contain, and
// nothing else.
//
// What it still refuses is the point. The name is interpolated into a
// stream filter, where `"`, `\`, `{`, `}`, `,`, `=`, `~`, `|`, `(`, `)`,
// `:` and whitespace are all syntax — so a name carrying one of them could
// end the filter early, or open a second alternative beside it, and either
// way the grant would be wider than the one somebody wrote. Those are
// refused here rather than escaped at the point of use, for the same
// reason a tenant name is: a name that needs escaping is a name nobody
// should have chosen. The first character excludes `.`, `/` and `-`
// because LogsQL will not start an unquoted token with one.
var fieldRE = regexp.MustCompile(`^[a-zA-Z0-9_][a-zA-Z0-9_./-]*$`)

// Grant is what one principal may read in one environment.
//
// Tenants lists the tenants by name. AllTenants means every tenant in that
// environment, present or future.
//
// The two are mutually exclusive and one is required. An empty Tenants
// list does NOT mean "everything": a list that is empty because a
// derivation produced nothing is the most likely way to widen a grant by
// accident, so it is refused and breadth has to be asked for by name.
type Grant struct {
	Env        string   `json:"env" yaml:"env"`
	Tenants    []string `json:"tenants,omitempty" yaml:"tenants,omitempty"`
	AllTenants bool     `json:"allTenants,omitempty" yaml:"allTenants,omitempty"`
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

	// TenantLabel and EnvLabel are the label keys the collectors stamp on
	// METRICS and on spans. They are inputs because they are the estate's
	// vocabulary, not this package's. Empty means "tenant" and "env".
	TenantLabel string `json:"tenantLabel,omitempty" yaml:"tenantLabel,omitempty"`
	EnvLabel    string `json:"envLabel,omitempty" yaml:"envLabel,omitempty"`

	// LogsTenantField and LogsEnvField are the FIELD names the log store
	// carries those same two dimensions under, and they are required:
	// there is no default, because every default anyone would write here
	// is wrong somewhere and wrong silently.
	//
	// The log path does not use the label keys above, and it cannot be
	// made to. vlagent delivers a namespace label as
	// `kubernetes.namespace_labels.<key>`, and it has no flag, header or
	// ingest pipeline that renames a field; VictoriaLogs' own `rename` and
	// `copy` pipes run at query time, after an injected stream filter has
	// already been applied, so they cannot rescue it either. A filter
	// naming a field the streams do not have matches nothing, and a stream
	// filter that matches nothing is an empty result rather than an error.
	//
	// So the names are stated by the caller, who is the only one that
	// knows which agent wrote the logs. charts/observability-emitters
	// derives the same pair and refuses to render until they appear in the
	// log agent's own stream fields; docs/safety.md has the rest.
	LogsTenantField string `json:"logsTenantField" yaml:"logsTenantField"`
	LogsEnvField    string `json:"logsEnvField" yaml:"logsEnvField"`

	// AllowUnfilteredTraceReads admits the trace store's read route even
	// though nothing can scope it, and it has this name because that is
	// what it does.
	//
	// vmauth applies a principal's filters by substituting them into the
	// route, and VictoriaTraces' Jaeger and Tempo select APIs accept no
	// query argument to substitute them into — see TracesRead. So a
	// reader given those paths reads every tenant's spans, whatever the
	// claim beside the route says.
	//
	// The option exists because for some estates that is the right
	// trade: one environment, one team, traces that carry nothing a
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

func (c Config) tenantLabel() string {
	if c.TenantLabel == "" {
		return "tenant"
	}
	return c.TenantLabel
}

func (c Config) envLabel() string {
	if c.EnvLabel == "" {
		return "env"
	}
	return c.EnvLabel
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

	if c.TenantLabel != "" && !nameRE.MatchString(c.TenantLabel) {
		errs = append(errs, fmt.Errorf("tenantLabel %q is not a valid label name (%s)", c.TenantLabel, nameRE))
	}
	if c.EnvLabel != "" && !nameRE.MatchString(c.EnvLabel) {
		errs = append(errs, fmt.Errorf("envLabel %q is not a valid label name (%s)", c.EnvLabel, nameRE))
	}

	errs = append(errs, c.validateLogFields()...)

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

		envs := map[string]bool{}
		for j, g := range p.Grants {
			gw := fmt.Sprintf("%s grant[%d]", where, j)

			switch {
			case g.Env == "":
				errs = append(errs, fmt.Errorf("%s: env is empty", gw))
			case !nameRE.MatchString(g.Env):
				errs = append(errs, fmt.Errorf("%s: env %q is not a valid name (%s). Names are interpolated into a filter expression, so one outside this shape could widen the grant", gw, g.Env, nameRE))
			default:
				if envs[g.Env] {
					errs = append(errs, fmt.Errorf("%s: env %q is granted twice to the same principal; merge them, or one grant is silently ignored", gw, g.Env))
				}
				envs[g.Env] = true
			}

			switch {
			case g.AllTenants && len(g.Tenants) > 0:
				errs = append(errs, fmt.Errorf("%s: allTenants is set and tenants are listed. One of them is wrong, and guessing which would be how a grant quietly widens", gw))
			case !g.AllTenants && len(g.Tenants) == 0:
				errs = append(errs, fmt.Errorf("%s: no tenants and allTenants is not set. An empty list is refused rather than read as \"everything\": that is the likeliest way a derivation widens a grant by accident", gw))
			}

			for _, t := range g.Tenants {
				if !nameRE.MatchString(t) {
					errs = append(errs, fmt.Errorf("%s: tenant %q is not a valid name (%s). Names are interpolated into a filter expression, so one outside this shape could widen the grant", gw, t, nameRE))
				}
			}
			if dup := firstDuplicate(g.Tenants); dup != "" {
				errs = append(errs, fmt.Errorf("%s: tenant %q is listed twice", gw, dup))
			}
		}
	}

	return errors.Join(errs...)
}

// validateLogFields refuses a log path that has not been named.
//
// This is the one place where a missing value is refused instead of
// defaulted, and the messages say why at length on purpose: the failure
// it prevents produces no error anywhere, so somebody reading this
// refusal is the last person who gets to be told.
func (c Config) validateLogFields() []error {
	var errs []error

	if c.LogsTenantField == "" {
		errs = append(errs, errors.New(`logsTenantField is empty, and it has no default. On the log path the tenant is not carried in a field named "tenant" and cannot be: vlagent delivers a namespace label as "kubernetes.namespace_labels.<key>" and can rename no field, so a stream filter naming anything else selects a field the streams do not have — which is an empty result rather than an error, and reads as "this tenant writes nothing" to the person who gets it. Name the field the log agent actually writes; charts/observability-emitters derives it and refuses to render until it is in the agent's own streamFields`))
	} else if !fieldRE.MatchString(c.LogsTenantField) {
		errs = append(errs, fmt.Errorf("logsTenantField %q is not a valid log field name (%s). The name is interpolated into a stream filter, where a quote, a brace, a comma or a `|` is syntax rather than a character — so a name carrying one could end the filter early or open a second alternative beside it, and widen the grant. Such a name is refused rather than escaped", c.LogsTenantField, fieldRE))
	}

	if c.LogsEnvField == "" {
		errs = append(errs, errors.New(`logsEnvField is empty, and it has no default for the same reason logsTenantField has none: the log store's field names are the log agent's, not this package's. An agent that adds the environment as a static extra field writes it under the name it was given, and a filter naming any other one returns nothing at all, silently`))
	} else if !fieldRE.MatchString(c.LogsEnvField) {
		errs = append(errs, fmt.Errorf("logsEnvField %q is not a valid log field name (%s). The name is interpolated into a stream filter, so one carrying filter syntax could widen the grant rather than look odd, and is refused rather than escaped", c.LogsEnvField, fieldRE))
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

// sortedGrants returns a principal's grants ordered by environment, so
// that rendering is deterministic and a diff shows a real change rather
// than a map iteration.
func sortedGrants(p Principal) []Grant {
	out := append([]Grant(nil), p.Grants...)
	sort.Slice(out, func(i, j int) bool { return out[i].Env < out[j].Env })
	for i := range out {
		out[i].Tenants = append([]string(nil), out[i].Tenants...)
		sort.Strings(out[i].Tenants)
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

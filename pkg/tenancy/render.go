package tenancy

import (
	"fmt"
	"strconv"
	"strings"
)

// Claim is the vm_access claim body for one principal: what an OIDC issuer
// mints into a token so that vmauth can template the filters out of it.
//
// Only the filter fields are set. Account and project ids are deliberately
// absent: this design scopes a query by label rather than by tenant id,
// because the log and trace stores cannot query across their own tenant
// ids and the fleet-wide question has to stay answerable.
//
// The two lists are not the same list and are not the same length.
// MetricsExtraFilters carries one selector per grant, which vmselect
// OR-s. LogsExtraStreamFilters carries exactly one stream filter for the
// whole principal, because VictoriaLogs AND-s each one it is given — see
// logsFilter. They also name different fields, because the log store
// cannot carry the metrics label keys.
type Claim struct {
	MetricsExtraFilters    []string `json:"metrics_extra_filters,omitempty" yaml:"metrics_extra_filters,omitempty"`
	LogsExtraStreamFilters []string `json:"logs_extra_stream_filters,omitempty" yaml:"logs_extra_stream_filters,omitempty"`
}

// VMAuthUser is one entry in vmauth's `users` list.
type VMAuthUser struct {
	Name             string            `json:"name,omitempty" yaml:"name,omitempty"`
	JWT              *VMAuthJWT        `json:"jwt,omitempty" yaml:"jwt,omitempty"`
	URLMap           []VMAuthURLMapRow `json:"url_map,omitempty" yaml:"url_map,omitempty"`
	RetryStatusCodes []int             `json:"retry_status_codes,omitempty" yaml:"retry_status_codes,omitempty"`
	LoadBalancingPol string            `json:"load_balancing_policy,omitempty" yaml:"load_balancing_policy,omitempty"`
	DefaultVMAccess  *Claim            `json:"default_vm_access_claim,omitempty" yaml:"default_vm_access_claim,omitempty"`
}

// VMAuthJWT selects a user by the claims on its token.
type VMAuthJWT struct {
	OIDC        string            `json:"oidc,omitempty" yaml:"oidc,omitempty"`
	MatchClaims map[string]string `json:"match_claims,omitempty" yaml:"match_claims,omitempty"`
}

// VMAuthURLMapRow routes a set of paths to a backend.
type VMAuthURLMapRow struct {
	SrcPaths  []string `json:"src_paths" yaml:"src_paths"`
	URLPrefix []string `json:"url_prefix" yaml:"url_prefix"`
}

// VMAuthConfig is what vmauth loads.
type VMAuthConfig struct {
	Users []VMAuthUser `json:"users" yaml:"users"`
}

// RenderClaim returns the vm_access claim body for one principal.
//
// The caller has usually validated the whole Config already; this
// validates the principal again rather than trusting that, because a claim
// minted from an unvalidated grant is a filter that may not mean what it
// says.
func (c Config) RenderClaim(p Principal) (Claim, error) {
	if err := (Config{
		ClaimName:       orDefault(c.ClaimName, "groups"),
		TenantLabel:     c.TenantLabel,
		EnvLabel:        c.EnvLabel,
		LogsTenantField: c.LogsTenantField,
		LogsEnvField:    c.LogsEnvField,
		Principals:      []Principal{p},
	}).Validate(); err != nil {
		return Claim{}, fmt.Errorf("rendering claim for %q: %w", p.Group, err)
	}

	grants := sortedGrants(p)

	var claim Claim
	for _, g := range grants {
		claim.MetricsExtraFilters = append(claim.MetricsExtraFilters, c.metricsFilter(g))
	}
	claim.LogsExtraStreamFilters = []string{c.logsFilter(grants)}
	return claim, nil
}

// metricsFilter renders one grant as a MetricsQL series selector, which
// vmauth appends to every query the principal makes.
//
//	{env=~"^(devel)$",tenant=~"^(dms|url-shortener)$"}
//
// A grant of every tenant omits the tenant matcher entirely rather than
// rendering a match-all: a matcher that has to match everything is one
// more place for a mistake to hide, and it would also drop series that
// carry no tenant label at all — which, for an estate mid-rollout, is
// exactly the data someone is looking for.
func (c Config) metricsFilter(g Grant) string {
	parts := []string{fmt.Sprintf("%s=%q", c.envLabel(), g.Env)}
	if !g.AllTenants {
		parts = append(parts, fmt.Sprintf("%s=~%q", c.tenantLabel(), alternation(g.Tenants)))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// logsFilter renders a principal's WHOLE reach as one LogsQL stream
// filter, with the grants as alternatives inside it.
//
//	{"env"="devel","kubernetes.namespace_labels.example.com/project"=~"^(example-app)$" or "env"="prod","kubernetes.namespace_labels.example.com/project"=~"^(example-app|other-app)$"}
//
// Three things differ from the metrics filter, and none of them is a
// matter of taste.
//
// The FIELD NAMES are the caller's log-path names rather than the label
// keys, because the log store cannot be made to carry the label keys. See
// Config.LogsTenantField.
//
// The NAMES ARE QUOTED. A LogsQL word is [a-zA-Z0-9_] and nothing else, so
// a real log field name — which has dots, and a slash when the label key
// has a prefix — is not a word and has to be quoted to be read as one
// name. Quoting is unconditional rather than applied where it looks
// needed: an unquoted name that happens to collide with a keyword or a
// pipe name parses as that keyword, and the shape in fieldRE guarantees
// there is nothing inside the quotes to escape.
//
// And it is ONE filter rather than one per grant. Every
// `extra_stream_filters` argument VictoriaLogs receives is AND-ed into the
// query as a separate global constraint, so a second entry does not widen
// a principal's reach — it narrows it to the intersection, and two grants
// naming two environments intersect in nothing at all. That is an empty
// result for exactly the people with the most access. The metrics path
// takes the opposite convention (vmselect OR-s its `extra_filters`), which
// is why the two claim fields are not the same list and a test asserts
// they are not.
func (c Config) logsFilter(grants []Grant) string {
	alternatives := make([]string, 0, len(grants))
	for _, g := range grants {
		parts := []string{fmt.Sprintf("%s=%q", strconv.Quote(c.LogsEnvField), g.Env)}
		if !g.AllTenants {
			parts = append(parts, fmt.Sprintf("%s=~%q", strconv.Quote(c.LogsTenantField), alternation(g.Tenants)))
		}
		alternatives = append(alternatives, strings.Join(parts, ","))
	}
	// Comma binds tighter than `or` inside `{...}`, so each alternative is
	// its own conjunction and a grant cannot borrow another grant's tenants.
	return "{" + strings.Join(alternatives, " or ") + "}"
}

// MetricsReadPaths, LogsReadPaths and TracesReadPaths are the routes a
// reader is given, and they are deliberately a list of named endpoints
// rather than a prefix.
//
// The tempting shapes are wrong in the same way. `/prometheus/.*` also
// matches `/prometheus/api/v1/write` and
// `/prometheus/api/v1/admin/tsdb/delete_series`; `/api/v1/.*` also
// matches `/api/v1/write` and `/api/v1/import`; `/.*` also matches
// `/internal/force_merge`, whose own authKey flag REPLACES the store's
// `-httpAuth.*` rather than adding to it. A reader's route that also
// accepts writes is not a reader's route, and nothing about it looks
// wrong until somebody uses it.
//
// charts/observability-stack renders these same lists into its VMUser
// objects, and its `tenancy` golden case exists to prove the two have not
// drifted.
var (
	MetricsReadPaths = []string{
		"/prometheus/api/v1/query",
		"/prometheus/api/v1/query_range",
		"/prometheus/api/v1/series",
		"/prometheus/api/v1/labels",
		"/prometheus/api/v1/label/[^/]+/values",
		"/prometheus/api/v1/metadata",
		"/prometheus/api/v1/status/[^/]+",
		"/prometheus/vmui.*",
	}

	LogsReadPaths = []string{
		"/select/logsql/.*",
		"/select/vmui.*",
	}

	TracesReadPaths = []string{
		"/select/jaeger/.*",
		"/select/tempo/.*",
	}
)

// RenderVMAuth returns a vmauth configuration in which each principal is a
// user selected by its group and carrying its own filters.
//
// Backends are required here and not in Validate, because a Config is
// perfectly usable for RenderClaim without them — in the issuer-side
// shape, vmauth learns the backends from its own values file and the
// claim carries only the filters.
func (c Config) RenderVMAuth(issuer string) (VMAuthConfig, error) {
	if err := c.Validate(); err != nil {
		return VMAuthConfig{}, err
	}
	if issuer == "" {
		return VMAuthConfig{}, fmt.Errorf("issuer is empty: vmauth would have no discovery endpoint and could verify no token")
	}
	if c.MetricsBackend == "" || c.LogsBackend == "" {
		return VMAuthConfig{}, fmt.Errorf("metricsBackend and logsBackend are required to render a vmauth configuration")
	}

	out := VMAuthConfig{}
	for _, p := range c.Principals {
		claim, err := c.RenderClaim(p)
		if err != nil {
			return VMAuthConfig{}, err
		}

		rows := []VMAuthURLMapRow{
			{SrcPaths: MetricsReadPaths, URLPrefix: []string{c.MetricsBackend}},
			{SrcPaths: LogsReadPaths, URLPrefix: []string{c.LogsBackend}},
		}
		if c.TracesBackend != "" {
			rows = append(rows, VMAuthURLMapRow{SrcPaths: TracesReadPaths, URLPrefix: []string{c.TracesBackend}})
		}

		out.Users = append(out.Users, VMAuthUser{
			Name: p.Group,
			JWT: &VMAuthJWT{
				OIDC:        issuer,
				MatchClaims: map[string]string{c.ClaimName: p.Group},
			},
			DefaultVMAccess: &claim,
			URLMap:          rows,
			// Reads go to the first healthy backend rather than round-robin:
			// with a redundant pair, a query balanced onto the replica that
			// is still replaying its buffer after a restart returns a gap,
			// and a gap in a dashboard is read as an outage.
			LoadBalancingPol: "first_available",
			RetryStatusCodes: []int{500, 502, 503},
		})
	}
	return out, nil
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

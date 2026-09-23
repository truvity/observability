package tenancy

import (
	"fmt"
	"strings"
)

// Claim is the vm_access claim body for one principal: what an OIDC issuer
// mints into a token so that vmauth can template the filters out of it.
//
// Only the filter fields are set. Account and project ids are deliberately
// absent: this design scopes a query by label rather than by tenant id,
// because the log and trace stores cannot query across their own tenant
// ids and the fleet-wide question has to stay answerable.
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
		ClaimName:   orDefault(c.ClaimName, "groups"),
		TenantLabel: c.TenantLabel,
		EnvLabel:    c.EnvLabel,
		Principals:  []Principal{p},
	}).Validate(); err != nil {
		return Claim{}, fmt.Errorf("rendering claim for %q: %w", p.Group, err)
	}

	var claim Claim
	for _, g := range sortedGrants(p) {
		claim.MetricsExtraFilters = append(claim.MetricsExtraFilters, c.metricsFilter(g))
		claim.LogsExtraStreamFilters = append(claim.LogsExtraStreamFilters, c.logsFilter(g))
	}
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

// logsFilter renders one grant as a LogsQL stream filter.
//
//	{env="devel",tenant=~"^(dms|url-shortener)$"}
//
// The shape is the same as the metrics one by construction. That is worth
// stating: the two stores have different query languages, and the only
// reason one function can serve both is that this design restricts itself
// to equality and alternation on stream labels. A filter that needed more
// than that would have to be written twice, and two hand-written filters
// meaning the same thing is how they stop meaning the same thing.
func (c Config) logsFilter(g Grant) string {
	return c.metricsFilter(g)
}

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
			{SrcPaths: []string{"/prometheus/.*", "/api/v1/query.*", "/api/v1/series", "/api/v1/label.*"}, URLPrefix: []string{c.MetricsBackend}},
			{SrcPaths: []string{"/select/logsql/.*"}, URLPrefix: []string{c.LogsBackend}},
		}
		if c.TracesBackend != "" {
			rows = append(rows, VMAuthURLMapRow{SrcPaths: []string{"/select/jaeger/.*"}, URLPrefix: []string{c.TracesBackend}})
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

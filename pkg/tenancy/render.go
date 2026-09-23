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
}

// VMAuthJWT selects a user by the claims on its token, and says what that
// token is taken to grant when it carries no `vm_access` claim of its own.
//
// DefaultVMAccess belongs HERE and not beside url_map, which is where an
// earlier version of this file put it. vmauth's `UserInfo` has no such
// field, so a configuration with it one level higher does not merely lose
// the default — vmauth refuses to parse the file and exits:
//
//	cannot unmarshal AuthConfig data: yaml: unmarshal errors:
//	  line N: field default_vm_access_claim not found in type main.UserInfo
//
// That is a fail-closed mistake rather than a leak, and the only reason it
// was caught is that somebody ran the rendered file against the binary. No
// golden can tell you that a field a program does not have is a field a
// program does not have.
type VMAuthJWT struct {
	OIDC            string            `json:"oidc,omitempty" yaml:"oidc,omitempty"`
	MatchClaims     map[string]string `json:"match_claims,omitempty" yaml:"match_claims,omitempty"`
	DefaultVMAccess *Claim            `json:"default_vm_access_claim,omitempty" yaml:"default_vm_access_claim,omitempty"`
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
		ClaimName:          orDefault(c.ClaimName, "groups"),
		ClusterLabel:       c.ClusterLabel,
		NamespaceLabel:     c.NamespaceLabel,
		LogsClusterField:   c.LogsClusterField,
		LogsNamespaceField: c.LogsNamespaceField,
		Principals:         []Principal{p},
	}).Validate(); err != nil {
		return Claim{}, fmt.Errorf("rendering claim for %q: %w", p.Group, err)
	}

	grants := sortedGrants(p)

	var claim Claim
	for _, g := range grants {
		claim.MetricsExtraFilters = append(claim.MetricsExtraFilters, c.metricsFilter(g))
	}
	claim.LogsExtraStreamFilters = []string{c.logsFilter(grants)}

	// An empty list here would not be a narrow grant, it would be no
	// grant enforced at all.
	//
	// vmauth substitutes a placeholder with the claim's VALUES, so an
	// empty list expands to nothing and the query argument disappears
	// from the request entirely. The only thing stopping a caller from
	// sending its own `extra_filters` is that such an argument CLASHES
	// with one the route already set — and once the route's argument is
	// gone there is no clash, so the caller's filter is forwarded
	// instead. Refused here rather than checked at the far end, because
	// at the far end it looks like a successful query.
	if len(claim.MetricsExtraFilters) == 0 || len(claim.LogsExtraStreamFilters) == 0 {
		return Claim{}, fmt.Errorf("rendering claim for %q: it carries no filter for one of the signals, which vmauth expands to an ABSENT query argument rather than to a deny — and an absent argument is one the caller may then supply itself", p.Group)
	}
	return claim, nil
}

// metricsFilter renders one grant as a MetricsQL series selector, which
// vmauth appends to every query the principal makes.
//
//	{k8s_cluster_name="example-cluster",k8s_namespace_name=~"^(example-app|other-app)$"}
//
// A grant of every namespace omits the namespace matcher entirely rather
// than rendering a match-all: a matcher that has to match everything is
// one more place for a mistake to hide, and it would also drop series
// that carry no namespace label at all — the node-level series from the
// kubelet, and anything from a cluster-scoped scrape — which for a
// principal allowed the whole cluster is exactly the data they came for.
func (c Config) metricsFilter(g Grant) string {
	parts := []string{fmt.Sprintf("%s=%q", c.clusterLabel(), g.Cluster)}
	if !g.AllNamespaces {
		parts = append(parts, fmt.Sprintf("%s=~%q", c.namespaceLabel(), alternation(g.Namespaces)))
	}
	return "{" + strings.Join(parts, ",") + "}"
}

// logsFilter renders a principal's WHOLE reach as one LogsQL stream
// filter, with the grants as alternatives inside it.
//
//	_stream:{"k8s.cluster.name"="example-cluster","kubernetes.pod_namespace"=~"^(example-app)$" or "k8s.cluster.name"="other-cluster","kubernetes.pod_namespace"=~"^(example-app|other-app)$"}
//
// Three things differ from the metrics filter, and none of them is a
// matter of taste.
//
// The FIELD NAMES are the log path's names rather than the label keys,
// because the log store cannot carry the label keys. See
// Config.LogsNamespaceField.
//
// The NAMES ARE QUOTED. A LogsQL word is [a-zA-Z0-9_] and nothing else, so
// a real log field name — which has dots — is not a word and has to be
// quoted to be read as one name. Quoting is unconditional rather than
// applied where it looks needed: an unquoted name that happens to collide
// with a keyword or a pipe name parses as that keyword, and the shape in
// fieldRE guarantees there is nothing inside the quotes to escape.
//
// And it is ONE filter rather than one per grant. Every
// `extra_stream_filters` argument VictoriaLogs receives is AND-ed into the
// query as a separate global constraint, so a second entry does not widen
// a principal's reach — it narrows it to the intersection, and two grants
// naming two clusters intersect in nothing at all. That is an empty
// result for exactly the people with the most access. The metrics path
// takes the opposite convention (vmselect OR-s its `extra_filters`), which
// is why the two claim fields are not the same list and a test asserts
// they are not.
func (c Config) logsFilter(grants []Grant) string {
	alternatives := make([]string, 0, len(grants))
	for _, g := range grants {
		parts := []string{fmt.Sprintf("%s=%q", strconv.Quote(c.logsClusterField()), g.Cluster)}
		if !g.AllNamespaces {
			parts = append(parts, fmt.Sprintf("%s=~%q", strconv.Quote(c.logsNamespaceField()), alternation(g.Namespaces)))
		}
		alternatives = append(alternatives, strings.Join(parts, ","))
	}
	// Comma binds tighter than `or` inside `{...}`, so each alternative is
	// its own conjunction and a grant cannot borrow another grant's
	// namespaces.
	//
	// `_stream:` is not decoration. VictoriaLogs reads an
	// `extra_stream_filters` argument that begins with `{"` as the JSON
	// object form — `{"field":"value"}` — and every filter this function
	// renders begins with `{"`, because the log store's field names have
	// to be quoted. Without the prefix the value reaches `fastjson.Parse`
	// and comes back as `cannot parse JSON: missing ':' after object
	// key`, which is a 400 on every log query the principal makes. With
	// it, the value does not start with `{"`, VictoriaLogs parses it as
	// LogsQL, and `_stream:{...}` is exactly the stream filter that the
	// bare `{...}` was meant to be.
	return "_stream:{" + strings.Join(alternatives, " or ") + "}"
}

// RenderVMAuth returns a vmauth configuration in which each principal is a
// user selected by its group AND by the audience its token was minted
// for, carrying its own filters, and reaching each store through a route
// that APPLIES them.
//
// The routes themselves live in route.go. What happens here is the
// binding of each one to its backend, and the refusal of any binding that
// would forward a read the claim does not scope.
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
	if c.Audience == "" {
		return VMAuthConfig{}, fmt.Errorf("audience is empty: vmauth validates a token's EXPIRY and, under OIDC discovery, its ISSUER, and nothing else — it has no audience option and never inspects `aud` on its own. Without this pin each user below is selected by its group alone, so ANY unexpired token that issuer minted is admitted whatever client it was minted for: a token the same person holds for a different application of the same issuer reads their namespaces here, and nothing anywhere reports it, because the token verifies and the filters apply. Set it to the client id this proxy's tokens are minted under; it is pinned into every user's `%s` claim, which is the only place vmauth can be made to check it", AudienceClaim)
	}

	// Rendered once, outside the principal loop: every principal gets the
	// same routes, and a refusal is about the estate rather than about a
	// person.
	rows := make([]VMAuthURLMapRow, 0, 3)
	for _, binding := range c.readRoutes() {
		row, err := binding.row(c.AllowUnfilteredTraceReads)
		if err != nil {
			return VMAuthConfig{}, err
		}
		rows = append(rows, row)
	}

	out := VMAuthConfig{}
	for _, p := range c.Principals {
		claim, err := c.RenderClaim(p)
		if err != nil {
			return VMAuthConfig{}, err
		}

		out.Users = append(out.Users, VMAuthUser{
			Name: p.Group,
			JWT: &VMAuthJWT{
				OIDC: issuer,
				// Two claims, and they answer two different questions.
				// The group says WHICH PRINCIPAL a token is; the
				// audience says the token was minted for THIS PROXY.
				// vmauth checks the second nowhere else — it validates
				// expiry and issuer and stops — so a user entry without
				// it admits every unexpired token of the issuer whose
				// groups happen to match.
				MatchClaims: map[string]string{
					c.ClaimName:   p.Group,
					AudienceClaim: audienceMatch(c.Audience),
				},
				DefaultVMAccess: &claim,
			},
			URLMap: append([]VMAuthURLMapRow(nil), rows...),
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

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
		return Claim{}, fmt.Errorf("rendering claim for %q: %w", p.label(), err)
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
	// sending its own `extra_filters` (or `extra_filters[]`, which the
	// route pins too) is that such an argument CLASHES with one the
	// route already set — and once the route's arguments are gone
	// there is no clash, so the caller's filter is forwarded instead. Refused here rather than checked at the far end, because
	// at the far end it looks like a successful query.
	if len(claim.MetricsExtraFilters) == 0 || len(claim.LogsExtraStreamFilters) == 0 {
		return Claim{}, fmt.Errorf("rendering claim for %q: it carries no filter for one of the signals, which vmauth expands to an ABSENT query argument rather "+
			"than to a deny — and an absent argument is one the caller may then supply itself", p.label())
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
//	_stream:{"k8s.cluster.name"="example-cluster","kubernetes.pod_namespace"=~"^(example-app)$"
//	  or "k8s.cluster.name"="other-cluster","kubernetes.pod_namespace"=~"^(example-app|other-app)$"}
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
		return VMAuthConfig{}, fmt.Errorf("audience is empty: vmauth validates a token's EXPIRY and, under OIDC discovery, its ISSUER, and nothing else — it has "+
			"no audience option and never inspects `aud` on its own. Without this pin each user below is selected by its group alone, so ANY unexpired token that "+
			"issuer minted is admitted whatever client it was minted for: a token the same person holds for a different application of the same issuer reads their "+
			"namespaces here, and nothing anywhere reports it, because the token verifies and the filters apply. Set it to the client id this proxy's tokens are "+
			"minted under; it is pinned into every user's `%s` claim, which is the only place vmauth can be made to check it", AudienceClaim)
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
			Name: p.label(),
			JWT: &VMAuthJWT{
				OIDC: issuer,
				// Two claims, and they answer two different questions.
				// The group says WHICH PRINCIPAL a token is; the
				// audience says the token was minted for THIS PROXY.
				// vmauth checks the second nowhere else — it validates
				// expiry and issuer and stops — so a user entry without
				// it admits every unexpired token of the issuer whose
				// groups happen to match.
				//
				// Both go through claimMatch, because vmauth compiles a
				// `match_claims` value as a regular expression and
				// neither value is ours: escaped, so each means itself,
				// and anchored, so each means only itself.
				MatchClaims: map[string]string{
					c.ClaimName:   claimMatchAny(p.groupList()),
					AudienceClaim: claimMatch(c.Audience),
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

// clusterReach is what a set of selected principals may read on one cluster.
type clusterReach struct {
	all        bool
	namespaces map[string]bool
}

// reachFor selects every principal one of the groups selects (compared
// exactly, never as a pattern) and merges their grants per cluster. It
// returns the selected principals' labels, empty when none is selected,
// and the merged reach. ClaimForGroups and WholeStoreReach both read it, so
// the two cannot disagree about what a set of groups holds.
func (c Config) reachFor(groups []string) ([]string, map[string]*clusterReach) {
	held := make(map[string]bool, len(groups))
	for _, g := range groups {
		held[g] = true
	}

	byCluster := map[string]*clusterReach{}
	var matched []string

	for _, p := range c.Principals {
		selected := false
		for _, g := range p.groupList() {
			if held[g] {
				selected = true
				break
			}
		}
		if !selected {
			continue
		}
		matched = append(matched, p.label())
		for _, g := range p.Grants {
			r := byCluster[g.Cluster]
			if r == nil {
				r = &clusterReach{namespaces: map[string]bool{}}
				byCluster[g.Cluster] = r
			}
			if g.AllNamespaces {
				r.all = true
			}
			for _, ns := range g.Namespaces {
				r.namespaces[ns] = true
			}
		}
	}
	return matched, byCluster
}

// WholeStoreReach reports whether a caller holding the given groups may
// read EVERY namespace on EVERY one of the given clusters: some selected
// principal grants allNamespaces on each of them. The clusters are the ones
// the store serves.
//
// It is the question the trace store forces. Its select APIs accept no
// filter (see TracesRead), so a caller can be given all of its spans or
// none; narrower than the whole store means none. A caller who holds a
// whole-cluster group AND a namespace group has whole-store reach: grants
// are unioned, as in ClaimForGroups. Namespace grants never add up to a
// cluster, however many are listed.
//
// An empty cluster list is false: with nothing named as served, nobody can
// be shown to see all of it, and the safe answer to a question about
// unfilterable data is no.
func (c Config) WholeStoreReach(groups, clusters []string) bool {
	if len(clusters) == 0 {
		return false
	}
	_, byCluster := c.reachFor(groups)
	for _, cl := range clusters {
		if r := byCluster[cl]; r == nil || !r.all {
			return false
		}
	}
	return true
}

// ClaimForGroups returns the vm_access claim body for a caller who holds
// the given groups: the UNION of what every principal those groups select
// may read.
//
// This is the per-person shape vmauth cannot build for itself. vmauth
// selects ONE user per token by `match_claims`, so a token holding two
// groups reads whichever principal came first and never both; an
// authoriser in front of it can read the whole list, merge the grants, and
// hand vmauth one claim that says so.
//
// A principal is selected when ANY of its groups is among the caller's
// (compared exactly, never as a pattern). Grants are merged per cluster:
// the namespaces of every grant on that cluster are unioned, and a grant
// of every namespace on a cluster swallows the rest of that cluster's
// grants. The merged principal is then rendered by RenderClaim, so the
// result is exactly what a principal holding that union would get from
// vmauth's own configuration — in particular the logs filter is ONE
// `_stream:{a or b}` string, because the log store ANDs several.
//
// The bool is false, with a nil error, when no principal is selected. That
// is "this caller may read nothing", and it is the caller's to turn into a
// refusal: an empty claim is not a narrow grant, it is no grant enforced.
// An error means the configuration itself is unusable.
func (c Config) ClaimForGroups(groups []string) (Claim, bool, error) {
	matched, byCluster := c.reachFor(groups)
	if len(matched) == 0 {
		return Claim{}, false, nil
	}

	merged := Principal{Group: strings.Join(matched, "+")}
	for cluster, r := range byCluster {
		g := Grant{Cluster: cluster}
		if r.all {
			g.AllNamespaces = true
		} else {
			for ns := range r.namespaces {
				g.Namespaces = append(g.Namespaces, ns)
			}
		}
		merged.Grants = append(merged.Grants, g)
	}

	claim, err := c.RenderClaim(merged)
	if err != nil {
		return Claim{}, false, err
	}
	return claim, true, nil
}

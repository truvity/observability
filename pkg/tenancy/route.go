package tenancy

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// A `vm_access` claim does nothing on its own.
//
// This is the whole mechanism, and it is worth stating plainly because
// the shape that gets it wrong looks right. vmauth verifies the token,
// selects the user by `match_claims`, computes the principal's
// `vm_access` claim (from the token, or from
// `default_vm_access_claim`) — and then, unless the route it chose
// contains a PLACEHOLDER, throws the claim away and forwards the
// request unfiltered:
//
//	// app/vmauth/jwt.go
//	func replaceJWTPlaceholders(bu *backendURL, hc HeadersConf, vma *jwt.VMAccessClaim) (*url.URL, HeadersConf) {
//	    if !bu.hasPlaceHolders && !hc.hasAnyPlaceHolders {
//	        return bu.url, hc
//	    }
//
// That early return is the only guard between "this principal may read
// one tenant" and "this principal reads every tenant", and nothing about
// a route without a placeholder looks wrong: the claim is still
// computed, still correct, still visible in the rendered configuration,
// and still discarded. It produces the same output as a working
// configuration for any test that only ever asks one tenant's question.
//
// So a placeholder is not something a caller adds to a route. It is part
// of what a route IS, which is what ReadRoute exists to make true.
//
// Two mechanical constraints come from the same function and are easy to
// get wrong:
//
//   - the placeholder must be the WHOLE value of the query argument.
//     Substitution is a map lookup on the complete value
//     (`if dv, ok := data[value]; ok`), not a string replacement, so
//     `extra_filters=x{{.MetricsExtraFilters}}` is forwarded verbatim.
//     Only the URL PATH is substring-replaced, and only for the tenant
//     and account placeholders.
//   - an EMPTY claim list silently removes the argument. The placeholder
//     expands to the claim's values, so an empty list expands to nothing,
//     the key disappears from the query — and vmauth's protection against
//     a client sending its own `extra_filters` is precisely that the key
//     CLASHES with one the route already set. With the key gone there is
//     no clash, and the client's own filter is forwarded instead. An
//     empty filter list is therefore not "deny", it is "the caller
//     chooses"; RenderClaim refuses to produce one.

// The placeholders vmauth substitutes from the `vm_access` claim, as
// spelled in app/vmauth/jwt.go. Only the two this design uses are named;
// the rest are listed in supportedPlaceholders so that a typo is refused
// rather than forwarded as a literal.
const (
	MetricsFilterPlaceholder = "{{.MetricsExtraFilters}}"
	LogsFilterPlaceholder    = "{{.LogsExtraStreamFilters}}"
)

// The query argument each store reads its enforced filter from.
//
// They are different arguments on purpose. vmselect OR-s the
// `extra_filters` it receives; VictoriaLogs AND-s every
// `extra_stream_filters` into the query AND into every subquery inside
// it — which is what makes it an access-control mechanism rather than a
// convenience, because otherwise a subquery would escape it.
const (
	MetricsFilterArg = "extra_filters"
	LogsFilterArg    = "extra_stream_filters"
)

// supportedPlaceholders is every placeholder vmauth knows. A route
// naming anything else would be forwarded to the store as a literal
// `{{...}}` string — which the store rejects as an unparseable filter if
// you are lucky, and ignores if you are not.
var supportedPlaceholders = map[string]bool{
	"{{.MetricsTenant}}":          true,
	"{{.MetricsAccountID}}":       true,
	"{{.MetricsProjectID}}":       true,
	"{{.MetricsExtraLabels}}":     true,
	"{{.MetricsExtraFilters}}":    true,
	"{{.LogsAccountID}}":          true,
	"{{.LogsProjectID}}":          true,
	"{{.LogsExtraFilters}}":       true,
	"{{.LogsExtraStreamFilters}}": true,
}

// ReadRoute is one row of a reader's url_map: the endpoints it may call,
// and the query argument that carries this principal's filter into the
// store behind them.
//
// Its fields are unexported and it has no exported constructor, so the
// only ReadRoutes that exist are the ones declared in this file. That is
// deliberate: the defect this type prevents is a route somebody added
// without a filter, and a type whose value can be assembled field by
// field elsewhere prevents nothing.
type ReadRoute struct {
	name        string
	paths       []string
	filterArg   string
	placeholder string

	// unenforceable is why the store behind these paths cannot carry a
	// filter at all, or "" when it can.
	//
	// It is a sentence rather than a boolean because it is quoted back to
	// the operator in the refusal, and an operator being asked to accept
	// an unscoped read route is owed the reason.
	unenforceable string
}

// filteredRoute is the only way to declare a route that carries a
// filter, and it takes the placeholder because there is no correct route
// without one.
func filteredRoute(name, filterArg, placeholder string, paths ...string) ReadRoute {
	return ReadRoute{name: name, paths: paths, filterArg: filterArg, placeholder: placeholder}
}

// unenforceableRoute declares a route whose store has no mechanism to
// carry a filter. Such a route is not rendered unless the caller asks
// for it by name — see Config.AllowUnfilteredTraceReads.
func unenforceableRoute(name, why string, paths ...string) ReadRoute {
	return ReadRoute{name: name, paths: paths, unenforceable: why}
}

// Name is what the route is called in an error message.
func (r ReadRoute) Name() string { return r.name }

// Paths are the endpoints this route admits.
func (r ReadRoute) Paths() []string { return append([]string(nil), r.paths...) }

// FilterArg is the query argument the store reads the filter from, or ""
// on an unenforceable route.
func (r ReadRoute) FilterArg() string { return r.filterArg }

// Placeholder is the vmauth placeholder that carries the principal's
// filter into FilterArg, or "" on an unenforceable route.
func (r ReadRoute) Placeholder() string { return r.placeholder }

// Unenforceable is why this route cannot be scoped, or "" when it can.
func (r ReadRoute) Unenforceable() string { return r.unenforceable }

// validate is the Validate-time half of "a route without its filter
// cannot be rendered". The constructors make an incomplete route hard to
// write; this makes one impossible to use.
func (r ReadRoute) validate() error {
	if len(r.paths) == 0 {
		return fmt.Errorf("read route %q has no paths", r.name)
	}
	for _, p := range r.paths {
		if _, err := regexp.Compile("^" + p + "$"); err != nil {
			return fmt.Errorf("read route %q: path %q is not a regular expression vmauth can compile: %w", r.name, p, err)
		}
	}

	if r.unenforceable != "" {
		if r.filterArg != "" || r.placeholder != "" {
			return fmt.Errorf("read route %q is declared unenforceable and also carries a filter; one of the two is a lie", r.name)
		}
		return nil
	}

	if r.filterArg == "" {
		return fmt.Errorf("read route %q has no filter argument: the principal's vm_access claim would be computed and then discarded, and every token that verifies would read every tenant", r.name)
	}
	if r.placeholder == "" {
		return fmt.Errorf("read route %q has no filter placeholder: vmauth applies a vm_access claim ONLY by substituting a placeholder into the route, so this route would forward every query unfiltered while the claim beside it says otherwise", r.name)
	}
	if !supportedPlaceholders[r.placeholder] {
		return fmt.Errorf("read route %q names placeholder %q, which vmauth does not substitute; it would reach the store as that literal string. The supported ones are listed in supportedPlaceholders", r.name, r.placeholder)
	}
	return nil
}

// urlPrefix renders the route's backend address with its filter argument
// attached.
//
// The placeholder is written as the whole value of the argument and
// nothing else, because vmauth substitutes a query argument only when
// the placeholder IS the value — a value that merely contains one is
// forwarded as written.
func (r ReadRoute) urlPrefix(backend string) (string, error) {
	if err := r.validate(); err != nil {
		return "", err
	}

	u, err := url.Parse(backend)
	if err != nil {
		return "", fmt.Errorf("read route %q: backend %q is not a URL: %w", r.name, backend, err)
	}
	if u.Scheme == "" || u.Host == "" {
		return "", fmt.Errorf("read route %q: backend %q needs a scheme and a host", r.name, backend)
	}
	if u.RawQuery != "" {
		return "", fmt.Errorf("read route %q: backend %q already carries a query string. vmauth would keep both arguments, and which one the store applies is not something this package can promise", r.name, backend)
	}

	if r.unenforceable != "" {
		return u.String(), nil
	}

	// Written literally rather than through url.Values.Encode(), which
	// would percent-escape the braces. Both forms work — vmauth decodes
	// the query before looking the placeholder up — but a configuration
	// a person can read is a configuration a person can review, and this
	// one is the vendor's own spelling.
	u.RawQuery = r.filterArg + "=" + r.placeholder
	return u.String(), nil
}

// MetricsRead, LogsRead and TracesRead are the routes a reader is given.
//
// The PATHS are a list of named endpoints rather than a prefix. The
// tempting shapes are wrong in the same way: `/prometheus/.*` also
// matches `/prometheus/api/v1/write` and
// `/prometheus/api/v1/admin/tsdb/delete_series`; `/api/v1/.*` also
// matches `/api/v1/write` and `/api/v1/import`; `/.*` also matches
// `/internal/force_merge`, whose own authKey flag REPLACES the store's
// `-httpAuth.*` rather than adding to it. A reader's route that also
// accepts writes is not a reader's route, and nothing about it looks
// wrong until somebody uses it.
//
// The FILTER is what makes the route a route for one principal rather
// than for everybody. See the comment at the top of this file.
//
// charts/observability-stack renders these same routes into its VMUser
// objects, and its `tenancy` golden case exists to prove the two have not
// drifted.
var (
	MetricsRead = filteredRoute("metrics", MetricsFilterArg, MetricsFilterPlaceholder,
		"/prometheus/api/v1/query",
		"/prometheus/api/v1/query_range",
		"/prometheus/api/v1/series",
		"/prometheus/api/v1/labels",
		"/prometheus/api/v1/label/[^/]+/values",
		// `/status/tsdb` is here and the rest of `/status/` is not.
		// TSDBStatusHandler takes its filters from getCommonParams, so
		// the principal's selector reaches it; `/status/active_queries`
		// and `/status/top_queries` return other principals' query TEXT
		// and take no filter at all, `/status/metric_names_stats`
		// returns metric names across every tenant, and
		// `/api/v1/metadata` returns the metadata of every series in the
		// store. A wildcard here was all four.
		"/prometheus/api/v1/status/tsdb",
		// Carries the store's version and nothing from any tenant, and
		// Grafana's Prometheus datasource asks for it to decide which
		// dialect it is talking to.
		"/prometheus/api/v1/status/buildinfo",
		"/prometheus/vmui.*",
	)

	LogsRead = filteredRoute("logs", LogsFilterArg, LogsFilterPlaceholder,
		"/select/logsql/.*",
		"/select/vmui.*",
	)

	// The trace store's read route, which carries NO filter, because
	// there is nothing to carry it in.
	//
	// VictoriaTraces routes `/select/jaeger/*` and `/select/tempo/*` away
	// from the LogsQL handler that reads `extra_filters` and into
	// handlers whose entire accepted input is
	// `tracecommon.GetCommonParams` — a tenant id from the
	// `AccountID`/`ProjectID` headers, `hidden_fields_filters`, and
	// `allow_partial_response`. `hidden_fields_filters` hides FIELDS from
	// a result, not rows, so it scopes nothing; the Jaeger query
	// parameters (`service`, `operation`, `tags`, ...) are the caller's
	// own and are overwritten rather than appended to, so a proxy cannot
	// narrow them.
	//
	// The headers are a real mechanism, but they are a different tenancy
	// model: they select one of the store's own tenant ids, and this
	// design scopes by LABEL precisely so that a fleet-wide question
	// stays answerable across tenants. Nothing writes per-tenant account
	// ids on the way in, so there would be nothing for them to select.
	//
	// So this route cannot be scoped through this proxy, and saying so is
	// the entire point of this declaration. See
	// Config.AllowUnfilteredTraceReads.
	TracesRead = unenforceableRoute("traces",
		"VictoriaTraces' Jaeger and Tempo select APIs accept no query argument that carries a server-side filter: "+
			"their handlers take a tenant id from headers, `hidden_fields_filters` (which hides fields, not rows) "+
			"and `allow_partial_response`, and nothing else. A principal given this route reads every tenant's spans",
		"/select/jaeger/.*",
		"/select/tempo/.*",
	)
)

// MetricsReadPaths, LogsReadPaths and TracesReadPaths are the routes'
// paths, kept as plain lists for callers that only want to know what a
// reader may call.
//
// They are derived rather than declared: a second copy of a route's paths
// is a second thing to forget to change.
var (
	MetricsReadPaths = MetricsRead.Paths()
	LogsReadPaths    = LogsRead.Paths()
	TracesReadPaths  = TracesRead.Paths()
)

// readRoutes returns the routes this configuration would render, in
// order. Traces appear only when a trace backend was given.
func (c Config) readRoutes() []routeBinding {
	out := []routeBinding{
		{route: MetricsRead, backend: c.MetricsBackend},
		{route: LogsRead, backend: c.LogsBackend},
	}
	if c.TracesBackend != "" {
		out = append(out, routeBinding{route: TracesRead, backend: c.TracesBackend})
	}
	return out
}

// routeBinding is a route and the address it forwards to.
type routeBinding struct {
	route   ReadRoute
	backend string
}

// row renders the binding as a url_map entry, refusing anything that
// would forward a read unfiltered without having been asked to.
func (b routeBinding) row(allowUnfiltered bool) (VMAuthURLMapRow, error) {
	if why := b.route.Unenforceable(); why != "" && !allowUnfiltered {
		return VMAuthURLMapRow{}, fmt.Errorf(
			"read route %q cannot be scoped to a principal and is not rendered: %s. "+
				"If that is acceptable for this estate — because every principal who can reach the proxy may see every tenant's %s — "+
				"set AllowUnfilteredTraceReads and say so where people will read it. "+
				"Leaving the route in place without the filter is the one option this package will not take: "+
				"it is indistinguishable, from the outside and in every test that asks about one tenant, from a route that scopes",
			b.route.Name(), why, b.route.Name())
	}

	prefix, err := b.route.urlPrefix(b.backend)
	if err != nil {
		return VMAuthURLMapRow{}, err
	}
	return VMAuthURLMapRow{SrcPaths: b.route.Paths(), URLPrefix: []string{prefix}}, nil
}

// CarriesFilter reports whether a rendered url_prefix carries the given
// placeholder as the WHOLE value of the given query argument, which is
// the only form vmauth substitutes.
//
// It is exported because the check belongs to whoever is holding a
// rendered route, and two of them are outside this package: the chart's
// agreement test, which walks the VMUser objects Helm produced, and
// anyone assembling a vmauth configuration from more than this library.
func CarriesFilter(urlPrefix, arg, placeholder string) bool {
	u, err := url.Parse(urlPrefix)
	if err != nil {
		return false
	}
	// RawQuery is read directly rather than through Query(), because
	// Query() would also accept a value that merely contains the
	// placeholder, and vmauth would not.
	for _, kv := range strings.Split(u.RawQuery, "&") {
		name, value, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		if name == arg && value == placeholder {
			return true
		}
	}
	return false
}

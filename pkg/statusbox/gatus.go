// Gatus page building: a typed, estate-neutral input (Catalogue) and one
// function (RenderGatus) that turns it into the Gatus v5 YAML a status
// box instance boots from — the combined ops page every consumer of this
// package ends up building: every piece of platform infrastructure and
// every company's own hosts, as ORDINARY Gatus `endpoints:` entries, no
// per-company Instance and no `external-endpoints:` at all, including
// the deadman and each company's own customer-facing signal.
//
// What lives HERE is the estate-neutral half: given a Catalogue, render
// the Config. What does NOT live here, and stays the consumer's own: how
// a Catalogue gets BUILT from that estate's own configuration (which
// hostnames are public, which company owns which, where the alerts-read
// path answers) — see this package's own docs/statusbox.md,
// "internal -> status, pulled", and the consumer that ported this file
// in the first place for what that derivation looks like.
//
// Two facts about Gatus this file exists to design around (found by
// running the thing, not documented by Gatus itself):
//
//  1. A Config with no endpoints at all refuses to boot ("should contain
//     at least one endpoint or suite"). RenderGatus refuses the same
//     empty input rather than let a consumer discover that at a box's
//     first boot.
//  2. vmalert's own `/api/v1/alerts` has no per-namespace, per-cluster or
//     per-company concept: the narrowing is DONE BY THE URL, via
//     vmalert's own `match[]` query parameter (a MetricsQL label
//     selector, applied against each alert's own labels only — not its
//     `state`, which `match[]` cannot see). Gatus's own `[BODY]`
//     condition language has no array-filter query of its own (dot
//     notation, array index and `len()` only), so the condition beside
//     each alerts-read endpoint only ever asks "how many alerts survived
//     the match[] filter", never "does one of these alerts look like
//     X" — that question is answered before the response ever reaches
//     Gatus.

package statusbox

import (
	"fmt"
	"net/url"
	"strings"

	"go.yaml.in/yaml/v3"
)

const (
	certExpiryCondition = "[CERTIFICATE_EXPIRATION] > 240h"
	statusCondition     = "[STATUS] == 200"

	// lenientStatusCondition is the fallback probe condition for a
	// company host whose CompanyHost.StatusPath is empty: an API
	// commonly has no health page of its own at `/` — a 404 there is a
	// perfectly healthy API, not a down one — so the box only asks "is
	// anything even answering", never "is this exact byte-for-byte page
	// here". Without a StatusPath, this also means an empty 404 from an
	// unrouted or undeployed host reads as healthy too — a caller with a
	// real status page for every host it cares about avoids this simply
	// by setting StatusPath.
	lenientStatusCondition = "[STATUS] < 500"

	// endpointNameSeparator is the naming glue between a host's own
	// first DNS label and its Env: "dms · prod", never a hyphen or
	// colon, which either could read as part of the hostname itself.
	endpointNameSeparator = " · "

	// alertsPresentCondition / alertsAbsentCondition are the only two
	// shapes an alerts-read check needs: `match[]` already narrowed the
	// response to exactly the alerts this check cares about, so what is
	// left is only ever "at least one survived" (the deadman) or "none
	// survived" (a company's green).
	//
	// NEITHER checks `state`: vmalert's `match[]` matches an alert's own
	// LABELS only, not whether it is `pending` or `firing`, so a rule
	// still inside its `for:` window counts as present here the same as
	// a firing one. That is the conservative direction for a status page
	// to be wrong in — early rather than late.
	alertsPresentCondition = "len([BODY].data.alerts) > 0"
	alertsAbsentCondition  = "len([BODY].data.alerts) == 0"

	companyProbeInterval = "1m"
	opsProbeInterval     = "2m"
	alertsReadInterval   = "1m"

	// oidcClientSecretVar is Gatus's own ${...} reference to the ONE
	// Secrets.Env entry a Public instance's Config needs
	// ("OIDC_CLIENT_SECRET"). Unlike alertVar, no prefix is added — see
	// Secrets.Env's own doc comment in statusbox.go for why: Env's whole
	// point is a Config writing exactly the name its key names, nothing
	// added.
	oidcClientSecretVar = "${OIDC_CLIENT_SECRET}"
)

type (
	// Catalogue is the whole, estate-neutral input RenderGatus needs.
	// Nothing here is derived by this package: every field is a plain
	// value or a slice the caller already worked out from its own
	// configuration.
	Catalogue struct {
		// PlatformHosts is every public hostname that belongs to no
		// company — infrastructure, probed under the "platform" group,
		// with the strict statusCondition and a certificate-expiry
		// check. Deduplicated and sorted is the caller's job; RenderGatus
		// renders them in the order given.
		PlatformHosts []string

		// Companies is every company this page shows a group for, in
		// the order their groups should render. A company with no hosts
		// yet still gets its own customer-facing signal endpoint — see
		// Company's own doc comment.
		Companies []Company

		// AlertsRead is the one pull path every alerts-read endpoint
		// (the deadman, and each company's customer-facing signal) is
		// built from.
		AlertsRead AlertsRead

		// Providers are the OPTIONAL outbound alert channels every
		// alerts-read check shares. A zero value renders NO alerting:
		// stanza and no alerts: refs at all — the page still boots and
		// still shows red/green, it just notifies nobody yet, which
		// RenderGatus makes visible with a leading YAML comment rather
		// than a silent gap.
		Providers DeadmanProviders

		// Security is this instance's OIDC block, or nil for a private,
		// unauthenticated instance (a breakglass twin reached over a
		// private network, for the day the OIDC issuer itself is down).
		// Two instances built from the SAME Catalogue but a different
		// Security are exactly how a public/breakglass pair share every
		// endpoint and differ only here.
		Security *OIDCSecurity

		// StoragePath is Gatus's own `storage.path` — a SQLite file, one
		// per instance, on a volume that survives a replacement.
		StoragePath string
	}

	// Company is one legal entity's own group on the page: its display
	// name, the hostnames it owns (each already resolved to its
	// environment and, if it has one, its own health-check path), and
	// the code alerts-read filters on (`company=<code>` in the
	// customer-facing signal's own match[]).
	Company struct {
		// Code is this company's short identifier — interpolated into
		// the alerts-read match[] filter (`company=%q`) and into the
		// customer-facing endpoint's own name (`<code>-customer-
		// facing`), so it is matched exactly and never a display
		// string.
		Code string
		// DisplayName is prose: the Gatus `group:` label every endpoint
		// of this company's own (business hosts, the customer-facing
		// signal) is grouped under.
		DisplayName string
		// Hosts is this company's own business hostnames. May be empty:
		// a company with no host yet still gets a customer-facing
		// signal endpoint, which is meaningful (an entity can have
		// customer-facing alerts without a probed hostname yet).
		Hosts []CompanyHost
	}

	// CompanyHost is one business hostname on a company's page: the
	// hostname the box actually probes, the cluster/environment it
	// belongs to (naming an endpoint), and its own health-check path if
	// it has one.
	CompanyHost struct {
		// Host is the hostname probed, e.g. "dms.devel.example.xyz".
		Host string
		// Env names the cluster/environment this host belongs to — an
		// endpoint is named "<Host's first DNS label> · <Env>", never
		// the company or project name.
		Env string
		// StatusPath is this host's own health-check path ("" for
		// none). Set, the probe URL becomes Host+StatusPath and the
		// condition is the strict statusCondition; unset, the probe
		// stays the bare host and the condition is the lenient one —
		// see probeConditions.
		StatusPath string
	}

	// AlertsRead is the one pull path every alerts-read endpoint reads
	// from: vmalert's own `/api/v1/alerts`, at Host, with the bearer
	// this Config references as ${ALERT_URL_<TokenEnvKey, upper-cased>}
	// — the exact environment-variable name statusbox's own
	// Secrets.AlertURLs (statusbox.go's alertURLName) computes from the
	// SAME key, so the two agree without either one hard-coding the
	// other's naming.
	AlertsRead struct {
		// Host is the bare host the alerts-read path answers on, e.g.
		// "alerts.kernel.example.private" — no scheme, no path;
		// alertsReadURL builds the rest.
		Host string
		// TokenEnvKey is the AlertURLs map key (statusbox.Secrets.
		// AlertURLs) whose value becomes every alerts-read endpoint's
		// bearer token.
		TokenEnvKey string
	}

	// OIDCSecurity builds Gatus v5's own security.oidc block (README
	// §OIDC) for a Public instance. Carries NO allowed-subjects: Gatus's
	// own README is explicit about what an unset security.oidc.
	// allowed-subjects means ("If this is not specified, all subjects
	// will be allowed") — so simply never writing the key is both the
	// correct and the honest way to admit any live user who completes
	// the OIDC flow, and this type has no field to set one with.
	OIDCSecurity struct {
		// IssuerURL is the OIDC issuer this instance signs in against.
		IssuerURL string
		// PublicHostname is this instance's OWN public address —
		// RenderGatus builds RedirectURL from it
		// ("https://"+PublicHostname+"/authorization-code/callback"),
		// which is Gatus's own requirement (redirect-url must end with
		// exactly that path).
		PublicHostname string
		// ClientID is the OIDC client this page signs in as. Not a
		// secret — see statusbox.Secrets.Env's own doc comment for
		// where the matching client_secret comes from instead (this
		// package never takes one as a literal; every rendered Config
		// references oidcClientSecretVar, and the caller wires the real
		// value through Args.Secrets.Env["OIDC_CLIENT_SECRET"]).
		ClientID string
	}

	// DeadmanProviders names the OPTIONAL outbound alert channels every
	// alerts-read check shares — the ${ALERT_URL_<KEY>} AlertURLs map
	// keys (see alertVar), or "" for a provider that is not configured.
	// A zero value renders no alerting: stanza and no alerts: refs at
	// all.
	DeadmanProviders struct {
		SlackKey     string
		PagerDutyKey string
	}
)

type (
	gatusConfig struct {
		Storage   gatusStorage    `yaml:"storage"`
		Security  *gatusSecurity  `yaml:"security,omitempty"`
		Endpoints []gatusEndpoint `yaml:"endpoints,omitempty"`
		Alerting  *gatusAlerting  `yaml:"alerting,omitempty"`
	}

	gatusSecurity struct {
		OIDC *gatusOIDC `yaml:"oidc,omitempty"`
	}

	gatusOIDC struct {
		IssuerURL    string   `yaml:"issuer-url"`
		RedirectURL  string   `yaml:"redirect-url"`
		ClientID     string   `yaml:"client-id"`
		ClientSecret string   `yaml:"client-secret"`
		Scopes       []string `yaml:"scopes"`
	}

	gatusStorage struct {
		Type string `yaml:"type"`
		Path string `yaml:"path"`
	}

	gatusEndpoint struct {
		Name  string `yaml:"name"`
		Group string `yaml:"group,omitempty"`
		URL   string `yaml:"url"`
		// Headers carries the alerts-read bearer (Authorization: Bearer
		// ${ALERT_URL_<KEY>}) — the ONLY endpoints that set it. An
		// ordinary hostname probe needs no header at all.
		Headers    map[string]string `yaml:"headers,omitempty"`
		Interval   string            `yaml:"interval"`
		Conditions []string          `yaml:"conditions"`
		Alerts     []gatusAlertRef   `yaml:"alerts,omitempty"`
	}

	gatusAlertRef struct {
		Type string `yaml:"type"`
	}

	gatusAlerting struct {
		Slack     *gatusSlackAlerting     `yaml:"slack,omitempty"`
		PagerDuty *gatusPagerDutyAlerting `yaml:"pagerduty,omitempty"`
	}

	gatusSlackAlerting struct {
		WebhookURL string `yaml:"webhook-url"`
	}

	gatusPagerDutyAlerting struct {
		IntegrationKey string `yaml:"integration-key"`
	}
)

// alertVar returns the ${ALERT_URL_<KEY>} reference for an AlertURLs
// key: upper-cased, exactly as statusbox.go's render() builds the
// environment variable name (alertURLName).
func alertVar(key string) string {
	return "${ALERT_URL_" + strings.ToUpper(key) + "}"
}

// alertsReadURL builds one call against vmalert's own `/api/v1/alerts`,
// narrowed server-side by ONE `match[]` label matcher (a MetricsQL
// vector selector, e.g. `{alertname="Watchdog"}`) — see this file's own
// package doc comment for why the matcher does the narrowing and the
// condition beside the endpoint only checks what survived it.
func alertsReadURL(host, matcher string) string {
	u := url.URL{Scheme: "https", Host: host, Path: "/api/v1/alerts"}
	q := u.Query()
	q.Set("match[]", matcher)
	u.RawQuery = q.Encode()

	return u.String()
}

// RenderGatus builds the Gatus v5 YAML for one instance from a
// Catalogue: the platform hosts, every company's own hosts and
// customer-facing signal, and the deadman, all as ordinary `endpoints:`
// entries — plus the security block Catalogue.Security carries, or none
// for a private/breakglass instance.
//
// Two Catalogues that are otherwise identical and differ only in
// Security produce Configs that differ ONLY in their security block —
// see TestOpsAndOpsBreakglassShareEveryEndpoint.
func RenderGatus(c Catalogue) (string, error) {
	if len(c.PlatformHosts) == 0 {
		return "", fmt.Errorf("statusbox: RenderGatus: Catalogue.PlatformHosts is empty — " +
			"a Gatus Config with no endpoints at all refuses to boot")
	}

	authHeader := map[string]string{"Authorization": "Bearer " + alertVar(c.AlertsRead.TokenEnvKey)}

	var alerts []gatusAlertRef
	var alerting *gatusAlerting

	if c.Providers.SlackKey != "" {
		alerts = append(alerts, gatusAlertRef{Type: "slack"})
		alerting = ensureAlerting(alerting)
		alerting.Slack = &gatusSlackAlerting{WebhookURL: alertVar(c.Providers.SlackKey)}
	}

	if c.Providers.PagerDutyKey != "" {
		alerts = append(alerts, gatusAlertRef{Type: "pagerduty"})
		alerting = ensureAlerting(alerting)
		alerting.PagerDuty = &gatusPagerDutyAlerting{IntegrationKey: alertVar(c.Providers.PagerDutyKey)}
	}

	endpoints := make([]gatusEndpoint, 0, len(c.PlatformHosts)+2*len(c.Companies)+1)

	for _, host := range c.PlatformHosts {
		endpoints = append(endpoints, gatusEndpoint{
			Name:       host,
			Group:      "platform",
			URL:        "https://" + host,
			Interval:   opsProbeInterval,
			Conditions: []string{statusCondition, certExpiryCondition},
		})
	}

	for _, company := range c.Companies {
		names := endpointNames(company.Hosts)

		for i, host := range company.Hosts {
			endpoints = append(endpoints, gatusEndpoint{
				Name:       names[i],
				Group:      company.DisplayName,
				URL:        probeURL(host),
				Interval:   companyProbeInterval,
				Conditions: probeConditions(host),
			})
		}

		endpoints = append(endpoints, gatusEndpoint{
			Name:  company.Code + "-customer-facing",
			Group: company.DisplayName,
			URL: alertsReadURL(c.AlertsRead.Host,
				fmt.Sprintf(`{customer_facing="true",company=%q}`, company.Code)),
			Headers:    authHeader,
			Interval:   alertsReadInterval,
			Conditions: []string{statusCondition, alertsAbsentCondition},
			Alerts:     alerts,
		})
	}

	endpoints = append(endpoints, gatusEndpoint{
		Name:       "deadman",
		Group:      "platform",
		URL:        alertsReadURL(c.AlertsRead.Host, `{alertname="Watchdog"}`),
		Headers:    authHeader,
		Interval:   alertsReadInterval,
		Conditions: []string{statusCondition, alertsPresentCondition},
		Alerts:     alerts,
	})

	var security *gatusSecurity
	if c.Security != nil {
		security = &gatusSecurity{
			OIDC: &gatusOIDC{
				IssuerURL:    c.Security.IssuerURL,
				RedirectURL:  "https://" + c.Security.PublicHostname + "/authorization-code/callback",
				ClientID:     c.Security.ClientID,
				ClientSecret: oidcClientSecretVar,
				Scopes:       []string{"openid"},
			},
		}
	}

	cfg := gatusConfig{
		Storage:   gatusStorage{Type: "sqlite", Path: c.StoragePath},
		Security:  security,
		Endpoints: endpoints,
		Alerting:  alerting,
	}

	out, err := marshalGatusConfig(cfg)
	if err != nil {
		return "", err
	}

	if len(alerts) == 0 {
		out = "# statusbox: no alert providers configured (Catalogue.Providers is empty) " +
			"— nobody is notified if the deadman or a company's own signal goes red.\n" + out
	}

	return out, nil
}

// endpointNames returns the Gatus `name:` for each host in hosts, in the
// same order: "<host's first DNS label> <endpointNameSeparator>
// <host.Env>" — "dms · prod", never a project/company name and never
// numbered.
//
// Two hosts owned by the SAME company on two different clusters (a
// project's primary hostname on devel and on prod) get two distinct
// names for free, because the label and the env both differ, with no
// counter involved. It can still happen that two hosts share both their
// first label AND their env — distinguished only by a deeper subdomain —
// and Gatus refuses two endpoints sharing a (name, group) pair at boot
// ("invalid endpoint _<name>: name and group combination must be
// unique", found by actually booting twinproduction/gatus:v5.37.0
// against this render). So every candidate name is computed FIRST, and
// any name shared by more than one host in this company's own list
// falls back to the FULL hostname, for EVERY host in that collision,
// deterministically.
func endpointNames(hosts []CompanyHost) []string {
	candidates := make([]string, len(hosts))
	counts := make(map[string]int, len(hosts))

	for i, host := range hosts {
		candidates[i] = firstDNSLabel(host.Host) + endpointNameSeparator + host.Env
		counts[candidates[i]]++
	}

	names := make([]string, len(hosts))
	for i, host := range hosts {
		if counts[candidates[i]] > 1 {
			names[i] = host.Host
			continue
		}
		names[i] = candidates[i]
	}

	return names
}

// firstDNSLabel returns host's leftmost DNS label — "dms" for
// "dms.devel.example.xyz" — the product-facing part of a hostname
// endpointNames names an endpoint after.
func firstDNSLabel(host string) string {
	if i := strings.IndexByte(host, '.'); i >= 0 {
		return host[:i]
	}

	return host
}

// probeURL builds the URL a business host is probed at: the host's own
// StatusPath appended when it set one, or the bare host when it did
// not — see probeConditions for the condition each shape pairs with.
func probeURL(host CompanyHost) string {
	if host.StatusPath == "" {
		return "https://" + host.Host
	}

	return "https://" + host.Host + host.StatusPath
}

// probeConditions returns the Gatus conditions for one business host: a
// host that declared a StatusPath gets the strict statusCondition
// against that exact path; a host that did not gets the
// lenientStatusCondition against the bare host, since an API with no
// health page of its own commonly 404s at `/` while perfectly healthy.
// Either way, certExpiryCondition rides along.
func probeConditions(host CompanyHost) []string {
	if host.StatusPath == "" {
		return []string{lenientStatusCondition, certExpiryCondition}
	}

	return []string{statusCondition, certExpiryCondition}
}

// ensureAlerting returns a, or a fresh *gatusAlerting if a is nil — the
// two provider branches in RenderGatus share it so neither has to know
// whether the other ran first.
func ensureAlerting(a *gatusAlerting) *gatusAlerting {
	if a == nil {
		return &gatusAlerting{}
	}

	return a
}

func marshalGatusConfig(cfg gatusConfig) (string, error) {
	out, err := yaml.Marshal(cfg)
	if err != nil {
		return "", fmt.Errorf("statusbox: marshal gatus config: %w", err)
	}

	return string(out), nil
}

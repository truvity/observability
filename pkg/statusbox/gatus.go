// Gatus page building: a typed, estate-neutral input (Catalogue) and one
// function (RenderGatus) that turns it into the Gatus v5 YAML a status
// box instance boots from — the combined ops page every consumer of this
// package ends up building: every piece of platform infrastructure and
// every company's own hosts, as ORDINARY Gatus `endpoints:` entries, no
// per-company Instance and no `external-endpoints:` at all, including
// the deadman, internal infrastructure checks, and each company's own
// customer-facing signal.
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
	"regexp"
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

	// deadmanInterval / deadmanFailureThreshold: every deadman check runs
	// every two minutes and pages after two consecutive failures, so a
	// single slow answer never pages and a real outage does within about
	// four minutes.
	deadmanInterval         = "2m"
	deadmanFailureThreshold = 2

	// alertmanagerWatchdogPath is Alertmanager's own active-alerts
	// listing, narrowed server-side to the Watchdog and to alerts that
	// are neither silenced nor inhibited: "the heartbeat is ACTIVE in the
	// router", which vmalert's own listing cannot say.
	alertmanagerWatchdogPath = "/api/v2/alerts"

	// alertmanagerAlertsPresentCondition: Alertmanager answers a JSON
	// array of alerts; its first element exists when the filter matched.
	alertmanagerAlertsPresentCondition = "[BODY][0].labels.alertname == Watchdog"

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
		// (the deadman, each company's customer-facing signal, and internal
		// checks) is built from.
		AlertsRead AlertsRead

		// Providers are the OPTIONAL outbound alert channels the deadman
		// and company signals share. Internal checks configured in
		// InternalChecks never alert, regardless of whether Providers is
		// configured. A zero value renders NO alerting: stanza and no
		// alerts: refs at all — the page still boots and still shows
		// red/green, it just notifies nobody yet, which RenderGatus makes
		// visible with a leading YAML comment rather than a silent gap.
		Providers DeadmanProviders

		// Deadman is the SEPARATE alert path of the deadman group: its own
		// channel, its own credential, never shared with Providers (which
		// the company signals use). Zero value: the deadman endpoint keeps
		// using Providers, as before. See DeadmanChecks.
		Deadman DeadmanChecks

		// InternalChecks are internal infrastructure alerts pulled from
		// the alerting pipeline but NEVER forwarded as alerts — they are
		// displayed on the status page only, in the "platform" group.
		// Each check reads a specific alert (by name) and shows red when
		// firing, green when absent.
		InternalChecks []InternalCheck

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
		// Component is the product or project that owns this host, a
		// label the CALLER keeps beside the host (typically to look up
		// the StatusPath). It is carried through the Catalogue so a
		// consumer can keep one host type end to end, and it is NEVER
		// rendered: an endpoint is named after the host's first DNS
		// label and Env, and no rendered byte depends on Component.
		Component string
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

	// DeadmanChecks is the deadman group: the checks that say the
	// alerting path itself is alive, and the one channel they page.
	//
	// What it renders, all in the "platform" group, every DeadmanInterval
	// (two minutes), alerting after two consecutive failures and again
	// when resolved:
	//
	//   - deadman: the Watchdog is present in vmalert (always rendered);
	//   - alertmanager-watchdog: the Watchdog is ACTIVE in Alertmanager
	//     (when AlertmanagerWatchdog), read from AlertsRead.Host's
	//     `/api/v2/alerts`;
	//   - one check per NotFiring entry: that alert is NOT firing in
	//     vmalert (SlackNotificationsFailing, say).
	//
	// All pull; nothing here is pushed to the box.
	DeadmanChecks struct {
		// Post is where the deadman pages. Nil: the checks still render
		// and still show red/green, they just page nobody (a second
		// instance of the same page, which must not page twice).
		Post *ChatPost
		// AlertmanagerWatchdog adds the Alertmanager check.
		AlertmanagerWatchdog bool
		// NotFiring are alerts that must NOT be firing in vmalert.
		NotFiring []NotFiringCheck
	}

	// ChatPost is a Gatus `custom` alerting provider shaped for a chat
	// API that takes `Authorization: Bearer <token>` and a JSON body of
	// {"channel", "text"} (Slack's chat.postMessage). Gatus's own slack
	// provider only takes an incoming-webhook URL; a bot token needs this.
	ChatPost struct {
		// URL is the API endpoint, e.g. https://slack.com/api/chat.postMessage.
		URL string
		// TokenEnvKey is the Secrets.AlertURLs key whose value is the bot
		// token (${ALERT_URL_<KEY>}).
		TokenEnvKey string
		// Channel is the channel the body names. A chat API answers 200
		// even for a refusal ("not_in_channel"), which Gatus cannot see:
		// the bot must already be in the channel.
		Channel string
	}

	// NotFiringCheck is an alert that must not be firing in vmalert.
	NotFiringCheck struct {
		// Name is the Gatus endpoint name.
		Name string
		// AlertName is the alerting rule name, matched exactly.
		AlertName string
	}

	// InternalCheck is one internal infrastructure alert to display on the
	// status page but never forward as an alert. It reads the alerting
	// pipeline's /api/v1/alerts and shows red when a specific alert is
	// firing, green when absent.
	InternalCheck struct {
		// Name is the Gatus endpoint name for this check — a neutral label
		// like "slack-notifications" that describes what is being checked,
		// not a company or project name.
		Name string
		// AlertName is the alerting rule name to query (e.g.,
		// "SlackNotificationsFailing") — interpolated into the alerts-read
		// match[] filter as {alertname=%q}.
		AlertName string
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
		Type        string `yaml:"type"`
		Description string `yaml:"description,omitempty"`
		// FailureThreshold is written only when set (the deadman group's);
		// an ordinary probe keeps Gatus's own default of 3.
		FailureThreshold int `yaml:"failure-threshold,omitempty"`
		// SendOnResolved: a page that never says it is over leaves a red
		// message nobody can close.
		SendOnResolved bool `yaml:"send-on-resolved,omitempty"`
	}

	gatusAlerting struct {
		Slack     *gatusSlackAlerting     `yaml:"slack,omitempty"`
		Custom    *gatusCustomAlerting    `yaml:"custom,omitempty"`
		PagerDuty *gatusPagerDutyAlerting `yaml:"pagerduty,omitempty"`
	}

	gatusCustomAlerting struct {
		URL     string            `yaml:"url"`
		Method  string            `yaml:"method"`
		Body    string            `yaml:"body"`
		Headers map[string]string `yaml:"headers"`
		// Placeholders renames [ALERT_TRIGGERED_OR_RESOLVED] per state
		// (Gatus v5.37 `alerting.custom.placeholders`).
		Placeholders map[string]map[string]string `yaml:"placeholders,omitempty"`
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
// customer-facing signal, internal infrastructure checks, and the deadman,
// all as ordinary `endpoints:` entries — plus the security block
// Catalogue.Security carries, or none for a private/breakglass instance.
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
		alerts = append(alerts, gatusAlertRef{Type: "slack", SendOnResolved: true})
		alerting = ensureAlerting(alerting)
		alerting.Slack = &gatusSlackAlerting{WebhookURL: alertVar(c.Providers.SlackKey)}
	}

	if c.Providers.PagerDutyKey != "" {
		alerts = append(alerts, gatusAlertRef{Type: "pagerduty", SendOnResolved: true})
		alerting = ensureAlerting(alerting)
		alerting.PagerDuty = &gatusPagerDutyAlerting{IntegrationKey: alertVar(c.Providers.PagerDutyKey)}
	}

	deadmanAlerts := alerts

	if c.Deadman.Post != nil {
		var err error

		deadmanAlerts, alerting, err = deadmanAlerting(c.Deadman, alerting)
		if err != nil {
			return "", err
		}
	}

	endpoints := make([]gatusEndpoint, 0, len(c.PlatformHosts)+2*len(c.Companies)+len(c.InternalChecks)+1)

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

	for _, check := range c.InternalChecks {
		endpoints = append(endpoints, gatusEndpoint{
			Name:  check.Name,
			Group: "platform",
			URL: alertsReadURL(c.AlertsRead.Host,
				fmt.Sprintf(`{alertname=%q}`, check.AlertName)),
			Headers:    authHeader,
			Interval:   alertsReadInterval,
			Conditions: []string{statusCondition, alertsPresentCondition},
			// Internal checks are NEVER alerted on, even if Providers is
			// configured. They are displayed on the status page only.
			Alerts: nil,
		})
	}

	deadmanInt := alertsReadInterval
	if c.Deadman.Post != nil || c.Deadman.AlertmanagerWatchdog || len(c.Deadman.NotFiring) > 0 {
		deadmanInt = deadmanInterval
	}

	endpoints = append(endpoints, gatusEndpoint{
		Name:       "deadman",
		Group:      "platform",
		URL:        alertsReadURL(c.AlertsRead.Host, `{alertname="Watchdog"}`),
		Headers:    authHeader,
		Interval:   deadmanInt,
		Conditions: []string{statusCondition, alertsPresentCondition},
		Alerts:     deadmanAlerts,
	})

	if c.Deadman.AlertmanagerWatchdog {
		endpoints = append(endpoints, gatusEndpoint{
			Name:       "alertmanager-watchdog",
			Group:      "platform",
			URL:        alertmanagerWatchdogURL(c.AlertsRead.Host),
			Headers:    authHeader,
			Interval:   deadmanInterval,
			Conditions: []string{statusCondition, alertmanagerAlertsPresentCondition},
			Alerts:     deadmanAlerts,
		})
	}

	for _, check := range c.Deadman.NotFiring {
		endpoints = append(endpoints, gatusEndpoint{
			Name:  check.Name,
			Group: "platform",
			URL: alertsReadURL(c.AlertsRead.Host,
				fmt.Sprintf(`{alertname=%q}`, check.AlertName)),
			Headers:    authHeader,
			Interval:   deadmanInterval,
			Conditions: []string{statusCondition, alertsAbsentCondition},
			Alerts:     deadmanAlerts,
		})
	}

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

	if len(alerts) == 0 && c.Deadman.Post == nil {
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

// alertmanagerWatchdogURL is Alertmanager's own listing of the ACTIVE
// Watchdog: filtered by name, and neither silenced nor inhibited, so a
// silence or an inhibition that swallowed the heartbeat turns the check
// red too.
func alertmanagerWatchdogURL(host string) string {
	u := url.URL{Scheme: "https", Host: host, Path: alertmanagerWatchdogPath}
	q := u.Query()
	q.Set("filter", `alertname="Watchdog"`)
	q.Set("active", "true")
	q.Set("silenced", "false")
	q.Set("inhibited", "false")
	u.RawQuery = q.Encode()

	return u.String()
}

// deadmanAlerting adds the deadman's own `custom` provider to alerting
// and returns the alert refs the deadman group's endpoints carry: one
// `custom` ref that fires after deadmanFailureThreshold failures and says
// when it resolves. It refuses a post that would render a provider Gatus
// refuses or one that leaks a credential.
func deadmanAlerting(d DeadmanChecks, alerting *gatusAlerting) ([]gatusAlertRef, *gatusAlerting, error) {
	p := d.Post

	if u, err := url.Parse(p.URL); err != nil || u.Scheme != "https" || u.Host == "" || u.User != nil || u.RawQuery != "" {
		return nil, nil, fmt.Errorf("statusbox: RenderGatus: Deadman.Post.URL %q must be an https URL with no user info and no query", p.URL)
	}

	if !alertKeyRE.MatchString(p.TokenEnvKey) {
		return nil, nil, fmt.Errorf("statusbox: RenderGatus: Deadman.Post.TokenEnvKey %q is not a valid Secrets.AlertURLs key", p.TokenEnvKey)
	}

	if !postChannelRE.MatchString(p.Channel) {
		return nil, nil, fmt.Errorf("statusbox: RenderGatus: Deadman.Post.Channel %q must be a channel name or id "+
			"(letters, digits, '-', '_', optional leading '#')", p.Channel)
	}

	seen := map[string]bool{"deadman": true, "alertmanager-watchdog": d.AlertmanagerWatchdog}
	for _, n := range d.NotFiring {
		if !heartbeatLikeNameRE.MatchString(n.Name) || seen[n.Name] {
			return nil, nil, fmt.Errorf("statusbox: RenderGatus: Deadman.NotFiring name %q is empty, not lower-case letters, digits and hyphens, or a duplicate", n.Name)
		}

		seen[n.Name] = true

		if n.AlertName == "" {
			return nil, nil, fmt.Errorf("statusbox: RenderGatus: Deadman.NotFiring %q has no AlertName", n.Name)
		}
	}

	if alerting == nil {
		alerting = &gatusAlerting{}
	}

	alerting.Custom = &gatusCustomAlerting{
		URL:    p.URL,
		Method: "POST",
		// Gatus substitutes [ALERT_TRIGGERED_OR_RESOLVED] LAST, so its
		// value cannot carry other placeholders, and the per-alert
		// description is shared by both states. The state-specific
		// sentence therefore lives in the placeholder values and the
		// description stays out of the body.
		Body: fmt.Sprintf(`{"channel":%q,"text":"[ENDPOINT_GROUP]/[ENDPOINT_NAME] - [ALERT_TRIGGERED_OR_RESOLVED]"}`,
			p.Channel),
		Placeholders: map[string]map[string]string{
			"ALERT_TRIGGERED_OR_RESOLVED": {
				"TRIGGERED": "TRIGGERED: " + deadmanTriggeredText,
				"RESOLVED":  "RESOLVED: the check is passing again",
			},
		},
		Headers: map[string]string{
			"Authorization": "Bearer " + alertVar(p.TokenEnvKey),
			"Content-Type":  "application/json; charset=utf-8",
		},
	}

	return []gatusAlertRef{{
		Type:             "custom",
		Description:      deadmanTriggeredText,
		FailureThreshold: deadmanFailureThreshold,
		SendOnResolved:   true,
	}}, alerting, nil
}

// deadmanTriggeredText is the sentence a TRIGGERED post carries (and the
// alert's description); it is only true while the check is failing, so the
// RESOLVED post must not reuse it.
const deadmanTriggeredText = "the alerting path may be down: this check has failed twice in a row"

var (
	postChannelRE       = regexp.MustCompile(`^#?[A-Za-z0-9][A-Za-z0-9_-]*$`)
	heartbeatLikeNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)
)

package status

import (
	"sort"
	"strings"

	"github.com/truvity/observability/pkg/statusbox"
)

const (
	// AlertsReadTokenKey is the AlertURLs key of the box's one alerts-read
	// bearer: an environment variable Gatus's own ${...} substitution reads,
	// presented in the alerts-read endpoints' Authorization header.
	AlertsReadTokenKey = "ops_alerts_read_token"

	// DeadmanSlackTokenKey is the AlertURLs key of the deadman chat App's bot
	// token: the Gatus `custom` provider posts with it. The box cannot fetch
	// anything from the cluster when it pages, so the token is baked into its
	// cloud-init like every other secret it holds.
	DeadmanSlackTokenKey = "deadman_slack_token"

	// DeadmanTelegramTokenKey is the AlertURLs key of the optional Telegram
	// alert channel's bot token (Gatus's native `alerting.telegram`). It goes
	// with DeadmanTelegramChatIDKey: both or neither.
	DeadmanTelegramTokenKey = "deadman_telegram_token"

	// DeadmanTelegramChatIDKey is the AlertURLs key of the Telegram chat id.
	DeadmanTelegramChatIDKey = "deadman_telegram_chat_id"

	// OIDCClientSecretEnvKey is the statusbox.Secrets.Env map key the Gatus
	// config references as ${OIDC_CLIENT_SECRET}: the whole variable name,
	// unlike an AlertURLs key.
	OIDCClientSecretEnvKey = "OIDC_CLIENT_SECRET"

	// deadmanPostURL is the chat API the deadman pages through.
	deadmanPostURL = "https://slack.com/api/chat.postMessage"

	// opsStoragePath is Gatus's storage.path for both ops instances.
	opsStoragePath = "/data/ops.db"
)

type (
	// HostGroup is one routable group of public hostnames, with the product
	// that owns it already resolved by the caller (the estate's own catalog
	// and project rows are the caller's; this package knows neither).
	HostGroup struct {
		// Cluster is the environment the hosts are served from.
		Cluster string
		// Hosts are the group's hostnames; wildcards are skipped.
		Hosts []string
		// Company is the owning entity's code, empty for a platform group
		// that belongs to no company.
		Company string
		// Component is the product name a viewer sees instead of the host.
		Component string
		// StatusPath is the product's probe path on this cluster, empty for
		// the default.
		StatusPath string
		// ExpectStatus is the status StatusPath must answer, 0 for 200.
		ExpectStatus int
	}

	// Entity is one entity's page: a code and the name a viewer sees.
	Entity struct {
		Code        string
		DisplayName string
	}

	// OIDC is the sign-in of the public instance.
	OIDC struct {
		IssuerURL string
		ClientID  string
	}

	// CatalogueInputs is the whole input of one ops instance's page.
	CatalogueInputs struct {
		// PlatformHosts are the hosts no company owns.
		PlatformHosts []string
		// HostProbes optionally overrides the probe of a platform host,
		// keyed by hostname. A host without an entry is probed as before.
		HostProbes map[string]statusbox.Probe
		// ByCompany buckets every company-owned host under its code.
		ByCompany map[string][]statusbox.CompanyHost
		// Entities are the entity pages, in the order they render.
		Entities []Entity
		// AlertsReadHost is where the alerts-read path answers.
		AlertsReadHost string
		// DeadmanChannel is where the deadman pages.
		DeadmanChannel string
		// Telegram adds the Telegram channel (keys above) to every endpoint
		// the deadman pages through. Like the chat post it only takes effect
		// on the instance that pages (not Public).
		Telegram bool
		// Public says this is the public instance: it shows every check and
		// pages nobody, behind OIDC when PublicHostname is set.
		Public         bool
		PublicHostname string
		OIDC           OIDC
	}
)

// IsWildcardHostname reports whether host is a wildcard domain: a valid entry
// in a tunnel's ingress rule but never a hostname a probe can literally dial.
func IsWildcardHostname(host string) bool {
	return strings.HasPrefix(host, "*.")
}

// PlatformHosts returns every hostname of a group that names no company,
// wildcards excluded, deduplicated and sorted. A host of a company group is
// deliberately NOT here: it already has its own probe in that company's group,
// with a status-path-aware condition a bare platform probe cannot express.
func PlatformHosts(groups []HostGroup) []string {
	set := map[string]bool{}

	for _, g := range groups {
		if g.Company != "" {
			continue
		}

		for _, host := range g.Hosts {
			if !IsWildcardHostname(host) {
				set[host] = true
			}
		}
	}

	out := make([]string, 0, len(set))
	for host := range set {
		out = append(out, host)
	}

	sort.Strings(out)

	return out
}

// HostsByCompany buckets every company-owned host under its company code, each
// paired with its component, cluster and status path, hosts sorted. A platform
// group names no company and is left off every entity's page: a customer-facing
// status page shows the products a company sells, not the platform that runs
// them. PlatformHosts and HostsByCompany split the SAME groups on the SAME
// field, so a host falls into exactly one of them.
func HostsByCompany(groups []HostGroup) map[string][]statusbox.CompanyHost {
	type hostInfo struct {
		component, env, statusPath string
		expectStatus               int
	}

	byCompany := map[string]map[string]hostInfo{}

	for _, g := range groups {
		if g.Company == "" {
			continue
		}

		if byCompany[g.Company] == nil {
			byCompany[g.Company] = map[string]hostInfo{}
		}

		for _, host := range g.Hosts {
			if !IsWildcardHostname(host) {
				byCompany[g.Company][host] = hostInfo{g.Component, g.Cluster, g.StatusPath, g.ExpectStatus}
			}
		}
	}

	out := make(map[string][]statusbox.CompanyHost, len(byCompany))

	for company, hosts := range byCompany {
		names := make([]string, 0, len(hosts))
		for host := range hosts {
			names = append(names, host)
		}

		sort.Strings(names)

		list := make([]statusbox.CompanyHost, 0, len(names))

		for _, host := range names {
			info := hosts[host]
			list = append(list, statusbox.CompanyHost{Host: host, Component: info.component, Env: info.env, StatusPath: info.statusPath, ExpectStatus: info.expectStatus})
		}

		out[company] = list
	}

	return out
}

// OpsCatalogue is the library input of one ops instance.
//
// The combined ops page is ONE input rendered TWICE: `ops`, Public, behind
// Gatus's own security.oidc, and `ops-breakglass`, private and tailnet-only
// with no auth, for the day the issuer itself is down. They differ in Security
// and in WHO PAGES: ops-breakglass is the one instance that pages, because
// Gatus refuses to start a security.oidc block it cannot complete discovery
// for, so the Public instance is down exactly when the issuer is, and a deadman
// that lives there fails with the thing it watches. Two instances that each
// paged would also page twice. Providers stay empty: the company signals page
// nobody. Security is nil when Public is false or PublicHostname is empty.
func OpsCatalogue(in CatalogueInputs) statusbox.Catalogue { //nolint:misspell // the library's own type name
	companies := make([]statusbox.Company, 0, len(in.Entities))

	for _, e := range in.Entities {
		companies = append(companies, statusbox.Company{
			Code:        e.Code,
			DisplayName: e.DisplayName,
			Hosts:       in.ByCompany[e.Code],
		})
	}

	// Every instance SHOWS the deadman checks; only ops-breakglass has a Post,
	// so only it pages.
	deadman := statusbox.DeadmanChecks{
		AlertmanagerWatchdog: true,
		NotFiring:            []statusbox.NotFiringCheck{{Name: "slack-notifications", AlertName: "SlackNotificationsFailing"}},
	}

	if !in.Public {
		deadman.Post = &statusbox.ChatPost{
			URL:         deadmanPostURL,
			TokenEnvKey: DeadmanSlackTokenKey,
			Channel:     in.DeadmanChannel,
		}
	}

	var telegram *statusbox.TelegramProvider
	if in.Telegram && !in.Public {
		telegram = &statusbox.TelegramProvider{TokenKey: DeadmanTelegramTokenKey, IDKey: DeadmanTelegramChatIDKey}
	}

	var security *statusbox.OIDCSecurity
	if in.Public && in.PublicHostname != "" {
		security = &statusbox.OIDCSecurity{
			IssuerURL:      in.OIDC.IssuerURL,
			PublicHostname: in.PublicHostname,
			ClientID:       in.OIDC.ClientID,
		}
	}

	return statusbox.Catalogue{ //nolint:misspell // the library's own type name
		PlatformHosts: in.PlatformHosts,
		HostProbes:    in.HostProbes,
		Companies:     companies,
		AlertsRead:    statusbox.AlertsRead{Host: in.AlertsReadHost, TokenEnvKey: AlertsReadTokenKey},
		Deadman:       deadman,
		Telegram:      telegram,
		Security:      security,
		StoragePath:   opsStoragePath,
	}
}

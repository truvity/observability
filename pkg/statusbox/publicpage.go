// The public, narrow page: one Gatus instance a company's customers read
// without signing in. It is the opposite of RenderGatus's combined page in
// every way that matters for who may see it: no platform hosts, no internal
// hostnames, no deadman, no internal checks, no alert providers and no
// security block. What is left is a list of the customer-facing hostnames,
// grouped under the service names customers know them by, probed from
// outside.

package statusbox

import (
	"errors"
	"fmt"
	"strings"
)

type (
	// PublicPage is the whole input RenderPublicPage needs. Like Catalogue,
	// nothing here is derived by this package: which hostnames are customer
	// facing, and for which environment, is the caller's to decide.
	PublicPage struct {
		// Title is the page's own heading and browser title (Gatus's
		// ui.header and ui.title), e.g. "Example status". Empty keeps
		// Gatus's defaults.
		Title string

		// Groups are the services the page shows, in the order they
		// render. Every host of every group must be one the page's
		// readers may see: the page carries no security block.
		Groups []PublicGroup

		// StoragePath is Gatus's own `storage.path`, a SQLite file of its
		// own (never another instance's).
		StoragePath string
	}

	// PublicGroup is one service on a public page: the name customers know
	// it by (Gatus's `group:`) and the hostnames it answers on.
	PublicGroup struct {
		// DisplayName is prose, the group label.
		DisplayName string
		// Hosts are probed exactly like a company's hosts on the combined
		// page (StatusPath, ExpectStatus and the lenient fallback without a
		// StatusPath; see probeConditions), and each endpoint is named
		// after its full hostname: the name a customer calls. Env and
		// Component are not rendered.
		Hosts []CompanyHost
	}
)

// RenderPublicPage builds the Gatus v5 YAML of a public, narrow page: one
// endpoint per host, grouped by service, every minute, with the
// certificate-expiry condition. It renders no `security:` (the page is
// public by design), no `alerting:` and no alerts-read endpoint: a page
// customers read must not page anyone and must not name an internal host,
// even in a URL Gatus's API would return.
//
// It refuses a page with no host at all (Gatus refuses to boot on one), a
// group with no display name or no host, a host that is empty, a wildcard,
// carries a scheme, path or port, a host listed twice, a StatusPath that
// does not start with "/", and an empty StoragePath.
func RenderPublicPage(p PublicPage) (string, error) {
	if err := p.validate(); err != nil {
		return "", err
	}

	var endpoints []gatusEndpoint

	for _, g := range p.Groups {
		for _, host := range g.Hosts {
			endpoints = append(endpoints, gatusEndpoint{
				Name:       host.Host,
				Group:      g.DisplayName,
				URL:        probeURL(host),
				Interval:   companyProbeInterval,
				Conditions: probeConditions(host),
			})
		}
	}

	cfg := gatusConfig{
		Storage:   gatusStorage{Type: "sqlite", Path: p.StoragePath},
		Endpoints: endpoints,
	}

	if p.Title != "" {
		cfg.UI = &gatusUI{Title: p.Title, Header: p.Title}
	}

	return marshalGatusConfig(cfg)
}

func (p PublicPage) validate() error {
	var errs []error

	if strings.TrimSpace(p.StoragePath) == "" {
		errs = append(errs, errors.New("statusbox: RenderPublicPage: StoragePath is empty"))
	}

	if strings.ContainsAny(p.Title, "\r\n") {
		errs = append(errs, errors.New("statusbox: RenderPublicPage: Title spans several lines"))
	}

	seen := map[string]string{}
	hosts := 0

	for i, g := range p.Groups {
		where := fmt.Sprintf("statusbox: RenderPublicPage: Groups[%d]", i)

		if strings.TrimSpace(g.DisplayName) == "" {
			errs = append(errs, fmt.Errorf("%s: DisplayName is empty: it is the group label customers read", where))
		} else {
			where = fmt.Sprintf("statusbox: RenderPublicPage: group %q", g.DisplayName)
		}

		if len(g.Hosts) == 0 {
			errs = append(errs, fmt.Errorf("%s: no hosts: a service with nothing to probe renders an empty group", where))
		}

		for _, h := range g.Hosts {
			hosts++

			if err := publicHostProblem(h); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", where, err))

				continue
			}

			if other, ok := seen[h.Host]; ok {
				errs = append(errs, fmt.Errorf("%s: host %q is also listed under %q: one hostname is one endpoint", where, h.Host, other))

				continue
			}

			seen[h.Host] = g.DisplayName
		}
	}

	if hosts == 0 {
		errs = append(errs, errors.New("statusbox: RenderPublicPage: no hosts at all: a Gatus Config with no endpoints refuses to boot"))
	}

	return errors.Join(errs...)
}

// publicHostProblem is why h cannot be probed as a bare https host, or nil.
func publicHostProblem(h CompanyHost) error {
	switch {
	case h.Host == "":
		return errors.New("a host is empty")
	case strings.HasPrefix(h.Host, "*."):
		return fmt.Errorf("host %q is a wildcard: it would fail every probe, forever", h.Host)
	case strings.ContainsAny(h.Host, ":/?#@ \t"):
		return fmt.Errorf("host %q is not a bare hostname (no scheme, port, path or user info)", h.Host)
	case h.StatusPath != "" && !strings.HasPrefix(h.StatusPath, "/"):
		return fmt.Errorf("host %q: StatusPath %q does not start with /", h.Host, h.StatusPath)
	}

	return nil
}

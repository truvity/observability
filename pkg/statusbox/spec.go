package statusbox

import (
	"errors"
	"fmt"
	"regexp"
	"sort"
)

// EntityCodeRE is what an entity code may look like. The code becomes the Gatus
// instance name (gatus-<code>, Instance.Name) and an SSM path segment, so it
// follows the shape Instance.Name has: a name that needs escaping is a name
// nobody should have chosen.
var EntityCodeRE = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// Spec is what an estate declares about the pages one box serves, before it is
// turned into Instances: it is checked on its own so that a mistake is found
// when the estate's configuration is loaded, and not at deploy time by
// Args.validate.
type Spec struct {
	// PublicHostname, when set, says the box serves a public page that signs in
	// through Gatus's security.oidc; OIDCIssuerURL and OIDCClientID are then
	// required.
	PublicHostname string
	OIDCIssuerURL  string
	OIDCClientID   string

	// Entities are the legal entities, by code. Each is one Gatus instance.
	Entities map[string]EntitySpec

	// TunnelHosts are the hostnames the box's tunnel carries today: each must be
	// an entity's Hostname or one of its Pages' Hostname, because a tunnel can
	// only carry a page the box renders.
	TunnelHosts []string
}

// EntitySpec is one entity's page.
type EntitySpec struct {
	// DisplayName is prose, never interpolated into a resource name or a script.
	DisplayName string
	// Hostname is the public address of the entity's page: what a tunnel ingress
	// rule and a DNS record point at. Optional when Pages is set.
	Hostname string
	// Pages are the entity's public, narrow pages, one per environment (each
	// rendered by RenderPublicPage): an entity whose customers use several
	// environments gets a page per environment, each at its own hostname.
	Pages []PageSpec
}

// PageSpec is one public page of an entity: the hostname it is served at and
// the environment whose customer-facing hosts it shows.
type PageSpec struct {
	// Hostname is the page's public address, e.g. status.example.com.
	Hostname string
	// Environment names the environment the page shows, e.g. prod. It has the
	// shape of an entity code, because a backend names the page's Gatus
	// instance <entity code>-<environment> (PageInstanceName).
	Environment string
}

// PageInstanceName is the Gatus instance name of an entity's page for one
// environment: <code>-<environment>.
func PageInstanceName(code, environment string) string { return code + "-" + environment }

// Validate reports every problem it can find, not just the first. The messages
// name the fields as the Spec's own (entities.<code>.hostname, tunnel_hosts[i]).
func (s Spec) Validate() error {
	var errs []error

	if s.PublicHostname != "" {
		if s.OIDCIssuerURL == "" {
			errs = append(errs, errors.New("public_hostname is set but oidc_issuer_url is empty: "+
				"the public page signs in through Gatus's security.oidc, which needs an issuer"))
		}

		if s.OIDCClientID == "" {
			errs = append(errs, errors.New("public_hostname is set but oidc_client_id is empty: "+
				"the client this page signs in as"))
		}
	}

	if len(s.Entities) == 0 {
		errs = append(errs, errors.New("entities is empty: a box with no page to serve is not a box worth provisioning"))
	}

	codes := make([]string, 0, len(s.Entities))
	for code := range s.Entities {
		codes = append(codes, code)
	}

	sort.Strings(codes)

	known := make(map[string]bool, len(s.Entities))
	pageHosts := map[string]string{}

	for _, code := range codes {
		e := s.Entities[code]
		where := "entities." + code

		if !EntityCodeRE.MatchString(code) {
			errs = append(errs, fmt.Errorf("%s: key %q is not a valid entity code (%s): "+
				"it becomes the Gatus instance name gatus-%s and an SSM path segment", where, code, EntityCodeRE, code))
		}

		if e.DisplayName == "" {
			errs = append(errs, fmt.Errorf("%s.display_name is required", where))
		}

		switch {
		case e.Hostname != "":
			known[e.Hostname] = true
		case len(e.Pages) == 0:
			errs = append(errs, fmt.Errorf("%s.hostname is required (or pages): a public page with no hostname is one "+
				"Args.validate refuses at deploy time anyway, better to catch it here", where))
		}

		errs = append(errs, s.validatePages(where, code, e.Pages, known, pageHosts)...)
	}

	for i, host := range s.TunnelHosts {
		switch {
		case host == "":
			errs = append(errs, fmt.Errorf("tunnel_hosts[%d] is empty", i))
		case !known[host]:
			errs = append(errs, fmt.Errorf("tunnel_hosts[%d] %q names no entities.*.hostname or entities.*.pages[*].hostname: "+
				"the tunnel can only carry a page this box actually renders", i, host))
		}
	}

	return errors.Join(errs...)
}

// validatePages checks one entity's pages: each has a hostname and an
// environment, no two pages anywhere share a hostname, no page uses the
// combined page's PublicHostname, an entity has one page per environment, and
// the instance name <code>-<environment> is a valid one. known collects the
// page hostnames for the tunnel_hosts check; pageHosts is shared across
// entities.
func (s Spec) validatePages(where, code string, pages []PageSpec, known map[string]bool, pageHosts map[string]string) []error {
	var errs []error

	envs := map[string]bool{}

	for i, p := range pages {
		at := fmt.Sprintf("%s.pages[%d]", where, i)

		switch {
		case p.Hostname == "":
			errs = append(errs, fmt.Errorf("%s.hostname is required", at))
		case p.Hostname == s.PublicHostname:
			errs = append(errs, fmt.Errorf("%s.hostname %q is public_hostname, the combined page's own address", at, p.Hostname))
		case pageHosts[p.Hostname] != "":
			errs = append(errs, fmt.Errorf("%s.hostname %q is also %s.hostname: one hostname serves one page", at, p.Hostname, pageHosts[p.Hostname]))
		default:
			pageHosts[p.Hostname] = at
			known[p.Hostname] = true
		}

		switch {
		case !EntityCodeRE.MatchString(p.Environment):
			errs = append(errs, fmt.Errorf("%s.environment %q is not a valid environment name (%s): "+
				"the page's Gatus instance is named %s", at, p.Environment, EntityCodeRE, PageInstanceName(code, "<environment>")))
		case envs[p.Environment]:
			errs = append(errs, fmt.Errorf("%s.environment %q has a page already: one page per environment", at, p.Environment))
		default:
			envs[p.Environment] = true
		}
	}

	return errs
}

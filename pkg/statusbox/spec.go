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
	// an entity's Hostname, because a tunnel can only carry a page the box
	// renders.
	TunnelHosts []string
}

// EntitySpec is one entity's page.
type EntitySpec struct {
	// DisplayName is prose, never interpolated into a resource name or a script.
	DisplayName string
	// Hostname is the public address of the entity's page: what a tunnel ingress
	// rule and a DNS record point at.
	Hostname string
}

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

		if e.Hostname == "" {
			errs = append(errs, fmt.Errorf("%s.hostname is required: a public page with no hostname is one "+
				"Args.validate refuses at deploy time anyway, better to catch it here", where))
		} else {
			known[e.Hostname] = true
		}
	}

	for i, host := range s.TunnelHosts {
		switch {
		case host == "":
			errs = append(errs, fmt.Errorf("tunnel_hosts[%d] is empty", i))
		case !known[host]:
			errs = append(errs, fmt.Errorf("tunnel_hosts[%d] %q names no entities.*.hostname: "+
				"the tunnel can only carry a page this box actually renders", i, host))
		}
	}

	return errors.Join(errs...)
}

package statusbox_test

import (
	"strings"
	"testing"

	"github.com/truvity/observability/pkg/statusbox"
)

func TestSpecValidate(t *testing.T) {
	ok := statusbox.Spec{
		PublicHostname: "status.example.com",
		OIDCIssuerURL:  "https://issuer.example.com",
		OIDCClientID:   "status",
		Entities:       map[string]statusbox.EntitySpec{"acme": {DisplayName: "Acme", Hostname: "status.example.com"}},
		TunnelHosts:    []string{"status.example.com"},
	}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid spec: %v", err)
	}

	for name, tc := range map[string]struct {
		mut  func(*statusbox.Spec)
		want string
	}{
		"issuer":      {func(s *statusbox.Spec) { s.OIDCIssuerURL = "" }, "oidc_issuer_url is empty"},
		"client":      {func(s *statusbox.Spec) { s.OIDCClientID = "" }, "oidc_client_id is empty"},
		"no entities": {func(s *statusbox.Spec) { s.Entities = nil; s.TunnelHosts = nil }, "entities is empty"},
		"code": {func(s *statusbox.Spec) {
			s.Entities = map[string]statusbox.EntitySpec{"Bad Code": {DisplayName: "x", Hostname: "h"}}
			s.TunnelHosts = nil
		}, "not a valid entity code"},
		"name": {func(s *statusbox.Spec) {
			s.Entities = map[string]statusbox.EntitySpec{"acme": {Hostname: "h"}}
			s.TunnelHosts = nil
		}, "entities.acme.display_name is required"},
		"hostname": {func(s *statusbox.Spec) {
			s.Entities = map[string]statusbox.EntitySpec{"acme": {DisplayName: "x"}}
			s.TunnelHosts = nil
		}, "entities.acme.hostname is required"},
		"tunnel": {
			func(s *statusbox.Spec) { s.TunnelHosts = []string{"status.nowhere.example"} },
			`tunnel_hosts[0] "status.nowhere.example" names no entities.*.hostname`,
		},
		"empty host": {func(s *statusbox.Spec) { s.TunnelHosts = []string{""} }, "tunnel_hosts[0] is empty"},
	} {
		s := ok
		tc.mut(&s)

		err := s.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err %v, want %q", name, err, tc.want)
		}
	}
}

func TestSpecValidatePages(t *testing.T) {
	pages := func() statusbox.Spec {
		return statusbox.Spec{
			PublicHostname: "status.example.org",
			OIDCIssuerURL:  "https://issuer.example.com",
			OIDCClientID:   "status",
			Entities: map[string]statusbox.EntitySpec{"acme": {DisplayName: "Acme", Pages: []statusbox.PageSpec{
				{Hostname: "status.example.com", Environment: "prod"},
				{Hostname: "status.example.net", Environment: "stage"},
			}}},
			TunnelHosts: []string{"status.example.com", "status.example.net"},
		}
	}

	if err := pages().Validate(); err != nil {
		t.Fatalf("valid spec with pages and no entity hostname: %v", err)
	}

	for name, tc := range map[string]struct {
		mut  func(*statusbox.Spec)
		want string
	}{
		"no hostname": {func(s *statusbox.Spec) { s.Entities["acme"].Pages[0].Hostname = "" }, "entities.acme.pages[0].hostname is required"},
		"combined": {
			func(s *statusbox.Spec) { s.Entities["acme"].Pages[0].Hostname = "status.example.org" },
			"entities.acme.pages[0].hostname \"status.example.org\" is public_hostname",
		},
		"twice": {
			func(s *statusbox.Spec) { s.Entities["acme"].Pages[1].Hostname = "status.example.com" },
			"entities.acme.pages[1].hostname \"status.example.com\" is also entities.acme.pages[0].hostname",
		},
		"bad env": {
			func(s *statusbox.Spec) { s.Entities["acme"].Pages[0].Environment = "Prod" },
			"entities.acme.pages[0].environment \"Prod\" is not a valid environment name",
		},
		"no env": {
			func(s *statusbox.Spec) { s.Entities["acme"].Pages[0].Environment = "" },
			"entities.acme.pages[0].environment \"\" is not a valid environment name",
		},
		"same env": {
			func(s *statusbox.Spec) { s.Entities["acme"].Pages[1].Environment = "prod" },
			"entities.acme.pages[1].environment \"prod\" has a page already",
		},
		"tunnel": {
			func(s *statusbox.Spec) { s.TunnelHosts = []string{"status.nowhere.example"} },
			"names no entities.*.hostname or entities.*.pages[*].hostname",
		},
	} {
		s := pages()
		tc.mut(&s)

		err := s.Validate()
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err %v, want %q", name, err, tc.want)
		}
	}

	if got := statusbox.PageInstanceName("acme", "prod"); got != "acme-prod" {
		t.Errorf("PageInstanceName: %q", got)
	}
}

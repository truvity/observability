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

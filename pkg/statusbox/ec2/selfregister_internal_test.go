package ec2

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/truvity/asg"
)

var testDNSRole = "arn:" + partition + ":iam::" + strings.Repeat("1", 12) + ":role/dns-register"

const testZone = "Z0EXAMPLE12345"

func selfRegisterArgs() Args {
	a := validArgs()
	a.SelfRegister = &SelfRegisterArgs{RoleARN: testDNSRole, HostedZoneID: testZone, RecordName: "box.example.test"}

	return a
}

// fullArgs is the box with everything on: the tunnel, SSH, a fixed host key,
// self-registration and the dead-man ping.
func fullArgs() Args {
	a := opksshOnlyArgs()
	a.SelfRegister = selfRegisterArgs().SelfRegister
	a.PingURLParameter = "/acme/status/ping-url"

	return a
}

func TestSelfRegisterNilLeavesTheRenderWithout(t *testing.T) {
	plain := cloudConfig(t, validArgs())
	require.NotContains(t, plain, "self-register")
	require.NotContains(t, plain, "DNS_RECORD")
	require.NotContains(t, plain, "sts:AssumeRole")
}

func TestSelfRegisterIsAnInServiceStepAfterTheHook(t *testing.T) {
	files := writtenFiles(t, selfRegisterArgs())

	conf := files["/etc/asg/self-register.conf"]
	for _, want := range []string{"DNS_ROLE_ARN='" + testDNSRole + "'", "DNS_ZONE_ID='" + testZone + "'", "DNS_RECORD='box.example.test'", "DNS_TTL=60"} {
		require.Contains(t, conf, want)
	}

	require.Contains(t, files["/usr/local/lib/asg/self-register.sh"], "UPSERT")
	require.Contains(t, files["/etc/systemd/system/asg-self-register.service"], "After=network-online.target bootstrap-ready.service")
	require.Contains(t, files["/usr/local/lib/asg/in-service.d/02-self-register.sh"], "systemctl start --no-block asg-self-register.service")

	a := selfRegisterArgs()
	a.SelfRegister.TTL = 120
	require.Contains(t, writtenFiles(t, a)["/etc/asg/self-register.conf"], "DNS_TTL=120")
}

// TestSelfRegisterRolePolicyHasExactlyOneAssumeRole checks the contribution
// the library turns into a statement of the role.
func TestSelfRegisterRolePolicyHasExactlyOneAssumeRole(t *testing.T) {
	doc, err := asg.Document(compose(t, selfRegisterArgs()).Contribution.Statements)
	require.NoError(t, err)

	var p struct {
		Statement []struct {
			Action   []string
			Resource []string
		}
	}
	require.NoError(t, json.Unmarshal([]byte(doc), &p))

	var found int

	for _, st := range p.Statement {
		for _, act := range st.Action {
			if act == "sts:AssumeRole" {
				found++

				require.Equal(t, []string{testDNSRole}, st.Resource)
			}
		}
	}

	require.Equal(t, 1, found)
}

func TestSelfRegisterRefusals(t *testing.T) {
	for name, mut := range map[string]func(*SelfRegisterArgs){
		"role": func(s *SelfRegisterArgs) { s.RoleARN = "arn:" + partition + ":iam::123:role/x" },
		"wildcard": func(s *SelfRegisterArgs) {
			s.RoleARN = "arn:" + partition + ":iam::" + strings.Repeat("1", 12) + ":role/*"
		},
		"zone":      func(s *SelfRegisterArgs) { s.HostedZoneID = "/hostedzone/Z1" },
		"record":    func(s *SelfRegisterArgs) { s.RecordName = "Box.Example." },
		"record2":   func(s *SelfRegisterArgs) { s.RecordName = "*.example.test" },
		"ttl":       func(s *SelfRegisterArgs) { s.TTL = -1 },
		"ttl-large": func(s *SelfRegisterArgs) { s.TTL = 999999 },
	} {
		t.Run(name, func(t *testing.T) {
			a := selfRegisterArgs()
			mut(a.SelfRegister)
			require.Error(t, a.validate())
		})
	}

	require.NoError(t, selfRegisterArgs().validate())
}

func TestUserDataWithEverythingIsWithinTheLimit(t *testing.T) {
	fakeRelease(t)

	plain, err := validArgs().UserData("status")
	require.NoError(t, err)

	full, err := fullArgs().UserData("status")
	require.NoError(t, err)

	t.Logf("user-data bytes: %d plain, %d with everything on (limit %d)", plain.Size, full.Size, userDataLimit)
	require.Less(t, full.Size, userDataLimit)
}

package ec2

import (
	"encoding/json"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/truvity/observability/pkg/statusbox"
)

var testDNSRole = "arn:" + partition + ":iam::" + strings.Repeat("1", 12) + ":role/dns-register"

const testZone = "Z0EXAMPLE12345"

func selfRegisterArgs() Args {
	a := validArgs()
	a.SelfRegister = &SelfRegisterArgs{RoleARN: testDNSRole, HostedZoneID: testZone, RecordName: "box.example.test"}

	return a
}

func TestSelfRegisterNilLeavesTheRenderUnchanged(t *testing.T) {
	plain, err := validArgs().bootstrap(namesFor("status"), fakeChecksums())
	require.NoError(t, err)
	require.NotContains(t, plain, "self-register")
	require.NotContains(t, plain, "SB_DNS_")
	require.NotContains(t, validArgs().instancePolicy(namesFor("status"), "eu-west-1", "x"), "sts:AssumeRole")
}

func TestSelfRegisterBootstrapCarriesTheStep(t *testing.T) {
	got, err := selfRegisterArgs().bootstrap(namesFor("status"), fakeChecksums())
	require.NoError(t, err)

	for _, want := range []string{
		"SB_DNS_ROLE_ARN='" + testDNSRole + "'",
		"SB_DNS_ZONE_ID='" + testZone + "'",
		"SB_DNS_RECORD='box.example.test'",
		"SB_DNS_TTL='60'",
		"cat > /usr/local/sbin/statusbox-self-register <<'STATUSBOX_SELF_REGISTER'",
		"ExecStartPost=-/usr/bin/timeout 120 /usr/local/sbin/statusbox-self-register",
		`\"Action\":\"UPSERT\"`,
	} {
		require.Contains(t, got, want)
	}

	// Written before the install phase, whose daemon-reload picks the drop-in up.
	require.Less(t, strings.Index(got, "statusbox-boot.service.d/self-register.conf"), strings.Index(got, "exec /usr/local/sbin/statusbox-setup install"))

	a := selfRegisterArgs()
	a.SelfRegister.TTL = 120
	got, err = a.bootstrap(namesFor("status"), fakeChecksums())
	require.NoError(t, err)
	require.Contains(t, got, "SB_DNS_TTL='120'")
}

func TestSelfRegisterRolePolicyHasExactlyOneAssumeRole(t *testing.T) {
	doc := selfRegisterArgs().instancePolicy(namesFor("status"), "eu-west-1", strings.Repeat("7", 12))

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

func TestSelfRegisterScriptParsesAndSkipsOutsideService(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("no bash")
	}

	require.NoError(t, exec.Command(bash, "-n", "selfregister.sh").Run())

	// No params file: it must do nothing and exit 0.
	cmd := exec.Command(bash, "selfregister.sh")
	cmd.Env = append(os.Environ(), "STATUSBOX_PARAMS=/nonexistent")
	require.NoError(t, cmd.Run())
}

func TestUserDataWithSelfRegisterIsWithinTheLimit(t *testing.T) {
	previous := statusbox.FetchChecksums
	statusbox.FetchChecksums = func(string) (string, error) {
		return fakeSHA64 + "  gatus_" + GatusVersion + "_linux_arm64\n" + fakeSHA64 + "  gatus_" + GatusVersion + "_linux_amd64\n", nil
	}

	t.Cleanup(func() { statusbox.FetchChecksums = previous })

	without, err := validArgs().UserData("status")
	require.NoError(t, err)

	a := sshArgs()
	a.SelfRegister = selfRegisterArgs().SelfRegister
	with, err := a.UserData("status")
	require.NoError(t, err)

	plain, err := selfRegisterArgs().UserData("status")
	require.NoError(t, err)

	t.Logf("user-data bytes: %d without, %d with self-register, %d with SSH and self-register (limit %d)", len(without), len(plain), len(with), userDataLimit)
	require.Less(t, len(with), userDataLimit)
	// The script is no longer compressed together with setup.sh (that travels
	// as an S3 object), so its delta is its own gzip+base64.
	require.Less(t, len(plain)-len(without), 3*1024)
}

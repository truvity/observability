package ec2

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/truvity/tailscale/pkg/hostaccess"

	"github.com/truvity/observability/pkg/statusbox"
)

func validSSH() *SSHArgs {
	return &SSHArgs{
		OPKSSH: hostaccess.OPKSSHPreset{
			Issuer:   "https://issuer.example.test/realms/acme",
			ClientID: "opkssh",
			User:     "ec2-user",
			Group:    "ops",
		},
		HostCert: hostaccess.HostCertPreset{
			Address:           "https://bao.example.test",
			Namespace:         "acme",
			AuthMount:         "aws",
			AuthRole:          "hostcert",
			ServerIDHeader:    "bao.example.test",
			SSHMount:          "ssh-host",
			SSHRole:           "host",
			PrincipalPatterns: []string{"ip-*.eu-west-3.compute.internal"},
		},
		IngressCIDRs: []string{"10.20.0.0/16"},
	}
}

func sshArgs() Args {
	a := validArgs()
	a.SSH = validSSH()

	return a
}

func TestHostaccessVersionMatchesGoMod(t *testing.T) {
	mod, err := os.ReadFile("../../../go.mod")
	require.NoError(t, err)
	require.Contains(t, string(mod), "github.com/truvity/tailscale v"+HostaccessVersion+"\n",
		"HostaccessVersion must be the truvity/tailscale version go.mod pins: the box verifies the digest of that build's script")
}

func TestSSHBootstrapCarriesTheFilesAndCommands(t *testing.T) {
	got, err := sshArgs().bootstrap(namesFor("status"), fakeChecksums())
	require.NoError(t, err)

	for _, want := range []string{
		"cat > /etc/hostaccess/hostaccess.env <<'STATUSBOX_SSH_FILE_0'",
		"HOST_CERT_PRINCIPAL_SOURCE=\"imds-hostname\"",
		"cat > /etc/hostaccess/opkssh-providers <<",
		"https://issuer.example.test/realms/acme opkssh 24h",
		"ec2-user oidc:groups:ops https://issuer.example.test/realms/acme",
		"cat > /etc/hostaccess/opkssh-auth_id <<",
		"dnf install -y checkpolicy",
		"hostaccess-setup-v" + HostaccessVersion + ".sh",
		"sha256sum -c -",
		"timeout 600 bash /opt/statusbox/ssh/setup.sh ||",
	} {
		require.Contains(t, got, want)
	}

	// SSH setup is attempted before the install phase that completes the hook.
	require.Less(t, strings.Index(got, "/opt/statusbox/ssh/setup.sh ||"), strings.Index(got, "exec /usr/local/sbin/statusbox-setup install"))
}

func TestBootstrapWithoutSSHHasNoSSH(t *testing.T) {
	got, err := validArgs().bootstrap(namesFor("status"), fakeChecksums())
	require.NoError(t, err)
	require.NotContains(t, got, "hostaccess")
	require.NotContains(t, got, "opkssh")
}

func TestSSHRefusals(t *testing.T) {
	for name, mutate := range map[string]func(*Args){
		"x86 box":      func(a *Args) { a.InstanceType = "t3.small" },
		"no CIDR":      func(a *Args) { a.SSH.IngressCIDRs = nil },
		"open world":   func(a *Args) { a.SSH.IngressCIDRs = []string{"0.0.0.0/0"} },
		"not a CIDR":   func(a *Args) { a.SSH.IngressCIDRs = []string{"nope"} },
		"bad issuer":   func(a *Args) { a.SSH.OPKSSH.Issuer = "http://issuer.example.test" },
		"wildcard all": func(a *Args) { a.SSH.HostCert.PrincipalPatterns = []string{"*"} },
	} {
		t.Run(name, func(t *testing.T) {
			a := sshArgs()
			mutate(&a)
			require.Error(t, a.validate())
		})
	}
}

// TestUserDataWithSSHIsWithinTheLimit measures the real thing: the gzip-wrapped
// user-data with SSH on, for the largest realistic box in the test fixtures.
// The budget leaves room for the estate's own Configs to grow.
func TestUserDataWithSSHIsWithinTheLimit(t *testing.T) {
	previous := statusbox.FetchChecksums
	statusbox.FetchChecksums = func(string) (string, error) {
		return fakeSHA64 + "  gatus_" + GatusVersion + "_linux_arm64\n" + fakeSHA64 + "  gatus_" + GatusVersion + "_linux_amd64\n", nil
	}

	t.Cleanup(func() { statusbox.FetchChecksums = previous })

	without, err := validArgs().UserData("status")
	require.NoError(t, err)

	with, err := sshArgs().UserData("status")
	require.NoError(t, err)

	t.Logf("user-data bytes: %d without SSH, %d with SSH (limit %d)", len(without), len(with), userDataLimit)
	require.Less(t, len(with), userDataLimit)
	require.Less(t, len(with)-len(without), 2*1024, "SSH should cost about a kilobyte of user-data (the script is downloaded, not embedded)")
}

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
		HostCert: &hostaccess.HostCertPreset{
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

func opksshOnlyArgs() Args {
	a := sshArgs()
	a.SSH.HostCert = nil
	a.SSH.HostKeyParameter = "/acme/status/ssh-host-key"

	return a
}

func TestSSHOpksshOnlyHasNoHostCert(t *testing.T) {
	a := opksshOnlyArgs()
	require.NoError(t, a.validate())

	got, err := a.bootstrap(namesFor("status"), fakeChecksums())
	require.NoError(t, err)
	require.Contains(t, got, "OPKSSH=\"true\"")
	require.Contains(t, got, "HOST_CERT=\"false\"")
	require.NotContains(t, got, "openbao-hostcert")
	require.NotContains(t, got, "HOST_CERT_ADDRESS")
}

func TestSSHHostKeyRestoreRunsBeforeSshdSetup(t *testing.T) {
	got, err := opksshOnlyArgs().bootstrap(namesFor("status"), fakeChecksums())
	require.NoError(t, err)

	for _, want := range []string{
		"aws ssm get-parameter --name /acme/status/ssh-host-key --with-decryption",
		"ssh-keygen -y -f",
		"install -m 0600 -o root -g root \"$tmp/key\" /etc/ssh/ssh_host_ed25519_key",
		"HostKey /etc/ssh/ssh_host_ed25519_key",
		"host key restore failed; sshd keeps its generated key",
	} {
		require.Contains(t, got, want)
	}

	require.Less(t, strings.Index(got, "ssh-keygen -y"), strings.Index(got, "hostaccess-setup-v"+HostaccessVersion), "the key is restored before the hostaccess setup restarts sshd")

	without, err := sshArgs().bootstrap(namesFor("status"), fakeChecksums())
	require.NoError(t, err)
	require.NotContains(t, without, "ssh-keygen")
}

func TestSSHHostKeyParameterIsReadableByTheRole(t *testing.T) {
	a := opksshOnlyArgs()
	require.Contains(t, a.parameterNames(), "/acme/status/ssh-host-key")
	require.Contains(t, a.instancePolicy(namesFor("status"), "eu-west-3", "ACCOUNT"), "parameter/acme/status/ssh-host-key")
	require.NotContains(t, sshArgs().parameterNames(), "/acme/status/ssh-host-key")

	a.SSH.HostKeyParameter = "relative/name"
	require.Error(t, a.validate())
}

func TestUserDataOpksshOnlyIsWithinTheLimit(t *testing.T) {
	previous := statusbox.FetchChecksums
	statusbox.FetchChecksums = func(string) (string, error) {
		return fakeSHA64 + "  gatus_" + GatusVersion + "_linux_arm64\n" + fakeSHA64 + "  gatus_" + GatusVersion + "_linux_amd64\n", nil
	}

	t.Cleanup(func() { statusbox.FetchChecksums = previous })

	got, err := opksshOnlyArgs().UserData("status")
	require.NoError(t, err)
	require.Less(t, len(got), 12*1024)
}

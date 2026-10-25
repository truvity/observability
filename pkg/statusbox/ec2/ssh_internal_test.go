package ec2

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/truvity/tailscale/pkg/hostaccess"
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

func TestSSHCarriesTheFilesAndCommands(t *testing.T) {
	files := writtenFiles(t, sshArgs())
	got := cloudConfig(t, sshArgs())

	require.Contains(t, files["/etc/hostaccess/hostaccess.env"], "HOST_CERT_PRINCIPAL_SOURCE=\"imds-hostname\"")
	require.Contains(t, files["/etc/hostaccess/opkssh-providers"], "https://issuer.example.test/realms/acme opkssh 24h")
	require.Contains(t, files["/etc/hostaccess/opkssh-auth_id"], "ec2-user oidc:groups:ops https://issuer.example.test/realms/acme")

	setup := files["/opt/asg/ssh/setup.sh"]
	for _, want := range []string{"dnf install -y checkpolicy", "hostaccess-setup-v" + HostaccessVersion + ".sh", "sha256sum -c -"} {
		require.Contains(t, setup, want)
	}

	// SSH setup is attempted before the box's install step, time-boxed and
	// never fatal.
	require.Less(t, strings.Index(got, "timeout 600 bash /opt/asg/ssh/setup.sh ||"), strings.Index(got, setupPath+" install"))
}

func TestUserDataWithoutSSHHasNoSSH(t *testing.T) {
	got := cloudConfig(t, validArgs())
	require.NotContains(t, got, "hostaccess")
	require.NotContains(t, got, "opkssh")
	require.NotContains(t, got, "ssh-keygen")
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

// TestUserDataWithSSHIsWithinTheLimit measures the real thing: the user-data
// with SSH, self-registration and the tunnel on, for the largest realistic box
// in the test fixtures. The budget leaves room for the estate's own Configs to
// grow.
func TestUserDataWithSSHIsWithinTheLimit(t *testing.T) {
	fakeRelease(t)

	without, err := validArgs().UserData("status")
	require.NoError(t, err)

	with, err := sshArgs().UserData("status")
	require.NoError(t, err)

	t.Logf("user-data bytes: %d without SSH, %d with SSH (limit %d)", without.Size, with.Size, userDataLimit)
	require.Less(t, with.Size, userDataLimit)
	require.Less(t, with.Size-without.Size, 2*1024, "SSH should cost about a kilobyte of user-data (the hostaccess script is downloaded, not embedded)")
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

	got := cloudConfig(t, a)
	require.Contains(t, got, "OPKSSH=\"true\"")
	require.Contains(t, got, "HOST_CERT=\"false\"")
	require.NotContains(t, got, "openbao-hostcert")
	require.NotContains(t, got, "HOST_CERT_ADDRESS")
}

func TestSSHHostKeyRestoreRunsBeforeSshdSetup(t *testing.T) {
	setup := writtenFiles(t, opksshOnlyArgs())["/opt/asg/ssh/setup.sh"]

	for _, want := range []string{
		"aws ssm get-parameter --name /acme/status/ssh-host-key --with-decryption",
		"ssh-keygen -y -f",
		"install -m 0600 -o root -g root \"$tmp/key\" /etc/ssh/ssh_host_ed25519_key",
		"HostKey /etc/ssh/ssh_host_ed25519_key",
		"host key restore failed; sshd keeps its generated key",
	} {
		require.Contains(t, setup, want)
	}

	require.Less(t, strings.Index(setup, "ssh-keygen -y"), strings.Index(setup, "hostaccess-setup-v"+HostaccessVersion),
		"the key is restored before the hostaccess setup restarts sshd")
	require.NotContains(t, writtenFiles(t, sshArgs())["/opt/asg/ssh/setup.sh"], "ssh-keygen")
}

func TestSSHHostKeyParameterIsReadableByTheRole(t *testing.T) {
	a := opksshOnlyArgs()
	require.Contains(t, compose(t, a).Rendered.SSMParameters, "/acme/status/ssh-host-key")
	require.NotContains(t, compose(t, sshArgs()).Rendered.SSMParameters, "/acme/status/ssh-host-key")

	a.SSH.HostKeyParameter = "relative/name"
	require.Error(t, a.validate())
}

func TestUserDataOpksshOnlyIsWithinTheLimit(t *testing.T) {
	fakeRelease(t)

	got, err := opksshOnlyArgs().UserData("status")
	require.NoError(t, err)
	require.Less(t, got.Size, 12*1024)
}

func TestSSHPrefersPostQuantumKeyExchangeFailSafe(t *testing.T) {
	setup := writtenFiles(t, sshArgs())["/opt/asg/ssh/setup.sh"]

	for _, want := range []string{
		"KexAlgorithms ^mlkem768x25519-sha256,sntrup761x25519-sha512@openssh.com",
		"/etc/ssh/sshd_config.d/06-asg-kex.conf",
		"if sshd -t; then systemctl restart sshd; else rm -f /etc/ssh/sshd_config.d/06-asg-kex.conf; exit 1; fi",
		"post-quantum key exchange not enabled; sshd keeps its defaults",
	} {
		require.Contains(t, setup, want)
	}

	require.Less(t, strings.Index(setup, "KexAlgorithms"), strings.Index(setup, "hostaccess-setup-v"+HostaccessVersion))
}

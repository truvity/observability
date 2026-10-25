package ec2

import (
	"errors"
	"fmt"
	"net/netip"
	"regexp"

	"github.com/truvity/asg/module"
	"github.com/truvity/asg/modules/ssh"
	"github.com/truvity/tailscale/pkg/hostaccess"
)

// HostaccessVersion is the truvity/tailscale release the SSH setup script is
// downloaded from. It MUST equal the version of github.com/truvity/tailscale in
// go.mod: the digest the box verifies is that of the copy embedded in this
// build. TestHostaccessVersionMatchesGoMod keeps the two together.
const HostaccessVersion = "1.24.2"

// SSHArgs turns on SSH access to the box through the estate's opkssh (OIDC
// sign-in) and OpenBAO-signed host certificates. The ec2-user key pair stays
// off: opkssh is the only way in. Nothing here is secret.
//
// The box authenticates to OpenBAO's AWS auth method with
// sts:GetCallerIdentity, which every role may call, so the instance role gains
// nothing. The box downloads the setup script and its pinned artifacts from
// github.com; the security group already lets everything out.
type SSHArgs struct {
	// OPKSSH says whom the box trusts and admits (issuer, client id, the local
	// user, the group).
	OPKSSH hostaccess.OPKSSHPreset
	// HostCert, when set, says where the OpenBAO host CA is and which
	// principals the box may ask a certificate for. The principal is the
	// instance's private DNS name (IMDS local-hostname). Nil: opkssh only, no
	// host certificate and nothing of the OpenBAO setup is rendered.
	HostCert *hostaccess.HostCertPreset
	// HostKeyParameter, when set, is the name of an SSM SecureString holding
	// the box's one ed25519 host key (OpenSSH private-key format). The
	// instance role may read exactly that parameter. At boot, before sshd is
	// restarted, the box restores it as /etc/ssh/ssh_host_ed25519_key, derives
	// the .pub and limits sshd to that key, so clients can pin one plain
	// known_hosts line. Fail-safe: on any failure sshd keeps its own generated
	// key. Empty: the box keeps whatever host key it generates.
	HostKeyParameter string
	// IngressCIDRs are the networks allowed to reach TCP 22. Required.
	IngressCIDRs []string
}

// hostaccessConfig is the Config the presets render. The principal source is
// the package's default for an EC2 host, IMDS local-hostname.
func (s SSHArgs) hostaccessConfig() hostaccess.Config {
	c := hostaccess.Config{
		OPKSSH:          hostaccess.NewOPKSSH(s.OPKSSH),
		PrincipalSource: hostaccess.PrincipalIMDSHostname,
	}

	if s.HostCert != nil {
		c.HostCert = hostaccess.NewHostCert(*s.HostCert)
	}

	return c
}

// hostKeyParameterRE is an absolute SSM parameter name, the characters SSM
// allows minus anything a shell could read twice.
var hostKeyParameterRE = regexp.MustCompile(`^/[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*$`)

func (s SSHArgs) bundle() (*hostaccess.Bundle, error) {
	return hostaccess.Render(s.hostaccessConfig(), hostaccess.Options{
		Delivery: hostaccess.DeliveryDownload,
		Version:  HostaccessVersion,
	})
}

func (a Args) validateSSH() []error {
	if a.SSH == nil {
		return nil
	}

	var errs []error

	if a.architecture() != archARM64 {
		errs = append(errs, fmt.Errorf("statusbox/ec2: SSH needs a Graviton (arm64) InstanceType, %q is not: the pinned opkssh and host-certificate "+
			"artifacts are arm64 builds", a.instanceType()))
	}

	if len(a.SSH.IngressCIDRs) == 0 {
		errs = append(errs, errors.New("statusbox/ec2: SSH.IngressCIDRs is empty: name the networks allowed to reach TCP 22"))
	}

	for _, c := range a.SSH.IngressCIDRs {
		if p, err := netip.ParsePrefix(c); err != nil || !p.Addr().Is4() {
			errs = append(errs, fmt.Errorf("statusbox/ec2: SSH.IngressCIDRs entry %q is not an IPv4 CIDR", c))
		} else if p.Bits() == 0 {
			errs = append(errs, fmt.Errorf("statusbox/ec2: SSH.IngressCIDRs entry %q opens SSH to the whole internet", c))
		}
	}

	if p := a.SSH.HostKeyParameter; p != "" && !hostKeyParameterRE.MatchString(p) {
		errs = append(errs, fmt.Errorf("statusbox/ec2: SSH.HostKeyParameter %q is not an absolute SSM parameter name", p))
	}

	if _, err := a.SSH.bundle(); err != nil {
		errs = append(errs, fmt.Errorf("statusbox/ec2: SSH: %w", err))
	}

	return errs
}

// sshModule is SSH as a module of github.com/truvity/asg: the hostaccess
// bundle (packages, files, commands) goes in as data, and the module installs
// it in one fail-safe, time-boxed program on the first boot, restores the fixed
// host key, prefers the post-quantum key exchange and opens TCP 22. Nil when
// SSH is unset.
func (a Args) sshModule() (module.Module, error) {
	if a.SSH == nil {
		return nil, nil
	}

	bundle, err := a.SSH.bundle()
	if err != nil {
		return nil, fmt.Errorf("statusbox/ec2: SSH: %w", err)
	}

	files := make([]ssh.File, len(bundle.Files))
	for i, f := range bundle.Files {
		files[i] = ssh.File{Path: f.Path, Mode: f.Mode, Content: f.Content}
	}

	return ssh.New(ssh.Args{
		Bundle:           ssh.Bundle{Packages: bundle.Packages, Files: files, Commands: bundle.Commands},
		HostKeyParameter: a.SSH.HostKeyParameter,
		IngressCIDRs:     a.SSH.IngressCIDRs,
	}), nil
}

// sshCIDRs are the networks admitted to TCP 22; none without SSH.
func (a Args) sshCIDRs() []string {
	if a.SSH == nil {
		return nil
	}

	return a.SSH.IngressCIDRs
}

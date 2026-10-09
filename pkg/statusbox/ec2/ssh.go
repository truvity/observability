package ec2

import (
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/truvity/tailscale/pkg/hostaccess"
)

// HostaccessVersion is the truvity/tailscale release the SSH setup script is
// downloaded from. It MUST equal the version of github.com/truvity/tailscale in
// go.mod: the digest the box verifies is that of the copy embedded in this
// build. TestHostaccessVersionMatchesGoMod keeps the two together.
const HostaccessVersion = "1.24.2"

// sshDir is where the bootstrap stages the SSH setup program on the instance.
// sshPort is the TCP port the security group opens to SSH.IngressCIDRs.
const sshPort = 22

const sshDir = "/opt/statusbox/ssh"

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
	// HostCert says where the OpenBAO host CA is and which principals the box
	// may ask a certificate for. The principal is the instance's private DNS
	// name (IMDS local-hostname).
	HostCert hostaccess.HostCertPreset
	// IngressCIDRs are the networks allowed to reach TCP 22. Required.
	IngressCIDRs []string
}

// hostaccessConfig is the Config the presets render. The principal source is
// the package's default for an EC2 host, IMDS local-hostname.
func (s SSHArgs) hostaccessConfig() hostaccess.Config {
	return hostaccess.Config{
		OPKSSH:          hostaccess.NewOPKSSH(s.OPKSSH),
		HostCert:        hostaccess.NewHostCert(s.HostCert),
		PrincipalSource: hostaccess.PrincipalIMDSHostname,
	}
}

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

	if _, err := a.SSH.bundle(); err != nil {
		errs = append(errs, fmt.Errorf("statusbox/ec2: SSH: %w", err))
	}

	return errs
}

// sshBootstrap renders the part of the bootstrap that applies the bundle: the
// files, then one program that installs the packages and runs the commands.
// It runs BEFORE the setup script's install phase, which is what starts the
// boot phase that completes the lifecycle hook, so an instance is InService
// only after SSH setup was attempted. It is fail-safe: every step's failure is
// logged and swallowed, and the whole program is time-boxed, so a broken SSH
// setup can never keep the box out of service.
func (a Args) sshBootstrap() (string, error) {
	if a.SSH == nil {
		return "", nil
	}

	bundle, err := a.SSH.bundle()
	if err != nil {
		return "", fmt.Errorf("statusbox/ec2: SSH: %w", err)
	}

	var b strings.Builder

	fmt.Fprintf(&b, "mkdir -p %s %s\n", hostaccess.ConfDir, sshDir)

	for i, f := range bundle.Files {
		delim := fmt.Sprintf("STATUSBOX_SSH_FILE_%d", i)
		if strings.Contains(f.Content, "\n"+delim+"\n") {
			return "", fmt.Errorf("statusbox/ec2: SSH file %s contains a heredoc delimiter of the bootstrap", f.Path)
		}

		fmt.Fprintf(&b, "cat > %s <<'%s'\n%s%s\n", f.Path, delim, ensureNewline(f.Content), delim)
		fmt.Fprintf(&b, "chmod %s %s\n", f.Mode, f.Path)
	}

	var prog strings.Builder

	prog.WriteString("#!/bin/bash\n# Rendered by pkg/statusbox/ec2 from truvity/tailscale pkg/hostaccess. Every step is fail-safe.\n")

	if len(bundle.Packages) > 0 {
		fmt.Fprintf(&prog, "dnf install -y %s || echo \"statusbox ssh: package install failed\"\n", strings.Join(bundle.Packages, " "))
	}

	for _, c := range bundle.Commands {
		prog.WriteString(c + "\n")
	}

	if strings.Contains(prog.String(), "\nSTATUSBOX_SSH_SETUP\n") {
		return "", errors.New("statusbox/ec2: SSH commands contain a heredoc delimiter of the bootstrap")
	}

	fmt.Fprintf(&b, "cat > %s/setup.sh <<'STATUSBOX_SSH_SETUP'\n%sSTATUSBOX_SSH_SETUP\n", sshDir, prog.String())
	fmt.Fprintf(&b, "chmod 0700 %s/setup.sh\n", sshDir)
	// Time-boxed and never fatal: set -e is on in the bootstrap.
	fmt.Fprintf(&b, "timeout 600 bash %s/setup.sh || echo \"statusbox ssh: setup did not finish; continuing without SSH\"\n\n", sshDir)

	return b.String(), nil
}

// sshCIDRs are the networks admitted to TCP 22; none without SSH.
func (a Args) sshCIDRs() []string {
	if a.SSH == nil {
		return nil
	}

	return a.SSH.IngressCIDRs
}

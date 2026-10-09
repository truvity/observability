package ec2

import (
	_ "embed"
	"errors"
	"fmt"
	"regexp"
	"strings"
)

// DefaultSelfRegisterTTL is the TTL of the record when SelfRegisterArgs.TTL is
// zero: short, because the address changes on every instance replacement.
const DefaultSelfRegisterTTL = 60

// selfRegisterScript is /usr/local/sbin/statusbox-self-register; see its header.
// It is a separate file, written only when SelfRegister is set and run as an
// ExecStartPost of statusbox-boot.service, so setup.sh (and every golden of a
// box without the feature) is untouched.
//
//go:embed selfregister.sh
var selfRegisterScript string

var (
	roleARNRE    = regexp.MustCompile(`^arn:[a-z-]+:iam::[0-9]{12}:role/[A-Za-z0-9+=,.@_/-]+$`)
	hostedZoneRE = regexp.MustCompile(`^Z[A-Z0-9]{3,31}$`)
	recordNameRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?\.)+[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?$`)
)

// SelfRegisterArgs makes the box write its own private IP into a Route 53
// private hosted zone, so a stable name always points at the CURRENT
// in-service instance (the address changes on every replacement or warm-pool
// takeover). Nothing here is secret.
//
// The instance role may only sts:AssumeRole on exactly RoleARN. That role,
// which lives in the zone's account and is the caller's to create, must trust
// the instance role and be limited to changing RecordName.
type SelfRegisterArgs struct {
	// RoleARN is the role to assume, typically in another account.
	RoleARN string
	// HostedZoneID is the zone that holds the record.
	HostedZoneID string
	// RecordName is the one A record the box UPSERTs.
	RecordName string
	// TTL defaults to DefaultSelfRegisterTTL.
	TTL int
}

func (s SelfRegisterArgs) ttl() int {
	if s.TTL == 0 {
		return DefaultSelfRegisterTTL
	}

	return s.TTL
}

func (a Args) validateSelfRegister() []error {
	s := a.SelfRegister
	if s == nil {
		return nil
	}

	var errs []error

	if !roleARNRE.MatchString(s.RoleARN) {
		errs = append(errs, fmt.Errorf("statusbox/ec2: SelfRegister.RoleARN %q is not a role ARN", s.RoleARN))
	}

	if !hostedZoneRE.MatchString(s.HostedZoneID) {
		errs = append(errs, fmt.Errorf("statusbox/ec2: SelfRegister.HostedZoneID %q is not a hosted zone ID", s.HostedZoneID))
	}

	if !recordNameRE.MatchString(s.RecordName) {
		errs = append(errs, fmt.Errorf("statusbox/ec2: SelfRegister.RecordName %q is not a lower-case DNS name without a trailing dot", s.RecordName))
	}

	if s.TTL < 0 || s.TTL > 86400 {
		errs = append(errs, fmt.Errorf("statusbox/ec2: SelfRegister.TTL %d is out of range (0 for the default %d, or 1 to 86400)", s.TTL, DefaultSelfRegisterTTL))
	}

	return errs
}

// selfRegisterParams are the params.sh lines of the feature; none when unset.
func (a Args) selfRegisterParams() string {
	if a.SelfRegister == nil {
		return ""
	}

	var b strings.Builder

	b.WriteString(shellLine("SB_DNS_ROLE_ARN", a.SelfRegister.RoleARN))
	b.WriteString(shellLine("SB_DNS_ZONE_ID", a.SelfRegister.HostedZoneID))
	b.WriteString(shellLine("SB_DNS_RECORD", a.SelfRegister.RecordName))
	b.WriteString(shellLine("SB_DNS_TTL", fmt.Sprint(a.SelfRegister.ttl())))

	return b.String()
}

// selfRegisterBootstrap writes the script and the systemd drop-in that runs it
// after statusbox-boot.service's ExecStart (which completes the lifecycle hook)
// succeeded. The "-" prefix ignores its exit status and timeout bounds it, so
// it can never fail or delay the unit. Empty when unset.
func (a Args) selfRegisterBootstrap() (string, error) {
	if a.SelfRegister == nil {
		return "", nil
	}

	if strings.Contains(selfRegisterScript, "\nSTATUSBOX_SELF_REGISTER\n") {
		return "", errors.New("statusbox/ec2: selfregister.sh contains a heredoc delimiter of the bootstrap")
	}

	var b strings.Builder

	fmt.Fprintf(&b, "cat > /usr/local/sbin/statusbox-self-register <<'STATUSBOX_SELF_REGISTER'\n%sSTATUSBOX_SELF_REGISTER\n", ensureNewline(selfRegisterScript))
	b.WriteString("chmod 0755 /usr/local/sbin/statusbox-self-register\n")
	b.WriteString("mkdir -p /etc/systemd/system/statusbox-boot.service.d\n")
	b.WriteString("cat > /etc/systemd/system/statusbox-boot.service.d/self-register.conf <<'STATUSBOX_SELF_REGISTER_UNIT'\n")
	b.WriteString("[Service]\nExecStartPost=-/usr/bin/timeout 120 /usr/local/sbin/statusbox-self-register\n")
	b.WriteString("STATUSBOX_SELF_REGISTER_UNIT\n\n")

	return b.String(), nil
}

// selfRegisterStatement is the one IAM statement the feature adds to the
// instance role: sts:AssumeRole on exactly RoleARN.
func (a Args) selfRegisterStatement() map[string]any {
	if a.SelfRegister == nil {
		return nil
	}

	return map[string]any{
		"Sid":      "SelfRegisterDNS",
		"Effect":   "Allow",
		"Action":   []string{"sts:AssumeRole"},
		"Resource": []string{a.SelfRegister.RoleARN},
	}
}

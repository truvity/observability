package ec2

import (
	"fmt"
	"regexp"

	"github.com/truvity/asg/module"
	"github.com/truvity/asg/modules/selfregister"
)

// DefaultSelfRegisterTTL is the TTL of the record when SelfRegisterArgs.TTL is
// zero: short, because the address changes on every instance replacement.
const DefaultSelfRegisterTTL = selfregister.DefaultTTL

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

// selfRegisterModule is the box's self-registration as a module of
// github.com/truvity/asg: the script, its unit, the step that starts it on a
// boot into service (never in the warm pool) and the role's one statement,
// sts:AssumeRole on exactly RoleARN. Nil when SelfRegister is unset.
func (a Args) selfRegisterModule() module.Module {
	if a.SelfRegister == nil {
		return nil
	}

	return selfregister.New(selfregister.Args{
		RoleARN:      a.SelfRegister.RoleARN,
		HostedZoneID: a.SelfRegister.HostedZoneID,
		RecordName:   a.SelfRegister.RecordName,
		TTL:          a.SelfRegister.TTL,
	})
}

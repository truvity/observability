// Package status deploys the status box: the one EC2 Auto Scaling group of one
// that watches an estate from outside it, wired onto the packages pkg/statusbox
// and pkg/statusbox/ec2 (docs/statusbox.md has the design and the Gatus boot
// constraints).
//
// The package renders both Gatus instances from one catalogue and creates the
// machine. Everything about the estate arrives in Inputs: the AWS provider (so
// its resource name stays the caller's), the host groups, and the names of the
// SSM parameters the box reads its secrets from at boot. The tunnel itself is
// the caller's, built with the Cloudflare module.
package status

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/observability/pkg/statusbox"
	"github.com/truvity/observability/pkg/statusbox/ec2"
)

const (
	// PrivatePort is the private ops instance's loopback port.
	PrivatePort = 8081

	// PublicPort is the public ops instance's loopback port: a tunnel's
	// ingress targets it.
	PublicPort = 8082

	// publicInstanceName / privateInstanceName are the two ops instances.
	publicInstanceName  = "ops"
	privateInstanceName = "ops-breakglass"
)

type (
	// Inputs are the facts Deploy cannot derive.
	Inputs struct {
		// BoxProvider is the AWS provider onto the account and region the box
		// lives in.
		BoxProvider *aws.Provider

		// Version is the truvity/observability release the box installs.
		Version string
		// TrustedCAs is a PEM bundle added to every Gatus container's trust
		// store, for probes of hosts under a private PKI.
		TrustedCAs string

		// PlatformHosts, ByCompany, Entities, AlertsReadHost and DeadmanChannel
		// are the page (see CatalogueInputs).
		PlatformHosts  []string
		HostProbes     map[string]statusbox.Probe
		ByCompany      map[string][]statusbox.CompanyHost
		Entities       []Entity
		AlertsReadHost string
		DeadmanChannel string

		// PublicHostname is empty for a box with no public page: every
		// instance is then private, and no tunnel or OIDC secret is needed.
		PublicHostname string
		OIDC           OIDC

		// EC2 is the box itself: network, replicas, and the SSM parameter
		// names the secrets are read from.
		EC2 EC2Inputs
	}

	// EC2Inputs describe the box: an Auto Scaling group of one with a warm
	// pool, Gatus and cloudflared as systemd units, SQLite replicated to S3 by
	// Litestream, and the secrets read from SSM Parameter Store at boot
	// (docs/statusbox.md, "EC2 backend").
	EC2Inputs struct {
		// VPCID and SubnetIDs are the caller's network: public subnets only,
		// in two or more zones.
		VPCID     pulumi.StringInput
		SubnetIDs []pulumi.StringInput
		// PrivateIngressCIDRs may reach the private page (PrivatePort), for
		// instance a peered VPC.
		PrivateIngressCIDRs []string

		// PrivatePort is the TCP port the private page listens on, and the one
		// the security group opens to PrivateIngressCIDRs. Zero means the
		// package's PrivatePort (8081), so existing stacks do not change. 80
		// serves it on plain `http://<name>/`: the box then grants that
		// instance's Gatus the bind capability for a port below 1024.
		PrivatePort int

		// Bucket and BucketPrefix hold the Litestream replicas; KMSKeyARN is
		// the optional customer-managed key for the parameters and the bucket.
		Bucket       string
		BucketPrefix string
		KMSKeyARN    string

		// PermissionsBoundary is the full ARN of the IAM permissions boundary
		// the instance role carries; empty means none.
		PermissionsBoundary string

		// SessionManager attaches AmazonSSMManagedInstanceCore to the
		// instance role for an SSM Session Manager shell; false changes
		// nothing.
		SessionManager bool

		// InstanceType defaults to the package's (t4g.nano).
		InstanceType string

		// SSH, when set, adds SSH access through opkssh and OpenBAO host
		// certificates, and TCP 22 from SSH.IngressCIDRs. Nil changes nothing.
		// It needs a Graviton InstanceType.
		SSH *ec2.SSHArgs

		// SelfRegister, when set, makes the box UPSERT its private IP as an A
		// record into a Route 53 hosted zone at every boot into service, through
		// the cross-account role it names; the instance role gains
		// sts:AssumeRole on that role only. Nil changes nothing.
		SelfRegister *ec2.SelfRegisterArgs

		// The SSM parameter NAMES of the box's secrets. TunnelTokenParameter
		// and OIDCClientSecretParameter are required with a public page; the
		// other two always.
		AlertsReadTokenParameter   string
		DeadmanSlackTokenParameter string
		OIDCClientSecretParameter  string
		TunnelTokenParameter       string

		// TelegramTokenParameter and TelegramChatIDParameter are the SSM
		// parameters (SecureString) of the optional Telegram alert channel:
		// the bot token and the chat id. Both or neither; empty renders no
		// telegram provider.
		TelegramTokenParameter  string
		TelegramChatIDParameter string
		// PingURLParameter is the SSM SecureString holding the dead-man ping
		// URL (healthchecks.io style). Empty: no ping units on the box.
		PingURLParameter string
	}
)

// privatePort is the port the private instance listens on: EC2.PrivatePort
// when set, otherwise PrivatePort.
func (in Inputs) privatePort() int {
	if in.EC2.PrivatePort != 0 {
		return in.EC2.PrivatePort
	}

	return PrivatePort
}

func (in Inputs) catalogue(public bool) CatalogueInputs {
	return CatalogueInputs{
		PlatformHosts: in.PlatformHosts, HostProbes: in.HostProbes, ByCompany: in.ByCompany, Entities: in.Entities,
		AlertsReadHost: in.AlertsReadHost, DeadmanChannel: in.DeadmanChannel,
		Public: public, PublicHostname: in.PublicHostname, OIDC: in.OIDC,
		Telegram: in.telegramEnabled(),
	}
}

// telegramEnabled reports whether the optional Telegram channel is configured.
func (in Inputs) telegramEnabled() bool {
	return in.EC2.TelegramTokenParameter != "" && in.EC2.TelegramChatIDParameter != ""
}

// Deploy renders every Gatus instance and provisions the box that runs them.
func Deploy(c *pulumi.Context, logger *slog.Logger, in Inputs) error {
	if (in.EC2.TelegramTokenParameter == "") != (in.EC2.TelegramChatIDParameter == "") {
		return errors.New("status: EC2.TelegramTokenParameter and EC2.TelegramChatIDParameter go together")
	}

	if in.PublicHostname != "" && (in.EC2.OIDCClientSecretParameter == "" || in.EC2.TunnelTokenParameter == "") {
		return errors.New("status: a public page needs EC2.OIDCClientSecretParameter and EC2.TunnelTokenParameter")
	}

	// Both instances render from the SAME inputs, differing only in the
	// security block and in which one pages (see OpsCatalogue).
	breakglassConfig, err := statusbox.RenderGatus(OpsCatalogue(in.catalogue(false)))
	if err != nil {
		return fmt.Errorf("status: render %s config: %w", privateInstanceName, err)
	}

	// ops-breakglass: the ONE private instance, for the day the issuer itself
	// is down.
	instances := []statusbox.Instance{{Name: privateInstanceName, Port: in.privatePort(), Public: false, Config: breakglassConfig}}

	hostnames := map[string]string{}

	if in.PublicHostname != "" {
		opsConfig, err := statusbox.RenderGatus(OpsCatalogue(in.catalogue(true)))
		if err != nil {
			return fmt.Errorf("status: render %s config: %w", publicInstanceName, err)
		}

		instances = append(instances, statusbox.Instance{Name: publicInstanceName, Port: PublicPort, Public: true, Config: opsConfig})
		hostnames[publicInstanceName] = in.PublicHostname
	} else {
		logger.InfoContext(c.Context(), "status: no public hostname: no tunnel, every instance private")
	}

	return deployEC2(c, logger, in, instances, hostnames)
}

// deployEC2 provisions the box: no secret value in the render, only the names
// of the SSM parameters the instance reads at boot.
func deployEC2(c *pulumi.Context, logger *slog.Logger, in Inputs, instances []statusbox.Instance, hostnames map[string]string) error {
	args := &ec2.Args{
		Version:    in.Version,
		Instances:  instances,
		Hostnames:  hostnames,
		TrustedCAs: in.TrustedCAs,
		AlertURLParameters: map[string]string{
			AlertsReadTokenKey:   in.EC2.AlertsReadTokenParameter,
			DeadmanSlackTokenKey: in.EC2.DeadmanSlackTokenParameter,
		},
		PingURLParameter:    in.EC2.PingURLParameter,
		VPCID:               in.EC2.VPCID,
		SubnetIDs:           in.EC2.SubnetIDs,
		PrivateIngressCIDRs: in.EC2.PrivateIngressCIDRs,
		Bucket:              in.EC2.Bucket,
		BucketPrefix:        in.EC2.BucketPrefix,
		KMSKeyARN:           in.EC2.KMSKeyARN,
		PermissionsBoundary: in.EC2.PermissionsBoundary,
		SessionManager:      in.EC2.SessionManager,
		InstanceType:        in.EC2.InstanceType,
		SSH:                 in.EC2.SSH,
		SelfRegister:        in.EC2.SelfRegister,
		Provider:            in.BoxProvider,
	}

	if in.telegramEnabled() {
		args.AlertURLParameters[DeadmanTelegramTokenKey] = in.EC2.TelegramTokenParameter
		args.AlertURLParameters[DeadmanTelegramChatIDKey] = in.EC2.TelegramChatIDParameter
	}

	if in.PublicHostname != "" {
		args.TunnelTokenParameter = in.EC2.TunnelTokenParameter
		args.EnvParameters = map[string]string{OIDCClientSecretEnvKey: in.EC2.OIDCClientSecretParameter}
	}

	var opts []pulumi.ResourceOption
	if in.BoxProvider != nil {
		opts = append(opts, pulumi.Provider(in.BoxProvider))
	}

	box, err := ec2.NewEC2(c, "status", args, opts...)
	if err != nil {
		return fmt.Errorf("status: create ec2 box: %w", err)
	}

	c.Export("statusBoxAutoScalingGroup", box.Group.Name)

	logger.InfoContext(c.Context(), "status box deployed",
		slog.Int("instances", len(instances)),
		slog.String("observability_version", in.Version),
		slog.String("alerts_read_host", in.AlertsReadHost),
	)

	return nil
}

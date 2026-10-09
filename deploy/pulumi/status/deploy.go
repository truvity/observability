// Package status deploys the status box: the one Lightsail virtual machine
// that watches an estate from outside it, wired onto the provider-neutral
// packages pkg/statusbox and pkg/statusbox/lightsail (docs/statusbox.md has the
// design and the Gatus boot constraints).
//
// The package renders both Gatus instances from one catalogue, mints the box's
// one-shot tailnet join key, and creates the machine. Everything about the
// estate arrives in Inputs: the providers (so their resource names stay the
// caller's), the host groups, the secrets the box bakes into its cloud-init,
// and the tunnel token when there is a public page. The tunnel itself is the
// caller's, built with the Cloudflare module.
package status

import (
	"errors"
	"fmt"
	"log/slog"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi-tailscale/sdk/go/tailscale"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/observability/pkg/statusbox"
	"github.com/truvity/observability/pkg/statusbox/ec2"
	"github.com/truvity/observability/pkg/statusbox/lightsail"
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

// Backend is where the box runs.
type Backend string

const (
	// BackendLightsail is the default (the empty value means it): one
	// Lightsail instance, secrets baked into its cloud-init, joined to a
	// tailnet.
	BackendLightsail Backend = "lightsail"

	// BackendEC2 is an Auto Scaling group of one with a warm pool, Gatus and
	// cloudflared as systemd units, SQLite replicated to S3 by Litestream and
	// secrets read from SSM Parameter Store at boot (docs/statusbox.md, "EC2
	// backend"). It mints no tailnet key and installs no Tailscale.
	BackendEC2 Backend = "ec2"
)

type (
	// Inputs are the facts Deploy cannot derive.
	Inputs struct {
		// BoxProvider is the AWS provider onto the account and region the box
		// lives in; TailscaleProvider mints its join key.
		BoxProvider       *aws.Provider
		TailscaleProvider *tailscale.Provider

		// Version is the truvity/observability release the box installs,
		// Generation the operator's lever for forcing a fresh box and key,
		// AvailabilityZone where the machine sits, Hostname its tailnet name.
		Version          string
		Generation       int
		AvailabilityZone string
		Hostname         string
		// TailscaleTag is the ACL tag the one-shot key carries.
		TailscaleTag string
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

		// AlertsReadToken and DeadmanSlackToken are baked into the box.
		// OIDCClientSecret and TunnelToken are required only with a public
		// page.
		AlertsReadToken   pulumi.StringInput
		DeadmanSlackToken pulumi.StringInput
		// TelegramToken and TelegramChatID (Lightsail) enable the optional
		// Telegram alert channel; both or neither.
		TelegramToken    pulumi.StringInput
		TelegramChatID   pulumi.StringInput
		OIDCClientSecret pulumi.StringInput
		TunnelToken      pulumi.StringInput

		// Backend selects where the box runs; empty is BackendLightsail, and
		// the render with it empty is exactly what it was before the field
		// existed. With BackendEC2, EC2 is required and the Lightsail-only
		// inputs (AvailabilityZone, Hostname, TailscaleProvider, TailscaleTag,
		// Generation) and every secret VALUE above are unused: the secrets
		// arrive as the SSM parameter names in EC2.
		Backend Backend
		EC2     EC2Inputs
	}

	// EC2Inputs are the EC2 backend's own inputs.
	EC2Inputs struct {
		// VPCID and SubnetIDs are the caller's network: public subnets only,
		// in two or more zones.
		VPCID     pulumi.StringInput
		SubnetIDs []pulumi.StringInput
		// PrivateIngressCIDRs may reach the private page (PrivatePort), for
		// instance a peered VPC.
		PrivateIngressCIDRs []string

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

func (b Backend) isEC2() bool { return b == BackendEC2 }

func (in Inputs) catalogue(public bool) CatalogueInputs {
	return CatalogueInputs{
		PlatformHosts: in.PlatformHosts, HostProbes: in.HostProbes, ByCompany: in.ByCompany, Entities: in.Entities,
		AlertsReadHost: in.AlertsReadHost, DeadmanChannel: in.DeadmanChannel,
		Public: public, PublicHostname: in.PublicHostname, OIDC: in.OIDC,
		Telegram: in.telegramEnabled(),
	}
}

// telegramEnabled reports whether the optional Telegram channel is configured
// for the selected backend.
func (in Inputs) telegramEnabled() bool {
	if in.Backend.isEC2() {
		return in.EC2.TelegramTokenParameter != "" && in.EC2.TelegramChatIDParameter != ""
	}

	return in.TelegramToken != nil && in.TelegramChatID != nil
}

// Deploy renders every Gatus instance and provisions the box that runs them.
func Deploy(c *pulumi.Context, logger *slog.Logger, in Inputs) error {
	ctx := c.Context()

	switch in.Backend {
	case "", BackendLightsail:
	case BackendEC2:
	default:
		return fmt.Errorf("status: unknown Backend %q (want %q or %q)", in.Backend, BackendLightsail, BackendEC2)
	}

	if in.Backend.isEC2() && (in.EC2.TelegramTokenParameter == "") != (in.EC2.TelegramChatIDParameter == "") {
		return errors.New("status: EC2.TelegramTokenParameter and EC2.TelegramChatIDParameter go together")
	}

	if !in.Backend.isEC2() && (in.TelegramToken == nil) != (in.TelegramChatID == nil) {
		return errors.New("status: TelegramToken and TelegramChatID go together")
	}

	if !in.Backend.isEC2() && in.PublicHostname != "" && (in.OIDCClientSecret == nil || in.TunnelToken == nil) {
		return errors.New("status: a public page needs OIDCClientSecret and TunnelToken")
	}

	if in.Backend.isEC2() && in.PublicHostname != "" && (in.EC2.OIDCClientSecretParameter == "" || in.EC2.TunnelTokenParameter == "") {
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
	instances := []statusbox.Instance{{Name: privateInstanceName, Port: PrivatePort, Public: false, Config: breakglassConfig}}

	envSecrets := map[string]pulumi.StringInput{}
	hostnames := map[string]string{}

	if in.PublicHostname != "" {
		envSecrets[OIDCClientSecretEnvKey] = in.OIDCClientSecret

		opsConfig, err := statusbox.RenderGatus(OpsCatalogue(in.catalogue(true)))
		if err != nil {
			return fmt.Errorf("status: render %s config: %w", publicInstanceName, err)
		}

		instances = append(instances, statusbox.Instance{Name: publicInstanceName, Port: PublicPort, Public: true, Config: opsConfig})
		hostnames[publicInstanceName] = in.PublicHostname
	} else {
		logger.InfoContext(ctx, "status: no public hostname: no tunnel, every instance private")
	}

	if in.Backend.isEC2() {
		return deployEC2(c, logger, in, instances, hostnames)
	}

	// boxShape names the Lightsail machine requested below. Built once and
	// reused by the key's fingerprint so the two can never disagree.
	boxShape := statusboxShape{AvailabilityZone: in.AvailabilityZone}

	// The tailnet key is MINTED here, single-use, ephemeral (the device
	// itself, not the key, is what should vanish from the tailnet once a
	// replaced box stops renewing its session) and pre-authorized so the
	// box's one `tailscale up --authkey=...` needs no interactive approval.
	//
	// Description carries the generation fingerprint: Description is one of
	// the provider's ForceNew inputs, so a generation change mints a fresh key
	// in step with the box replacement it accompanies. RecreateIfInvalid is
	// deliberately left unset: "always" would re-mint the key (and so change
	// userData) on every apply once the box's own join consumed it, replacing
	// the box on every deploy.
	generation := tailnetKeyGeneration(in.Version, instances, boxShape, in.Generation)

	key, err := tailscale.NewTailnetKey(c, "status-box", tailnetKeyArgs(generation, in.TailscaleTag), pulumi.Provider(in.TailscaleProvider))
	if err != nil {
		return fmt.Errorf("status: mint tailnet key: %w", err)
	}

	alertURLs := map[string]pulumi.StringInput{
		AlertsReadTokenKey:   in.AlertsReadToken,
		DeadmanSlackTokenKey: in.DeadmanSlackToken,
	}

	if in.telegramEnabled() {
		alertURLs[DeadmanTelegramTokenKey] = in.TelegramToken
		alertURLs[DeadmanTelegramChatIDKey] = in.TelegramChatID
	}

	var tunnelToken pulumi.StringInput
	if in.PublicHostname != "" {
		tunnelToken = in.TunnelToken
	}

	box, err := lightsail.NewLightsail(c, "status", &lightsail.LightsailArgs{
		Args: statusbox.Args{
			Version:   in.Version,
			Hostname:  in.Hostname,
			Instances: instances,
			Hostnames: hostnames,
			// TrustedCAs is additive to every Gatus container's own public
			// trust store (SSL_CERT_DIR).
			TrustedCAs: in.TrustedCAs,
			Secrets: statusbox.Secrets{
				TailscaleAuthKey: key.Key,
				// nil with no public page: statusbox.Args.validate requires it
				// only when some Instance is Public.
				TunnelToken: tunnelToken,
				AlertURLs:   alertURLs,
				Env:         envSecrets,
			},
		},
		AvailabilityZone: boxShape.AvailabilityZone,
		BlueprintID:      boxShape.BlueprintID,
		BundleID:         boxShape.BundleID,
		DiskSizeGB:       boxShape.DiskSizeGB,
	}, pulumi.Provider(in.BoxProvider))
	if err != nil {
		return fmt.Errorf("status: create lightsail box: %w", err)
	}

	c.Export("statusBoxInstanceId", box.Instance.ID())

	logger.InfoContext(ctx, "status box deployed",
		slog.Int("instances", len(instances)),
		slog.String("availability_zone", in.AvailabilityZone),
		slog.String("observability_version", in.Version),
		slog.String("alerts_read_host", in.AlertsReadHost),
	)

	return nil
}

// deployEC2 provisions the EC2 backend: no tailnet key, no secret value in the
// render, only the names of the SSM parameters the instance reads at boot.
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
		slog.String("backend", string(BackendEC2)),
		slog.Int("instances", len(instances)),
		slog.String("observability_version", in.Version),
		slog.String("alerts_read_host", in.AlertsReadHost),
	)

	return nil
}

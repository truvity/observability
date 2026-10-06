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
		OIDCClientSecret  pulumi.StringInput
		TunnelToken       pulumi.StringInput
	}
)

func (in Inputs) catalogue(public bool) CatalogueInputs {
	return CatalogueInputs{
		PlatformHosts: in.PlatformHosts, ByCompany: in.ByCompany, Entities: in.Entities,
		AlertsReadHost: in.AlertsReadHost, DeadmanChannel: in.DeadmanChannel,
		Public: public, PublicHostname: in.PublicHostname, OIDC: in.OIDC,
	}
}

// Deploy renders every Gatus instance and provisions the box that runs them.
func Deploy(c *pulumi.Context, logger *slog.Logger, in Inputs) error {
	ctx := c.Context()

	if in.PublicHostname != "" && (in.OIDCClientSecret == nil || in.TunnelToken == nil) {
		return errors.New("status: a public page needs OIDCClientSecret and TunnelToken")
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
				AlertURLs: map[string]pulumi.StringInput{
					AlertsReadTokenKey:   in.AlertsReadToken,
					DeadmanSlackTokenKey: in.DeadmanSlackToken,
				},
				Env: envSecrets,
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

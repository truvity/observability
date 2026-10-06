// Package otlplayer publishes the generic OTLP Lambda layer: the extension
// that gives a Lambda function telemetry authenticated by its own IAM role,
// with no secret anywhere.
//
// The layer zips are release assets of truvity/observability
// (otlp-lambda-layer_<version>_linux_<arch>.zip, built from
// observability/lambdaext), downloaded at deploy time and verified against
// the release's checksums.txt. They are not the registry's S3 "Lambda ZIP"
// artifacts: that path stores a project's own build output keyed by project,
// whereas a layer version is published straight from a file and owned by no
// project.
//
// A layer version is account-local, so every stack that wants the layer
// (package lambdaprobe, or a caller's own stack) publishes its own copy of the
// same verified file.
package otlplayer

import (
	"context"
	"fmt"
	"path/filepath"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/observability/deploy/pulumi/releaseassets"
)

const (
	// Repo is where the layer is released.
	Repo = "truvity/observability"

	// ExtensionPath is where the extension binary sits inside the zip. A
	// layer's zip root is /opt, and the file name is the extension's name to
	// the Lambda platform, so it must be exactly this.
	ExtensionPath = "extensions/otlp-lambda"
)

type (
	// Source says which layer to publish.
	Source struct {
		// Version is the truvity/observability release, without the "v".
		Version string
		// Name is the layer name's prefix; the layer is published as
		// <Name>-<arch>.
		Name               string
		Architectures      []string
		CompatibleRuntimes []string
	}
)

// AssetName is the release asset for an architecture.
func AssetName(version, arch string) string {
	return fmt.Sprintf("otlp-lambda-layer_%s_linux_%s.zip", version, arch)
}

// LambdaArch maps the layer's release arch to Lambda's architecture name.
func LambdaArch(arch string) string {
	if arch == "amd64" {
		return "x86_64"
	}

	return arch
}

// Release is the observability release a Source names.
func (s Source) Release() releaseassets.Release {
	return releaseassets.Release{Repo: Repo, Version: s.Version}
}

// Fetch downloads the layer zips for every architecture of the source into
// dir, verified against the release's checksums file, and refuses a zip that
// does not hold the extension at ExtensionPath (a layer without it publishes
// fine and then never starts).
func Fetch(ctx context.Context, fetch releaseassets.Fetcher, dir string, s Source) (map[string]releaseassets.Asset, error) {
	names := make([]string, len(s.Architectures))
	for i, arch := range s.Architectures {
		names[i] = AssetName(s.Version, arch)
	}

	assets, err := releaseassets.Fetch(ctx, fetch, dir, s.Release(), names...)
	if err != nil {
		return nil, err
	}

	out := make(map[string]releaseassets.Asset, len(s.Architectures))

	for _, arch := range s.Architectures {
		a := assets[AssetName(s.Version, arch)]

		ok, err := releaseassets.HasMember(a.Path, ExtensionPath)
		if err != nil {
			return nil, err
		}

		if !ok {
			return nil, fmt.Errorf("%s has no %s: not a layer this stack can publish", filepath.Base(a.Path), ExtensionPath)
		}

		out[arch] = a
	}

	return out, nil
}

// Publish downloads and verifies the layer zips and publishes one layer
// version per architecture, with the provider the caller passes. The Pulumi
// resource names are "layer-<arch>", which devel's probe has carried since it
// was first deployed.
func Publish(
	ctx context.Context, c *pulumi.Context, fetch releaseassets.Fetcher, dir string, s Source, opts ...pulumi.ResourceOption,
) (map[string]*lambda.LayerVersion, error) {
	zips, err := Fetch(ctx, fetch, dir, s)
	if err != nil {
		return nil, err
	}

	layers := make(map[string]*lambda.LayerVersion, len(s.Architectures))

	for _, arch := range s.Architectures {
		z := zips[arch]

		lv, err := lambda.NewLayerVersion(c, "layer-"+arch, &lambda.LayerVersionArgs{
			LayerName: pulumi.Sprintf("%s-%s", s.Name, arch),
			Description: pulumi.Sprintf("OTLP Lambda extension %s (%s): authenticates Lambda telemetry with the function role",
				s.Version, arch),
			// Pulumi copies a .zip FileArchive verbatim when the target format is
			// zip, so the extension keeps its 0755 mode.
			Code:                    pulumi.NewFileArchive(z.Path),
			SourceCodeHash:          pulumi.String(z.SourceCodeHash),
			CompatibleArchitectures: pulumi.StringArray{pulumi.String(LambdaArch(arch))},
			CompatibleRuntimes:      pulumi.ToStringArray(s.CompatibleRuntimes),
		}, opts...)
		if err != nil {
			return nil, fmt.Errorf("publish layer %s: %w", arch, err)
		}

		layers[arch] = lv
	}

	return layers, nil
}

package ec2

import "fmt"

// The pinned third-party binaries. Each has a version and a sha256 per
// architecture, copied from the upstream release; bumping one is an edit here
// (and a new release of this repository), reviewed like any other. Gatus is not
// here: it is built in this repository's release from the tag below and its
// checksum is read from the release's checksums.txt at deploy time.
const (
	// GatusVersion is the upstream Gatus tag the release builds its binaries
	// from (hack/build-gatus.sh reads it from here).
	GatusVersion = "v5.37.0"

	// LitestreamVersion is the Litestream release (without the leading v).
	LitestreamVersion = "0.5.17"
	// CloudflaredVersion is the cloudflared release.
	CloudflaredVersion = "2026.10.0"
)

// architecture is one CPU architecture the backend supports, with the names
// each upstream uses for it.
type architecture struct {
	// key is the suffix in params.sh and in this repository's Gatus asset name.
	key string
	// ssmAMI is the public SSM parameter holding the newest Amazon Linux 2023
	// image for the architecture.
	ssmAMI string
	// litestreamArch and litestreamSHA256: the linux tarball.
	litestreamArch   string
	litestreamSHA256 string
	// cloudflaredSHA256: the cloudflared-linux-<key> binary.
	cloudflaredSHA256 string
}

var (
	archARM64 = architecture{
		key:               "arm64",
		ssmAMI:            "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64",
		litestreamArch:    "arm64",
		litestreamSHA256:  "f8ca4a050095c1efbda2c4365172e61bf9d955ea0d9ac42f448b52e51819baa5",
		cloudflaredSHA256: "e6422b9d4f72d3194bc5a38676f13667c06666523217b842a877d72a80b5ac08",
	}
	archAMD64 = architecture{
		key:               "amd64",
		ssmAMI:            "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64",
		litestreamArch:    "x86_64",
		litestreamSHA256:  "cfb371176d164437ae869f8351cfde49bd1804ae71c61923f75c9cba9c9c006d",
		cloudflaredSHA256: "d33ff2d14475178d2012c2c56beba87389ac5ded27649519f198a7d3134a99db",
	}

	architectures = []architecture{archARM64, archAMD64}
)

// gatusAsset is the name of the Gatus binary attached to a release of this
// repository for an architecture.
func gatusAsset(a architecture) string {
	return fmt.Sprintf("gatus_%s_linux_%s", GatusVersion, a.key)
}

func (a architecture) litestreamURL() string {
	return fmt.Sprintf("https://github.com/benbjohnson/litestream/releases/download/v%s/litestream-%s-linux-%s.tar.gz",
		LitestreamVersion, LitestreamVersion, a.litestreamArch)
}

func (a architecture) cloudflaredURL() string {
	return fmt.Sprintf("https://github.com/cloudflare/cloudflared/releases/download/%s/cloudflared-linux-%s", CloudflaredVersion, a.key)
}

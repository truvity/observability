package ec2

import "fmt"

// The pinned third-party binaries (cloudflared is pinned in truvity/edge, which
// renders the tunnel connector). Each has a version and a sha256 per
// architecture, copied from the upstream release; bumping one is an edit here
// (and a new release of this repository), reviewed like any other. Gatus is not
// here: it is built in this repository's release from the fork commit below and its
// checksum is read from the release's checksums.txt at deploy time.
const (
	// GatusVersion names the Gatus build: the upstream tag it is based on,
	// with "-pkce" while it is the truvity fork. It is the version in the
	// attached asset name (gatus_<GatusVersion>_linux_<arch>) and the Gatus
	// version the box runs. hack/build-gatus.sh reads it from here.
	GatusVersion = "v5.37.0-pkce"

	// GatusRepo and GatusRef are what hack/build-gatus.sh builds: the fork's
	// repository and a PINNED commit SHA (never a branch). The script refuses
	// to build when the checked-out commit is not GatusRef.
	//
	// Temporary fork, D698/D716: return to upstream (github.com/TwiN/gatus at
	// a release tag, GatusVersion without "-pkce") when it releases PKCE.
	// Fork branch pkce-v5.37.0: upstream v5.37.0 + the opt-in
	// security.oidc.pkce patch (5098ce92) + the fork's image workflow.
	GatusRepo = "https://github.com/truvity/gatus"
	GatusRef  = "8d60fe3cd6af7aef4f3a3e7a38dd2134c3cad4de"

	// LitestreamVersion is the Litestream release (without the leading v).
	LitestreamVersion = "0.5.17"
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
}

var (
	archARM64 = architecture{
		key:              "arm64",
		ssmAMI:           "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-arm64",
		litestreamArch:   "arm64",
		litestreamSHA256: "f8ca4a050095c1efbda2c4365172e61bf9d955ea0d9ac42f448b52e51819baa5",
	}
	archAMD64 = architecture{
		key:              "amd64",
		ssmAMI:           "/aws/service/ami-amazon-linux-latest/al2023-ami-kernel-default-x86_64",
		litestreamArch:   "x86_64",
		litestreamSHA256: "cfb371176d164437ae869f8351cfde49bd1804ae71c61923f75c9cba9c9c006d",
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

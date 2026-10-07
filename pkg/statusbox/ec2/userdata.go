package ec2

import (
	_ "embed"
	"encoding/base64"
	"fmt"
	"sort"
	"strings"

	"github.com/truvity/observability/pkg/statusbox"
)

// setupScript is /usr/local/sbin/statusbox-setup on the instance: see its own
// header for the phases. It is embedded rather than fetched, so the user-data
// IS the whole program (a change to it is a new launch template version, the
// same "immutable by construction" the Lightsail box has) and nothing at boot
// trusts a download except the binaries, which are checksum-pinned.
//
//go:embed setup.sh
var setupScript string

// userDataLimit is EC2's ceiling on user-data, in bytes before base64.
const userDataLimit = 16 * 1024

// shellLine is name=quoted-value for params.sh.
func shellLine(name, value string) string {
	return name + "=" + statusbox.ShellQuote(value) + "\n"
}

// shellArray is name=( 'a' 'b' ) for params.sh.
func shellArray(name string, values []string) string {
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = statusbox.ShellQuote(v)
	}

	return name + "=(" + strings.Join(quoted, " ") + ")\n"
}

// params renders /etc/statusbox/params.sh: every fact the setup script needs,
// and no secret. The SSM parameters appear as NAMES.
func (a Args) params(box names, gatusSHA map[string]string) (string, error) {
	var b strings.Builder

	b.WriteString("# Rendered by pkg/statusbox/ec2. Names and pinned checksums only; no secret.\n")
	b.WriteString(shellLine("SB_ASG_NAME", box.asg))
	b.WriteString(shellLine("SB_HOOK_NAME", box.hook))
	b.WriteString(shellLine("SB_BUCKET", a.Bucket))
	b.WriteString(shellLine("SB_PREFIX", a.BucketPrefix))
	b.WriteString(shellLine("SB_HEALTH_MINUTES", fmt.Sprint(a.healthMinutes())))
	b.WriteString(shellLine("SB_TUNNEL_PARAM", a.TunnelTokenParameter))

	// The environment a Gatus Config references: an AlertURLs key becomes
	// ALERT_URL_<KEY>, an Env key is the whole name.
	var env []string
	for _, k := range sortedKeys(a.AlertURLParameters) {
		env = append(env, statusbox.AlertURLName(k)+"="+a.AlertURLParameters[k])
	}

	for _, k := range sortedKeys(a.EnvParameters) {
		env = append(env, k+"="+a.EnvParameters[k])
	}

	b.WriteString(shellArray("SB_ENV_PARAMS", env))

	insts := make([]string, len(a.Instances))

	for i, inst := range a.Instances {
		db, err := databaseFile(inst)
		if err != nil {
			return "", err
		}

		insts[i] = fmt.Sprintf("%s:%d:%t:%s", inst.Name, inst.Port, inst.Public, db)
	}

	b.WriteString(shellArray("SB_INSTANCES", insts))

	for _, arch := range architectures {
		b.WriteString(shellLine("SB_GATUS_URL_"+arch.key, statusbox.ReleaseURL(a.Version, gatusAsset(arch))))
		b.WriteString(shellLine("SB_GATUS_SHA_"+arch.key, gatusSHA[arch.key]))
		b.WriteString(shellLine("SB_LITESTREAM_URL_"+arch.key, arch.litestreamURL()))
		b.WriteString(shellLine("SB_LITESTREAM_SHA_"+arch.key, arch.litestreamSHA256))
		b.WriteString(shellLine("SB_CLOUDFLARED_URL_"+arch.key, arch.cloudflaredURL()))
		b.WriteString(shellLine("SB_CLOUDFLARED_SHA_"+arch.key, arch.cloudflaredSHA256))
	}

	return b.String(), nil
}

// bootstrap renders the plain user-data script: it stages params.sh, every
// Gatus configuration and the trusted CA bundle, writes the setup script, and
// runs its install phase. Pure: no network, no Pulumi.
func (a Args) bootstrap(box names, gatusSHA map[string]string) (string, error) {
	if strings.Contains(setupScript, "\nSTATUSBOX_SETUP\n") || strings.Contains(setupScript, "\nSTATUSBOX_PARAMS\n") {
		return "", fmt.Errorf("statusbox/ec2: setup.sh contains a heredoc delimiter of the bootstrap")
	}

	params, err := a.params(box, gatusSHA)
	if err != nil {
		return "", err
	}

	var b strings.Builder

	b.WriteString("#!/bin/bash\n")
	b.WriteString("set -euo pipefail\n\n")
	b.WriteString("mkdir -p /etc/statusbox /opt/statusbox/staged /usr/local/sbin\n\n")
	fmt.Fprintf(&b, "cat > /etc/statusbox/params.sh <<'STATUSBOX_PARAMS'\n%sSTATUSBOX_PARAMS\n\n", params)

	if a.TrustedCAs != "" {
		gz, err := statusbox.GzipBase64(a.TrustedCAs)
		if err != nil {
			return "", fmt.Errorf("statusbox/ec2: gzip TrustedCAs: %w", err)
		}

		fmt.Fprintf(&b, "cat > /opt/statusbox/staged/trusted-cas.pem.gz.b64 <<'STATUSBOX_TRUSTED_CAS'\n%s\nSTATUSBOX_TRUSTED_CAS\n\n", gz)
	}

	for _, inst := range a.Instances {
		gz, err := statusbox.GzipBase64(inst.Config)
		if err != nil {
			return "", fmt.Errorf("statusbox/ec2: gzip Config for instance %q: %w", inst.Name, err)
		}

		delim := "STATUSBOX_CFG_" + strings.ToUpper(strings.ReplaceAll(inst.Name, "-", "_"))
		fmt.Fprintf(&b, "cat > /opt/statusbox/staged/%s.yaml.gz.b64 <<'%s'\n%s\n%s\n\n", inst.Name, delim, gz, delim)
	}

	fmt.Fprintf(&b, "cat > /usr/local/sbin/statusbox-setup <<'STATUSBOX_SETUP'\n%sSTATUSBOX_SETUP\n", ensureNewline(setupScript))
	b.WriteString("chmod 0755 /usr/local/sbin/statusbox-setup\n")
	b.WriteString("exec /usr/local/sbin/statusbox-setup install\n")

	return b.String(), nil
}

func ensureNewline(s string) string {
	if strings.HasSuffix(s, "\n") {
		return s
	}

	return s + "\n"
}

// UserData renders what the launch template carries: the bootstrap, gzipped
// into a short wrapper (see statusbox.WrapUserData), refused if over EC2's
// limit. It reads this release's checksums.txt through statusbox.FetchChecksums
// once, for the Gatus binaries' checksums. Nothing secret can be in it: Args
// holds no secret value, only SSM parameter names.
func (a Args) UserData(name string) (string, error) {
	if err := a.validate(); err != nil {
		return "", err
	}

	checksums, err := statusbox.FetchChecksums(a.Version)
	if err != nil {
		return "", err
	}

	gatusSHA := make(map[string]string, len(architectures))

	for _, arch := range architectures {
		sum, err := statusbox.ChecksumFor(checksums, gatusAsset(arch), a.Version)
		if err != nil {
			return "", err
		}

		gatusSHA[arch.key] = sum
	}

	script, err := a.bootstrap(namesFor(name), gatusSHA)
	if err != nil {
		return "", err
	}

	wrapped, err := statusbox.WrapUserData(script)
	if err != nil {
		return "", err
	}

	if len(wrapped) > userDataLimit {
		return "", fmt.Errorf("statusbox/ec2: rendered user-data is %d bytes, over EC2's %d-byte limit: shrink an instance's Config or run fewer instances on "+
			"this box", len(wrapped), userDataLimit)
	}

	return wrapped, nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}

// base64Of is the base64 text a launch template's UserData takes.
func base64Of(s string) string { return base64.StdEncoding.EncodeToString([]byte(s)) }

// Package statusbox holds what the status box's backends share: the Gatus
// instances a box runs, their validation, the Gatus page renderer and the
// small helpers (user-data wrapping, checksums, shell quoting) the EC2 backend
// in pkg/statusbox/ec2 builds on. See docs/statusbox.md and docs/doctrine.md
// ("The watcher lives outside") for the shape and the reasons.
//
// This package knows nothing about any cloud provider, which is the reason the
// AWS SDK never appears in it.
package statusbox

import (
	"bytes"
	"compress/gzip"
	"crypto/x509"
	"encoding/base64"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
)

// releaseRepo is where a Version is a tag of, and where checksums.txt is
// fetched from. It is this repository's own identity,
// not an estate's, so it is a constant rather than an input.
const releaseRepo = "truvity/observability"

// instanceNameRE is what an Instance.Name or a alert-URL key may
// look like.
//
// Both are interpolated into a shell heredoc delimiter and a file path in
// the rendered script, so a name containing a shell metacharacter would
// not merely look odd — a delimiter that fails to match ends the heredoc
// early and runs whatever follows as a command. Refused rather than
// escaped, for the reason pkg/tenancy gives for its own names: a name
// that needs escaping is a name nobody should have chosen, and both of
// these names are the estate's own to choose.
var instanceNameRE = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// alertKeyRE is what a alert-URL key may look like: it becomes
// the suffix of an environment variable name (ALERT_URL_<KEY>) that a
// Config may reference for Gatus's own environment-variable
// substitution, so it has to be one.
var alertKeyRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// envNameRE is what a env key may look like: unlike an
// AlertURLs key, it becomes the WHOLE environment variable name a
// Config references (no ALERT_URL_ prefix added), so the shape has to
// be a valid one on its own.
var envNameRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// reservedEnvNames are the environment variable names the box already gives a
// fixed meaning to. An env entry using one of these would silently overwrite
// that meaning (TUNNEL_TOKEN).
var reservedEnvNames = map[string]bool{
	"TUNNEL_TOKEN": true,
}

// alertURLName is the environment variable name an alert key k becomes:
// ALERT_URL_<K>, upper-cased. ValidateSecretNames uses it to refuse an env
// entry that would collide with it.
func alertURLName(k string) string {
	return "ALERT_URL_" + strings.ToUpper(k)
}

// Instance is one Gatus process on the box: its own image, its own
// configuration, its own SQLite file on the attached disk. There is no
// shared database and no clustering between instances — see
// docs/statusbox.md ("The shape") for why that is not an oversight.
type Instance struct {
	// Name becomes the container name, gatus-<Name>, the SQLite file's
	// directory on /data, and (see Hostnames) the key an estate's
	// hostname mapping is looked up by. It is shaped like a Kubernetes
	// DNS label for the same reason a cluster or namespace name is in
	// pkg/tenancy: it is interpolated into the rendered script, so a
	// wider shape would be a way to inject into it.
	Name string

	// Port is the loopback port this instance's Gatus is published on:
	// what a Public instance's tunnel ingress rule points at; a private
	// instance listens on it on every interface (the security group admits
	// the allowed networks). Two instances cannot share a Port, because a
	// second instance on the same port would either fail to start or
	// silently replace the first one
	// in the compose file, neither of which is a startup a boot log
	// makes obvious.
	Port int

	// Public says whether this instance's page belongs in the tunnel's
	// ingress (see Hostnames) or is reachable only over the private
	// network. gatus-ops, the deadman and probe receiver, is never
	// Public: docs/statusbox.md's whole reason for existing is that the
	// deadman is received somewhere the estate's own failure cannot
	// reach, and a public ingress rule is one more thing that can be
	// down when the estate is.
	//
	// At most one Instance may set this false. The box serves one
	// combined private page by design (see "The shape" in
	// docs/statusbox.md: every company's own component and every piece
	// of cluster infrastructure belong on the SAME private page, not one
	// each), and a second private instance would need its own port opened
	// to the same networks. Validation refuses a second one rather than let
	// the box silently pick a winner.
	Public bool

	// Config is the Gatus YAML for this instance, already rendered by
	// the estate. This package never inspects it — a page's endpoints,
	// its alerting integrations and its heartbeat interval are entirely
	// the caller's — it only carries it to the box, compressed.
	Config string
}

// validateInstances is the part of validate that concerns the instances and
// the hostnames of the public ones. It is shared with the EC2 backend
// (ValidateInstances) so both refuse the same shapes with the same
// messages. anyPublic reports whether some instance is Public.
func validateInstances(instances []Instance, hostnames map[string]string) (anyPublic bool, errs []error) {
	if len(instances) == 0 {
		errs = append(errs, errors.New("statusbox: no instances: a box with nothing to run is not a box worth provisioning"))
	}

	names := map[string]bool{}
	ports := map[int]string{}
	var privateNames []string

	for i, inst := range instances {
		where := fmt.Sprintf("statusbox: instance[%d]", i)
		if inst.Name != "" {
			where = fmt.Sprintf("statusbox: instance %q", inst.Name)
		}

		switch {
		case inst.Name == "":
			errs = append(errs, fmt.Errorf("%s: Name is empty", where))
		case !instanceNameRE.MatchString(inst.Name):
			errs = append(errs, fmt.Errorf("%s: Name %q is not a valid name (%s): it becomes a heredoc delimiter and a file path in the rendered script, so a name "+
				"outside this shape could end the heredoc early and run whatever follows as a command", where, inst.Name, instanceNameRE))
		case names[inst.Name]:
			errs = append(errs, fmt.Errorf("%s: appears twice; the second instance would silently overwrite the first one's staged files under the same name", where))
		default:
			names[inst.Name] = true
		}

		if inst.Port <= 0 || inst.Port > 65535 {
			errs = append(errs, fmt.Errorf("%s: Port %d is not a valid TCP port", where, inst.Port))
		} else if other, ok := ports[inst.Port]; ok {
			errs = append(errs, fmt.Errorf("%s: Port %d is also used by instance %q. Two Gatus instances cannot share a port: the second would either "+
				"fail to bind or replace the first", where, inst.Port, other))
		} else {
			ports[inst.Port] = inst.Name
		}

		if strings.TrimSpace(inst.Config) == "" {
			errs = append(errs, fmt.Errorf("%s: Config is empty: there is no Gatus YAML to stage, which is indistinguishable from a caller that forgot to render "+
				"one", where))
		}

		if inst.Public {
			anyPublic = true
			if strings.TrimSpace(hostnames[inst.Name]) == "" {
				errs = append(errs, fmt.Errorf("%s: Public is true but Hostnames[%q] is empty. A public instance with no hostname is a page this box is about to serve "+
					"with no tunnel ingress rule pointed at it — add the hostname to the Hostnames (the tunnel ingress is the estate's own "+
					"edge configuration to own)", where, inst.Name))
			}
		} else if inst.Name != "" {
			privateNames = append(privateNames, inst.Name)
		}
	}

	if len(privateNames) > 1 {
		errs = append(errs, fmt.Errorf("statusbox: %d instances are not Public (%s): this box serves at most one private instance: the box "+
			"has one private page by design. Make every instance but one Public, or run the extra private instance on a second "+
			"box", len(privateNames), strings.Join(privateNames, ", ")))
	}

	for name := range hostnames {
		if name != "" && !names[name] {
			errs = append(errs, fmt.Errorf("statusbox: Hostnames[%q] names no instance in Args.Instances: it would never be used, which is the likeliest sign of a "+
				"typo in one or the other", name))
		}
	}

	return anyPublic, errs
}

// wrapUserData gzips and base64-encodes a multi-line script and returns
// the user-data cloud-init actually boots from it: a leading
// `#!/bin/bash` line followed by a `bash -c "$(...)"` line that
// substitutes the decoded (and, at that point, multi-line) script as
// bash's own argument.
//
// The shebang line is not decoration. cloud-init classifies user-data by
// its FIRST line alone: `#!` is what makes it treat the payload as
// text/x-shellscript and execute it; anything else — including a bare
// `bash -c "..."` line, which is what this function used to return on
// its own — becomes text/plain, which cloud-init stores on the box and
// never runs. See docs/statusbox.md for the full story and why this
// used to be believed to need a single physical line: it does not — the
// only real constraint on user-data is its size cap, not a line count.
func wrapUserData(script string) (string, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(script)); err != nil {
		return "", fmt.Errorf("statusbox: gzip bootstrap script: %w", err)
	}
	if err := gz.Close(); err != nil {
		return "", fmt.Errorf("statusbox: gzip bootstrap script: %w", err)
	}
	b64 := base64.StdEncoding.EncodeToString(buf.Bytes())
	return fmt.Sprintf("#!/bin/bash\n"+`bash -c "$(echo %s | base64 -d | gunzip)"`+"\n", shellQuote(b64)), nil
}

// parseTrustedCAs validates TrustedCAs: it must decode as one or
// more PEM blocks, each of type CERTIFICATE, each itself a CA per its
// own BasicConstraints extension. It returns nothing on success —
// render stages a.TrustedCAs's own text unchanged, byte for byte, so
// this function's only job is to refuse a bundle that could not
// possibly work as a trust anchor before it ever reaches a box.
//
// Trailing bytes after the last PEM block that do not themselves decode
// as one are refused too, on the same reasoning pkg/tenancy applies to
// a name that would need escaping: a bundle nobody meant to paste
// garbage after is a bundle where that garbage is a mistake worth
// surfacing now, not silently ignored the way pem.Decode's own "rest"
// return would let it be.
func parseTrustedCAs(bundle string) error {
	rest := []byte(bundle)
	n := 0
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		n++
		if block.Type != "CERTIFICATE" {
			return fmt.Errorf("statusbox: TrustedCAs: PEM block %d is %q, not CERTIFICATE: only a CA certificate belongs in this bundle", n, block.Type)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return fmt.Errorf("statusbox: TrustedCAs: PEM block %d does not parse as an X.509 certificate: %w", n, err)
		}
		if !cert.IsCA {
			return fmt.Errorf("statusbox: TrustedCAs: PEM block %d (subject %q) is not a CA certificate (BasicConstraints.IsCA is not set): a leaf or "+
				"intermediate missing the CA bit cannot act as a trust anchor", n, cert.Subject)
		}
	}
	if n == 0 {
		return errors.New("statusbox: TrustedCAs is set but contains no PEM CERTIFICATE block")
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return fmt.Errorf("statusbox: TrustedCAs: %d byte(s) follow the last PEM block and are not themselves a PEM block", len(bytes.TrimSpace(rest)))
	}
	return nil
}

// gzipBase64 compresses and base64-encodes a Config so it can travel as
// the body of a quoted heredoc: base64's alphabet has no shell
// metacharacter in it, so nothing inside the blob can end the heredoc
// early regardless of what the original YAML contained.
func gzipBase64(s string) (string, error) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	if _, err := gz.Write([]byte(s)); err != nil {
		return "", err
	}
	if err := gz.Close(); err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), nil
}

// shellQuote wraps a value in single quotes for interpolation into the
// rendered script, escaping an embedded single quote with the standard
// close-quote/escaped-quote/reopen-quote trick. Every value this package
// puts into the script goes through here, the same way every value
// pkg/tenancy puts into a match_claims map goes through claimMatch: the
// property needs to be structural, not something each call site has to
// remember.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// FetchChecksums downloads a release's checksums.txt and returns its
// body. pkg/statusbox/ec2 calls it once per render, with the release Version.
//
// It is an exported variable rather than an unexported function or a
// plain constant for two reasons that both come down to the same thing:
// this package's only network access has to be replaceable from outside
// itself. A test in another package replaces it with a fixed body so the
// Pulumi plumbing can be exercised without a released version to fetch from. And an estate whose Pulumi
// runs with no general internet egress — the same posture that puts a
// box outside every cluster in the first place — can point it at an
// internal mirror of this repository's releases instead of GitHub.
var FetchChecksums = func(version string) (string, error) {
	url := fmt.Sprintf("https://github.com/%s/releases/download/%s/checksums.txt", releaseRepo, version)
	// The URL is built from a constant and a caller-supplied tag, over
	// HTTPS, at deploy time; no request context is available this deep in
	// a Pulumi ApplyT chain.
	resp, err := http.Get(url) //nolint:gosec,noctx
	if err != nil {
		return "", fmt.Errorf("statusbox: fetch %s: %w", url, err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("statusbox: fetch %s: HTTP %d — %q is not a released version of %s, or checksums.txt was not attached to "+
			"it", url, resp.StatusCode, version, releaseRepo)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("statusbox: read %s: %w", url, err)
	}
	return string(body), nil
}

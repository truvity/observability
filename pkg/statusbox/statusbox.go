// Package statusbox renders the cloud-init that turns a small virtual
// machine into the watcher outside: see docs/statusbox.md and
// docs/doctrine.md ("The watcher lives outside") for the shape and the
// reasons.
//
// This package knows nothing about any cloud provider. It takes a
// version of this repository, a list of Gatus instances and a handful
// of secrets, and returns the single string a provider's user-data field
// carries. pkg/statusbox/lightsail is the first place that string is
// used; a second provider is a small package over the same CloudInit,
// which is the reason the AWS SDK never appears in this file.
package statusbox

import (
	"bytes"
	"compress/gzip"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
)

// releaseRepo is where a Version is a tag of, and where setup.sh and
// checksums.txt are fetched from. It is this repository's own identity,
// not an estate's, so it is a constant rather than an input.
const releaseRepo = "truvity/observability"

// userDataLimit is the first provider's ceiling on rendered user-data:
// 16 KB, in bytes, on Lightsail. CloudInit refuses to render past it —
// see the doc comment on CloudInit for why a refusal and not a silent
// truncation.
const userDataLimit = 16 * 1024

// instanceNameRE is what an Instance.Name or a Secrets.AlertURLs key may
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

// alertKeyRE is what a Secrets.AlertURLs key may look like: it becomes
// the suffix of an environment variable name (ALERT_URL_<KEY>) that a
// Config may reference for Gatus's own environment-variable
// substitution, so it has to be one.
var alertKeyRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// envNameRE is what a Secrets.Env key may look like: unlike an
// AlertURLs key, it becomes the WHOLE environment variable name a
// Config references (no ALERT_URL_ prefix added), so the shape has to
// be a valid one on its own.
var envNameRE = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

// reservedEnvNames are the environment variable names CloudInit and
// setup.sh already give a fixed meaning to. A Secrets.Env entry using
// one of these would either silently overwrite that meaning (TS_AUTHKEY,
// TUNNEL_TOKEN — see render) or collide with the ALERT_URL_ namespace
// AlertURLs owns (see the alertURLName collision check in validate).
var reservedEnvNames = map[string]bool{
	"TS_AUTHKEY":   true,
	"TUNNEL_TOKEN": true,
}

// alertURLName is the environment variable name AlertURLs key k becomes:
// ALERT_URL_<K>, upper-cased. Shared between validate (to refuse a
// Secrets.Env entry that would collide with it) and render.
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
	// what a Public instance's tunnel ingress rule points at. A private
	// instance is reached over the tailnet on port 80 instead, always —
	// never this Port — so an operator loads it at plain `http://
	// <Hostname>/`, no port to remember; see setup.sh's
	// serve_private_instances. Two instances cannot share a Port — see
	// the refusal in Args.validate — because setup.sh publishes each
	// container at 127.0.0.1:<Port>, and a second instance on the same
	// port would either fail to start or silently replace the first one
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
	// each) and setup.sh's serve_private_instances forwards it to the
	// tailnet on the one port that needs no port in the URL, 80 — a
	// second private instance would need that same port 80 on the same
	// box and cannot have it. Args.validate refuses a second one rather
	// than let setup.sh silently pick a winner.
	Public bool

	// Config is the Gatus YAML for this instance, already rendered by
	// the estate. This package never inspects it — a page's endpoints,
	// its alerting integrations and its heartbeat interval are entirely
	// the caller's — it only carries it to the box, compressed.
	Config string
}

// Secrets are the values CloudInit needs and Config must not carry
// itself: a Config the estate writes down (in its own repository,
// generated from its own catalogue) that also carried a bearer secret
// would be a secret checked in wherever that YAML is. Each field ends up
// in the rendered user-data as plain text nonetheless — see CloudInit's
// doc comment on why that is an accepted, documented risk rather than a
// gap.
type Secrets struct {
	// TailscaleAuthKey is a one-shot pre-authorised key: setup.sh uses
	// it once, at `tailscale up`, and it is never read again after the
	// box's first boot. Required — the tailnet is how the install's
	// Alertmanager reaches gatus-ops, and how an operator reaches the
	// box at all, so a box that cannot join it is a box that provisioned
	// into nothing.
	TailscaleAuthKey pulumi.StringInput

	// TunnelToken authenticates cloudflared to the one tunnel that
	// carries every Public instance's ingress rule. Required only when
	// at least one Instance is Public — a box with nothing to publish
	// has nothing for the tunnel to carry, and asking an estate for a
	// token it would only leave unused is a refusal waiting to happen
	// for no reason.
	TunnelToken pulumi.StringInput

	// AlertURLs are push-service endpoints for Gatus's OWN alerting,
	// keyed by whatever name an instance's Config references as
	// ${ALERT_URL_<KEY>} (the key upper-cased) using Gatus's
	// environment-variable substitution. doctrine.md's "the deadman's
	// alert" is why this exists at all: gatus-ops has to alert on its
	// own silence through a channel that does not depend on the one the
	// rest of the estate's alerts use, and the value that channel needs
	// is exactly the kind of thing that should never be typed into a
	// Config literally. Optional: an instance whose Config references no
	// such variable needs no entry here.
	AlertURLs map[string]pulumi.StringInput

	// Env carries any OTHER secret a Config needs to reference by its
	// own environment-variable substitution — Gatus's `security.oidc`
	// block asking for `client-secret: ${OIDC_CLIENT_SECRET}`, for
	// instance. It differs from AlertURLs in exactly one way: a key
	// here becomes the WHOLE variable name a Config writes as ${<KEY>}
	// — no ALERT_URL_ prefix is added — because unlike an alert push
	// URL, which is always "the credential for THIS instance's own
	// alerting", the shape of what a Config needs a secret for here is
	// not this package's to guess or namespace.
	//
	// A key colliding with TS_AUTHKEY, TUNNEL_TOKEN, or the
	// ALERT_URL_<NAME> an AlertURLs entry already produces is refused in
	// Args.validate — both are this package's own reserved names, and a
	// Config silently getting the wrong one of two values written under
	// the same variable is worse than a refusal at deploy time. Optional:
	// an instance whose Config references no such variable needs no
	// entry here.
	Env map[string]pulumi.StringInput
}

// Args is CloudInit's whole input: a version of this repository, the
// instances to run, the secrets they need, and the hostname a Public
// instance is expected to answer on.
type Args struct {
	// Version names a release of this repository. setup.sh and
	// checksums.txt are fetched from that release's assets — see
	// CloudInit's doc comment — so a Version that is not a real tag is a
	// box that refuses to boot rather than one that boots a stale or
	// wrong script.
	Version string

	Instances []Instance

	Secrets Secrets

	// Hostname is the box's OWN tailnet device name — the OS hostname
	// `setup.sh` passes to `tailscale up --hostname=`, distinct from
	// (and not to be confused with) Hostnames below, which is about a
	// PUBLIC instance's page, not the box itself.
	//
	// Required, and shaped like an Instance.Name for the same reason:
	// without it the box joins under whatever hostname the provider's
	// own image happens to boot with, which is not a fact CloudInit
	// controls or a caller can predict — and an estate that wants to
	// reach the box by a stable tailnet name (MagicDNS: `<Hostname>.
	// <tailnet>.ts.net`), including from a DNS record that follows the
	// box across a replacement, needs that name to be a fact it chose
	// rather than one it has to go and read off a running instance
	// after the fact.
	Hostname string

	// Hostnames maps an Instance.Name to the public hostname its tunnel
	// ingress rule is expected to carry. It is never rendered into the
	// script — setup.sh "knows no hostname" by design, see
	// docs/statusbox.md, because the ingress rule is the estate's edge
	// configuration's to own and would otherwise have to be kept in two
	// places at once. What Hostnames is for is the refusal: a Public
	// instance with no entry here is one whose page an estate is about
	// to stand up with no tunnel rule pointed at it, and CloudInit
	// catches that at the one moment the caller cannot easily discover
	// it any other way — before the box has been provisioned at all,
	// rather than after a customer finds the ingress rule missing.
	Hostnames map[string]string

	// TrustedCAs is an optional PEM bundle of one or more extra root
	// certificates every Gatus instance's own TLS verification should
	// trust, alongside its image's ordinary public bundle — never
	// instead of it (see write_compose's own doc comment in setup.sh for
	// why an addition, not a replacement, is what actually happens at
	// runtime).
	//
	// It exists for exactly one shape of probe: an `endpoints:` entry
	// whose URL is an HTTPS service inside the estate's own private
	// network, whose certificate is issued by the estate's own private
	// root rather than a publicly-trusted one — "internal → status,
	// pulled" in docs/statusbox.md is the case this was built for, since
	// that probe also carries a bearer token in its Authorization
	// header, which is exactly the situation `-k` / `skip-tls-verify`
	// must never be reached for as an answer: a probe that skips
	// verification would send a token to whatever answered on that IP,
	// verified or not. Trusting the estate's own root instead of
	// skipping verification is what lets the handshake succeed AND the
	// token stay protected.
	//
	// Left empty (the ordinary case: most probes are public HTTPS with a
	// publicly-trusted certificate), nothing changes: no file is staged,
	// no directory is mounted into a container, no environment variable
	// is set, and the rendered cloud-init is byte-for-byte what it was
	// before this field existed — see TestRenderGoldenTwoInstances, whose
	// fixture never sets it.
	//
	// Validated at render time (see Args.validate and parseTrustedCAs):
	// it must decode as one or more PEM blocks, each of type
	// CERTIFICATE, each itself a CA per its own BasicConstraints
	// extension (x509.Certificate.IsCA) — a bundle containing a leaf or
	// intermediate certificate missing the CA bit would be silently
	// useless as a trust anchor, so it is refused here rather than left
	// for an operator to discover the day a probe's handshake still
	// fails.
	TrustedCAs string
}

// CloudInit renders the script a provider's user-data field carries: a
// single self-verifying bootstrap that stages every instance's
// configuration and every secret, fetches setup.sh from the release
// named by Version, and refuses to run it if its checksum does not
// match.
//
// Checksum at deploy, verify at boot. The caller pins only Version; this
// function fetches that release's checksums.txt over the network at
// CALL time and bakes the sha256 it finds for setup.sh into the
// rendered script. The box downloads setup.sh from the same release at
// boot and runs `sha256sum -c` on it before executing it. That is the
// whole difference between `curl | sh` as a moving target and
// `curl | sh` as something content-addressed: the hash is not
// hand-copied by anyone, and a release whose setup.sh does not match its
// own checksums.txt produces a box that refuses to finish booting
// instead of one that ran whatever it was handed.
//
// The rendered bootstrap is gzipped and base64-encoded once, and the
// returned user-data is a short `#!/bin/bash` wrapper that decodes and
// runs it — see wrapUserData for why the leading shebang line is load
// bearing rather than cosmetic. CloudInit refuses to return a string
// over 16 KB (Lightsail's ceiling) rather than let a provider silently
// accept and then never boot a user-data payload it truncated — see the
// size-limit refusal below. Every instance's Config is inside that same
// gzip, which is where headroom for the limit actually comes from; the
// wrapper and the staged secrets are tiny by comparison.
//
// user-data is readable, in plaintext, from the instance metadata
// service by any process running on the box — this is true of every
// cloud provider's user-data mechanism, not a defect of this one. It is
// an accepted risk rather than a gap because of what the box is: single
// purpose, nothing else ever runs on it, the tailnet key is one-shot and
// spent at first boot, and an alert URL in Secrets is rotated the day
// the box is ever asked to be anything else.
func CloudInit(_ *pulumi.Context, a Args) (pulumi.StringOutput, error) {
	// ctx is unused today: nothing here registers a resource or reads
	// stack configuration. It stays in the signature — see
	// docs/statusbox.md — so a provider-neutral need that does need one
	// (a config value, a stack reference) has somewhere to come from
	// without changing every caller's call site.

	if err := a.validate(); err != nil {
		return pulumi.StringOutput{}, err
	}

	checksums, err := FetchChecksums(a.Version)
	if err != nil {
		return pulumi.StringOutput{}, err
	}
	sha, err := setupSHA256(checksums, a.Version)
	if err != nil {
		return pulumi.StringOutput{}, err
	}

	alertKeys := sortedKeys(a.Secrets.AlertURLs)
	envKeys := sortedKeys(a.Secrets.Env)

	type slot struct {
		key string
	}
	var slots []slot
	var inputs []interface{}
	add := func(key string, in pulumi.StringInput) {
		if in == nil {
			return
		}
		slots = append(slots, slot{key: key})
		inputs = append(inputs, in)
	}
	add("tailscale", a.Secrets.TailscaleAuthKey)
	add("tunnel", a.Secrets.TunnelToken)
	for _, k := range alertKeys {
		add("alert:"+k, a.Secrets.AlertURLs[k])
	}
	for _, k := range envKeys {
		add("env:"+k, a.Secrets.Env[k])
	}

	out := pulumi.All(inputs...).ApplyT(func(vals []interface{}) (string, error) {
		values := make(map[string]string, len(slots))
		for i, s := range slots {
			values[s.key] = vals[i].(string)
		}
		alertVals := make(map[string]string, len(alertKeys))
		for _, k := range alertKeys {
			alertVals[k] = values["alert:"+k]
		}
		envVals := make(map[string]string, len(envKeys))
		for _, k := range envKeys {
			envVals[k] = values["env:"+k]
		}
		return render(a, sha, values["tailscale"], values["tunnel"], alertVals, envVals)
	})

	return out.(pulumi.StringOutput), nil
}

// validate reports every problem it can find, not just the first — the
// same reason pkg/tenancy's Validate does: an estate's renderer that
// produced three bad instances should not have to be run four times to
// learn that.
func (a Args) validate() error {
	var errs []error

	if a.Version == "" {
		errs = append(errs, errors.New("statusbox: Version is empty: setup.sh and checksums.txt are fetched from a release of this repository named by Version, "+
			"and there is no release named \"\""))
	}

	switch {
	case a.Hostname == "":
		errs = append(errs, errors.New("statusbox: Hostname is empty: the box needs a stable tailnet device name, and an unset one leaves it whatever the "+
			"provider's image happens to boot with"))
	case !instanceNameRE.MatchString(a.Hostname):
		errs = append(errs, fmt.Errorf("statusbox: Hostname %q is not a valid name (%s): it is passed to `tailscale up --hostname=`, so a name outside this shape "+
			"is refused rather than escaped", a.Hostname, instanceNameRE))
	}

	if len(a.Instances) == 0 {
		errs = append(errs, errors.New("statusbox: no instances: a box with nothing to run is not a box worth provisioning"))
	}

	names := map[string]bool{}
	ports := map[int]string{}
	anyPublic := false
	var privateNames []string

	for i, inst := range a.Instances {
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
			errs = append(errs, fmt.Errorf("%s: Port %d is also used by instance %q. Two Gatus instances cannot share a port: setup.sh publishes each one at "+
				"127.0.0.1:<Port>, and the second would either fail to bind or replace the first in the compose file", where, inst.Port, other))
		} else {
			ports[inst.Port] = inst.Name
		}

		if strings.TrimSpace(inst.Config) == "" {
			errs = append(errs, fmt.Errorf("%s: Config is empty: there is no Gatus YAML to stage, which is indistinguishable from a caller that forgot to render "+
				"one", where))
		}

		if inst.Public {
			anyPublic = true
			if strings.TrimSpace(a.Hostnames[inst.Name]) == "" {
				errs = append(errs, fmt.Errorf("%s: Public is true but Hostnames[%q] is empty. A public instance with no hostname is a page this box is about to serve "+
					"with no tunnel ingress rule pointed at it — add the hostname to Args.Hostnames (setup.sh itself never sees it; the tunnel ingress is the estate's own "+
					"edge configuration to own)", where, inst.Name))
			}
		} else if inst.Name != "" {
			privateNames = append(privateNames, inst.Name)
		}
	}

	if len(privateNames) > 1 {
		errs = append(errs, fmt.Errorf("statusbox: %d instances are not Public (%s): this box serves at most one private instance. setup.sh forwards it to the "+
			"tailnet on port 80 — the one port that needs no port in the URL an operator loads, http://<Hostname>/ — and a second private instance would need that "+
			"same port 80 on the same box and cannot have it. Make every instance but one Public, or run the extra private instance on a second "+
			"box", len(privateNames), strings.Join(privateNames, ", ")))
	}

	for name := range a.Hostnames {
		if name != "" && !names[name] {
			errs = append(errs, fmt.Errorf("statusbox: Hostnames[%q] names no instance in Args.Instances: it would never be used, which is the likeliest sign of a "+
				"typo in one or the other", name))
		}
	}

	for k := range a.Secrets.AlertURLs {
		if !alertKeyRE.MatchString(k) {
			errs = append(errs, fmt.Errorf("statusbox: Secrets.AlertURLs key %q is not a valid name (%s): it becomes the suffix of an environment variable, "+
				"ALERT_URL_%s, that a Config may reference", k, alertKeyRE, strings.ToUpper(k)))
		}
	}

	alertURLNames := map[string]string{}
	for k := range a.Secrets.AlertURLs {
		alertURLNames[alertURLName(k)] = k
	}
	for k := range a.Secrets.Env {
		where := fmt.Sprintf("statusbox: Secrets.Env key %q", k)
		switch {
		case !envNameRE.MatchString(k):
			errs = append(errs, fmt.Errorf("%s is not a valid environment-variable name (%s)", where, envNameRE))
		case reservedEnvNames[k]:
			errs = append(errs, fmt.Errorf("%s is reserved by statusbox itself (see Secrets.TailscaleAuthKey / Secrets.TunnelToken)", where))
		case alertURLNames[k] != "":
			errs = append(errs, fmt.Errorf("%s collides with Secrets.AlertURLs[%q], which already becomes the environment variable %s: a Config referencing ${%s} "+
				"would get whichever of the two happened to be written last", where, alertURLNames[k], k, k))
		}
	}

	if a.TrustedCAs != "" {
		if err := parseTrustedCAs(a.TrustedCAs); err != nil {
			errs = append(errs, err)
		}
	}

	if a.Secrets.TailscaleAuthKey == nil {
		errs = append(errs, errors.New("statusbox: Secrets.TailscaleAuthKey is nil: the tailnet is how the install's Alertmanager reaches gatus-ops and how an "+
			"operator reaches the box at all, so it is required on every box"))
	}
	if anyPublic && a.Secrets.TunnelToken == nil {
		errs = append(errs, errors.New("statusbox: at least one instance is Public but Secrets.TunnelToken is nil: a public page needs the tunnel that carries its "+
			"ingress rule"))
	}

	return errors.Join(errs...)
}

// manifestInstance is the shape setup.sh reads back out of
// /opt/statusbox/staged/manifest.json. It carries exactly what setup.sh
// needs to build a compose service and nothing an estate would recognise
// as its own — see Args.Hostnames's doc comment for why a hostname is
// not one of these fields.
type manifestInstance struct {
	Name   string `json:"name"`
	Port   int    `json:"port"`
	Public bool   `json:"public"`
}

// render is CloudInit's pure core: given the secret VALUES (already
// resolved out of their pulumi.StringInput) and the checksum, it builds
// the bootstrap script, wraps it into the user-data cloud-init will
// actually boot from, and refuses to return a result over
// userDataLimit. Kept separate from CloudInit so it can be tested
// without a Pulumi context or the network call CloudInit itself makes.
func render(a Args, setupSHA256, tailscaleKey, tunnelToken string, alertVals, envVals map[string]string) (string, error) {
	var b strings.Builder

	b.WriteString("#!/bin/bash\n")
	b.WriteString("set -euo pipefail\n\n")

	fmt.Fprintf(&b, "export STATUSBOX_VERSION=%s\n", shellQuote(a.Version))
	fmt.Fprintf(&b, "export STATUSBOX_SETUP_SHA256=%s\n", shellQuote(setupSHA256))
	fmt.Fprintf(&b, "export TS_HOSTNAME=%s\n", shellQuote(a.Hostname))
	fmt.Fprintf(&b, "export TS_AUTHKEY=%s\n", shellQuote(tailscaleKey))
	fmt.Fprintf(&b, "export TUNNEL_TOKEN=%s\n", shellQuote(tunnelToken))
	for _, k := range sortedStringKeys(alertVals) {
		fmt.Fprintf(&b, "export %s=%s\n", alertURLName(k), shellQuote(alertVals[k]))
	}
	// Secrets.Env is staged under a STATUSBOX_ENV_ prefix rather than
	// exported under its own name directly: setup.sh's write_env has to
	// tell "a secret CloudInit staged for a container" apart from every
	// OTHER environment variable already present in the boot script's own
	// shell (PATH, HOME, STATUSBOX_VERSION above, ...), and `grep
	// '^STATUSBOX_ENV_'` is how — setup.sh strips the prefix back off
	// before writing the real name (see write_env), so a Config still
	// only ever writes ${<KEY>}, never ${STATUSBOX_ENV_<KEY>}.
	for _, k := range sortedStringKeys(envVals) {
		fmt.Fprintf(&b, "export STATUSBOX_ENV_%s=%s\n", k, shellQuote(envVals[k]))
	}

	b.WriteString("\nmkdir -p /opt/statusbox/staged\n\n")

	manifest := make([]manifestInstance, len(a.Instances))
	for i, inst := range a.Instances {
		manifest[i] = manifestInstance{Name: inst.Name, Port: inst.Port, Public: inst.Public}
	}
	// TrustedCAs is a bool here, not the bundle itself: manifest.json is
	// what setup.sh reads back to tell "a bundle was staged" from "none
	// was" (see setup_trusted_cas in setup.sh) without probing the
	// filesystem for a file that only sometimes exists, the same reason
	// every other staged artefact here is manifest-driven. `omitempty`
	// keeps an Args with no TrustedCAs producing the exact manifest JSON
	// it always has — see TestRenderGoldenTwoInstances, whose fixture
	// never sets this field, so the golden file already proves it.
	manifestJSON, err := json.Marshal(struct {
		Instances  []manifestInstance `json:"instances"`
		TrustedCAs bool               `json:"trustedCAs,omitempty"`
	}{Instances: manifest, TrustedCAs: a.TrustedCAs != ""})
	if err != nil {
		return "", fmt.Errorf("statusbox: marshal manifest: %w", err)
	}
	fmt.Fprintf(&b, "cat > /opt/statusbox/staged/manifest.json <<'STATUSBOX_MANIFEST'\n%s\nSTATUSBOX_MANIFEST\n\n", manifestJSON)

	// Staged only when set: an Args that leaves TrustedCAs empty (the
	// ordinary case) adds nothing here at all, which is what keeps this
	// function's output byte-identical to a pre-TrustedCAs render for
	// every existing caller.
	if a.TrustedCAs != "" {
		gz, err := gzipBase64(a.TrustedCAs)
		if err != nil {
			return "", fmt.Errorf("statusbox: gzip Args.TrustedCAs: %w", err)
		}
		fmt.Fprintf(&b, "cat > /opt/statusbox/staged/trusted-cas.pem.gz.b64 <<'STATUSBOX_TRUSTED_CAS'\n%s\nSTATUSBOX_TRUSTED_CAS\n\n", gz)
	}

	for _, inst := range a.Instances {
		gz, err := gzipBase64(inst.Config)
		if err != nil {
			return "", fmt.Errorf("statusbox: gzip Config for instance %q: %w", inst.Name, err)
		}
		delim := "STATUSBOX_CFG_" + strings.ToUpper(strings.ReplaceAll(inst.Name, "-", "_"))
		fmt.Fprintf(&b, "cat > /opt/statusbox/staged/%s.yaml.gz.b64 <<'%s'\n%s\n%s\n\n", inst.Name, delim, gz, delim)
	}

	fmt.Fprintf(&b, "curl -fsSL -o /opt/statusbox/setup.sh \"https://github.com/%s/releases/download/${STATUSBOX_VERSION}/setup.sh\"\n", releaseRepo)
	b.WriteString(`echo "${STATUSBOX_SETUP_SHA256}  /opt/statusbox/setup.sh" | sha256sum -c -` + "\n")
	b.WriteString("chmod +x /opt/statusbox/setup.sh\n")
	b.WriteString("exec /opt/statusbox/setup.sh\n")

	wrapped, err := wrapUserData(b.String())
	if err != nil {
		return "", err
	}
	if len(wrapped) > userDataLimit {
		return "", fmt.Errorf("statusbox: rendered user-data is %d bytes, over the %d-byte limit the first provider imposes: shrink an instance's Config (every "+
			"one is already gzipped) or run fewer instances on this box", len(wrapped), userDataLimit)
	}
	return wrapped, nil
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
// only real constraint Lightsail's user-data has is the size cap this
// package already enforces as userDataLimit, not a line count.
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

// parseTrustedCAs validates Args.TrustedCAs: it must decode as one or
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
			return fmt.Errorf("statusbox: Args.TrustedCAs: PEM block %d is %q, not CERTIFICATE: only a CA certificate belongs in this bundle", n, block.Type)
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return fmt.Errorf("statusbox: Args.TrustedCAs: PEM block %d does not parse as an X.509 certificate: %w", n, err)
		}
		if !cert.IsCA {
			return fmt.Errorf("statusbox: Args.TrustedCAs: PEM block %d (subject %q) is not a CA certificate (BasicConstraints.IsCA is not set): a leaf or "+
				"intermediate missing the CA bit cannot act as a trust anchor", n, cert.Subject)
		}
	}
	if n == 0 {
		return errors.New("statusbox: Args.TrustedCAs is set but contains no PEM CERTIFICATE block")
	}
	if len(bytes.TrimSpace(rest)) != 0 {
		return fmt.Errorf("statusbox: Args.TrustedCAs: %d byte(s) follow the last PEM block and are not themselves a PEM block", len(bytes.TrimSpace(rest)))
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

func sortedKeys(m map[string]pulumi.StringInput) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func sortedStringKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// FetchChecksums downloads a release's checksums.txt and returns its
// body. CloudInit calls it once per render, with a.Version, and hands the
// result to setupSHA256.
//
// It is an exported variable rather than an unexported function or a
// plain constant for two reasons that both come down to the same thing:
// this package's only network access has to be replaceable from outside
// itself. A test — this package's own, and pkg/statusbox/lightsail's,
// which calls CloudInit indirectly through NewLightsail and cannot reach
// an unexported identifier of a package it only imports — replaces it
// with a fixed body so CloudInit's Pulumi plumbing can be exercised
// without a released version to fetch from. And an estate whose Pulumi
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

// setupSHA256 finds setup.sh's checksum in a checksums.txt body. The
// format is goreleaser's default: one line per artifact, `<sha256>
// <filename>`.
func setupSHA256(checksums, version string) (string, error) {
	for _, line := range strings.Split(checksums, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		if fields[len(fields)-1] == "setup.sh" {
			sum := fields[0]
			if len(sum) != 64 {
				return "", fmt.Errorf("statusbox: checksums.txt for %s: %q is not a sha256 sum (want 64 hex characters, got %d)", version, sum, len(sum))
			}
			return sum, nil
		}
	}
	return "", fmt.Errorf("statusbox: checksums.txt for %s carries no entry for setup.sh — it was not attached to that release, or the release predates "+
		"it", version)
}

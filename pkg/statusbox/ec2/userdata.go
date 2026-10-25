package ec2

import (
	_ "embed"
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/truvity/asg"
	"github.com/truvity/asg/bootstrap"
	"github.com/truvity/asg/module"
	"github.com/truvity/asg/modules/tunnel"
	"github.com/truvity/asg/modules/watchdog"
	"github.com/truvity/edge/v4/cloudflare/instanceconnector"

	"github.com/truvity/observability/pkg/statusbox"
)

// setupScript is /usr/local/sbin/statusbox-setup.sh on the instance: see its
// own header for the phases. It travels as an S3 object, not in the user-data
// (user-data is capped at 16 KiB and the script alone is over half of it): the
// plan pins it by sha256 and its first boot step fetches and verifies it, so a
// change to it is still a new launch template version, "immutable by
// construction", and nothing at boot trusts a download except by digest.
//
//go:embed setup.sh
var setupScript string

const (
	// userDataLimit is EC2's ceiling on user-data, in bytes before base64.
	userDataLimit = 16 * 1024

	// setupPath is where the plan installs setupScript. The extension is part
	// of the object key (config/<sha256>.sh), the key it always had.
	setupPath = "/usr/local/sbin/statusbox-setup.sh"
	// paramsPath is the file of facts the setup script reads.
	paramsPath = "/etc/statusbox/params.sh"

	// tunnelUnit, tunnelUser and tunnelEnvFile name the cloudflared connector.
	// The token file is under the directory the setup script owns in /run.
	tunnelUser    = "statusbox"
	tunnelEnvFile = "/run/statusbox/tunnel.env"
	// tunnelMetricsPort is cloudflared's metrics listener, on the loopback:
	// its /ready answers 200 once the connector holds an edge connection.
	tunnelMetricsPort = 20241

	// regionPlaceholder stands in for the instance's region while the
	// connector is rendered: edge wants one, and the region is the provider's,
	// known only at deploy time. It is replaced by an IMDS lookup.
	regionPlaceholder = "xx-placeholder-1"
)

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
	b.WriteString(shellLine("SB_PING_PARAM", a.PingURLParameter))

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
	b.WriteString(shellArray("SB_CONFIGS", a.configEntries()))

	for _, arch := range architectures {
		b.WriteString(shellLine("SB_GATUS_URL_"+arch.key, statusbox.ReleaseURL(a.Version, gatusAsset(arch))))
		b.WriteString(shellLine("SB_GATUS_SHA_"+arch.key, gatusSHA[arch.key]))
		b.WriteString(shellLine("SB_LITESTREAM_URL_"+arch.key, arch.litestreamURL()))
		b.WriteString(shellLine("SB_LITESTREAM_SHA_"+arch.key, arch.litestreamSHA256))
	}

	return b.String(), nil
}

// boxModule is the part of the box that is the status box's own: the facts, the
// setup script, its install step, the in-service step that restores and starts
// Gatus, and the check that every Gatus answers.
type boxModule struct {
	a      Args
	params string
}

func (boxModule) Name() string { return "statusbox" }

func (m boxModule) Contribute() (module.Contribution, error) {
	check := setupPath + " check\n"

	return module.Contribution{
		Fragments: []bootstrap.Fragment{bootstrap.Compose("statusbox",
			bootstrap.File{Path: paramsPath, Mode: "0644", Content: m.params, Delivery: bootstrap.DeliveryInline},
			// An object: the plan fetches it, refuses a wrong digest and
			// abandons the launch.
			bootstrap.File{Path: setupPath, Mode: "0755", Content: setupScript, Delivery: bootstrap.DeliveryObject},
			// First boot, warm-pool instances too: swap, binaries, configs and
			// units. Nothing is started.
			bootstrap.Step{Name: "statusbox install", Phase: bootstrap.PhaseInstall, Order: 0, Script: setupPath + " install\n"},
		)},
		SSMParameters: m.a.parameterNames(),
		Statements:    m.a.instanceStatements(),
		// Only a boot into service restores the databases and starts Gatus,
		// which is what keeps the replica single-writer. Order 0: before the
		// tunnel (100), the self-registration and the watchdog.
		InService:   []module.Step{{Name: "statusbox", Order: 0, Script: setupPath + " in-service\n"}},
		ReadyChecks: []module.ReadyCheck{{Name: "gatus", Script: check, TimeoutSeconds: 120}},
		Watch:       []module.WatchCheck{{Name: "gatus", Script: check}},
	}, nil
}

// tunnelModule is the cloudflared connector as a module of
// github.com/truvity/asg, rendered by truvity/edge's instanceconnector and
// copied field by field (neither library imports the other). Nil without a
// tunnel.
func (a Args) tunnelModule() (module.Module, error) {
	if a.TunnelTokenParameter == "" {
		return nil, nil
	}

	frag, err := instanceconnector.Bootstrap(instanceconnector.Args{
		Arch:           a.architecture().key,
		TokenParameter: a.TunnelTokenParameter,
		Region:         regionPlaceholder,
		User:           tunnelUser,
		EnvFile:        tunnelEnvFile,
	})
	if err != nil {
		return nil, fmt.Errorf("statusbox/ec2: tunnel: %w", err)
	}

	// The region of the token's parameter is the instance's own.
	loadToken := strings.ReplaceAll(frag.LoadToken, "'"+regionPlaceholder+"'", imdsRegion)
	if strings.Contains(loadToken, regionPlaceholder) || loadToken == frag.LoadToken {
		return nil, errors.New("statusbox/ec2: tunnel: the connector's token loader no longer names its region the way this package expects")
	}

	return tunnel.New(tunnel.Args{
		Connector: bootstrap.UnitFragment{
			Install:       frag.Install,
			Unit:          frag.Unit,
			UnitPath:      frag.UnitPath,
			LoadToken:     loadToken,
			Start:         frag.Start,
			SSMParameters: frag.SSMParameters,
		},
		// After the Gatus step (order 0), which waited for /health.
		Order:       tunnel.DefaultOrder,
		MetricsPort: tunnelMetricsPort,
		Watchdog:    true,
	}), nil
}

// imdsRegion is the instance's region from IMDSv2, as a shell word.
const imdsRegion = `"$(tok="$(curl -fsS -m 5 -X PUT -H 'X-aws-ec2-metadata-token-ttl-seconds: 60' http://169.254.169.254/latest/api/token)" && ` +
	`curl -fsS -m 5 -H "X-aws-ec2-metadata-token: $tok" http://169.254.169.254/latest/meta-data/placement/region)"`

// composed is the user-data and what the modules ask of the role, the network
// and the objects.
type composed struct {
	Rendered     *bootstrap.Rendered
	Contribution asg.Contribution
}

// compose builds the plan from the box's own module and the library's:
// watchdog (always), tunnel, ssh and self-registration (when set), and renders
// it. Pure: no network, no Pulumi.
func (a Args) compose(name string, gatusSHA map[string]string) (composed, error) {
	box := namesFor(name)

	params, err := a.params(box, gatusSHA)
	if err != nil {
		return composed{}, err
	}

	mods := []asg.Module{
		boxModule{a: a, params: params},
		// Marks the instance Unhealthy when Gatus (and the tunnel) keep failing
		// for HealthFailMinutes: the checks are the other modules'.
		watchdog.New(watchdog.Args{FailMinutes: a.healthMinutes()}),
	}

	tun, err := a.tunnelModule()
	if err != nil {
		return composed{}, err
	}

	sshMod, err := a.sshModule()
	if err != nil {
		return composed{}, err
	}

	for _, m := range []asg.Module{tun, sshMod, a.selfRegisterModule()} {
		if m != nil {
			mods = append(mods, m)
		}
	}

	contribution, err := asg.Collect(mods...)
	if err != nil {
		return composed{}, fmt.Errorf("statusbox/ec2: %w", err)
	}

	p, err := bootstrap.New(bootstrap.Options{
		Lifecycle: asg.Names{Base: box.asg, Role: box.role, Hook: box.hook}.Resolve(name).Lifecycle(),
		Objects:   &bootstrap.ObjectStore{Bucket: a.Bucket, Prefix: a.BucketPrefix},
		Limit:     userDataLimit,
	})
	if err != nil {
		return composed{}, fmt.Errorf("statusbox/ec2: %w", err)
	}

	if err := contribution.AddTo(p); err != nil {
		return composed{}, fmt.Errorf("statusbox/ec2: %w", err)
	}

	rendered, err := p.Render()
	if err != nil {
		return composed{}, fmt.Errorf("statusbox/ec2: rendered user-data: %w (it carries parameter names, pins and small scripts only, "+
			"so something unusually large was passed in: SSH, SelfRegister or the parameter maps)", err)
	}

	return composed{Rendered: rendered, Contribution: contribution}, nil
}

// gatusChecksums reads this release's checksums.txt through
// statusbox.FetchChecksums once, for the Gatus binaries' checksums.
func (a Args) gatusChecksums() (map[string]string, error) {
	checksums, err := statusbox.FetchChecksums(a.Version)
	if err != nil {
		return nil, err
	}

	gatusSHA := make(map[string]string, len(architectures))

	for _, arch := range architectures {
		sum, err := statusbox.ChecksumFor(checksums, gatusAsset(arch), a.Version)
		if err != nil {
			return nil, err
		}

		gatusSHA[arch.key] = sum
	}

	return gatusSHA, nil
}

// UserData renders what the launch template carries: the composed cloud-config
// of the box and the library's modules, gzipped when it is large, refused if
// over EC2's limit. Nothing secret can be in it: Args holds no secret value,
// only SSM parameter names.
func (a Args) UserData(name string) (*bootstrap.Rendered, error) {
	if err := a.validate(); err != nil {
		return nil, err
	}

	gatusSHA, err := a.gatusChecksums()
	if err != nil {
		return nil, err
	}

	c, err := a.compose(name, gatusSHA)
	if err != nil {
		return nil, err
	}

	return c.Rendered, nil
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}

	sort.Strings(keys)

	return keys
}

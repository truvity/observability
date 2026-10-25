package ec2

import (
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"

	"github.com/truvity/asg"

	"github.com/truvity/observability/pkg/statusbox"
)

var update = flag.Bool("update", false, "update the golden files this package's render tests compare against")

const (
	fakeSHA64 = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	configOps = "storage:\n  type: sqlite\n  path: /data/ops.db\nendpoints:\n  - name: site\n    url: https://status.example.test/health\n    interval: 1m\n" +
		"    conditions:\n      - \"[STATUS] == 200\"\n"
)

func validArgs() Args {
	return Args{
		Version: "v1.0.0",
		Instances: []statusbox.Instance{
			{Name: "ops", Port: 8082, Public: true, Config: configOps},
			{Name: "ops-breakglass", Port: 8081, Public: false, Config: configOps},
		},
		Hostnames:            map[string]string{"ops": "status.example.test"},
		TunnelTokenParameter: "/acme/status/tunnel-token",
		AlertURLParameters:   map[string]string{"ops_alerts_read_token": "/acme/status/alerts-read-token"},
		EnvParameters:        map[string]string{"OIDC_CLIENT_SECRET": "/acme/status/oidc-client-secret"},
		VPCID:                pulumi.String("vpc-0example"),
		SubnetIDs:            []pulumi.StringInput{pulumi.String("subnet-0a"), pulumi.String("subnet-0b")},
		PrivateIngressCIDRs:  []string{"10.20.0.0/16"},
		Bucket:               "acme-status-replica",
		BucketPrefix:         "box",
	}
}

func fakeChecksums() map[string]string {
	return map[string]string{"arm64": fakeSHA64, "amd64": strings.Repeat("ab", 32)}
}

// compose renders the box the way NewEC2 does, with the Gatus checksums faked.
func compose(t *testing.T, a Args) composed {
	t.Helper()

	c, err := a.compose("status", fakeChecksums())
	require.NoError(t, err)

	return c
}

// cloudConfig is the rendered cloud-config text of the box.
func cloudConfig(t *testing.T, a Args) string { return compose(t, a).Rendered.CloudConfig }

// writtenFiles are the files the cloud-config writes, by path.
func writtenFiles(t *testing.T, a Args) map[string]string {
	t.Helper()

	var cfg struct {
		WriteFiles []struct {
			Path    string `yaml:"path"`
			Content string `yaml:"content"`
		} `yaml:"write_files"`
	}

	require.NoError(t, yaml.Unmarshal([]byte(cloudConfig(t, a)), &cfg))

	out := map[string]string{}
	for _, f := range cfg.WriteFiles {
		out[f.Path] = f.Content
	}

	return out
}

// fakeRelease makes the release's checksums.txt available for the test.
func fakeRelease(t *testing.T) {
	t.Helper()

	previous := statusbox.FetchChecksums
	statusbox.FetchChecksums = func(string) (string, error) {
		return fakeSHA64 + "  gatus_" + GatusVersion + "_linux_arm64\n" + fakeSHA64 + "  gatus_" + GatusVersion + "_linux_amd64\n", nil
	}

	t.Cleanup(func() { statusbox.FetchChecksums = previous })
}

func TestValidateAcceptsAValidArgs(t *testing.T) {
	require.NoError(t, validArgs().validate())
}

func TestValidateDefaults(t *testing.T) {
	a := validArgs()
	require.Equal(t, "t4g.nano", a.instanceType())
	require.Equal(t, 8, a.rootVolumeGiB())
	require.Equal(t, 5, a.healthMinutes())
	require.Equal(t, archARM64, a.architecture())
}

func TestArchitectureFollowsTheInstanceType(t *testing.T) {
	for it, want := range map[string]architecture{
		"t4g.nano": archARM64, "c7gn.large": archARM64, "g5g.xlarge": archARM64, "m7g.medium": archARM64,
		"t3.micro": archAMD64, "m6a.large": archAMD64, "g5.xlarge": archAMD64, "c6i.large": archAMD64,
	} {
		a := validArgs()
		a.InstanceType = it
		require.Equal(t, want, a.architecture(), it)
	}
}

func TestValidateRefusals(t *testing.T) {
	cases := map[string]struct {
		mutate func(*Args)
		want   string
	}{
		"empty version":          {func(a *Args) { a.Version = "" }, "Version"},
		"version with a space":   {func(a *Args) { a.Version = "v1 0" }, "Version"},
		"no instances":           {func(a *Args) { a.Instances = nil; a.Hostnames = nil; a.TunnelTokenParameter = "" }, "no instances"},
		"duplicate port":         {func(a *Args) { a.Instances[1].Port = 8082 }, "cannot share a port"},
		"public without host":    {func(a *Args) { a.Hostnames = nil }, "Hostnames"},
		"public without tunnel":  {func(a *Args) { a.TunnelTokenParameter = "" }, "TunnelTokenParameter is empty"},
		"tunnel without public":  {func(a *Args) { a.Instances[0].Public = false; a.Hostnames = nil }, "no instance is Public"},
		"ping param not a path":  {func(a *Args) { a.PingURLParameter = "ping" }, "PingURLParameter"},
		"tunnel not a path":      {func(a *Args) { a.TunnelTokenParameter = "tunnel" }, "SSM parameter name"},
		"alert param not a path": {func(a *Args) { a.AlertURLParameters["ops_alerts_read_token"] = "x y" }, "SSM parameter name"},
		"env name reserved":      {func(a *Args) { a.EnvParameters["TUNNEL_TOKEN"] = "/acme/x" }, "reserved"},
		"env collides alert":     {func(a *Args) { a.EnvParameters["ALERT_URL_OPS_ALERTS_READ_TOKEN"] = "/acme/x" }, "collides"},
		"alert key invalid":      {func(a *Args) { a.AlertURLParameters["bad key"] = "/acme/x" }, "alert key"},
		"one subnet":             {func(a *Args) { a.SubnetIDs = a.SubnetIDs[:1] }, "subnet"},
		"nil vpc":                {func(a *Args) { a.VPCID = nil }, "VPCID"},
		"world-open private":     {func(a *Args) { a.PrivateIngressCIDRs = []string{"0.0.0.0/0"} }, "whole internet"},
		"bad cidr":               {func(a *Args) { a.PrivateIngressCIDRs = []string{"10.0.0.0/33"} }, "IPv4 CIDR"},
		"no bucket":              {func(a *Args) { a.Bucket = "" }, "Bucket"},
		"bucket upper case":      {func(a *Args) { a.Bucket = "Acme" }, "Bucket"},
		"prefix slash":           {func(a *Args) { a.BucketPrefix = "/box" }, "BucketPrefix"},
		"prefix dotdot":          {func(a *Args) { a.BucketPrefix = "a/../b" }, "BucketPrefix"},
		"kms alias":              {func(a *Args) { a.KMSKeyARN = "alias/acme" }, "KMSKeyARN"},
		"bad instance type":      {func(a *Args) { a.InstanceType = "nano" }, "InstanceType"},
		"small root":             {func(a *Args) { a.RootVolumeGiB = 4 }, "RootVolumeGiB"},
		"negative health":        {func(a *Args) { a.HealthFailMinutes = -1 }, "HealthFailMinutes"},
		"not sqlite": {func(a *Args) {
			a.Instances[0].Config = "storage:\n  type: memory\nendpoints: []\n"
		}, "storage.type: sqlite"},
		"db outside data": {func(a *Args) {
			a.Instances[0].Config = "storage:\n  type: sqlite\n  path: /tmp/ops.db\n"
		}, "directly under /data/"},
		"db in a subdirectory": {func(a *Args) {
			a.Instances[0].Config = "storage:\n  type: sqlite\n  path: /data/sub/ops.db\n"
		}, "directly under /data/"},
		"config not yaml": {func(a *Args) { a.Instances[0].Config = "storage: [" }, "not YAML"},
		"bad CA bundle":   {func(a *Args) { a.TrustedCAs = "not a pem" }, "TrustedCAs"},
	}

	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			a := validArgs()
			a.AlertURLParameters = map[string]string{"ops_alerts_read_token": "/acme/status/alerts-read-token"}
			a.EnvParameters = map[string]string{"OIDC_CLIENT_SECRET": "/acme/status/oidc-client-secret"}
			c.mutate(&a)
			err := a.validate()
			require.Error(t, err)
			require.Contains(t, err.Error(), c.want)
		})
	}
}

func TestValidateReportsEveryProblem(t *testing.T) {
	a := validArgs()
	a.Bucket = ""
	a.Version = ""
	a.SubnetIDs = nil
	err := a.validate()
	require.ErrorContains(t, err, "Bucket")
	require.ErrorContains(t, err, "Version")
	require.ErrorContains(t, err, "subnet")
}

func TestPrivateOnlyBoxNeedsNoTunnel(t *testing.T) {
	a := validArgs()
	a.Instances = a.Instances[1:]
	a.Hostnames = nil
	a.TunnelTokenParameter = ""
	require.NoError(t, a.validate())
}

func TestDatabaseFile(t *testing.T) {
	f, err := databaseFile(statusbox.Instance{Name: "ops", Config: configOps})
	require.NoError(t, err)
	require.Equal(t, "ops.db", f)
}

func TestParameterNamesAreUniqueAndSorted(t *testing.T) {
	a := validArgs()
	a.EnvParameters["EXTRA"] = "/acme/status/alerts-read-token"
	// The tunnel token and the SSH host key are the modules' parameters: they
	// reach the role through the modules' contribution.
	require.Equal(t, []string{"/acme/status/alerts-read-token", "/acme/status/oidc-client-secret"}, a.parameterNames())

	c := compose(t, a)
	require.Equal(t, []string{
		"/acme/status/alerts-read-token", "/acme/status/oidc-client-secret", "/acme/status/tunnel-token",
	}, c.Rendered.SSMParameters)
}

// TestUserDataGolden pins the whole rendered cloud-config: the parameters, the
// staged Configs' names, the watchdog, the tunnel, the library's lifecycle files
// and the bootstrap steps. The setup script is an object, so it is not in it.
// A change to any of them is a reviewable diff, not a surprise.
func TestUserDataGolden(t *testing.T) {
	for name, a := range map[string]Args{"userdata.golden.yaml": validArgs(), "userdata-full.golden.yaml": fullArgs()} {
		// The repository is public: the account id and the ARN prefix of the
		// fixtures are replaced by placeholders, as in the resource-set goldens.
		got := strings.ReplaceAll(cloudConfig(t, a), strings.Repeat("1", 12), "@ACCOUNT@")
		got = strings.ReplaceAll(got, "arn:"+partition, "arn:@PARTITION@")
		golden := filepath.Join("testdata", name)

		if *update {
			require.NoError(t, os.MkdirAll("testdata", 0o755))
			require.NoError(t, os.WriteFile(golden, []byte(got), 0o644))

			continue
		}

		want, err := os.ReadFile(golden)
		require.NoError(t, err, "run 'go test ./pkg/statusbox/ec2 -run TestUserDataGolden -update' and review the diff")
		require.Equal(t, string(want), got, "the rendered user-data moved: run 'go test ./pkg/statusbox/ec2 -run TestUserDataGolden -update' and review the diff")
	}
}

func TestUserDataWithCAsAndPublicOnly(t *testing.T) {
	a := validArgs()
	a.Instances = a.Instances[:1]
	a.EnvParameters = nil
	got := cloudConfig(t, a)
	require.Contains(t, got, "SB_INSTANCES=('ops:8082:true:ops.db')")
	require.NotContains(t, got, "ops-breakglass")
}

// TestUserDataCarriesNoSecret is the property the design rests on. Args has no
// secret-valued field at all (nothing of type pulumi.StringInput or an Output),
// the secrets are parameter names, and the script only ever reads a value into
// a file under /run.
func TestUserDataCarriesNoSecret(t *testing.T) {
	var walk func(rt reflect.Type, path string)
	walk = func(rt reflect.Type, path string) {
		switch rt.Kind() {
		case reflect.Map, reflect.Slice, reflect.Pointer:
			walk(rt.Elem(), path+"[]")
		case reflect.Struct:
			if rt.PkgPath() == "github.com/pulumi/pulumi-aws/sdk/v7/go/aws" {
				return // the provider handle, not a value
			}

			for i := 0; i < rt.NumField(); i++ {
				walk(rt.Field(i).Type, path+"."+rt.Field(i).Name)
			}
		case reflect.Interface:
			// VPCID and SubnetIDs are network ids, not secrets; any other
			// interface-typed field must be listed here on purpose.
			require.Contains(t, []string{".VPCID", ".SubnetIDs[]"}, path, "Args.%s is an Input: a secret could be handed in through it", path)
		default:
		}
	}
	walk(reflect.TypeOf(Args{}), "")

	script := cloudConfig(t, validArgs())

	// The setup script (an S3 object, pinned by sha256 in the user-data) reads
	// a value and writes it under /run, nowhere else, and never prints it.
	require.Contains(t, setupScript, "--with-decryption")
	require.NotContains(t, setupScript, "echo \"$value\"")
	require.NotContains(t, setupScript, "printf '%s\\n' \"$value\"")
	require.Equal(t, 0, strings.Count(setupScript, "TS_AUTHKEY"), "no tailnet key on this backend")
	require.NotContains(t, script, "tailscale up")

	// Parameter names are there, as names.
	for _, p := range compose(t, validArgs()).Rendered.SSMParameters {
		require.Contains(t, script, p)
	}
}

func TestUserDataIsComposedAndWithinTheLimit(t *testing.T) {
	fakeRelease(t)

	ud, err := validArgs().UserData("status")
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(ud.CloudConfig, "#cloud-config\n"), "cloud-init reads user-data that starts with #cloud-config")
	require.Less(t, ud.Size, userDataLimit)
	require.Equal(t, ud.Size, len(ud.UserData))

	// The setup script is the one object the plan fetches; the Gatus
	// configurations are the box's own.
	require.Len(t, ud.Objects, 1)
	require.Equal(t, "box/config/"+setupObject().SHA256()+".sh", ud.Objects[0].Key)
	require.Equal(t, setupScript, string(ud.Objects[0].Content))
}

func TestUserDataRefusesAReleaseWithoutTheGatusAsset(t *testing.T) {
	previous := statusbox.FetchChecksums
	statusbox.FetchChecksums = func(string) (string, error) { return fakeSHA64 + "  setup.sh\n", nil }

	t.Cleanup(func() { statusbox.FetchChecksums = previous })

	_, err := validArgs().UserData("status")
	require.ErrorContains(t, err, "gatus_"+GatusVersion+"_linux_arm64")
}

func TestUserDataRefusesOverTheLimit(t *testing.T) {
	fakeRelease(t)

	a := validArgs()
	// Incompressible filler: random-looking, so gzip cannot shrink it. It
	// comes in through the parameter maps, the only free-form text left in the
	// user-data (a Config travels as an S3 object).
	x := uint32(1)
	for i := 0; i < 400; i++ {
		var b strings.Builder

		for b.Len() < 60 {
			x = x*1664525 + 1013904223
			b.WriteString(string(rune('a' + (x>>24)%26)))
		}

		a.EnvParameters["FILLER_"+b.String()[:8]] = "/acme/status/" + b.String()
	}

	_, err := a.UserData("status")
	require.ErrorContains(t, err, "-byte limit")
}

func TestPinsAreWellFormed(t *testing.T) {
	sha := regexp.MustCompile(`^[0-9a-f]{64}$`)

	for _, a := range architectures {
		require.Regexp(t, sha, a.litestreamSHA256)
		require.Contains(t, a.litestreamURL(), "v"+LitestreamVersion+"/litestream-"+LitestreamVersion+"-linux-")
		require.Equal(t, "gatus_"+GatusVersion+"_linux_"+a.key, gatusAsset(a))
		require.True(t, strings.HasPrefix(a.ssmAMI, "/aws/service/ami-amazon-linux-latest/al2023-"))
	}
}

func TestInstanceStatementsAreScoped(t *testing.T) {
	a := validArgs()
	a.KMSKeyARN = "arn:" + partition + ":kms:eu-west-1:" + strings.Repeat("7", 12) + ":key/1234abcd-12ab-34cd-56ef-1234567890ab"
	doc, err := asg.Document(a.instanceStatements())
	require.NoError(t, err)

	require.Contains(t, doc, "arn:"+partition+":s3:::acme-status-replica/box/*")
	require.Contains(t, doc, a.KMSKeyARN)
	require.NotContains(t, doc, `"*"`, "no wildcard resource")

	plain, err := asg.Document(validArgs().instanceStatements())
	require.NoError(t, err)
	require.NotContains(t, plain, "kms:")
	// The parameters, the objects and the group's two actions are the
	// library's, generated from what the plan reads.
	require.NotContains(t, plain, "ssm:GetParameter")
	require.NotContains(t, plain, "autoscaling")
}

// TestSetupScriptLeavesTheLifecycleToTheLibrary: the hook, the warm-pool
// decision, the health timer and the connector are asg's, not this script's.
func TestSetupScriptLeavesTheLifecycleToTheLibrary(t *testing.T) {
	for _, gone := range []string{
		"cloudflared", "CLOUDFLARED", "SB_TUNNEL_PARAM", "set-instance-health", "SB_HEALTH_MINUTES", "target-lifecycle-state",
		"boot_phase", "health_phase", "statusbox-boot", "statusbox-health", "lifecycle-action-result CONTINUE", "SB_DNS_",
	} {
		require.NotContains(t, strings.ReplaceAll(setupScript, "the tunnel connector", ""), gone, "%s moved to github.com/truvity/asg", gone)
	}

	// The only hook call left abandons a launch whose configurations could not
	// be verified.
	require.Equal(t, 1, strings.Count(setupScript, "complete_hook ABANDON"))
	require.Contains(t, setupScript, "in_service_phase()")
	require.Contains(t, setupScript, "check_phase()")
}

// TestLowPortGetsTheBindCapability: a Gatus below port 1024 (the private page
// on plain port 80) gets CAP_NET_BIND_SERVICE as an ambient capability through
// a per-instance drop-in, and only then; the health and ping probes already
// follow INST_PORT.
func TestLowPortGetsTheBindCapability(t *testing.T) {
	require.Contains(t, setupScript, `if [ "$INST_PORT" -lt 1024 ]; then`)
	require.Contains(t, setupScript, "gatus@$INST_NAME.service.d/10-low-port.conf")
	require.Contains(t, setupScript, "AmbientCapabilities=CAP_NET_BIND_SERVICE")
	require.Contains(t, setupScript, "CapabilityBoundingSet=CAP_NET_BIND_SERVICE")
	require.Equal(t, 3, strings.Count(setupScript, `http://127.0.0.1:$INST_PORT/health`), "every probe uses the instance's own port")
}

func pingArgs() Args {
	a := validArgs()
	a.PingURLParameter = "/acme/status/ping-url"

	return a
}

// TestPingIsOptional: without PingURLParameter the params carry an empty name
// and the role reads no extra parameter; with it, the role may read exactly it.
func TestPingIsOptional(t *testing.T) {
	off, err := validArgs().params(namesFor("status"), fakeChecksums())
	require.NoError(t, err)
	require.Contains(t, off, "SB_PING_PARAM=''\n")
	require.NotContains(t, validArgs().parameterNames(), "/acme/status/ping-url")

	on, err := pingArgs().params(namesFor("status"), fakeChecksums())
	require.NoError(t, err)
	require.Contains(t, on, "SB_PING_PARAM='/acme/status/ping-url'\n")
	require.Contains(t, pingArgs().parameterNames(), "/acme/status/ping-url")
	require.NoError(t, pingArgs().validate())
}

// TestPingUnitsCarryNoURL: the units are written by the setup script only when
// a parameter is configured, and the URL never appears in the user-data.
func TestPingUnitsCarryNoURL(t *testing.T) {
	require.Contains(t, setupScript, "statusbox-ping.service")
	require.Contains(t, setupScript, "statusbox-ping.timer")
	require.Contains(t, setupScript, "OnUnitActiveSec=60s")
	require.Contains(t, setupScript, `[ -n "$SB_PING_PARAM" ] || return 0`)

	got := cloudConfig(t, pingArgs())
	require.Contains(t, setupScript, "ExecStart=/usr/local/sbin/statusbox-setup.sh ping")
	require.Contains(t, got, "SB_PING_PARAM='/acme/status/ping-url'")
}

// TestPingPhase runs the embedded ping function against a stub curl: healthy
// Gatus pings the URL, an unhealthy one pings <url>/fail, and the URL reaches
// curl on stdin, never on its command line.
func TestPingPhase(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not available")
	}

	for name, tc := range map[string]struct {
		healthy bool
		want    string
	}{
		"healthy":   {true, "https://ping.example.test/uuid"},
		"unhealthy": {false, "https://ping.example.test/uuid/fail"},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			root := filepath.Join(dir, "root")
			bin := filepath.Join(dir, "bin")
			require.NoError(t, os.MkdirAll(filepath.Join(root, "etc/statusbox"), 0o755))
			require.NoError(t, os.MkdirAll(filepath.Join(root, "run/statusbox"), 0o700))
			require.NoError(t, os.MkdirAll(bin, 0o755))

			require.NoError(t, os.WriteFile(filepath.Join(root, "etc/statusbox/params.sh"),
				[]byte("SB_PING_PARAM=/acme/status/ping-url\nSB_INSTANCES=('ops:8082:true:ops.db')\n"), 0o644))
			require.NoError(t, os.WriteFile(filepath.Join(root, "run/statusbox/ping.url"), []byte("https://ping.example.test/uuid/\n"), 0o600))

			stub := "#!/bin/bash\n" +
				"for a in \"$@\"; do case \"$a\" in http://127.0.0.1*) [ \"$HEALTHY\" = 1 ] && exit 0 || exit 22 ;; esac; done\n" +
				"printf '%s\\n' \"$*\" >>\"$OUT/args\"\n" +
				"cat >\"$OUT/stdin\"\n"
			require.NoError(t, os.WriteFile(filepath.Join(bin, "curl"), []byte(stub), 0o755))

			healthy := "0"
			if tc.healthy {
				healthy = "1"
			}

			cmd := exec.Command(bash, "-c", `. "$SCRIPT"; ping_phase`)
			cmd.Env = append(os.Environ(),
				"STATUSBOX_SOURCE_ONLY=1", "STATUSBOX_ROOT="+root, "SCRIPT="+scriptPath(t, dir),
				"PATH="+bin+":"+os.Getenv("PATH"), "HEALTHY="+healthy, "OUT="+dir)
			out, err := cmd.CombinedOutput()
			require.NoError(t, err, string(out))

			stdin, err := os.ReadFile(filepath.Join(dir, "stdin"))
			require.NoError(t, err)
			require.Equal(t, `url = "`+tc.want+`"`+"\n", string(stdin))

			args, err := os.ReadFile(filepath.Join(dir, "args"))
			require.NoError(t, err)
			require.NotContains(t, string(args), "ping.example.test", "the URL must not be on the command line")
			require.NotContains(t, string(out), "ping.example.test", "the URL must not be logged")
		})
	}
}

func scriptPath(t *testing.T, dir string) string {
	t.Helper()

	p := filepath.Join(dir, "setup.sh")
	require.NoError(t, os.WriteFile(p, []byte(setupScript), 0o755))

	return p
}

// TestConfigsTravelAsObjects: no Config (and no setup script) is in the
// user-data; it names each object's key and sha256, and the key is the sha.
func TestConfigsTravelAsObjects(t *testing.T) {
	a := validArgs()
	a.TrustedCAs = "-----BEGIN CERTIFICATE-----\nMIIB\n-----END CERTIFICATE-----\n"
	c := compose(t, a)
	got := c.Rendered.CloudConfig
	require.NotContains(t, got, "endpoints:")
	require.NotContains(t, got, "restore_instance", "the setup script is an object too")

	for _, o := range a.bucketObjects() {
		require.Len(t, o.SHA256(), 64)
		require.Equal(t, "box/config/"+o.SHA256()+"."+o.Ext, o.Key("box"))
	}

	// The setup script is pinned by the digest in its object fetch, under the
	// same key it always had.
	setup := setupObject()
	require.Equal(t, "box/config/"+setup.SHA256()+".sh", setup.Key("box"))
	require.Contains(t, got, "bs_fetch 'box/config/"+setup.SHA256()+".sh' '"+setup.SHA256()+"' '"+setupPath+"' 0755")
	require.Contains(t, got, "ops:"+configObject{Content: configOps}.SHA256()+":box/config/"+configObject{Content: configOps}.SHA256()+".yaml")
	require.Contains(t, got, "trusted-cas:", "the CA bundle is an object too")
	require.Equal(t, "config/"+configObject{Content: "x", Ext: "yaml"}.SHA256()+".yaml", configObject{Content: "x", Ext: "yaml"}.Key(""))
}

// A changed Config is a new digest in the user-data, so the launch template
// moves and the instance rolls, as it did when the Config was inline.
func TestAChangedConfigChangesTheUserData(t *testing.T) {
	a := validArgs()
	before := cloudConfig(t, a)

	a.Instances = append([]statusbox.Instance(nil), a.Instances...)
	a.Instances[0].Config += "# changed\n"
	require.NotEqual(t, before, cloudConfig(t, a))
}

func TestConfigIsAReservedInstanceName(t *testing.T) {
	a := validArgs()
	a.Instances = append([]statusbox.Instance(nil), a.Instances...)
	a.Instances[1].Name = "config"
	a.Hostnames = map[string]string{"ops": "status.example.test"}
	require.ErrorContains(t, a.validate(), `instance name "config" is reserved`)
}

// TestFetchConfigs runs the embedded fetch_configs against a stub aws that
// serves a directory standing in for the bucket: a good object is staged; a
// digest mismatch or a failing fetch fails the function and stages nothing.
func TestFetchConfigs(t *testing.T) {
	bash, err := exec.LookPath("bash")
	if err != nil {
		t.Skip("bash is not available")
	}

	good := "storage:\n  type: sqlite\n"
	want := configObject{Content: good}.SHA256()

	for name, tc := range map[string]struct {
		serve, sha string
		ok         bool
	}{
		"verified":      {good, want, true},
		"wrong digest":  {"evil: true\n", want, false},
		"fetch failure": {"", want, false},
	} {
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			root := filepath.Join(dir, "root")
			bin := filepath.Join(dir, "bin")
			bucket := filepath.Join(dir, "bucket")
			require.NoError(t, os.MkdirAll(filepath.Join(root, "etc/statusbox"), 0o755))
			require.NoError(t, os.MkdirAll(filepath.Join(bucket, "box/config"), 0o755))
			require.NoError(t, os.MkdirAll(bin, 0o755))

			if tc.serve != "" {
				require.NoError(t, os.WriteFile(filepath.Join(bucket, "box/config/"+want+".yaml"), []byte(tc.serve), 0o644))
			}

			// stub aws: `aws s3 cp [flags] s3://<bucket>/<key> <dest>`
			stub := "#!/bin/bash\nsrc=\"\"; for a in \"$@\"; do case \"$a\" in s3://*) src=\"${a#s3://*/}\" ;; *) dest=\"$a\" ;; esac; done\n" +
				"printf '%s\\n' \"$*\" >>\"$OUT/aws.log\"\ncp \"$BUCKET/$src\" \"$dest\"\n"
			require.NoError(t, os.WriteFile(filepath.Join(bin, "aws"), []byte(stub), 0o755))

			params := "SB_BUCKET=acme\nSB_CONFIGS=('ops:" + tc.sha + ":box/config/" + want + ".yaml')\n"
			require.NoError(t, os.WriteFile(filepath.Join(root, "etc/statusbox/params.sh"), []byte(params), 0o644))

			cmd := exec.Command(bash, "-c", `. "$SCRIPT"; load_params; fetch_configs`)
			cmd.Env = append(os.Environ(),
				"STATUSBOX_SOURCE_ONLY=1", "STATUSBOX_ROOT="+root, "SCRIPT="+scriptPath(t, dir), "STATUSBOX_RETRY_SLEEP=0",
				"PATH="+bin+":"+os.Getenv("PATH"), "BUCKET="+bucket, "OUT="+dir)
			out, err := cmd.CombinedOutput()
			staged := filepath.Join(root, "opt/statusbox/staged")

			if tc.ok {
				require.NoError(t, err, string(out))
				b, rerr := os.ReadFile(filepath.Join(staged, "ops"))
				require.NoError(t, rerr)
				require.Equal(t, good, string(b))

				return
			}

			require.Error(t, err, string(out))
			require.NoFileExists(t, filepath.Join(staged, "ops"))
			require.NoFileExists(t, filepath.Join(staged, "ops.part"))
		})
	}
}

// Public narrow pages are more Public instances: each listens on loopback at
// its own port behind the one tunnel, and none opens the security group.
func TestSeveralPublicPagesShareTheTunnel(t *testing.T) {
	a := validArgs()
	for _, p := range []struct {
		name string
		port int
	}{{"acme-prod", 8083}, {"acme-stage", 8084}} {
		a.Instances = append(a.Instances, statusbox.Instance{
			Name: p.name, Port: p.port, Public: true,
			Config: strings.Replace(configOps, "/data/ops.db", "/data/"+p.name+".db", 1),
		})
		a.Hostnames[p.name] = "status-" + p.name + ".example.test"
	}

	require.NoError(t, a.validate())
	require.Equal(t, []int{8081}, a.privatePorts(), "only the break-glass page is reachable from the peered network")

	got := cloudConfig(t, a)
	require.Contains(t, got, "SB_INSTANCES=('ops:8082:true:ops.db' 'ops-breakglass:8081:false:ops.db' "+
		"'acme-prod:8083:true:acme-prod.db' 'acme-stage:8084:true:acme-stage.db')")

	a.Instances[3].Port = 8083
	require.ErrorContains(t, a.validate(), "Port 8083 is also used by instance \"acme-prod\"")
}

// The Gatus build is pinned to a fork commit, never a moving ref: the script
// refuses a ref that is not a full SHA, so the pin must be one.
func TestGatusPinIsAFullCommitSHA(t *testing.T) {
	require.Regexp(t, `^[0-9a-f]{40}$`, GatusRef)
	require.Equal(t, "https://github.com/truvity/gatus", GatusRepo)
	require.Contains(t, GatusVersion, "-pkce", "the fork's version says so")
}

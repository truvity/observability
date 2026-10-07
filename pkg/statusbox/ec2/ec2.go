// Package ec2 is the second provider pkg/statusbox is provisioned on: one
// Amazon Linux 2023 instance in an Auto Scaling group of exactly one, with a
// warm pool of one stopped instance, running Gatus and cloudflared as systemd
// units and keeping Gatus's SQLite databases in S3 through Litestream.
//
// What it shares with pkg/statusbox/lightsail is the idea, the instances and
// their Gatus Configs; what it does not share is the machinery. There is no
// Docker, no Tailscale and no attached disk, and no secret in user-data: the
// instance reads SSM Parameter Store at boot through its role, and the Args
// carry parameter NAMES only. See docs/statusbox.md ("EC2 backend").
package ec2

import (
	"errors"
	"fmt"
	"net/netip"
	"path"
	"regexp"
	"sort"
	"strings"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"go.yaml.in/yaml/v3"

	"github.com/truvity/observability/pkg/statusbox"
)

// partition is the ARN partition of the policies the module renders.
const partition = "aws"

const (
	// DefaultInstanceType is what an estate gets for free: a Graviton nano,
	// enough for a handful of Gatus processes and the swap file behind them.
	DefaultInstanceType = "t4g.nano"
	// DefaultRootVolumeGiB is the root volume: the Amazon Linux 2023 minimum.
	DefaultRootVolumeGiB = 8
	// DefaultHealthFailMinutes is how long Gatus or cloudflared may be down
	// before the instance marks itself unhealthy.
	DefaultHealthFailMinutes = 5

	// dataDir is where the setup script bind-mounts each instance's database
	// directory inside its unit; a Config's storage.path lives directly in it,
	// the same path a container would have had.
	dataDir = "/data/"
)

var (
	versionRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	// nameRE is the component name: it becomes the Auto Scaling group name.
	nameRE         = regexp.MustCompile(`^[a-z][a-z0-9-]{0,40}$`)
	bucketRE       = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]$`)
	prefixRE       = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)
	parameterRE    = regexp.MustCompile(`^/[A-Za-z0-9_.-]+(/[A-Za-z0-9_.-]+)*$`)
	instanceTypeRE = regexp.MustCompile(`^[a-z][a-z0-9-]*\.[a-z0-9]+$`)
	// gravitonRE recognises the Graviton families (t4g, c7gn, g5g, ...): a
	// letter prefix, the generation digits, then a 'g' among the attributes.
	gravitonRE = regexp.MustCompile(`^[a-z]+[0-9]+g[a-z]*\.`)
	dbFileRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)
	kmsARNRE   = regexp.MustCompile(`^arn:[a-z-]+:kms:[a-z0-9-]+:[0-9]{12}:key/[A-Za-z0-9-]+$`)
)

// Args is NewEC2's whole input. Unlike statusbox.Args it holds no secret
// value, by construction: every secret is an SSM parameter NAME, read by the
// instance at boot.
type Args struct {
	// Version names a release of this repository: the Gatus binaries and their
	// checksums.txt are fetched from its assets.
	Version string

	// Instances, Hostnames and TrustedCAs are statusbox.Args's, with the same
	// checks. Each Config must use SQLite storage whose path is directly under
	// /data (for instance /data/ops.db): that file is what Litestream
	// replicates, and the setup script mounts a per-instance directory there.
	Instances  []statusbox.Instance
	Hostnames  map[string]string
	TrustedCAs string

	// TunnelTokenParameter is the SSM parameter holding the cloudflared tunnel
	// token. Required when some Instance is Public, refused otherwise.
	TunnelTokenParameter string
	// AlertURLParameters maps a Config's ALERT_URL_<KEY> to the SSM parameter
	// holding it, with statusbox.Secrets.AlertURLs's key rules. This is where
	// the alerts-read token and a deadman's chat token go.
	AlertURLParameters map[string]string
	// EnvParameters maps a whole environment variable name a Config references
	// (OIDC_CLIENT_SECRET) to its SSM parameter.
	EnvParameters map[string]string

	// VPCID and SubnetIDs are the caller's network: a VPC with public subnets
	// only, two or more, in different Availability Zones. The instance gets a
	// dynamic public IPv4 address; there is no Elastic IP and no NAT.
	VPCID     pulumi.StringInput
	SubnetIDs []pulumi.StringInput
	// PrivateIngressCIDRs are the networks (a peered VPC) allowed to reach the
	// private instance's port. Empty: nothing may.
	PrivateIngressCIDRs []string

	// Bucket and BucketPrefix are where Litestream keeps the replicas, as
	// <Bucket>/<BucketPrefix>/<instance name>. The bucket is the caller's.
	Bucket       string
	BucketPrefix string
	// KMSKeyARN, when set, is the customer-managed key that encrypts the SSM
	// SecureString parameters and/or the bucket: the role may decrypt with it
	// and generate data keys. Empty: the AWS-managed defaults need no grant.
	KMSKeyARN string

	// InstanceType defaults to DefaultInstanceType. The architecture follows
	// from it: a Graviton family is arm64, anything else amd64.
	InstanceType string
	// RootVolumeGiB defaults to DefaultRootVolumeGiB.
	RootVolumeGiB int
	// HealthFailMinutes defaults to DefaultHealthFailMinutes.
	HealthFailMinutes int

	// Provider is the AWS provider the account's lookups (region, account,
	// image) go through; the resources themselves take the options NewEC2 is
	// called with.
	Provider *aws.Provider

	// Tags are added to every resource that takes tags.
	Tags map[string]string
}

func (a Args) instanceType() string {
	if a.InstanceType == "" {
		return DefaultInstanceType
	}

	return a.InstanceType
}

func (a Args) rootVolumeGiB() int {
	if a.RootVolumeGiB == 0 {
		return DefaultRootVolumeGiB
	}

	return a.RootVolumeGiB
}

func (a Args) healthMinutes() int {
	if a.HealthFailMinutes == 0 {
		return DefaultHealthFailMinutes
	}

	return a.HealthFailMinutes
}

// architecture is the CPU architecture of the instance type.
func (a Args) architecture() architecture {
	if gravitonRE.MatchString(a.instanceType()) {
		return archARM64
	}

	return archAMD64
}

func (a Args) anyPublic() bool {
	for _, inst := range a.Instances {
		if inst.Public {
			return true
		}
	}

	return false
}

// names are the physical names derived from the component name.
type names struct{ asg, hook, role string }

func namesFor(name string) names {
	asg := "statusbox-" + name

	return names{asg: asg, hook: asg + "-launch", role: asg}
}

// databaseFile is the SQLite file name under /data an instance's Config asks
// Gatus to use.
func databaseFile(inst statusbox.Instance) (string, error) {
	var cfg struct {
		Storage struct {
			Type string `yaml:"type"`
			Path string `yaml:"path"`
		} `yaml:"storage"`
	}

	if err := yaml.Unmarshal([]byte(inst.Config), &cfg); err != nil {
		return "", fmt.Errorf("statusbox/ec2: instance %q: Config is not YAML: %w", inst.Name, err)
	}

	if cfg.Storage.Type != "sqlite" {
		return "", fmt.Errorf("statusbox/ec2: instance %q: Config needs `storage.type: sqlite` (it is %q): "+
			"Litestream replicates a SQLite file", inst.Name, cfg.Storage.Type)
	}

	file, ok := strings.CutPrefix(cfg.Storage.Path, dataDir)
	if !ok || file != path.Base(file) || !dbFileRE.MatchString(file) {
		return "", fmt.Errorf("statusbox/ec2: instance %q: storage.path %q must be a file directly under %s (for instance %sops.db): that directory is what the "+
			"instance mounts and Litestream replicates", inst.Name, cfg.Storage.Path, dataDir, dataDir)
	}

	return file, nil
}

// validate reports every problem it can find, not just the first.
func (a Args) validate() error {
	var errs []error

	if !versionRE.MatchString(a.Version) {
		errs = append(errs, fmt.Errorf("statusbox/ec2: Version %q is not a release tag shape (%s): "+
			"the Gatus binaries and checksums.txt are fetched from that release", a.Version, versionRE))
	}

	errs = append(errs, a.validateInstances()...)
	errs = append(errs, a.validateSecrets()...)
	errs = append(errs, a.validateNetwork()...)
	errs = append(errs, a.validateStorageAndSize()...)

	return errors.Join(errs...)
}

func (a Args) validateInstances() []error {
	var errs []error

	if err := statusbox.ValidateInstances(a.Instances, a.Hostnames); err != nil {
		errs = append(errs, err)
	}

	for _, inst := range a.Instances {
		if strings.TrimSpace(inst.Config) == "" || inst.Name == "" {
			continue
		}

		if _, err := databaseFile(inst); err != nil {
			errs = append(errs, err)
		}
	}

	if a.TrustedCAs != "" {
		if err := statusbox.ValidateTrustedCAs(a.TrustedCAs); err != nil {
			errs = append(errs, err)
		}
	}

	return errs
}

func (a Args) validateSecrets() []error {
	var errs []error

	checkParam := func(what, name string) {
		if !parameterRE.MatchString(name) {
			errs = append(errs, fmt.Errorf("statusbox/ec2: %s %q is not an SSM parameter name (%s): it must be a path starting with /", what, name, parameterRE))
		}
	}

	switch {
	case a.anyPublic() && a.TunnelTokenParameter == "":
		errs = append(errs, errors.New("statusbox/ec2: at least one instance is Public but TunnelTokenParameter is empty: "+
			"a public page needs the tunnel that carries its ingress rule"))
	case !a.anyPublic() && a.TunnelTokenParameter != "":
		errs = append(errs, errors.New("statusbox/ec2: TunnelTokenParameter is set but no instance is Public: nothing would use the tunnel"))
	case a.TunnelTokenParameter != "":
		checkParam("TunnelTokenParameter", a.TunnelTokenParameter)
	}

	for _, k := range sortedKeys(a.AlertURLParameters) {
		checkParam(fmt.Sprintf("AlertURLParameters[%q]", k), a.AlertURLParameters[k])
	}

	for _, k := range sortedKeys(a.EnvParameters) {
		checkParam(fmt.Sprintf("EnvParameters[%q]", k), a.EnvParameters[k])
	}

	if err := statusbox.ValidateSecretNames(sortedKeys(a.AlertURLParameters), sortedKeys(a.EnvParameters)); err != nil {
		errs = append(errs, err)
	}

	return errs
}

func (a Args) validateNetwork() []error {
	var errs []error

	if a.VPCID == nil {
		errs = append(errs, errors.New("statusbox/ec2: VPCID is nil"))
	}

	if len(a.SubnetIDs) < 2 {
		errs = append(errs, fmt.Errorf("statusbox/ec2: %d subnet(s): the group needs subnets in two Availability Zones "+
			"so a zone outage can be replaced", len(a.SubnetIDs)))
	}

	for i, s := range a.SubnetIDs {
		if s == nil {
			errs = append(errs, fmt.Errorf("statusbox/ec2: SubnetIDs[%d] is nil", i))
		}
	}

	for _, c := range a.PrivateIngressCIDRs {
		if p, err := netip.ParsePrefix(c); err != nil || !p.Addr().Is4() {
			errs = append(errs, fmt.Errorf("statusbox/ec2: PrivateIngressCIDRs entry %q is not an IPv4 CIDR", c))
		} else if p.Bits() == 0 {
			errs = append(errs, fmt.Errorf("statusbox/ec2: PrivateIngressCIDRs entry %q opens the private page to the whole internet", c))
		}
	}

	return errs
}

func (a Args) validateStorageAndSize() []error {
	var errs []error

	if !bucketRE.MatchString(a.Bucket) {
		errs = append(errs, fmt.Errorf("statusbox/ec2: Bucket %q is not an S3 bucket name", a.Bucket))
	}

	if a.BucketPrefix != "" && (!prefixRE.MatchString(a.BucketPrefix) || strings.Contains(a.BucketPrefix, "..")) {
		errs = append(errs, fmt.Errorf("statusbox/ec2: BucketPrefix %q must be slash-separated plain segments with no leading or trailing slash", a.BucketPrefix))
	}

	if a.KMSKeyARN != "" && !kmsARNRE.MatchString(a.KMSKeyARN) {
		errs = append(errs, fmt.Errorf("statusbox/ec2: KMSKeyARN %q is not a KMS key ARN (a key, not an alias: IAM needs the key)", a.KMSKeyARN))
	}

	if !instanceTypeRE.MatchString(a.instanceType()) {
		errs = append(errs, fmt.Errorf("statusbox/ec2: InstanceType %q is not an instance type", a.instanceType()))
	}

	if a.rootVolumeGiB() < DefaultRootVolumeGiB {
		errs = append(errs, fmt.Errorf("statusbox/ec2: RootVolumeGiB %d is below %d, the Amazon Linux 2023 image size", a.rootVolumeGiB(), DefaultRootVolumeGiB))
	}

	if a.HealthFailMinutes < 0 {
		errs = append(errs, fmt.Errorf("statusbox/ec2: HealthFailMinutes %d is negative", a.HealthFailMinutes))
	}

	return errs
}

// parameterNames is every SSM parameter the role may read, sorted, unique.
func (a Args) parameterNames() []string {
	set := map[string]bool{}

	if a.TunnelTokenParameter != "" {
		set[a.TunnelTokenParameter] = true
	}

	for _, p := range a.AlertURLParameters {
		set[p] = true
	}

	for _, p := range a.EnvParameters {
		set[p] = true
	}

	out := make([]string, 0, len(set))
	for p := range set {
		out = append(out, p)
	}

	sort.Strings(out)

	return out
}

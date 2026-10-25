package ec2_test

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/common/resource"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"
	"github.com/stretchr/testify/require"
	"github.com/truvity/tailscale/pkg/hostaccess"

	statusboxec2 "github.com/truvity/observability/pkg/statusbox/ec2"
)

// The resource-set goldens are the proof that NewEC2 asks the provider for the
// same resources whatever builds them: every resource's type, logical name,
// parent, dependencies, custom timeouts and every input, for three boxes that
// between them turn on every option. They were recorded from the hand-built
// module, before it moved onto the shared appliance library, and must not move
// when the library changes: a moved golden is a replaced or updated resource in
// a live estate. Run `go test ./pkg/statusbox/ec2 -run TestResourceSet -update`
// only for a change that is meant to move the resources, and review the diff.

// testCA is a throwaway self-signed certificate, valid PEM and nothing else.
const testCA = `-----BEGIN CERTIFICATE-----
MIIBTzCCAQGgAwIBAgIUXRUR3N3L+3gEuCsmwgCvVGZQ0pgwBQYDK2VwMBwxGjAY
BgNVBAMMEXN0YXR1c2JveCB0ZXN0IGNhMCAXDTI2MTAxNDIyNTE1OFoYDzIxMjYw
OTIwMjI1MTU4WjAcMRowGAYDVQQDDBFzdGF0dXNib3ggdGVzdCBjYTAqMAUGAytl
cAMhAJmgPUk8YfaYowmvJGzpb31fqsouZRtopIJcXA5UUihho1MwUTAdBgNVHQ4E
FgQURqj3pjFHm4k/uwds1CAn/4l8b2QwHwYDVR0jBBgwFoAURqj3pjFHm4k/uwds
1CAn/4l8b2QwDwYDVR0TAQH/BAUwAwEB/zAFBgMrZXADQQAMIUuEn7QHfDeF+mCM
tGjpRWWMe1ToKm2O8pWmZ42biR588JfD9KXhlLzuO0kMZavVMzSVtMLEnEb3zIvL
3qsF
-----END CERTIFICATE-----
`

var (
	accountRE = regexp.MustCompile(account)
	// partitionRE matches the ARN partition prefix, spelled apart so the source
	// has no ARN literal; the goldens hold a placeholder, as the account id is.
	partitionRE = regexp.MustCompile("arn:" + part)
)

// setRecorder remembers everything the mocked provider is asked for.
type setRecorder struct {
	mu      sync.Mutex
	all     []map[string]any
	lookups []string
}

func urnTail(u string) string {
	if _, rest, ok := strings.Cut(u, "::"); ok {
		if _, tail, ok := strings.Cut(rest, "::"); ok {
			return tail
		}
	}

	return u
}

func sha(s string) string {
	h := sha256.Sum256([]byte(s))

	return hex.EncodeToString(h[:])
}

func (r *setRecorder) NewResource(args pulumi.MockResourceArgs) (string, resource.PropertyMap, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	in := args.Inputs.Mappable()
	entry := map[string]any{"type": args.TypeToken, "name": args.Name}

	// The user-data and object bodies are recorded by digest: they are large,
	// and a digest is exactly as strict.
	switch args.TypeToken {
	case "aws:ec2/launchTemplate:LaunchTemplate":
		if s, ok := in["userData"].(string); ok {
			in["userData"] = "sha256:" + sha(s)
		}
	case "aws:s3/bucketObjectv2:BucketObjectv2":
		if s, ok := in["content"].(string); ok {
			in["content"] = "sha256:" + sha(s)
		}
	}

	entry["inputs"] = in

	if rpc := args.RegisterRPC; rpc != nil {
		entry["parent"] = urnTail(rpc.GetParent())

		deps := make([]string, 0, len(rpc.GetDependencies()))
		for _, d := range rpc.GetDependencies() {
			deps = append(deps, urnTail(d))
		}

		sort.Strings(deps)

		if len(deps) > 0 {
			entry["dependencies"] = deps
		}

		if t := rpc.GetCustomTimeouts(); t != nil {
			entry["timeouts"] = map[string]string{"create": t.GetCreate(), "update": t.GetUpdate(), "delete": t.GetDelete()}
		}
	}

	r.all = append(r.all, entry)

	out := args.Inputs.Copy()
	if args.TypeToken == "aws:ec2/launchTemplate:LaunchTemplate" {
		out["latestVersion"] = resource.NewNumberProperty(3)
	}

	return args.Name + "_id", out, nil
}

func (r *setRecorder) Call(args pulumi.MockCallArgs) (resource.PropertyMap, error) {
	r.mu.Lock()
	look, _ := json.Marshal(args.Args.Mappable())
	r.lookups = append(r.lookups, args.Token+" "+string(look))
	r.mu.Unlock()

	switch args.Token {
	case "aws:index/getRegion:getRegion":
		return resource.PropertyMap{"region": resource.NewStringProperty("eu-west-1"), "name": resource.NewStringProperty("eu-west-1")}, nil
	case "aws:index/getCallerIdentity:getCallerIdentity":
		return resource.PropertyMap{"accountId": resource.NewStringProperty(account)}, nil
	case "aws:ssm/getParameter:getParameter":
		return resource.PropertyMap{"name": args.Args["name"], "value": resource.NewStringProperty("ami-0example")}, nil
	}

	return args.Args, nil
}

// dump is the sorted resource set as indented JSON, the account id and the ARN
// partition replaced by placeholders so a public golden carries neither.
func (r *setRecorder) dump(t *testing.T, boxUserData string) []byte {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()

	sort.Slice(r.all, func(i, j int) bool {
		return r.all[i]["type"].(string)+"/"+r.all[i]["name"].(string) < r.all[j]["type"].(string)+"/"+r.all[j]["name"].(string)
	})

	sort.Strings(r.lookups)

	b, err := json.MarshalIndent(map[string]any{
		"resources":   r.all,
		"lookups":     r.lookups,
		"boxUserData": "sha256:" + sha(boxUserData),
	}, "", "  ")
	require.NoError(t, err)

	b = accountRE.ReplaceAll(b, []byte("@ACCOUNT@"))
	b = partitionRE.ReplaceAll(b, []byte("arn:@PARTITION@"))

	return append(b, '\n')
}

// resourceSetScenarios are boxes that between them use every option.
func resourceSetScenarios() map[string]func() *statusboxec2.Args {
	return map[string]func() *statusboxec2.Args{
		// The estate's box: a public and a private page, Graviton nano.
		"plain": validArgs,

		// Every option on: SSH with a host certificate and a pinned host key,
		// self-registration, a customer key, a boundary, Session Manager, the
		// dead-man ping, extra CAs, tags, other sizes and an x86 type.
		"full": func() *statusboxec2.Args {
			a := validArgs()
			a.SSH = &statusboxec2.SSHArgs{
				OPKSSH: hostaccess.OPKSSHPreset{Issuer: "https://issuer.example.test", ClientID: "opkssh", User: "ec2-user", Group: "ops"},
				HostCert: &hostaccess.HostCertPreset{
					Address: "https://bao.example.test", AuthMount: "aws", AuthRole: "hostcert", ServerIDHeader: "bao.example.test",
					SSHMount: "ssh-host", SSHRole: "host", PrincipalPatterns: []string{"ip-*.eu-west-3.compute.internal"},
				},
				HostKeyParameter: "/acme/status/ssh-host-key",
				IngressCIDRs:     []string{"10.30.0.0/16", "10.31.0.0/16"},
			}
			a.SelfRegister = &statusboxec2.SelfRegisterArgs{
				RoleARN:      "arn:" + part + ":iam::" + account + ":role/dns-register",
				HostedZoneID: "Z0EXAMPLE",
				RecordName:   "status.internal.example.test",
				TTL:          120,
			}
			a.KMSKeyARN = "arn:" + part + ":kms:eu-west-1:" + account + ":key/1234abcd-12ab-34cd-56ef-1234567890ab"
			a.PermissionsBoundary = "arn:" + part + ":iam::" + account + ":policy/boundary"
			a.SessionManager = true
			a.PingURLParameter = "/acme/status/ping-url"
			a.EnvParameters = map[string]string{"OIDC_CLIENT_SECRET": "/acme/status/oidc-secret"}
			a.TrustedCAs = testCA
			a.Tags = map[string]string{"Estate": "acme", "Team": "ops"}
			a.InstanceType = "t4g.small"
			a.RootVolumeGiB = 20
			a.HealthFailMinutes = 9
			a.BucketPrefix = "box/replica"

			return a
		},

		// An x86 box with a custom size and tags and the AWS-managed key.
		"x86": func() *statusboxec2.Args {
			a := validArgs()
			a.InstanceType = "m6i.large"
			a.RootVolumeGiB = 30
			a.Tags = map[string]string{"Estate": "acme"}
			a.PermissionsBoundary = "arn:" + part + ":iam::" + account + ":policy/boundary"

			return a
		},

		// A private-only box, nothing public and no tunnel, no prefix.
		"private": func() *statusboxec2.Args {
			a := validArgs()
			a.Instances = a.Instances[1:]
			a.Hostnames = nil
			a.TunnelTokenParameter = ""
			a.BucketPrefix = ""
			a.PrivateIngressCIDRs = []string{"10.20.0.0/16", "10.21.0.0/16"}

			return a
		},
	}
}

func TestResourceSetIsUnchanged(t *testing.T) {
	updating := flag.Lookup("update") != nil && flag.Lookup("update").Value.String() == "true"

	for name, build := range resourceSetScenarios() {
		t.Run(name, func(t *testing.T) {
			stubChecksums(t)

			rec := &setRecorder{}

			var userData string

			err := pulumi.RunErr(func(ctx *pulumi.Context) error {
				box, err := statusboxec2.NewEC2(ctx, "status", build())
				if err != nil {
					return err
				}

				userData = string(box.UserData.UserData)

				return nil
			}, pulumi.WithMocks("statusbox-ec2-test", "test", rec))
			require.NoError(t, err)

			got := rec.dump(t, userData)
			file := filepath.Join("testdata", "resourceset-"+name+".golden.json")

			if updating {
				require.NoError(t, os.WriteFile(file, got, 0o644))

				return
			}

			want, err := os.ReadFile(file)
			require.NoError(t, err, "run 'go test ./pkg/statusbox/ec2 -run TestResourceSetIsUnchanged -update' and review the diff")
			require.Equal(t, string(want), string(got), "the resource set moved: a live estate would replace or update resources")
		})
	}
}

// Package credentials mints the credentials an observability install needs
// and writes them to a cluster's secret store.
//
// Neither value is issued by anybody. They are two arbitrary strings that the
// install's own components use to authenticate to each other -- the basic
// auth the three stores demand of everything that reaches them, and the
// bearer the collectors write with -- so the thing that "holds" them is
// whatever wrote them last. A random password per credential, a rotation
// lever per credential, and a write of the value to where each reader looks:
// that is the whole mechanism.
//
// The package carries no estate data. Which cluster, which rotation dates,
// which SSM paths, which key a client secret lives under, which namespaces
// receive a shared bearer: all of it arrives in Inputs. What the package does
// own is the Pulumi resource names, which are state identity and must not
// change, and the shape of the install's key layout (KVKind plus
// "/store-credentials", "/write-token", ...), which is a contract with the
// charts that mount the values.
package credentials

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"unicode"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ssm"
	"github.com/pulumi/pulumi-random/sdk/v4/go/random"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/k8s/pkg/aws/podidentity"
)

const (
	// IAM policy-document JSON keys and values.
	polVersion   = "Version"
	polDocDate   = "2012-10-17"
	polStatement = "Statement"
	polSid       = "Sid"
	polEffect    = "Effect"
	polAllow     = "Allow"
	polAction    = "Action"
	polResource  = "Resource"

	// rotatedKeeperKey is the keepers map key every credential uses for its
	// own rotation lever: one shared property name, each credential's own
	// value.
	rotatedKeeperKey = "rotated"

	// StoreUser is the basic-auth username the three stores run with. A name
	// and not a secret: what the pair protects is carried entirely by the
	// password.
	StoreUser = "observability"

	// storePasswordLength and writeTokenLength are characters of the
	// alphanumeric alphabet: about 190 bits for the store password and about
	// 380 for the bearer, which is the credential that travels.
	storePasswordLength = 32
	writeTokenLength    = 64

	// tokenLength is the length of every other bearer or client secret: a
	// value presented over HTTPS, never typed by a person.
	tokenLength = 64
)

type (
	// KVWriter writes one (namespace, key) of the secret store. The estate's
	// writer refuses a key its layout does not declare, which keeps the
	// credentials' spellings, the ExternalSecrets that read them and the
	// charts that mount them one spelling.
	KVWriter interface {
		Put(c *pulumi.Context, namespace, key string, data map[string]pulumi.StringInput, opts ...pulumi.ResourceOption) error
	}

	// Inputs are everything Deploy needs; none of it has a default.
	Inputs struct {
		// Cluster is the namespace the install's own credentials are written
		// to.
		Cluster string
		Writer  KVWriter
		// KVKind is the key prefix of the install's values (for the store
		// pair, KVKind+"/store-credentials").
		KVKind string
		// StoreInstall says the cluster runs the full install (its own
		// stores demanding the basic-auth pair), as against an
		// operator-only one that holds no store.
		StoreInstall bool
		// Rotated is the shared rotation lever of the store password and the
		// write token. Editing it replaces both resources; never derive it
		// from anything that varies by run.
		Rotated string
		// StorePasswordRotated pins ONE cluster's store-password lever; empty
		// falls back to Rotated.
		StorePasswordRotated string
		// Kernel is set for the management cluster only.
		Kernel *Kernel
	}

	// Kernel are the extra credentials the management cluster mints on the
	// same apply, because it is the one place holding both an operator
	// sign-in to the secret store and an ordinary AWS provider.
	Kernel struct {
		// Provider is the AWS provider onto the cluster's own account; it is
		// built once by the caller, since a second one with the same name
		// registers the same URN twice.
		Provider *aws.Provider

		// StatusboxRotated and StatusboxReadTokenSSM: the status box's
		// alerts-read bearer, written to KVKind+"/statusbox" and to SSM.
		StatusboxRotated      string
		StatusboxReadTokenSSM string

		// EvaluatorRotated and EvaluatorNamespaces: the bearer a remote
		// evaluator reads a store with, written under KVKind+"/kernel-evaluator"
		// to every listed namespace (one mint, several writes).
		EvaluatorRotated    string
		EvaluatorNamespaces []string

		// OIDCClientSecretKV is the exact key the issuer reads to verify a
		// client's token exchange; the value is also written to
		// OIDCClientSecretSSM.
		OIDCClientSecretKV      string
		OIDCClientSecretRotated string
		OIDCClientSecretSSM     string

		// DeadmanSlack copies a bot token the secret store holds into SSM.
		DeadmanSlack *DeadmanSlack

		// MetricsBackup is the metrics-store backup's Pod Identity role; nil
		// skips it (the backup bucket is not deployed yet).
		MetricsBackup *MetricsBackup
	}

	// DeadmanSlack is the copy of the deadman Slack App's bot token.
	DeadmanSlack struct {
		// Skip, when non-empty, is the reason this run copies nothing.
		Skip string
		// Read returns the token, or "" when the App is not installed yet.
		Read func(ctx context.Context) (string, error)
		// SSMPath is where the copy goes.
		SSMPath string
		// Source names the token's origin, for messages.
		Source string
	}

	// MetricsBackup is the role the metrics store's backup jobs run as, through
	// EKS Pod Identity, with no static key.
	MetricsBackup struct {
		ClusterName string
		// ClusterARN is the cluster's ARN, which the association is bound to.
		ClusterARN          string
		Region              string
		AccountID           string
		PermissionsBoundary string
		Namespace           string
		ServiceAccount      string
		RoleName            string
		// Prefix is the one bucket prefix the role may read and write.
		Prefix    string
		BucketARN string
		KMSKeyARN string
		// BucketName is exported for reference.
		BucketName string
	}
)

// keepers is the one-entry keepers map of a rotation lever.
func keepers(lever string) pulumi.StringMap {
	return pulumi.StringMap{rotatedKeeperKey: pulumi.String(lever)}
}

// mint registers one alphanumeric RandomPassword.
func mint(ctx *pulumi.Context, name string, length int, lever string) (*random.RandomPassword, error) {
	return random.NewRandomPassword(ctx, name, &random.RandomPasswordArgs{
		Length:  pulumi.Int(length),
		Keepers: keepers(lever),
		// Letters and digits only: the value rides an HTTP header or an
		// environment variable, and every character that needs a quoting
		// rule is a way to lose an hour.
		Special: pulumi.Bool(false),
	})
}

// Deploy mints the install's credentials for one cluster and writes them to
// that cluster's namespace of the secret store, under KVKind+"/store-credentials"
// (username, password) and KVKind+"/write-token" (token).
func Deploy(ctx *pulumi.Context, logger *slog.Logger, in Inputs) error {
	storeLever := in.Rotated
	if in.StorePasswordRotated != "" {
		storeLever = in.StorePasswordRotated
	}

	if in.StoreInstall {
		password, err := mint(ctx, "observability-store-password", storePasswordLength, storeLever)
		if err != nil {
			return fmt.Errorf("mint the observability store password: %w", err)
		}

		if err := in.Writer.Put(ctx, in.Cluster, in.KVKind+"/store-credentials", map[string]pulumi.StringInput{
			"username": pulumi.String(StoreUser),
			"password": password.Result,
		}); err != nil {
			return err
		}
	}

	token, err := mint(ctx, "observability-write-token", writeTokenLength, in.Rotated)
	if err != nil {
		return fmt.Errorf("mint the observability write token: %w", err)
	}

	if err := in.Writer.Put(ctx, in.Cluster, in.KVKind+"/write-token", map[string]pulumi.StringInput{
		"token": token.Result,
	}); err != nil {
		return err
	}

	logger.InfoContext(ctx.Context(), "observability credentials written",
		slog.String("cluster", in.Cluster),
		slog.String(rotatedKeeperKey, in.Rotated),
	)

	if in.Kernel == nil {
		return nil
	}

	return deployKernel(ctx, logger, in)
}

func deployKernel(ctx *pulumi.Context, logger *slog.Logger, in Inputs) error {
	k := in.Kernel

	if err := deployStatusboxReadToken(ctx, logger, in); err != nil {
		return fmt.Errorf("statusbox read token: %w", err)
	}

	if err := deployEvaluatorReadToken(ctx, logger, in); err != nil {
		return fmt.Errorf("kernel evaluator read token: %w", err)
	}

	if err := deployOIDCClientSecret(ctx, logger, in); err != nil {
		return fmt.Errorf("statusbox OIDC client secret: %w", err)
	}

	if k.DeadmanSlack != nil {
		if err := copyDeadmanSlackToken(ctx, logger, k.Provider, k.DeadmanSlack); err != nil {
			return fmt.Errorf("statusbox deadman Slack token: %w", err)
		}
	}

	if k.MetricsBackup != nil {
		if err := mintMetricsBackupRole(ctx, logger, k.Provider, k.MetricsBackup); err != nil {
			return fmt.Errorf("metrics backup role: %w", err)
		}
	}

	return nil
}

// deployStatusboxReadToken mints the status box's one estate-internal bearer
// and puts it where each of its two readers can reach it: the cluster's
// namespace of the secret store (for the install's own alert readers) and SSM
// (for the box's own stack, which runs in an isolated account with no route to
// the secret store).
func deployStatusboxReadToken(ctx *pulumi.Context, logger *slog.Logger, in Inputs) error {
	k := in.Kernel

	token, err := mint(ctx, "statusbox-alerts-read-token", tokenLength, k.StatusboxRotated)
	if err != nil {
		return fmt.Errorf("mint the statusbox alerts-read token: %w", err)
	}

	if err := in.Writer.Put(ctx, in.Cluster, in.KVKind+"/statusbox", map[string]pulumi.StringInput{
		"read-token": token.Result,
	}); err != nil {
		return err
	}

	if _, err := ssm.NewParameter(ctx, "statusbox-ssm-read-token", &ssm.ParameterArgs{
		Name:      pulumi.String(k.StatusboxReadTokenSSM),
		Type:      pulumi.String("SecureString"),
		Value:     token.Result,
		Overwrite: pulumi.Bool(true),
	}, pulumi.Provider(k.Provider)); err != nil {
		return fmt.Errorf("write statusbox alerts-read token to SSM: %w", err)
	}

	logger.InfoContext(ctx.Context(), "statusbox alerts-read token minted and written",
		slog.String(rotatedKeeperKey, k.StatusboxRotated),
	)

	return nil
}

// deployEvaluatorReadToken mints the bearer a remote-evaluator vmalert reads
// another cluster's metrics store with and writes the SAME value into every
// namespace that needs it. One mint, several writes.
func deployEvaluatorReadToken(ctx *pulumi.Context, logger *slog.Logger, in Inputs) error {
	k := in.Kernel

	token, err := mint(ctx, "observability-kernel-evaluator-read-token", tokenLength, k.EvaluatorRotated)
	if err != nil {
		return fmt.Errorf("mint the kernel evaluator read token: %w", err)
	}

	for _, namespace := range k.EvaluatorNamespaces {
		if err := in.Writer.Put(ctx, namespace, in.KVKind+"/kernel-evaluator", map[string]pulumi.StringInput{
			"token": token.Result,
		}); err != nil {
			return err
		}
	}

	logger.InfoContext(ctx.Context(), "kernel evaluator read token minted and written",
		slog.String(rotatedKeeperKey, k.EvaluatorRotated),
	)

	return nil
}

// deployOIDCClientSecret mints a roster client's OIDC secret and puts it where
// each of its two readers can reach it: the key the issuer itself reads, and
// SSM for the box's own stack. A separate resource from every other credential,
// so one rotation never silently rotates another.
func deployOIDCClientSecret(ctx *pulumi.Context, logger *slog.Logger, in Inputs) error {
	k := in.Kernel

	secret, err := mint(ctx, "statusbox-oidc-client-secret", tokenLength, k.OIDCClientSecretRotated)
	if err != nil {
		return fmt.Errorf("mint the statusbox OIDC client secret: %w", err)
	}

	if err := in.Writer.Put(ctx, in.Cluster, k.OIDCClientSecretKV, map[string]pulumi.StringInput{
		"client-secret": secret.Result,
	}); err != nil {
		return err
	}

	if _, err := ssm.NewParameter(ctx, "statusbox-ssm-oidc-client-secret", &ssm.ParameterArgs{
		Name:      pulumi.String(k.OIDCClientSecretSSM),
		Type:      pulumi.String("SecureString"),
		Value:     secret.Result,
		Overwrite: pulumi.Bool(true),
	}, pulumi.Provider(k.Provider)); err != nil {
		return fmt.Errorf("write statusbox OIDC client secret to SSM: %w", err)
	}

	logger.InfoContext(ctx.Context(), "statusbox OIDC client secret minted and written",
		slog.String(rotatedKeeperKey, k.OIDCClientSecretRotated),
	)

	return nil
}

// copyDeadmanSlackToken copies the deadman Slack App's bot token from the
// secret store into SSM, where the status box's stack reads it. The box must
// page when the cluster is down, so it cannot fetch the token when it alerts:
// the token is baked into the box's cloud-init like every other secret it
// holds.
//
// An App that is not installed yet has no token: the copy is skipped with a
// warning and nothing is written. A run with Skip set skips it too.
func copyDeadmanSlackToken(ctx *pulumi.Context, logger *slog.Logger, provider *aws.Provider, d *DeadmanSlack) error {
	if d.Skip != "" {
		logger.WarnContext(ctx.Context(), "NOT copying the deadman Slack token this run",
			slog.String("skip_reason", d.Skip))

		return nil
	}

	token, err := d.Read(ctx.Context())
	if err != nil {
		return err
	}

	if token == "" {
		logger.WarnContext(ctx.Context(), "the deadman Slack App has no token in the secret store yet (not installed?): nothing copied",
			slog.String("key", d.Source))

		return nil
	}

	if strings.IndexFunc(token, unicode.IsSpace) != -1 {
		return fmt.Errorf("%s holds whitespace; refusing to copy it", d.Source)
	}

	if _, err := ssm.NewParameter(ctx, "statusbox-ssm-deadman-slack-token", &ssm.ParameterArgs{
		Name:      pulumi.String(d.SSMPath),
		Type:      pulumi.String("SecureString"),
		Value:     pulumi.ToSecret(pulumi.String(token)).(pulumi.StringOutput),
		Overwrite: pulumi.Bool(true),
	}, pulumi.Provider(provider)); err != nil {
		return fmt.Errorf("write the deadman Slack token to SSM: %w", err)
	}

	logger.InfoContext(ctx.Context(), "deadman Slack token copied from the secret store to SSM")

	return nil
}

// mintMetricsBackupRole mints the Pod Identity role the metrics store's backup
// CronJobs run as: an identity policy scoped exactly to the backup bucket's
// one prefix and its KMS key, and a PodIdentityAssociation to the exact
// (namespace, ServiceAccount) the chart renders. The bucket lives in an
// isolated account and the role where the cluster does, so this is the second
// gate: the bucket and key policies already admit this role by ARN, and AWS
// also requires the caller's own account to grant the action.
func mintMetricsBackupRole(ctx *pulumi.Context, logger *slog.Logger, provider *aws.Provider, b *MetricsBackup) error {
	policy, err := metricsBackupPolicy(b.Prefix, b.BucketARN, b.KMSKeyARN)
	if err != nil {
		return err
	}

	// The default trust policy admits exactly one ServiceAccount of one
	// namespace on one cluster through EKS Pod Identity. The children keep
	// their top-level names (the role's own name, "-policy" and "-pia")
	// through an alias.
	role, err := podidentity.New(ctx, b.RoleName, &podidentity.Args{
		Provider:            provider,
		ClusterName:         pulumi.String(b.ClusterName),
		Region:              b.Region,
		Namespace:           b.Namespace,
		ServiceAccounts:     []string{b.ServiceAccount},
		RoleName:            b.RoleName,
		PermissionsBoundary: b.PermissionsBoundary,
		AccountID:           b.AccountID,
		ClusterARN:          b.ClusterARN,
		InlinePolicy:        &podidentity.InlinePolicy{Name: "observability-metrics-backup", Document: pulumi.String(policy)},
		LegacyTopLevel:      true,
		Names: func(c podidentity.Child) string {
			switch c.Kind {
			case podidentity.KindRolePolicy:
				return b.RoleName + "-policy"
			case podidentity.KindAssociation:
				return b.RoleName + "-pia"
			default:
				return b.RoleName
			}
		},
	})
	if err != nil {
		return fmt.Errorf("metrics backup pod identity: %w", err)
	}

	ctx.Export("metricsBackupRoleArn", role.RoleARN)
	ctx.Export("metricsBackupBucket", pulumi.String(b.BucketName))

	logger.InfoContext(ctx.Context(), "observability metrics backup role minted",
		slog.String("role", b.RoleName),
		slog.String("bucket", b.BucketName),
	)

	return nil
}

// metricsBackupPolicy is the writer's identity-side grant: list the bucket
// scoped to the prefix, read/write/delete objects under that one prefix
// (vmbackup's incremental job diffs against what is already there and removes
// parts the store has since merged away; the weekly full job's origin copy
// reads the incremental's own objects server-side), and use the backup's own
// KMS key in both directions.
func metricsBackupPolicy(prefix, bucketARN, keyARN string) (string, error) {
	raw, err := json.Marshal(map[string]any{
		polVersion: polDocDate,
		polStatement: []map[string]any{
			{
				polSid:      "ListMetricsBackupPrefix",
				polEffect:   polAllow,
				polAction:   []string{"s3:ListBucket", "s3:ListBucketMultipartUploads"},
				polResource: bucketARN,
				"Condition": map[string]any{
					"StringLike": map[string]any{"s3:prefix": []string{prefix, prefix + "/*"}},
				},
			},
			{
				polSid:      "ReadWriteMetricsBackup",
				polEffect:   polAllow,
				polAction:   []string{"s3:PutObject", "s3:GetObject", "s3:DeleteObject", "s3:AbortMultipartUpload", "s3:ListMultipartUploadParts"},
				polResource: bucketARN + "/" + prefix + "/*",
			},
			{
				polSid:      "UseMetricsBackupKey",
				polEffect:   polAllow,
				polAction:   []string{"kms:GenerateDataKey", "kms:Encrypt", "kms:Decrypt", "kms:DescribeKey"},
				polResource: keyARN,
			},
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal metrics backup policy: %w", err)
	}

	return string(raw), nil
}

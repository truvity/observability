// Package lambdaprobe publishes the generic OTLP Lambda layer and deploys a
// small probe function that uses it: the continuous end-to-end check for
// "Lambda -> OTLP, authenticated by its IAM role".
//
// The layer zips are truvity/observability release assets, downloaded at
// deploy time and verified against the release's checksums file
// (package otlplayer, which an audit stack also uses).
// They are not the registry's S3 "Lambda ZIP" artifacts: that path stores a
// project's own build output keyed by project, whereas a layer version is
// published straight from a file and owned by no project.
//
// Everything is driven by Config and refuses to run in any account but the
// one Config names.
package lambdaprobe

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudwatch"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/truvity/observability/deploy/pulumi/otlplayer"
	"github.com/truvity/observability/deploy/pulumi/releaseassets"
)

const (
	basicExecutionPolicyARN = "arn:aws:iam::aws:policy/service-role/AWSLambdaBasicExecutionRole"
	logRetentionDays        = 14

	assumeRolePolicy = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Principal":{"Service":"lambda.amazonaws.com"},"Action":"sts:AssumeRole"}]}`
)

//go:embed handler.py
var handlerSource string

// Inputs are the deploy-time facts Deploy cannot derive from cfg.
type (
	Inputs struct {
		// Account is the logical account name this program runs in.
		Account string
		// PermissionsBoundary is the full ARN of the IAM permissions boundary
		// the function role carries; empty means none.
		PermissionsBoundary string
		Provider            pulumi.ProviderResource
		Fetch               releaseassets.Fetcher
		// CacheDir receives the verified layer zips.
		CacheDir string
	}
)

// policyDocument renders the function role's sts policy: the identity token
// may only be asked for the issuer's audience, with the algorithm and
// lifetime the layer uses by default.
//
// sts:IdentityTokenAudience is MULTI-VALUED (GetWebIdentityToken takes a list
// of audiences), so it needs ForAllValues:StringEquals; a plain StringEquals
// evaluates to implicitDeny when the request carries the audience as a list.
// ForAllValues passes on an empty set, which is safe only because Audience is
// a required parameter of GetWebIdentityToken.
func policyDocument(audience string) (string, error) {
	doc := map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{map[string]any{
			"Sid":      "OutboundIdentityToken",
			"Effect":   "Allow",
			"Action":   "sts:GetWebIdentityToken",
			"Resource": "*",
			"Condition": map[string]any{
				"ForAllValues:StringEquals": map[string]any{"sts:IdentityTokenAudience": audience},
				"StringEquals":              map[string]any{"sts:SigningAlgorithm": "ES384"},
				"NumericLessThanEquals":     map[string]any{"sts:DurationSeconds": "300"},
			},
		}},
	}

	b, err := json.Marshal(doc)

	return string(b), err
}

// Deploy publishes the layer versions and the probe function.
func Deploy(c *pulumi.Context, logger *slog.Logger, cfg *Config, in Inputs) error {
	if in.Account != cfg.Account {
		return fmt.Errorf("lambda-otel-probe is configured for account %q (the caller's config), this program runs in %q", cfg.Account, in.Account)
	}

	ctx := context.Background()
	opt := pulumi.Provider(in.Provider)

	layers, err := otlplayer.Publish(ctx, c, in.Fetch, in.CacheDir, otlplayer.Source{
		Version:            cfg.Layer.Version,
		Name:               cfg.Layer.Name,
		Architectures:      cfg.Layer.Architectures,
		CompatibleRuntimes: cfg.Layer.CompatibleRuntimes,
	}, opt)
	if err != nil {
		return err
	}

	probe := cfg.Probe

	doc, err := policyDocument(probe.STSAudience())
	if err != nil {
		return fmt.Errorf("render role policy: %w", err)
	}

	roleArgs := &iam.RoleArgs{
		Name:             pulumi.String(probe.FunctionName),
		Description:      pulumi.String("lambda-otel-probe: sends telemetry through the OTLP layer, authenticated by this role"),
		AssumeRolePolicy: pulumi.String(assumeRolePolicy),
	}
	if in.PermissionsBoundary != "" {
		roleArgs.PermissionsBoundary = pulumi.String(in.PermissionsBoundary)
	}

	role, err := iam.NewRole(c, "role", roleArgs, opt)
	if err != nil {
		return fmt.Errorf("create role: %w", err)
	}

	if _, err := iam.NewRolePolicyAttachment(c, "role-basic-execution", &iam.RolePolicyAttachmentArgs{
		Role:      role.Name,
		PolicyArn: pulumi.String(basicExecutionPolicyARN),
	}, opt); err != nil {
		return fmt.Errorf("attach basic execution: %w", err)
	}

	if _, err := iam.NewRolePolicy(c, "role-identity-token", &iam.RolePolicyArgs{
		Name:   pulumi.String("outbound-identity-token"),
		Role:   role.Name,
		Policy: pulumi.String(doc),
	}, opt); err != nil {
		return fmt.Errorf("create role policy: %w", err)
	}

	logGroup, err := cloudwatch.NewLogGroup(c, "logs", &cloudwatch.LogGroupArgs{
		Name:            pulumi.Sprintf("/aws/lambda/%s", probe.FunctionName),
		RetentionInDays: pulumi.Int(logRetentionDays),
	}, opt)
	if err != nil {
		return fmt.Errorf("create log group: %w", err)
	}

	env := pulumi.StringMap{
		"ACCESS_ROSTER_ISSUER":        pulumi.String(probe.IssuerURL),
		"ACCESS_ROSTER_AUDIENCE":      pulumi.String(probe.STSAudience()),
		"ACCESS_ROSTER_OTLP_ENDPOINT": pulumi.String(probe.OTLPEndpoint),
		"OTEL_SERVICE_NAME":           pulumi.String(probe.FunctionName),
	}
	if probe.OTLPAudience != "" {
		env["ACCESS_ROSTER_OTLP_AUDIENCE"] = pulumi.String(probe.OTLPAudience)
	}

	fn, err := lambda.NewFunction(c, "function", &lambda.FunctionArgs{
		Name:          pulumi.String(probe.FunctionName),
		Description:   pulumi.String("Heartbeat: one span, one counter and one log line per run through the OTLP layer"),
		Role:          role.Arn,
		Runtime:       pulumi.String(probe.Runtime),
		Handler:       pulumi.String("handler.handler"),
		Architectures: pulumi.StringArray{pulumi.String(otlplayer.LambdaArch(probe.Architecture))},
		Layers:        pulumi.StringArray{layers[probe.Architecture].Arn},
		Timeout:       pulumi.Int(30),
		MemorySize:    pulumi.Int(128),
		Code: pulumi.NewAssetArchive(map[string]any{
			"handler.py": pulumi.NewStringAsset(handlerSource),
		}),
		Environment: &lambda.FunctionEnvironmentArgs{Variables: env},
		LoggingConfig: &lambda.FunctionLoggingConfigArgs{
			LogFormat: pulumi.String("Text"),
			LogGroup:  logGroup.Name,
		},
	}, opt)
	if err != nil {
		return fmt.Errorf("create function: %w", err)
	}

	rule, err := cloudwatch.NewEventRule(c, "schedule", &cloudwatch.EventRuleArgs{
		Name:               pulumi.Sprintf("%s-schedule", probe.FunctionName),
		Description:        pulumi.String("lambda-otel-probe heartbeat"),
		ScheduleExpression: pulumi.String(probe.Schedule),
	}, opt)
	if err != nil {
		return fmt.Errorf("create schedule: %w", err)
	}

	if _, err := cloudwatch.NewEventTarget(c, "schedule-target", &cloudwatch.EventTargetArgs{
		Rule: rule.Name,
		Arn:  fn.Arn,
	}, opt); err != nil {
		return fmt.Errorf("create schedule target: %w", err)
	}

	if _, err := lambda.NewPermission(c, "schedule-invoke", &lambda.PermissionArgs{
		Action:    pulumi.String("lambda:InvokeFunction"),
		Function:  fn.Name,
		Principal: pulumi.String("events.amazonaws.com"),
		SourceArn: rule.Arn,
	}, opt); err != nil {
		return fmt.Errorf("allow schedule to invoke: %w", err)
	}

	for _, arch := range cfg.Layer.Architectures {
		c.Export("layerArn-"+arch, layers[arch].Arn)
	}

	c.Export("functionArn", fn.Arn)
	c.Export("roleArn", role.Arn)

	logger.InfoContext(ctx, "lambda-otel-probe planned",
		slog.String("layer_version", cfg.Layer.Version),
		slog.String("audience", probe.STSAudience()))

	return nil
}

// The notification convention end to end, from AWS's own sample payloads:
// every source this service maps (GuardDuty through the chart's preset,
// Budgets, Cost Anomaly Detection and CloudWatch through the mappings
// docs/alert-ingress.md shows) must produce an alert whose `summary`,
// `description` and `link` say what happened without opening a console, and
// a message no mapping knows must still say what arrived.
//
// Each produced alert is also written to testdata/alerts/<case>.json
// (`go test ./cmd/alert-ingress -run TestAWSSamples -update` rewrites them).
// tests/notification_text_test.go renders those files through the shipped
// Slack template, so a change on either side shows in a reviewed diff.
package main

import (
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

var updateGolden = flag.Bool("update", false, "rewrite testdata/alerts/*.json from the current output")

const (
	securityTopic  = arnPrefix + ":sns:eu-central-1:" + account + ":security-alerts"
	budgetsTopic   = arnPrefix + ":sns:us-east-1:" + account + ":cost-budgets"
	anomaliesTopic = arnPrefix + ":sns:us-east-1:" + account + ":cost-anomalies"
)

// guarddutyPreset reads the chart's own preset, so the test exercises the
// exact text a consumer lists in valueFiles.
func guarddutyPreset(t *testing.T) []Mapping {
	t.Helper()

	raw, err := os.ReadFile("../../charts/alert-ingress/presets/guardduty.yaml")
	require.NoError(t, err)

	var doc struct {
		PresetMappings map[string][]Mapping `yaml:"presetMappings"`
	}

	require.NoError(t, yaml.Unmarshal(raw, &doc))
	require.Len(t, doc.PresetMappings["guardduty"], 2)

	return doc.PresetMappings["guardduty"]
}

func fixtureMappings(t *testing.T, file string) []Mapping {
	t.Helper()

	raw, err := os.ReadFile("testdata/" + file)
	require.NoError(t, err)

	var doc struct {
		Mappings []Mapping `yaml:"mappings"`
	}

	require.NoError(t, yaml.Unmarshal([]byte(fill.Replace(string(raw))), &doc))
	require.NotEmpty(t, doc.Mappings)

	return doc.Mappings
}

type sampleCase struct {
	name    string
	topic   string
	subject string
	payload string
}

var awsSamples = []sampleCase{
	{"guardduty-root-credential-usage", securityTopic, "", "guardduty-root-credential-usage.json"},
	{"guardduty-ec2-bitcoin", securityTopic, "", "guardduty-ec2-bitcoin.json"},
	{"budget", budgetsTopic, "AWS Budgets: monthly-total has exceeded your alert threshold", "budget-notification.txt"},
	{"cost-anomaly", anomaliesTopic, "AWS Cost Management: Cost anomaly detected on 2026-10-19", "cost-anomaly.json"},
	{"cloudwatch-alarm", alarmTopic, "ALARM: \"audit-writer-errors\" in EU (Frankfurt)", "cloudwatch-alarm.json"},
	{"unmapped", securityTopic, "", "unmapped-config-change.json"},
}

// sampleAlert runs one sample through the whole handler: signed envelope,
// allow-list, mappings (the preset first, as the chart orders them), render.
func sampleAlert(t *testing.T, c sampleCase) AlertmanagerAlert {
	t.Helper()

	raw, err := os.ReadFile("testdata/" + c.payload)
	require.NoError(t, err)

	fixture := newSigningFixture(t)
	am := newFakeAlertmanager(t)
	cfg := baseConfig()
	cfg.Topics = []string{securityTopic, budgetsTopic, anomaliesTopic, alarmTopic}
	cfg.Mappings = guarddutyPreset(t)
	cfg.Mappings = append(cfg.Mappings, fixtureMappings(t, "cost-mappings.yaml")...)
	cfg.Mappings = append(cfg.Mappings, fixtureMappings(t, "cloudwatch-mapping.yaml")...)
	cfg.Unmapped = Unmapped{Labels: map[string]string{"source": "unmapped"}}
	cfg.ResolveAfter = Duration(time.Hour)
	srv := newTestServer(t, fixture, cfg, am)

	env := fixture.sign(t, Envelope{
		Type:      "Notification",
		MessageID: "sample-" + c.name,
		TopicArn:  c.topic,
		Subject:   c.subject,
		Message:   fill.Replace(string(raw)),
		Timestamp: "2026-10-20T09:30:00.000Z",
	})

	resp := post(t, srv, env).Result()
	require.Equal(t, http.StatusOK, resp.StatusCode)
	require.Len(t, am.posts, 1)
	require.Len(t, am.posts[0], 1)

	return am.posts[0][0]
}

// unfill puts the placeholders back, so a golden file never carries an ARN
// or an account id (hack/leak-canary.sh).
var unfill = strings.NewReplacer(arnPrefix, "ARN_PREFIX", account, "ACCOUNT_ID")

func TestAWSSamples(t *testing.T) {
	for _, c := range awsSamples {
		t.Run(c.name, func(t *testing.T) {
			a := sampleAlert(t, c)

			got, err := json.MarshalIndent(struct {
				Labels      map[string]string `json:"labels"`
				Annotations map[string]string `json:"annotations"`
			}{a.Labels, a.Annotations}, "", "  ")
			require.NoError(t, err)

			out := unfill.Replace(string(got)) + "\n"
			path := filepath.Join("testdata", "alerts", c.name+".json")

			if *updateGolden {
				require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
				require.NoError(t, os.WriteFile(path, []byte(out), 0o644))
			}

			want, err := os.ReadFile(path)
			require.NoError(t, err, "run with -update to create it")
			assert.Equal(t, string(want), out, "the alert changed; review and run with -update")

			// The convention itself, whatever the golden says.
			assert.NotEmpty(t, a.Annotations["summary"])
			assert.NotContains(t, a.Annotations["summary"], "\n", "summary is one line")
			assert.NotEmpty(t, strings.TrimSpace(a.Annotations["description"]))

			for k, v := range a.Annotations {
				assert.NotContains(t, v, "<no value>", "annotation %s reads a field the sample does not have", k)
			}

			if c.name != "unmapped" {
				assert.True(t, strings.HasPrefix(a.Annotations["link"], "https://"), "link: %q", a.Annotations["link"])
				assert.NotEmpty(t, a.Annotations["link_name"])
			}
		})
	}
}

func TestGuardDutyDescriptionSaysWhatHappened(t *testing.T) {
	root := sampleAlert(t, awsSamples[0])
	assert.Equal(t, "RootCredentialUsage", root.Labels["alertname"])
	assert.Equal(t, "critical", root.Labels["severity"])

	d := root.Annotations["description"]
	for _, want := range []string{
		"API ListBuckets was invoked using root credentials from IP address 198.51.100.7.",
		"API call ListBuckets from 198.51.100.7 (Netherlands)",
		"Severity 2 (low) · Policy:IAMUser/RootCredentialUsage",
		"Resource: AccessKey ASIAEXAMPLEROOTKEY01 user Root (Root)",
		"Account " + account + " · eu-central-1 · count 3 · first seen 2026-10-20T08:01:12.000Z · last seen 2026-10-20T08:09:47.000Z",
	} {
		assert.Contains(t, d, want)
	}

	assert.Equal(t,
		"https://eu-central-1.console.aws.amazon.com/guardduty/home?region=eu-central-1#/findings?macros=current&fId=a2c9e0f6b1d84b2e9c7a3f5d6e8b1c20",
		root.Annotations["link"])
	assert.Equal(t, "GuardDuty finding", root.Annotations["link_name"])
	assert.Equal(t, "ListBuckets", root.Annotations["api"], "the existing annotations stay")
	assert.Equal(t, "198.51.100.7", root.Annotations["remote_ip"])

	ec2 := sampleAlert(t, awsSamples[1])
	assert.Equal(t, "CloudSecurityFinding", ec2.Labels["alertname"])
	assert.Equal(t, "critical", ec2.Labels["severity"])
	assert.Contains(t, ec2.Annotations["description"], "Severity 8 (high) · CryptoCurrency:EC2/BitcoinTool.B!DNS")
	assert.Contains(t, ec2.Annotations["description"], "Resource: Instance i-0abc1234def567890 (m5.large)")
	assert.NotContains(t, ec2.Annotations["description"], "API call", "no API-call line for a finding without one")
}

// A finding with none of the optional fields still renders: the preset reads
// them with `get`, so a sparse finding is a short description, never
// CloudEventUnmapped.
func TestGuardDutySparseFindingStillMaps(t *testing.T) {
	body := parseBody(`{"detail-type":"GuardDuty Finding","account":"a","region":"r","detail":{"id":"x","type":"T","title":"t","severity":5}}`)

	for _, m := range guarddutyPreset(t) {
		if !matchesMapping(m, body) {
			continue
		}

		_, ann, err := renderAlert(m.Alert, body)
		require.NoError(t, err)
		assert.Equal(t, "Severity 5 (medium) · T\nAccount a · r", strings.TrimSpace(ann["description"]))

		return
	}

	t.Fatal("no preset mapping matched")
}

func TestCostSamplesCarryTheFacts(t *testing.T) {
	budget := sampleAlert(t, awsSamples[2])
	assert.Equal(t, "CloudBudgetThreshold", budget.Labels["alertname"])
	assert.Contains(t, budget.Annotations["description"], "The ACTUAL Cost associated with this budget is $843.17.")
	assert.Equal(t, "https://us-east-1.console.aws.amazon.com/billing/home#/budgets/details?name=monthly-total", budget.Annotations["link"])

	anomaly := sampleAlert(t, awsSamples[3])
	d := anomaly.Annotations["description"]
	assert.Contains(t, d, "Impact: $1001.25 (334% over expected), 2026-10-17 – 2026-10-19")
	assert.Contains(t, d, "Root cause: Amazon Elastic Compute Cloud - Compute · workloads ("+account+") · eu-central-1 · EUC1-BoxUsage:r6g.2xlarge · $812.40")
	assert.Equal(t, 3, strings.Count(d, "Root cause:"), "the first three root causes, not all of them")
	assert.True(t, strings.HasPrefix(anomaly.Annotations["link"], "https://console.aws.amazon.com/cost-management/home#/anomaly-detection/"))
}

func TestCloudWatchDescriptionDropsTheSeverityToken(t *testing.T) {
	a := sampleAlert(t, awsSamples[4])
	assert.Equal(t, "Threshold Crossed: 1 out of the last 1 datapoints [3.0 (04/10/26 09:51:00)] was greater than or equal to the threshold (1.0) "+
		"(minimum 1 datapoint for OK -> ALARM transition).\nThe audit writer Lambda is failing.", a.Annotations["description"])
	assert.Equal(t, "https://eu-central-1.console.aws.amazon.com/cloudwatch/home?region=eu-central-1#alarmsV2:alarm/audit-writer-errors", a.Annotations["link"])
}

func TestUnmappedSampleSaysWhatArrived(t *testing.T) {
	a := sampleAlert(t, awsSamples[5])
	assert.Equal(t, "CloudEventUnmapped", a.Labels["alertname"])
	assert.Equal(t, "unmapped", a.Labels["source"])
	assert.Equal(t, "Unmapped cloud notification from topic security-alerts", a.Annotations["summary"])
	assert.Contains(t, a.Annotations["description"], "No mapping rule matched this notification from topic security-alerts.")
	assert.Contains(t, a.Annotations["description"], "\"detail-type\": \"Config Configuration Item Change\"", "the body, indented")
	assert.Empty(t, a.Annotations["link"])
}

// A mapping whose template fails names itself in the unmapped alert, so the
// person who sees it knows which mapping to fix.
func TestRenderFailureNamesTheMapping(t *testing.T) {
	fixture := newSigningFixture(t)
	am := newFakeAlertmanager(t)
	cfg := baseConfig()
	cfg.Topics = []string{securityTopic}
	cfg.Mappings = []Mapping{{
		Name:  "broken",
		Match: map[string]string{"detail-type": "*"},
		Alert: AlertSpec{Alertname: "X", Severity: "warning", Annotations: map[string]string{"summary": "{{ num .x }}"}},
	}}
	srv := newTestServer(t, fixture, cfg, am)

	env := fixture.sign(t, Envelope{Type: "Notification", MessageID: "m", TopicArn: securityTopic,
		Subject: "S", Message: `{"detail-type":"x","x":"not a number"}`, Timestamp: "2026-10-20T09:30:00.000Z"})
	require.Equal(t, http.StatusOK, post(t, srv, env).Code)
	require.Len(t, am.posts, 1)

	a := am.posts[0][0]
	assert.Equal(t, "CloudEventUnmapped", a.Labels["alertname"])
	assert.Equal(t, "Mapping broken failed to render: S", a.Annotations["summary"])
	assert.Contains(t, a.Annotations["description"], `Mapping "broken" matched but failed to render: `)
}

func TestGuarddutyResource(t *testing.T) {
	for name, tc := range map[string]struct {
		in   string
		want string
	}{
		"access key": {
			`{"resourceType":"AccessKey","accessKeyDetails":{"accessKeyId":"AKIAX","userName":"ci","userType":"IAMUser"}}`,
			"AccessKey AKIAX user ci (IAMUser)",
		},
		"instance": {`{"resourceType":"Instance","instanceDetails":{"instanceId":"i-1"}}`, "Instance i-1"},
		"buckets":  {`{"resourceType":"S3Bucket","s3BucketDetails":[{"name":"a"},{"name":"b"}]}`, "S3Bucket a, b"},
		"eks": {
			`{"resourceType":"EKSCluster","eksClusterDetails":{"name":"c1"},` +
				`"kubernetesDetails":{"kubernetesWorkloadDetails":{"name":"w","namespace":"ns"},"kubernetesUserDetails":{"username":"u"}}}`,
			"EKSCluster c1 workload ns/w user u",
		},
		"eks cluster only": {`{"resourceType":"EKSCluster","eksClusterDetails":{"name":"c1"}}`, "EKSCluster c1"},
		"ecs":              {`{"resourceType":"ECSCluster","ecsClusterDetails":{"name":"e"}}`, "ECSCluster e"},
		"container":        {`{"resourceType":"Container","containerDetails":{"name":"n","image":"img:1"}}`, "Container n image img:1"},
		"rds": {
			`{"resourceType":"RDSDBInstance","rdsDbInstanceDetails":{"dbInstanceIdentifier":"db"},"rdsDbUserDetails":{"user":"app"}}`,
			"RDSDBInstance db user app",
		},
		"limitless":    {`{"resourceType":"RDSLimitlessDB","rdsLimitlessDbDetails":{"dbShardGroupIdentifier":"g"}}`, "RDSLimitlessDB g"},
		"lambda":       {`{"resourceType":"Lambda","lambdaDetails":{"functionName":"f"}}`, "Lambda f"},
		"unknown type": {`{"resourceType":"S3Object"}`, "S3Object"},
		"no details":   {`{"resourceType":"Instance"}`, "Instance"},
		"empty":        {`{}`, ""},
	} {
		var r map[string]any
		require.NoError(t, json.Unmarshal([]byte(tc.in), &r), name)
		assert.Equal(t, tc.want, guarddutyResource(r), name)
	}

	assert.Empty(t, guarddutyResource(nil))
	assert.Empty(t, guarddutyResource("not a map"))
}

func TestGetPath(t *testing.T) {
	body := parseBody(`{"a":{"b":{"c":3,"n":null}},"s":"x"}`)
	assert.Equal(t, float64(3), getPath(body, "a.b.c"))
	assert.Equal(t, "x", getPath(body, "s"))
	assert.Equal(t, "", getPath(body, "a.b.missing"))
	assert.Equal(t, "", getPath(body, "a.b.n"), "a JSON null is absent")
	assert.Equal(t, "", getPath(body, "s.deeper"))
	assert.Equal(t, "", getPath("not a map", "a"))
}

func TestTruncateKeepsCharactersWhole(t *testing.T) {
	assert.Equal(t, "ab", truncate("ab", 2))
	assert.Equal(t, "a... (truncated)", truncate("aé", 2), "never half of a two-byte character")
}

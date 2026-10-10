package contracts_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/truvity/observability/pkg/contracts"
	"github.com/truvity/observability/pkg/dashboardlint"
	"github.com/truvity/observability/pkg/rulecheck"
	"github.com/truvity/observability/pkg/tenancy"
)

// The values are the wire contract: changing one is a breaking change for
// every component chart, so the literals are pinned here on purpose.
func TestWireValues(t *testing.T) {
	assert.Equal(t, "k8s_cluster_name", contracts.ClusterLabel)
	assert.Equal(t, "k8s_namespace_name", contracts.NamespaceLabel)
	assert.Equal(t, "source", contracts.SourceLabel)
	assert.Equal(t, "observability.truvity.io/evaluator", contracts.EvaluatorLabel)
	assert.Equal(t, "observability.truvity.io/rule-type", contracts.RuleTypeLabel)
	assert.Equal(t, []string{"metrics", "logs", "traces"}, contracts.Datasources())
	assert.Equal(t, "datasource", contracts.DatasourceVariable)
}

// One source: the older packages re-export, they do not restate.
func TestOldPackagesAgree(t *testing.T) {
	assert.Equal(t, contracts.ClusterLabel, tenancy.DefaultClusterLabel)
	assert.Equal(t, contracts.NamespaceLabel, tenancy.DefaultNamespaceLabel)
	assert.Equal(t, contracts.LogsClusterField, tenancy.DefaultLogsClusterField)
	assert.Equal(t, contracts.LogsNamespaceField, tenancy.DefaultLogsNamespaceField)
	assert.Equal(t, contracts.TracesClusterAttribute, tenancy.TracesClusterAttribute)
	assert.Equal(t, contracts.TracesNamespaceAttribute, tenancy.TracesNamespaceAttribute)
	assert.Equal(t, contracts.EnvironmentLabel, tenancy.EnvironmentLabel)
	assert.Equal(t, contracts.EnvironmentAttribute, tenancy.EnvironmentAttribute)
	assert.Equal(t, contracts.AudienceClaim, tenancy.AudienceClaim)
	assert.Equal(t, contracts.ClusterLabel, rulecheck.DefaultClusterLabel)
	_ = dashboardlint.Finding{} // dashboardlint reads contracts.DatasourceVariable directly
}

// docs/contracts.md carries the version the package reports.
func TestDocVersionMatches(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "contracts.md"))
	require.NoError(t, err)
	m := regexp.MustCompile(`(?m)^Contract version: (\d+\.\d+)\s*$`).FindSubmatch(b)
	require.NotNil(t, m, "docs/contracts.md has no `Contract version: X.Y` line")
	assert.Equal(t, contracts.Version, string(m[1]))
}

// Every exported name is documented, so a name added here without the
// human contract noticing fails.
func TestDocMentionsEveryKey(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "docs", "contracts.md"))
	require.NoError(t, err)
	doc := string(b)
	for _, s := range []string{
		contracts.ClusterLabel, contracts.NamespaceLabel, contracts.EnvironmentLabel, contracts.OwnerLabel, contracts.SourceLabel,
		contracts.LogsClusterField, contracts.LogsNamespaceField, contracts.TracesClusterAttribute,
		contracts.TracesNamespaceAttribute, contracts.EnvironmentAttribute,
		contracts.EvaluatorLabel, contracts.RuleTypeLabel, contracts.RuleTypeAlert, contracts.RuleTypeRecording,
		contracts.OwnerPlatform, contracts.OwnerComponent,
		contracts.DatasourceMetrics, contracts.DatasourceLogs, contracts.DatasourceTraces,
		string(contracts.IdentityHuman), string(contracts.IdentityMachine), string(contracts.IdentityStoreConnector),
	} {
		assert.Contains(t, doc, "`"+s+"`", "docs/contracts.md does not mention %q", s)
	}
}

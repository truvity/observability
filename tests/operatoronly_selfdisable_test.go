package tests

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestOperatorOnlySelfDisableIsByteIdentical is item 4 of 0.9.0
// "consumer simplification": `mode: operator-only` now turns vmauth,
// vmalert, alertmanager and metricsSelfScrape off ITSELF — their
// `enabled` fields default to `null`, which `mode` resolves — rather
// than refusing until the consumer writes `enabled: false` under each
// of them by hand.
//
// tests/cases/observability-stack/operator-only/values.yaml is the
// long-hand case: every one of the fifteen components `mode`'s own
// values.yaml comment names is turned off explicitly, exactly as a
// pre-0.9.0 consumer had to write it. tests/cases/observability-stack/
// operator-only-implicit/values.yaml is the SAME install, 0.9.0-short:
// only the four self-disabling fields are left unset, plus the five a
// real Helm subchart's own values (the three stores, `grafana.enabled`,
// `victoria-metrics-k8s-stack.syncJob.enabled`) that no parent template
// can compute — Helm coalesces a subchart's values before any template
// runs (see `mode`'s own values.yaml comment) — so those still have to
// be written by hand on both sides of 0.9.0.
//
// The two renders must be byte-identical: the short-hand case is not
// merely "a smaller values file that happens to also produce a working
// install", it is the SAME install.
func TestOperatorOnlySelfDisableIsByteIdentical(t *testing.T) {
	explicit, err := os.ReadFile("golden/observability-stack/operator-only.yaml")
	require.NoError(t, err)

	implicit, err := os.ReadFile("golden/observability-stack/operator-only-implicit.yaml")
	require.NoError(t, err)

	require.Equal(t, string(explicit), string(implicit),
		"mode: operator-only alone must render identically to writing out every explicit off")
}

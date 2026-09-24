// A label the cloud provider owns, on every series this chart writes.
//
// `labelmap` over `__meta_kubernetes_node_label_(.+)` is the conventional
// snippet for node scrapes, and it is unbounded by construction: it copies
// whatever labels the node happens to carry, and nobody here decides what
// those are. On EKS a node carries around forty
// (`eks_amazonaws_com_instance_*`, karpenter, topology), so every kubelet
// and cadvisor series arrived with 46 to 52 labels — past
// VictoriaMetrics' `-maxLabelsPerTimeseries=40`.
//
// What makes it worth a test rather than a fix is HOW it failed. The store
// ignored those series and answered 200. The agent reported 888k rows
// written, zero errors and zero dropped; the store held none of them; and
// the only record anywhere was a warning in the store's own log. Every
// counter on the writing side said success.
//
// So: no scrape config this chart renders may copy labels it has not
// named. Breadth is asked for by name, where somebody can count it.
package tests

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

type vmagentDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		InlineScrapeConfig string `yaml:"inlineScrapeConfig"`
	} `yaml:"spec"`
}

// TestNoScrapeConfigCopiesLabelsItDoesNotName walks the rendered agents
// and refuses an unbounded labelmap.
func TestNoScrapeConfigCopiesLabelsItDoesNotName(t *testing.T) {
	t.Parallel()

	goldens, err := filepath.Glob("golden/observability-emitters/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, goldens)

	var checked int

	for _, g := range goldens {
		for _, doc := range splitDocs(t, g) {
			var agent vmagentDoc
			if err := yaml.Unmarshal(doc, &agent); err != nil || agent.Kind != "VMAgent" {
				continue
			}

			if agent.Spec.InlineScrapeConfig == "" {
				continue
			}

			checked++

			assert.NotContainsf(t, agent.Spec.InlineScrapeConfig, "action: labelmap",
				"%s: VMAgent %s renders a `labelmap`, which copies labels this chart has not named. "+
					"On a cloud-provider node that is around forty of them, which puts every series past "+
					"the store's -maxLabelsPerTimeseries — where it is IGNORED and the write still "+
					"answers 200. Name the labels instead (metrics.scrape.nodeLabels)",
				g, agent.Metadata.Name)
		}
	}

	assert.Positive(t, checked, "no VMAgent with an inline scrape config was found in any golden")
	t.Logf("%d inline scrape configs checked", checked)
}

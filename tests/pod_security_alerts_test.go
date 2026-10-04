// The platform-alerts `podSecurity` group: the one LogsQL group, so the one
// that renders a VMRule of its own.
//
// The Event fields the expression reads (`k8s.event.reason`,
// `k8s.namespace.name`, `k8s.object.kind`, `k8s.object.name`,
// `k8s.cluster.name`) were read off a live log store that
// `observability-emitters`' `k8s_events` receiver writes to, and the
// whole pipeline was run against it with the message phrase swapped for one
// that has real events (see the pull request that added the group).
package tests

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

func TestPodSecurityAlertRendersAsALogsVMRule(t *testing.T) {
	var logs, metrics int

	for _, doc := range splitDocs(t, "golden/platform-alerts/pod-security-shared-store.yaml") {
		var rule struct {
			Kind     string `yaml:"kind"`
			Metadata struct {
				Labels map[string]string `yaml:"labels"`
			} `yaml:"metadata"`
			Spec struct {
				Groups []struct {
					Name  string `yaml:"name"`
					Type  string `yaml:"type"`
					Rules []struct {
						Alert       string            `yaml:"alert"`
						Expr        string            `yaml:"expr"`
						For         string            `yaml:"for"`
						Labels      map[string]string `yaml:"labels"`
						Annotations map[string]string `yaml:"annotations"`
					} `yaml:"rules"`
				} `yaml:"groups"`
			} `yaml:"spec"`
		}
		if yaml.Unmarshal(doc, &rule) != nil || rule.Kind != "VMRule" {
			continue
		}

		if rule.Metadata.Labels["observability.rule-type"] != "vlogs" {
			metrics++

			continue
		}

		logs++

		require.Len(t, rule.Spec.Groups, 1)
		g := rule.Spec.Groups[0]
		assert.Equal(t, "platform-alerts.pod-security", g.Name)
		assert.Equal(t, "vlogs", g.Type)
		require.Len(t, g.Rules, 1)

		r := g.Rules[0]
		assert.Equal(t, "PodSecurityAdmissionRejected", r.Alert)
		assert.Equal(t,
			`_time:15m k8s.event.reason:="FailedCreate" "violates PodSecurity"`+
				` | rename k8s.cluster.name as k8s_cluster_name, k8s.namespace.name as namespace,`+
				` k8s.object.kind as owner_kind, k8s.object.name as owner_name`+
				` | stats by (k8s_cluster_name, namespace, owner_kind, owner_name, owner) count() as rejections`,
			strings.Join(strings.Fields(r.Expr), " "))
		assert.Equal(t, "0s", r.For)
		assert.Equal(t, "warning", r.Labels["severity"])
		// keepClusterLabel: the Event's own cluster must survive, so the
		// estate's common label naming the evaluating cluster is left off.
		assert.NotContains(t, r.Labels, "k8s_cluster_name")
		assert.Equal(t, "platform", r.Labels["team"])
		assert.Contains(t, r.Annotations["description"], "securityContext")
		assert.Contains(t, r.Annotations["description"], "namespace")
	}

	assert.Equal(t, 1, logs, "exactly one LogsQL VMRule")
	assert.Zero(t, metrics, "no metrics VMRule when only the LogsQL group is on")
}

// The audit alert filters on the field `audit.violations`, which only a
// forwarded audit event carries (the reader's `transform/audit` sets it from
// the annotation). A free-text match on the annotation's name also matches a
// log line that merely mentions it, such as the reader's own startup log of
// its OTTL config: that line has the phrase but no `audit.violations` field.
func TestPodSecurityAuditAlertFiltersOnTheFieldNotThePhrase(t *testing.T) {
	var seen int

	for _, doc := range splitDocs(t, "golden/platform-alerts/pod-security-audit-shared-store.yaml") {
		var rule struct {
			Kind string `yaml:"kind"`
			Spec struct {
				Groups []struct {
					Rules []struct {
						Alert string `yaml:"alert"`
						Expr  string `yaml:"expr"`
					} `yaml:"rules"`
				} `yaml:"groups"`
			} `yaml:"spec"`
		}
		if yaml.Unmarshal(doc, &rule) != nil || rule.Kind != "VMRule" {
			continue
		}

		for _, g := range rule.Spec.Groups {
			for _, r := range g.Rules {
				if r.Alert != "PodSecurityAuditViolations" {
					continue
				}

				seen++

				expr := strings.Join(strings.Fields(r.Expr), " ")
				// A forwarded event (field set) matches.
				assert.Contains(t, expr, "audit.violations:*")
				// A collector startup line (phrase in the text, no field) does not.
				assert.NotContains(t, expr, "pod-security.kubernetes.io/audit-violations")
				assert.Equal(t,
					`_time:24h audit.violations:*`+
						` | rename k8s.cluster.name as k8s_cluster_name, audit.namespace as namespace`+
						` | stats by (k8s_cluster_name, namespace) count() as violations`,
					expr)
			}
		}
	}

	assert.Equal(t, 1, seen)
}

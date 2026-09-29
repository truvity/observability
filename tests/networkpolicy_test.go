// The proxy's own NetworkPolicy selects the VMAuth pod and locks it down
// to exactly the ports this chart names — which is also every port the
// vm-operator's own sidecars listen on, whether this chart named them or
// not. The operator injects a config-reloader into that same pod to
// watch the Secret it generates for VMAuth and signal a reload, and
// renders a second endpoint for it, `reloader-http`, on the
// VMServiceScrape it manages alongside this chart's objects. A policy
// that selects the pod and does not admit that endpoint drops every
// sample at admission — the socket answers, the packet never lands —
// which reads as `up=0` and a permanently firing TargetDown/ServiceDown
// on a pod that is otherwise perfectly healthy. See
// charts/observability-stack/templates/networkpolicy.yaml and
// docs/safety.md, "A store whose own policy hides it from the scraper",
// for the shape this repeats one port later.
//
// This proves the fix against every golden the proxy's NetworkPolicy is
// rendered into, rather than against the template's source text, so a
// future edit that reopens the gap fails here first.
package tests

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.yaml.in/yaml/v3"
)

// networkPolicyDoc is the minimum needed to inspect one rendered
// NetworkPolicy's ingress rules.
type networkPolicyDoc struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name string `yaml:"name"`
	} `yaml:"metadata"`
	Spec struct {
		Ingress []struct {
			From  []map[string]any `yaml:"from"`
			Ports []struct {
				Protocol string `yaml:"protocol"`
				Port     int    `yaml:"port"`
			} `yaml:"ports"`
		} `yaml:"ingress"`
	} `yaml:"spec"`
}

// TestProxyPolicyAdmitsReloaderScrape is the check the permanently firing
// TargetDown/ServiceDown on `vmauth-...-reloader-http` needed: whatever
// peers `networkPolicy.scrapeFrom` names (defaulting to vmagent, the same
// default the three store policies fall back to) must also be admitted
// to the proxy pod on 8435, the config-reloader sidecar's port.
func TestProxyPolicyAdmitsReloaderScrape(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, goldens)

	var seen int

	for _, g := range goldens {
		for _, doc := range splitDocs(t, g) {
			var np networkPolicyDoc
			if err := yaml.Unmarshal(doc, &np); err != nil || np.Kind != "NetworkPolicy" {
				continue
			}

			if !strings.HasSuffix(np.Metadata.Name, "-proxy") {
				continue
			}

			seen++

			t.Run(filepath.Base(g)+"/"+np.Metadata.Name, func(t *testing.T) {
				var (
					reloaderRule  = -1
					reloaderPeers []map[string]any
				)

				for i, rule := range np.Spec.Ingress {
					for _, p := range rule.Ports {
						if p.Port == 8435 {
							reloaderRule = i
							reloaderPeers = rule.From
						}
					}
				}

				require.NotEqualf(t, -1, reloaderRule,
					"%s: %s admits nothing on 8435 — the operator's config-reloader sidecar for this "+
						"VMAuth pod is scraped on that port and every sample is dropped at the policy, "+
						"not the socket: up=0 and TargetDown/ServiceDown fire forever on a healthy pod.",
					g, np.Metadata.Name)

				assert.NotEmptyf(t, reloaderPeers,
					"%s: %s admits 8435 to nobody (an empty `from` list denies all traffic on that rule)",
					g, np.Metadata.Name)

				// The default this chart falls back to when
				// `networkPolicy.scrapeFrom` is unset — none of the
				// fixtures under tests/cases/observability-stack override
				// it, so every golden's reloader rule must show exactly
				// this peer.
				want := map[string]any{
					"podSelector": map[string]any{
						"matchLabels": map[string]any{
							"app.kubernetes.io/name": "vmagent",
						},
					},
				}

				assert.Containsf(t, reloaderPeers, want,
					"%s: %s's 8435 rule does not admit the default scrape peer "+
						"(app.kubernetes.io/name: vmagent) that networkPolicy.scrapeFrom falls back to",
					g, np.Metadata.Name)
			})
		}
	}

	assert.Positive(t, seen, "no proxy NetworkPolicy found in any observability-stack golden; "+
		"this check went blind rather than passing")
}

// TestVmalertPolicyAdmitsReloaderScrape is the same check, one object
// over: vmalert pods carry the identical config-reloader sidecar
// (`reloader-http`, port 8435), and `templates/networkpolicy.yaml`'s
// `<fullname>-vmalert` NetworkPolicy selected the pod without admitting
// it — a policy present for as long as `tenancy.alertReaders` existed,
// and wrong for as long as it existed, since it admits vmauth and the
// scrape peer on 8080 (vmalert's own API and `/metrics`) and stops
// there. Every rule in `charts/platform-alerts` and this chart's own
// `selfAlerts` that watches this job's own scrape health depends on the
// same sample the proxy policy was dropping at admission one port over
// (docs/safety.md, "A store whose own policy hides it from the
// scraper").
func TestVmalertPolicyAdmitsReloaderScrape(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, goldens)

	var seen int

	for _, g := range goldens {
		for _, doc := range splitDocs(t, g) {
			var np networkPolicyDoc
			if err := yaml.Unmarshal(doc, &np); err != nil || np.Kind != "NetworkPolicy" {
				continue
			}

			if !strings.HasSuffix(np.Metadata.Name, "-vmalert") {
				continue
			}

			seen++

			t.Run(filepath.Base(g)+"/"+np.Metadata.Name, func(t *testing.T) {
				var (
					reloaderRule  = -1
					reloaderPeers []map[string]any
					metricsRule   = -1
					metricsPeers  []map[string]any
				)

				for i, rule := range np.Spec.Ingress {
					for _, p := range rule.Ports {
						switch p.Port {
						case 8435:
							reloaderRule = i
							reloaderPeers = rule.From
						case 8080:
							// More than one 8080 rule exists (vmauth,
							// then the scrape peer); the one this
							// check cares about is whichever is NOT
							// vmauth's own — asserted separately below.
							if !containsVMAuth(rule.From) {
								metricsRule = i
								metricsPeers = rule.From
							}
						}
					}
				}

				require.NotEqualf(t, -1, reloaderRule,
					"%s: %s admits nothing on 8435 — the operator's config-reloader sidecar for this "+
						"VMAlert pod is scraped on that port and every sample is dropped at the policy, "+
						"not the socket: up=0 and TargetDown/ServiceDown fire forever on a healthy pod.",
					g, np.Metadata.Name)
				assert.NotEmptyf(t, reloaderPeers,
					"%s: %s admits 8435 to nobody (an empty `from` list denies all traffic on that rule)",
					g, np.Metadata.Name)

				require.NotEqualf(t, -1, metricsRule,
					"%s: %s has no scrape-peer rule on 8080 (distinct from vmauth's own) — vmalert's "+
						"own /metrics would never be scraped either", g, np.Metadata.Name)

				// The default this chart falls back to when
				// `networkPolicy.scrapeFrom` is unset — the same
				// default (and the same Go-template variable) the
				// store and proxy policies fall back to, never a
				// second, separately hard-coded one.
				want := map[string]any{
					"podSelector": map[string]any{
						"matchLabels": map[string]any{
							"app.kubernetes.io/name": "vmagent",
						},
					},
				}

				assert.Containsf(t, reloaderPeers, want,
					"%s: %s's 8435 rule does not admit the default scrape peer "+
						"(app.kubernetes.io/name: vmagent) that networkPolicy.scrapeFrom falls back to",
					g, np.Metadata.Name)
				assert.Equalf(t, metricsPeers, reloaderPeers,
					"%s: %s admits a different peer set on 8080 (scrape) than on 8435 (reloader) — "+
						"networkPolicy.scrapeFrom should gate both the same way", g, np.Metadata.Name)
			})
		}
	}

	assert.Positive(t, seen, "no vmalert NetworkPolicy found in any observability-stack golden; "+
		"this check went blind rather than passing")
}

// containsVMAuth reports whether a `from` list is the vmauth-only peer
// `templates/networkpolicy.yaml` renders on the vmalert policy's own
// first 8080 rule (for `tenancy.alertReaders`), so the scrape peer's
// own 8080 rule can be told apart from it.
func containsVMAuth(from []map[string]any) bool {
	for _, peer := range from {
		sel, ok := peer["podSelector"].(map[string]any)
		if !ok {
			continue
		}
		labels, ok := sel["matchLabels"].(map[string]any)
		if !ok {
			continue
		}
		if labels["app.kubernetes.io/name"] == "vmauth" {
			return true
		}
	}
	return false
}

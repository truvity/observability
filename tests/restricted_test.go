// Every pod this chart's renders create meets the Pod Security `restricted`
// profile, except the node-level DaemonSets that cannot.
//
// The profile is what a namespace enforces when it says `restricted`; a
// workload that does not meet it is not rejected until that label arrives,
// which is exactly when nobody is looking at this chart. The check is made
// against the goldens, so every shape the chart renders is covered, and
// against the pod template (or, for an object the VictoriaMetrics operator
// builds the pod from, the one `securityContext` it inlines into the pod and
// into every container it adds).
//
// Not covered, on purpose: DaemonSets. The node exporter reads the host and
// the log collector reads its files; neither can be `restricted`.
package tests

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// containersOf returns the init and regular containers of a pod spec.
func containersOf(spec any) []map[string]any {
	var out []map[string]any
	for _, key := range []string{"initContainers", "containers"} {
		list, _ := dig(spec, key).([]any)
		for _, c := range list {
			if m, ok := c.(map[string]any); ok {
				out = append(out, m)
			}
		}
	}
	return out
}

// assertRestrictedPod holds a pod spec to the profile. `runAsNonRoot` and the
// seccomp profile count wherever the profile accepts them: on the pod, or on
// every one of its containers.
func assertRestrictedPod(t *testing.T, where string, spec any) {
	t.Helper()
	psc := dig(spec, "securityContext")
	containers := containersOf(spec)
	for _, c := range containers {
		csc := dig(c, "securityContext")
		assert.Equalf(t, false, dig(csc, "allowPrivilegeEscalation"), "%s: container %v: allowPrivilegeEscalation is not false", where, c["name"])
		drop, _ := dig(csc, "capabilities", "drop").([]any)
		assert.Equalf(t, []any{"ALL"}, drop, "%s: container %v: capabilities.drop is not [ALL]", where, c["name"])
	}
	everyContainer := func(path ...string) func() bool {
		return func() bool {
			for _, c := range containers {
				if dig(dig(c, "securityContext"), path...) == nil {
					return false
				}
			}
			return len(containers) > 0
		}
	}
	if dig(psc, "runAsNonRoot") != true {
		assert.Truef(t, everyContainer("runAsNonRoot")(), "%s: neither the pod nor every container sets runAsNonRoot", where)
	}
	if dig(psc, "seccompProfile", "type") != "RuntimeDefault" {
		assert.Truef(t, everyContainer("seccompProfile", "type")(), "%s: neither the pod nor every container sets a RuntimeDefault seccomp profile", where)
	}
}

func TestEveryRenderedPodMeetsPodSecurityRestricted(t *testing.T) {
	var goldens []string
	for _, chart := range []string{"observability-stack", "observability-emitters"} {
		g, err := filepath.Glob("golden/" + chart + "/*.yaml")
		require.NoError(t, err)
		goldens = append(goldens, g...)
	}
	require.NotEmpty(t, goldens)

	var pods, operatorObjects int

	for _, g := range goldens {
		for _, d := range renderedDocs(t, g) {
			where := g + ": " + d["kind"].(string) + "/" + asString(dig(d, "metadata", "name"))
			switch d["kind"] {
			case "Deployment", "StatefulSet", "Job":
				pods++
				assertRestrictedPod(t, where, dig(d, "spec", "template", "spec"))
			case "CronJob":
				pods++
				assertRestrictedPod(t, where, dig(d, "spec", "jobTemplate", "spec", "template", "spec"))
			case "VMAlert", "VMAuth", "VMAlertmanager", "VMAgent", "VMSingle":
				operatorObjects++
				sc := dig(d, "spec", "securityContext")
				assert.Equalf(t, true, dig(sc, "runAsNonRoot"), "%s: spec.securityContext.runAsNonRoot is not true", where)
				assert.Equalf(t, "RuntimeDefault", dig(sc, "seccompProfile", "type"), "%s: spec.securityContext.seccompProfile is not RuntimeDefault", where)
				assert.Equalf(t, false, dig(sc, "allowPrivilegeEscalation"), "%s: spec.securityContext.allowPrivilegeEscalation is not false", where)
				drop, _ := dig(sc, "capabilities", "drop").([]any)
				assert.Equalf(t, []any{"ALL"}, drop, "%s: spec.securityContext.capabilities.drop is not [ALL]", where)
			}
		}
	}

	// A check that found nothing proves nothing.
	require.Positive(t, pods, "no workload in any golden; this test would pass vacuously")
	require.Positive(t, operatorObjects, "no operator-managed object in any golden; this test would pass vacuously")
}

func asString(v any) string {
	s, _ := v.(string)
	return s
}

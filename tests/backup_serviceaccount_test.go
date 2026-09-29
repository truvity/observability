// Every backup pod's ServiceAccount exists, and every backup
// ServiceAccount is somebody's.
//
// Under `backup.auth.mode: ambient` a backup job's ONLY credential is the
// ServiceAccount its pod runs as: IRSA reads the role from that SA's
// annotation, EKS Pod Identity keys its association on that SA's name. So
// the two ways this goes wrong are both silent until the first run:
//
//   - a CronJob naming a ServiceAccount the render does not contain — the
//     Job's pods are never created ("serviceaccount not found"), which
//     `CronJobNotSucceeding` sees only after its own window;
//   - a rendered backup ServiceAccount no CronJob runs as — with 0.10.0's
//     per-store ServiceAccounts, the release-wide one carries a role that
//     nothing assumes once every store has its own, which is exactly the
//     over-broad grant least-privilege backups exist to remove.
//
// The goldens only ever reference ServiceAccounts they render (an
// existing one, `create: false`, is the estate's to provide), so both
// directions are checked against every golden.
package tests

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestEveryBackupServiceAccountIsRenderedAndUsed(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)
	require.NotEmpty(t, goldens)

	var checked, perStore int

	for _, g := range goldens {
		rendered := map[string]map[string]any{} // backup SA name -> the SA
		used := map[string]bool{}
		for _, d := range renderedDocs(t, g) {
			name, _ := dig(d, "metadata", "name").(string)
			switch d["kind"] {
			case "ServiceAccount":
				if strings.Contains(name, "backup") {
					rendered[name] = d
				}
			case "CronJob":
				if dig(d, "metadata", "labels", "observability.backup") == nil {
					continue
				}
				sa, _ := dig(d, "spec", "jobTemplate", "spec", "template", "spec", "serviceAccountName").(string)
				if sa == "" {
					continue // `secret` mode: no chosen ServiceAccount at all
				}
				checked++
				used[sa] = true
				assert.Containsf(t, rendered, sa,
					"%s: backup CronJob %s runs as ServiceAccount %q, which this render does not contain", g, name, sa)
			}
		}
		for name, sa := range rendered {
			assert.Truef(t, used[name],
				"%s: backup ServiceAccount %q is rendered but no backup CronJob runs as it — a role nothing assumes", g, name)
			if dig(sa, "metadata", "labels", "observability.backup") != nil {
				perStore++
			}
		}
	}

	// A check that found nothing to check proves nothing.
	require.Positive(t, checked, "no backup CronJob with a serviceAccountName in any golden; this test would pass vacuously")
	require.Positive(t, perStore, "no per-store backup ServiceAccount in any golden; the per-store cases are missing")
}

// The per-store golden is the least-privilege shape: three stores, three
// ServiceAccounts, three roles, and no two stores sharing one.
func TestPerStoreBackupServiceAccountsCarryTheirOwnRole(t *testing.T) {
	roles := map[string]string{} // CronJob -> the role annotation on its SA
	docs := renderedDocs(t, "golden/observability-stack/backup-ambient-per-store.yaml")

	sas := map[string]map[string]any{}
	for _, d := range docs {
		if d["kind"] == "ServiceAccount" {
			sas[dig(d, "metadata", "name").(string)] = d
		}
	}
	for _, d := range docs {
		if d["kind"] != "CronJob" || dig(d, "metadata", "labels", "observability.backup") == nil {
			continue
		}
		sa := dig(d, "spec", "jobTemplate", "spec", "template", "spec", "serviceAccountName").(string)
		role, _ := dig(sas[sa], "metadata", "annotations", "eks.amazonaws.com/role-arn").(string)
		require.NotEmptyf(t, role, "backup CronJob %s runs as %q, which carries no role annotation", dig(d, "metadata", "name"), sa)
		roles[dig(d, "metadata", "labels", "observability.backup").(string)+"/"+dig(d, "metadata", "name").(string)] = role
	}

	byStore := map[string]string{}
	for key, role := range roles {
		store := strings.SplitN(key, "/", 2)[0]
		if prior, ok := byStore[store]; ok {
			assert.Equalf(t, prior, role, "two jobs of store %s run under different roles", store)
		}
		byStore[store] = role
	}
	require.Len(t, byStore, 3, "expected metrics, logs and traces backups in the per-store golden")

	seen := map[string]string{}
	for store, role := range byStore {
		if other, dup := seen[role]; dup {
			assert.Failf(t, "two stores share a role", "%s and %s both run under %q", store, other, role)
		}
		seen[role] = store
	}
}

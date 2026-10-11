package tests

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The reconcile script is run for real (sh + curl) against a fake Grafana
// that answers the way Grafana 13 does: /api/users/lookup carries only
// `authLabels`, /api/users/:id carries `isExternallySynced`, and a role change
// of an externally synced user is a 403. The org role is NOT in the lookups
// (`/api/org/users/lookup` is a UserLookupDTO without `role`): only
// /api/users/:id/orgs carries it.

type fakeGrafana struct {
	mu       sync.Mutex
	exists   bool
	external bool
	role     string
	reqs     []string
}

const fakeUID = 7

func (f *fakeGrafana) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, r.Method+" "+r.URL.Path)
	w.Header().Set("Content-Type", "application/json")
	reply := func(code int, body string) { w.WriteHeader(code); _, _ = fmt.Fprint(w, body) }
	switch {
	case r.URL.Path == "/api/health":
		reply(200, `{"database":"ok"}`)
	case r.URL.Path == "/api/folders/c1":
		reply(200, `{"uid":"c1"}`)
	case r.Method == "GET" && r.URL.Path == "/api/users/lookup":
		if !f.exists {
			reply(404, `{"message":"user not found"}`)
			return
		}
		reply(200, fmt.Sprintf(`{"id":%d,"login":"x","authLabels":[]}`, fakeUID))
	case r.Method == "POST" && r.URL.Path == "/api/admin/users":
		f.exists, f.role = true, "Viewer"
		reply(200, fmt.Sprintf(`{"id":%d,"message":"User created"}`, fakeUID))
	case r.Method == "GET" && r.URL.Path == fmt.Sprintf("/api/users/%d", fakeUID):
		reply(200, fmt.Sprintf(`{"id":%d,"isExternal":%t,"isExternallySynced":%t,"isGrafanaAdminExternallySynced":false}`, fakeUID, f.external, f.external))
	case r.Method == "GET" && r.URL.Path == "/api/org/users/lookup":
		// UserLookupDTO: userId, login, avatarUrl. NO role.
		reply(200, fmt.Sprintf(`[{"userId":99,"login":"other","avatarUrl":"/avatar/a"},{"userId":%d,"login":"x","avatarUrl":"/avatar/b"}]`, fakeUID))
	case r.Method == "GET" && r.URL.Path == fmt.Sprintf("/api/users/%d/orgs", fakeUID):
		// UserOrgDTO list (pkg/api GetUserOrgList): orgId, name, role.
		reply(200, fmt.Sprintf(`[{"orgId":1,"name":"Main Org.","role":"%s"},{"orgId":2,"name":"Zed","role":"Admin"}]`, f.role))
	case r.Method == "PATCH" && r.URL.Path == fmt.Sprintf("/api/org/users/%d", fakeUID):
		if f.external {
			reply(403, `{"message":"Cannot change role for externally synced user"}`)
			return
		}
		f.role = "None"
		reply(200, `{"message":"Organization user updated"}`)
	case r.Method == "POST" && r.URL.Path == fmt.Sprintf("/api/access-control/folders/c1/users/%d", fakeUID):
		reply(200, `{"message":"Permissions updated"}`)
	default:
		reply(404, `{"message":"unexpected"}`)
	}
}

func runOperatorFolders(t *testing.T, f *fakeGrafana) (string, error) {
	t.Helper()
	srv := httptest.NewServer(f)
	defer srv.Close()
	script, err := filepath.Abs(filepath.Join("..", "charts", "observability-grafana", "files", "operator-folders.sh"))
	require.NoError(t, err)
	cmd := exec.Command("sh", script)
	cmd.Env = append(os.Environ(),
		"GRAFANA_URL="+srv.URL, "GRAFANA_ADMIN_USER=admin", "GRAFANA_ADMIN_PASSWORD=hunter2",
		"AUTH_FILE="+filepath.Join(t.TempDir(), "auth"),
		"CLUSTERS=c1", "SUBJECT_SUFFIX=:op", "PRUNE=false")
	out, err := cmd.CombinedOutput()
	assert.NotContains(t, string(out), "hunter2")
	return string(out), err
}

func (f *fakeGrafana) did(req string) bool {
	for _, r := range f.reqs {
		if r == req {
			return true
		}
	}
	return false
}

func TestOperatorFoldersScript(t *testing.T) {
	patch := fmt.Sprintf("PATCH /api/org/users/%d", fakeUID)
	edit := fmt.Sprintf("POST /api/access-control/folders/c1/users/%d", fakeUID)

	t.Run("missing user is created, set to None, granted Edit", func(t *testing.T) {
		f := &fakeGrafana{}
		out, err := runOperatorFolders(t, f)
		require.NoError(t, err, out)
		assert.True(t, f.did("POST /api/admin/users"))
		assert.True(t, f.did(patch))
		assert.True(t, f.did(edit))
		assert.Equal(t, "None", f.role)
	})

	t.Run("local user keeps the role PATCH", func(t *testing.T) {
		f := &fakeGrafana{exists: true, role: "Viewer"}
		out, err := runOperatorFolders(t, f)
		require.NoError(t, err, out)
		assert.False(t, f.did("POST /api/admin/users"))
		assert.True(t, f.did(patch))
		assert.True(t, f.did(edit))
		assert.Equal(t, "None", f.role)
	})

	t.Run("externally synced user with role None: no PATCH, Edit granted", func(t *testing.T) {
		f := &fakeGrafana{exists: true, external: true, role: "None"}
		out, err := runOperatorFolders(t, f)
		require.NoError(t, err, out)
		assert.False(t, f.did(patch), "Grafana would answer 403")
		assert.True(t, f.did(edit))
		assert.Contains(t, out, "cluster c1 reconciled")
	})

	t.Run("externally synced user with role Viewer: the cluster fails, no Edit", func(t *testing.T) {
		f := &fakeGrafana{exists: true, external: true, role: "Viewer"}
		out, err := runOperatorFolders(t, f)
		require.Error(t, err, out)
		assert.False(t, f.did(patch))
		assert.False(t, f.did(edit), "never grant Edit to a Viewer")
		assert.Contains(t, out, "externally synced with role 'Viewer'")
		assert.True(t, strings.Contains(out, "noRoleSubjectSuffixes"))
	})
}

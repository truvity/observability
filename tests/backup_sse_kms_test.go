// An SSE-KMS bucket's S3 ETags are not MD5s. rclone, told nothing, reads
// them as MD5s, and the logs/traces jobs' `--checksum` then fails every
// single-part file as "corrupted on transfer: md5 hashes differ" on every
// run. `backup.serverSideEncryption` tells rclone, and only rclone: the
// metrics job (vmbackup) must never get the variables, and a render that
// does not set the value must not carry them either.
package tests

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func backupEnv(t *testing.T, d map[string]any) map[string]string {
	t.Helper()
	cs, _ := dig(d, "spec", "jobTemplate", "spec", "template", "spec", "containers").([]any)
	require.Len(t, cs, 1)
	env := map[string]string{}
	vars, _ := cs[0].(map[string]any)["env"].([]any)
	for _, e := range vars {
		m := e.(map[string]any)
		v, _ := m["value"].(string)
		env[m["name"].(string)] = v
	}
	return env
}

func TestBackupServerSideEncryptionReachesOnlyRclone(t *testing.T) {
	goldens, err := filepath.Glob("golden/observability-stack/*.yaml")
	require.NoError(t, err)

	var sawKMS bool
	for _, g := range goldens {
		for _, d := range renderedDocs(t, g) {
			if d["kind"] != "CronJob" || dig(d, "metadata", "labels", "observability.backup") == nil {
				continue
			}
			store, _ := dig(d, "metadata", "labels", "observability.backup").(string)
			env := backupEnv(t, d)
			_, sse := env["RCLONE_S3_SERVER_SIDE_ENCRYPTION"]
			_, key := env["RCLONE_S3_SSE_KMS_KEY_ID"]
			if filepath.Base(g) != "backup-sse-kms.yaml" {
				assert.Falsef(t, sse || key, "%s: %s job carries SSE settings the case never set", g, store)
				continue
			}
			if store == "metrics" {
				assert.Falsef(t, sse || key, "%s: vmbackup must not get rclone settings", g)
				continue
			}
			sawKMS = true
			assert.Equalf(t, "aws:kms", env["RCLONE_S3_SERVER_SIDE_ENCRYPTION"], "%s: %s job", g, store)
			assert.Equalf(t, "alias/example-backups", env["RCLONE_S3_SSE_KMS_KEY_ID"], "%s: %s job", g, store)
		}
	}
	assert.True(t, sawKMS, "backup-sse-kms.yaml rendered no logs/traces backup job")
}

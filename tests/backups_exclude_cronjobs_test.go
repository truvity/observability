package tests

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestExcludeCronjobsMatchers pins where groups.backups.excludeCronjobs
// lands: the CronJob-name selectors of CronJobNotSucceeding, and the
// kube_job_owner selector of BackupJobFailed (the one place that rule ties a
// Job to its CronJob, so excluding there removes the Job from the join and
// from the result). With the value empty, nothing is rendered.
func TestExcludeCronjobsMatchers(t *testing.T) {
	const cron = `cronjob!~".*-pg-restore$"`
	const owner = `owner_name!~".*-pg-restore$"`

	age, _ := backupsRule(t, "golden/platform-alerts/exclude-cronjobs.yaml", "CronJobNotSucceeding")
	assert.Contains(t, age, `kube_cronjob_status_last_successful_time{namespace=~".*", `+cron+`}`)
	assert.Contains(t, age, `kube_cronjob_spec_suspend{namespace=~".*", `+cron+`}`)

	failed, _ := backupsRule(t, "golden/platform-alerts/exclude-cronjobs.yaml", "BackupJobFailed")
	assert.Contains(t, failed, `kube_job_owner{namespace=~".*", owner_kind="CronJob", `+owner+`}`)
	assert.NotContains(t, failed, cron)

	for _, alert := range []string{"CronJobNotSucceeding", "BackupJobFailed"} {
		expr, _ := backupsRule(t, "golden/platform-alerts/minimal.yaml", alert)
		assert.NotContains(t, expr, "!~", alert+" must render no exclusion by default")
	}
}

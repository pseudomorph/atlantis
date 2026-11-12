// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"context"
	"encoding/json"
	"io"
	"io/fs"
	"testing"
	"time"

	"github.com/pressly/goose/v3"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
	"gorm.io/gorm"
)

// databaseAt creates the database at an earlier schema version, as an older
// release would have left it, and runs fill against it before closing.
func databaseAt(t *testing.T, d TestDatabase, version int64, fill func(*gorm.DB)) {
	t.Helper()
	orm := openUnmanaged(t, d)
	conn, err := orm.DB()
	Ok(t, err)
	defer conn.Close()
	files, err := fs.Sub(migrationFiles, "migrations")
	Ok(t, err)
	provider, err := goose.NewProvider(d.gooseDialect(), conn, files)
	Ok(t, err)
	_, err = provider.UpTo(context.Background(), version)
	Ok(t, err)
	current, err := provider.GetDBVersion(context.Background())
	Ok(t, err)
	Equals(t, version, current)
	fill(orm)
}

// upgradePull is the pull every upgrade test writes with the older schema.
func upgradePull() models.PullRequest {
	p := claimPull()
	p.URL, p.Author = "https://git.example/team/infra/-/merge_requests/17", "author"
	return p
}

// writeLocksAndStatus inserts the rows version 1 stores for a locked project
// with a plan status: identity rows, a project lock, pull and project status,
// and a global command lock.
func writeLocksAndStatus(t *testing.T, db *gorm.DB, p models.PullRequest, lockedAt time.Time) {
	t.Helper()
	i := p.BaseRepo.StorageIdentity()
	Ok(t, db.Exec("INSERT INTO vcs_instances(id, host, port, base_path, provider) VALUES ('instance', ?, ?, ?, ?)", i.Host, i.Port, i.BasePath, p.BaseRepo.VCSHost.Type).Error)
	Ok(t, db.Exec("INSERT INTO repositories(id, instance_id, full_name, clone_url) VALUES ('repository', 'instance', ?, ?)", p.BaseRepo.FullName, p.BaseRepo.CloneURL).Error)
	Ok(t, db.Exec("INSERT INTO pulls(id, repository_id, number, head_commit, url, head_branch, base_branch, author, state) VALUES ('pull', 'repository', ?, ?, ?, ?, ?, ?, ?)",
		p.Num, p.HeadCommit, p.URL, p.HeadBranch, p.BaseBranch, p.Author, models.OpenPullState).Error)
	Ok(t, db.Exec("INSERT INTO projects(id, repository_id, directory, name, workspace) VALUES ('project', 'repository', '.', '', 'default')").Error)
	Ok(t, db.Exec("INSERT INTO locks(project_id, repository_id, pull_id, revision, username, teams, created_at) VALUES ('project', 'repository', 'pull', ?, 'locker', '[\"admins\"]', ?)", p.HeadCommit, databaseTime(lockedAt)).Error)
	Ok(t, db.Exec("INSERT INTO pull_statuses(pull_id, revision, base_branch) VALUES ('pull', ?, ?)", p.HeadCommit, p.BaseBranch).Error)
	Ok(t, db.Exec("INSERT INTO project_statuses(pull_id, project_id, repository_id, position, status, policies, plan_hash) VALUES ('pull', 'project', 'repository', 0, ?, 'null', 'hash')", models.PlannedPlanStatus).Error)
	Ok(t, db.Exec("INSERT INTO command_locks(command, unix_time) VALUES (1, 123)").Error)
}

func TestUpgradeFromLocksAndStatus(t *testing.T) {
	ForEachDatabase(t, func(t *testing.T, d TestDatabase) {
		p := upgradePull()
		lockedAt := time.Date(2026, 1, 2, 3, 4, 5, 6000, time.UTC)
		databaseAt(t, d, 1, func(db *gorm.DB) { writeLocksAndStatus(t, db, p, lockedAt) })

		s, err := d.Open()
		Ok(t, err)
		defer s.Close()

		locks, err := s.List()
		Ok(t, err)
		Equals(t, 1, len(locks))
		Equals(t, p.Num, locks[0].Pull.Num)
		Equals(t, models.User{Username: "locker", Teams: []string{"admins"}}, locks[0].User)
		Assert(t, locks[0].Time.Equal(lockedAt), "lock time changed to %s", locks[0].Time)
		status, err := s.GetPullStatus(p)
		Ok(t, err)
		Equals(t, 1, len(status.Projects))
		Equals(t, models.PlannedPlanStatus, status.Projects[0].Status)
		Equals(t, "hash", status.Projects[0].PlanHash)

		var lifecycle struct {
			Closed, Cancelled, CleanupComplete, ReopenRequested bool
			ClosedAt                                            *time.Time
			ClosureGeneration                                   int64
		}
		Ok(t, s.db.Table("pulls").Where("id = ?", "pull").Take(&lifecycle).Error)
		Assert(t, !lifecycle.Closed && !lifecycle.Cancelled && !lifecycle.CleanupComplete && !lifecycle.ReopenRequested && lifecycle.ClosedAt == nil && lifecycle.ClosureGeneration == 0, "existing pull is not open: %+v", lifecycle)

		// The lifecycle checks added with the columns hold for existing rows.
		for _, invalid := range []string{
			"UPDATE pulls SET closed = TRUE, closed_at = CURRENT_TIMESTAMP WHERE id = 'pull'",
			"UPDATE pulls SET cancelled = TRUE, closed = TRUE WHERE id = 'pull'",
			"UPDATE pulls SET cancelled = TRUE, cleanup_complete = TRUE WHERE id = 'pull'",
			"UPDATE pulls SET cancelled = TRUE, reopen_requested = TRUE WHERE id = 'pull'",
			"UPDATE pulls SET closure_generation = -1 WHERE id = 'pull'",
		} {
			checkViolation(t, s.db.Exec(invalid).Error)
		}

		// The migrated pull accepts claimed work and closes normally.
		p = claimed(t, s, p)
		j := claimJob(p, "job")
		Ok(t, s.StartJob(j))
		Ok(t, s.FinishJob(j.ID, "succeeded", ""))
		checkViolation(t, s.db.Exec("UPDATE jobs SET project_id = NULL WHERE id = 'job'").Error)
		checkViolation(t, s.db.Exec("UPDATE jobs SET kind = 'other' WHERE id = 'job'").Error)
		Ok(t, s.ReleaseClaim(p))
		Ok(t, s.ClosePull(p))
		Ok(t, s.db.Table("pulls").Where("id = ?", "pull").Take(&lifecycle).Error)
		Assert(t, lifecycle.Closed && lifecycle.Cancelled && lifecycle.ClosedAt != nil && lifecycle.ClosureGeneration == 1, "migrated pull did not close: %+v", lifecycle)
	})
}

// writeJobsAndClaims adds the rows version 2 stores for finished work on the
// pull from writeLocksAndStatus: a succeeded plan, an apply interrupted by
// recovery, and the recovery audit.
func writeJobsAndClaims(t *testing.T, db *gorm.DB, startedAt time.Time) {
	t.Helper()
	insert := "INSERT INTO jobs(id, repository_id, pull_id, project_id, command, sub_command, arguments, steps, issuer, issuer_teams, trigger, revision, state, error, private, started_at, finished_at, owner, kind) VALUES (?, 'repository', 'pull', 'project', ?, '', '[\"-no-color\"]', '[]', 'issuer', '[\"admins\"]', 'comment', 'revision', ?, ?, FALSE, ?, ?, ?, 'command')"
	Ok(t, db.Exec(insert, "old-plan", "plan", "succeeded", "", databaseTime(startedAt), databaseTime(startedAt.Add(time.Minute)), "old-owner").Error)
	Ok(t, db.Exec(insert, "old-apply", "apply", "interrupted", "operator reconciled stopped execution", databaseTime(startedAt.Add(2*time.Minute)), databaseTime(startedAt.Add(3*time.Minute)), "crashed-owner").Error)
	Ok(t, db.Exec("INSERT INTO execution_recoveries(id, pull_id, previous_owner, recovery_owner, source_job_id, actor, recovered_at) VALUES ('recovery', 'pull', 'crashed-owner', 'recovery-owner', 'old-apply', 'operator', ?)", databaseTime(startedAt.Add(3*time.Minute))).Error)
}

func TestUpgradeFromJobsAndClaims(t *testing.T) {
	ForEachDatabase(t, func(t *testing.T, d TestDatabase) {
		p := upgradePull()
		startedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		databaseAt(t, d, 2, func(db *gorm.DB) {
			writeLocksAndStatus(t, db, p, startedAt)
			writeJobsAndClaims(t, db, startedAt)
		})

		s, err := d.Open()
		Ok(t, err)
		defer s.Close()

		old, err := s.Job("old-plan")
		Ok(t, err)
		Assert(t, old != nil, "old plan job is missing")
		Equals(t, "plan", old.Command)
		Equals(t, "succeeded", old.State)
		Equals(t, []string{"-no-color"}, old.Arguments)
		Equals(t, models.User{Username: "issuer", Teams: []string{"admins"}}, old.Issuer)
		Assert(t, old.StartedAt.Equal(startedAt), "start time changed to %s", old.StartedAt)
		Assert(t, old.ManagedPlan && old.PlanJobID == "" && old.ToolDistribution == "" && old.ToolVersion == "", "old job defaults changed: %+v", old)
		var recoveries int64
		Ok(t, s.db.Table("execution_recoveries").Where("source_job_id = ?", "old-apply").Count(&recoveries).Error)
		Equals(t, int64(1), recoveries)

		// The unique index added for composite references covers existing jobs.
		Assert(t, s.db.Create(&pendingPlan{ProjectID: "project", PullID: "pull", JobID: "missing"}).Error != nil, "pending plan accepted a missing job")
		Ok(t, s.db.Create(&pendingPlan{ProjectID: "project", PullID: "pull", JobID: "old-plan"}).Error)
		Ok(t, s.db.Where("job_id = ?", "old-plan").Delete(&pendingPlan{}).Error)
		checkViolation(t, s.db.Create(&planBinding{JobID: "old-apply", PlanJobID: "old-apply", ProjectID: "project", PullID: "pull"}).Error)

		// An old plan has no snapshot, so a consumer admitted after the upgrade
		// binds only a plan produced by the new version.
		p = claimed(t, s, p)
		unbound := claimJob(p, "unbound-apply")
		unbound.Command = "apply"
		Ok(t, s.StartJob(unbound))
		got, err := s.Job(unbound.ID)
		Ok(t, err)
		Equals(t, "", got.PlanJobID)
		Ok(t, s.FinishJob(unbound.ID, "failed", "no plan"))
		plan := readyLifecyclePlan(t, s, p)
		consumer := claimJob(p, "apply")
		consumer.Command = "apply"
		Ok(t, s.StartJob(consumer))
		got, err = s.Job(consumer.ID)
		Ok(t, err)
		Equals(t, plan.ID, got.PlanJobID)
		Ok(t, s.FinishJob(consumer.ID, "succeeded", ""))
		pending, err := s.PendingPlans(p.BaseRepo, p.Num)
		Ok(t, err)
		Equals(t, 0, len(pending))
	})
}

// writeDurablePlans adds the rows version 3 stores for a published plan from
// writeJobsAndClaims: tool metadata, a ready snapshot with its database blob,
// and the pending plan slot.
func writeDurablePlans(t *testing.T, db *gorm.DB, createdAt time.Time) models.PlanSnapshot {
	t.Helper()
	snapshot := validSnapshot("old-plan")
	manifest, err := json.Marshal(snapshot.Manifest)
	Ok(t, err)
	Ok(t, db.Exec("UPDATE jobs SET tool_distribution = 'terraform', tool_version = '1.11.1' WHERE id = 'old-plan'").Error)
	Ok(t, db.Exec("INSERT INTO plan_snapshots(job_id, manifest, size, sha256, created_at, ready) VALUES ('old-plan', ?, ?, ?, ?, TRUE)", string(manifest), snapshot.Size, snapshot.SHA256, databaseTime(createdAt)).Error)
	Ok(t, db.Exec("INSERT INTO snapshot_blobs(job_id, content) VALUES ('old-plan', ?)", []byte("bundle")).Error)
	Ok(t, db.Exec("INSERT INTO pending_plans(project_id, pull_id, job_id, ready) VALUES ('project', 'pull', 'old-plan', TRUE)").Error)
	return snapshot
}

func TestUpgradeFromDurablePlans(t *testing.T) {
	ForEachDatabase(t, func(t *testing.T, d TestDatabase) {
		p := upgradePull()
		startedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		var snapshot models.PlanSnapshot
		databaseAt(t, d, 3, func(db *gorm.DB) {
			writeLocksAndStatus(t, db, p, startedAt)
			writeJobsAndClaims(t, db, startedAt)
			snapshot = writeDurablePlans(t, db, startedAt.Add(time.Minute))
		})

		s, err := d.Open()
		Ok(t, err)
		defer s.Close()

		// Earlier jobs recorded no output and have no journal.
		stream, err := s.Log("old-plan")
		Ok(t, err)
		Assert(t, stream == nil, "old job gained a log stream: %+v", stream)
		stream, data, err := s.ReadLogJournal("old-plan", 0, 10)
		Ok(t, err)
		Assert(t, stream == nil && data == nil, "old job returned log content")
		expired, err := s.ExpiredLogs(startedAt.Add(24*time.Hour), time.Hour)
		Ok(t, err)
		Equals(t, 0, len(expired))
		foreignKeyViolation(t, s.db.Create(&logStream{LogStream: models.LogStream{JobID: "missing"}}).Error)

		// The published plan, its blob and its pending slot survive.
		old, err := s.Job("old-plan")
		Ok(t, err)
		Equals(t, "1.11.1", old.ToolVersion)
		stored, err := s.Snapshot("old-plan")
		Ok(t, err)
		Assert(t, stored != nil, "old snapshot is missing")
		Equals(t, snapshot.Manifest, stored.Manifest)
		Equals(t, snapshot.SHA256, stored.SHA256)
		blob, err := BlobStore{Store: s}.OpenSnapshot("old-plan")
		Ok(t, err)
		content, err := io.ReadAll(blob)
		Ok(t, err)
		Equals(t, "bundle", string(content))
		pending, err := s.PendingPlans(p.BaseRepo, p.Num)
		Ok(t, err)
		Equals(t, 1, len(pending))
		Equals(t, "old-plan", pending[0].ID)

		// A consumer admitted after the upgrade binds the old plan and journals its output.
		p = claimed(t, s, p)
		hook := claimJob(p, "hook")
		hook.Kind, hook.Command = models.WorkflowHookJob, "pre_workflow_hook"
		Ok(t, s.StartJob(hook))
		Ok(t, s.FinishJob(hook.ID, "succeeded", ""))
		consumer := claimJob(p, "apply")
		consumer.Command = "apply"
		Ok(t, s.StartJob(consumer))
		got, err := s.Job(consumer.ID)
		Ok(t, err)
		Equals(t, "old-plan", got.PlanJobID)
		input, err := s.InputSnapshot(consumer.ID)
		Ok(t, err)
		Assert(t, input != nil && input.JobID == "old-plan", "consumer lost the old snapshot")
		Ok(t, s.AppendLogChunk(consumer.ID, []byte("applied")))
		checkViolation(t, s.db.Create(&logChunk{JobID: consumer.ID, StartOffset: 7, Content: []byte{}}).Error)
		Ok(t, s.FinishJob(consumer.ID, "succeeded", ""))
		stream, data, err = s.ReadLogJournal(consumer.ID, 0, 100)
		Ok(t, err)
		Assert(t, stream != nil && stream.SealedAt != nil, "consumer log was not sealed")
		Equals(t, "applied", string(data))
		pending, err = s.PendingPlans(p.BaseRepo, p.Num)
		Ok(t, err)
		Equals(t, 0, len(pending))
	})
}

func TestUpgradeFromJobLogs(t *testing.T) {
	ForEachDatabase(t, func(t *testing.T, d TestDatabase) {
		p := upgradePull()
		startedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		databaseAt(t, d, 4, func(db *gorm.DB) {
			writeLocksAndStatus(t, db, p, startedAt)
			writeJobsAndClaims(t, db, startedAt)
			insert := "INSERT INTO jobs(id, repository_id, pull_id, project_id, command, sub_command, arguments, steps, issuer, issuer_teams, trigger, revision, state, error, private, started_at, owner, kind) VALUES ('old-running', 'repository', 'pull', 'project', 'plan', '', '[]', '[]', 'issuer', '[]', 'comment', 'revision', 'running', '', FALSE, ?, 'stopped-owner', 'command')"
			Ok(t, db.Exec(insert, databaseTime(startedAt.Add(4*time.Minute))).Error)
			Ok(t, db.Exec("INSERT INTO job_logs(job_id, byte_length) VALUES ('old-running', 0)").Error)
			Ok(t, db.Exec("INSERT INTO execution_claims(pull_id, owner, state, acquired_at) VALUES ('pull', 'stopped-owner', 'active', ?)", databaseTime(startedAt.Add(4*time.Minute))).Error)
		})

		s, err := d.Open()
		Ok(t, err)
		defer s.Close()

		// Existing rows have no instance, and a claim's heartbeat starts at acquisition.
		claim := storedClaim(t, s, "stopped-owner")
		Equals(t, "", claim.ServerInstanceID)
		Assert(t, claim.HeartbeatAt.Equal(startedAt.Add(4*time.Minute)), "heartbeat was not backfilled: %s", claim.HeartbeatAt)
		old, err := s.Job("old-plan")
		Ok(t, err)
		Equals(t, "", old.ServerInstance)
		var audit struct{ PreviousServerInstanceID, RecoveryServerInstanceID string }
		Ok(t, s.db.Table("execution_recoveries").Where("id = ?", "recovery").Take(&audit).Error)
		Equals(t, "", audit.PreviousServerInstanceID+audit.RecoveryServerInstanceID)

		// The upgraded claim's heartbeat is long expired; the sweep marks it uncertain
		// without transferring it, and attested recovery records the unknown instance.
		expired, err := s.ExpireClaims()
		Ok(t, err)
		Equals(t, 1, expired)
		Equals(t, "uncertain", storedClaim(t, s, "stopped-owner").State)
		stream, err := s.Log("old-running")
		Ok(t, err)
		Assert(t, stream.SealedAt != nil && stream.Interrupted, "upgraded running log was not interrupted")
		Ok(t, s.AcquireUnlockClaim(p, "recovery", "operator", true))
		Ok(t, s.db.Table("execution_recoveries").Where("recovery_owner = ?", "recovery").Take(&audit).Error)
		Equals(t, "", audit.PreviousServerInstanceID)
		Equals(t, s.InstanceID(), audit.RecoveryServerInstanceID)
		Equals(t, s.InstanceID(), storedClaim(t, s, "recovery").ServerInstanceID)
	})
}

func TestUpgradeFromClaimLiveness(t *testing.T) {
	ForEachDatabase(t, func(t *testing.T, d TestDatabase) {
		p := upgradePull()
		startedAt := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		databaseAt(t, d, 5, func(db *gorm.DB) {
			writeLocksAndStatus(t, db, p, startedAt)
			writeJobsAndClaims(t, db, startedAt)
		})

		s, err := d.Open()
		Ok(t, err)
		defer s.Close()
		var deliveries int64
		Ok(t, s.db.Model(&webhookDelivery{}).Count(&deliveries).Error)
		Equals(t, int64(0), deliveries)
		first, err := s.RecordDelivery("github", "after-upgrade")
		Ok(t, err)
		Assert(t, first, "upgraded database rejected a new delivery")
		old, err := s.Job("old-plan")
		Ok(t, err)
		Equals(t, "succeeded", old.State)
	})
}

// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"context"
	"io/fs"
	"net/url"
	"path/filepath"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/pressly/goose/v3"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// databaseAt creates the SQLite file in dir at an earlier schema version, as an
// older release would have left it, and runs fill against it before closing.
func databaseAt(t *testing.T, dir string, version int64, fill func(*gorm.DB)) {
	t.Helper()
	u := url.URL{Scheme: "file", Path: filepath.Join(dir, "atlantis.sqlite")}
	u.RawQuery = url.Values{"_pragma": {"foreign_keys(1)"}}.Encode()
	orm, err := gorm.Open(sqlite.Open(u.String()), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	Ok(t, err)
	conn, err := orm.DB()
	Ok(t, err)
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	files, err := fs.Sub(migrationFiles, "migrations")
	Ok(t, err)
	provider, err := goose.NewProvider(goose.DialectSQLite3, conn, files)
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
	dir := t.TempDir()
	p := upgradePull()
	lockedAt := time.Date(2026, 1, 2, 3, 4, 5, 6000, time.UTC)
	databaseAt(t, dir, 1, func(db *gorm.DB) { writeLocksAndStatus(t, db, p, lockedAt) })

	s, err := OpenServer(dir)
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
		ErrContains(t, "CHECK constraint failed", s.db.Exec(invalid).Error)
	}

	// The migrated pull accepts claimed work and closes normally.
	p = claimed(t, s, p)
	j := claimJob(p, "job")
	Ok(t, s.StartJob(j))
	Ok(t, s.FinishJob(j.ID, "succeeded", ""))
	ErrContains(t, "CHECK constraint failed", s.db.Exec("UPDATE jobs SET project_id = NULL WHERE id = 'job'").Error)
	ErrContains(t, "CHECK constraint failed", s.db.Exec("UPDATE jobs SET kind = 'other' WHERE id = 'job'").Error)
	Ok(t, s.ReleaseClaim(p))
	Ok(t, s.ClosePull(p))
	Ok(t, s.db.Table("pulls").Where("id = ?", "pull").Take(&lifecycle).Error)
	Assert(t, lifecycle.Closed && lifecycle.Cancelled && lifecycle.ClosedAt != nil && lifecycle.ClosureGeneration == 1, "migrated pull did not close: %+v", lifecycle)
}

// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"context"
	"fmt"
	"io/fs"
	"testing"
	"testing/fstest"
	"time"

	"github.com/google/uuid"
	"github.com/pressly/goose/v3"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func conformanceStore(t *testing.T) *Store {
	t.Helper()
	s, err := OpenServer(t.TempDir())
	Ok(t, err)
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func claimPull() models.PullRequest {
	repo := models.Repo{FullName: "team/infra", Owner: "team", Name: "infra", CloneURL: "https://git.example:8443/gitlab/team/infra.git", VCSHost: models.VCSHost{Type: models.Gitlab, Hostname: "git.example"}}
	return models.PullRequest{Num: 17, BaseRepo: repo, HeadCommit: "revision", HeadBranch: "feature", BaseBranch: "main"}
}

func seedClaimJob(t *testing.T, s *Store, p models.PullRequest, state string) job {
	t.Helper()
	project := models.NewProject(p.BaseRepo.FullName, ".", "")
	acquired, _, err := s.TryLock(models.ProjectLock{Pull: p, Project: project, Workspace: "default"})
	Ok(t, err)
	Assert(t, acquired, "project lock unavailable")
	var pr pull
	Ok(t, s.db.Where("id IN (?)", pullIDs(s.db, p.BaseRepo, p.Num)).Take(&pr).Error)
	var projectID string
	Ok(t, projectIDs(s.db, p.BaseRepo, project, "default").Scan(&projectID).Error)
	started, err := s.Now()
	Ok(t, err)
	row := job{ID: uuid.NewString(), RepositoryID: pr.RepositoryID, PullID: pr.ID, ProjectID: &projectID,
		Owner: p.ExecutionOwner(), Kind: models.CommandJob, ManagedPlan: true,
		State: state, StartedAt: started}
	Ok(t, s.db.Create(&row).Error)
	return row
}

func TestExecutionDatabaseConformance(t *testing.T) {
	t.Run("unfinished ownership blocks reuse", func(t *testing.T) {
		dir := t.TempDir()
		s, err := OpenServer(dir)
		Ok(t, err)
		t.Cleanup(func() { _ = s.Close() })
		p := claimPull()
		Ok(t, s.AcquireClaim(p, "owner-a"))
		p.Execution = &models.Execution{Owner: "owner-a"}
		p.BaseRepo.Execution = p.Execution
		Assert(t, s.AcquireClaim(p, "owner-b") != nil, "duplicate claim acquired")
		seedClaimJob(t, s, p, "running")
		Assert(t, s.ReleaseClaim(p) != nil, "unfinished jobs released execution ownership")
		Ok(t, s.Close())
		s, err = OpenServer(dir)
		Ok(t, err)
		Ok(t, s.MarkPreviousExecutionsUncertain())
		Assert(t, s.CheckClaim(p) != nil, "uncertain owner remained active")
		Assert(t, s.AcquireClaim(p, "owner-b") != nil, "unfinished work allowed takeover")
		locked, err := s.GetLock(p.BaseRepo, models.NewProject(p.BaseRepo.FullName, ".", ""), "default")
		Ok(t, err)
		Assert(t, locked != nil, "startup removed user project lock")
	})
	t.Run("closure invalidates pending plans without releasing ownership", func(t *testing.T) {
		s := conformanceStore(t)
		p := claimPull()
		Ok(t, s.AcquireClaim(p, "owner"))
		p.Execution = &models.Execution{Owner: "owner"}
		row := seedClaimJob(t, s, p, "succeeded")
		Ok(t, s.db.Create(&pendingPlan{ProjectID: *row.ProjectID, PullID: row.PullID, JobID: row.ID, Ready: true}).Error)
		Ok(t, s.ClosePull(p))
		var pending int64
		Ok(t, s.db.Model(&pendingPlan{}).Count(&pending).Error)
		Equals(t, int64(0), pending)
		idle, err := s.PullIdle(p)
		Ok(t, err)
		Assert(t, !idle, "closure discarded execution ownership")
		Assert(t, s.AcquireClaim(p, "contender") != nil, "closed pull accepted execution")
		Ok(t, s.ReleaseClaim(p))
		idle, err = s.PullIdle(p)
		Ok(t, err)
		Assert(t, idle, "stopped execution remained owned")
	})
	t.Run("cancellation preserves active ownership", func(t *testing.T) {
		s := conformanceStore(t)
		p := claimPull()
		Ok(t, s.AcquireClaim(p, "owner"))
		p.Execution = &models.Execution{Owner: "owner"}
		row := seedClaimJob(t, s, p, "running")
		Ok(t, s.CancelPull(p))
		cancelled, err := s.IsCancelled(p)
		Ok(t, err)
		Assert(t, cancelled, "cancellation was not recorded")
		Ok(t, s.CheckClaim(p))
		Assert(t, s.ReleaseClaim(p) != nil, "cancellation released unfinished work")
		Ok(t, s.db.Model(&job{}).Where("id = ?", row.ID).Update("state", "succeeded").Error)
		Ok(t, s.ReleaseClaim(p))
		Ok(t, s.AcquireClaim(p, "next-owner"))
		cancelled, err = s.IsCancelled(p)
		Ok(t, err)
		Assert(t, !cancelled, "new execution inherited prior cancellation")
	})
	t.Run("pull identity and foreign keys", func(t *testing.T) {
		s := conformanceStore(t)
		p := claimPull()
		Ok(t, s.AcquireClaim(p, "owner"))
		p.Execution = &models.Execution{Owner: "owner"}
		other := p
		other.BaseRepo.CloneURL = "https://other.example/gitlab/team/infra.git"
		Assert(t, s.CheckClaim(other) != nil, "claim crossed VCS instances")
		other = p
		other.Num++
		Assert(t, s.CheckClaim(other) != nil, "claim crossed pull requests")
		Assert(t, s.db.Exec("INSERT INTO projects(id,repository_id,directory,name,workspace) VALUES ('project','missing','.','','default')").Error != nil, "missing foreign key accepted")
		var claim executionClaim
		Ok(t, s.db.First(&claim).Error)
		var stored pull
		Ok(t, s.db.First(&stored).Error)
		Equals(t, stored.ID, claim.PullID)
		Ok(t, s.ReleaseClaim(p))
	})
}

func TestPortableSchemaConformance(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		t.Run("lock listing follows creation time", func(t *testing.T) {
			s := conformanceStore(t)
			p := claimPull()
			Ok(t, s.AcquireClaim(p, "owner"))
			p.Execution = &models.Execution{Owner: "owner"}
			at := time.Date(2026, 11, 5, 12, 0, 0, 0, time.UTC)
			for i, directory := range []string{"second", "first"} {
				lock := models.ProjectLock{Pull: p, Project: models.NewProject(p.BaseRepo.FullName, directory, ""), Workspace: "default", Time: at.Add(-time.Duration(i) * time.Hour)}
				acquired, _, err := s.TryLock(lock)
				Ok(t, err)
				Assert(t, acquired, "project lock unavailable")
			}
			locks, err := s.List()
			Ok(t, err)
			Equals(t, 2, len(locks))
			Equals(t, "first", locks[0].Project.Path)
			Equals(t, "second", locks[1].Project.Path)
		})
	})
}

func TestSchemaConstraintsAndAtomicStatus(t *testing.T) {
	s, err := OpenServer(t.TempDir())
	Ok(t, err)
	defer s.Close()
	p := models.PullRequest{Num: 1, HeadCommit: "first", BaseBranch: "main", BaseRepo: models.Repo{FullName: "owner/repo", VCSHost: models.VCSHost{Hostname: "git.example"}}}
	p.Execution = &models.Execution{Owner: "schema-test"}
	Ok(t, s.AcquireClaim(p, p.ExecutionOwner()))
	result := command.ProjectResult{RepoRelDir: ".", Workspace: "default", ProjectName: "first"}
	_, err = s.UpdatePullWithResults(p, []command.ProjectResult{result})
	Ok(t, err)
	for _, table := range []string{"vcs_instances", "repositories", "pulls", "projects", "pull_statuses", "project_statuses"} {
		var n int64
		Ok(t, s.db.Table(table).Count(&n).Error)
		Equals(t, int64(1), n)
	}
	Assert(t, s.db.Exec("INSERT INTO projects(id, repository_id, directory, name, workspace) VALUES ('project', 'missing', '.', '', 'default')").Error != nil, "foreign keys not enforced")
	Ok(t, s.db.Exec("CREATE TRIGGER reject_status BEFORE INSERT ON project_statuses BEGIN SELECT RAISE(ABORT, 'injected failure'); END").Error)
	p.HeadCommit = "second"
	result.ProjectName = "second"
	_, err = s.UpdatePullWithResults(p, []command.ProjectResult{result})
	Assert(t, err != nil, "injected failure ignored")
	got, err := s.GetPullStatus(p)
	Ok(t, err)
	Equals(t, "first", got.Pull.HeadCommit)
	Equals(t, "first", got.Projects[0].ProjectName)
	var stored pull
	Ok(t, s.db.First(&stored).Error)
	Equals(t, "first", stored.HeadCommit)
	var count int64
	Ok(t, s.db.Model(&project{}).Count(&count).Error)
	Equals(t, int64(1), count)
}

func TestNewerSchemaRejected(t *testing.T) {
	dir := t.TempDir()
	s, err := OpenServer(dir)
	Ok(t, err)
	Ok(t, s.db.Exec("INSERT INTO goose_db_version(version_id, is_applied) VALUES (999, 1)").Error)
	Ok(t, s.Close())
	_, err = OpenServer(dir)
	Assert(t, err != nil, "accepted unsupported schema")
}

func TestVersionedMigrationPreservesRows(t *testing.T) {
	store, err := OpenServer(t.TempDir())
	Ok(t, err)
	defer store.Close()
	Ok(t, store.db.Exec("INSERT INTO command_locks(command, unix_time) VALUES (?, ?)", int(command.Apply), 123).Error)
	files := fstest.MapFS{}
	entries, err := fs.ReadDir(migrationFiles, "migrations")
	Ok(t, err)
	for _, entry := range entries {
		data, err := migrationFiles.ReadFile("migrations/" + entry.Name())
		Ok(t, err)
		files[entry.Name()] = &fstest.MapFile{Data: data}
	}
	files[fmt.Sprintf("%05d_test.sql", len(entries)+1)] = &fstest.MapFile{Data: []byte("-- +goose Up\nALTER TABLE command_locks ADD COLUMN note TEXT NOT NULL DEFAULT '';\n")}

	provider, err := goose.NewProvider(goose.DialectSQLite3, store.conn, files)
	Ok(t, err)
	applied, err := provider.Up(context.Background())
	Ok(t, err)
	Equals(t, 1, len(applied))
	lock, err := store.CheckCommandLock(command.Apply)
	Ok(t, err)
	Equals(t, int64(123), lock.LockMetadata.UnixTime)
	var note string
	Ok(t, store.db.Raw("SELECT note FROM command_locks WHERE command = ?", int(command.Apply)).Scan(&note).Error)
	Equals(t, "", note)
}

func TestIncompleteDraftSchemaRejected(t *testing.T) {
	for _, statement := range []string{
		"DROP TABLE execution_recoveries",
		"DROP TABLE job_log_chunks",
		"DROP TABLE job_log_chunks; DROP TABLE job_logs",
		"ALTER TABLE jobs DROP COLUMN tool_distribution",
		"ALTER TABLE jobs DROP COLUMN tool_version",
	} {
		t.Run(statement, func(t *testing.T) {
			dir := t.TempDir()
			store, err := OpenServer(dir)
			Ok(t, err)
			Ok(t, store.db.Exec(statement).Error)
			Ok(t, store.Close())
			reopened, err := OpenServer(dir)
			if reopened != nil {
				_ = reopened.Close()
			}
			ErrContains(t, "unsupported; drain Atlantis and use a fresh data directory", err)
		})
	}
}

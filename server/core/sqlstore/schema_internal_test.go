// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"context"
	"database/sql"
	"path/filepath"
	"testing"
	"testing/fstest"

	"github.com/pressly/goose/v3"

	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

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

func TestRevisionAndTargetChangesReplaceProjects(t *testing.T) {
	approved := []models.PolicySetStatus{{PolicySetName: "security", Passed: true}}
	p := models.PullRequest{HeadCommit: "first", BaseBranch: "main"}
	prior := &models.PullStatus{Pull: p, Projects: []models.ProjectStatus{
		{RepoRelDir: ".", ProjectName: "keep", Workspace: "default", PolicyStatus: approved},
		{RepoRelDir: "removed", Workspace: "default"},
	}}
	results := []command.ProjectResult{{RepoRelDir: ".", ProjectName: "keep", Workspace: "default"}}
	for _, changed := range []models.PullRequest{{HeadCommit: "second", BaseBranch: "main"}, {HeadCommit: "first", BaseBranch: "release"}} {
		got := mergeStatus(prior, changed, results)
		Equals(t, 1, len(got.Projects))
		Equals(t, approved, got.Projects[0].PolicyStatus)
	}
	got := mergeStatus(prior, p, results)
	Equals(t, 2, len(got.Projects))
}

func TestUpgradeEarlierMigrationsPreservesRows(t *testing.T) {
	dir := t.TempDir()
	conn, err := sql.Open("sqlite", filepath.Join(dir, "atlantis.sqlite"))
	Ok(t, err)
	files := fstest.MapFS{}
	for _, name := range []string{"00001_core.sql", "00002_jobs_artifacts.sql"} {
		content, err := migrationFiles.ReadFile("migrations/" + name)
		Ok(t, err)
		files[name] = &fstest.MapFile{Data: content}
	}
	provider, err := goose.NewProvider(goose.DialectSQLite3, conn, files)
	Ok(t, err)
	_, err = provider.Up(context.Background())
	Ok(t, err)
	_, err = conn.Exec("INSERT INTO command_locks(command, unix_time) VALUES (?, ?)", int(command.Apply), 123)
	Ok(t, err)
	Ok(t, conn.Close())
	store, err := OpenServer(dir)
	Ok(t, err)
	defer store.Close()
	lock, err := store.CheckCommandLock(command.Apply)
	Ok(t, err)
	Equals(t, int64(123), lock.LockMetadata.UnixTime)
	var count int64
	Ok(t, store.db.Raw("SELECT COUNT(*) FROM pragma_table_info('project_statuses') WHERE name = 'plan_hash'").Scan(&count).Error)
	Equals(t, int64(1), count)
}

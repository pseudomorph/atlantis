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

// storedLock acquires a project lock and returns its stored row.
func storedLock(t *testing.T, s *Store, p models.PullRequest, directory string) lock {
	t.Helper()
	project := models.NewProject(p.BaseRepo.FullName, directory, "")
	acquired, _, err := s.TryLock(models.ProjectLock{Pull: p, Project: project, Workspace: "default", Time: time.Now().UTC()})
	Ok(t, err)
	Assert(t, acquired, "project lock unavailable")
	var row lock
	Ok(t, s.db.Where("project_id IN (?)", projectIDs(s.db, p.BaseRepo, project, "default")).Take(&row).Error)
	return row
}

func TestSchemaRejectsCrossPullRelationships(t *testing.T) {
	s := conformanceStore(t)
	p := claimPull()
	first := storedLock(t, s, p, ".")
	other := p
	other.BaseRepo.FullName = "team/other"
	second := storedLock(t, s, other, ".")
	Assert(t, s.db.Exec("UPDATE locks SET repository_id = ? WHERE project_id = ?", second.RepositoryID, first.ProjectID).Error != nil, "lock accepted another repository")
	Assert(t, s.db.Exec("UPDATE locks SET pull_id = ? WHERE project_id = ?", second.PullID, first.ProjectID).Error != nil, "lock accepted another repository's pull")
	Assert(t, s.db.Exec("UPDATE locks SET project_id = ? WHERE project_id = ?", second.ProjectID, first.ProjectID).Error != nil, "lock accepted another repository's project")
	Assert(t, s.db.Exec("UPDATE pulls SET repository_id = ? WHERE id = ?", "missing", first.PullID).Error != nil, "pull accepted a missing repository")
}

func TestPortableSchemaConformance(t *testing.T) {
	t.Run("sqlite", func(t *testing.T) {
		t.Run("stable UUID relationships", func(t *testing.T) {
			s := conformanceStore(t)
			p := claimPull()
			first := storedLock(t, s, p, ".")
			_, err := s.UpdatePullWithResults(p, []command.ProjectResult{{RepoRelDir: ".", Workspace: "default"}})
			Ok(t, err)
			for _, table := range []string{"vcs_instances", "repositories", "pulls", "projects"} {
				var ids []string
				Ok(t, s.db.Table(table).Pluck("id", &ids).Error)
				Equals(t, 1, len(ids))
				_, err := uuid.Parse(ids[0])
				Ok(t, err)
			}
			other := p
			other.BaseRepo.FullName = "team/other"
			second := storedLock(t, s, other, "other")
			Assert(t, s.db.Exec("INSERT INTO locks(project_id,repository_id,pull_id,revision,username,created_at) VALUES (?,?,?,?,?,?)", uuid.NewString(), second.RepositoryID, first.PullID, "revision", "user", time.Now().UTC()).Error != nil, "cross-repository foreign key accepted")
		})
		t.Run("UTC lock timestamps", func(t *testing.T) {
			s := conformanceStore(t)
			previousLocation := time.Local
			time.Local = time.FixedZone("local-test", -7*60*60)
			t.Cleanup(func() { time.Local = previousLocation })
			p := claimPull()
			at := time.Date(2026, 11, 5, 12, 30, 0, 123456789, time.FixedZone("offset", -7*60*60))
			project := models.NewProject(p.BaseRepo.FullName, ".", "")
			acquired, _, err := s.TryLock(models.ProjectLock{Pull: p, Project: project, Workspace: "default", Time: at})
			Ok(t, err)
			Assert(t, acquired, "project lock unavailable")
			stored, err := s.GetLockInRepo(p.BaseRepo, project, "default")
			Ok(t, err)
			Assert(t, stored.Time.Equal(at.Truncate(time.Microsecond)), "lock timestamp changed instant")
			var matching int64
			Ok(t, s.db.Model(&lock{}).Where("created_at = ?", at.UTC().Truncate(time.Microsecond)).Count(&matching).Error)
			Equals(t, int64(1), matching)
		})
		t.Run("lock listing follows creation time", func(t *testing.T) {
			s := conformanceStore(t)
			p := claimPull()
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

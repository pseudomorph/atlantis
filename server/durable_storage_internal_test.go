// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/runatlantis/atlantis/server/core/sqlstore"

	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	. "github.com/runatlantis/atlantis/testing"
)

func TestDurableStorageServer(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "project locks", true: "repository locking disabled"}[disabled], func(t *testing.T) {
			s, err := NewServer(UserConfig{Experiments: []string{"durable-storage"}, Storage: &StorageConfig{Database: StorageDatabaseConfig{Type: "sqlite"}}, DataDir: t.TempDir(),
				AllowCommands: "plan,apply", AtlantisURL: "http://example.com", GithubHostname: "github.com", GithubUser: "user", DisableRepoLocking: disabled}, Config{AtlantisVersion: "test"})
			Ok(t, err)
			t.Cleanup(func() { Ok(t, s.closeDatabase(time.Second)); Ok(t, s.StatsCloser.Close()) })
			_, ok := s.database.(*sqlstore.Store)
			Assert(t, ok, "server selected another database")
			p := models.PullRequest{Num: 1, BaseRepo: models.Repo{FullName: "owner/repo"}}
			Ok(t, s.database.(*sqlstore.Store).AcquireClaim(p, "test-owner"))
			p.Execution = &models.Execution{Owner: "test-owner"}
			p.BaseRepo.Execution = p.Execution
			_, err = s.Locker.TryLock(models.NewProject("owner/repo", ".", ""), "default", p, models.User{})
			Ok(t, err)
			locks, err := s.database.List()
			Ok(t, err)
			Equals(t, map[bool]int{false: 1, true: 0}[disabled], len(locks))
			_, err = s.database.UpdatePullWithResults(p, nil)
			Ok(t, err)
			status, err := s.database.GetPullStatus(p)
			Ok(t, err)
			Assert(t, status != nil, "status not persisted")
			_, err = s.ApplyLocker.LockApply()
			Ok(t, err)
			lock, err := s.database.CheckCommandLock(command.Apply)
			Ok(t, err)
			Assert(t, lock != nil, "global lock not persisted")
		})
	}
}

func TestDurableStorageRejectsMissingPermissionBeforeStartup(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created")
	_, err := NewServer(UserConfig{DataDir: dir, Storage: &StorageConfig{Database: StorageDatabaseConfig{Type: "sqlite"}}}, Config{})
	Assert(t, err != nil, "SQLite started without permission")
	_, err = os.Stat(dir)
	Assert(t, os.IsNotExist(err), "validation created state")
}

func TestArtifactCollectionConfinedToReservedNamespace(t *testing.T) {
	store, err := sqlstore.OpenServer(t.TempDir())
	Ok(t, err)
	defer store.Close()
	directory := t.TempDir()
	runtime, err := newStorageRuntime(store, UserConfig{Storage: &StorageConfig{Database: StorageDatabaseConfig{Type: "sqlite"}, Artifacts: StorageArtifactsConfig{Type: "filesystem", Directory: directory}}}, nil)
	Ok(t, err)
	defer runtime.Artifacts.Blobs.Close()
	unrelated := filepath.Join(directory, "shared.plan")
	orphan := filepath.Join(directory, "artifacts", "v1", "orphan")
	Ok(t, os.MkdirAll(filepath.Dir(orphan), 0700))
	old := time.Now().Add(-2 * time.Hour)
	for _, file := range []string{unrelated, orphan} {
		Ok(t, os.WriteFile(file, []byte("data"), 0600))
		Ok(t, os.Chtimes(file, old, old))
	}
	Ok(t, runtime.Artifacts.Collect(time.Now(), time.Hour, 0))
	_, err = os.Stat(unrelated)
	Ok(t, err)
	_, err = os.Stat(orphan)
	Assert(t, os.IsNotExist(err), "orphan was not removed")
}

func TestDurableStorageStartupBlocksPreviousExecution(t *testing.T) {
	dir := t.TempDir()
	previous, err := sqlstore.OpenServer(dir)
	Ok(t, err)
	pull := models.PullRequest{Num: 1, BaseRepo: models.Repo{FullName: "owner/repo"}, Execution: &models.Execution{Owner: "previous-owner"}}
	Ok(t, previous.AcquireClaim(pull, pull.ExecutionOwner()))
	Ok(t, previous.Close())
	s, err := NewServer(UserConfig{Experiments: []string{"durable-storage"}, Storage: &StorageConfig{Database: StorageDatabaseConfig{Type: "sqlite"}}, DataDir: dir,
		AllowCommands: "plan,apply", AtlantisURL: "http://example.com", GithubHostname: "github.com", GithubUser: "user"}, Config{AtlantisVersion: "test"})
	Ok(t, err)
	t.Cleanup(func() { Ok(t, s.closeDatabase(time.Second)); Ok(t, s.StatsCloser.Close()) })
	store := s.database.(*sqlstore.Store)
	Assert(t, store.CheckClaim(pull) != nil, "startup accepted a previous execution owner")
	Assert(t, store.AcquireClaim(pull, "replacement") != nil, "startup silently took over unfinished work")
	pull.Num++
	Ok(t, store.AcquireClaim(pull, "unrelated"))
	Assert(t, s.Drainer != nil, "server did not wire execution draining")
}

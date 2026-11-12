// Copyright 2017 HootSuite Media Inc.
// SPDX-License-Identifier: Apache-2.0
// Modified hereafter by contributors to runatlantis/atlantis.

package server

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/core/db/mocks"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
	"github.com/stretchr/testify/assert"
	"go.uber.org/mock/gomock"
)

func TestServer_CloseDatabase(t *testing.T) {

	timeout := time.Second

	type databaseCase struct {
		description      string
		closeFn          func() error
		expectedErr      string
		expectedDuration time.Duration
	}

	cases := []databaseCase{
		{
			description: "closes successfully",
			closeFn:     func() error { return nil },
		},
		{
			description: "returns database error",
			closeFn:     func() error { return errors.New("boom") },
			expectedErr: "boom",
		},
		{
			description: "times out after 1s",
			closeFn: func() error {
				time.Sleep(1500 * time.Millisecond)
				return nil
			},
			expectedErr:      "timed out",
			expectedDuration: time.Second,
		},
		{
			description: "nil database",
			closeFn:     nil, // nil means database itself is nil
		},
	}

	for _, tt := range cases {
		t.Run(tt.description, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				var database db.Database
				if tt.closeFn != nil {
					ctrl := gomock.NewController(t)
					m := mocks.NewMockDatabase(ctrl)
					closeFn := tt.closeFn
					m.EXPECT().Close().DoAndReturn(func() error {
						return closeFn()
					})
					database = m
				}

				s := &Server{
					database: database,
					Logger:   logging.NewNoopLogger(t),
				}

				start := time.Now()
				err := s.closeDatabase(timeout)
				duration := time.Since(start)

				assert.Equal(t, tt.expectedDuration, duration)

				//nolint:testifylint // testing error behavior, not precondition
				if tt.expectedErr == "" {
					assert.NoError(t, err)
				} else {
					assert.ErrorContains(t, err, tt.expectedErr)
				}

				// Make sure enough fake time so nothing is left running
				time.Sleep(2 * time.Second)
			})
		})
	}
}

func TestDurableStorageServer(t *testing.T) {
	for _, disabled := range []bool{false, true} {
		t.Run(map[bool]string{false: "project locks", true: "repository locking disabled"}[disabled], func(t *testing.T) {
			s, err := NewServer(UserConfig{Experiments: []string{"durable-storage"}, Storage: &StorageConfig{Database: StorageDatabaseConfig{Type: "sqlite"}}, DataDir: t.TempDir(),
				AllowCommands: "plan,apply", AtlantisURL: "http://example.com", GithubHostname: "github.com", GithubUser: "user", DisableRepoLocking: disabled}, Config{AtlantisVersion: "test"})
			Ok(t, err)
			t.Cleanup(func() { Ok(t, s.closeDatabase(time.Second)); Ok(t, s.StatsCloser.Close()) })
			_, ok := s.database.(*sqlstore.Store)
			Assert(t, ok, "server selected another database")
			Assert(t, s.LocksController.Durable, "locks controller kept the legacy status update")
			p := models.PullRequest{Num: 1, BaseRepo: models.Repo{FullName: "owner/repo"}}
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

// A legacy server ignores durable-storage data in its data directory, starts on
// its legacy backend, and warns that the recorded locks and plans are unused.
func TestLegacyServerWarnsAndIgnoresDurableDataDirectory(t *testing.T) {
	for _, want := range []string{"durable-storage data", "ignores", "locks and plans"} {
		Assert(t, strings.Contains(ignoredDurableDataWarning, want), "warning %q does not mention %q", ignoredDurableDataWarning, want)
	}
	Assert(t, !strings.Contains(ignoredDurableDataWarning, ":"), "log message must not contain colons")
	Equals(t, strings.ToLower(ignoredDurableDataWarning), ignoredDurableDataWarning)

	dir := t.TempDir()
	store, err := sqlstore.OpenServer(dir)
	Ok(t, err)
	Ok(t, store.Close())
	// The structured logger writes to stderr, which it resolves when it is built.
	captured, err := os.CreateTemp(t.TempDir(), "stderr")
	Ok(t, err)
	stderr := os.Stderr
	os.Stderr = captured
	s, err := NewServer(UserConfig{DataDir: dir, LockingDBType: "boltdb", AllowCommands: "plan,apply", AtlantisURL: "http://example.com", GithubHostname: "github.com", GithubUser: "user"}, Config{AtlantisVersion: "test"})
	os.Stderr = stderr
	Ok(t, err)
	t.Cleanup(func() { Ok(t, s.closeDatabase(time.Second)); Ok(t, s.StatsCloser.Close()) })
	_, durable := s.database.(*sqlstore.Store)
	Assert(t, !durable, "legacy server opened the durable database")
	Ok(t, captured.Close())
	logged, err := os.ReadFile(captured.Name())
	Ok(t, err)
	Assert(t, strings.Contains(string(logged), ignoredDurableDataWarning), "warning was not logged: %s", logged)
}

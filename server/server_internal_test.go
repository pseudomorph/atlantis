// Copyright 2017 HootSuite Media Inc.
// SPDX-License-Identifier: Apache-2.0
// Modified hereafter by contributors to runatlantis/atlantis.

package server

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/gorilla/mux"
	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/core/db/mocks"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/core/sqlstore/sqlstoretest"
	"github.com/runatlantis/atlantis/server/events"
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

// Servers may share a PostgreSQL schema. Starting one leaves claims held by
// another server active, since that server may still be running them; the
// expiry sweep handles claims of a server that stopped. Each server needs its
// own data directory.
func TestDurableStorageStartupOnPostgres(t *testing.T) {
	dsn := sqlstoretest.PostgresDSN(t)
	previous, err := sqlstore.OpenPostgresServer(dsn, t.TempDir())
	Ok(t, err)
	pull := models.PullRequest{Num: 1, BaseRepo: models.Repo{FullName: "owner/repo"}, Execution: &models.Execution{Owner: "previous-owner"}}
	Ok(t, previous.AcquireClaim(pull, pull.ExecutionOwner()))
	Ok(t, previous.Close())
	t.Setenv("ATLANTIS_TEST_STORAGE_DSN", dsn)
	config := func(dataDir string) UserConfig {
		return UserConfig{Experiments: []string{"durable-storage"}, Storage: &StorageConfig{Database: StorageDatabaseConfig{Type: "postgres", ConnectionStringEnv: "ATLANTIS_TEST_STORAGE_DSN"}}, DataDir: dataDir,
			AllowCommands: "plan,apply", AtlantisURL: "http://example.com", GithubHostname: "github.com", GithubUser: "user"}
	}
	dataDir := t.TempDir()
	s, err := NewServer(config(dataDir), Config{AtlantisVersion: "test"})
	Ok(t, err)
	t.Cleanup(func() { Ok(t, s.closeDatabase(time.Second)); Ok(t, s.StatsCloser.Close()) })
	other, err := NewServer(config(t.TempDir()), Config{AtlantisVersion: "test"})
	Ok(t, err)
	t.Cleanup(func() { Ok(t, other.closeDatabase(time.Second)); Ok(t, other.StatsCloser.Close()) })
	for _, server := range []*Server{s, other} {
		store := server.database.(*sqlstore.Store)
		Ok(t, store.CheckClaim(pull))
		Assert(t, store.AcquireClaim(pull, "replacement") != nil, "startup took over another server's claim")
	}
	_, err = NewServer(config(dataDir), Config{AtlantisVersion: "test"})
	ErrContains(t, "already in use by another server", err)
}

func TestDurableRecoveryRouteUsesProductionGate(t *testing.T) {
	for _, durable := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "durable"}[durable], func(t *testing.T) {
			config := UserConfig{Experiments: []string{"api"}, APISecret: "secret", DataDir: t.TempDir(), AllowCommands: "plan,apply", AtlantisURL: "http://example.com", GithubHostname: "github.com", GithubUser: "user"}
			if durable {
				config.Experiments = append(config.Experiments, "durable-storage")
				config.Storage = &StorageConfig{Database: StorageDatabaseConfig{Type: "sqlite"}}
			}
			s, err := NewServer(config, Config{AtlantisVersion: "test"})
			Ok(t, err)
			t.Cleanup(func() { Ok(t, s.closeDatabase(time.Second)); Ok(t, s.StatsCloser.Close()) })
			s.SetupRoutes()
			request := httptest.NewRequest(http.MethodPost, "/api/jobs/00000000-0000-0000-0000-000000000001/unlock", nil)
			var matched mux.RouteMatch
			Equals(t, durable, s.Router.Match(request, &matched))
			if durable {
				response := httptest.NewRecorder()
				s.Router.ServeHTTP(response, request)
				Equals(t, http.StatusUnauthorized, response.Code)
			}
		})
	}
}

func TestServerUnlockRecoveryParserGate(t *testing.T) {
	for _, durable := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "durable"}[durable], func(t *testing.T) {
			config := UserConfig{ExecutableName: "atlantis", DataDir: t.TempDir(), AllowCommands: "plan,apply,unlock", AtlantisURL: "http://example.com", GithubHostname: "github.com", GithubUser: "user"}
			if durable {
				config.Experiments = []string{"durable-storage"}
				config.Storage = &StorageConfig{Database: StorageDatabaseConfig{Type: "sqlite"}}
			}
			s, err := NewServer(config, Config{AtlantisVersion: "test"})
			Ok(t, err)
			t.Cleanup(func() { Ok(t, s.closeDatabase(time.Second)); Ok(t, s.StatsCloser.Close()) })
			parser := s.VCSEventsController.CommentParser
			parsed := parser.Parse("atlantis unlock --confirm-stopped", models.Github)
			Equals(t, durable, parsed.Command != nil)
			help := parser.Parse("atlantis unlock --help", models.Github)
			Equals(t, durable, strings.Contains(help.CommentResponse, "--confirm-stopped"))
		})
	}
}

func TestStorageMaintenanceParticipatesInDraining(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		started := make(chan struct{})
		release := make(chan struct{})
		releaseOperation := sync.OnceFunc(func() { close(release) })
		defer releaseOperation()
		var calls atomic.Int32
		s := &Server{Drainer: &events.Drainer{}, storageMaintenance: func() {
			if calls.Add(1) == 1 {
				close(started)
				<-release
			}
		}}
		go s.runStorageMaintenance()
		<-started
		Equals(t, 1, s.Drainer.GetStatus().InProgressOps)
		drained := make(chan struct{})
		go func() { s.Drainer.ShutdownBlocking(); close(drained) }()
		synctest.Wait()
		Assert(t, s.Drainer.GetStatus().ShuttingDown, "drain did not close admission")
		select {
		case <-drained:
			t.Fatal("shutdown completed while maintenance was still using storage")
		default:
		}
		s.runStorageMaintenance()
		Equals(t, int32(1), calls.Load())
		releaseOperation()
		<-drained
		synctest.Wait()
		Equals(t, 0, s.Drainer.GetStatus().InProgressOps)
		s.runStorageMaintenance()
		Equals(t, int32(1), calls.Load())
	})
}

func TestStorageHeartbeatContinuesWhileDraining(t *testing.T) {
	var calls atomic.Int32
	s := &Server{Drainer: &events.Drainer{}, storageHeartbeat: func() { calls.Add(1) }}
	Assert(t, s.Drainer.StartOp(), "drainer refused an operation")
	go s.Drainer.ShutdownBlocking()
	for !s.Drainer.GetStatus().ShuttingDown {
		time.Sleep(time.Millisecond)
	}
	// Draining commands still hold claims, so their heartbeats must continue.
	s.runStorageHeartbeat()
	Equals(t, int32(1), calls.Load())
	s.Drainer.OpDone()
}

func TestDurableStorageRejectsLegacyExternalPlanStore(t *testing.T) {
	_, err := NewServer(UserConfig{Experiments: []string{"durable-storage"}, Storage: &StorageConfig{Database: StorageDatabaseConfig{Type: "sqlite"}}, DataDir: t.TempDir(),
		RepoConfigJSON: `{"repos":[{"id":"/.*/"}],"external_stores":{"plan_store":{"type":"s3","s3":{"bucket":"plans","region":"us-east-1"}}}}`,
		AllowCommands:  "plan,apply", AtlantisURL: "http://example.com", GithubHostname: "github.com", GithubUser: "user"}, Config{AtlantisVersion: "test"})
	ErrContains(t, "storage cannot be combined with external_stores.plan_store", err)
}

func TestExternalStoresLogsDeprecationWarning(t *testing.T) {
	// The structured logger writes to stderr, which it resolves when it is built.
	captured, err := os.CreateTemp(t.TempDir(), "stderr")
	Ok(t, err)
	stderr := os.Stderr
	os.Stderr = captured
	_, err = NewServer(UserConfig{DataDir: t.TempDir(), EnableExternalStores: true, AllowCommands: "plan,apply", AtlantisURL: "http://example.com", GithubHostname: "github.com", GithubUser: "user"}, Config{AtlantisVersion: "test"})
	os.Stderr = stderr
	// No plan store is configured, so startup stops after the warning.
	ErrContains(t, "no external_stores.plan_store.type is configured", err)
	Ok(t, captured.Close())
	logged, err := os.ReadFile(captured.Name())
	Ok(t, err)
	Assert(t, strings.Contains(string(logged), externalStoresDeprecationWarning), "warning was not logged: %s", logged)
}

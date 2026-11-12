// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/runatlantis/atlantis/server/core/artifacts"
	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/core/logs"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/jobs"
	"github.com/runatlantis/atlantis/server/logging"
)

// openDurableStore opens the configured durable-storage database.
func openDurableStore(config StorageDatabaseConfig, dataDir string) (*sqlstore.Store, error) {
	if config.Type != "postgres" {
		return sqlstore.OpenServer(dataDir)
	}
	connString, err := config.ConnectionString()
	if err != nil {
		return nil, err
	}
	return sqlstore.OpenPostgresServer(connString, dataDir)
}

// sharedArtifactsWarning is logged when PostgreSQL uses filesystem artifacts
// inside the data directory. Each server has its own data directory, so other
// servers sharing the database cannot read those artifacts.
func sharedArtifactsWarning(config UserConfig) string {
	if config.Storage == nil || config.Storage.Database.Type != "postgres" || config.Storage.Artifacts.Type != "filesystem" {
		return ""
	}
	dataDir, err := filepath.Abs(config.DataDir)
	if err != nil {
		return ""
	}
	directory, err := filepath.Abs(config.Storage.ArtifactDirectory(config.DataDir))
	if err != nil {
		return ""
	}
	relative, err := filepath.Rel(dataDir, directory)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return ""
	}
	return "filesystem artifacts are inside data-dir, which belongs to this server; " +
		"servers sharing the postgres database must all use one artifact directory on a shared disk with atomic rename"
}

type storageRuntime struct {
	Store               *sqlstore.Store
	Claims              db.ExecutionStore
	Execution           *events.ExecutionCoordinator
	Artifacts           *artifacts.Service
	Jobs                *jobs.Service
	Logs                *logs.Service
	logs, planArtifacts time.Duration
}

func newStorageRuntime(store *sqlstore.Store, config UserConfig) (*storageRuntime, error) {
	var raw artifacts.Store
	var blobs artifacts.SnapshotBackend
	var err error
	switch config.Storage.Artifacts.Type {
	case "", "database":
		blobs = sqlstore.BlobStore{Store: store}
	case "filesystem":
		raw, err = artifacts.NewFilesystem(config.Storage.ArtifactDirectory(config.DataDir))
	}
	if err != nil {
		return nil, fmt.Errorf("opening artifact storage: %w", err)
	}
	// Running output always goes to the database journal. An object store, when
	// configured, receives one write-once archive per sealed log under logs/v1,
	// outside the snapshot namespace that orphan collection sweeps.
	journal := &logs.Service{Records: store, Archive: raw}
	if raw != nil {
		blobs = artifacts.ObjectSnapshots{Store: raw}
	}
	if blobs == nil {
		return nil, fmt.Errorf("unsupported artifact storage type %q", config.Storage.Artifacts.Type)
	}
	if err = store.MarkPreviousExecutionsUncertain(); err != nil {
		_ = blobs.Close()
		return nil, err
	}
	logRetention, planArtifacts, err := config.Storage.Durations()
	if err != nil {
		_ = blobs.Close()
		return nil, err
	}
	files := &artifacts.Service{Records: store, Blobs: blobs, WorkflowFiles: slices.Clone(config.Storage.WorkflowFiles)}
	return &storageRuntime{
		Store:         store,
		Claims:        store,
		Execution:     &events.ExecutionCoordinator{Store: store},
		Artifacts:     files,
		Jobs:          &jobs.Service{Store: store, Logs: journal},
		Logs:          journal,
		logs:          logRetention,
		planArtifacts: planArtifacts,
	}, nil
}

// Heartbeat refreshes the claims this server instance holds, then marks claims
// whose heartbeat has expired uncertain. With SQLite, startup already made
// earlier claims uncertain, so the sweep only finds claims whose own refresh
// stopped. With PostgreSQL every server sweeps, which is how the claims of a
// server that stopped become uncertain.
func (s *storageRuntime) Heartbeat(logger logging.SimpleLogging) func() {
	return func() {
		if _, err := s.Store.RefreshClaimHeartbeats(); err != nil {
			logger.Err("refreshing execution claim heartbeats: %v", err)
		}
		expired, err := s.Store.ExpireClaims()
		if err != nil {
			logger.Err("sweeping expired execution claims: %v", err)
		}
		if expired > 0 {
			logger.Warn("marked %d expired execution claims uncertain, verify stopped work and recover with unlock --confirm-stopped", expired)
		}
	}
}

// Maintain runs each maintenance pass under its own lock, so with servers
// sharing PostgreSQL one server runs a given pass at a time. Log archiving and
// retention share one lock: retention deletes the archive object of an expired
// log, and an archive write still in flight on another server could land after
// that delete.
func (s *storageRuntime) Maintain(worker *events.PullClosedExecutor, logger logging.SimpleLogging) func() {
	return func() {
		s.maintenancePass("lifecycle verification", logger, func() { s.verifyLifecycles(worker, logger) })
		s.maintenancePass("deferred cleanup", logger, func() { s.cleanUpClosedPulls(worker, logger) })
		s.maintenancePass("log archiving and retention", logger, func() {
			if err := s.Logs.ArchiveSealed(); err != nil {
				logger.Err("archiving completed job output: %v", err)
			}
			s.collectExpired(logger)
		})
	}
}

func (s *storageRuntime) maintenancePass(name string, logger logging.SimpleLogging, pass func()) {
	ran, err := s.Store.RunMaintenance(name, pass)
	if err != nil {
		logger.Err("starting %s: %v", name, err)
	} else if !ran {
		logger.Debug("skipping %s, another server is running it", name)
	}
}

func (s *storageRuntime) verifyLifecycles(worker *events.PullClosedExecutor, logger logging.SimpleLogging) {
	pending, err := s.Store.PendingLifecycleChecks()
	if err != nil {
		logger.Err("listing closure verification: %v", err)
		return
	}
	for _, pull := range pending {
		if worker.LivePullHeadFetcher == nil {
			logger.Err("closure verification requires a live VCS reader")
			continue
		}
		current, err := worker.LivePullHeadFetcher.GetLivePullIdentity(command.ProjectContext{Pull: pull, Log: logger})
		if err != nil {
			logger.Err("verifying queued closure: %v", err)
			continue
		}
		if current.State == models.ClosedPullState {
			err = s.Store.ClosePull(current)
		} else {
			err = s.Store.RequestReopen(current)
		}
		if err != nil {
			logger.Err("recording verified lifecycle: %v", err)
			continue
		}
		if err := s.Store.ResolveLifecycleCheck(pull); err != nil {
			logger.Err("clearing lifecycle verification: %v", err)
		}
	}
}

func (s *storageRuntime) cleanUpClosedPulls(worker *events.PullClosedExecutor, logger logging.SimpleLogging) {
	ready, err := s.Store.ClosedPullsReady()
	if err != nil {
		logger.Err("listing deferred cleanup: %v", err)
		return
	}
	for _, pull := range ready {
		// CleanUpPull records completion, which lets a reopened pull request claim work.
		if err := worker.CleanUpPull(logger, pull.BaseRepo, pull); err != nil {
			logger.Err("deferred cleanup: %v", err)
		}
	}
}

func (s *storageRuntime) collectExpired(logger logging.SimpleLogging) {
	now, err := s.Store.Now()
	if err != nil {
		logger.Err("reading retention clock: %v", err)
		return
	}
	if err := s.Logs.Collect(now, s.logs); err != nil {
		logger.Err("collecting expired job output: %v", err)
	}
	if err := s.Artifacts.Collect(now, s.planArtifacts); err != nil {
		logger.Err("collecting expired artifacts: %v", err)
	}
	if err := s.Store.CollectDeliveries(now, sqlstore.DeliveryRetention); err != nil {
		logger.Err("collecting webhook deliveries: %v", err)
	}
}

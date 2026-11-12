// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"fmt"
	"slices"
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
	case "s3":
		options := artifacts.S3Config(config.Storage.Artifacts.S3)
		client, clientErr := artifacts.NewS3Client(options)
		err = clientErr
		if err == nil {
			raw = artifacts.NewS3WithClient(client, options.Bucket, options.Prefix)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("opening artifact storage: %w", err)
	}
	journal := &logs.Service{Records: store, Mode: config.Storage.Artifacts.Type}
	switch journal.Mode {
	case "":
		journal.Mode = "database"
	case "filesystem":
		journal.Directory = config.Storage.ArtifactDirectory(config.DataDir)
	case "s3":
		// Log keys occupy logs/v1, outside artifact orphan collection.
		journal.Archive = raw
	}
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
		Jobs:          &jobs.Service{Store: store, Artifacts: files, Logs: journal},
		Logs:          journal,
		logs:          logRetention,
		planArtifacts: planArtifacts,
	}, nil
}

func (s *storageRuntime) Maintain(worker *events.PullClosedExecutor, logger logging.SimpleLogging) func() {
	return func() {
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
		now, err := s.Store.Now()
		if err != nil {
			logger.Err("reading retention clock: %v", err)
			return
		}
		if err := s.Logs.ArchiveSealed(); err != nil {
			logger.Err("archiving completed job output: %v", err)
		}
		if err := s.Logs.Collect(now, s.logs); err != nil {
			logger.Err("collecting expired job output: %v", err)
		}
		if err := s.Artifacts.Collect(now, s.logs, s.planArtifacts); err != nil {
			logger.Err("collecting expired artifacts: %v", err)
		}
	}
}

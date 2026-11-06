// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"fmt"
	"slices"
	"time"

	"github.com/runatlantis/atlantis/server/core/artifacts"
	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/jobs"
	"github.com/runatlantis/atlantis/server/logging"
)

type storageRuntime struct {
	Store               *sqlstore.Store
	Claims              db.ExecutionStore
	Execution           *events.ExecutionCoordinator
	Artifacts           *artifacts.Service
	Jobs                *jobs.Service
	logs, planArtifacts time.Duration
}

func newStorageRuntime(store *sqlstore.Store, config UserConfig, _ logging.SimpleLogging) (*storageRuntime, error) {
	var blobs artifacts.Store
	var err error
	switch config.Storage.Artifacts.Type {
	case "", "database":
		blobs = sqlstore.BlobStore{Store: store}
	case "filesystem":
		blobs, err = artifacts.NewFilesystem(config.Storage.ArtifactDirectory(config.DataDir))
	case "s3":
		options := artifacts.S3Config(config.Storage.Artifacts.S3)
		client, clientErr := artifacts.NewS3Client(options)
		err = clientErr
		if err == nil {
			blobs = artifacts.NewS3WithClient(client, options.Bucket, options.Prefix)
		}
	}
	if err != nil {
		return nil, fmt.Errorf("opening artifact storage: %w", err)
	}
	blobs = artifacts.Scoped{Store: blobs, Prefix: "artifacts/v1", OwnsStore: true}
	if err = store.MarkPreviousExecutionsUncertain(); err != nil {
		_ = blobs.Close()
		return nil, err
	}
	logs, planArtifacts, err := config.Storage.Durations()
	if err != nil {
		_ = blobs.Close()
		return nil, err
	}
	files := &artifacts.Service{Records: store, Blobs: blobs, WorkflowFiles: slices.Clone(config.Storage.WorkflowFiles)}
	return &storageRuntime{Store: store, Claims: store, Execution: &events.ExecutionCoordinator{Store: store}, Artifacts: files, Jobs: &jobs.Service{Store: store, Artifacts: files}, logs: logs, planArtifacts: planArtifacts}, nil
}

func (s *storageRuntime) Maintain(worker *events.PullClosedExecutor, logger logging.SimpleLogging) func() {
	return func() {
		ready, err := s.Store.ClosedPullsReady()
		if err != nil {
			logger.Err("listing deferred cleanup: %v", err)
			return
		}
		for _, pull := range ready {
			if err := worker.CleanUpPull(logger, pull.BaseRepo, pull); err != nil {
				logger.Err("deferred cleanup: %v", err)
			} else if err := s.Store.MarkClosedClean(pull); err != nil {
				logger.Err("recording cleanup: %v", err)
			}
		}
		now, err := s.Store.Now()
		if err != nil {
			logger.Err("reading retention clock: %v", err)
			return
		}
		if err := s.Artifacts.Collect(now, s.logs, s.planArtifacts); err != nil {
			logger.Err("collecting expired artifacts: %v", err)
		}
	}
}

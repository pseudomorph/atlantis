// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package server

import (
	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/jobs"
	"github.com/runatlantis/atlantis/server/logging"
)

type storageRuntime struct {
	Store     *sqlstore.Store
	Claims    db.ExecutionStore
	Execution *events.ExecutionCoordinator
	Jobs      *jobs.Service
}

func newStorageRuntime(store *sqlstore.Store) (*storageRuntime, error) {
	// Claims left active by the previous server may still have running Terraform children.
	if err := store.MarkPreviousExecutionsUncertain(); err != nil {
		return nil, err
	}
	return &storageRuntime{
		Store:     store,
		Claims:    store,
		Execution: &events.ExecutionCoordinator{Store: store},
		Jobs:      &jobs.Service{Store: store},
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
	}
}

// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package controllers

import (
	"testing"

	"github.com/runatlantis/atlantis/server/core/locking"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

type reservationRecorder struct {
	locking.Locker
	calls int
	repo  models.Repo
}

func (r *reservationRecorder) UnlockByPull(repo models.Repo, _ int) ([]models.ProjectLock, error) {
	r.calls++
	r.repo = repo
	return nil, nil
}

func TestAPICleanupPreservesPRReservations(t *testing.T) {
	store, err := sqlstore.OpenServer(t.TempDir())
	Ok(t, err)
	defer store.Close()
	locker := &reservationRecorder{}
	controller := &APIController{Locker: locker, Execution: &events.ExecutionCoordinator{Store: store}}
	controller.cleanupProjectLocks(&command.Context{Pull: models.PullRequest{Num: 42}, Log: logging.NewNoopLogger(t)})
	Equals(t, 0, locker.calls)
}

func TestAPICleanupRequiresPersistedCompletion(t *testing.T) {
	store, err := sqlstore.OpenServer(t.TempDir())
	Ok(t, err)
	defer store.Close()
	locker := &reservationRecorder{}
	controller := &APIController{Locker: locker, Execution: &events.ExecutionCoordinator{Store: store}}
	pull := models.PullRequest{Num: -1, BaseRepo: models.Repo{FullName: "org/repo"}, Execution: &models.Execution{Owner: "owner"}}
	Ok(t, store.AcquireClaim(pull, "owner"))
	pull.BaseRepo.Execution = pull.Execution
	job := models.Job{ID: "apply", Pull: pull, Project: models.NewProject("org/repo", ".", ""), Workspace: "default", Command: "apply"}
	Ok(t, store.StartJob(job))
	ctx := &command.Context{Pull: pull, HeadRepo: models.Repo{FullName: "fork/repo"}, Log: logging.NewNoopLogger(t)}
	controller.cleanupProjectLocks(ctx)
	Equals(t, 0, locker.calls)
	Ok(t, store.FinishJob(job.ID, "succeeded", ""))
	controller.cleanupProjectLocks(ctx)
	Equals(t, 1, locker.calls)
	Equals(t, "org/repo", locker.repo.FullName)
	Ok(t, store.MarkUncertain(pull))
	controller.cleanupProjectLocks(ctx)
	Equals(t, 1, locker.calls)
}

func TestLegacyAPICleanupUsesBaseRepository(t *testing.T) {
	locker := &reservationRecorder{}
	controller := &APIController{Locker: locker}
	controller.cleanupProjectLocks(&command.Context{Pull: models.PullRequest{Num: 42, BaseRepo: models.Repo{FullName: "base/repo"}}, HeadRepo: models.Repo{FullName: "fork/repo"}, Log: logging.NewNoopLogger(t)})
	Equals(t, 1, locker.calls)
	Equals(t, "base/repo", locker.repo.FullName)
}

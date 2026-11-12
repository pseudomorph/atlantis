// Copyright 2025 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"fmt"
	"strings"

	"github.com/runatlantis/atlantis/server/events/command"

	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/core/locking"
	"github.com/runatlantis/atlantis/server/core/planstore"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
)

//go:generate go tool pegomock generate github.com/runatlantis/atlantis/server/events --package mocks -o mocks/mock_delete_lock_command.go DeleteLockCommand

// DeleteLockCommand is the first step after a command request has been parsed.
type DeleteLockCommand interface {
	DeleteLock(logger logging.SimpleLogging, id string) (*models.ProjectLock, error)
	DeleteLocksByPull(logger logging.SimpleLogging, repo models.Repo, pullNum int) (int, error)
}

// DefaultDeleteLockCommand deletes a specific lock after a request from the LocksController.
type DefaultDeleteLockCommand struct {
	Execution        *ExecutionCoordinator
	Locker           locking.Locker
	WorkingDir       WorkingDir
	WorkingDirLocker WorkingDirLocker
	Database         db.Database
	PlanStore        planstore.PlanStore
}

// DeleteLock handles deleting the lock at id
func (l *DefaultDeleteLockCommand) DeleteLock(logger logging.SimpleLogging, id string) (*models.ProjectLock, error) {
	var lock *models.ProjectLock
	var err error
	if l.Execution != nil {
		existing, e := l.Locker.GetLock(id)
		if e != nil || existing == nil {
			return existing, e
		}
		if existing.Pull.Num < 0 {
			id := "{job-id}"
			if reader, ok := l.Execution.Store.(interface {
				RecoveryJob(models.Repo, int) (string, error)
			}); ok {
				candidate, err := reader.RecoveryJob(existing.Pull.BaseRepo, existing.Pull.Num)
				if err != nil {
					return nil, err
				}
				if candidate != "" {
					id = candidate
				}
			}
			return nil, fmt.Errorf("API invocation reservations require authenticated POST /api/jobs/%s/unlock with a stopped-work attestation", id)
		}
		ctx := &command.Context{Pull: existing.Pull, Log: logger}
		release, e := l.Execution.Acquire(ctx)
		if e != nil {
			return nil, e
		}
		defer release()
		lock, err = l.Locker.UnlockOwned(id, ctx.Pull)
	} else {
		lock, err = l.Locker.Unlock(id)
	}
	if err != nil {
		return nil, err
	}
	if lock == nil {
		return nil, nil
	}

	removeErr := l.WorkingDir.DeletePlan(logger, lock.Pull.BaseRepo, lock.Pull, lock.Workspace, lock.Project.Path, lock.Project.ProjectName)
	if removeErr != nil {
		logger.Warn("Failed to delete plan: %s", removeErr)
		return nil, removeErr
	}

	if l.PlanStore != nil {
		if err := l.PlanStore.DeletePlanForProject(lock.Pull.BaseRepo, lock.Pull.Num, lock.Workspace, lock.Project.Path, lock.Project.ProjectName); err != nil {
			logger.Warn("Failed to delete plan from external store: %s", err)
		}
	}

	return lock, nil
}

// DeleteLocksByPull handles deleting all locks for the pull request
func (l *DefaultDeleteLockCommand) DeleteLocksByPull(logger logging.SimpleLogging, repo models.Repo, pullNum int) (int, error) {
	locks, err := l.Locker.UnlockByPull(repo, pullNum)
	numLocks := len(locks)
	if err != nil {
		return numLocks, err
	}

	for i := range numLocks {
		lock := locks[i]

		err := l.WorkingDir.DeletePlan(logger, lock.Pull.BaseRepo, lock.Pull, lock.Workspace, lock.Project.Path, lock.Project.ProjectName)
		if err != nil {
			logger.Warn("Failed to delete plan: %s", err)
			return numLocks, err
		}
	}

	// Always clean up the external plan store for this pull, even when no
	// locks were found locally. Locks can be cleaned via other paths (manual
	// unlock, partial failure) which would otherwise leave orphaned S3 plans.
	if l.PlanStore != nil {
		_, _, ok := strings.Cut(repo.FullName, "/")
		if !ok {
			logger.Warn("cannot parse owner/repo from %q; skipping external plan store cleanup", repo.FullName)
		} else if err := l.PlanStore.DeleteForPull(repo, pullNum); err != nil {
			logger.Warn("Failed to delete plans from external store: %s", err)
		}
	}

	if numLocks == 0 {
		logger.Debug("No locks found for repo '%v', pull request: %v", repo.FullName, pullNum)
	}

	return numLocks, nil
}

// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"os"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
)

type ExecutionCoordinator struct {
	Store   db.ExecutionStore
	Drainer *Drainer
	// WorkingDir holds this server's checkouts. Durable state lives in the
	// database and artifact storage, so a checkout lives for one command: it
	// is removed when the command takes ownership and again before it gives
	// ownership up. Any server may run the next command for the pull, and
	// nothing it reads can be left over from an earlier command.
	WorkingDir WorkingDir
	// NewAPIPullNumber supplies replacement candidates after an identity collision.
	NewAPIPullNumber func() int
}

// Acquire owns the whole command, including hooks, policy, publication and cleanup.
func (s *ExecutionCoordinator) Acquire(ctx *command.Context) (func(), error) {
	return s.acquire(ctx, false, false)
}

// AcquireUnlock gives reconciliation its own admission path, including closed PRs.
func (s *ExecutionCoordinator) AcquireUnlock(ctx *command.Context, confirmedStopped bool) (func(), error) {
	return s.acquire(ctx, true, confirmedStopped)
}

func (s *ExecutionCoordinator) acquire(ctx *command.Context, unlock, confirmedStopped bool) (func(), error) {
	if ctx.Pull.ExecutionOwner() != "" {
		if err := s.Store.CheckClaim(ctx.Pull); err != nil {
			return nil, err
		}
		return func() {}, nil
	}
	// Include API executions in the same shutdown drain as webhook commands.
	finishDrain := func() {}
	if s.Drainer != nil {
		if !s.Drainer.StartOp() {
			return nil, fmt.Errorf("server is shutting down")
		}
		finishDrain = s.Drainer.OpDone
	}
	handle := &models.Execution{Owner: uuid.NewString()}
	ctx.Pull.Execution = handle
	ctx.Pull.BaseRepo.Execution = handle
	var claimErr error
	for attempt := 0; attempt < 8; attempt++ {
		if unlock {
			recovery, ok := s.Store.(interface {
				AcquireUnlockClaim(models.PullRequest, string, string, bool) error
			})
			if !ok {
				claimErr = fmt.Errorf("execution recovery is unavailable")
			} else {
				claimErr = recovery.AcquireUnlockClaim(ctx.Pull, handle.Owner, ctx.User.Username, confirmedStopped)
			}
		} else {
			claimErr = s.Store.AcquireClaim(ctx.Pull, handle.Owner)
		}
		if !errors.Is(claimErr, db.ErrSyntheticPullCollision) || !ctx.API || ctx.Pull.Num >= 0 {
			break
		}
		next := s.NewAPIPullNumber
		if next == nil {
			next = newAPIPullNumber
		}
		ctx.Pull.Num = next()
		if ctx.Pull.Num >= 0 {
			claimErr = fmt.Errorf("invalid synthetic API identity")
			break
		}
	}
	if claimErr != nil {
		finishDrain()
		ctx.Pull.Execution = nil
		ctx.Pull.BaseRepo.Execution = nil
		return nil, claimErr
	}
	pull := ctx.Pull
	if err := s.removeCheckout(ctx.Log, pull); err != nil {
		if releaseErr := s.Store.ReleaseClaim(pull); releaseErr != nil {
			ctx.Log.Err("releasing execution claim: %v", releaseErr)
		}
		finishDrain()
		ctx.Pull.Execution = nil
		ctx.Pull.BaseRepo.Execution = nil
		return nil, fmt.Errorf("removing previous checkout: %w", err)
	}
	handle.Release = func() {
		defer finishDrain()
		if err := s.removeCheckout(ctx.Log, pull); err != nil {
			ctx.Log.Err("removing checkout: %v", err)
		}
		if err := s.Store.ReleaseClaim(pull); err != nil {
			ctx.Log.Err("releasing execution claim: %v", err)
			if markErr := s.Store.MarkUncertain(pull); markErr != nil {
				ctx.Log.Err("recording uncertain execution: %v", markErr)
			}
		}
	}
	return handle.Close, nil
}

// removeCheckout deletes this server's checkout of the pull, if it has one.
func (s *ExecutionCoordinator) removeCheckout(logger logging.SimpleLogging, pull models.PullRequest) error {
	if s.WorkingDir == nil {
		return nil
	}
	if _, err := s.WorkingDir.GetPullDir(pull.BaseRepo, pull); errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return s.WorkingDir.Delete(logger, pull.BaseRepo, pull)
}

func (s *ExecutionCoordinator) Check(ctx command.ProjectContext) error {
	if err := s.Store.CheckClaim(ctx.Pull); err != nil {
		return err
	}
	cancelled, err := s.Store.IsCancelled(ctx.Pull)
	if err != nil {
		return err
	}
	if cancelled {
		return fmt.Errorf("execution cancelled or pull request closed")
	}
	return nil
}

func newAPIPullNumber() int {
	id := uuid.New()
	n := int(binary.BigEndian.Uint64(id[:8]) & uint64(math.MaxInt))
	if n == 0 {
		n = 1
	}
	return -n
}

// RecoverAPIJob addresses existing API ownership by job UUID. It never enters
// fresh-request allocation, including when the invocation has a negative PR ID.
func (s *ExecutionCoordinator) RecoverAPIJob(id, actor string, logger logging.SimpleLogging) (models.PullRequest, func(), error) {
	finish := func() {}
	if s.Drainer != nil {
		if !s.Drainer.StartOp() {
			return models.PullRequest{}, nil, fmt.Errorf("server is shutting down")
		}
		finish = s.Drainer.OpDone
	}
	store, ok := s.Store.(interface {
		AcquireAPIJobUnlock(string, string, string, bool) (models.PullRequest, error)
	})
	if !ok {
		finish()
		return models.PullRequest{}, nil, fmt.Errorf("API job recovery is unavailable")
	}
	pull, err := store.AcquireAPIJobUnlock(id, "recovery:"+uuid.NewString(), actor, true)
	if err != nil {
		finish()
		return models.PullRequest{}, nil, err
	}
	handle := pull.Execution
	handle.Release = func() {
		defer finish()
		if err := s.Store.ReleaseClaim(pull); err != nil {
			logger.Err("releasing API recovery ownership: %v", err)
			if err := s.Store.MarkUncertain(pull); err != nil {
				logger.Err("marking API recovery uncertain: %v", err)
			}
		}
	}
	return pull, handle.Close, nil
}

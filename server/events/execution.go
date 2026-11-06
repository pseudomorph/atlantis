// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"fmt"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
)

type ExecutionCoordinator struct {
	Store   db.ExecutionStore
	Drainer *Drainer
}

// Acquire owns the whole command, including hooks, policy, publication and cleanup.
func (s *ExecutionCoordinator) Acquire(ctx *command.Context) (func(), error) {
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
	scope := ctx.Pull.ExecutionScopeID()
	if ctx.Pull.Num < 0 {
		scope = "api:" + uuid.NewString()
	}
	handle := &models.Execution{Owner: uuid.NewString(), Scope: scope}
	ctx.Pull.Execution = handle
	ctx.Pull.BaseRepo.Execution = handle
	if err := s.Store.AcquireClaim(ctx.Pull, handle.Owner); err != nil {
		finishDrain()
		ctx.Pull.Execution = nil
		ctx.Pull.BaseRepo.Execution = nil
		return nil, err
	}
	pull := ctx.Pull
	handle.Release = func() {
		defer finishDrain()
		if err := s.Store.ReleaseClaim(pull); err != nil {
			ctx.Log.Err("releasing execution claim: %v", err)
			if markErr := s.Store.MarkUncertain(pull); markErr != nil {
				ctx.Log.Err("recording uncertain execution: %v", markErr)
			}
		}
	}
	return handle.Close, nil
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

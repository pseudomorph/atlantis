// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"testing"
	"time"

	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

func TestCancellationKeepsOwnershipUntilActiveWorkFinishes(t *testing.T) {
	for _, closed := range []bool{false, true} {
		t.Run(map[bool]string{false: "cancel", true: "close"}[closed], func(t *testing.T) {
			store, err := sqlstore.OpenServer(t.TempDir())
			Ok(t, err)
			defer store.Close()
			runner := &ExecutionCoordinator{Store: store}
			ctx := &command.Context{Pull: models.PullRequest{Num: 1, BaseRepo: models.Repo{FullName: "org/repo"}}, Log: logging.NewNoopLogger(t)}
			release, err := runner.Acquire(ctx)
			Ok(t, err)
			defer release()
			Assert(t, ctx.Pull.Execution == ctx.Pull.BaseRepo.Execution, "execution authority was copied")
			job := models.Job{ID: "active", Pull: ctx.Pull, Project: models.NewProject(ctx.Pull.BaseRepo.FullName, ".", ""), Workspace: "default", Command: "version", StartedAt: time.Now().UTC()}
			Ok(t, store.StartJob(job))
			if closed {
				Ok(t, store.ClosePull(ctx.Pull))
			} else {
				Ok(t, store.CancelPull(ctx.Pull))
			}
			Assert(t, runner.Check(command.ProjectContext{Pull: ctx.Pull}) != nil, "cancelled execution accepted queued work")
			Ok(t, store.CheckClaim(ctx.Pull))
			Assert(t, store.ReleaseClaim(ctx.Pull) != nil, "cancellation released unfinished work")
			Ok(t, store.FinishJob(job.ID, "succeeded", ""))
			release()
			release()
			idle, err := store.PullIdle(ctx.Pull)
			Ok(t, err)
			Assert(t, idle, "completed execution remained owned")
		})
	}
}

func TestExecutionDrainsBeforeStorageShutdown(t *testing.T) {
	store, err := sqlstore.OpenServer(t.TempDir())
	Ok(t, err)
	defer store.Close()
	drainer := &Drainer{}
	runner := &ExecutionCoordinator{Store: store, Drainer: drainer}
	ctx := &command.Context{Pull: models.PullRequest{Num: 1, BaseRepo: models.Repo{FullName: "org/repo"}}, Log: logging.NewNoopLogger(t)}
	release, err := runner.Acquire(ctx)
	Ok(t, err)
	defer release()
	Equals(t, 1, drainer.GetStatus().InProgressOps)
	// A rejected competing claim must not leak a drain operation.
	competitor := &command.Context{Pull: models.PullRequest{Num: 1, BaseRepo: ctx.Pull.BaseRepo}, Log: ctx.Log}
	competitor.Pull.BaseRepo.Execution = nil
	_, err = runner.Acquire(competitor)
	Assert(t, err != nil, "competing execution acquired ownership")
	Equals(t, 1, drainer.GetStatus().InProgressOps)
	drained := make(chan struct{})
	go func() { drainer.ShutdownBlocking(); close(drained) }()
	deadline := time.Now().Add(time.Second)
	for !drainer.GetStatus().ShuttingDown {
		if time.Now().After(deadline) {
			t.Fatal("shutdown did not start")
		}
		time.Sleep(time.Millisecond)
	}
	select {
	case <-drained:
		t.Fatal("shutdown passed an active execution")
	default:
	}
	rejected := &command.Context{Pull: models.PullRequest{Num: -2, BaseRepo: competitor.Pull.BaseRepo}, Log: ctx.Log}
	_, err = runner.Acquire(rejected)
	Assert(t, err != nil, "execution started during shutdown")
	release()
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("completed execution prevented shutdown")
	}
	Equals(t, 0, drainer.GetStatus().InProgressOps)
}

func TestAPICollisionRetriesBeforeReturningExecutionContext(t *testing.T) {
	store, err := sqlstore.OpenServer(t.TempDir())
	Ok(t, err)
	defer store.Close()
	repo := models.Repo{FullName: "org/repo"}
	historical := models.PullRequest{Num: -1, BaseRepo: repo, Execution: &models.Execution{Owner: "old"}}
	Ok(t, store.AcquireClaim(historical, "old"))
	Ok(t, store.ReleaseClaim(historical))
	attempts := 0
	coordinator := &ExecutionCoordinator{Store: store, NewAPIPullNumber: func() int { attempts++; return -2 }}
	ctx := &command.Context{Pull: models.PullRequest{Num: -1, BaseRepo: repo}, Log: logging.NewNoopLogger(t)}
	release, err := coordinator.Acquire(ctx)
	Ok(t, err)
	defer release()
	Equals(t, -2, ctx.Pull.Num)
	Equals(t, 1, attempts)
	Ok(t, store.CheckClaim(ctx.Pull))

	coordinator.NewAPIPullNumber = func() int { return -1 }
	_, err = coordinator.Acquire(&command.Context{Pull: models.PullRequest{Num: -1, BaseRepo: repo}, Log: ctx.Log})
	Assert(t, err != nil, "exhausted collision retries reused historical identity")
}

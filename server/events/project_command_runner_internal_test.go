// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"testing"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/core/config/valid"
	"github.com/runatlantis/atlantis/server/core/runtime"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/jobs"
	. "github.com/runatlantis/atlantis/testing"
)

func TestDurableRunnerRecordsActualExecution(t *testing.T) {
	db, err := sqlstore.OpenServer(t.TempDir())
	Ok(t, err)
	defer db.Close()
	service := &jobs.Service{Store: db}
	runner := &DefaultProjectCommandRunner{Jobs: service, Execution: &ExecutionCoordinator{Store: db}}
	repo := models.Repo{FullName: "org/repo", VCSHost: models.VCSHost{Hostname: "git.example"}}
	ctx := command.ProjectContext{JobID: uuid.NewString(), BaseRepo: repo, Pull: models.PullRequest{BaseRepo: repo, Num: 1, HeadCommit: "abc"}, RepoRelDir: ".", Workspace: "default", User: models.User{Username: "alice"}}
	ctx.Pull.Execution = &models.Execution{Owner: uuid.NewString()}
	Ok(t, db.AcquireClaim(ctx.Pull, ctx.Pull.ExecutionOwner()))
	result := runner.withDurableJob(ctx, command.Plan, func(actual command.ProjectContext) command.ProjectCommandOutput {
		j, err := db.Job(actual.JobID)
		Ok(t, err)
		Equals(t, "running", j.State)
		Equals(t, "alice", j.Issuer.Username)
		Equals(t, "plan", j.Command)
		return command.ProjectCommandOutput{PlanSuccess: &models.PlanSuccess{}}
	})
	Ok(t, result.Error)
	Equals(t, ctx.JobID, result.JobID)
	j, err := db.Job(ctx.JobID)
	Ok(t, err)
	Equals(t, "succeeded", j.State)

	ctx.JobID = uuid.NewString()
	failed := runner.withDurableJob(ctx, command.Apply, func(command.ProjectContext) command.ProjectCommandOutput {
		return command.ProjectCommandOutput{Failure: "apply requirements not met"}
	})
	Equals(t, "apply requirements not met", failed.Failure)
	j, err = db.Job(ctx.JobID)
	Ok(t, err)
	Equals(t, "failed", j.State)
}

func TestDurableCancellationLetsActiveJobFinishItsSteps(t *testing.T) {
	store, err := sqlstore.OpenServer(t.TempDir())
	Ok(t, err)
	defer store.Close()
	service := &jobs.Service{Store: store}
	defer service.Close()
	runner := &DefaultProjectCommandRunner{Jobs: service, Execution: &ExecutionCoordinator{Store: store}, WorkingDir: durableRunnerWorkingDir{}, EnvStepRunner: &runtime.EnvStepRunner{}}
	repo := models.Repo{FullName: "org/repo"}
	ctx := command.ProjectContext{JobID: uuid.NewString(), Pull: models.PullRequest{BaseRepo: repo, Num: 1, Execution: &models.Execution{Owner: "owner"}}, RepoRelDir: ".", Workspace: "default"}
	Ok(t, store.AcquireClaim(ctx.Pull, "owner"))
	result := runner.withDurableJob(ctx, command.Version, func(active command.ProjectContext) command.ProjectCommandOutput {
		Ok(t, store.CancelPull(active.Pull))
		_, err := runner.runSteps([]valid.Step{{StepName: "env", EnvVarName: "VALUE", EnvVarValue: "finished"}}, active, t.TempDir())
		return command.ProjectCommandOutput{Error: err}
	})
	Ok(t, result.Error)
	job, err := store.Job(ctx.JobID)
	Ok(t, err)
	Equals(t, "succeeded", job.State)
	ctx.JobID = uuid.NewString()
	queued := runner.withDurableJob(ctx, command.Version, func(command.ProjectContext) command.ProjectCommandOutput {
		t.Fatal("cancelled queued job executed")
		return command.ProjectCommandOutput{}
	})
	Assert(t, queued.Error != nil, "cancelled queued job succeeded")
	Ok(t, store.ReleaseClaim(ctx.Pull))
}

type durableRunnerWorkingDir struct{ WorkingDir }

func (durableRunnerWorkingDir) GitReadLock(models.Repo, models.PullRequest, string) func() {
	return func() {}
}

// Queue verification immediately after a successful preliminary cancellation
// check, before the job service starts its admission transaction.
type lifecycleAfterPrecheck struct {
	*sqlstore.Store
	checks int
}

func (s *lifecycleAfterPrecheck) IsCancelled(p models.PullRequest) (bool, error) {
	stopped, err := s.Store.IsCancelled(p)
	if err != nil || stopped {
		return stopped, err
	}
	s.checks++
	if s.checks == 1 {
		if err := s.QueueLifecycleCheck(p); err != nil {
			return false, err
		}
	}
	return false, nil
}

func TestDurableJobAdmissionRechecksPendingClosure(t *testing.T) {
	store, err := sqlstore.OpenServer(t.TempDir())
	Ok(t, err)
	defer store.Close()
	service := &jobs.Service{Store: store}
	defer service.Close()
	repo := models.Repo{FullName: "org/repo"}
	ctx := command.ProjectContext{JobID: uuid.NewString(), BaseRepo: repo, Pull: models.PullRequest{Num: 1, BaseRepo: repo, HeadCommit: "revision", Execution: &models.Execution{Owner: "owner"}}, RepoRelDir: ".", Workspace: "default", CommandName: command.Apply}
	Ok(t, store.AcquireClaim(ctx.Pull, "owner"))
	checks := &lifecycleAfterPrecheck{Store: store}
	runner := &DefaultProjectCommandRunner{Jobs: service, Execution: &ExecutionCoordinator{Store: checks}}
	executed := false
	result := runner.withDurableJob(ctx, command.Apply, func(command.ProjectContext) command.ProjectCommandOutput {
		executed = true
		return command.ProjectCommandOutput{}
	})
	ErrContains(t, "starting durable job", result.Error)
	ErrContains(t, "awaiting VCS verification", result.Error)
	Equals(t, 1, checks.checks)
	Assert(t, !executed, "queued project executed after closure verification was queued")
	recorded, err := store.Job(ctx.JobID)
	Ok(t, err)
	Assert(t, recorded == nil, "rejected apply was recorded as running")
	Ok(t, store.CheckClaim(ctx.Pull))
	Ok(t, store.ReleaseClaim(ctx.Pull))
}

// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/core/artifacts"
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
	blobs, err := artifacts.NewFilesystem(t.TempDir())
	Ok(t, err)
	defer blobs.Close()
	files := &artifacts.Service{Records: db, Blobs: artifacts.ObjectSnapshots{Store: blobs}}
	service := &jobs.Service{Store: db}
	runner := &DefaultProjectCommandRunner{Jobs: service, Artifacts: files, Execution: &ExecutionCoordinator{Store: db}}
	repo := models.Repo{FullName: "org/repo", VCSHost: models.VCSHost{Hostname: "git.example"}}
	ctx := command.ProjectContext{ArtifactRoot: t.TempDir(), JobID: uuid.NewString(), BaseRepo: repo, Pull: models.PullRequest{BaseRepo: repo, Num: 1, HeadCommit: "abc"}, RepoRelDir: ".", Workspace: "default", User: models.User{Username: "alice"}}
	file := filepath.Join(ctx.ArtifactRoot, "plan")
	Ok(t, os.WriteFile(file, []byte("plan"), 0600))
	ctx.Pull.Execution = &models.Execution{Owner: uuid.NewString()}
	Ok(t, db.AcquireClaim(ctx.Pull, ctx.Pull.ExecutionOwner()))
	result := runner.withDurableJob(ctx, command.Plan, func(actual command.ProjectContext) command.ProjectCommandOutput {
		j, err := db.Job(actual.JobID)
		Ok(t, err)
		Equals(t, "running", j.State)
		Equals(t, "alice", j.Issuer.Username)
		Equals(t, "plan", j.Command)
		Ok(t, db.StartPlan(actual.JobID))
		Ok(t, files.SnapshotPlan(actual, actual.ArtifactRoot, file))
		return command.ProjectCommandOutput{PlanSuccess: &models.PlanSuccess{}}
	})
	Ok(t, result.Error)
	Equals(t, ctx.JobID, result.JobID)
	j, err := db.Job(ctx.JobID)
	Ok(t, err)
	Equals(t, "succeeded", j.State)
	pending, err := db.PendingPlans(repo, 1)
	Ok(t, err)
	Equals(t, 1, len(pending))

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

func TestDurablePlanDoesNotRepublishOldFiles(t *testing.T) {
	db, err := sqlstore.OpenServer(t.TempDir())
	Ok(t, err)
	defer db.Close()
	blobs, err := artifacts.NewFilesystem(t.TempDir())
	Ok(t, err)
	defer blobs.Close()
	files := &artifacts.Service{Records: db, Blobs: artifacts.ObjectSnapshots{Store: blobs}}
	service := &jobs.Service{Store: db}
	repo := models.Repo{FullName: "org/repo"}
	ctx := command.ProjectContext{ArtifactRoot: t.TempDir(), JobID: uuid.NewString(), CommandName: command.Plan, BaseRepo: repo, Pull: models.PullRequest{BaseRepo: repo, Num: 1}, RepoRelDir: ".", Workspace: "default"}
	ctx.Pull.Execution = &models.Execution{Owner: uuid.NewString()}
	Ok(t, db.AcquireClaim(ctx.Pull, ctx.Pull.ExecutionOwner()))
	Ok(t, service.Begin(ctx))
	dir := ctx.ArtifactRoot
	Ok(t, os.WriteFile(filepath.Join(dir, "default.tfplan"), []byte("stale plan"), 0600))
	Ok(t, os.WriteFile(filepath.Join(dir, "default.json"), []byte("stale show"), 0600))
	runner := &DefaultProjectCommandRunner{Jobs: service, Artifacts: files, Execution: &ExecutionCoordinator{Store: db}, WorkingDir: durableRunnerWorkingDir{}}
	_, err = runner.runSteps(nil, ctx, dir)
	ErrContains(t, "no recoverable output", err)
	Ok(t, service.Finish(ctx, command.ProjectCommandOutput{Error: err}))
	pending, err := db.PendingPlans(repo, 1)
	Ok(t, err)
	Equals(t, 0, len(pending))
	records, err := db.Snapshot(ctx.JobID)
	Ok(t, err)
	Assert(t, records == nil, "stale files were published")
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
	blobs, err := artifacts.NewFilesystem(t.TempDir())
	Ok(t, err)
	defer blobs.Close()
	files := &artifacts.Service{Records: store, Blobs: artifacts.ObjectSnapshots{Store: blobs}}
	service := &jobs.Service{Store: store}
	defer service.Close()
	repo := models.Repo{FullName: "org/repo"}
	ctx := command.ProjectContext{ArtifactRoot: t.TempDir(), JobID: uuid.NewString(), BaseRepo: repo, Pull: models.PullRequest{Num: 1, BaseRepo: repo, HeadCommit: "revision", Execution: &models.Execution{Owner: "owner"}}, RepoRelDir: ".", Workspace: "default", CommandName: command.Plan, RequiresAtlantisManagedPlanFile: true}
	Ok(t, store.AcquireClaim(ctx.Pull, "owner"))
	Ok(t, service.Begin(ctx))
	Ok(t, store.StartPlan(ctx.JobID))
	path := filepath.Join(ctx.ArtifactRoot, "default.tfplan")
	Ok(t, os.WriteFile(path, []byte("saved plan"), 0600))
	Ok(t, files.SnapshotPlan(ctx, ctx.ArtifactRoot, path))
	Ok(t, service.Finish(ctx, command.ProjectCommandOutput{PlanSuccess: &models.PlanSuccess{}}))
	planID := ctx.JobID
	ctx.JobID = uuid.NewString()
	checks := &lifecycleAfterPrecheck{Store: store}
	runner := &DefaultProjectCommandRunner{Jobs: service, Artifacts: files, Execution: &ExecutionCoordinator{Store: checks}}
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
	pending, err := store.PendingPlans(repo, ctx.Pull.Num)
	Ok(t, err)
	Equals(t, 1, len(pending))
	Equals(t, planID, pending[0].ID)
	Ok(t, store.CheckClaim(ctx.Pull))
	Ok(t, store.ReleaseClaim(ctx.Pull))
}

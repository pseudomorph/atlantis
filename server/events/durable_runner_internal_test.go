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
	service := &jobs.Service{Store: db, Artifacts: &artifacts.Service{Records: db, Blobs: blobs}}
	runner := &DefaultProjectCommandRunner{Jobs: service, Artifacts: service.Artifacts, Execution: &ExecutionCoordinator{Store: db}}
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
		Ok(t, db.StartPlan(actual.JobID))
		Ok(t, service.Artifacts.SaveFile(actual, "plan", file))
		return command.ProjectCommandOutput{PlanSuccess: &models.PlanSuccess{}}
	})
	Ok(t, result.Error)
	j, err := db.Job(ctx.JobID)
	Ok(t, err)
	Equals(t, "succeeded", j.State)
	pending, err := db.PendingPlans(repo, 1)
	Ok(t, err)
	Equals(t, 1, len(pending))
}

func TestDurableCancellationLetsActiveJobFinishItsSteps(t *testing.T) {
	store, err := sqlstore.OpenServer(t.TempDir())
	Ok(t, err)
	defer store.Close()
	blobs, err := artifacts.NewFilesystem(t.TempDir())
	Ok(t, err)
	defer blobs.Close()
	service := &jobs.Service{Store: store, Artifacts: &artifacts.Service{Records: store, Blobs: blobs}}
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

func TestDurableWorkingDirectoriesAreInstanceScoped(t *testing.T) {
	w := &FileWorkspace{DataDir: t.TempDir(), InstanceScoped: true}
	repo := models.Repo{FullName: "org/repo", CloneURL: "https://git.example:8443/gitlab/org/repo.git", VCSHost: models.VCSHost{Type: models.Gitlab}}
	a, err := w.repoPullDir(repo, models.PullRequest{Num: 1})
	Ok(t, err)
	repo.CloneURL = "https://git.example/other/org/repo.git"
	b, err := w.repoPullDir(repo, models.PullRequest{Num: 1})
	Ok(t, err)
	Assert(t, a != b, "instances share a working directory")
	planDir, err := w.planPullDir(repo, models.PullRequest{Num: 1})
	Ok(t, err)
	Equals(t, b, planDir)
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
	service := &jobs.Service{Store: db, Artifacts: &artifacts.Service{Records: db, Blobs: blobs}}
	repo := models.Repo{FullName: "org/repo"}
	ctx := command.ProjectContext{ArtifactRoot: t.TempDir(), JobID: uuid.NewString(), CommandName: command.Plan, BaseRepo: repo, Pull: models.PullRequest{BaseRepo: repo, Num: 1}, RepoRelDir: ".", Workspace: "default"}
	ctx.Pull.Execution = &models.Execution{Owner: uuid.NewString()}
	Ok(t, db.AcquireClaim(ctx.Pull, ctx.Pull.ExecutionOwner()))
	Ok(t, service.Begin(ctx))
	dir := t.TempDir()
	Ok(t, os.WriteFile(filepath.Join(dir, "default.tfplan"), []byte("stale plan"), 0600))
	Ok(t, os.WriteFile(filepath.Join(dir, "default.json"), []byte("stale show"), 0600))
	runner := &DefaultProjectCommandRunner{Jobs: service, Artifacts: service.Artifacts, Execution: &ExecutionCoordinator{Store: db}, WorkingDir: durableRunnerWorkingDir{}}
	_, err = runner.runSteps(nil, ctx, dir)
	Ok(t, err)
	Ok(t, service.Finish(ctx, command.ProjectCommandOutput{}))
	pending, err := db.PendingPlans(repo, 1)
	Ok(t, err)
	Equals(t, 0, len(pending))
	records, err := db.Artifacts(ctx.JobID)
	Ok(t, err)
	Equals(t, 0, len(records))
}

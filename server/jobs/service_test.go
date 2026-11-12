// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package jobs_test

import (
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/core/config/valid"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/jobs"
	. "github.com/runatlantis/atlantis/testing"
)

func setup(t *testing.T) *jobs.Service {
	t.Helper()
	db, err := sqlstore.OpenServer(t.TempDir())
	Ok(t, err)
	t.Cleanup(func() { _ = db.Close() })
	service := &jobs.Service{Store: db}
	t.Cleanup(func() { _ = service.Close() })
	return service
}
func projectContext() command.ProjectContext {
	repo := models.Repo{FullName: "org/infra", Owner: "org", Name: "infra", CloneURL: "https://git.example/gitlab/org/infra.git", VCSHost: models.VCSHost{Hostname: "git.example", Type: models.Gitlab}}
	return command.ProjectContext{JobID: uuid.NewString(), CommandName: command.Plan, BaseRepo: repo, Pull: models.PullRequest{BaseRepo: repo, Num: 7, HeadCommit: "revision", Execution: &models.Execution{Owner: uuid.NewString()}},
		ProjectName: "network", RepoRelDir: "env/prod", Workspace: "production", User: models.User{Username: "alice", Teams: []string{"platform"}}, CommentArgs: []string{"-target=module.network"}, Trigger: command.CommentTrigger}
}

func TestJobRecordsInvocation(t *testing.T) {
	service := setup(t)
	ctx := projectContext()
	claimContext(t, service, &ctx)
	Ok(t, service.Begin(ctx))
	Ok(t, service.Finish(ctx, command.ProjectCommandOutput{PlanSuccess: &models.PlanSuccess{}}))
	j, err := service.Store.Job(ctx.JobID)
	Ok(t, err)
	Equals(t, "alice", j.Issuer.Username)
	Equals(t, ctx.CommentArgs, j.Arguments)
	Equals(t, "comment", j.Trigger)
	Equals(t, "plan", j.Command)
	Equals(t, "revision", j.Revision)
	Equals(t, "succeeded", j.State)
	Assert(t, j.FinishedAt != nil, "job never finished")

	failed := ctx
	failed.JobID = uuid.NewString()
	failed.API = true
	Ok(t, service.Begin(failed))
	Ok(t, service.Finish(failed, command.ProjectCommandOutput{Failure: "plan failed"}))
	j, err = service.Store.Job(failed.JobID)
	Ok(t, err)
	Equals(t, "api", j.Trigger)
	Equals(t, "failed", j.State)
}

func TestRecoveryKeepsUncertainExecutionHistory(t *testing.T) {
	service := setup(t)
	ctx := projectContext()
	claimContext(t, service, &ctx)
	Ok(t, service.Begin(ctx))
	Ok(t, service.Finish(ctx, command.ProjectCommandOutput{}))
	apply := ctx
	apply.JobID = uuid.NewString()
	apply.CommandName = command.Apply
	Ok(t, service.Begin(apply))
	Ok(t, service.Close())
	Ok(t, service.Store.(*sqlstore.Store).MarkUncertain(ctx.Pull))
	Ok(t, service.Store.(*sqlstore.Store).MarkPreviousExecutionsUncertain())
	Assert(t, service.Store.(*sqlstore.Store).CheckClaim(ctx.Pull) != nil, "uncertain execution allowed retry")
	j, err := service.Store.Job(apply.JobID)
	Ok(t, err)
	Equals(t, "running", j.State)
	j, err = service.Store.Job(ctx.JobID)
	Ok(t, err)
	Equals(t, "succeeded", j.State)
}

func claimContext(t *testing.T, service *jobs.Service, ctx *command.ProjectContext) {
	t.Helper()
	if service.Store.(*sqlstore.Store).CheckClaim(ctx.Pull) == nil {
		return
	}
	if ctx.Pull.ExecutionOwner() == "" {
		ctx.Pull.Execution = &models.Execution{Owner: uuid.NewString()}
	}
	ctx.BaseRepo.Execution = ctx.Pull.Execution
	ctx.Pull.BaseRepo.Execution = ctx.Pull.Execution
	Ok(t, service.Store.(*sqlstore.Store).AcquireClaim(ctx.Pull, ctx.Pull.ExecutionOwner()))
}

func TestUnknownWriterCannotCompleteJob(t *testing.T) {
	service := setup(t)
	ctx := projectContext()
	claimContext(t, service, &ctx)
	Ok(t, service.Store.StartJob(models.Job{ID: ctx.JobID, Kind: models.CommandJob, Pull: ctx.Pull, Project: models.NewProject(ctx.BaseRepo.FullName, ctx.RepoRelDir, ctx.ProjectName), Workspace: ctx.Workspace}))
	Assert(t, service.Complete(ctx.JobID, "") != nil, "missing writer allowed completion")
	job, err := service.Store.Job(ctx.JobID)
	Ok(t, err)
	Equals(t, "running", job.State)
}

func TestClosedServiceRejectsNewJobs(t *testing.T) {
	service := setup(t)
	ctx := projectContext()
	claimContext(t, service, &ctx)
	Ok(t, service.Close())
	Assert(t, service.Begin(ctx) != nil, "closed service started a writer")
	job, err := service.Store.Job(ctx.JobID)
	Ok(t, err)
	Assert(t, job == nil, "closed service created a job")
}

func TestPersistedStepsExcludeInlineSecrets(t *testing.T) {
	service := setup(t)
	ctx := projectContext()
	ctx.Steps = []valid.Step{
		{StepName: "env", EnvVarName: "TOKEN", EnvVarValue: "distinct-inline-secret", RunCommand: "echo distinct-command-secret"},
		{StepName: "run", RunCommand: "curl --token distinct-command-secret", ExtraArgs: []string{"distinct-argument-secret"}},
	}
	claimContext(t, service, &ctx)
	Ok(t, service.Begin(ctx))
	stored, err := service.Store.Job(ctx.JobID)
	Ok(t, err)
	Equals(t, `[{"name":"env"},{"name":"run","has_extra_args":true}]`, stored.Steps)
	for _, secret := range []string{"distinct-inline-secret", "distinct-command-secret", "distinct-argument-secret"} {
		Assert(t, !strings.Contains(stored.Steps, secret), "inline workflow secret persisted")
	}
	Ok(t, service.Finish(ctx, command.ProjectCommandOutput{}))
}

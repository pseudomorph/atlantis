// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package jobs_test

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/core/artifacts"
	"github.com/runatlantis/atlantis/server/core/config/valid"
	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/core/logs"
	"github.com/runatlantis/atlantis/server/core/runtime"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/jobs"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

func setup(t *testing.T) (*jobs.Service, string) {
	t.Helper()
	db, err := sqlstore.OpenServer(t.TempDir())
	Ok(t, err)
	t.Cleanup(func() { _ = db.Close() })
	dir := t.TempDir()
	blobs, err := artifacts.NewFilesystem(dir)
	Ok(t, err)
	t.Cleanup(func() { _ = blobs.Close() })
	service := &jobs.Service{Store: db, Logs: &logs.Service{Records: db, Mode: "database"}, Artifacts: &artifacts.Service{Records: db, Blobs: blobs}}
	t.Cleanup(func() { _ = service.Close() })
	return service, dir
}
func projectContext(t *testing.T) command.ProjectContext {
	repo := models.Repo{FullName: "org/infra", Owner: "org", Name: "infra", CloneURL: "https://git.example/gitlab/org/infra.git", VCSHost: models.VCSHost{Hostname: "git.example", Type: models.Gitlab}}
	return command.ProjectContext{ArtifactRoot: t.TempDir(), JobID: uuid.NewString(), CommandName: command.Plan, BaseRepo: repo, Pull: models.PullRequest{BaseRepo: repo, Num: 7, HeadCommit: "revision", Execution: &models.Execution{Owner: uuid.NewString()}},
		ProjectName: "network", RepoRelDir: "env/prod", Workspace: "production", User: models.User{Username: "alice", Teams: []string{"platform"}}, CommentArgs: []string{"-target=module.network"}, Trigger: command.CommentTrigger}
}
func TestJobArtifactsAndPendingPlanBinding(t *testing.T) {
	service, root := setup(t)
	ctx := projectContext(t)
	store := service.Artifacts
	beginPlan(t, service, &ctx)
	file := filepath.Join(ctx.ArtifactRoot, "network-production.tfplan")
	Ok(t, os.WriteFile(file, []byte("saved plan"), 0600))
	Ok(t, service.Artifacts.SaveFile(ctx, "plan", file))
	show := filepath.Join(filepath.Dir(file), ctx.GetShowResultFileName())
	Ok(t, os.WriteFile(show, []byte("{}"), 0600))
	Ok(t, service.Artifacts.SaveFile(ctx, "show", show))
	Ok(t, service.AppendLog(ctx.JobID, "planning"))
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
	records, err := service.Artifacts.Records.Artifacts(ctx.JobID)
	Ok(t, err)
	Equals(t, 2, len(records))
	for _, a := range records {
		Assert(t, a.Size > 0 && len(a.SHA256) == 64, "missing artifact integrity metadata")
		_, err := os.Stat(filepath.Join(root, a.Location))
		Ok(t, err)
	}
	apply := ctx
	apply.CommandName = command.Apply
	apply.ExpectedPlanHash = fmt.Sprintf("%x", sha256.Sum256([]byte("saved plan")))
	apply.JobID = uuid.NewString()
	claimContext(t, service, &apply)
	Ok(t, service.Begin(apply))
	j, err = service.Store.Job(apply.JobID)
	Ok(t, err)
	Equals(t, ctx.JobID, j.PlanJobID)
	Ok(t, os.Remove(file))
	Ok(t, os.Remove(show))
	unscoped := apply
	unscoped.ArtifactRoot = ""
	Assert(t, store.Load(unscoped, file) != nil, "restored plan without an assigned artifact root")
	Ok(t, store.Load(apply, file))
	content, err := os.ReadFile(file)
	Ok(t, err)
	Equals(t, "saved plan", string(content))
	content, err = os.ReadFile(show)
	Ok(t, err)
	Equals(t, "{}", string(content))
	// A later plan at the same revision must not silently replace the apply's input.
	newer := ctx
	newer.JobID = uuid.NewString()
	beginPlan(t, service, &newer)
	Ok(t, service.Artifacts.SaveFile(newer, "plan", file))
	Ok(t, service.Finish(newer, command.ProjectCommandOutput{}))
	Assert(t, store.Load(apply, file) != nil, "apply switched to a different plan job")
	Ok(t, service.Artifacts.Records.InvalidatePlans(ctx.BaseRepo, ctx.Pull.Num))
	jobs, err := service.Store.Jobs()
	Ok(t, err)
	Equals(t, 3, len(jobs))
	records, err = service.Artifacts.Records.Artifacts(ctx.JobID)
	Ok(t, err)
	Equals(t, 2, len(records))
	log, err := service.Logs.ReadAll(ctx.JobID)
	Ok(t, err)
	Equals(t, "planning\n", string(log))
}

func TestCorruptAndInvalidatedArtifactsFailClosed(t *testing.T) {
	service, root := setup(t)
	ctx := projectContext(t)
	store := service.Artifacts
	beginPlan(t, service, &ctx)
	file := filepath.Join(ctx.ArtifactRoot, "plan.tfplan")
	Ok(t, os.WriteFile(file, []byte("original"), 0600))
	Ok(t, service.Artifacts.SaveFile(ctx, "plan", file))
	Ok(t, service.Finish(ctx, command.ProjectCommandOutput{}))
	apply := ctx
	apply.JobID = uuid.NewString()
	apply.CommandName = command.Apply
	claimContext(t, service, &apply)
	Ok(t, service.Begin(apply))
	records, err := service.Artifacts.Records.Artifacts(ctx.JobID)
	Ok(t, err)
	Ok(t, os.WriteFile(filepath.Join(root, records[0].Location), []byte("corrupt"), 0600))
	Assert(t, store.Load(apply, file) != nil, "corrupt artifact accepted")
	data, err := os.ReadFile(file)
	Ok(t, err)
	Equals(t, "original", string(data))
	Ok(t, service.Artifacts.Records.InvalidatePlans(ctx.BaseRepo, ctx.Pull.Num))
	Assert(t, store.Load(apply, file) != nil, "invalidated plan restored")
}

func TestSupersededPlanCannotBecomePending(t *testing.T) {
	service, _ := setup(t)
	old := projectContext(t)
	newer := old
	newer.JobID = uuid.NewString()
	beginPlan(t, service, &old)
	beginPlan(t, service, &newer)
	file := filepath.Join(old.ArtifactRoot, "plan")
	Ok(t, os.WriteFile(file, []byte("plan"), 0600))
	Ok(t, service.Artifacts.SaveFile(old, "plan", file))
	Ok(t, service.Artifacts.SaveFile(newer, "plan", file))
	Ok(t, service.Finish(newer, command.ProjectCommandOutput{}))
	Assert(t, service.Finish(old, command.ProjectCommandOutput{}) != nil, "stale plan published")
	pending, err := service.Artifacts.Records.PendingPlans(old.BaseRepo, old.Pull.Num)
	Ok(t, err)
	Equals(t, 1, len(pending))
	Equals(t, newer.JobID, pending[0].ID)
}

func TestRecoveryKeepsUncertainExecutionHistory(t *testing.T) {
	service, _ := setup(t)
	ctx := projectContext(t)
	beginPlan(t, service, &ctx)
	file := filepath.Join(ctx.ArtifactRoot, "plan")
	Ok(t, os.WriteFile(file, []byte("plan"), 0600))
	Ok(t, service.Artifacts.SaveFile(ctx, "plan", file))
	Ok(t, service.Finish(ctx, command.ProjectCommandOutput{}))
	apply := ctx
	apply.JobID = uuid.NewString()
	apply.CommandName = command.Apply
	claimContext(t, service, &apply)
	Ok(t, service.Begin(apply))
	Ok(t, service.AppendLog(apply.JobID, "applying"))
	Ok(t, service.Close())
	Ok(t, service.Store.(*sqlstore.Store).MarkUncertain(ctx.Pull))
	Ok(t, service.Store.(*sqlstore.Store).MarkPreviousExecutionsUncertain())
	pending, err := service.Artifacts.Records.PendingPlans(ctx.BaseRepo, ctx.Pull.Num)
	Ok(t, err)
	Equals(t, 1, len(pending))
	Assert(t, service.Store.(*sqlstore.Store).CheckClaim(ctx.Pull) != nil, "uncertain execution allowed retry")
	j, err := service.Store.Job(apply.JobID)
	Ok(t, err)
	Equals(t, "running", j.State)
	log, err := service.Logs.ReadAll(apply.JobID)
	Ok(t, err)
	Equals(t, "applying\n", string(log))
	records, err := service.Artifacts.Records.Artifacts(ctx.JobID)
	Ok(t, err)
	Equals(t, 1, len(records))
}

func beginPlan(t *testing.T, service *jobs.Service, ctx *command.ProjectContext) {
	t.Helper()
	claimContext(t, service, ctx)
	Ok(t, service.Begin(*ctx))
	Ok(t, service.Store.StartPlan(ctx.JobID))
}

func TestRejectedPlanDoesNotInvalidateOwner(t *testing.T) {
	service, _ := setup(t)
	owner := projectContext(t)
	beginPlan(t, service, &owner)
	file := filepath.Join(owner.ArtifactRoot, "plan")
	Ok(t, os.WriteFile(file, []byte("plan"), 0600))
	Ok(t, service.Artifacts.SaveFile(owner, "plan", file))
	Ok(t, service.Finish(owner, command.ProjectCommandOutput{}))
	contender := owner
	contender.JobID = uuid.NewString()
	contender.Pull.Num++
	contender.Pull.Execution = &models.Execution{Owner: uuid.NewString()}
	claimContext(t, service, &contender)
	Ok(t, service.Begin(contender))
	Ok(t, service.Finish(contender, command.ProjectCommandOutput{Failure: "project locked"}))
	pending, err := service.Artifacts.Records.PendingPlans(owner.BaseRepo, owner.Pull.Num)
	Ok(t, err)
	Equals(t, 1, len(pending))
	Equals(t, owner.JobID, pending[0].ID)
}

func TestRestorePlansRejectsSymlinkOutsidePull(t *testing.T) {
	service, _ := setup(t)
	ctx := projectContext(t)
	beginPlan(t, service, &ctx)
	file := filepath.Join(ctx.ArtifactRoot, "plan.tfplan")
	Ok(t, os.WriteFile(file, []byte("plan"), 0600))
	Ok(t, service.Artifacts.SaveFile(ctx, "plan", file))
	Ok(t, service.Finish(ctx, command.ProjectCommandOutput{}))
	pullDir, outside := t.TempDir(), t.TempDir()
	Ok(t, os.Symlink(outside, filepath.Join(pullDir, ctx.Workspace)))
	scoped := service.Artifacts
	err := scoped.RestorePlans(pullDir, models.Repo{}, ctx.Pull.Num)
	Assert(t, err != nil, "restored a plan outside the assigned pull directory")
	entries, err := os.ReadDir(outside)
	Ok(t, err)
	Equals(t, 0, len(entries))
}

func TestDeclaredWorkflowFilesRestoreWithoutConventionalPlan(t *testing.T) {
	service, _ := setup(t)
	service.Artifacts.WorkflowFiles = []string{"reviewed.plan"}
	ctx := projectContext(t)
	ctx.RequiresAtlantisManagedPlanFile = false
	ctx.ArtifactRoot = t.TempDir()
	project := filepath.Join(ctx.ArtifactRoot, ctx.RepoRelDir)
	Ok(t, os.MkdirAll(project, 0700))
	filename := filepath.Join(project, "reviewed.plan")
	Ok(t, os.WriteFile(filename, []byte("custom workflow input"), 0600))
	beginPlan(t, service, &ctx)
	Ok(t, service.Artifacts.SnapshotWorkflow(ctx, project))
	Ok(t, service.Finish(ctx, command.ProjectCommandOutput{}))
	Ok(t, os.Remove(filename))
	apply := ctx
	apply.CommandName = command.Apply
	apply.JobID = uuid.NewString()
	claimContext(t, service, &apply)
	Ok(t, service.Begin(apply))
	store := service.Artifacts
	apply.ExpectedPlanHash = "missing-plan-checksum"
	Assert(t, store.Load(apply, filepath.Join(project, "conventional.tfplan")) != nil, "ignored expected checksum without a plan artifact")
	apply.ExpectedPlanHash = ""
	Ok(t, store.Load(apply, filepath.Join(project, "conventional.tfplan")))
	content, err := os.ReadFile(filename)
	Ok(t, err)
	Equals(t, "custom workflow input", string(content))
}

func TestArtifactSourceSymlinkCannotEscapeWorkingDirectory(t *testing.T) {
	service, _ := setup(t)
	ctx := projectContext(t)
	ctx.ArtifactRoot = t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	Ok(t, os.WriteFile(outside, []byte("outside secret"), 0600))
	link := filepath.Join(ctx.ArtifactRoot, "symlink")
	Ok(t, os.Symlink(outside, link))
	beginPlan(t, service, &ctx)
	Assert(t, service.Artifacts.SaveFile(ctx, "workflow", link) != nil, "captured file outside assigned working directory")
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

func TestLogChunksPersistDuringExecutionAndReplayOnce(t *testing.T) {
	service, _ := setup(t)
	ctx := projectContext(t)
	claimContext(t, service, &ctx)
	Ok(t, service.Begin(ctx))
	Ok(t, service.AppendLog(ctx.JobID, "first"))
	deadline := time.Now().Add(3 * time.Second)
	for {
		data, err := service.Logs.ReadAll(ctx.JobID)
		Ok(t, err)
		if string(data) == "first\n" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("output was not persisted while job was running")
		}
		time.Sleep(10 * time.Millisecond)
	}
	job, err := service.Store.Job(ctx.JobID)
	Ok(t, err)
	Equals(t, "running", job.State)
	handler := &jobs.DurableOutputHandler{Service: service}
	receiver := make(chan string, 4)
	go handler.Register(ctx.JobID, receiver)
	Ok(t, service.AppendLog(ctx.JobID, "second"))
	Ok(t, service.Finish(ctx, command.ProjectCommandOutput{}))
	var lines []string
	for {
		select {
		case line, open := <-receiver:
			if !open {
				Equals(t, []string{"first", "second"}, lines)
				return
			}
			lines = append(lines, line)
		case <-time.After(3 * time.Second):
			t.Fatal("job replay did not complete")
		}
	}
}

type rejectedLogStore struct{ db.LogStore }

func (rejectedLogStore) AppendLogChunk(string, []byte) error {
	return fmt.Errorf("injected journal write failure")
}

func TestLogWriteFailurePreventsSuccessfulCompletion(t *testing.T) {
	service, _ := setup(t)
	service.Logs.Records = rejectedLogStore{service.Logs.Records}
	ctx := projectContext(t)
	claimContext(t, service, &ctx)
	Ok(t, service.Begin(ctx))
	Ok(t, service.AppendLog(ctx.JobID, "output"))
	Assert(t, service.Finish(ctx, command.ProjectCommandOutput{}) != nil, "log failure allowed success")
	job, err := service.Store.Job(ctx.JobID)
	Ok(t, err)
	Equals(t, "failed", job.State)
	records, err := service.Artifacts.Records.Artifacts(ctx.JobID)
	Ok(t, err)
	Equals(t, 0, len(records))
}

func TestShutdownPreservesLogFailuresUntilCompletion(t *testing.T) {
	for _, failure := range []string{"flush", "late-output"} {
		t.Run(failure, func(t *testing.T) {
			service, _ := setup(t)
			ctx := projectContext(t)
			claimContext(t, service, &ctx)
			Ok(t, service.Begin(ctx))
			if failure == "flush" {
				service.Logs.Records = rejectedLogStore{service.Logs.Records}
				Ok(t, service.AppendLog(ctx.JobID, "output"))
				Assert(t, service.Close() != nil, "shutdown ignored failed output flush")
			} else {
				Ok(t, service.Close())
				Assert(t, service.AppendLog(ctx.JobID, "late output") != nil, "closed writer accepted output")
			}
			Assert(t, service.Complete(ctx.JobID, "") != nil, "lost output allowed success")
			job, err := service.Store.Job(ctx.JobID)
			Ok(t, err)
			Equals(t, "failed", job.State)
			Equals(t, "job output could not be persisted", job.Error)
		})
	}
}

func TestUnknownWriterCannotCompleteJob(t *testing.T) {
	service, _ := setup(t)
	ctx := projectContext(t)
	claimContext(t, service, &ctx)
	Ok(t, service.Store.StartJob(models.Job{ID: ctx.JobID, Kind: models.CommandJob, Pull: ctx.Pull, Project: models.NewProject(ctx.BaseRepo.FullName, ctx.RepoRelDir, ctx.ProjectName), Workspace: ctx.Workspace}))
	Assert(t, service.Complete(ctx.JobID, "") != nil, "missing writer allowed completion")
	job, err := service.Store.Job(ctx.JobID)
	Ok(t, err)
	Equals(t, "running", job.State)
}

func TestClosedServiceRejectsNewJobs(t *testing.T) {
	service, _ := setup(t)
	ctx := projectContext(t)
	claimContext(t, service, &ctx)
	Ok(t, service.Close())
	Assert(t, service.Begin(ctx) != nil, "closed service started a writer")
	job, err := service.Store.Job(ctx.JobID)
	Ok(t, err)
	Assert(t, job == nil, "closed service created a job")
}

func TestHookOutcomeDoesNotChangePendingPlan(t *testing.T) {
	service, _ := setup(t)
	ctx := projectContext(t)
	beginPlan(t, service, &ctx)
	file := filepath.Join(ctx.ArtifactRoot, "plan.tfplan")
	Ok(t, os.WriteFile(file, []byte("plan"), 0600))
	Ok(t, service.Artifacts.SaveFile(ctx, "plan", file))
	Ok(t, service.Finish(ctx, command.ProjectCommandOutput{}))
	handler := &jobs.DurableOutputHandler{Service: service}
	runner := runtime.DefaultPreWorkflowHookRunner{Jobs: service, OutputHandler: handler}
	for _, label := range []string{"plan", "apply"} {
		hook := models.WorkflowHookCommandContext{HookID: uuid.NewString(), HookStepName: label, Pull: ctx.Pull, BaseRepo: ctx.BaseRepo, RepoRelDir: ctx.RepoRelDir, Workspace: ctx.Workspace, Log: logging.NewNoopLogger(t)}
		_, _, err := runner.Run(hook, "printf never-retain-this-output; exit 7", "sh", "-c", ctx.ArtifactRoot)
		Assert(t, err != nil, "hook failure was ignored")
		job, err := service.Store.Job(hook.HookID)
		Ok(t, err)
		Equals(t, models.WorkflowHookJob, job.Kind)
		Equals(t, "failed", job.State)
		Assert(t, !strings.Contains(job.Error, "never-retain-this-output"), "hook output escaped log retention")
		Equals(t, "", job.PlanJobID)
		pending, err := service.Artifacts.Records.PendingPlans(ctx.BaseRepo, ctx.Pull.Num)
		Ok(t, err)
		Equals(t, 1, len(pending))
		Equals(t, ctx.JobID, pending[0].ID)
	}
}

func TestConcurrentPlanLogsAndRetention(t *testing.T) {
	for _, backend := range []string{"filesystem", "database"} {
		t.Run(backend, func(t *testing.T) {
			service, outputRoot := setup(t)
			if backend == "filesystem" {
				service.Logs.Mode, service.Logs.Directory = "filesystem", filepath.Join(outputRoot, "job-output")
			}
			if backend == "database" {
				service.Artifacts.Blobs = sqlstore.BlobStore{Store: service.Store.(*sqlstore.Store)}
			}
			ctx := projectContext(t)
			beginPlan(t, service, &ctx)
			file := filepath.Join(ctx.ArtifactRoot, "plan.tfplan")
			Ok(t, os.WriteFile(file, []byte("saved plan"), 0600))
			var workers sync.WaitGroup
			errors := make(chan error, 3)
			start := make(chan struct{})
			workers.Go(func() {
				<-start
				errors <- service.Artifacts.SaveFile(ctx, "plan", file)
			})
			workers.Go(func() {
				<-start
				for range 16 {
					// Force several log chunks while the plan and maintenance share one connection.
					if err := service.AppendLog(ctx.JobID, strings.Repeat("x", 64*1024)); err != nil {
						errors <- err
						return
					}
				}
				errors <- nil
			})
			workers.Go(func() {
				<-start
				for range 16 {
					now, err := service.Store.(*sqlstore.Store).Now()
					if err == nil {
						err = service.Artifacts.Collect(now, time.Hour, 0)
					}
					if err != nil {
						errors <- err
						return
					}
				}
				errors <- nil
			})
			close(start)
			done := make(chan struct{})
			go func() { workers.Wait(); close(done) }()
			select {
			case <-done:
			case <-time.After(15 * time.Second):
				t.Fatal("plan, logs and retention blocked on the shared SQLite connection")
			}
			close(errors)
			for err := range errors {
				Ok(t, err)
			}
			Ok(t, service.Finish(ctx, command.ProjectCommandOutput{}))
			log, err := service.Logs.ReadAll(ctx.JobID)
			Ok(t, err)
			Equals(t, 16*(64*1024+1), len(log))
			pending, err := service.Artifacts.Records.PendingPlans(ctx.BaseRepo, ctx.Pull.Num)
			Ok(t, err)
			Equals(t, 1, len(pending))
		})
	}
}

func TestPlanSnapshotRestoresDependencyLockFile(t *testing.T) {
	service, _ := setup(t)
	ctx := projectContext(t)
	project := filepath.Join(ctx.ArtifactRoot, ctx.RepoRelDir)
	Ok(t, os.MkdirAll(project, 0700))
	planFile := filepath.Join(project, "network-production.tfplan")
	lockFile := filepath.Join(project, artifacts.DependencyLockFile)
	Ok(t, os.WriteFile(planFile, []byte("saved plan"), 0600))
	Ok(t, os.WriteFile(lockFile, []byte("provider selections at plan time"), 0600))
	beginPlan(t, service, &ctx)
	Ok(t, service.Artifacts.SnapshotPlan(ctx, project, planFile))
	Ok(t, service.Finish(ctx, command.ProjectCommandOutput{PlanSuccess: &models.PlanSuccess{}}))

	// A lost working directory comes back as a fresh clone whose lock file may differ.
	Ok(t, os.Remove(planFile))
	Ok(t, os.WriteFile(lockFile, []byte("committed lock file"), 0600))
	apply := ctx
	apply.CommandName = command.Apply
	apply.JobID = uuid.NewString()
	claimContext(t, service, &apply)
	Ok(t, service.Begin(apply))
	Ok(t, service.Artifacts.Load(apply, planFile))
	content, err := os.ReadFile(lockFile)
	Ok(t, err)
	Equals(t, "provider selections at plan time", string(content))
}

func TestPersistedStepsExcludeInlineSecrets(t *testing.T) {
	service, _ := setup(t)
	ctx := projectContext(t)
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

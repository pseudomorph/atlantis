// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/google/uuid"
	. "github.com/petergtz/pegomock/v4"
	"github.com/runatlantis/atlantis/server/core/artifacts"
	"github.com/runatlantis/atlantis/server/core/config"
	"github.com/runatlantis/atlantis/server/core/config/valid"
	"github.com/runatlantis/atlantis/server/core/sqlstore"
	tfclientmocks "github.com/runatlantis/atlantis/server/core/terraform/tfclient/mocks"
	"github.com/runatlantis/atlantis/server/events"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/mocks"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/jobs"
	"github.com/runatlantis/atlantis/server/logging"
	"github.com/runatlantis/atlantis/server/metrics/metricstest"
	. "github.com/runatlantis/atlantis/testing"
)

func TestDurableApplyAllUsesPendingPlanInventory(t *testing.T) {
	for _, missingCheckout := range []bool{false, true} {
		name := "existing checkout"
		if missingCheckout {
			name = "missing checkout"
		}
		t.Run(name, func(t *testing.T) {
			RegisterMockTestingT(t)
			databaseDir := t.TempDir()
			db, err := sqlstore.Open(databaseDir)
			Ok(t, err)
			defer func() { _ = db.Close() }()
			blobs, err := artifacts.NewFilesystem(t.TempDir())
			Ok(t, err)
			defer blobs.Close()
			service := &jobs.Service{Store: db, Artifacts: &artifacts.Service{Records: db, Blobs: blobs}}
			repo := models.Repo{FullName: "org/repo", Owner: "org", Name: "repo"}
			pull := models.PullRequest{BaseRepo: repo, Num: 1, Execution: &models.Execution{Owner: uuid.NewString()}}
			Ok(t, db.AcquireClaim(pull, pull.ExecutionOwner(), time.Hour))
			repo.Execution = pull.Execution
			var projects []models.ProjectStatus
			var planContexts []command.ProjectContext
			for _, workspace := range []string{"default", "staging"} {
				ctx := command.ProjectContext{ArtifactRoot: t.TempDir(), JobID: uuid.NewString(), CommandName: command.Plan, BaseRepo: repo, Pull: pull, RepoRelDir: "infra", Workspace: workspace}
				locked, _, err := db.TryLock(models.ProjectLock{Pull: pull, Project: models.NewProject(repo.FullName, "infra", ""), Workspace: workspace})
				Ok(t, err)
				Assert(t, locked, "project lock was not acquired")
				Ok(t, service.Begin(ctx))
				Ok(t, db.StartPlan(ctx.JobID))
				file := filepath.Join(ctx.ArtifactRoot, "plan")
				Ok(t, os.WriteFile(file, []byte("durable plan"), 0600))
				Ok(t, service.Artifacts.SaveFile(ctx, "plan", file))
				Ok(t, service.Finish(ctx, command.ProjectCommandOutput{}))
				projects = append(projects, models.ProjectStatus{RepoRelDir: "infra", Workspace: workspace, Status: models.PlannedPlanStatus})
				planContexts = append(planContexts, ctx)
			}
			// Reopen the database so discovery must use persisted pending plans.
			Ok(t, db.Close())
			db, err = sqlstore.Open(databaseDir)
			Ok(t, err)
			service.Store = db
			service.Artifacts.Records = db

			pullDir := t.TempDir()
			if missingCheckout {
				pullDir = filepath.Join(pullDir, "checkout")
			}
			workingDir := &fakeWorkingDir{
				MockWorkingDir: *mocks.NewMockWorkingDir(),
				pullDir:        pullDir,
				onClone: func(workspace, dir string) error {
					projectDir := filepath.Join(dir, "infra")
					if err := os.MkdirAll(projectDir, 0700); err != nil {
						return err
					}
					// Tracked plan paths are invisible to the legacy untracked-file finder.
					file := filepath.Join(projectDir, workspace+".tfplan")
					if err := os.WriteFile(file, []byte("stale checkout plan"), 0600); err != nil {
						return err
					}
					if err := exec.Command("git", "-C", dir, "add", ".").Run(); err != nil {
						return err
					}
					return os.WriteFile(filepath.Join(dir, "stray-"+workspace+".tfplan"), []byte("unregistered plan"), 0600)
				},
			}
			logger := logging.NewNoopLogger(t)
			scope := metricstest.NewLoggingScope(t, logger, "atlantis")
			builder := events.NewProjectCommandBuilder(false, &config.ParserValidator{}, &events.DefaultProjectFinder{}, nil,
				workingDir, events.NewDefaultWorkingDirLocker(), valid.NewGlobalCfgFromArgs(valid.GlobalCfgArgs{}),
				nil, &events.CommentParser{ExecutableName: "atlantis"}, false, false, false, false, false,
				"", defaultUserConfig.AutoplanFileList, false, "", false, false, "auto", scope, tfclientmocks.NewMockClient(),
				service.Artifacts)
			ctx := &command.Context{Log: logger, Scope: scope, Pull: pull, PullStatus: &models.PullStatus{Projects: projects}}
			commands, err := builder.BuildApplyCommands(ctx, &events.CommentCommand{Name: command.Apply})
			Ok(t, err)
			Equals(t, 2, len(commands))
			for _, c := range commands {
				Equals(t, "infra", c.RepoRelDir)
				Assert(t, c.ExpectedPlanHash != "", "restored plan was not hashed")
				content, err := os.ReadFile(filepath.Join(pullDir, c.Workspace, "infra", c.Workspace+".tfplan"))
				Ok(t, err)
				Equals(t, "durable plan", string(content))
			}

			apply := planContexts[0]
			apply.JobID = uuid.NewString()
			apply.CommandName = command.Apply
			Ok(t, service.Begin(apply))
			Ok(t, service.Finish(apply, command.ProjectCommandOutput{}))
			lock, err := db.GetLock(repo, models.NewProject(repo.FullName, "infra", ""), "default")
			Ok(t, err)
			Assert(t, lock != nil, "apply completion released the project lock")
			Equals(t, pull.Num, lock.Pull.Num)
			ctx.PullStatus.Projects[0].Status = models.AppliedPlanStatus
			commands, err = builder.BuildApplyCommands(ctx, &events.CommentCommand{Name: command.Apply})
			Ok(t, err)
			Equals(t, 1, len(commands))
			Equals(t, "staging", commands[0].Workspace)
			locks, err := db.UnlockByPull(repo, pull.Num)
			Ok(t, err)
			Equals(t, 2, len(locks))
			pending, err := db.PendingPlans(repo, pull.Num)
			Ok(t, err)
			Equals(t, 0, len(pending))
		})
	}
}

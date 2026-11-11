// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package events

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/runatlantis/atlantis/server/core/config"
	"github.com/runatlantis/atlantis/server/core/config/valid"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/events/vcs"
	"github.com/runatlantis/atlantis/server/logging"
	. "github.com/runatlantis/atlantis/testing"
)

var errBuilderCloneReached = errors.New("clone reached")

type builderLockWorkingDir struct {
	WorkingDir
	directory string
	pullDir   string
	clones    int
}

func (d *builderLockWorkingDir) Clone(logging.SimpleLogging, models.Repo, models.PullRequest, string) (string, error) {
	d.clones++
	return "", errBuilderCloneReached
}
func (d *builderLockWorkingDir) GetWorkingDir(models.Repo, models.PullRequest, string) (string, error) {
	return d.directory, nil
}

func (d *builderLockWorkingDir) GetPullDir(models.Repo, models.PullRequest) (string, error) {
	return d.pullDir, nil
}

type builderLockVCS struct{ vcs.Client }

func (builderLockVCS) GetModifiedFiles(logging.SimpleLogging, models.Repo, models.PullRequest) ([]string, error) {
	return []string{"main.tf"}, nil
}

func TestBuilderContendsWithRuntimeWorkingDirectoryLock(t *testing.T) {
	cases := []struct {
		name, project string
		build         func(*DefaultProjectCommandBuilder, *command.Context) ([]command.ProjectContext, error)
	}{
		{"all configured projects", "", func(b *DefaultProjectCommandBuilder, c *command.Context) ([]command.ProjectContext, error) {
			return b.buildAllProjectsByCfg(c, command.Plan, "", nil, false)
		}},
		{"modified projects", "", func(b *DefaultProjectCommandBuilder, c *command.Context) ([]command.ProjectContext, error) {
			return b.buildAllCommandsByCfg(c, command.Plan, "", nil, false)
		}},
		{"named plan", "project", func(b *DefaultProjectCommandBuilder, c *command.Context) ([]command.ProjectContext, error) {
			return b.buildProjectPlanCommand(c, &CommentCommand{Name: command.Plan, ProjectName: "project"})
		}},
		{"named apply", "project", func(b *DefaultProjectCommandBuilder, c *command.Context) ([]command.ProjectContext, error) {
			return b.buildProjectCommand(c, &CommentCommand{Name: command.Apply, ProjectName: "project"})
		}},
	}
	for _, durable := range []bool{false, true} {
		mode := map[bool]string{false: "legacy", true: "durable"}[durable]
		for _, tc := range cases {
			t.Run(mode+"/"+tc.name, func(t *testing.T) {
				repo := models.Repo{FullName: "org/repo", VCSHost: models.VCSHost{Hostname: "git.example"}}
				if durable {
					repo.Execution = &models.Execution{Owner: "running-command"}
				}
				pull := models.PullRequest{Num: 1, BaseRepo: repo, Execution: repo.Execution}
				locker := NewDefaultWorkingDirLocker()
				release, err := locker.TryLock(WorkingDirectoryRepoKey(repo), pull.Num, DefaultWorkspace, DefaultRepoRelDir, tc.project, command.Plan, WorkingDirLockMetadata{})
				Ok(t, err)
				defer release()
				directory := &builderLockWorkingDir{directory: t.TempDir()}
				builder := &DefaultProjectCommandBuilder{WorkingDirLocker: locker, WorkingDir: directory, VCSClient: builderLockVCS{}, IncludeGitUntrackedFiles: true, ParserValidator: &config.ParserValidator{}, GlobalCfg: valid.NewGlobalCfgFromArgs(valid.GlobalCfgArgs{})}
				_, err = tc.build(builder, &command.Context{Pull: pull, HeadRepo: repo, Log: logging.NewNoopLogger(t)})
				ErrContains(t, "currently locked", err)
				Equals(t, 0, directory.clones)
			})
		}
	}
}

func TestBuilderWorkingDirectoryLockSeparatesVCSInstances(t *testing.T) {
	repo := models.Repo{FullName: "org/repo", VCSHost: models.VCSHost{Hostname: "first.example"}, Execution: &models.Execution{Owner: "running-command"}}
	locker := NewDefaultWorkingDirLocker()
	release, err := locker.TryLock(WorkingDirectoryRepoKey(repo), 1, DefaultWorkspace, DefaultRepoRelDir, "", command.Plan, WorkingDirLockMetadata{})
	Ok(t, err)
	defer release()
	repo.VCSHost.Hostname = "second.example"
	directory := &builderLockWorkingDir{directory: t.TempDir()}
	builder := &DefaultProjectCommandBuilder{WorkingDirLocker: locker, WorkingDir: directory}
	_, err = builder.buildAllProjectsByCfg(&command.Context{Pull: models.PullRequest{Num: 1, BaseRepo: repo, Execution: repo.Execution}, HeadRepo: repo, Log: logging.NewNoopLogger(t)}, command.Plan, "", nil, false)
	Assert(t, errors.Is(err, errBuilderCloneReached), "another VCS instance was blocked: %v", err)
	Equals(t, 1, directory.clones)
}

func TestApplyAllBuilderUsesRuntimeLockKeys(t *testing.T) {
	for _, activePlan := range []bool{false, true} {
		name := map[bool]string{false: "project workspace", true: "active plan"}[activePlan]
		t.Run(name, func(t *testing.T) {
			repo := models.Repo{FullName: "org/repo", VCSHost: models.VCSHost{Hostname: "git.example"}, Execution: &models.Execution{Owner: "running-command"}}
			pull := models.PullRequest{Num: 1, BaseRepo: repo, Execution: repo.Execution, HeadCommit: "revision", BaseBranch: "main"}
			root := t.TempDir()
			directory := filepath.Join(root, DefaultWorkspace)
			Ok(t, os.MkdirAll(directory, 0700))
			output, err := exec.Command("git", "init", directory).CombinedOutput()
			Assert(t, err == nil, "git init: %v: %s", err, output)
			Ok(t, os.WriteFile(filepath.Join(directory, "default.tfplan"), []byte("saved plan"), 0600))
			locker := NewDefaultWorkingDirLocker()
			heldCommand := command.Version
			if activePlan {
				heldCommand = command.Plan
			}
			release, err := locker.TryLock(WorkingDirectoryRepoKey(repo), pull.Num, DefaultWorkspace, DefaultRepoRelDir, "", heldCommand, WorkingDirLockMetadata{})
			Ok(t, err)
			defer release()
			workingDir := &builderLockWorkingDir{directory: directory, pullDir: root}
			builder := &DefaultProjectCommandBuilder{WorkingDirLocker: locker, WorkingDir: workingDir, PendingPlanFinder: &DefaultPendingPlanFinder{}, ParserValidator: &config.ParserValidator{}, GlobalCfg: valid.NewGlobalCfgFromArgs(valid.GlobalCfgArgs{})}
			ctx := &command.Context{Pull: pull, HeadRepo: repo, Log: logging.NewNoopLogger(t), PullStatus: &models.PullStatus{Pull: pull, Projects: []models.ProjectStatus{{RepoRelDir: DefaultRepoRelDir, Workspace: DefaultWorkspace, Status: models.PlannedPlanStatus}}}}
			_, err = builder.buildAllProjectCommandsByPlan(ctx, &CommentCommand{Name: command.Apply})
			if activePlan {
				ErrContains(t, "a plan is currently running", err)
			} else {
				ErrContains(t, "currently locked", err)
			}
		})
	}
}

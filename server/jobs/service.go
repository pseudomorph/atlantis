// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package jobs

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
)

// Service records durable jobs for the project commands that own an execution claim.
type Service struct {
	Store     db.JobStore
	runs      sync.Map
	lifecycle sync.RWMutex
	closed    bool
}

func (s *Service) Begin(ctx command.ProjectContext) error {
	j := jobFromContext(ctx)
	steps := make([]models.JobStep, 0, len(ctx.Steps))
	for _, step := range ctx.Steps {
		steps = append(steps, models.JobStep{Name: step.StepName, HasExtraArgs: len(step.ExtraArgs) > 0})
	}
	encodedSteps, err := json.Marshal(steps)
	if err != nil {
		return err
	}
	j.Steps = string(encodedSteps)
	return s.Start(j)
}

func (s *Service) Start(j models.Job) error {
	s.lifecycle.RLock()
	defer s.lifecycle.RUnlock()
	if s.closed {
		return fmt.Errorf("job service is closed")
	}
	j.Owner = j.Pull.ExecutionOwner()
	if j.Kind == "" {
		j.Kind = models.CommandJob
	}
	if err := s.Store.StartJob(j); err != nil {
		return err
	}
	s.runs.Store(j.ID, j)
	return nil
}

func (s *Service) Finish(ctx command.ProjectContext, result command.ProjectCommandOutput) error {
	message := ""
	if result.Error != nil || result.Failure != "" {
		message = "command failed"
	}
	if err := s.Complete(ctx.JobID, message); err != nil {
		return err
	}
	completed, err := s.Store.Job(ctx.JobID)
	if err != nil {
		return err
	}
	if completed != nil && completed.State == "cancelled" {
		return fmt.Errorf("execution cancelled")
	}
	return nil
}

// Complete records the outcome of a job this service started.
func (s *Service) Complete(id, message string) error {
	if _, ok := s.runs.LoadAndDelete(id); !ok {
		return fmt.Errorf("job writer is missing")
	}
	state := "succeeded"
	if message != "" {
		state = "failed"
	}
	return s.Store.FinishJob(id, state, message)
}

func jobFromContext(ctx command.ProjectContext) models.Job {
	trigger := "comment"
	if ctx.API {
		trigger = "api"
	} else if ctx.Trigger == command.AutoTrigger {
		trigger = "automatic"
	}
	tool, toolVersion := "", ""
	if ctx.TerraformDistribution != nil {
		tool = *ctx.TerraformDistribution
	}
	if ctx.TerraformVersion != nil {
		toolVersion = ctx.TerraformVersion.String()
	}
	return models.Job{ToolDistribution: tool, ToolVersion: toolVersion, Kind: models.CommandJob, ManagedPlan: ctx.RequiresAtlantisManagedPlanFile, ID: ctx.JobID, Owner: ctx.Pull.ExecutionOwner(), Pull: ctx.Pull, Project: models.NewProject(ctx.BaseRepo.FullName, ctx.RepoRelDir, ctx.ProjectName), Workspace: ctx.Workspace,
		Command: ctx.CommandName.String(), SubCommand: ctx.SubCommand, Arguments: ctx.CommentArgs, Issuer: ctx.User, Trigger: trigger,
		Revision: ctx.Pull.HeadCommit, State: "running", Private: ctx.SuppressJobOutput}
}

// Close rejects new jobs. Running jobs keep their claim and finish normally or
// become uncertain at the next startup.
func (s *Service) Close() error {
	s.lifecycle.Lock()
	s.closed = true
	s.lifecycle.Unlock()
	return nil
}

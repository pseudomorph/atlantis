// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/runatlantis/atlantis/server/core/artifacts"
	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
)

type Service struct {
	Store     db.JobStore
	Artifacts *artifacts.Service
	runs      sync.Map
	lifecycle sync.RWMutex
	closed    bool
}

type jobRun struct {
	job        models.Job
	mu         sync.Mutex
	output     strings.Builder
	err        error
	closed     bool
	stop, done chan struct{}
	once       sync.Once
}

func (s *Service) Begin(ctx command.ProjectContext) error {
	j := jobFromContext(ctx)
	steps, err := json.Marshal(ctx.Steps)
	if err != nil {
		return err
	}
	j.Steps = string(steps)
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
	run := &jobRun{job: j, stop: make(chan struct{}), done: make(chan struct{})}
	s.runs.Store(j.ID, run)
	go func() {
		defer close(run.done)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				s.flush(run)
			case <-run.stop:
				run.mu.Lock()
				run.closed = true
				s.flushLocked(run)
				run.mu.Unlock()
				return
			}
		}
	}()
	return nil
}

func (s *Service) Finish(ctx command.ProjectContext, result command.ProjectCommandOutput) error {
	message := ""
	if result.Error != nil || result.Failure != "" {
		message = "command failed"
	}
	finishErr := s.Complete(ctx.JobID, message)
	if finishErr != nil {
		return finishErr
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

// Complete flushes output before the database can mark the job terminal.
func (s *Service) Complete(id, message string) error {
	value, ok := s.runs.LoadAndDelete(id)
	if !ok {
		return fmt.Errorf("job output writer is missing")
	}
	run := value.(*jobRun)
	run.once.Do(func() { close(run.stop) })
	<-run.done
	run.mu.Lock()
	logErr := run.err
	run.mu.Unlock()
	// Durable output is required for job completion; failures remain visible to callers.
	state := "succeeded"
	if message != "" || logErr != nil {
		state = "failed"
		if logErr != nil {
			message = "job output could not be persisted"
		}
	}
	return errors.Join(logErr, s.Store.FinishJob(id, state, message))
}

func jobFromContext(ctx command.ProjectContext) models.Job {
	trigger := "comment"
	if ctx.API {
		trigger = "api"
	} else if ctx.Trigger == command.AutoTrigger {
		trigger = "automatic"
	}
	return models.Job{Kind: models.CommandJob, ManagedPlan: ctx.RequiresAtlantisManagedPlanFile, ID: ctx.JobID, Owner: ctx.Pull.ExecutionOwner(), Pull: ctx.Pull, Project: models.NewProject(ctx.BaseRepo.FullName, ctx.RepoRelDir, ctx.ProjectName), Workspace: ctx.Workspace,
		Command: ctx.CommandName.String(), SubCommand: ctx.SubCommand, Arguments: ctx.CommentArgs, Issuer: ctx.User, Trigger: trigger,
		Revision: ctx.Pull.HeadCommit, State: "running", Private: ctx.SuppressJobOutput}
}

func (s *Service) AppendLog(id, message string) error {
	value, ok := s.runs.Load(id)
	if !ok {
		return fmt.Errorf("job output writer is missing")
	}
	run := value.(*jobRun)
	run.mu.Lock()
	defer run.mu.Unlock()
	if run.closed {
		if run.err == nil {
			run.err = fmt.Errorf("job output writer is closed")
		}
		return run.err
	}
	if run.err != nil {
		return run.err
	}
	run.output.WriteString(message)
	run.output.WriteByte('\n')
	if run.output.Len() >= 64*1024 {
		s.flushLocked(run)
	}
	return run.err
}

func (s *Service) flush(run *jobRun) { run.mu.Lock(); defer run.mu.Unlock(); s.flushLocked(run) }
func (s *Service) flushLocked(run *jobRun) {
	if run.err != nil || run.output.Len() == 0 {
		return
	}
	run.err = s.Artifacts.Publish(run.job, "log", "", strings.NewReader(run.output.String()))
	run.output.Reset()
}

// Close flushes active writers without claiming that their commands completed.
func (s *Service) Close() error {
	s.lifecycle.Lock()
	s.closed = true
	s.lifecycle.Unlock()
	var err error
	s.runs.Range(func(_, value any) bool {
		run := value.(*jobRun)
		run.once.Do(func() { close(run.stop) })
		<-run.done
		run.mu.Lock()
		err = errors.Join(err, run.err)
		run.mu.Unlock()
		return true
	})
	return err
}

func (s *Service) BeginHook(ctx models.WorkflowHookCommandContext) error {
	return s.Start(models.Job{Kind: models.WorkflowHookJob, ID: ctx.HookID, Owner: ctx.Pull.ExecutionOwner(), Pull: ctx.Pull, Project: models.NewProject(ctx.BaseRepo.FullName, ctx.RepoRelDir, ctx.ProjectName), Workspace: ctx.Workspace,
		Command: ctx.HookStepName, Arguments: ctx.EscapedCommentArgs, Steps: ctx.HookDescription, Issuer: ctx.User, Trigger: "workflow-hook", Revision: ctx.Pull.HeadCommit, Private: ctx.SuppressJobOutput})
}

func (s *Service) FinishHook(id string, outcome error) error {
	message := ""
	if outcome != nil {
		message = "workflow hook failed"
	}
	return s.Complete(id, message)
}

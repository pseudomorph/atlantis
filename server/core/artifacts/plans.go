// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"github.com/runatlantis/atlantis/server/utils"
)

// Save defers publication until the runner has completed all plan workflow steps.
func (p *Service) Save(command.ProjectContext, string) error { return nil }
func (p *Service) Load(ctx command.ProjectContext, filename string) error {
	if ctx.ArtifactRoot == "" {
		return fmt.Errorf("artifact root is required for restoring a plan")
	}
	job, err := p.Records.Job(ctx.JobID)
	if err != nil {
		return err
	}
	if job == nil || job.PlanJobID == "" {
		return fmt.Errorf("no pending durable plan; run plan before applying")
	}
	plans, err := p.Records.PendingPlans(ctx.Pull.BaseRepo, ctx.Pull.Num)
	if err != nil {
		return err
	}
	for _, plan := range plans {
		if plan.ID != job.PlanJobID {
			continue
		}
		if plan.Revision != ctx.Pull.HeadCommit {
			return fmt.Errorf("pending plan revision differs; run plan again")
		}
		snapshot, err := p.Records.InputSnapshot(ctx.JobID)
		if err != nil {
			return err
		}
		if snapshot == nil || snapshot.JobID != plan.ID {
			return fmt.Errorf("pending plan snapshot is missing")
		}
		file, havePlan := snapshot.Manifest.File("plan")
		if ctx.ExpectedPlanHash != "" && (!havePlan || file.SHA256 != ctx.ExpectedPlanHash) {
			return fmt.Errorf("pending plan differs from expected plan checksum")
		}
		if plan.ManagedPlan && !havePlan {
			return fmt.Errorf("plan snapshot has no managed plan")
		}
		return p.restoreSnapshot(*snapshot, filename, filepath.Join(filepath.Dir(filename), ctx.GetShowResultFileName()), ctx.ArtifactRoot, ctx.ArtifactRoot, ctx.RestoredDataDirectory != "")
	}
	return fmt.Errorf("pending plan was invalidated or replaced; run plan again")
}
func (p *Service) restoreJob(job models.Job, planPath, showPath, root string) error {
	snapshot, err := p.Records.Snapshot(job.ID)
	if err != nil {
		return err
	}
	if snapshot == nil {
		return fmt.Errorf("plan snapshot is missing for job %q", job.ID)
	}
	if _, exists := snapshot.Manifest.File("plan"); job.ManagedPlan && !exists {
		return fmt.Errorf("plan snapshot has no managed plan")
	}
	return p.restoreSnapshot(*snapshot, planPath, showPath, root, filepath.Join(root, job.Workspace), false)
}

func (p *Service) Remove(_ command.ProjectContext, filename string) error {
	err := os.Remove(filename)
	showErr := os.Remove(strings.TrimSuffix(filename, ".tfplan") + ".json")
	if showErr != nil && !os.IsNotExist(showErr) {
		return showErr
	}
	if os.IsNotExist(err) {
		return nil
	}
	return err
}
func (p *Service) ListWorkspacesInRepo(repo models.Repo, number int) ([]string, error) {
	if repo.FullName == "" {
		return nil, fmt.Errorf("repository scope is required for durable plans")
	}
	plans, err := p.Records.PendingPlans(repo, number)
	if err != nil {
		return nil, err
	}
	var workspaces []string
	seen := map[string]bool{}
	for _, plan := range plans {
		if !seen[plan.Workspace] {
			workspaces = append(workspaces, plan.Workspace)
			seen[plan.Workspace] = true
		}
	}
	return workspaces, nil
}
func (p *Service) RestorePlansInRepo(directory string, repo models.Repo, number int) error {
	if directory == "" {
		return nil
	}
	if repo.FullName == "" {
		return fmt.Errorf("repository scope is required for durable plans")
	}
	plans, err := p.Records.PendingPlans(repo, number)
	if err != nil {
		return err
	}
	for _, plan := range plans {
		name := plan.Workspace
		if plan.Project.ProjectName != "" {
			name = strings.ReplaceAll(plan.Project.ProjectName, "/", "::") + "-" + plan.Workspace
		}
		target := filepath.Join(directory, plan.Workspace, plan.Project.Path, name+".tfplan")
		if err := utils.EnsureSubPath(directory, target); err != nil {
			return err
		}
		if err := p.restoreJob(plan, target, strings.TrimSuffix(target, ".tfplan")+".json", directory); err != nil {
			return err
		}
	}
	return nil
}
func (p *Service) DeleteForPullInRepo(repo models.Repo, number int) error {
	if repo.FullName == "" {
		return fmt.Errorf("repository scope is required for durable plans")
	}
	return p.Records.InvalidatePlans(repo, number)
}

// Releasing the scoped database lock already invalidates its pending plan. Retain artifacts.
func (p *Service) DeletePlanForProjectInRepo(_ models.Repo, _ int, _, _, _ string) error {
	return nil
}

// errRepositoryIdentityRequired rejects owner/name plan lookups. Durable plans
// are scoped by VCS host as well, so callers use the *InRepo methods.
var errRepositoryIdentityRequired = errors.New("durable plans require repository identity; use the repository-scoped plan store methods")

// ListWorkspaces satisfies the legacy plan store interface. Durable callers use ListWorkspacesInRepo.
func (p *Service) ListWorkspaces(_, _ string, _ int) ([]string, error) {
	return nil, errRepositoryIdentityRequired
}

// RestorePlans satisfies the legacy plan store interface. An empty directory
// is the restore capability probe; durable callers use RestorePlansInRepo.
func (p *Service) RestorePlans(directory, _, _ string, _ int) error {
	if directory == "" {
		return nil
	}
	return errRepositoryIdentityRequired
}

// DeleteForPull satisfies the legacy plan store interface. Durable callers use DeleteForPullInRepo.
func (p *Service) DeleteForPull(_, _ string, _ int) error {
	return errRepositoryIdentityRequired
}

// DeletePlanForProject satisfies the legacy plan store interface. Releasing the
// scoped lock already invalidates the pending plan, so there is nothing to do.
func (p *Service) DeletePlanForProject(_, _ string, _ int, _, _, _ string) error { return nil }

func (p *Service) PendingPlans(repo models.Repo, number int) ([]models.Job, error) {
	return p.Records.PendingPlans(repo, number)
}

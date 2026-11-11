// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package artifacts

import (
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
		records, err := p.Records.InputArtifacts(ctx.JobID)
		if err != nil {
			return err
		}
		root := ctx.ArtifactRoot
		verifiedPlan := ctx.ExpectedPlanHash == ""
		for _, a := range records {
			if a.Kind == "plan" {
				if ctx.ExpectedPlanHash != "" && a.SHA256 != ctx.ExpectedPlanHash {
					return fmt.Errorf("pending plan differs from expected plan checksum")
				}
				verifiedPlan = true
				break
			}
		}
		if !verifiedPlan {
			return fmt.Errorf("expected plan checksum requires a plan artifact")
		}
		return p.restoreArtifacts(plan, records, filename, filepath.Join(filepath.Dir(filename), ctx.GetShowResultFileName()), root, root)
	}
	return fmt.Errorf("pending plan was invalidated or replaced; run plan again")
}
func (p *Service) restoreJob(job models.Job, planPath, showPath, root string) error {
	records, err := p.Records.Artifacts(job.ID)
	if err != nil {
		return err
	}
	return p.restoreArtifacts(job, records, planPath, showPath, root, filepath.Join(root, job.Workspace))
}
func (p *Service) restoreArtifacts(job models.Job, records []models.Artifact, planPath, showPath, root, workflowRoot string) error {
	havePlan, haveShow := false, false
	for _, a := range records {
		if a.Kind == "workflow" || a.Kind == "lockfile" {
			if !filepath.IsLocal(a.RelativePath) {
				return fmt.Errorf("invalid workflow artifact destination")
			}
			if err := p.Restore(a, filepath.Join(workflowRoot, a.RelativePath), root); err != nil {
				return err
			}
		}
		if a.Kind == "plan" && !havePlan {
			if err := p.Restore(a, planPath, root); err != nil {
				return err
			}
			havePlan = true
		}
		if a.Kind == "show" && !haveShow {
			if err := p.Restore(a, showPath, root); err != nil {
				return err
			}
			haveShow = true
		}
	}
	if !havePlan && job.ManagedPlan {
		return fmt.Errorf("plan artifact missing for job %q", job.ID)
	}
	return nil
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
func (p *Service) ListWorkspaces(repo models.Repo, number int) ([]string, error) {
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
func (p *Service) RestorePlans(directory string, repo models.Repo, number int) error {
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
func (p *Service) DeleteForPull(repo models.Repo, number int) error {
	if repo.FullName == "" {
		return fmt.Errorf("repository scope is required for durable plans")
	}
	return p.Records.InvalidatePlans(repo, number)
}

// Releasing the scoped database lock already invalidates its pending plan. Retain artifacts.
func (p *Service) DeletePlanForProject(_ models.Repo, _ int, _, _, _ string) error { return nil }

func (p *Service) PendingPlans(repo models.Repo, number int) ([]models.Job, error) {
	return p.Records.PendingPlans(repo, number)
}

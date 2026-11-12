// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"errors"
	"fmt"

	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/events/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ db.JobStore = (*Store)(nil)

func (s *Store) StartJob(want models.Job) error {
	if want.Kind == "" {
		want.Kind = models.CommandJob
	}
	if want.Kind != models.CommandJob {
		return fmt.Errorf("invalid job kind")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		p, err := savePull(tx, want.Pull)
		if err != nil {
			return err
		}
		project, err := saveProject(tx, p.RepositoryID, want.Project.Path, want.Project.ProjectName, want.Workspace)
		if err != nil {
			return err
		}
		if err := s.guard(tx, want.Pull); err != nil {
			return err
		}
		// Serialize admission with cancellation, closure and lifecycle verification.
		var stopped struct{ Cancelled, Closed bool }
		if err := tx.Table("pulls").Select("cancelled", "closed").Where("id = ?", p.ID).Take(&stopped).Error; err != nil {
			return err
		}
		if stopped.Cancelled || stopped.Closed {
			return fmt.Errorf("execution cancelled or pull request closed")
		}
		var pendingVerification int64
		if err := tx.Table("pending_pull_lifecycles").Where("pull_id = ?", p.ID).Count(&pendingVerification).Error; err != nil {
			return err
		}
		if pendingVerification != 0 {
			return fmt.Errorf("pull request closure is awaiting VCS verification; retry shortly")
		}
		startedAt := databaseTime(want.StartedAt)
		if want.StartedAt.IsZero() {
			startedAt, err = databaseNow(tx)
			if err != nil {
				return err
			}
		}
		row := job{ToolDistribution: want.ToolDistribution, ToolVersion: want.ToolVersion, Kind: want.Kind, ManagedPlan: want.ManagedPlan, Owner: want.Pull.ExecutionOwner(), ID: want.ID, RepositoryID: p.RepositoryID, PullID: p.ID, ProjectID: &project.ID, Command: want.Command, SubCommand: want.SubCommand,
			Arguments: want.Arguments, Steps: want.Steps, Issuer: want.Issuer.Username, IssuerTeams: want.Issuer.Teams, Trigger: want.Trigger, Revision: want.Revision,
			State: "running", Private: want.Private, StartedAt: startedAt}
		var binding *planBinding
		if consumesPlan(want.Command) {
			var pending struct{ JobID, Kind, Command, State string }
			err := tx.Table("pending_plans AS pending").Select("pending.job_id, producer.kind, producer.command, producer.state").
				Joins("JOIN jobs AS producer ON producer.id = pending.job_id").
				Where("pending.project_id = ? AND pending.pull_id = ? AND pending.ready = ?", project.ID, p.ID, true).Take(&pending).Error
			if err == nil {
				if pending.Kind != string(models.CommandJob) || pending.Command != "plan" || pending.State != "succeeded" {
					return fmt.Errorf("selected pending producer is not a completed plan")
				}
				binding = &planBinding{JobID: row.ID, PlanJobID: pending.JobID, ProjectID: project.ID, PullID: p.ID}
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("creating job: %w", err)
		}
		if binding != nil {
			if err := tx.Create(binding).Error; err != nil {
				return fmt.Errorf("binding plan: %w", err)
			}
		}
		return nil
	})
}

func (s *Store) FinishJob(id, state, message string) error {
	if state != "succeeded" && state != "failed" && state != "cancelled" && state != "interrupted" {
		return fmt.Errorf("invalid terminal job state %q", state)
	}
	var completionErr error
	err := s.db.Transaction(func(tx *gorm.DB) error {
		// mutation_started must be read under the lock that MarkJobMutating takes.
		row, err := s.lockedJob(tx, "id = ?", id)
		if err != nil {
			return err
		}
		var closed struct{ Closed bool }
		if err := tx.Table("pulls").Select("closed").Where("id = ?", row.PullID).Take(&closed).Error; err != nil {
			return err
		}
		succeeded := state == "succeeded"
		now, err := databaseNow(tx)
		if err != nil {
			return err
		}
		result := tx.Model(&job{}).Where("id = ? AND state = ?", id, "running").Updates(map[string]any{"state": state, "error": message, "finished_at": now})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return fmt.Errorf("job %q is no longer running", id)
		}
		if row.Kind == models.CommandJob && row.Command == "plan" {
			if state != "succeeded" || closed.Closed {
				return tx.Where("job_id = ?", id).Delete(&pendingPlan{}).Error
			}
			var snapshot planSnapshot
			err := tx.Where("job_id = ? AND ready = ? AND deleted = ?", id, true, false).Take(&snapshot).Error
			if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
			var count int64
			if err == nil {
				output, err := decodeSnapshot(snapshot)
				if err != nil {
					return err
				}
				_, hasPlan := output.Manifest.File("plan")
				hasWorkflow := false
				for _, file := range output.Manifest.Files {
					if file.Kind == "workflow" {
						hasWorkflow = true
					}
				}
				if hasPlan || (!row.ManagedPlan && hasWorkflow) {
					count = 1
				}
			}
			if count == 0 {
				return tx.Where("job_id = ?", id).Delete(&pendingPlan{}).Error
			}
			result := tx.Model(&pendingPlan{}).Where("job_id = ?", id).Update("ready", true)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected == 0 {
				// Closure may invalidate a plan between the earlier read and publication.
				if err := tx.Table("pulls").Select("closed").Where("id = ?", row.PullID).Take(&closed).Error; err != nil {
					return err
				}
				if closed.Closed {
					return nil
				}
				completionErr = fmt.Errorf("plan job %q was superseded or invalidated", id)
				return tx.Model(&job{}).Where("id = ?", id).Updates(map[string]any{"state": "failed", "error": completionErr.Error()}).Error
			}
		}
		if row.Kind == models.CommandJob && (row.Command == "apply" || row.Command == "import" || row.Command == "state") && (succeeded || row.MutationStarted) {
			planJobID, err := boundPlan(tx, row.ID)
			if err != nil || planJobID == "" {
				return err
			}
			return tx.Where("job_id = ?", planJobID).Delete(&pendingPlan{}).Error
		}
		return nil
	})
	return errors.Join(err, completionErr)
}

func loadJob(tx *gorm.DB, row job) (models.Job, error) {
	p, err := loadPull(tx, row.PullID)
	if err != nil {
		return models.Job{}, err
	}
	p.HeadCommit = row.Revision
	var selectedProject models.Project
	workspace := ""
	if row.ProjectID != nil {
		var project project
		if err := tx.Where("id = ?", *row.ProjectID).Take(&project).Error; err != nil {
			return models.Job{}, err
		}
		selectedProject = models.NewProject(p.BaseRepo.FullName, project.Directory, project.Name)
		workspace = project.Workspace
	}
	result := models.Job{ToolDistribution: row.ToolDistribution, ToolVersion: row.ToolVersion, Kind: row.Kind, ManagedPlan: row.ManagedPlan, ID: row.ID, Owner: row.Owner, Pull: p, Project: selectedProject, Workspace: workspace,
		Command: row.Command, SubCommand: row.SubCommand, Arguments: row.Arguments, Steps: row.Steps, Issuer: models.User{Username: row.Issuer, Teams: row.IssuerTeams},
		Trigger: row.Trigger, Revision: row.Revision, State: row.State, Error: row.Error, Private: row.Private, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt}
	result.PlanJobID, err = boundPlan(tx, row.ID)
	return result, err
}
func (s *Store) Job(id string) (*models.Job, error) {
	var result models.Job
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var row job
		if err := tx.Where("id = ?", id).Take(&row).Error; err != nil {
			return err
		}
		var err error
		result, err = loadJob(tx, row)
		return err
	})
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	return &result, err
}
func (s *Store) Jobs() ([]models.Job, error) {
	var result []models.Job
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var rows []job
		if err := tx.Where("private = ?", false).Order("started_at DESC, id").Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			j, err := loadJob(tx, row)
			if err != nil {
				return err
			}
			result = append(result, j)
		}
		return nil
	})
	return result, err
}
func (s *Store) artifactJob(tx *gorm.DB, id string) (job, error) {
	return s.lockedJob(tx, "id = ? AND state = ?", id, "running")
}

// lockedJob finds a job, locks its pull and claim, then reads it again. Only
// lock holders change job state, so the second read stays current until commit.
func (s *Store) lockedJob(tx *gorm.DB, query string, args ...any) (job, error) {
	var found job
	if err := tx.Where(query, args...).Take(&found).Error; err != nil {
		return found, err
	}
	if err := s.guardJob(tx, found); err != nil {
		return found, err
	}
	var row job
	err := tx.Where(query, args...).Take(&row).Error
	return row, err
}

func (s *Store) PendingPlans(repo models.Repo, number int) ([]models.Job, error) {
	var result []models.Job
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var rows []job
		ids := tx.Model(&pendingPlan{}).Select("job_id").Where("pull_id IN (?) AND ready = ?", pullIDs(tx, repo, number), true)
		if err := tx.Where("id IN (?)", ids).Order("started_at, id").Find(&rows).Error; err != nil {
			return err
		}
		for _, row := range rows {
			j, err := loadJob(tx, row)
			if err != nil {
				return err
			}
			result = append(result, j)
		}
		return nil
	})
	return result, err
}
func (s *Store) InvalidatePlans(repo models.Repo, number int) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		p := models.PullRequest{BaseRepo: repo, Num: number, Execution: repo.Execution}
		if err := s.guardRemoval(tx, p); err != nil {
			return err
		}
		return tx.Where("pull_id IN (?)", pullIDs(tx, repo, number)).Delete(&pendingPlan{}).Error
	})
}

// StartPlan changes the pending generation only after the runner acquires its execution locks.
func (s *Store) StartPlan(id string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		row, err := s.lockedJob(tx, "id = ? AND command = ? AND state = ? AND kind = ?", id, "plan", "running", models.CommandJob)
		if err != nil {
			return err
		}
		pending := pendingPlan{ProjectID: *row.ProjectID, PullID: row.PullID, JobID: id}
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "project_id"}, {Name: "pull_id"}}, DoUpdates: clause.AssignmentColumns([]string{"pull_id", "job_id", "ready"})}).Create(&pending).Error
	})
}

func consumesPlan(command string) bool {
	switch command {
	case "apply", "policy_check", "approve_policies", "import", "state":
		return true
	}
	return false
}

// MarkJobMutating records the boundary after which failure requires a new plan.
func (s *Store) MarkJobMutating(id string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.artifactJob(tx, id); err != nil {
			return err
		}
		return tx.Model(&job{}).Where("id = ?", id).Update("mutation_started", true).Error
	})
}

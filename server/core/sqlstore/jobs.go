// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"errors"
	"fmt"
	"time"

	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/events/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var _ db.JobStore = (*Store)(nil)

type job struct {
	MutationStarted                 bool
	Kind                            models.JobKind
	ManagedPlan                     bool
	Scope                           string
	ID                              string `gorm:"primaryKey"`
	Owner                           string
	RepositoryID, PullID, ProjectID string
	Command, SubCommand             string
	Arguments                       []string `gorm:"serializer:json"`
	Steps, Issuer                   string
	IssuerTeams                     []string `gorm:"serializer:json"`
	Trigger, Revision               string
	PlanJobID                       *string
	State, Error                    string
	Private                         bool
	StartedAt                       time.Time
	FinishedAt                      *time.Time
}

// artifact tracks publication and cleanup separately from the immutable content record.
type artifact struct {
	models.Artifact        `gorm:"embedded"`
	Ready, Deleted, Purged bool
	UnreferencedAt         *time.Time
}

func artifactRecords(rows []artifact) []models.Artifact {
	out := make([]models.Artifact, 0, len(rows))
	for _, row := range rows {
		out = append(out, row.Artifact)
	}
	return out
}

type pendingPlan struct {
	ProjectID string `gorm:"primaryKey;autoIncrement:false"`
	PullID    string `gorm:"primaryKey;autoIncrement:false"`
	JobID     string
	Ready     bool
}

func (s *Store) StartJob(want models.Job) error {
	if want.Kind == "" {
		want.Kind = models.CommandJob
	}
	if want.Kind != models.CommandJob && want.Kind != models.WorkflowHookJob {
		return fmt.Errorf("invalid job kind")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		p, err := savePull(tx, want.Pull)
		if err != nil {
			return err
		}
		if err := saveExecutionScope(tx, want.Pull, p); err != nil {
			return err
		}
		project, err := saveProject(tx, p.RepositoryID, want.Project.Path, want.Project.ProjectName, want.Workspace)
		if err != nil {
			return err
		}
		if err := s.guard(tx, want.Pull); err != nil {
			return err
		}
		// savePull locks the PR row, so cancellation cannot race this job's start.
		var stopped struct{ Cancelled, Closed bool }
		if err := tx.Table("pulls").Select("cancelled", "closed").Where("id = ?", p.ID).Take(&stopped).Error; err != nil {
			return err
		}
		if stopped.Cancelled || stopped.Closed {
			return fmt.Errorf("execution cancelled or pull request closed")
		}
		startedAt := databaseTime(want.StartedAt)
		if want.StartedAt.IsZero() {
			startedAt, err = databaseNow(tx)
			if err != nil {
				return err
			}
		}
		row := job{Kind: want.Kind, ManagedPlan: want.ManagedPlan, Scope: want.Pull.ExecutionScopeID(), Owner: want.Pull.ExecutionOwner(), ID: want.ID, RepositoryID: p.RepositoryID, PullID: p.ID, ProjectID: project.ID, Command: want.Command, SubCommand: want.SubCommand,
			Arguments: want.Arguments, Steps: want.Steps, Issuer: want.Issuer.Username, IssuerTeams: want.Issuer.Teams, Trigger: want.Trigger, Revision: want.Revision,
			State: "running", Private: want.Private, StartedAt: startedAt}
		if want.Kind == models.CommandJob && consumesPlan(want.Command) {
			var pending pendingPlan
			err := tx.Where("project_id = ? AND pull_id = ? AND ready = ? AND job_id IN (?)", project.ID, p.ID, true, tx.Model(&job{}).Select("id").Where("scope = ?", row.Scope)).Take(&pending).Error
			if err == nil {
				row.PlanJobID = &pending.JobID
			} else if !errors.Is(err, gorm.ErrRecordNotFound) {
				return err
			}
		}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("creating job: %w", err)
		}

		if row.PlanJobID != nil {
			if err := tx.Exec(`INSERT INTO artifact_inputs(job_id,artifact_id)
 SELECT ?, id FROM artifacts WHERE job_id = ? AND ready = ? AND deleted = ? AND kind <> 'log' `, row.ID, *row.PlanJobID, true, false).Error; err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *Store) FinishJob(id, state, message string) error {
	var completionErr error
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var row job
		if err := tx.Where("id = ?", id).Take(&row).Error; err != nil {
			return err
		}
		if err := s.guardJob(tx, row); err != nil {
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
			var count int64
			if err := tx.Table("artifacts").Where("job_id = ? AND kind = ? AND ready = ? AND deleted = ?", id, "plan", true, false).Count(&count).Error; err != nil {
				return err
			}
			if count == 0 && !row.ManagedPlan {
				if err := tx.Table("artifacts").Where("job_id = ? AND kind = ? AND ready = ? AND deleted = ?", id, "workflow", true, false).Count(&count).Error; err != nil {
					return err
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
		if row.Kind == models.CommandJob && (row.Command == "apply" || row.Command == "import" || row.Command == "state") && row.PlanJobID != nil && (succeeded || row.MutationStarted) {
			return tx.Where("job_id = ?", *row.PlanJobID).Delete(&pendingPlan{}).Error
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
	var project project
	if err := tx.Where("id = ?", row.ProjectID).Take(&project).Error; err != nil {
		return models.Job{}, err
	}
	result := models.Job{Kind: row.Kind, ManagedPlan: row.ManagedPlan, Scope: row.Scope, ID: row.ID, Owner: row.Owner, Pull: p, Project: models.NewProject(p.BaseRepo.FullName, project.Directory, project.Name), Workspace: project.Workspace,
		Command: row.Command, SubCommand: row.SubCommand, Arguments: row.Arguments, Steps: row.Steps, Issuer: models.User{Username: row.Issuer, Teams: row.IssuerTeams},
		Trigger: row.Trigger, Revision: row.Revision, State: row.State, Error: row.Error, Private: row.Private, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt}
	if row.PlanJobID != nil {
		result.PlanJobID = *row.PlanJobID
	}
	return result, nil
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
	var row job
	if err := tx.Where("id = ? AND state = ?", id, "running").Take(&row).Error; err != nil {
		return row, err
	}
	return row, s.guardJob(tx, row)
}

// BeginArtifact records intent before bytes are written, so failed writes remain discoverable.
func (s *Store) BeginArtifact(a models.Artifact) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.artifactJob(tx, a.JobID); err != nil {
			return err
		}
		if a.CreatedAt.IsZero() {
			var err error
			a.CreatedAt, err = databaseNow(tx)
			if err != nil {
				return err
			}
		} else {
			a.CreatedAt = databaseTime(a.CreatedAt)
		}
		if err := tx.Table("artifacts").Select("COALESCE(MAX(sequence), 0) + 1").Where("job_id = ?", a.JobID).Scan(&a.Sequence).Error; err != nil {
			return err
		}
		return tx.Create(&artifact{Artifact: a}).Error
	})
}

// CommitArtifact makes a completed write visible only while its execution still owns the job.
func (s *Store) CommitArtifact(a models.Artifact) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.artifactJob(tx, a.JobID); err != nil {
			return err
		}
		result := tx.Model(&artifact{}).Where("id = ? AND job_id = ? AND location = ? AND ready = ?", a.ID, a.JobID, a.Location, false).Updates(map[string]any{"size": a.Size, "sha256": a.SHA256, "ready": true})
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("artifact publication intent is missing or already committed")
		}
		return nil
	})
}

func (s *Store) Artifacts(jobID string) ([]models.Artifact, error) {
	var rows []artifact
	err := s.db.Where("job_id = ? AND deleted = ? AND ready = ?", jobID, false, true).Order("sequence").Find(&rows).Error
	return artifactRecords(rows), err
}

func (s *Store) LogArtifactsAfter(jobID string, sequence int64) ([]models.Artifact, error) {
	var rows []artifact
	err := s.db.Where("job_id = ? AND kind = ? AND deleted = ? AND ready = ? AND sequence > ?", jobID, "log", false, true, sequence).Order("sequence").Find(&rows).Error
	return artifactRecords(rows), err
}

func (s *Store) InputArtifacts(jobID string) ([]models.Artifact, error) {
	var rows []artifact
	err := s.db.Where("id IN (?) AND deleted = ? AND ready = ?", s.db.Table("artifact_inputs").Select("artifact_id").Where("job_id = ?", jobID), false, true).Order("sequence").Find(&rows).Error
	return artifactRecords(rows), err
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

// MarkPreviousExecutionsUncertain blocks work left by the prior server.
// The SQLite process lock proves the server exited, not that Terraform children stopped.
func (s *Store) MarkPreviousExecutionsUncertain() error {
	return s.db.Model(&executionClaim{}).Where("state = ?", "active").Update("state", "uncertain").Error
}

// StartPlan changes the pending generation only after the runner acquires its execution locks.
func (s *Store) StartPlan(id string) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		var row job
		if err := tx.Where("id = ? AND command = ? AND state = ? AND kind = ?", id, "plan", "running", models.CommandJob).Take(&row).Error; err != nil {
			return err
		}
		if err := s.guardJob(tx, row); err != nil {
			return err
		}
		pending := pendingPlan{ProjectID: row.ProjectID, PullID: row.PullID, JobID: id}
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
		row, err := s.artifactJob(tx, id)
		if err != nil {
			return err
		}
		if row.Kind != models.CommandJob {
			return fmt.Errorf("workflow hooks cannot mutate plan state")
		}
		return tx.Model(&job{}).Where("id = ?", id).Update("mutation_started", true).Error
	})
}

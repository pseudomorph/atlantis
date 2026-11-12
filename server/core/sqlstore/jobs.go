// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"errors"
	"fmt"

	"github.com/runatlantis/atlantis/server/core/db"
	"github.com/runatlantis/atlantis/server/events/models"
	"gorm.io/gorm"
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
		row := job{Kind: want.Kind, Owner: want.Pull.ExecutionOwner(), ID: want.ID, RepositoryID: p.RepositoryID, PullID: p.ID, ProjectID: &project.ID, Command: want.Command, SubCommand: want.SubCommand,
			Arguments: want.Arguments, Steps: want.Steps, Issuer: want.Issuer.Username, IssuerTeams: want.Issuer.Teams, Trigger: want.Trigger, Revision: want.Revision,
			State: "running", Private: want.Private, StartedAt: startedAt}
		if err := tx.Create(&row).Error; err != nil {
			return fmt.Errorf("creating job: %w", err)
		}
		return nil
	})
}

func (s *Store) FinishJob(id, state, message string) error {
	if state != "succeeded" && state != "failed" && state != "cancelled" && state != "interrupted" {
		return fmt.Errorf("invalid terminal job state %q", state)
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		if _, err := s.lockedJob(tx, "id = ?", id); err != nil {
			return err
		}
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
		return nil
	})
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
	return models.Job{Kind: row.Kind, ID: row.ID, Owner: row.Owner, Pull: p, Project: selectedProject, Workspace: workspace,
		Command: row.Command, SubCommand: row.SubCommand, Arguments: row.Arguments, Steps: row.Steps, Issuer: models.User{Username: row.Issuer, Teams: row.IssuerTeams},
		Trigger: row.Trigger, Revision: row.Revision, State: row.State, Error: row.Error, Private: row.Private, StartedAt: row.StartedAt, FinishedAt: row.FinishedAt}, nil
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

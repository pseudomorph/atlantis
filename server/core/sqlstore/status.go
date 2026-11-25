// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"errors"
	"fmt"

	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type pullStatus struct {
	PullID               string `gorm:"primaryKey;autoIncrement:false"`
	Revision, BaseBranch string
}

type projectStatus struct {
	PullID, ProjectID, RepositoryID string
	Position                        int
	Status                          models.ProjectPlanStatus
	PlanHash                        string
	Policies                        []models.PolicySetStatus `gorm:"serializer:json"`
}

func readStatus(tx *gorm.DB, p models.PullRequest) (*models.PullStatus, error) {
	var row pullStatus
	err := tx.Where("pull_id IN (?)", pullIDs(tx, p.BaseRepo, p.Num)).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	stored, err := loadPull(tx, row.PullID)
	if err != nil {
		return nil, err
	}
	stored.HeadCommit, stored.BaseBranch = row.Revision, row.BaseBranch
	result := &models.PullStatus{Pull: stored}
	var rows []projectStatus
	if err := tx.Where("pull_id = ?", row.PullID).Order("position").Find(&rows).Error; err != nil {
		return nil, err
	}
	for _, status := range rows {
		var project project
		if err := tx.Where("id = ?", status.ProjectID).Take(&project).Error; err != nil {
			return nil, err
		}
		result.Projects = append(result.Projects, models.ProjectStatus{RepoRelDir: project.Directory, ProjectName: project.Name, Workspace: project.Workspace, Status: status.Status, PolicyStatus: status.Policies, PlanHash: status.PlanHash})
	}
	return result, nil
}

func (s *Store) GetPullStatus(p models.PullRequest) (*models.PullStatus, error) {
	var result *models.PullStatus
	err := s.db.Transaction(func(tx *gorm.DB) error { var err error; result, err = readStatus(tx, p); return err })
	if err != nil {
		return nil, fmt.Errorf("reading pull status: %w", err)
	}
	return result, nil
}

func (s *Store) DeletePullStatus(p models.PullRequest) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.guardRemoval(tx, p); err != nil {
			return err
		}
		if err := tx.Where("pull_id IN (?)", pullIDs(tx, p.BaseRepo, p.Num)).Delete(&pendingPlan{}).Error; err != nil {
			return err
		}
		return tx.Where("pull_id IN (?)", pullIDs(tx, p.BaseRepo, p.Num)).Delete(&pullStatus{}).Error
	})
}

// UpdateProjectStatus matches BoltDB and Redis: it changes the first project,
// in status order, with this workspace and repoRelDir, whatever its name.
func (s *Store) UpdateProjectStatus(p models.PullRequest, workspace, directory string, status models.ProjectPlanStatus) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.guardRemoval(tx, p); err != nil {
			return err
		}
		projects := tx.Model(&project{}).Select("id").Where("repository_id IN (?) AND directory = ? AND workspace = ?", repoIDs(tx, p.BaseRepo), directory, workspace)
		var target projectStatus
		err := tx.Where("pull_id IN (?) AND project_id IN (?)", pullIDs(tx, p.BaseRepo, p.Num), projects).Order("position").Take(&target).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		if err != nil {
			return err
		}
		updates := map[string]any{"status": status}
		if status == models.DiscardedPlanStatus || status == models.AppliedPlanStatus {
			updates["plan_hash"] = ""
		}
		return tx.Model(&projectStatus{}).Where("pull_id = ? AND project_id = ?", target.PullID, target.ProjectID).Updates(updates).Error
	})
}

func (s *Store) UpdatePullWithResults(p models.PullRequest, results []command.ProjectResult) (models.PullStatus, error) {
	var next models.PullStatus
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.guard(tx, p); err != nil {
			return err
		}
		prior, err := readStatus(tx, p)
		if err != nil {
			return err
		}
		next = mergeStatus(prior, p, results)
		row, err := savePull(tx, p)
		if err != nil {
			return err
		}
		status := pullStatus{PullID: row.ID, Revision: p.HeadCommit, BaseBranch: p.BaseBranch}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "pull_id"}}, DoUpdates: clause.AssignmentColumns([]string{"revision", "base_branch"})}).Create(&status).Error; err != nil {
			return err
		}
		if err := tx.Where("pull_id = ?", row.ID).Delete(&projectStatus{}).Error; err != nil {
			return err
		}
		for position, result := range next.Projects {
			project, err := saveProject(tx, row.RepositoryID, result.RepoRelDir, result.ProjectName, result.Workspace)
			if err != nil {
				return err
			}
			entry := projectStatus{PullID: row.ID, ProjectID: project.ID, RepositoryID: row.RepositoryID, Position: position, Status: result.Status, Policies: result.PolicyStatus, PlanHash: result.PlanHash}
			if err := tx.Create(&entry).Error; err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return models.PullStatus{}, fmt.Errorf("saving pull status: %w", err)
	}
	return next, nil
}

func mergeStatus(prior *models.PullStatus, p models.PullRequest, results []command.ProjectResult) models.PullStatus {
	next := models.PullStatus{Pull: p}
	reset := prior == nil || prior.Pull.HeadCommit != p.HeadCommit || (p.BaseBranch != "" && prior.Pull.BaseBranch != p.BaseBranch)
	if !reset {
		next.Projects = append(next.Projects, prior.Projects...)
	}
	for _, result := range results {
		entry := models.ProjectStatus{RepoRelDir: result.RepoRelDir, ProjectName: result.ProjectName, Workspace: result.Workspace, Status: result.PlanStatus(), PolicyStatus: result.PolicyStatus(), PlanHash: result.PlanHashAfterMerge("")}
		if !reset {
			for _, old := range next.Projects {
				if old.RepoRelDir == entry.RepoRelDir && old.ProjectName == entry.ProjectName && old.Workspace == entry.Workspace {
					entry.PlanHash = result.PlanHashAfterMerge(old.PlanHash)
					break
				}
			}
		}
		if prior != nil {
			for _, old := range prior.Projects {
				if old.RepoRelDir != entry.RepoRelDir || old.ProjectName != entry.ProjectName || old.Workspace != entry.Workspace || len(old.PolicyStatus) == 0 {
					continue
				}
				policies := append([]models.PolicySetStatus(nil), old.PolicyStatus...)
				if !reset {
					for i := range policies {
						for _, policy := range entry.PolicyStatus {
							if policy.PolicySetName == policies[i].PolicySetName {
								policies[i] = policy
							}
						}
					}
				}
				entry.PolicyStatus = policies
				break
			}
		}
		index := -1
		for i, old := range next.Projects {
			if old.RepoRelDir == entry.RepoRelDir && old.ProjectName == entry.ProjectName && old.Workspace == entry.Workspace {
				index = i
				break
			}
		}
		if index >= 0 {
			next.Projects[index] = entry
		} else {
			next.Projects = append(next.Projects, entry)
		}
	}
	return next
}

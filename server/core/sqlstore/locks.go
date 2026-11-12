// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"errors"
	"fmt"
	"time"

	"github.com/runatlantis/atlantis/server/events/command"
	"github.com/runatlantis/atlantis/server/events/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type lock struct {
	ProjectID            string `gorm:"primaryKey;autoIncrement:false"`
	RepositoryID, PullID string
	Revision, Username   string
	Teams                []string `gorm:"serializer:json"`
	CreatedAt            time.Time
}

type commandLock struct {
	Command  command.Name `gorm:"primaryKey;autoIncrement:false"`
	UnixTime int64
}

func (s *Store) TryLock(want models.ProjectLock) (bool, models.ProjectLock, error) {
	if want.Project.RepoFullName != want.Pull.BaseRepo.FullName {
		return false, models.ProjectLock{}, fmt.Errorf("lock project and pull must belong to the same repository")
	}
	var acquired bool
	var actual models.ProjectLock
	err := s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.guard(tx, want.Pull); err != nil {
			return err
		}
		rows, err := loadLocks(tx, tx.Where("project_id IN (?)", projectIDs(tx, want.Pull.BaseRepo, want.Project, want.Workspace)))
		if err != nil {
			return err
		}
		if len(rows) != 0 {
			actual = rows[0]
			return nil
		}
		p, err := savePull(tx, want.Pull)
		if err != nil {
			return err
		}
		project, err := saveProject(tx, p.RepositoryID, want.Project.Path, want.Project.ProjectName, want.Workspace)
		if err != nil {
			return err
		}
		createdAt := databaseTime(want.Time)
		if want.Time.IsZero() {
			createdAt, err = databaseNow(tx)
			if err != nil {
				return err
			}
		}
		want.Time = createdAt.Local()
		row := lock{ProjectID: project.ID, RepositoryID: p.RepositoryID, PullID: p.ID, Revision: want.Pull.HeadCommit, Username: want.User.Username, Teams: want.User.Teams, CreatedAt: createdAt}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			rows, err := loadLocks(tx, tx.Where("project_id = ?", project.ID))
			if err != nil {
				return err
			}
			if len(rows) != 1 {
				return fmt.Errorf("competing project lock disappeared")
			}
			actual = rows[0]
			return nil
		}
		acquired, actual = true, want
		return nil
	})
	if err != nil {
		return false, models.ProjectLock{}, fmt.Errorf("acquiring project lock: %w", err)
	}
	return acquired, actual, nil
}

func loadLocks(tx, query *gorm.DB) ([]models.ProjectLock, error) {
	var rows []lock
	if err := query.Order("created_at, project_id").Find(&rows).Error; err != nil {
		return nil, err
	}
	var result []models.ProjectLock
	for _, row := range rows {
		p, err := loadPull(tx, row.PullID)
		if err != nil {
			return nil, err
		}
		p.HeadCommit = row.Revision
		var project project
		if err := tx.Where("id = ?", row.ProjectID).Take(&project).Error; err != nil {
			return nil, err
		}
		result = append(result, models.ProjectLock{Pull: p, Project: models.NewProject(p.BaseRepo.FullName, project.Directory, project.Name), Workspace: project.Workspace,
			User: models.User{Username: row.Username, Teams: row.Teams}, Time: row.CreatedAt.Local()})
	}
	return result, nil
}

// locks reads and optionally removes the same rows in one transaction. Removal
// locks each pull through guardRemoval, then deletes exactly the rows it returns
// and discards their project status.
func (s *Store) locks(where func(*gorm.DB) *gorm.DB, remove bool, owner string) ([]models.ProjectLock, error) {
	var result []models.ProjectLock
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var err error
		result, err = loadLocks(tx, where(tx))
		if err != nil {
			return err
		}
		if remove {
			for _, lock := range result {
				p := lock.Pull
				p.Execution = &models.Execution{Owner: owner}
				if err := s.guardRemoval(tx, p); err != nil {
					return err
				}
			}
			for _, lock := range result {
				if err := tx.Model(&projectStatus{}).Where("pull_id IN (?) AND project_id IN (?)", pullIDs(tx, lock.Pull.BaseRepo, lock.Pull.Num), projectIDs(tx, lock.Pull.BaseRepo, lock.Project, lock.Workspace)).Update("status", models.DiscardedPlanStatus).Error; err != nil {
					return err
				}
			}
			for _, held := range result {
				deleted := tx.Where("project_id IN (?) AND pull_id IN (?)", projectIDs(tx, held.Pull.BaseRepo, held.Project, held.Workspace), pullIDs(tx, held.Pull.BaseRepo, held.Pull.Num)).Delete(&lock{})
				if deleted.Error != nil {
					return deleted.Error
				}
				if deleted.RowsAffected != 1 {
					return fmt.Errorf("project lock changed during unlock")
				}
			}
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("accessing project locks: %w", err)
	}
	return result, nil
}

func firstLock(locks []models.ProjectLock, err error) (*models.ProjectLock, error) {
	if err != nil || len(locks) == 0 {
		return nil, err
	}
	return &locks[0], nil
}

func (s *Store) List() ([]models.ProjectLock, error) {
	return s.locks(func(tx *gorm.DB) *gorm.DB { return tx.Model(&lock{}) }, false, "")
}

func (s *Store) GetLockInRepo(repo models.Repo, p models.Project, workspace string) (*models.ProjectLock, error) {
	return firstLock(s.locks(func(tx *gorm.DB) *gorm.DB { return tx.Where("project_id IN (?)", projectIDs(tx, repo, p, workspace)) }, false, ""))
}

func (s *Store) UnlockInRepo(repo models.Repo, p models.Project, workspace string) (*models.ProjectLock, error) {
	return firstLock(s.locks(func(tx *gorm.DB) *gorm.DB { return tx.Where("project_id IN (?)", projectIDs(tx, repo, p, workspace)) }, true, repo.ExecutionOwner()))
}

func (s *Store) UnlockInRepoIfOwnedByPull(repo models.Repo, p models.Project, workspace string, number int) (*models.ProjectLock, error) {
	return firstLock(s.locks(func(tx *gorm.DB) *gorm.DB {
		return tx.Where("project_id IN (?) AND pull_id IN (?)", projectIDs(tx, repo, p, workspace), pullIDs(tx, repo, number))
	}, true, repo.ExecutionOwner()))
}

func (s *Store) UnlockByPullInRepo(repo models.Repo, number int) ([]models.ProjectLock, error) {
	return s.locks(func(tx *gorm.DB) *gorm.DB { return tx.Where("pull_id IN (?)", pullIDs(tx, repo, number)) }, true, repo.ExecutionOwner())
}

// errRepositoryIdentityRequired rejects lock lookups by repository full name.
// Durable locks are scoped by VCS host as well, so callers go through
// db.RepoScopedLocks, which locking.Client selects automatically.
var errRepositoryIdentityRequired = errors.New("durable locks require repository identity; use the repository-scoped lock methods")

// GetLock satisfies db.Database. Durable callers use GetLockInRepo.
func (s *Store) GetLock(models.Project, string) (*models.ProjectLock, error) {
	return nil, errRepositoryIdentityRequired
}

// Unlock satisfies db.Database. Durable callers use UnlockInRepo.
func (s *Store) Unlock(models.Project, string) (*models.ProjectLock, error) {
	return nil, errRepositoryIdentityRequired
}

// UnlockIfOwnedByPull satisfies db.Database. Durable callers use UnlockInRepoIfOwnedByPull.
func (s *Store) UnlockIfOwnedByPull(models.Project, string, int) (*models.ProjectLock, error) {
	return nil, errRepositoryIdentityRequired
}

// UnlockByPull satisfies db.Database. Durable callers use UnlockByPullInRepo.
func (s *Store) UnlockByPull(string, int) ([]models.ProjectLock, error) {
	return nil, errRepositoryIdentityRequired
}

func (s *Store) LockCommand(name command.Name, at time.Time) (*command.Lock, error) {
	row := commandLock{Command: name, UnixTime: at.Unix()}
	result := s.db.Clauses(clause.OnConflict{DoNothing: true}).Create(&row)
	if result.Error != nil {
		return nil, fmt.Errorf("locking command: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return nil, fmt.Errorf("lock already exists")
	}
	return &command.Lock{CommandName: name, LockMetadata: command.LockMetadata{UnixTime: row.UnixTime}}, nil
}

func (s *Store) UnlockCommand(name command.Name) error {
	result := s.db.Where("command = ?", name).Delete(&commandLock{})
	if result.Error != nil {
		return fmt.Errorf("unlocking command: %w", result.Error)
	}
	if result.RowsAffected == 0 {
		return fmt.Errorf("no lock exists")
	}
	return nil
}

func (s *Store) CheckCommandLock(name command.Name) (*command.Lock, error) {
	var row commandLock
	err := s.db.Where("command = ?", name).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("checking command lock: %w", err)
	}
	return &command.Lock{CommandName: name, LockMetadata: command.LockMetadata{UnixTime: row.UnixTime}}, nil
}

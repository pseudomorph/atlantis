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

type executionClaim struct {
	PullID       string `gorm:"primaryKey;autoIncrement:false"`
	ScopeID      string
	Owner, State string
	AcquiredAt   time.Time
}

// AcquireClaim rejects existing ownership, including unfinished work from a prior server.
func (s *Store) AcquireClaim(p models.PullRequest, owner string) error {
	if owner == "" {
		return fmt.Errorf("invalid execution claim")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		row, err := savePull(tx, p)
		if err != nil {
			return err
		}
		if err := saveExecutionScope(tx, p, row); err != nil {
			return err
		}
		var closed bool
		if err := tx.Model(&pull{}).Select("closed").Where("id = ?", row.ID).Scan(&closed).Error; err != nil {
			return err
		}
		if closed {
			return fmt.Errorf("pull request is closed")
		}
		now, err := databaseNow(tx)
		if err != nil {
			return err
		}
		claim := executionClaim{ScopeID: p.ExecutionScopeID(), PullID: row.ID, Owner: owner, State: "active", AcquiredAt: now}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&claim)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("execution is already owned or uncertain; reconcile stopped work before retrying")
		}
		return tx.Model(&pull{}).Where("id = ?", row.ID).Update("cancelled", false).Error
	})
}
func (s *Store) guard(tx *gorm.DB, p models.PullRequest) error {
	var claim executionClaim
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("pull_id IN (?)", pullIDs(tx, p.BaseRepo, p.Num)).Take(&claim).Error
	return checkExecutionClaim(claim, p.ExecutionOwner(), p.ExecutionScope(), err)
}

func checkExecutionClaim(claim executionClaim, owner, scope string, err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("%w: claim is missing", db.ErrExecutionClaimLost)
	}
	if err != nil {
		return fmt.Errorf("reading execution claim: %w", err)
	}
	if owner == "" || (scope != "" && claim.ScopeID != scope) || claim.Owner != owner || claim.State != "active" {
		return fmt.Errorf("%w: claim is inactive or belongs to another execution", db.ErrExecutionClaimLost)
	}
	return nil
}

func (s *Store) CheckClaim(p models.PullRequest) error {
	return s.db.Transaction(func(tx *gorm.DB) error { return s.guard(tx, p) })
}
func (s *Store) ReleaseClaim(p models.PullRequest) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.guard(tx, p); err != nil {
			return err
		}
		var running int64
		if err := tx.Model(&job{}).Where("owner = ? AND state = ?", p.ExecutionOwner(), "running").Count(&running).Error; err != nil {
			return err
		}
		if running != 0 {
			return fmt.Errorf("execution still has unfinished jobs")
		}
		// Non-PR inputs belong to one invocation, so later API scopes cannot consume them.
		if p.Num < 0 {
			jobs := tx.Model(&job{}).Select("id").Where("scope = ?", p.ExecutionScopeID())
			if err := tx.Where("job_id IN (?)", jobs).Delete(&pendingPlan{}).Error; err != nil {
				return err
			}
		}
		return tx.Where("owner = ? AND pull_id IN (?)", p.ExecutionOwner(), pullIDs(tx, p.BaseRepo, p.Num)).Delete(&executionClaim{}).Error
	})
}
func (s *Store) CancelPull(p models.PullRequest) error {
	row, err := s.ensurePull(p)
	if err != nil {
		return err
	}
	return s.db.Model(&pull{}).Where("id = ?", row.ID).Update("cancelled", true).Error
}
func (s *Store) ensurePull(p models.PullRequest) (pull, error) {
	var out pull
	err := s.db.Transaction(func(tx *gorm.DB) error { var err error; out, err = savePull(tx, p); return err })
	return out, err
}
func (s *Store) IsCancelled(p models.PullRequest) (bool, error) {
	var row struct{ Cancelled, Closed bool }
	err := s.db.Model(&pull{}).Select("cancelled", "closed").Where("id IN (?)", pullIDs(s.db, p.BaseRepo, p.Num)).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return row.Cancelled || row.Closed, err
}

// ClosePull requests cancellation without waiting for the execution owner.
func (s *Store) ClosePull(p models.PullRequest) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		row, err := savePull(tx, p)
		if err != nil {
			return err
		}
		now, err := databaseNow(tx)
		if err != nil {
			return err
		}
		if err = tx.Model(&pull{}).Where("id = ?", row.ID).Updates(map[string]any{"closed": true, "cancelled": true, "closed_at": gorm.Expr("COALESCE(closed_at, ?)", now)}).Error; err != nil {
			return err
		}
		return tx.Where("pull_id = ?", row.ID).Delete(&pendingPlan{}).Error
	})
}
func (s *Store) PullIdle(p models.PullRequest) (bool, error) {
	var n int64
	err := s.db.Model(&executionClaim{}).Where("pull_id IN (?)", pullIDs(s.db, p.BaseRepo, p.Num)).Count(&n).Error
	return n == 0, err
}

func (s *Store) guardRemoval(tx *gorm.DB, p models.PullRequest) error {
	if err := s.guard(tx, p); err == nil {
		return nil
	}
	var row struct{ Closed bool }
	if err := tx.Table("pulls").Where("id IN (?)", pullIDs(tx, p.BaseRepo, p.Num)).Take(&row).Error; err != nil {
		return err
	}
	var claims int64
	if err := tx.Model(&executionClaim{}).Where("pull_id IN (?)", pullIDs(tx, p.BaseRepo, p.Num)).Count(&claims).Error; err != nil {
		return err
	}
	if row.Closed && claims == 0 {
		return nil
	}
	return fmt.Errorf("cleanup requires the execution owner or a closed idle scope")
}

func (s *Store) MarkUncertain(p models.PullRequest) error {
	return s.db.Model(&executionClaim{}).Where("owner = ? AND scope_id = ?", p.ExecutionOwner(), p.ExecutionScopeID()).Update("state", "uncertain").Error
}

type executionScope struct {
	ID                           string `gorm:"primaryKey"`
	RepositoryID, MetadataPullID string
	PullID                       *string
	Kind, Ref                    string
}

func saveExecutionScope(tx *gorm.DB, p models.PullRequest, row pull) error {
	scope := executionScope{ID: p.ExecutionScopeID(), RepositoryID: row.RepositoryID, MetadataPullID: row.ID, Kind: "pr"}
	if p.Num < 0 {
		scope.Kind = "api"
		scope.Ref = p.HeadBranch
	} else {
		scope.PullID = &row.ID
	}
	return tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&scope).Error
}

func (s *Store) ClosedPullsReady() ([]models.PullRequest, error) {
	var rows []pull
	if err := s.db.Where("closed = ? AND cleanup_complete = ? AND id NOT IN (?)", true, false, s.db.Model(&executionClaim{}).Select("pull_id")).Find(&rows).Error; err != nil {
		return nil, err
	}
	var out []models.PullRequest
	for _, row := range rows {
		p, err := loadPull(s.db, row.ID)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}
func (s *Store) MarkClosedClean(p models.PullRequest) error {
	return s.db.Model(&pull{}).Where("id IN (?) AND closed = ? AND id NOT IN (?)", pullIDs(s.db, p.BaseRepo, p.Num), true, s.db.Model(&executionClaim{}).Select("pull_id")).Update("cleanup_complete", true).Error
}

// MarkPreviousExecutionsUncertain blocks work left by the prior server.
// The SQLite process lock proves the server exited, not that Terraform children stopped.
func (s *Store) MarkPreviousExecutionsUncertain() error {
	return s.db.Model(&executionClaim{}).Where("state = ?", "active").Update("state", "uncertain").Error
}

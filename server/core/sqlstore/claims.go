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
		var verification int64
		if err := tx.Table("pending_pull_lifecycles").Where("pull_id = ?", row.ID).Count(&verification).Error; err != nil {
			return err
		}
		if verification != 0 {
			return fmt.Errorf("pull request closure is awaiting VCS verification; retry shortly")
		}
		if err := reopenIfRequested(tx, row.ID); err != nil {
			return err
		}
		now, err := databaseNow(tx)
		if err != nil {
			return err
		}
		claim := executionClaim{PullID: row.ID, Owner: owner, State: "active", AcquiredAt: now}
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
	return checkExecutionClaim(claim, p.ExecutionOwner(), err)
}

func checkExecutionClaim(claim executionClaim, owner string, err error) error {
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return fmt.Errorf("%w: claim is missing", db.ErrExecutionClaimLost)
	}
	if err != nil {
		return fmt.Errorf("reading execution claim: %w", err)
	}
	if owner == "" || claim.Owner != owner || claim.State != "active" {
		return fmt.Errorf("%w: claim is inactive or belongs to another execution", db.ErrExecutionClaimLost)
	}
	return nil
}

// Jobs retain the pull and owner that admitted them, fencing writes after recovery.
func (s *Store) guardJob(tx *gorm.DB, row job) error {
	var claim executionClaim
	err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("pull_id = ?", row.PullID).Take(&claim).Error
	return checkExecutionClaim(claim, row.Owner, err)
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
		if err := tx.Model(&job{}).Where("pull_id IN (?) AND owner = ? AND state = ?", pullIDs(tx, p.BaseRepo, p.Num), p.ExecutionOwner(), "running").Count(&running).Error; err != nil {
			return err
		}
		if running != 0 {
			return fmt.Errorf("execution still has unfinished jobs")
		}
		// Non-PR inputs belong to one invocation and are invalidated on release.
		if p.Num < 0 {
			if err := tx.Where("pull_id IN (?)", pullIDs(tx, p.BaseRepo, p.Num)).Delete(&pendingPlan{}).Error; err != nil {
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
	var row struct{ Cancelled, Closed, Pending bool }
	err := s.db.Model(&pull{}).Select("cancelled, closed, EXISTS(SELECT 1 FROM pending_pull_lifecycles WHERE pull_id = pulls.id) AS pending").Where("id IN (?)", pullIDs(s.db, p.BaseRepo, p.Num)).Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return false, nil
	}
	return row.Cancelled || row.Closed || row.Pending, err
}

// ClosePull records a verified closure. Deferred cleanup uses its generation
// instead of replaying the close event, so a second closure cancels any reopen request.
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
		if err = tx.Model(&pull{}).Where("id = ?", row.ID).Updates(map[string]any{"closed": true, "cancelled": true, "reopen_requested": false, "closed_at": gorm.Expr("COALESCE(closed_at, ?)", now), "closure_generation": gorm.Expr("CASE WHEN closed THEN closure_generation ELSE closure_generation + 1 END"), "cleanup_complete": gorm.Expr("CASE WHEN closed THEN cleanup_complete ELSE ? END", false)}).Error; err != nil {
			return err
		}
		if err := anchorClosedLogs(tx, row.ID); err != nil {
			return err
		}
		return tx.Where("pull_id = ?", row.ID).Delete(&pendingPlan{}).Error
	})
}

// RequestReopen records a reopened pull request. The next claim reopens it once
// closure cleanup has finished, so cleanup never races new work in its directory.
func (s *Store) RequestReopen(p models.PullRequest) error {
	row, err := s.ensurePull(p)
	if err != nil {
		return err
	}
	return s.db.Model(&pull{}).Where("id = ? AND closed = ?", row.ID, true).Update("reopen_requested", true).Error
}

func reopenIfRequested(tx *gorm.DB, pullID string) error {
	var row struct{ Closed, ReopenRequested, CleanupComplete bool }
	if err := tx.Model(&pull{}).Select("closed", "reopen_requested", "cleanup_complete").Where("id = ?", pullID).Take(&row).Error; err != nil {
		return err
	}
	switch {
	case !row.Closed:
		return nil
	case !row.ReopenRequested:
		return fmt.Errorf("pull request is closed")
	case !row.CleanupComplete:
		return fmt.Errorf("pull request was reopened, but cleanup from its closure has not finished; retry shortly")
	}
	return tx.Model(&pull{}).Where("id = ?", pullID).Updates(map[string]any{
		"closed": false, "cancelled": false, "closed_at": nil, "cleanup_complete": false, "reopen_requested": false,
	}).Error
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
	return fmt.Errorf("cleanup requires the execution owner or a closed idle pull request")
}

func (s *Store) MarkUncertain(p models.PullRequest) error {
	if p.ExecutionOwner() == "" {
		return fmt.Errorf("%w: missing execution owner", db.ErrExecutionClaimLost)
	}
	result := s.db.Model(&executionClaim{}).Where("owner = ?", p.ExecutionOwner()).Update("state", "uncertain")
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("%w: could not mark execution uncertain", db.ErrExecutionClaimLost)
	}
	return nil
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
	if p.ExecutionOwner() != "" && p.ClosureGeneration != 0 {
		return s.db.Transaction(func(tx *gorm.DB) error {
			if err := s.guard(tx, p); err != nil {
				return err
			}
			var closed pull
			if err := tx.Where("id IN (?)", pullIDs(tx, p.BaseRepo, p.Num)).Take(&closed).Error; err != nil {
				return err
			}
			if err := anchorClosedLogs(tx, closed.ID); err != nil {
				return err
			}
			result := tx.Model(&pull{}).Where("id IN (?) AND closed = ? AND closure_generation = ?", pullIDs(tx, p.BaseRepo, p.Num), true, p.ClosureGeneration).Update("cleanup_complete", true)
			if result.Error != nil {
				return result.Error
			}
			if result.RowsAffected != 1 {
				return fmt.Errorf("closure generation changed during cleanup")
			}
			return nil
		})
	}
	return s.db.Model(&pull{}).Where("id IN (?) AND closed = ? AND id NOT IN (?)", pullIDs(s.db, p.BaseRepo, p.Num), true, s.db.Model(&executionClaim{}).Select("pull_id")).Update("cleanup_complete", true).Error
}

// MarkPreviousExecutionsUncertain blocks work left by the prior server.
// The SQLite process lock proves the server exited, not that Terraform children stopped.
func (s *Store) MarkPreviousExecutionsUncertain() error {
	return s.db.Model(&executionClaim{}).Where("state = ?", "active").Update("state", "uncertain").Error
}

// ClaimCleanup serializes destructive cleanup with other cleaners and commands.
func (s *Store) ClaimCleanup(p models.PullRequest, owner string) (models.PullRequest, bool, error) {
	claimed := false
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var row struct {
			ID                      string
			Closed, CleanupComplete bool
			ClosureGeneration       int64
		}
		if err := tx.Table("pulls").Where("id IN (?)", pullIDs(tx, p.BaseRepo, p.Num)).Take(&row).Error; err != nil {
			return err
		}
		if !row.Closed || row.CleanupComplete || (p.ClosureGeneration != 0 && p.ClosureGeneration != row.ClosureGeneration) {
			return nil
		}
		now, err := databaseNow(tx)
		if err != nil {
			return err
		}
		claim := executionClaim{PullID: row.ID, Owner: owner, State: "active", AcquiredAt: now}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&claim)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected != 1 {
			return fmt.Errorf("cleanup deferred until execution is stopped or reconciled")
		}
		p.ClosureGeneration = row.ClosureGeneration
		p.Execution = &models.Execution{Owner: owner}
		p.BaseRepo.Execution = p.Execution
		claimed = true
		return nil
	})
	return p, claimed, err
}

// CheckExecutionStopped requires a recorded terminal outcome for every owned job.
func (s *Store) CheckExecutionStopped(p models.PullRequest) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		if err := s.guard(tx, p); err != nil {
			return err
		}
		var running int64
		if err := tx.Table("jobs").Where("pull_id IN (?) AND owner = ? AND state = ?", pullIDs(tx, p.BaseRepo, p.Num), p.ExecutionOwner(), "running").Count(&running).Error; err != nil {
			return err
		}
		if running != 0 {
			return fmt.Errorf("execution has jobs without a persisted completion; reconcile stopped work before unlocking")
		}
		return nil
	})
}

// Each log's retention clock belongs to the closure after its job, not to a later
// reopen/close cycle of the same PR. Final cleanup also anchors late log chunks.
func anchorClosedLogs(tx *gorm.DB, pullID string) error {
	return tx.Exec(`UPDATE artifacts SET unreferenced_at = (SELECT closed_at FROM pulls WHERE id = ?)
      WHERE kind = 'log' AND unreferenced_at IS NULL AND job_id IN (SELECT id FROM jobs WHERE pull_id = ?)`, pullID, pullID).Error
}

// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/runatlantis/atlantis/server/events/models"
	"gorm.io/gorm"
)

// AcquireUnlockClaim reconciles explicitly attested uncertain PR work. Active
// commands must finish; a database claim cannot terminate their child processes.
func (s *Store) AcquireUnlockClaim(p models.PullRequest, owner, actor string, confirmedStopped bool) error {
	if p.Num <= 0 || owner == "" {
		return fmt.Errorf("PR unlock recovery requires a real pull request and execution owner")
	}
	return s.db.Transaction(func(tx *gorm.DB) error {
		row, err := savePull(tx, p)
		if err != nil {
			return err
		}
		return s.acquireRecovery(tx, p, row.ID, owner, actor, confirmedStopped, nil)
	})
}

// AcquireAPIJobUnlock resolves an existing API invocation without allocating a new
// synthetic number. One job identifies the whole multi-project invocation.
func (s *Store) AcquireAPIJobUnlock(id, owner, actor string, confirmedStopped bool) (models.PullRequest, error) {
	var p models.PullRequest
	if owner == "" || actor == "" || !confirmedStopped {
		return p, fmt.Errorf("API recovery requires an authenticated stopped-work attestation")
	}
	err := s.db.Transaction(func(tx *gorm.DB) error {
		var source job
		if err := tx.Where("id = ?", id).Take(&source).Error; err != nil {
			return err
		}
		var err error
		p, err = loadPull(tx, source.PullID)
		if err != nil {
			return err
		}
		if p.Num >= 0 {
			return fmt.Errorf("use the PR unlock workflow for PR-associated jobs")
		}
		p.Execution = &models.Execution{Owner: owner}
		p.BaseRepo.Execution = p.Execution
		return s.acquireRecovery(tx, p, source.PullID, owner, actor, confirmedStopped, &source)
	})
	return p, err
}

func (s *Store) acquireRecovery(tx *gorm.DB, p models.PullRequest, pullID, owner, actor string, confirmed bool, source *job) error {
	// The pull lock also fences the missing-claim case, where no claim row exists to lock.
	if _, err := lockPull(tx, pullID); err != nil {
		return err
	}
	var existing executionClaim
	err := tx.Clauses(rowLock).Where("pull_id = ?", pullID).Take(&existing).Error
	missing := errors.Is(err, gorm.ErrRecordNotFound)
	if err != nil && !missing {
		return err
	}
	now, err := databaseNow(tx)
	if err != nil {
		return err
	}
	// A claim whose heartbeat has expired is uncertain even before the sweep
	// reaches it. Expiry never transfers the claim; it still needs attestation.
	if !missing && claimExpired(existing, now, s.claimExpiry) {
		if err := expireClaim(tx, existing, now); err != nil {
			return err
		}
		existing.State = "uncertain"
	}
	if !missing {
		if existing.State != "uncertain" {
			return fmt.Errorf("execution is active; wait for it to stop before unlocking")
		}
		if !confirmed || actor == "" {
			return fmt.Errorf("execution is uncertain; independently verify that Terraform and workflow processes have stopped, then run atlantis unlock --confirm-stopped")
		}
		if source != nil && existing.Owner != source.Owner {
			var prior int64
			if err := tx.Table("execution_recoveries").Where("pull_id = ? AND recovery_owner = ? AND source_job_id IN (?)", source.PullID, existing.Owner, tx.Model(&job{}).Select("id").Where("pull_id = ? AND owner = ?", source.PullID, source.Owner)).Count(&prior).Error; err != nil {
				return err
			}
			if prior == 0 {
				return fmt.Errorf("job belongs to an older execution owner; refusing recovery")
			}
		}
	}
	if confirmed && (source != nil || !missing) {
		var sourceID *string
		if source != nil {
			sourceID = &source.ID
		}
		previous, previousInstance := existing.Owner, existing.ServerInstanceID
		if missing && source != nil {
			previous, previousInstance = source.Owner, source.ServerInstanceID
		}
		if err := tx.Exec("INSERT INTO execution_recoveries(id, pull_id, previous_owner, recovery_owner, source_job_id, actor, recovered_at, previous_server_instance_id, recovery_server_instance_id) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
			uuid.NewString(), pullID, previous, owner, sourceID, actor, now, previousInstance, s.instance).Error; err != nil {
			return err
		}
		if err := tx.Table("jobs").Where("pull_id = ? AND state = ?", pullID, "running").Updates(map[string]any{"state": "interrupted", "error": "operator reconciled stopped execution", "finished_at": now}).Error; err != nil {
			return err
		}
		if err := tx.Model(&logStream{}).Where("job_id IN (?) AND sealed_at IS NULL", tx.Model(&job{}).Select("id").Where("pull_id = ?", pullID)).Updates(map[string]any{"sealed_at": now, "interrupted": true}).Error; err != nil {
			return err
		}
		if p.Num < 0 {
			if err := tx.Model(&logStream{}).Where("job_id IN (?) AND retention_started_at IS NULL", tx.Model(&job{}).Select("id").Where("pull_id = ?", pullID)).Update("retention_started_at", now).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("pull_id = ?", pullID).Delete(&pendingPlan{}).Error; err != nil {
			return err
		}
	} else {
		var running int64
		if err := tx.Table("jobs").Where("pull_id = ? AND state = ?", pullID, "running").Count(&running).Error; err != nil {
			return err
		}
		if running != 0 {
			return fmt.Errorf("unfinished jobs require stopped-work reconciliation")
		}
	}
	if missing {
		claim := s.newClaim(pullID, owner, now)
		return tx.Create(&claim).Error
	}
	result := tx.Model(&executionClaim{}).Where("pull_id = ? AND owner = ? AND state = ?", pullID, existing.Owner, "uncertain").
		Updates(map[string]any{"owner": owner, "state": "active", "acquired_at": now, "server_instance_id": s.instance, "heartbeat_at": now})
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("execution ownership changed during recovery")
	}
	return nil
}

// RecoveryJob gives authenticated lock listings a stable recovery address even
// when the original request disconnected before receiving its job IDs.
func (s *Store) RecoveryJob(repo models.Repo, number int) (string, error) {
	var row job
	err := s.db.Where("pull_id IN (?) AND kind = ?", pullIDs(s.db, repo, number), models.CommandJob).Order("started_at DESC, id DESC").Take(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return "", nil
	}
	return row.ID, err
}

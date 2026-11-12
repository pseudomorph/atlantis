// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"errors"
	"fmt"
	"time"

	"gorm.io/gorm"
)

const (
	// ClaimHeartbeatInterval is how often a server refreshes the claims it holds.
	ClaimHeartbeatInterval = 15 * time.Second
	// ClaimExpiry is how long an active claim may go without a heartbeat before
	// the sweep marks it uncertain. It spans several missed heartbeats so that a
	// slow transaction or a busy database does not expire a live claim.
	ClaimExpiry = 2 * time.Minute
)

// InstanceID identifies this server process in the claims, jobs and recovery
// audits it writes. Each process that opens the store gets a new ID.
func (s *Store) InstanceID() string { return s.instance }

// claimExpired reports whether an active claim's heartbeat is older than the expiry.
func claimExpired(claim executionClaim, now time.Time, expiry time.Duration) bool {
	return claim.State == "active" && claim.HeartbeatAt.Before(now.Add(-expiry))
}

// RefreshClaimHeartbeats records a heartbeat for every active claim this server
// instance holds. It is one conditional update; a claim that the sweep or a
// recovery has already changed no longer matches and is not revived.
func (s *Store) RefreshClaimHeartbeats() (int64, error) {
	if err := s.checkServerLock(); err != nil {
		return 0, err
	}
	now, err := databaseNow(s.db)
	if err != nil {
		return 0, err
	}
	result := s.db.Model(&executionClaim{}).Where("server_instance_id = ? AND state = ?", s.instance, "active").Update("heartbeat_at", now)
	return result.RowsAffected, result.Error
}

// ExpireClaims marks active claims whose heartbeat is older than the expiry
// uncertain and seals their open logs as interrupted. It never transfers a
// claim: an expired claim requires the audited stopped-work recovery, because a
// missing heartbeat does not prove that Terraform children stopped.
func (s *Store) ExpireClaims() (int, error) {
	now, err := databaseNow(s.db)
	if err != nil {
		return 0, err
	}
	var candidates []executionClaim
	if err := s.db.Where("state = ?", "active").Order("pull_id").Find(&candidates).Error; err != nil {
		return 0, err
	}
	expired := 0
	for _, candidate := range candidates {
		if !claimExpired(candidate, now, s.claimExpiry) {
			continue
		}
		swept := false
		err := s.db.Transaction(func(tx *gorm.DB) error {
			claim, err := lockClaim(tx, candidate.PullID)
			if errors.Is(err, gorm.ErrRecordNotFound) {
				return nil
			}
			if err != nil {
				return err
			}
			now, err := databaseNow(tx)
			if err != nil {
				return err
			}
			// A heartbeat or release may have landed before the lock was taken.
			if claim.Owner != candidate.Owner || !claimExpired(claim, now, s.claimExpiry) {
				return nil
			}
			if err := expireClaim(tx, claim, now); err != nil {
				return err
			}
			swept = true
			return nil
		})
		if err != nil {
			return expired, fmt.Errorf("expiring execution claim: %w", err)
		}
		if swept {
			expired++
		}
	}
	return expired, nil
}

// expireClaim runs with the claim locked and marks it uncertain, sealing the
// owner's open logs as interrupted.
func expireClaim(tx *gorm.DB, claim executionClaim, now time.Time) error {
	result := tx.Model(&executionClaim{}).Where("pull_id = ? AND owner = ? AND state = ?", claim.PullID, claim.Owner, "active").Update("state", "uncertain")
	if result.Error != nil {
		return result.Error
	}
	if result.RowsAffected != 1 {
		return fmt.Errorf("execution claim changed while expiring")
	}
	return tx.Model(&logStream{}).Where("sealed_at IS NULL AND job_id IN (?)", tx.Model(&job{}).Select("id").Where("pull_id = ? AND owner = ?", claim.PullID, claim.Owner)).
		Updates(map[string]any{"sealed_at": now, "interrupted": true}).Error
}

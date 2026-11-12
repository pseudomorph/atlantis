// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"fmt"

	"github.com/runatlantis/atlantis/server/events/models"
	"gorm.io/gorm"
)

// QueueLifecycleCheck persists an unverified closure instead of losing it when
// the VCS API is unavailable. Admission stays blocked until verification succeeds.
func (s *Store) QueueLifecycleCheck(p models.PullRequest) error {
	return s.db.Transaction(func(tx *gorm.DB) error {
		row, err := savePull(tx, p)
		if err != nil {
			return err
		}
		return tx.Exec(`INSERT INTO pending_pull_lifecycles(pull_id, generation) VALUES (?, 1)
          ON CONFLICT(pull_id) DO UPDATE SET generation = pending_pull_lifecycles.generation + 1`, row.ID).Error
	})
}

func (s *Store) PendingLifecycleChecks() ([]models.PullRequest, error) {
	var rows []struct {
		PullID     string
		Generation int64
	}
	if err := s.db.Table("pending_pull_lifecycles").Find(&rows).Error; err != nil {
		return nil, err
	}
	var out []models.PullRequest
	for _, row := range rows {
		p, err := loadPull(s.db, row.PullID)
		if err != nil {
			return nil, err
		}
		p.LifecycleGeneration = row.Generation
		out = append(out, p)
	}
	return out, nil
}

func (s *Store) ResolveLifecycleCheck(p models.PullRequest) error {
	if p.LifecycleGeneration == 0 {
		return fmt.Errorf("lifecycle verification generation is missing")
	}
	return s.db.Exec("DELETE FROM pending_pull_lifecycles WHERE pull_id IN (?) AND generation = ?", pullIDs(s.db, p.BaseRepo, p.Num), p.LifecycleGeneration).Error
}

// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"errors"

	"github.com/runatlantis/atlantis/server/events/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Locking rule: a transaction that reads pull-scoped state and writes based on it
// first locks the governing pulls row with lockPull. The pull row exists before
// any lock or status row for it, so the lock also fences inserts that a
// missing-row check cannot. Writes that do not run under that lock are single
// conditional statements whose RowsAffected is checked. SQLite serializes writers
// with _txlock=immediate and the glebarez dialector omits FOR UPDATE; a backend
// with row locking under READ COMMITTED relies on this rule instead.
// Transactions locking several pulls take them in a deterministic order.
var rowLock = clause.Locking{Strength: clause.LockingStrengthUpdate}

// lockPull locks and returns the pull row selected by an ID or an ID subquery.
func lockPull(tx *gorm.DB, ids any) (pull, error) {
	var row pull
	err := tx.Clauses(rowLock).Where("id IN (?)", ids).Take(&row).Error
	return row, err
}

// guard locks the governing pull row, when one exists, before a pull-scoped
// read-then-write. A pull without a row has no lock or status rows to protect.
func (s *Store) guard(tx *gorm.DB, p models.PullRequest) error {
	_, err := lockPull(tx, pullIDs(tx, p.BaseRepo, p.Num))
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	return err
}

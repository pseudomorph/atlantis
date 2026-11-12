// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	. "github.com/runatlantis/atlantis/testing"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRowLockOmittedBySQLite(t *testing.T) {
	statement := lockStatement(t, false)
	Assert(t, !strings.Contains(statement, "FOR UPDATE"), "sqlite received a row lock clause: %s", statement)
}

// A dialector without the SQLite FOR override must receive the lock clause.
func TestRowLockRequestedWhereSupported(t *testing.T) {
	statement := lockStatement(t, true)
	Assert(t, strings.Contains(statement, "FOR UPDATE"), "row lock clause missing: %s", statement)
}

func lockStatement(t *testing.T, rowLocking bool) string {
	orm, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{DryRun: true, Logger: logger.Default.LogMode(logger.Silent)})
	Ok(t, err)
	if rowLocking {
		delete(orm.ClauseBuilders, "FOR")
	}
	var statement string
	Ok(t, orm.Callback().Query().After("gorm:query").Register("capture", func(tx *gorm.DB) { statement = tx.Statement.SQL.String() }))
	_, err = lockPull(orm, "pull")
	Ok(t, err)
	Assert(t, statement != "", "no query captured")
	return statement
}

func TestOwnerlessCleanupWaitsForCleanupClaim(t *testing.T) {
	s := conformanceStore(t)
	p := claimPull()
	Ok(t, s.ClosePull(p))
	_, acquired, err := s.ClaimCleanup(p, "cleaner")
	Ok(t, err)
	Assert(t, acquired, "cleanup claim was not acquired")
	Ok(t, s.MarkClosedClean(p))
	ready, err := s.ClosedPullsReady()
	Ok(t, err)
	Equals(t, 0, len(ready))
	var complete bool
	Ok(t, s.db.Table("pulls").Select("cleanup_complete").Take(&complete).Error)
	Assert(t, !complete, "ownerless cleanup completed while a cleaner held the claim")
}

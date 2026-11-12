// Copyright 2026 The Atlantis Authors
// SPDX-License-Identifier: Apache-2.0

package sqlstore

import (
	"strings"
	"testing"

	"github.com/glebarez/sqlite"
	. "github.com/runatlantis/atlantis/testing"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRowLockOmittedBySQLite(t *testing.T) {
	update, share := lockStatements(t, sqlite.Open(":memory:"))
	Assert(t, !strings.Contains(update, " FOR "), "sqlite received a row lock clause: %s", update)
	Assert(t, !strings.Contains(share, " FOR "), "sqlite received a row lock clause: %s", share)
}

// PostgreSQL runs transactions concurrently under READ COMMITTED, so the
// governing rows must be locked.
func TestRowLockEmittedByPostgres(t *testing.T) {
	update, share := lockStatements(t, postgres.New(postgres.Config{DSN: "host=unused"}))
	Assert(t, strings.HasSuffix(update, "FOR UPDATE"), "row lock clause missing: %s", update)
	Assert(t, strings.HasSuffix(share, "FOR SHARE"), "log read lock clause missing: %s", share)
}

// lockStatements captures the SQL of lockPull and of the log read lock.
func lockStatements(t *testing.T, dialector gorm.Dialector) (string, string) {
	orm, err := gorm.Open(dialector, &gorm.Config{DryRun: true, DisableAutomaticPing: true, Logger: logger.Default.LogMode(logger.Silent)})
	Ok(t, err)
	var statement string
	Ok(t, orm.Callback().Query().After("gorm:query").Register("capture", func(tx *gorm.DB) { statement = tx.Statement.SQL.String() }))
	_, err = lockPull(orm, "pull")
	Ok(t, err)
	update := statement
	var row logStream
	Ok(t, orm.Clauses(logReadLock).Where("job_id = ?", "job").Take(&row).Error)
	Assert(t, update != "" && statement != update, "no query captured")
	return update, statement
}

func TestOwnerlessCleanupWaitsForCleanupClaim(t *testing.T) {
	ForEachDatabase(t, func(t *testing.T, d TestDatabase) {
		s := d.Store(t)
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
	})
}
